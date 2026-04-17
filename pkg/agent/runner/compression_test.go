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
	"strings"
	"testing"

	"github.com/kubeswarm/kubeswarm/pkg/agent/config"
	"github.com/kubeswarm/kubeswarm/pkg/agent/mcp"
	"github.com/kubeswarm/kubeswarm/pkg/agent/providers"
	"github.com/kubeswarm/kubeswarm/pkg/agent/queue"
)

// completerProvider implements LLMProvider + Completer so the compressor can
// call Complete(). Records which model was used.
type completerProvider struct {
	completeCalls int
	completeReply string
	lastModel     string // model passed to Complete()
}

func (p *completerProvider) RunTask(
	_ context.Context,
	_ *config.Config,
	_ queue.Task,
	_ []mcp.Tool,
	_ func(context.Context, string, json.RawMessage) (string, error),
	_ func(string),
) (string, queue.TokenUsage, error) {
	return "", queue.TokenUsage{}, nil
}

func (p *completerProvider) Embed(_ context.Context, _ string) ([]float32, error) {
	return nil, providers.ErrEmbeddingNotSupported
}

func (p *completerProvider) Complete(_ context.Context, model, _, _ string, _ int) (string, error) {
	p.completeCalls++
	p.lastModel = model
	return p.completeReply, nil
}

// TestLoopCompressor_ThresholdTriggersCompression covers TEST-TRACKER.md item:
//
//	[x] Runtime: in-loop context compression at threshold
//
// Behaviour under test: the LoopCompressor accumulates tool-result tokens and
// triggers compression when the running total crosses the configured threshold
// percentage of the context window. Uses a deliberately small context window
// (40 tokens) so we don't depend on exact estimateTokens arithmetic.
func TestLoopCompressor_ThresholdTriggersCompression(t *testing.T) {
	prov := &completerProvider{completeReply: "compressed summary"}

	cfg := &config.LoopCompressionConfig{
		ThresholdPercent: 50,
		ContextWindow:    40, // 40 tokens; threshold at 20 tokens
	}

	c := newCompressor(context.Background(), cfg, "test-model", prov)
	if c == nil {
		t.Fatal("newCompressor returned nil with valid config")
	}

	// Track a tiny result - well under threshold.
	c.Track("hi")
	if c.NeedsCompression() {
		t.Error("NeedsCompression() = true with tiny input, threshold is 20 tokens")
	}

	// Track a large result - guaranteed to cross 20 tokens even with generous
	// token estimation. 200 chars is at least 50 tokens at 4 chars/token.
	c.Track(strings.Repeat("x", 200))
	if !c.NeedsCompression() {
		t.Fatal("NeedsCompression() = false after large input, expected threshold crossed")
	}

	// Compress should call the provider and return the summary.
	summary, ok := c.Compress(context.Background())
	if !ok {
		t.Fatal("Compress() returned ok=false")
	}
	if summary != "compressed summary" {
		t.Errorf("summary = %q, want %q", summary, "compressed summary")
	}
	if prov.completeCalls != 1 {
		t.Errorf("Complete() called %d times, want 1", prov.completeCalls)
	}

	// After compression, accumulator resets - should no longer need compression.
	if c.NeedsCompression() {
		t.Error("NeedsCompression() = true after Compress() should have reset accumulator")
	}
}

// TestLoopCompressor_BelowThresholdNeedsCompressionFalse verifies that when
// accumulated tokens stay below the threshold, NeedsCompression() returns false.
// The runner checks NeedsCompression() before calling Compress(), so this gate
// prevents unnecessary compression calls.
func TestLoopCompressor_BelowThresholdNeedsCompressionFalse(t *testing.T) {
	prov := &completerProvider{completeReply: "should not be called"}

	cfg := &config.LoopCompressionConfig{
		ThresholdPercent: 90,    // very high threshold
		ContextWindow:    10000, // large window - 90% = 9000 tokens
	}

	c := newCompressor(context.Background(), cfg, "test-model", prov)
	if c == nil {
		t.Fatal("newCompressor returned nil")
	}

	// Track a moderate result - well under 9000 tokens.
	c.Track(strings.Repeat("x", 400)) // ~100 tokens
	if c.NeedsCompression() {
		t.Error("NeedsCompression() = true at ~100 tokens, threshold is ~9000")
	}

	// Since NeedsCompression() returned false, the runner would not call Compress().
	// Verify the provider was never called.
	if prov.completeCalls != 0 {
		t.Errorf("Complete() called %d times before threshold crossed", prov.completeCalls)
	}
}

