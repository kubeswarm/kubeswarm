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

// Package anthropic implements LLMProvider for the Anthropic Claude API.
// Importing this package (even with a blank import) registers the "anthropic"
// provider in the global registry.
package anthropic

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	anthropicsdk "github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	oteltrace "go.opentelemetry.io/otel/trace"

	"github.com/kubeswarm/kubeswarm/pkg/agent/config"
	"github.com/kubeswarm/kubeswarm/pkg/agent/mcp"
	"github.com/kubeswarm/kubeswarm/pkg/agent/providers"
	"github.com/kubeswarm/kubeswarm/pkg/agent/queue"
	"github.com/kubeswarm/kubeswarm/pkg/observability"
)

func init() {
	providers.Register("anthropic", func() providers.LLMProvider { return &Provider{} })
}

// Provider implements providers.LLMProvider using the Anthropic Claude API.
type Provider struct{}

// RunTask executes a task through the Anthropic agentic tool-use loop.
// It keeps calling the API until the model stops requesting tool use.
// Token usage is accumulated across all API calls and returned with the result.
// If chunkFn is non-nil, every turn is streamed and text tokens are forwarded to chunkFn as they arrive.
func (p *Provider) RunTask(
	ctx context.Context,
	cfg *config.Config,
	task queue.Task,
	tools []mcp.Tool,
	callTool func(context.Context, string, json.RawMessage) (string, error),
	chunkFn func(string),
) (string, queue.TokenUsage, error) {
	apiKey := os.Getenv("ANTHROPIC_API_KEY")
	if apiKey == "" {
		return "", queue.TokenUsage{}, fmt.Errorf("ANTHROPIC_API_KEY is not set; required for the anthropic provider")
	}
	client := anthropicsdk.NewClient(option.WithAPIKey(apiKey))

	anthropicTools := toAnthropicTools(tools)

	messages := []anthropicsdk.MessageParam{
		anthropicsdk.NewUserMessage(anthropicsdk.NewTextBlock(task.Prompt)),
	}

	var usage queue.TokenUsage

	for {
		params := anthropicsdk.MessageNewParams{
			Model:     cfg.Model,
			MaxTokens: int64(cfg.MaxTokensPerCall),
			System: []anthropicsdk.TextBlockParam{
				{Text: cfg.SystemPrompt},
			},
			Messages: messages,
		}
		if len(anthropicTools) > 0 {
			params.Tools = anthropicTools
		}

		turnCtx, llmSpan := observability.Tracer("kubeswarm-anthropic").Start(ctx, "kubeswarm.llm.call",
			oteltrace.WithAttributes(
				attribute.String("llm.provider", "anthropic"),
				attribute.String("llm.model", cfg.Model),
			),
		)

		if chunkFn != nil {
			text, stopReason, content, turnUsage, err := p.runStreamingTurn(turnCtx, client, params, chunkFn)
			llmSpan.SetAttributes(
				attribute.Int64("llm.input_tokens", turnUsage.InputTokens),
				attribute.Int64("llm.output_tokens", turnUsage.OutputTokens),
			)
			if err != nil {
				err = wrapAPIError(err)
				llmSpan.RecordError(err)
				llmSpan.SetStatus(codes.Error, err.Error())
				llmSpan.End()
				return "", usage, err
			}
			llmSpan.End()
			usage.InputTokens += turnUsage.InputTokens
			usage.OutputTokens += turnUsage.OutputTokens
			messages = append(messages, streamingAssistantMessage(text, content))
			if stopReason != string(anthropicsdk.StopReasonToolUse) || len(content) == 0 {
				return text, usage, nil
			}
			toolResults := executeStreamingTools(ctx, content, callTool)
			if len(toolResults) == 0 {
				return text, usage, nil
			}
			messages = append(messages, anthropicsdk.NewUserMessage(toolResults...))
			continue
		}

		resp, err := client.Messages.New(turnCtx, params)
		if err != nil {
			err = wrapAPIError(err)
			llmSpan.RecordError(err)
			llmSpan.SetStatus(codes.Error, err.Error())
			llmSpan.End()
			return "", usage, fmt.Errorf("anthropic API error: %w", err)
		}
		llmSpan.SetAttributes(
			attribute.Int64("llm.input_tokens", resp.Usage.InputTokens),
			attribute.Int64("llm.output_tokens", resp.Usage.OutputTokens),
		)
		llmSpan.End()

		usage.InputTokens += resp.Usage.InputTokens
		usage.OutputTokens += resp.Usage.OutputTokens
		messages = append(messages, assistantMessage(resp.Content))

		if resp.StopReason == anthropicsdk.StopReasonEndTurn {
			return extractText(resp.Content), usage, nil
		}

		toolResults := executeTools(ctx, resp.Content, callTool)
		if len(toolResults) == 0 {
			return extractText(resp.Content), usage, nil
		}
		messages = append(messages, anthropicsdk.NewUserMessage(toolResults...))
	}
}

