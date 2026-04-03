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

package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/kubeswarm/kubeswarm/pkg/agent/config"
	"github.com/kubeswarm/kubeswarm/pkg/agent/memory"
	"github.com/kubeswarm/kubeswarm/pkg/agent/providers"
)

// LoopMemoryHook reads from and writes to a VectorStore within the tool-use loop.
//
// Before each tool call:
//   - Retrieve top-K similar prior findings by embedding the tool name + args.
//   - Prepend a <swarm:prior-findings> block to the result returned to the model.
//
// After each tool call:
//   - Optionally summarise the result (if provider implements Completer and
//     StoreSummaryTokens > 0), then store the embedding in the VectorStore.
//
// Embeddings use a dedicated provider when cfg.EmbeddingModel is set (injected
// from SwarmMemory.spec.embedding by the operator). This allows the embedding
// model to differ from the task LLM - e.g. nomic-embed-text on Ollama while
// the task runs qwen2.5:7b. Falls back to the task provider's Embed() when unset.
//
// All errors are logged and fail-open: the task always continues.
type LoopMemoryHook struct {
	cfg      *config.LoopMemoryConfig
	store    memory.VectorStore
	provider providers.LLMProvider // task LLM provider (used for summarisation)
	embedder providers.LLMProvider // embedding provider; may equal provider
	taskID   string
	stepIdx  int // monotonic counter for generating unique store IDs
}

// newMemoryHook creates a LoopMemoryHook. Returns nil when memory policy is
// not configured or the VectorStore URL is empty.
func newMemoryHook(cfg *config.LoopMemoryConfig, vectorStoreURL string, taskID string, p providers.LLMProvider) *LoopMemoryHook {
	if cfg == nil || (!cfg.Store && !cfg.Retrieve) {
		return nil
	}
	if vectorStoreURL == "" {
		return nil
	}
	vs, err := memory.NewVectorStore(vectorStoreURL)
	if err != nil {
		log.Printf("loop-memory: failed to open vector store %q: %v (skipping memory hook)", vectorStoreURL, err)
		return nil
	}

	// Use a dedicated embedding provider when configured; fall back to task provider.
	embedder := p
	if cfg.EmbeddingProvider != "" && cfg.EmbeddingModel != "" {
		if ep, err := providers.New(cfg.EmbeddingProvider); err == nil {
			embedder = ep
		} else {
			log.Printf("loop-memory: embedding provider %q unavailable: %v (falling back to task provider)", cfg.EmbeddingProvider, err)
		}
	}

	return &LoopMemoryHook{
		cfg:      cfg,
		store:    vs,
		provider: p,
		embedder: embedder,
		taskID:   taskID,
	}
}

// BeforeCall retrieves similar prior findings for the given tool call.
// Returns a formatted prior-findings block, or "" if nothing was found or
// retrieval is disabled.
func (h *LoopMemoryHook) BeforeCall(ctx context.Context, toolName string, input json.RawMessage) string {
	if h == nil || !h.cfg.Retrieve {
		return ""
	}
	queryText := buildQueryText(toolName, input)
	vec, err := h.embedder.Embed(ctx, queryText)
	if err != nil {
		// Fail-open: embeddings unavailable (e.g. Anthropic provider returns ErrEmbeddingNotSupported).
		return ""
	}

	topK := h.cfg.TopK
	if topK <= 0 {
		topK = 3
	}
	results, err := h.store.Query(ctx, vec, topK)
	if err != nil {
		log.Printf("loop-memory: query failed: %v", err)
		return ""
	}

	minSim := h.cfg.MinSimilarity
	if minSim <= 0 {
		minSim = 0.70
	}
	var kept []string
	for _, r := range results {
		if float64(r.Score) < minSim {
			continue
		}
		if text, ok := r.Payload["text"].(string); ok && text != "" {
			kept = append(kept, text)
		}
	}
	if len(kept) == 0 {
		return ""
	}
	return fmt.Sprintf("<swarm:prior-findings>\n%s\n</swarm:prior-findings>",
		strings.Join(kept, "\n---\n"))
}

// AfterCall stores the tool result in the vector store after execution.
// Skipped when Store is false. Fail-open on all errors.
func (h *LoopMemoryHook) AfterCall(ctx context.Context, toolName string, input json.RawMessage, result string) {
	if h == nil || !h.cfg.Store {
		return
	}
	h.stepIdx++
	storeCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	text := h.summarise(storeCtx, toolName, result)
	vec, err := h.embedder.Embed(storeCtx, text)
	if err != nil {
		// Fail-open: embedding not supported by this provider.
		return
	}

	id := fmt.Sprintf("%s-%d", h.taskID, h.stepIdx)
	payload := map[string]any{
		"text":     text,
		"tool":     toolName,
		"task_id":  h.taskID,
		"step_idx": h.stepIdx,
	}
	if err := h.store.Upsert(storeCtx, id, vec, payload); err != nil {
		log.Printf("loop-memory: upsert failed for %s: %v", id, err)
	}
}

// Close releases the vector store connection.
func (h *LoopMemoryHook) Close() {
	if h == nil {
		return
	}
	_ = h.store.Close()
}

// summarise returns a short text for embedding. When StoreSummaryTokens > 0
// and the provider implements Completer, a cheap summarisation call is made.
// Falls back to truncation when Completer is unavailable or the call fails.
func (h *LoopMemoryHook) summarise(ctx context.Context, toolName, result string) string {
	const summaryPrompt = "Summarise the following tool result in a few sentences, preserving key findings."

	summaryTokens := max(h.cfg.SummaryTokens,
		// use truncation path
		0)

	if summaryTokens > 0 {
		completer, ok := h.provider.(providers.Completer)
		if ok {
			userMsg := fmt.Sprintf("Tool: %s\n\nResult:\n%s", toolName, result)
			summary, err := completer.Complete(ctx, defaultCompressionModel, summaryPrompt, userMsg, summaryTokens)
			if err == nil && summary != "" {
				return summary
			}
		}
	}

	// Truncation fallback.
	maxTokens := h.cfg.MaxTokens
	if maxTokens <= 0 {
		maxTokens = 1024
	}
	maxChars := maxTokens * 4
	if len(result) > maxChars {
		result = result[:maxChars]
	}
	return result
}

// buildQueryText forms a short query string combining the tool name and args
// for use as the embedding input during retrieval.
func buildQueryText(toolName string, input json.RawMessage) string {
	return fmt.Sprintf("tool:%s args:%s", toolName, string(input))
}

// injectPriorFindings prepends a prior-findings block to a tool result.
// Returns result unchanged when findings is empty.
func injectPriorFindings(findings, result string) string {
	if findings == "" {
		return result
	}
	return findings + "\n\n" + result
}
