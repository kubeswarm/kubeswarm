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

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

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
	return r.buildEnvVars(agent, nil, nil, nil, mcpServers)
}

var _ = Describe("SwarmAgent Controller - env var mapping (pure functions)", func() {

	// -------------------------------------------------------------------------
	// Guardrails limits -> env vars
	// -------------------------------------------------------------------------

	Context("guardrails.limits env var mapping", func() {
		It("should set default limits when guardrails is nil", func() {
			agent := &kubeswarmv1alpha1.SwarmAgent{
				ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default"},
				Spec: kubeswarmv1alpha1.SwarmAgentSpec{
					Model:  "claude-sonnet-4-6",
					Prompt: &kubeswarmv1alpha1.AgentPrompt{Inline: "test"},
				},
			}
			envs := buildTestEnvVars(agent, nil)

			v, ok := envVal(envs, "AGENT_MAX_TOKENS")
			Expect(ok).To(BeTrue())
			Expect(v).To(Equal("8000"))

			v, ok = envVal(envs, "AGENT_TIMEOUT_SECONDS")
			Expect(ok).To(BeTrue())
			Expect(v).To(Equal("120"))

			v, ok = envVal(envs, "AGENT_MAX_RETRIES")
			Expect(ok).To(BeTrue())
			Expect(v).To(Equal("3"))

			v, ok = envVal(envs, "AGENT_DAILY_TOKEN_LIMIT")
			Expect(ok).To(BeTrue())
			Expect(v).To(Equal("0"))
		})

		It("should propagate custom guardrails limits to env vars", func() {
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
			Expect(v).To(Equal("4000"))
			v, _ = envVal(envs, "AGENT_TIMEOUT_SECONDS")
			Expect(v).To(Equal("60"))
			v, _ = envVal(envs, "AGENT_MAX_RETRIES")
			Expect(v).To(Equal("5"))
			v, _ = envVal(envs, "AGENT_DAILY_TOKEN_LIMIT")
			Expect(v).To(Equal("500000"))
		})

		It("should use defaults when limits struct is present but fields are zero", func() {
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
			Expect(v).To(Equal("8000"), "zero tokensPerCall should keep default")
			v, _ = envVal(envs, "AGENT_TIMEOUT_SECONDS")
			Expect(v).To(Equal("120"), "zero timeoutSeconds should keep default")
			v, _ = envVal(envs, "AGENT_MAX_RETRIES")
			Expect(v).To(Equal("3"), "zero retries should keep default")
		})
	})

	// -------------------------------------------------------------------------
	// Model + name -> env vars
	// -------------------------------------------------------------------------

	Context("model and identity env vars", func() {
		It("should set AGENT_MODEL from spec.model", func() {
			agent := &kubeswarmv1alpha1.SwarmAgent{
				ObjectMeta: metav1.ObjectMeta{Name: "my-agent", Namespace: "default"},
				Spec: kubeswarmv1alpha1.SwarmAgentSpec{
					Model:  "gpt-4o",
					Prompt: &kubeswarmv1alpha1.AgentPrompt{Inline: "you are gpt"},
				},
			}
			envs := buildTestEnvVars(agent, nil)
			v, ok := envVal(envs, "AGENT_MODEL")
			Expect(ok).To(BeTrue())
			Expect(v).To(Equal("gpt-4o"))
		})

		It("should set AGENT_NAME from metadata.name", func() {
			agent := &kubeswarmv1alpha1.SwarmAgent{
				ObjectMeta: metav1.ObjectMeta{Name: "code-reviewer", Namespace: "default"},
				Spec: kubeswarmv1alpha1.SwarmAgentSpec{
					Model:  "claude-sonnet-4-6",
					Prompt: &kubeswarmv1alpha1.AgentPrompt{Inline: "test"},
				},
			}
			envs := buildTestEnvVars(agent, nil)
			v, ok := envVal(envs, "AGENT_NAME")
			Expect(ok).To(BeTrue())
			Expect(v).To(Equal("code-reviewer"))
		})
	})

	// -------------------------------------------------------------------------
	// MCP servers -> AGENT_MCP_SERVERS JSON
	// -------------------------------------------------------------------------

	Context("MCP server env vars", func() {
		It("should serialize MCP servers to AGENT_MCP_SERVERS JSON", func() {
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
			Expect(ok).To(BeTrue())

			var servers []map[string]any
			Expect(json.Unmarshal([]byte(v), &servers)).To(Succeed())
			Expect(servers).To(HaveLen(1))
			Expect(servers[0]["name"]).To(Equal("filesystem"))
			Expect(servers[0]["url"]).To(Equal("https://mcp.example.com/sse"))
		})

		It("should set authType=bearer for MCP servers with bearer auth", func() {
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
			Expect(json.Unmarshal([]byte(v), &servers)).To(Succeed())
			Expect(servers[0]["authType"]).To(Equal("bearer"))
			Expect(servers[0]["tokenEnvVar"]).NotTo(BeEmpty())
		})
	})

	// -------------------------------------------------------------------------
	// Webhook tools -> AGENT_WEBHOOK_TOOLS JSON
	// -------------------------------------------------------------------------

	Context("webhook tool env vars", func() {
		It("should serialize webhook tools to AGENT_WEBHOOK_TOOLS JSON", func() {
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
			Expect(ok).To(BeTrue())

			var tools []map[string]any
			Expect(json.Unmarshal([]byte(v), &tools)).To(Succeed())
			Expect(tools).To(HaveLen(1))
			Expect(tools[0]["name"]).To(Equal("notify"))
		})

		It("should not set AGENT_WEBHOOK_TOOLS when no webhooks configured", func() {
			agent := &kubeswarmv1alpha1.SwarmAgent{
				ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default"},
				Spec: kubeswarmv1alpha1.SwarmAgentSpec{
					Model:  "claude-sonnet-4-6",
					Prompt: &kubeswarmv1alpha1.AgentPrompt{Inline: "test"},
				},
			}
			envs := buildTestEnvVars(agent, nil)
			_, ok := envVal(envs, "AGENT_WEBHOOK_TOOLS")
			Expect(ok).To(BeFalse())
		})
	})

	// -------------------------------------------------------------------------
	// Semantic health check -> AGENT_VALIDATOR_PROMPT
	// -------------------------------------------------------------------------

	Context("health check env vars", func() {
		It("should set AGENT_VALIDATOR_PROMPT for semantic health check", func() {
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
			Expect(ok).To(BeTrue())
			Expect(v).To(Equal("Reply OK if ready."))
		})

		It("should not set AGENT_VALIDATOR_PROMPT for ping health check", func() {
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
			Expect(ok).To(BeFalse())
		})
	})

	// -------------------------------------------------------------------------
	// Plugin addresses -> env vars
	// -------------------------------------------------------------------------

	Context("plugin env vars", func() {
		It("should set plugin env vars when plugins configured", func() {
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
			Expect(ok).To(BeTrue())
			Expect(v).To(Equal("llm.svc:50051"))

			v, ok = envVal(envs, "SWARM_PLUGIN_QUEUE_ADDR")
			Expect(ok).To(BeTrue())
			Expect(v).To(Equal("queue.svc:50052"))
		})

		It("should not set plugin env vars when plugins not configured", func() {
			agent := &kubeswarmv1alpha1.SwarmAgent{
				ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default"},
				Spec: kubeswarmv1alpha1.SwarmAgentSpec{
					Model:  "claude-sonnet-4-6",
					Prompt: &kubeswarmv1alpha1.AgentPrompt{Inline: "test"},
				},
			}
			envs := buildTestEnvVars(agent, nil)
			_, ok := envVal(envs, "SWARM_PLUGIN_LLM_ADDR")
			Expect(ok).To(BeFalse())
			_, ok = envVal(envs, "SWARM_PLUGIN_QUEUE_ADDR")
			Expect(ok).To(BeFalse())
		})
	})

	// -------------------------------------------------------------------------
	// Loop policy -> AGENT_LOOP_POLICY JSON
	// -------------------------------------------------------------------------

	Context("loop policy env vars", func() {
		It("should serialize loop policy to AGENT_LOOP_POLICY JSON", func() {
			agent := &kubeswarmv1alpha1.SwarmAgent{
				ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default"},
				Spec: kubeswarmv1alpha1.SwarmAgentSpec{
					Model:  "claude-sonnet-4-6",
					Prompt: &kubeswarmv1alpha1.AgentPrompt{Inline: "test"},
					Runtime: kubeswarmv1alpha1.AgentRuntime{
						Loop: &kubeswarmv1alpha1.AgentLoopPolicy{
							Dedup: true,
							Compression: &kubeswarmv1alpha1.LoopCompressionConfig{
								Threshold:           0.80,
								PreserveRecentTurns: 3,
								Model:               "claude-haiku-4-5-20251001",
							},
						},
					},
				},
			}
			envs := buildTestEnvVars(agent, nil)
			v, ok := envVal(envs, "AGENT_LOOP_POLICY")
			Expect(ok).To(BeTrue())

			var lp map[string]any
			Expect(json.Unmarshal([]byte(v), &lp)).To(Succeed())
			Expect(lp["dedup"]).To(BeTrue())
			comp, ok := lp["compression"].(map[string]any)
			Expect(ok).To(BeTrue())
			Expect(comp["threshold"]).To(BeNumerically("~", 0.80, 0.01))
			Expect(comp["model"]).To(Equal("claude-haiku-4-5-20251001"))
		})

		It("should not set AGENT_LOOP_POLICY when loop is nil", func() {
			agent := &kubeswarmv1alpha1.SwarmAgent{
				ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default"},
				Spec: kubeswarmv1alpha1.SwarmAgentSpec{
					Model:  "claude-sonnet-4-6",
					Prompt: &kubeswarmv1alpha1.AgentPrompt{Inline: "test"},
				},
			}
			envs := buildTestEnvVars(agent, nil)
			_, ok := envVal(envs, "AGENT_LOOP_POLICY")
			Expect(ok).To(BeFalse())
		})
	})

	// -------------------------------------------------------------------------
	// Team annotations -> env vars
	// -------------------------------------------------------------------------

	Context("team env vars from annotations", func() {
		It("should set team env vars from annotations and labels", func() {
			agent := &kubeswarmv1alpha1.SwarmAgent{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test",
					Namespace: "default",
					Annotations: map[string]string{
						annotationTeamQueueURL: "redis://redis:6379/team-queue",
						annotationTeamRoutes:   `{"reviewer":"redis://redis:6379/reviewer"}`,
						annotationTeamRole:     "coordinator",
					},
					Labels: map[string]string{
						"kubeswarm/team": "my-team",
					},
				},
				Spec: kubeswarmv1alpha1.SwarmAgentSpec{
					Model:  "claude-sonnet-4-6",
					Prompt: &kubeswarmv1alpha1.AgentPrompt{Inline: "test"},
				},
			}
			envs := buildTestEnvVars(agent, nil)

			v, ok := envVal(envs, "TASK_QUEUE_URL")
			Expect(ok).To(BeTrue())
			Expect(v).To(Equal("redis://redis:6379/team-queue"))

			v, ok = envVal(envs, "AGENT_TEAM_ROUTES")
			Expect(ok).To(BeTrue())
			Expect(v).To(ContainSubstring("reviewer"))

			v, ok = envVal(envs, "AGENT_TEAM_ROLE")
			Expect(ok).To(BeTrue())
			Expect(v).To(Equal("coordinator"))

			v, ok = envVal(envs, "AGENT_TEAM_NAME")
			Expect(ok).To(BeTrue())
			Expect(v).To(Equal("my-team"))
		})
	})
})