const blockTypeToolUse = "tool_use"

// streamingToolUse tracks a tool_use block being assembled from streaming events.
type streamingToolUse struct {
	id    string
	name  string
	input strings.Builder
}

// runStreamingTurn calls the Anthropic streaming API, forwarding text deltas to chunkFn.
// Returns the accumulated text, stop_reason, any tool-use blocks, token usage, and error.
func (p *Provider) runStreamingTurn(
	ctx context.Context,
	client anthropicsdk.Client,
	params anthropicsdk.MessageNewParams,
	chunkFn func(string),
) (string, string, []streamingToolUse, queue.TokenUsage, error) {
	stream := client.Messages.NewStreaming(ctx, params)
	defer func() { _ = stream.Close() }()

	var textSB strings.Builder
	var stopReason string
	var usage queue.TokenUsage

	// blockIndex → streamingToolUse for in-flight tool_use blocks.
	toolBlocks := map[int64]*streamingToolUse{}
	var currentIndex int64

	for stream.Next() {
		event := stream.Current()
		switch event.Type {
		case "message_start":
			v := event.AsMessageStart()
			usage.InputTokens += v.Message.Usage.InputTokens
			usage.OutputTokens += v.Message.Usage.OutputTokens
		case "message_delta":
			v := event.AsMessageDelta()
			usage.OutputTokens += v.Usage.OutputTokens
			stopReason = string(v.Delta.StopReason)
		case "content_block_start":
			v := event.AsContentBlockStart()
			currentIndex = v.Index
			if v.ContentBlock.Type == blockTypeToolUse {
				toolBlocks[currentIndex] = &streamingToolUse{
					id:   v.ContentBlock.ID,
					name: v.ContentBlock.Name,
				}
			}
		case "content_block_delta":
			v := event.AsContentBlockDelta()
			switch delta := v.Delta.AsAny().(type) {
			case anthropicsdk.TextDelta:
				chunkFn(delta.Text)
				textSB.WriteString(delta.Text)
			case anthropicsdk.InputJSONDelta:
				if tb, ok := toolBlocks[currentIndex]; ok {
					tb.input.WriteString(delta.PartialJSON)
				}
			}
		}
	}
	if err := stream.Err(); err != nil {
		return "", "", nil, usage, wrapAPIError(fmt.Errorf("anthropic streaming error: %w", err))
	}

	var tools []streamingToolUse
	for _, tb := range toolBlocks {
		tools = append(tools, *tb)
	}
	return textSB.String(), stopReason, tools, usage, nil
}

// streamingAssistantMessage builds an assistant MessageParam from streaming turn output.
func streamingAssistantMessage(text string, tools []streamingToolUse) anthropicsdk.MessageParam {
	var blocks []anthropicsdk.ContentBlockParamUnion
	if text != "" {
		blocks = append(blocks, anthropicsdk.NewTextBlock(text))
	}
	for _, t := range tools {
		blocks = append(blocks, anthropicsdk.NewToolUseBlock(t.id, json.RawMessage(t.input.String()), t.name))
	}
	return anthropicsdk.NewAssistantMessage(blocks...)
}

// executeStreamingTools runs tool calls assembled from a streaming turn.
func executeStreamingTools(
	ctx context.Context,
	tools []streamingToolUse,
	callTool func(context.Context, string, json.RawMessage) (string, error),
) []anthropicsdk.ContentBlockParamUnion {
	var results []anthropicsdk.ContentBlockParamUnion
	for _, t := range tools {
		output, err := callTool(ctx, t.name, json.RawMessage(t.input.String()))
		if err != nil {
			results = append(results, anthropicsdk.NewToolResultBlock(t.id, err.Error(), true))
			continue
		}
		results = append(results, anthropicsdk.NewToolResultBlock(t.id, output, false))
	}
	return results
}

// toAnthropicTools converts generic mcp.Tools into the Anthropic SDK format.
// ToolInputSchemaParam expects Properties to hold just the properties map and
// Required to hold the required field names — not the full schema blob.
func toAnthropicTools(tools []mcp.Tool) []anthropicsdk.ToolUnionParam {
	params := make([]anthropicsdk.ToolUnionParam, 0, len(tools))
	for _, t := range tools {
		schema := parseToolSchema(t.InputSchema)
		tool := anthropicsdk.ToolParam{
			Name:        t.Name,
			Description: anthropicsdk.String(t.Description),
			InputSchema: schema,
		}
		params = append(params, anthropicsdk.ToolUnionParam{OfTool: &tool})
	}
	return params
}