// TestLoopCompressor_NilConfig verifies that a nil compression config produces
// a nil compressor (disabled) and all methods are nil-safe.
func TestLoopCompressor_NilConfig(t *testing.T) {
	c := newCompressor(context.Background(), nil, "test-model", &completerProvider{})
	if c != nil {
		t.Error("expected nil compressor with nil config")
	}
	// Nil-safe methods should be no-ops.
	c.Track("anything")
	if c.NeedsCompression() {
		t.Error("nil compressor NeedsCompression() = true")
	}
	summary, ok := c.Compress(context.Background())
	if ok || summary != "" {
		t.Errorf("nil compressor Compress() = (%q, %v), want (\"\", false)", summary, ok)
	}
}

// TestLoopCompressor_ModelOverride covers TEST-TRACKER.md item:
//
//	[x] Runtime: loop compression model override
//
// Behaviour under test: when LoopCompressionConfig.Model is set, the compressor
// passes that model (not the agent's primary model or the default) to the
// provider's Complete method.
func TestLoopCompressor_ModelOverride(t *testing.T) {
	prov := &completerProvider{completeReply: "ok"}

	cfg := &config.LoopCompressionConfig{
		ThresholdPercent: 10,
		ContextWindow:    40,
		Model:            "custom-compression-model",
	}

	c := newCompressor(context.Background(), cfg, "primary-model", prov)
	if c == nil {
		t.Fatal("newCompressor returned nil")
	}

	// Push past threshold and compress.
	c.Track(strings.Repeat("x", 200))
	if !c.NeedsCompression() {
		t.Fatal("expected NeedsCompression() = true")
	}

	_, ok := c.Compress(context.Background())
	if !ok {
		t.Fatal("Compress() returned ok=false")
	}

	// Verify the custom model was passed to Complete, not the primary model.
	if prov.lastModel != "custom-compression-model" {
		t.Errorf("Complete() called with model %q, want %q", prov.lastModel, "custom-compression-model")
	}
}

// TestLoopCompressor_DefaultModelUsed verifies the default compression model
// is passed to Complete when no override is specified.
func TestLoopCompressor_DefaultModelUsed(t *testing.T) {
	prov := &completerProvider{completeReply: "ok"}

	cfg := &config.LoopCompressionConfig{
		ThresholdPercent: 10,
		ContextWindow:    40,
		// Model not set - should use default.
	}

	c := newCompressor(context.Background(), cfg, "primary-model", prov)
	if c == nil {
		t.Fatal("newCompressor returned nil")
	}

	c.Track(strings.Repeat("x", 200))
	c.Compress(context.Background())

	if prov.lastModel != defaultCompressionModel {
		t.Errorf("Complete() called with model %q, want default %q", prov.lastModel, defaultCompressionModel)
	}
}

// TestInjectSummary verifies the summary annotation format.
func TestInjectSummary(t *testing.T) {
	result := injectSummary("prior context", "tool output")
	if !strings.Contains(result, "<swarm:compressed-history>") {
		t.Error("missing compressed-history tag")
	}
	if !strings.Contains(result, "prior context") {
		t.Error("missing summary content")
	}
	if !strings.Contains(result, "tool output") {
		t.Error("missing tool result")
	}

	// Empty summary should pass through unchanged.
	if got := injectSummary("", "tool output"); got != "tool output" {
		t.Errorf("empty summary: got %q, want %q", got, "tool output")
	}
}
