/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// Package gemini implements LLMProvider for Google Gemini models.
// Importing this package (even with a blank import) registers the "gemini"
// provider in the global registry.
//
// The provider reads GEMINI_API_KEY from the environment. It uses the
// Google AI Gemini API (not Vertex AI). To target Vertex AI set both
// GEMINI_PROJECT and GEMINI_LOCATION env vars instead of GEMINI_API_KEY.
package gemini

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	oteltrace "go.opentelemetry.io/otel/trace"
	"google.golang.org/genai"

	"github.com/kubeswarm/kubeswarm/pkg/agent/config"
	"github.com/kubeswarm/kubeswarm/pkg/agent/mcp"
	"github.com/kubeswarm/kubeswarm/pkg/agent/providers"
	"github.com/kubeswarm/kubeswarm/pkg/agent/queue"
	"github.com/kubeswarm/kubeswarm/pkg/observability"
)

func init() {
	providers.Register("gemini", func() providers.LLMProvider { return &Provider{} })
}

// Provider implements providers.LLMProvider using the Google Gemini API.
type Provider struct{}

// RunTask executes a task through the Gemini agentic tool-use loop.
// It keeps calling the API until the model stops requesting function calls.
// Token usage is accumulated across all API calls and returned with the result.
// If chunkFn is non-nil, the final text turn is streamed token-by-token.
func (p *Provider) RunTask(
	ctx context.Context,
	cfg *config.Config,
	task queue.Task,
	tools []mcp.Tool,
	callTool func(context.Context, string, json.RawMessage) (string, error),
	chunkFn func(string),
) (string, queue.TokenUsage, error) {
	client, err := newClient(ctx)
	if err != nil {
		return "", queue.TokenUsage{}, err
	}

	geminiTools := toGeminiTools(tools)

	genCfg := &genai.GenerateContentConfig{
		SystemInstruction: &genai.Content{
			Parts: []*genai.Part{{Text: cfg.SystemPrompt}},
		},
	}
	if cfg.MaxTokensPerCall > 0 {
		genCfg.MaxOutputTokens = int32(cfg.MaxTokensPerCall) //nolint:gosec
	}
	if len(geminiTools) > 0 {
		genCfg.Tools = geminiTools
	}

	// Conversation history: starts with the user prompt.
	contents := []*genai.Content{
		{Role: "user", Parts: []*genai.Part{{Text: task.Prompt}}},
	}

	var usage queue.TokenUsage

	for {
		turnCtx, llmSpan := observability.Tracer("kubeswarm-gemini").Start(ctx, "kubeswarm.llm.call",
			oteltrace.WithAttributes(
				attribute.String("llm.provider", "gemini"),
				attribute.String("llm.model", cfg.Model),
			),
		)

		// Use streaming for the final text turn when chunkFn is set and no tools are active.
		if chunkFn != nil && len(geminiTools) == 0 {
			text, turnUsage, streamErr := p.runStreamingTurn(turnCtx, client, cfg.Model, contents, genCfg, chunkFn)
			llmSpan.SetAttributes(
				attribute.Int64("llm.input_tokens", turnUsage.InputTokens),
				attribute.Int64("llm.output_tokens", turnUsage.OutputTokens),
			)
			if streamErr != nil {
				llmSpan.RecordError(streamErr)
				llmSpan.SetStatus(codes.Error, streamErr.Error())
				llmSpan.End()
				return "", usage, streamErr
			}
			llmSpan.End()
			usage.InputTokens += turnUsage.InputTokens
			usage.OutputTokens += turnUsage.OutputTokens
			return text, usage, nil
		}

		resp, genErr := client.Models.GenerateContent(turnCtx, cfg.Model, contents, genCfg)
		if genErr != nil {
			llmSpan.RecordError(genErr)
			llmSpan.SetStatus(codes.Error, genErr.Error())
			llmSpan.End()
			return "", usage, fmt.Errorf("gemini API error: %w", genErr)
		}

		var inputTokens, outputTokens int64
		if resp.UsageMetadata != nil {
			inputTokens = int64(resp.UsageMetadata.PromptTokenCount)
			outputTokens = int64(resp.UsageMetadata.CandidatesTokenCount)
		}
		llmSpan.SetAttributes(
			attribute.Int64("llm.input_tokens", inputTokens),
			attribute.Int64("llm.output_tokens", outputTokens),
		)
		llmSpan.End()

		usage.InputTokens += inputTokens
		usage.OutputTokens += outputTokens

		if len(resp.Candidates) == 0 || resp.Candidates[0].Content == nil {
			return "", usage, fmt.Errorf("gemini: empty response")
		}

		candidate := resp.Candidates[0]
		// Append assistant turn to history.
		contents = append(contents, candidate.Content)

		// Collect any function calls from the parts.
		var functionCalls []*genai.FunctionCall
		for _, part := range candidate.Content.Parts {
			if part.FunctionCall != nil {
				functionCalls = append(functionCalls, part.FunctionCall)
			}
		}

		log.Printf("gemini turn: finish_reason=%q function_calls=%d", candidate.FinishReason, len(functionCalls))

		if len(functionCalls) == 0 {
			// No tool calls — extract text and return.
			text := extractText(candidate.Content.Parts)
			if chunkFn != nil {
				chunkFn(text)
			}
			return text, usage, nil
		}

		// Execute all function calls and append results as a single content turn.
		responseParts := make([]*genai.Part, 0, len(functionCalls))
		for _, fc := range functionCalls {
			argsJSON, marshalErr := json.Marshal(fc.Args)
			if marshalErr != nil {
				argsJSON = []byte("{}")
			}
			output, execErr := callTool(ctx, fc.Name, json.RawMessage(argsJSON))
			response := map[string]any{"output": output}
			if execErr != nil {
				response = map[string]any{"error": execErr.Error()}
			}
			responseParts = append(responseParts, genai.NewPartFromFunctionResponse(fc.Name, response))
		}
		contents = append(contents, &genai.Content{
			Role:  "tool",
			Parts: responseParts,
		})
	}
}

