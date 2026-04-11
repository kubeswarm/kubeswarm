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

package config_test

import (
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/kubeswarm/kubeswarm/pkg/agent/config"
)

func setEnv(t *testing.T, key, value string) {
	t.Helper()
	t.Setenv(key, value)
}

func requiredEnvs(t *testing.T) {
	t.Helper()
	setEnv(t, "AGENT_MODEL", "claude-sonnet-4-6")
	setEnv(t, "AGENT_SYSTEM_PROMPT", "You are a test agent.")
	setEnv(t, "TASK_QUEUE_URL", "localhost:6379")
}

func TestLoad_Defaults(t *testing.T) {
	requiredEnvs(t)

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.Model != "claude-sonnet-4-6" {
		t.Errorf("Model = %q, want %q", cfg.Model, "claude-sonnet-4-6")
	}
	if cfg.MaxTokensPerCall != 8000 {
		t.Errorf("MaxTokensPerCall = %d, want 8000", cfg.MaxTokensPerCall)
	}
	if cfg.TimeoutSeconds != 120 {
		t.Errorf("TimeoutSeconds = %d, want 120", cfg.TimeoutSeconds)
	}
	if cfg.MaxRetries != 3 {
		t.Errorf("MaxRetries = %d, want 3", cfg.MaxRetries)
	}
}

func TestLoad_OptionalOverrides(t *testing.T) {
	requiredEnvs(t)
	setEnv(t, "AGENT_MAX_TOKENS", "4000")
	setEnv(t, "AGENT_TIMEOUT_SECONDS", "60")
	setEnv(t, "AGENT_MAX_RETRIES", "5")
	setEnv(t, "AGENT_PROVIDER", "anthropic")
	setEnv(t, "AGENT_VALIDATOR_PROMPT", "Reply HEALTHY")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.MaxTokensPerCall != 4000 {
		t.Errorf("MaxTokensPerCall = %d, want 4000", cfg.MaxTokensPerCall)
	}
	if cfg.TimeoutSeconds != 60 {
		t.Errorf("TimeoutSeconds = %d, want 60", cfg.TimeoutSeconds)
	}
	if cfg.MaxRetries != 5 {
		t.Errorf("MaxRetries = %d, want 5", cfg.MaxRetries)
	}
	if cfg.Provider != "anthropic" {
		t.Errorf("Provider = %q, want %q", cfg.Provider, "anthropic")
	}
	if cfg.ValidatorPrompt != "Reply HEALTHY" {
		t.Errorf("ValidatorPrompt = %q, want %q", cfg.ValidatorPrompt, "Reply HEALTHY")
	}
}

func TestLoad_MCPServers(t *testing.T) {
	requiredEnvs(t)
	servers := []config.MCPServerConfig{
		{Name: "search", URL: "https://search.example.com/sse"},
		{Name: "browser", URL: "https://browser.example.com/sse"},
	}
	raw, _ := json.Marshal(servers)
	setEnv(t, "AGENT_MCP_SERVERS", string(raw))

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cfg.MCPServers) != 2 {
		t.Fatalf("MCPServers len = %d, want 2", len(cfg.MCPServers))
	}
	if cfg.MCPServers[0].Name != "search" {
		t.Errorf("MCPServers[0].Name = %q, want %q", cfg.MCPServers[0].Name, "search")
	}
}

func TestLoad_MissingRequired(t *testing.T) {
	_ = os.Unsetenv("AGENT_MODEL")
	_ = os.Unsetenv("AGENT_SYSTEM_PROMPT")
	_ = os.Unsetenv("AGENT_SYSTEM_PROMPT_PATH")
	_ = os.Unsetenv("TASK_QUEUE_URL")

	_, err := config.Load()
	if err == nil {
		t.Fatal("expected error for missing AGENT_MODEL, got nil")
	}
}

func TestLoad_InvalidMaxTokens(t *testing.T) {
	requiredEnvs(t)
	setEnv(t, "AGENT_MAX_TOKENS", "not-a-number")

	_, err := config.Load()
	if err == nil {
		t.Fatal("expected error for invalid AGENT_MAX_TOKENS, got nil")
	}
}

func TestLoad_InvalidTimeout(t *testing.T) {
	requiredEnvs(t)
	setEnv(t, "AGENT_TIMEOUT_SECONDS", "bad")

	_, err := config.Load()
	if err == nil {
		t.Fatal("expected error for invalid AGENT_TIMEOUT_SECONDS, got nil")
	}
}

func TestLoad_InvalidMCPServersJSON(t *testing.T) {
	requiredEnvs(t)
	setEnv(t, "AGENT_MCP_SERVERS", "not-json")

	_, err := config.Load()
	if err == nil {
		t.Fatal("expected error for invalid AGENT_MCP_SERVERS JSON, got nil")
	}
}