// parseToolSchema extracts Properties and Required from a full JSON Schema object
// into the form that anthropicsdk.ToolInputSchemaParam expects.
func parseToolSchema(raw json.RawMessage) anthropicsdk.ToolInputSchemaParam {
	if len(raw) == 0 {
		return anthropicsdk.ToolInputSchemaParam{}
	}
	var full struct {
		Properties json.RawMessage `json:"properties"`
		Required   []string        `json:"required"`
	}
	if err := json.Unmarshal(raw, &full); err != nil || len(full.Properties) == 0 {
		return anthropicsdk.ToolInputSchemaParam{Properties: raw}
	}
	var props any
	if err := json.Unmarshal(full.Properties, &props); err != nil {
		return anthropicsdk.ToolInputSchemaParam{Properties: raw}
	}
	return anthropicsdk.ToolInputSchemaParam{
		Properties: props,
		Required:   full.Required,
	}
}

// executeTools runs each tool_use block via callTool and returns result blocks.
func executeTools(
	ctx context.Context,
	content []anthropicsdk.ContentBlockUnion,
	callTool func(context.Context, string, json.RawMessage) (string, error),
) []anthropicsdk.ContentBlockParamUnion {
	var results []anthropicsdk.ContentBlockParamUnion
	for _, block := range content {
		if block.Type != blockTypeToolUse {
			continue
		}
		output, err := callTool(ctx, block.Name, block.Input)
		if err != nil {
			results = append(results, anthropicsdk.NewToolResultBlock(block.ID, err.Error(), true))
			continue
		}
		results = append(results, anthropicsdk.NewToolResultBlock(block.ID, output, false))
	}
	return results
}

// assistantMessage converts a response content slice into a MessageParam.
func assistantMessage(content []anthropicsdk.ContentBlockUnion) anthropicsdk.MessageParam {
	params := make([]anthropicsdk.ContentBlockParamUnion, 0, len(content))
	for _, block := range content {
		switch block.Type {
		case "text":
			params = append(params, anthropicsdk.NewTextBlock(block.Text))
		case blockTypeToolUse:
			params = append(params, anthropicsdk.NewToolUseBlock(block.ID, block.Input, block.Name))
		}
	}
	return anthropicsdk.NewAssistantMessage(params...)
}

// Complete implements providers.Completer for single-turn non-streaming calls.
// It is used by callers that need a simple system-prompt + user-message → text
// response without the full agentic tool-use loop (e.g. the optimizer meta-agent).
func (p *Provider) Complete(ctx context.Context, model, systemPrompt, userMsg string, maxTokens int) (string, error) {
	apiKey := os.Getenv("ANTHROPIC_API_KEY")
	if apiKey == "" {
		return "", fmt.Errorf("ANTHROPIC_API_KEY is not set; required for the anthropic provider")
	}
	if maxTokens <= 0 {
		maxTokens = 4000
	}
	client := anthropicsdk.NewClient(option.WithAPIKey(apiKey))
	resp, err := client.Messages.New(ctx, anthropicsdk.MessageNewParams{
		Model:     model,
		MaxTokens: int64(maxTokens),
		System:    []anthropicsdk.TextBlockParam{{Text: systemPrompt}},
		Messages:  []anthropicsdk.MessageParam{anthropicsdk.NewUserMessage(anthropicsdk.NewTextBlock(userMsg))},
	})
	if err != nil {
		return "", err
	}
	if len(resp.Content) == 0 {
		return "", fmt.Errorf("provider returned empty response")
	}
	return resp.Content[0].Text, nil
}

// wrapAPIError inspects an Anthropic SDK error and wraps it with
// queue.PermanentFailure when the HTTP status code indicates a condition that
// cannot be resolved by retrying (billing exhausted, bad API key, permissions).
// Transient errors (429 rate-limit, 500/529 server overload) are returned as-is
// so the retry budget is used normally.
func wrapAPIError(err error) error {
	var apiErr *anthropicsdk.Error
	if errors.As(err, &apiErr) {
		switch apiErr.StatusCode {
		case 400, 401, 403, 404:
			return fmt.Errorf("%s: %w", queue.PermanentFailure, err)
		}
	}
	return err
}

// extractText returns the text of the first text block in content.
func extractText(content []anthropicsdk.ContentBlockUnion) string {
	for _, block := range content {
		if block.Type == "text" {
			return block.Text
		}
	}
	return ""
}

// Embed implements providers.LLMProvider.
// Anthropic does not expose an embeddings API; always returns ErrEmbeddingNotSupported.
func (p *Provider) Embed(_ context.Context, _ string) ([]float32, error) {
	return nil, providers.ErrEmbeddingNotSupported
}