// newClient creates a Gemini client. Supports Gemini API (GEMINI_API_KEY) and
// Vertex AI (GEMINI_PROJECT + GEMINI_LOCATION).
func newClient(ctx context.Context) (*genai.Client, error) {
	project := os.Getenv("GEMINI_PROJECT")
	location := os.Getenv("GEMINI_LOCATION")

	if project != "" && location != "" {
		return genai.NewClient(ctx, &genai.ClientConfig{
			Project:  project,
			Location: location,
			Backend:  genai.BackendVertexAI,
		})
	}

	apiKey := os.Getenv("GEMINI_API_KEY")
	if apiKey == "" {
		return nil, fmt.Errorf("GEMINI_API_KEY is not set; required for the gemini provider (or set GEMINI_PROJECT + GEMINI_LOCATION for Vertex AI)")
	}
	return genai.NewClient(ctx, &genai.ClientConfig{
		APIKey:  apiKey,
		Backend: genai.BackendGeminiAPI,
	})
}

// toGeminiTools converts generic mcp.Tool slice into Gemini Tool declarations.
func toGeminiTools(tools []mcp.Tool) []*genai.Tool {
	if len(tools) == 0 {
		return nil
	}
	decls := make([]*genai.FunctionDeclaration, 0, len(tools))
	for _, t := range tools {
		fd := &genai.FunctionDeclaration{
			Name:        t.Name,
			Description: t.Description,
		}
		if len(t.InputSchema) > 0 {
			fd.ParametersJsonSchema = rawToMap(t.InputSchema)
		}
		decls = append(decls, fd)
	}
	return []*genai.Tool{{FunctionDeclarations: decls}}
}

// rawToMap converts a json.RawMessage into map[string]any.
func rawToMap(raw json.RawMessage) map[string]any {
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return map[string]any{}
	}
	return m
}

// extractText concatenates all text parts from a content's parts.
func extractText(parts []*genai.Part) string {
	var sb strings.Builder
	for _, p := range parts {
		sb.WriteString(p.Text)
	}
	return sb.String()
}

// runStreamingTurn calls the Gemini streaming API and forwards each text delta to chunkFn.
func (p *Provider) runStreamingTurn(
	ctx context.Context,
	client *genai.Client,
	model string,
	contents []*genai.Content,
	cfg *genai.GenerateContentConfig,
	chunkFn func(string),
) (string, queue.TokenUsage, error) {
	var sb strings.Builder
	var usage queue.TokenUsage

	for resp, err := range client.Models.GenerateContentStream(ctx, model, contents, cfg) {
		if err != nil {
			return "", usage, fmt.Errorf("gemini streaming error: %w", err)
		}
		if resp.UsageMetadata != nil {
			usage.InputTokens = int64(resp.UsageMetadata.PromptTokenCount)
			usage.OutputTokens = int64(resp.UsageMetadata.CandidatesTokenCount)
		}
		if len(resp.Candidates) > 0 && resp.Candidates[0].Content != nil {
			for _, part := range resp.Candidates[0].Content.Parts {
				if part.Text != "" {
					chunkFn(part.Text)
					sb.WriteString(part.Text)
				}
			}
		}
	}
	return sb.String(), usage, nil
}

// Embed implements providers.LLMProvider.
// Gemini embedding support is not yet wired; always returns ErrEmbeddingNotSupported.
func (p *Provider) Embed(_ context.Context, _ string) ([]float32, error) {
	return nil, providers.ErrEmbeddingNotSupported
}
