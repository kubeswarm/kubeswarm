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

package controller

import (
	"encoding/json"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kubeswarmv1alpha1 "github.com/kubeswarm/kubeswarm/api/v1alpha1"
)

// envVal finds an env var by name in a slice. Returns ("", false) if not found.
func envVal(envs []corev1.EnvVar, name string) (string, bool) {
	for _, e := range envs {
		if e.Name == name {
			return e.Value, true
		}
	}
	return "", false
}

// buildTestEnvVars calls the production buildEnvVars with minimal dependencies.
func buildTestEnvVars(agent *kubeswarmv1alpha1.SwarmAgent, mcpServers []kubeswarmv1alpha1.MCPToolSpec) []corev1.EnvVar {
	r := &SwarmAgentReconciler{AgentImage: "test:latest"}
	return r.buildEnvVars(agent, nil, nil, nil, mcpServers, nil, nil)
}

func TestSwarmAgentControllerEnvVarMapping(t *testing.T) {

	// -------------------------------------------------------------------------
	// Guardrails limits -> env vars
	// -------------------------------------------------------------------------

	t.Run("guardrails.limits env var mapping", func(t *testing.T) {
		t.Run("should set default limits when guardrails is nil", func(t *testing.T) {
			agent := &kubeswarmv1alpha1.SwarmAgent{
				ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default"},
				Spec: kubeswarmv1alpha1.SwarmAgentSpec{
					Model:  "claude-sonnet-4-6",
					Prompt: &kubeswarmv1alpha1.AgentPrompt{Inline: "test"},
				},
			}
			envs := buildTestEnvVars(agent, nil)

			v, ok := envVal(envs, "AGENT_MAX_TOKENS")
			requireTrue(t, ok)
			requireEqual(t, v, "8000")

			v, ok = envVal(envs, "AGENT_TIMEOUT_SECONDS")
			requireTrue(t, ok)
			requireEqual(t, v, "120")

			v, ok = envVal(envs, "AGENT_MAX_RETRIES")
			requireTrue(t, ok)
			requireEqual(t, v, "3")

			v, ok = envVal(envs, "AGENT_DAILY_TOKEN_LIMIT")
			requireTrue(t, ok)
			requireEqual(t, v, "0")
		})

		t.Run("should propagate custom guardrails limits to env vars", func(t *testing.T) {
			agent := &kubeswarmv1alpha1.SwarmAgent{
				ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default"},
				Spec: kubeswarmv1alpha1.SwarmAgentSpec{
					Model:  "claude-sonnet-4-6",
					Prompt: &kubeswarmv1alpha1.AgentPrompt{Inline: "test"},
					Guardrails: &kubeswarmv1alpha1.AgentGuardrails{
						Limits: &kubeswarmv1alpha1.GuardrailLimits{
							TokensPerCall:  4000,
							TimeoutSeconds: 60,
							Retries:        5,
							DailyTokens:    500000,
						},
					},
				},
			}
			envs := buildTestEnvVars(agent, nil)

			v, _ := envVal(envs, "AGENT_MAX_TOKENS")
			requireEqual(t, v, "4000")
			v, _ = envVal(envs, "AGENT_TIMEOUT_SECONDS")
			requireEqual(t, v, "60")
			v, _ = envVal(envs, "AGENT_MAX_RETRIES")
			requireEqual(t, v, "5")
			v, _ = envVal(envs, "AGENT_DAILY_TOKEN_LIMIT")
			requireEqual(t, v, "500000")
		})

		t.Run("should use defaults when limits struct is present but fields are zero", func(t *testing.T) {
			agent := &kubeswarmv1alpha1.SwarmAgent{
				ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default"},
				Spec: kubeswarmv1alpha1.SwarmAgentSpec{
					Model:  "claude-sonnet-4-6",
					Prompt: &kubeswarmv1alpha1.AgentPrompt{Inline: "test"},
					Guardrails: &kubeswarmv1alpha1.AgentGuardrails{
						Limits: &kubeswarmv1alpha1.GuardrailLimits{}, // all zero
					},
				},
			}
			envs := buildTestEnvVars(agent, nil)

			v, _ := envVal(envs, "AGENT_MAX_TOKENS")
			requireEqual(t, v, "8000", "zero tokensPerCall should keep default")
			v, _ = envVal(envs, "AGENT_TIMEOUT_SECONDS")
			requireEqual(t, v, "120", "zero timeoutSeconds should keep default")
			v, _ = envVal(envs, "AGENT_MAX_RETRIES")
			requireEqual(t, v, "3", "zero retries should keep default")
		})
	})

	// -------------------------------------------------------------------------
	// Model + name -> env vars
	// -------------------------------------------------------------------------

	t.Run("model and identity env vars", func(t *testing.T) {
		t.Run("should set AGENT_MODEL from spec.model", func(t *testing.T) {
			agent := &kubeswarmv1alpha1.SwarmAgent{
				ObjectMeta: metav1.ObjectMeta{Name: "my-agent", Namespace: "default"},
				Spec: kubeswarmv1alpha1.SwarmAgentSpec{
					Model:  "gpt-4o",
					Prompt: &kubeswarmv1alpha1.AgentPrompt{Inline: "you are gpt"},
				},
			}
			envs := buildTestEnvVars(agent, nil)
			v, ok := envVal(envs, "AGENT_MODEL")
			requireTrue(t, ok)
			requireEqual(t, v, "gpt-4o")
		})

		t.Run("should set AGENT_NAME from metadata.name", func(t *testing.T) {
			agent := &kubeswarmv1alpha1.SwarmAgent{
				ObjectMeta: metav1.ObjectMeta{Name: "code-reviewer", Namespace: "default"},
				Spec: kubeswarmv1alpha1.SwarmAgentSpec{
					Model:  "claude-sonnet-4-6",
					Prompt: &kubeswarmv1alpha1.AgentPrompt{Inline: "test"},
				},
			}
			envs := buildTestEnvVars(agent, nil)
			v, ok := envVal(envs, "AGENT_NAME")
			requireTrue(t, ok)
			requireEqual(t, v, "code-reviewer")
		})
	})

	// -------------------------------------------------------------------------
	// MCP servers -> AGENT_MCP_SERVERS JSON
	// -------------------------------------------------------------------------

	t.Run("MCP server env vars", func(t *testing.T) {
		t.Run("should serialize MCP servers to AGENT_MCP_SERVERS JSON", func(t *testing.T) {
			agent := &kubeswarmv1alpha1.SwarmAgent{
				ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default"},
				Spec: kubeswarmv1alpha1.SwarmAgentSpec{
					Model:  "claude-sonnet-4-6",
					Prompt: &kubeswarmv1alpha1.AgentPrompt{Inline: "test"},
				},
			}
			mcpServers := []kubeswarmv1alpha1.MCPToolSpec{
				{Name: "filesystem", URL: "https://mcp.example.com/sse"},
			}
			envs := buildTestEnvVars(agent, mcpServers)
			v, ok := envVal(envs, "AGENT_MCP_SERVERS")
			requireTrue(t, ok)

			var servers []map[string]any
			requireNoError(t, json.Unmarshal([]byte(v), &servers))
			requireLen(t, servers, 1)
			requireEqual(t, servers[0]["name"].(string), "filesystem")
			requireEqual(t, servers[0]["url"].(string), "https://mcp.example.com/sse")
		})

		t.Run("should set authType=bearer for MCP servers with bearer auth", func(t *testing.T) {
			agent := &kubeswarmv1alpha1.SwarmAgent{
				ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default"},
				Spec: kubeswarmv1alpha1.SwarmAgentSpec{
					Model:  "claude-sonnet-4-6",
					Prompt: &kubeswarmv1alpha1.AgentPrompt{Inline: "test"},
				},
			}
			mcpServers := []kubeswarmv1alpha1.MCPToolSpec{
				{
					Name: "authed-server",
					URL:  "https://mcp.example.com/sse",
					Auth: &kubeswarmv1alpha1.MCPServerAuth{
						Bearer: &kubeswarmv1alpha1.BearerAuth{
							SecretKeyRef: corev1.SecretKeySelector{
								LocalObjectReference: corev1.LocalObjectReference{Name: "tok"},
								Key:                  "key",
							},
						},
					},
				},
			}
			envs := buildTestEnvVars(agent, mcpServers)
			v, _ := envVal(envs, "AGENT_MCP_SERVERS")

			var servers []map[string]any
			requireNoError(t, json.Unmarshal([]byte(v), &servers))
			requireEqual(t, servers[0]["authType"].(string), "bearer")
			tokenEnvVar, ok := servers[0]["tokenEnvVar"].(string)
			requireTrue(t, ok)
			requireNotEmpty(t, tokenEnvVar)
		})
	})

	// -------------------------------------------------------------------------
	// Webhook tools -> AGENT_WEBHOOK_TOOLS JSON
	// -------------------------------------------------------------------------

	t.Run("webhook tool env vars", func(t *testing.T) {
		t.Run("should serialize webhook tools to AGENT_WEBHOOK_TOOLS JSON", func(t *testing.T) {
			agent := &kubeswarmv1alpha1.SwarmAgent{
				ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default"},
				Spec: kubeswarmv1alpha1.SwarmAgentSpec{
					Model:  "claude-sonnet-4-6",
					Prompt: &kubeswarmv1alpha1.AgentPrompt{Inline: "test"},
					Tools: &kubeswarmv1alpha1.AgentTools{
						Webhooks: []kubeswarmv1alpha1.WebhookToolSpec{
							{Name: "notify", URL: "https://hooks.example.com/notify", Method: "POST", Description: "Send notification"},
						},
					},
				},
			}
			envs := buildTestEnvVars(agent, nil)
			v, ok := envVal(envs, "AGENT_WEBHOOK_TOOLS")
			requireTrue(t, ok)

			var tools []map[string]any
			requireNoError(t, json.Unmarshal([]byte(v), &tools))
			requireLen(t, tools, 1)
			requireEqual(t, tools[0]["name"].(string), "notify")
		})

		t.Run("should not set AGENT_WEBHOOK_TOOLS when no webhooks configured", func(t *testing.T) {
			agent := &kubeswarmv1alpha1.SwarmAgent{
				ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default"},
				Spec: kubeswarmv1alpha1.SwarmAgentSpec{
					Model:  "claude-sonnet-4-6",
					Prompt: &kubeswarmv1alpha1.AgentPrompt{Inline: "test"},
				},
			}
			envs := buildTestEnvVars(agent, nil)
			_, ok := envVal(envs, "AGENT_WEBHOOK_TOOLS")
			requireFalse(t, ok)
		})
	})

	// -------------------------------------------------------------------------
	// Semantic health check -> AGENT_VALIDATOR_PROMPT
	// -------------------------------------------------------------------------

	t.Run("health check env vars", func(t *testing.T) {
		t.Run("should set AGENT_VALIDATOR_PROMPT for semantic health check", func(t *testing.T) {
			agent := &kubeswarmv1alpha1.SwarmAgent{
				ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default"},
				Spec: kubeswarmv1alpha1.SwarmAgentSpec{
					Model:  "claude-sonnet-4-6",
					Prompt: &kubeswarmv1alpha1.AgentPrompt{Inline: "test"},
					Observability: &kubeswarmv1alpha1.AgentObservability{
						HealthCheck: &kubeswarmv1alpha1.AgentHealthCheck{
							Type:   "semantic",
							Prompt: "Reply OK if ready.",
						},
					},
				},
			}
			envs := buildTestEnvVars(agent, nil)
			v, ok := envVal(envs, "AGENT_VALIDATOR_PROMPT")
			requireTrue(t, ok)
			requireEqual(t, v, "Reply OK if ready.")
		})

		t.Run("should not set AGENT_VALIDATOR_PROMPT for ping health check", func(t *testing.T) {
			agent := &kubeswarmv1alpha1.SwarmAgent{
				ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default"},
				Spec: kubeswarmv1alpha1.SwarmAgentSpec{
					Model:  "claude-sonnet-4-6",
					Prompt: &kubeswarmv1alpha1.AgentPrompt{Inline: "test"},
					Observability: &kubeswarmv1alpha1.AgentObservability{
						HealthCheck: &kubeswarmv1alpha1.AgentHealthCheck{Type: "ping"},
					},
				},
			}
			envs := buildTestEnvVars(agent, nil)
			_, ok := envVal(envs, "AGENT_VALIDATOR_PROMPT")
			requireFalse(t, ok)
		})
	})

	// -------------------------------------------------------------------------
	// Plugin addresses -> env vars
	// -------------------------------------------------------------------------

	t.Run("plugin env vars", func(t *testing.T) {
		t.Run("should set plugin env vars when plugins configured", func(t *testing.T) {
			agent := &kubeswarmv1alpha1.SwarmAgent{
				ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default"},
				Spec: kubeswarmv1alpha1.SwarmAgentSpec{
					Model:  "claude-sonnet-4-6",
					Prompt: &kubeswarmv1alpha1.AgentPrompt{Inline: "test"},
					Infrastructure: &kubeswarmv1alpha1.AgentInfrastructure{
						Plugins: &kubeswarmv1alpha1.AgentPlugins{
							LLM:   &kubeswarmv1alpha1.PluginEndpoint{Address: "llm.svc:50051"},
							Queue: &kubeswarmv1alpha1.PluginEndpoint{Address: "queue.svc:50052"},
						},
					},
				},
			}
			envs := buildTestEnvVars(agent, nil)
			v, ok := envVal(envs, "SWARM_PLUGIN_LLM_ADDR")
			requireTrue(t, ok)
			requireEqual(t, v, "llm.svc:50051")

			v, ok = envVal(envs, "SWARM_PLUGIN_QUEUE_ADDR")
			requireTrue(t, ok)
			requireEqual(t, v, "queue.svc:50052")
		})

		t.Run("should not set plugin env vars when plugins not configured", func(t *testing.T) {
			agent := &kubeswarmv1alpha1.SwarmAgent{
				ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default"},
				Spec: kubeswarmv1alpha1.SwarmAgentSpec{
					Model:  "claude-sonnet-4-6",
					Prompt: &kubeswarmv1alpha1.AgentPrompt{Inline: "test"},
				},
			}
			envs := buildTestEnvVars(agent, nil)
			_, ok := envVal(envs, "SWARM_PLUGIN_LLM_ADDR")
			requireFalse(t, ok)
			_, ok = envVal(envs, "SWARM_PLUGIN_QUEUE_ADDR")
			requireFalse(t, ok)
		})
	})

	// -------------------------------------------------------------------------
	// Loop policy -> AGENT_LOOP_POLICY JSON
	// -------------------------------------------------------------------------

	t.Run("loop policy env vars", func(t *testing.T) {
		t.Run("should serialize loop policy to AGENT_LOOP_POLICY JSON", func(t *testing.T) {
			agent := &kubeswarmv1alpha1.SwarmAgent{
				ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default"},
				Spec: kubeswarmv1alpha1.SwarmAgentSpec{
					Model:  "claude-sonnet-4-6",
					Prompt: &kubeswarmv1alpha1.AgentPrompt{Inline: "test"},
					Runtime: kubeswarmv1alpha1.AgentRuntime{
						Loop: &kubeswarmv1alpha1.AgentLoopPolicy{
							Dedup: true,
							Compression: &kubeswarmv1alpha1.LoopCompressionConfig{
								ThresholdPercent:    80,
								PreserveRecentTurns: 3,
								Model:               "claude-haiku-4-5-20251001",
							},
						},
					},
				},
			}
			envs := buildTestEnvVars(agent, nil)
			v, ok := envVal(envs, "AGENT_LOOP_POLICY")
			requireTrue(t, ok)

			var lp map[string]any
			requireNoError(t, json.Unmarshal([]byte(v), &lp))
			requireEqual(t, lp["dedup"].(bool), true)
			comp, ok := lp["compression"].(map[string]any)
			requireTrue(t, ok)
			requireEqual(t, comp["thresholdPercent"].(float64), float64(80))
			requireEqual(t, comp["model"].(string), "claude-haiku-4-5-20251001")
		})

		t.Run("should not set AGENT_LOOP_POLICY when loop is nil", func(t *testing.T) {
			agent := &kubeswarmv1alpha1.SwarmAgent{
				ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default"},
				Spec: kubeswarmv1alpha1.SwarmAgentSpec{
					Model:  "claude-sonnet-4-6",
					Prompt: &kubeswarmv1alpha1.AgentPrompt{Inline: "test"},
				},
			}
			envs := buildTestEnvVars(agent, nil)
			_, ok := envVal(envs, "AGENT_LOOP_POLICY")
			requireFalse(t, ok)
		})
	})

	// -------------------------------------------------------------------------
	// Reasoning + thinking/answer token caps -> env vars (RFC-0033 phase 4)
	// -------------------------------------------------------------------------

	t.Run("buildEnvVars reasoning injection", func(t *testing.T) {
		baseAgent := func() *kubeswarmv1alpha1.SwarmAgent {
			return &kubeswarmv1alpha1.SwarmAgent{
				ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default"},
				Spec: kubeswarmv1alpha1.SwarmAgentSpec{
					Model:  "claude-sonnet-4-6",
					Prompt: &kubeswarmv1alpha1.AgentPrompt{Inline: "test"},
				},
			}
		}
		i32 := func(v int32) *int32 { return &v }

		t.Run("injects all five vars when spec.reasoning and guardrails.limits are fully set", func(t *testing.T) {
			agent := baseAgent()
			agent.Spec.Reasoning = &kubeswarmv1alpha1.ReasoningConfig{
				Mode:         kubeswarmv1alpha1.ReasoningExplicit,
				Effort:       kubeswarmv1alpha1.ReasoningEffortHigh,
				BudgetTokens: i32(2048),
			}
			agent.Spec.Guardrails = &kubeswarmv1alpha1.AgentGuardrails{
				Limits: &kubeswarmv1alpha1.GuardrailLimits{
					MaxThinkingTokensPerCall: i32(4096),
					MaxAnswerTokensPerCall:   i32(8192),
				},
			}
			envs := buildTestEnvVars(agent, nil)

			v, ok := envVal(envs, "AGENT_REASONING_MODE")
			requireTrue(t, ok)
			requireEqual(t, v, "Explicit")

			v, ok = envVal(envs, "AGENT_REASONING_EFFORT")
			requireTrue(t, ok)
			requireEqual(t, v, "High")

			v, ok = envVal(envs, "AGENT_REASONING_BUDGET_TOKENS")
			requireTrue(t, ok)
			requireEqual(t, v, "2048")

			v, ok = envVal(envs, "AGENT_MAX_THINKING_TOKENS_PER_CALL")
			requireTrue(t, ok)
			requireEqual(t, v, "4096")

			v, ok = envVal(envs, "AGENT_MAX_ANSWER_TOKENS_PER_CALL")
			requireTrue(t, ok)
			requireEqual(t, v, "8192")
		})

		t.Run("injects AGENT_REASONING_MODE only when mode is the only field set", func(t *testing.T) {
			agent := baseAgent()
			agent.Spec.Reasoning = &kubeswarmv1alpha1.ReasoningConfig{
				Mode: kubeswarmv1alpha1.ReasoningAuto,
			}
			envs := buildTestEnvVars(agent, nil)

			v, ok := envVal(envs, "AGENT_REASONING_MODE")
			requireTrue(t, ok)
			requireEqual(t, v, "Auto")

			_, ok = envVal(envs, "AGENT_REASONING_EFFORT")
			requireFalse(t, ok)
			_, ok = envVal(envs, "AGENT_REASONING_BUDGET_TOKENS")
			requireFalse(t, ok)
			_, ok = envVal(envs, "AGENT_MAX_THINKING_TOKENS_PER_CALL")
			requireFalse(t, ok)
			_, ok = envVal(envs, "AGENT_MAX_ANSWER_TOKENS_PER_CALL")
			requireFalse(t, ok)
		})

		t.Run("omits all reasoning env vars when spec.reasoning is nil and guardrails limits fields are nil", func(t *testing.T) {
			agent := baseAgent()
			envs := buildTestEnvVars(agent, nil)

			for _, name := range []string{
				"AGENT_REASONING_MODE",
				"AGENT_REASONING_EFFORT",
				"AGENT_REASONING_BUDGET_TOKENS",
				"AGENT_MAX_THINKING_TOKENS_PER_CALL",
				"AGENT_MAX_ANSWER_TOKENS_PER_CALL",
			} {
				_, ok := envVal(envs, name)
				requireFalse(t, ok, "expected "+name+" to be absent")
			}
		})

		t.Run("injects AGENT_MAX_THINKING_TOKENS_PER_CALL when set even if spec.reasoning is nil", func(t *testing.T) {
			agent := baseAgent()
			agent.Spec.Guardrails = &kubeswarmv1alpha1.AgentGuardrails{
				Limits: &kubeswarmv1alpha1.GuardrailLimits{
					MaxThinkingTokensPerCall: i32(1024),
				},
			}
			envs := buildTestEnvVars(agent, nil)

			v, ok := envVal(envs, "AGENT_MAX_THINKING_TOKENS_PER_CALL")
			requireTrue(t, ok)
			requireEqual(t, v, "1024")

			_, ok = envVal(envs, "AGENT_REASONING_MODE")
			requireFalse(t, ok)
			_, ok = envVal(envs, "AGENT_REASONING_EFFORT")
			requireFalse(t, ok)
			_, ok = envVal(envs, "AGENT_REASONING_BUDGET_TOKENS")
			requireFalse(t, ok)
		})

		t.Run("handles BudgetTokens nil pointer without panic", func(t *testing.T) {
			agent := baseAgent()
			agent.Spec.Reasoning = &kubeswarmv1alpha1.ReasoningConfig{
				Mode:         kubeswarmv1alpha1.ReasoningExplicit,
				Effort:       kubeswarmv1alpha1.ReasoningEffortMedium,
				BudgetTokens: nil,
			}
			var envs []corev1.EnvVar
			requireNoPanic(t, func() { envs = buildTestEnvVars(agent, nil) })

			_, ok := envVal(envs, "AGENT_REASONING_BUDGET_TOKENS")
			requireFalse(t, ok)

			v, ok := envVal(envs, "AGENT_REASONING_MODE")
			requireTrue(t, ok)
			requireEqual(t, v, "Explicit")
		})
	})

	t.Run("team env vars from annotations", func(t *testing.T) {
		t.Run("should set team env vars from annotations and labels", func(t *testing.T) {
			agent := &kubeswarmv1alpha1.SwarmAgent{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test",
					Namespace: "default",
					Annotations: map[string]string{
						kubeswarmv1alpha1.AnnotationTeamQueueURL: "redis://redis:6379/team-queue",
						kubeswarmv1alpha1.AnnotationTeamRoutes:   `{"reviewer":"redis://redis:6379/reviewer"}`,
						kubeswarmv1alpha1.AnnotationTeamRole:     "coordinator",
					},
					Labels: map[string]string{
						kubeswarmv1alpha1.LabelTeam: "my-team",
					},
				},
				Spec: kubeswarmv1alpha1.SwarmAgentSpec{
					Model:  "claude-sonnet-4-6",
					Prompt: &kubeswarmv1alpha1.AgentPrompt{Inline: "test"},
				},
			}
			envs := buildTestEnvVars(agent, nil)

			v, ok := envVal(envs, "TASK_QUEUE_URL")
			requireTrue(t, ok)
			requireEqual(t, v, "redis://redis:6379/team-queue")

			v, ok = envVal(envs, "AGENT_TEAM_ROUTES")
			requireTrue(t, ok)
			requireContains(t, v, "reviewer")

			v, ok = envVal(envs, "AGENT_TEAM_ROLE")
			requireTrue(t, ok)
			requireEqual(t, v, "coordinator")

			v, ok = envVal(envs, "AGENT_TEAM_NAME")
			requireTrue(t, ok)
			requireEqual(t, v, "my-team")
		})
	})

	// -------------------------------------------------------------------------
	// Efficiency -> prompt cache env vars (RFC-0045)
	// -------------------------------------------------------------------------

	t.Run("efficiency.promptCache env var mapping", func(t *testing.T) {
		t.Run("should not set AGENT_PROMPT_CACHE when efficiency is nil", func(t *testing.T) {
			agent := &kubeswarmv1alpha1.SwarmAgent{
				ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default"},
				Spec: kubeswarmv1alpha1.SwarmAgentSpec{
					Model:  "claude-sonnet-4-6",
					Prompt: &kubeswarmv1alpha1.AgentPrompt{Inline: "test"},
				},
			}
			envs := buildTestEnvVars(agent, nil)
			_, ok := envVal(envs, "AGENT_PROMPT_CACHE")
			if ok {
				t.Error("expected AGENT_PROMPT_CACHE to be absent when efficiency is nil")
			}
		})

		t.Run("should not set AGENT_PROMPT_CACHE when disabled", func(t *testing.T) {
			agent := &kubeswarmv1alpha1.SwarmAgent{
				ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default"},
				Spec: kubeswarmv1alpha1.SwarmAgentSpec{
					Model:  "claude-sonnet-4-6",
					Prompt: &kubeswarmv1alpha1.AgentPrompt{Inline: "test"},
					Efficiency: &kubeswarmv1alpha1.EfficiencyConfig{
						PromptCache: &kubeswarmv1alpha1.PromptCacheConfig{
							Enabled: false,
						},
					},
				},
			}
			envs := buildTestEnvVars(agent, nil)
			_, ok := envVal(envs, "AGENT_PROMPT_CACHE")
			if ok {
				t.Error("expected AGENT_PROMPT_CACHE to be absent when disabled")
			}
		})

		t.Run("should set AGENT_PROMPT_CACHE when enabled", func(t *testing.T) {
			agent := &kubeswarmv1alpha1.SwarmAgent{
				ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default"},
				Spec: kubeswarmv1alpha1.SwarmAgentSpec{
					Model:  "claude-sonnet-4-6",
					Prompt: &kubeswarmv1alpha1.AgentPrompt{Inline: "test"},
					Efficiency: &kubeswarmv1alpha1.EfficiencyConfig{
						PromptCache: &kubeswarmv1alpha1.PromptCacheConfig{
							Enabled:               true,
							CacheableSystemPrompt: true,
							CacheableTools:        true,
							MinPrefixTokens:       2048,
						},
					},
				},
			}
			envs := buildTestEnvVars(agent, nil)
			v, ok := envVal(envs, "AGENT_PROMPT_CACHE")
			if !ok {
				t.Fatal("expected AGENT_PROMPT_CACHE to be set")
			}
			var parsed map[string]any
			if err := json.Unmarshal([]byte(v), &parsed); err != nil {
				t.Fatalf("AGENT_PROMPT_CACHE is not valid JSON: %v", err)
			}
			if parsed["enabled"] != true {
				t.Error("expected enabled=true in AGENT_PROMPT_CACHE")
			}
			if parsed["cacheableSystemPrompt"] != true {
				t.Error("expected cacheableSystemPrompt=true")
			}
			if parsed["cacheableTools"] != true {
				t.Error("expected cacheableTools=true")
			}
			if int(parsed["minPrefixTokens"].(float64)) != 2048 {
				t.Errorf("minPrefixTokens = %v, want 2048", parsed["minPrefixTokens"])
			}
		})
	})
}