func TestTaskTimeout(t *testing.T) {
	cfg := &config.Config{TimeoutSeconds: 30}
	got := config.TaskTimeout(cfg)
	want := 30 * time.Second
	if got != want {
		t.Errorf("TaskTimeout = %v, want %v", got, want)
	}
}

func TestLoad_LoopPolicy(t *testing.T) {
	requiredEnvs(t)
	// JSON matches the CRD AgentLoopPolicy json tags (dedup, compression, memory).
	lp := `{"dedup":true,"compression":{"thresholdPercent":80,"preserveRecentTurns":3,"model":"claude-haiku-4-5-20251001","timeoutSeconds":30,"instructions":"Summarize concisely."},"memory":{"store":true,"retrieve":true,"topK":5,"minSimilarityPercent":75,"summaryTokens":128,"maxTokens":512}}`
	setEnv(t, "AGENT_LOOP_POLICY", lp)

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.LoopPolicy == nil {
		t.Fatal("LoopPolicy is nil")
	}
	if !cfg.LoopPolicy.Dedup {
		t.Error("LoopPolicy.Dedup = false, want true")
	}
	if cfg.LoopPolicy.Compression == nil {
		t.Fatal("Compression is nil")
	}
	if cfg.LoopPolicy.Compression.ThresholdPercent != 80 {
		t.Errorf("Compression.ThresholdPercent = %d, want 80", cfg.LoopPolicy.Compression.ThresholdPercent)
	}
	if cfg.LoopPolicy.Compression.PreserveRecentTurns != 3 {
		t.Errorf("Compression.PreserveRecentTurns = %d, want 3", cfg.LoopPolicy.Compression.PreserveRecentTurns)
	}
	if cfg.LoopPolicy.Compression.Model != "claude-haiku-4-5-20251001" {
		t.Errorf("Compression.Model = %q", cfg.LoopPolicy.Compression.Model)
	}
	if cfg.LoopPolicy.Compression.Instructions != "Summarize concisely." {
		t.Errorf("Compression.Instructions = %q", cfg.LoopPolicy.Compression.Instructions)
	}
	if cfg.LoopPolicy.Memory == nil {
		t.Fatal("Memory is nil")
	}
	if !cfg.LoopPolicy.Memory.Store {
		t.Error("Memory.Store = false, want true")
	}
	if cfg.LoopPolicy.Memory.TopK != 5 {
		t.Errorf("Memory.TopK = %d, want 5", cfg.LoopPolicy.Memory.TopK)
	}
	if cfg.LoopPolicy.Memory.MinSimilarityPercent != 75 {
		t.Errorf("Memory.MinSimilarityPercent = %d, want 75", cfg.LoopPolicy.Memory.MinSimilarityPercent)
	}
	if cfg.LoopPolicy.Memory.SummaryTokens != 128 {
		t.Errorf("Memory.SummaryTokens = %d, want 128", cfg.LoopPolicy.Memory.SummaryTokens)
	}
	if cfg.LoopPolicy.Memory.MaxTokens != 512 {
		t.Errorf("Memory.MaxTokens = %d, want 512", cfg.LoopPolicy.Memory.MaxTokens)
	}
}

func TestLoad_LoopPolicy_EmbeddingOverride(t *testing.T) {
	requiredEnvs(t)
	setEnv(t, "AGENT_LOOP_POLICY", `{"memory":{"store":true}}`)
	setEnv(t, "AGENT_EMBEDDING_MODEL", "text-embedding-3-small")
	setEnv(t, "AGENT_EMBEDDING_PROVIDER", "openai")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.LoopPolicy == nil || cfg.LoopPolicy.Memory == nil {
		t.Fatal("LoopPolicy.Memory is nil")
	}
	if cfg.LoopPolicy.Memory.EmbeddingModel != "text-embedding-3-small" {
		t.Errorf("EmbeddingModel = %q, want text-embedding-3-small", cfg.LoopPolicy.Memory.EmbeddingModel)
	}
	if cfg.LoopPolicy.Memory.EmbeddingProvider != "openai" {
		t.Errorf("EmbeddingProvider = %q, want openai", cfg.LoopPolicy.Memory.EmbeddingProvider)
	}
}

func TestLoad_LoopPolicy_NotSet(t *testing.T) {
	requiredEnvs(t)
	// No AGENT_LOOP_POLICY set.
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.LoopPolicy != nil {
		t.Errorf("LoopPolicy = %v, want nil when env not set", cfg.LoopPolicy)
	}
}

