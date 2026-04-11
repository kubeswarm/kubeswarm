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
	"fmt"
	"strings"
	"time"

	"github.com/kubeswarm/kubeswarm/pkg/agent/config"
	"github.com/kubeswarm/kubeswarm/pkg/agent/providers"
)

const (
	defaultCompressionModel  = "claude-haiku-4-5-20251001"
	defaultCompressionPrompt = "You are a context compression assistant. " +
		"Summarize the following tool results into a concise digest that preserves " +
		"key findings, data points, and decisions. Output only the summary text."
)

// LoopCompressor accumulates tool-result token counts and compresses them
// when the running total approaches the model's context window.
//
// Compression is performed on accumulated tool results only (scope=tool-results-only).
// The compressor calls the provider's Completer interface with a cheap fast model;
// if the provider does not implement Completer the compression call is skipped (fail-open).
//
// Additionally, when the provider implements ConversationCompressor, the compressor
// registers a hook so the provider can compress its internal conversation history too.
type LoopCompressor struct {
	cfg       *config.LoopCompressionConfig
	model     string // agent's primary model (for context window resolution)
	compModel string // model used for the compression call
	window    int    // resolved context window in tokens; 0 = disabled

	provider providers.LLMProvider

	// accumulated tool results pending compression.
	accum       strings.Builder
	accumTokens int
}

// newCompressor creates a LoopCompressor. Returns nil when compression is disabled.
func newCompressor(ctx context.Context, cfg *config.LoopCompressionConfig, model string, p providers.LLMProvider) *LoopCompressor {
	if cfg == nil {
		return nil
	}
	compModel := cfg.Model
	if compModel == "" {
		compModel = defaultCompressionModel
	}
	window := resolveContextWindow(ctx, model, cfg.ContextWindow, p)
	if window == 0 {
		// Unknown context window - compression cannot trigger safely; skip.
		return nil
	}
	c := &LoopCompressor{
		cfg:       cfg,
		model:     model,
		compModel: compModel,
		window:    window,
		provider:  p,
	}
	// If the provider supports conversation-level compression, register the hook.
	if cc, ok := p.(providers.ConversationCompressor); ok {
		cc.RegisterCompressionHook(c.compressionHook)
	}
	return c
}

// Track records a tool result for the running token budget.
func (c *LoopCompressor) Track(result string) {
	if c == nil {
		return
	}
	c.accum.WriteString(result)
	c.accum.WriteByte('\n')
	c.accumTokens += estimateTokens(result)
}

// NeedsCompression returns true when accumulated tool-result tokens have crossed
// the configured fraction of the context window.
func (c *LoopCompressor) NeedsCompression() bool {
	if c == nil || c.window == 0 {
		return false
	}
	return float64(c.accumTokens) >= float64(c.window)*c.cfg.ThresholdFloat()
}

// Compress calls the compression model on accumulated tool results and returns
// a summary. On timeout or provider error the call is skipped (fail-open) and
// ("", false) is returned so the task continues uninterrupted.
func (c *LoopCompressor) Compress(ctx context.Context) (summary string, ok bool) {
	if c == nil {
		return "", false
	}
	if c.accum.Len() == 0 {
		return "", false
	}

	timeout := time.Duration(c.cfg.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	compCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	prompt := c.cfg.Instructions
	if prompt == "" {
		prompt = defaultCompressionPrompt
	}

	completer, canComplete := c.provider.(providers.Completer)
	if !canComplete {
		// Fail-open: provider cannot compress. Return without error so the task continues.
		return "", false
	}
	text := c.accum.String()
	result, err := completer.Complete(compCtx, c.compModel, prompt, text, 1024)
	if err != nil {
		// Fail-open on timeout or any error.
		return "", false
	}

	// Reset accumulator after a successful compression.
	c.accum.Reset()
	c.accumTokens = 0
	return result, true
}

// compressionHook is registered with providers.ConversationCompressor to allow
// the provider to drive compression of its own conversation history.
func (c *LoopCompressor) compressionHook(ctx context.Context, tokenCount int) (string, bool) {
	if float64(tokenCount) < float64(c.window)*c.cfg.ThresholdFloat() {
		return "", false
	}
	return c.Compress(ctx)
}

// InjectSummary wraps a tool result with a prior-summary preamble when compression
// was performed. The summary is injected as a human-readable annotation so the LLM
// sees compressed history alongside the fresh tool result.
func injectSummary(summary, toolResult string) string {
	if summary == "" {
		return toolResult
	}
	return fmt.Sprintf("<swarm:compressed-history>\n%s\n</swarm:compressed-history>\n\n%s", summary, toolResult)
}
