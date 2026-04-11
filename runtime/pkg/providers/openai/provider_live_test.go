package openai

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/kubeswarm/kubeswarm/pkg/agent/config"
	"github.com/kubeswarm/kubeswarm/pkg/agent/mcp"
	"github.com/kubeswarm/kubeswarm/pkg/agent/queue"
)

func TestLiveQwen35_BasicCall(t *testing.T) {
	if os.Getenv("LIVE_TEST") != "1" {
		t.Skip("set LIVE_TEST=1 to run against local Ollama")
	}
	// Ollama doesn't check API key but the provider requires it set.
	t.Setenv("OPENAI_API_KEY", "ollama")
	t.Setenv("OPENAI_BASE_URL", "http://localhost:11434/v1")

	p := &Provider{}
	cfg := &config.Config{
		Model:                 "qwen3:8b",
		SystemPrompt:          "You are a helpful assistant. Reply concisely.",
		MaxTokensPerCall:      200,
		ReasoningMode:         "Auto",
		ReasoningBudgetTokens: 4096,
	}
	task := queue.Task{
		ID:     "test-1",
		Prompt: "What is 2+2? Reply in one word.",
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	result, usage, err := p.RunTask(ctx, cfg, task, []mcp.Tool{}, func(_ context.Context, _ string, _ json.RawMessage) (string, error) {
		return "", nil
	}, nil)

	if err != nil {
		t.Fatalf("RunTask error: %v", err)
	}
	t.Logf("Result: %q", result)
	t.Logf("Usage: input=%d output=%d thinking=%d", usage.InputTokens, usage.OutputTokens, usage.ThinkingTokens)

	if result == "" {
		t.Error("expected non-empty result")
	}
	if usage.InputTokens == 0 {
		t.Error("expected non-zero input tokens")
	}
	if usage.OutputTokens == 0 {
		t.Error("expected non-zero output tokens")
	}
	t.Logf("ThinkingTokens=%d", usage.ThinkingTokens)
	if usage.ThinkingTokens > 0 {
		t.Logf("Reasoning token estimation working (chars/4 fallback for Ollama)")
	}
}