func TestLoad_ReasoningDefaults(t *testing.T) {
	requiredEnvs(t)

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.ReasoningMode != "" {
		t.Errorf("ReasoningMode = %q, want empty", cfg.ReasoningMode)
	}
	if cfg.ReasoningEffort != "" {
		t.Errorf("ReasoningEffort = %q, want empty", cfg.ReasoningEffort)
	}
	if cfg.ReasoningBudgetTokens != 0 {
		t.Errorf("ReasoningBudgetTokens = %d, want 0", cfg.ReasoningBudgetTokens)
	}
	if cfg.MaxThinkingTokensPerCall != 0 {
		t.Errorf("MaxThinkingTokensPerCall = %d, want 0", cfg.MaxThinkingTokensPerCall)
	}
	if cfg.MaxAnswerTokensPerCall != 0 {
		t.Errorf("MaxAnswerTokensPerCall = %d, want 0", cfg.MaxAnswerTokensPerCall)
	}
}

func TestLoad_ReasoningFromEnv(t *testing.T) {
	requiredEnvs(t)
	setEnv(t, "AGENT_REASONING_MODE", "Explicit")
	setEnv(t, "AGENT_REASONING_EFFORT", "High")
	setEnv(t, "AGENT_REASONING_BUDGET_TOKENS", "2048")
	setEnv(t, "AGENT_MAX_THINKING_TOKENS_PER_CALL", "4096")
	setEnv(t, "AGENT_MAX_ANSWER_TOKENS_PER_CALL", "8192")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.ReasoningMode != "Explicit" {
		t.Errorf("ReasoningMode = %q, want Explicit", cfg.ReasoningMode)
	}
	if cfg.ReasoningEffort != "High" {
		t.Errorf("ReasoningEffort = %q, want High", cfg.ReasoningEffort)
	}
	if cfg.ReasoningBudgetTokens != 2048 {
		t.Errorf("ReasoningBudgetTokens = %d, want 2048", cfg.ReasoningBudgetTokens)
	}
	if cfg.MaxThinkingTokensPerCall != 4096 {
		t.Errorf("MaxThinkingTokensPerCall = %d, want 4096", cfg.MaxThinkingTokensPerCall)
	}
	if cfg.MaxAnswerTokensPerCall != 8192 {
		t.Errorf("MaxAnswerTokensPerCall = %d, want 8192", cfg.MaxAnswerTokensPerCall)
	}
}

func TestLoad_ReasoningModeAndEffortTable(t *testing.T) {
	modes := []string{"Disabled", "Auto", "Explicit"}
	efforts := []string{"Low", "Medium", "High"}
	for _, m := range modes {
		for _, e := range efforts {
			name := m + "_" + e
			t.Run(name, func(t *testing.T) {
				requiredEnvs(t)
				setEnv(t, "AGENT_REASONING_MODE", m)
				setEnv(t, "AGENT_REASONING_EFFORT", e)

				cfg, err := config.Load()
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if cfg.ReasoningMode != m {
					t.Errorf("ReasoningMode = %q, want %q", cfg.ReasoningMode, m)
				}
				if cfg.ReasoningEffort != e {
					t.Errorf("ReasoningEffort = %q, want %q", cfg.ReasoningEffort, e)
				}
			})
		}
	}
}

func TestLoad_ReasoningBudgetTokensMalformed(t *testing.T) {
	requiredEnvs(t)
	setEnv(t, "AGENT_REASONING_BUDGET_TOKENS", "not-a-number")

	_, err := config.Load()
	if err == nil {
		t.Fatal("expected error for malformed AGENT_REASONING_BUDGET_TOKENS, got nil")
	}
}

func TestLoad_MaxThinkingTokensPerCallMalformed(t *testing.T) {
	requiredEnvs(t)
	setEnv(t, "AGENT_MAX_THINKING_TOKENS_PER_CALL", "bad")

	_, err := config.Load()
	if err == nil {
		t.Fatal("expected error for malformed AGENT_MAX_THINKING_TOKENS_PER_CALL, got nil")
	}
}

func TestLoad_MaxAnswerTokensPerCallMalformed(t *testing.T) {
	requiredEnvs(t)
	setEnv(t, "AGENT_MAX_ANSWER_TOKENS_PER_CALL", "bad")

	_, err := config.Load()
	if err == nil {
		t.Fatal("expected error for malformed AGENT_MAX_ANSWER_TOKENS_PER_CALL, got nil")
	}
}

func TestLoad_WebhookTools(t *testing.T) {
	requiredEnvs(t)
	tools := `[{"name":"notify","description":"Send notification","url":"https://hooks.example.com","method":"POST","inputSchema":"{}"}]`
	setEnv(t, "AGENT_WEBHOOK_TOOLS", tools)

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cfg.WebhookTools) != 1 {
		t.Fatalf("WebhookTools len = %d, want 1", len(cfg.WebhookTools))
	}
	if cfg.WebhookTools[0].Name != "notify" {
		t.Errorf("WebhookTools[0].Name = %q, want notify", cfg.WebhookTools[0].Name)
	}
}
