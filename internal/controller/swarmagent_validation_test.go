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
	"fmt"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	kubeswarmv1alpha1 "github.com/kubeswarm/kubeswarm/api/v1alpha1"
)

func TestSwarmAgentCRDValidation(t *testing.T) {
	const namespace = "default"
	var nameIdx int

	uniqueName := func(prefix string) string {
		nameIdx++
		return fmt.Sprintf("%s-%d", prefix, nameIdx)
	}

	validAgent := func(name string) *kubeswarmv1alpha1.SwarmAgent {
		return &kubeswarmv1alpha1.SwarmAgent{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
			Spec: kubeswarmv1alpha1.SwarmAgentSpec{
				Model:  "claude-sonnet-4-6",
				Prompt: &kubeswarmv1alpha1.AgentPrompt{Inline: "You are a test agent."},
			},
		}
	}

	cleanup := func(t *testing.T) {
		t.Helper()
		t.Cleanup(func() {
			// Best-effort cleanup of any agents created during test.
			list := &kubeswarmv1alpha1.SwarmAgentList{}
			if err := k8sClient.List(ctx, list); err == nil {
				for i := range list.Items {
					_ = k8sClient.Delete(ctx, &list.Items[i])
				}
			}
		})
	}

	// -------------------------------------------------------------------------
	// Happy path
	// -------------------------------------------------------------------------

	t.Run("valid minimal spec", func(t *testing.T) {
		cleanup(t)

		t.Run("should accept a SwarmAgent with model + inline prompt", func(t *testing.T) {
			agent := validAgent(uniqueName("valid-minimal"))
			requireNoError(t, k8sClient.Create(ctx, agent))
		})
	})

	t.Run("valid full spec", func(t *testing.T) {
		cleanup(t)

		t.Run("should accept a SwarmAgent with all sections populated", func(t *testing.T) {
			replicas := int32(2)
			agent := validAgent(uniqueName("valid-full"))
			agent.Spec.Runtime = kubeswarmv1alpha1.AgentRuntime{
				Replicas:  &replicas,
				Resources: &corev1.ResourceRequirements{},
			}
			agent.Spec.Guardrails = &kubeswarmv1alpha1.AgentGuardrails{
				Limits: &kubeswarmv1alpha1.GuardrailLimits{
					TokensPerCall:   4000,
					ConcurrentTasks: 3,
					TimeoutSeconds:  60,
					DailyTokens:     100000,
					Retries:         5,
				},
				Tools: &kubeswarmv1alpha1.ToolPermissions{
					Allow: []string{"filesystem/*"},
					Deny:  []string{"shell/exec"},
					Trust: &kubeswarmv1alpha1.ToolTrustPolicy{
						Default: "external",
					},
				},
			}
			agent.Spec.Tools = &kubeswarmv1alpha1.AgentTools{
				MCP: []kubeswarmv1alpha1.MCPToolSpec{
					{Name: "fs", URL: "https://mcp.example.com/sse"},
				},
				Webhooks: []kubeswarmv1alpha1.WebhookToolSpec{
					{Name: "notify", URL: "https://hooks.example.com/notify", Method: "POST"},
				},
			}
			agent.Spec.Capabilities = []kubeswarmv1alpha1.AgentCapability{
				{
					Name:        "code-review",
					Description: "Reviews code",
					Tags:        []string{"code"},
					ExposeMCP:   true,
					InputSchema: &runtime.RawExtension{Raw: []byte(`{"type":"object"}`)},
				},
			}
			agent.Spec.Observability = &kubeswarmv1alpha1.AgentObservability{
				Logging: &kubeswarmv1alpha1.AgentLogging{Level: kubeswarmv1alpha1.LogLevelInfo, ToolCalls: true},
			}
			requireNoError(t, k8sClient.Create(ctx, agent))
		})
	})

	// -------------------------------------------------------------------------
	// Enum validation
	// -------------------------------------------------------------------------

	t.Run("trust level enum", func(t *testing.T) {
		cleanup(t)

		t.Run("should accept valid trust levels", func(t *testing.T) {
			for _, trust := range []kubeswarmv1alpha1.ToolTrustLevel{"internal", "external", "sandbox"} {
				agent := validAgent(uniqueName("trust"))
				agent.Spec.Tools = &kubeswarmv1alpha1.AgentTools{
					MCP: []kubeswarmv1alpha1.MCPToolSpec{
						{Name: "srv", URL: "https://example.com/sse", Trust: trust},
					},
				}
				requireNoError(t, k8sClient.Create(ctx, agent))
			}
		})

		t.Run("should reject invalid trust level", func(t *testing.T) {
			agent := validAgent(uniqueName("bad-trust"))
			agent.Spec.Tools = &kubeswarmv1alpha1.AgentTools{
				MCP: []kubeswarmv1alpha1.MCPToolSpec{
					{Name: "srv", URL: "https://example.com/sse", Trust: "bogus"},
				},
			}
			err := k8sClient.Create(ctx, agent)
			requireError(t, err)
		})
	})

	t.Run("networkPolicy enum", func(t *testing.T) {
		cleanup(t)

		t.Run("should accept valid values", func(t *testing.T) {
			for _, np := range []kubeswarmv1alpha1.NetworkPolicyMode{"default", "strict", "disabled"} {
				agent := validAgent(uniqueName("np"))
				if agent.Spec.Infrastructure == nil {
					agent.Spec.Infrastructure = &kubeswarmv1alpha1.AgentInfrastructure{}
				}
				agent.Spec.Infrastructure.NetworkPolicy = np
				requireNoError(t, k8sClient.Create(ctx, agent))
			}
		})

		t.Run("should reject invalid networkPolicy", func(t *testing.T) {
			agent := validAgent(uniqueName("bad-np"))
			if agent.Spec.Infrastructure == nil {
				agent.Spec.Infrastructure = &kubeswarmv1alpha1.AgentInfrastructure{}
			}
			agent.Spec.Infrastructure.NetworkPolicy = "open"
			err := k8sClient.Create(ctx, agent)
			requireError(t, err)
		})
	})

	t.Run("healthCheck type enum", func(t *testing.T) {
		cleanup(t)

		t.Run("should accept semantic and ping", func(t *testing.T) {
			for _, hcType := range []kubeswarmv1alpha1.HealthCheckType{kubeswarmv1alpha1.HealthCheckSemantic, kubeswarmv1alpha1.HealthCheckPing} {
				agent := validAgent(uniqueName("hc"))
				agent.Spec.Observability = &kubeswarmv1alpha1.AgentObservability{
					HealthCheck: &kubeswarmv1alpha1.AgentHealthCheck{Type: hcType},
				}
				requireNoError(t, k8sClient.Create(ctx, agent))
			}
		})

		t.Run("should reject invalid healthCheck type", func(t *testing.T) {
			agent := validAgent(uniqueName("bad-hc"))
			agent.Spec.Observability = &kubeswarmv1alpha1.AgentObservability{
				HealthCheck: &kubeswarmv1alpha1.AgentHealthCheck{Type: kubeswarmv1alpha1.HealthCheckType("tcp")},
			}
			err := k8sClient.Create(ctx, agent)
			requireError(t, err)
		})
	})

	t.Run("logging level enum", func(t *testing.T) {
		cleanup(t)

		t.Run("should accept valid levels", func(t *testing.T) {
			for _, level := range []kubeswarmv1alpha1.LogLevel{kubeswarmv1alpha1.LogLevelDebug, kubeswarmv1alpha1.LogLevelInfo, kubeswarmv1alpha1.LogLevelWarn, kubeswarmv1alpha1.LogLevelError} {
				agent := validAgent(uniqueName("log"))
				agent.Spec.Observability = &kubeswarmv1alpha1.AgentObservability{
					Logging: &kubeswarmv1alpha1.AgentLogging{Level: level},
				}
				requireNoError(t, k8sClient.Create(ctx, agent))
			}
		})

		t.Run("should reject invalid logging level", func(t *testing.T) {
			agent := validAgent(uniqueName("bad-log"))
			agent.Spec.Observability = &kubeswarmv1alpha1.AgentObservability{
				Logging: &kubeswarmv1alpha1.AgentLogging{Level: kubeswarmv1alpha1.LogLevel("trace")},
			}
			err := k8sClient.Create(ctx, agent)
			requireError(t, err)
		})
	})

	t.Run("webhook method enum", func(t *testing.T) {
		cleanup(t)

		t.Run("should accept valid methods", func(t *testing.T) {
			for _, method := range []string{"GET", "POST", "PUT", "PATCH"} {
				agent := validAgent(uniqueName("wh"))
				agent.Spec.Tools = &kubeswarmv1alpha1.AgentTools{
					Webhooks: []kubeswarmv1alpha1.WebhookToolSpec{
						{Name: "hook", URL: "https://example.com/hook", Method: method},
					},
				}
				requireNoError(t, k8sClient.Create(ctx, agent))
			}
		})

		t.Run("should reject invalid webhook method", func(t *testing.T) {
			agent := validAgent(uniqueName("bad-wh"))
			agent.Spec.Tools = &kubeswarmv1alpha1.AgentTools{
				Webhooks: []kubeswarmv1alpha1.WebhookToolSpec{
					{Name: "hook", URL: "https://example.com/hook", Method: "DELETE"},
				},
			}
			err := k8sClient.Create(ctx, agent)
			requireError(t, err)
		})
	})

	// -------------------------------------------------------------------------
	// Numeric range validation
	// -------------------------------------------------------------------------

	t.Run("replicas range", func(t *testing.T) {
		cleanup(t)

		t.Run("should reject replicas above 50", func(t *testing.T) {
			over := int32(51)
			agent := validAgent(uniqueName("rep-high"))
			agent.Spec.Runtime = kubeswarmv1alpha1.AgentRuntime{Replicas: &over}
			err := k8sClient.Create(ctx, agent)
			requireError(t, err)
		})

		t.Run("should accept replicas at 0", func(t *testing.T) {
			zero := int32(0)
			agent := validAgent(uniqueName("rep-zero"))
			agent.Spec.Runtime = kubeswarmv1alpha1.AgentRuntime{Replicas: &zero}
			requireNoError(t, k8sClient.Create(ctx, agent))
		})
	})

	t.Run("retries range", func(t *testing.T) {
		cleanup(t)

		t.Run("should reject retries above 100", func(t *testing.T) {
			agent := validAgent(uniqueName("ret-high"))
			agent.Spec.Guardrails = &kubeswarmv1alpha1.AgentGuardrails{
				Limits: &kubeswarmv1alpha1.GuardrailLimits{Retries: 101},
			}
			err := k8sClient.Create(ctx, agent)
			requireError(t, err)
		})

		t.Run("should accept retries at 0", func(t *testing.T) {
			agent := validAgent(uniqueName("ret-zero"))
			agent.Spec.Guardrails = &kubeswarmv1alpha1.AgentGuardrails{
				Limits: &kubeswarmv1alpha1.GuardrailLimits{Retries: 0},
			}
			requireNoError(t, k8sClient.Create(ctx, agent))
		})
	})

	// -------------------------------------------------------------------------
	// CEL mutual exclusivity rules
	// -------------------------------------------------------------------------

	t.Run("MCPToolSpec url/capabilityRef mutual exclusivity", func(t *testing.T) {
		cleanup(t)

		t.Run("should accept url only", func(t *testing.T) {
			agent := validAgent(uniqueName("mcp-url"))
			agent.Spec.Tools = &kubeswarmv1alpha1.AgentTools{
				MCP: []kubeswarmv1alpha1.MCPToolSpec{
					{Name: "srv", URL: "https://example.com/sse"},
				},
			}
			requireNoError(t, k8sClient.Create(ctx, agent))
		})

		t.Run("should accept capabilityRef only", func(t *testing.T) {
			agent := validAgent(uniqueName("mcp-cap"))
			agent.Spec.Tools = &kubeswarmv1alpha1.AgentTools{
				MCP: []kubeswarmv1alpha1.MCPToolSpec{
					{Name: "srv", CapabilityRef: "code-search"},
				},
			}
			requireNoError(t, k8sClient.Create(ctx, agent))
		})

		t.Run("should reject both url and capabilityRef", func(t *testing.T) {
			agent := validAgent(uniqueName("mcp-both"))
			agent.Spec.Tools = &kubeswarmv1alpha1.AgentTools{
				MCP: []kubeswarmv1alpha1.MCPToolSpec{
					{Name: "srv", URL: "https://example.com/sse", CapabilityRef: "code-search"},
				},
			}
			err := k8sClient.Create(ctx, agent)
			requireError(t, err)
		})
	})

	t.Run("AgentConnection agentRef/capabilityRef mutual exclusivity", func(t *testing.T) {
		cleanup(t)

		t.Run("should accept agentRef only", func(t *testing.T) {
			agent := validAgent(uniqueName("conn-agent"))
			agent.Spec.Agents = []kubeswarmv1alpha1.AgentConnection{
				{Name: "helper", AgentRef: &corev1.LocalObjectReference{Name: "other-agent"}},
			}
			requireNoError(t, k8sClient.Create(ctx, agent))
		})

		t.Run("should accept capabilityRef only", func(t *testing.T) {
			agent := validAgent(uniqueName("conn-cap"))
			agent.Spec.Agents = []kubeswarmv1alpha1.AgentConnection{
				{Name: "helper", CapabilityRef: &corev1.LocalObjectReference{Name: "search"}},
			}
			requireNoError(t, k8sClient.Create(ctx, agent))
		})

		t.Run("should reject both agentRef and capabilityRef", func(t *testing.T) {
			agent := validAgent(uniqueName("conn-both"))
			agent.Spec.Agents = []kubeswarmv1alpha1.AgentConnection{
				{
					Name:          "helper",
					AgentRef:      &corev1.LocalObjectReference{Name: "other"},
					CapabilityRef: &corev1.LocalObjectReference{Name: "search"},
				},
			}
			err := k8sClient.Create(ctx, agent)
			requireError(t, err)
		})

		t.Run("should reject neither agentRef nor capabilityRef", func(t *testing.T) {
			agent := validAgent(uniqueName("conn-none"))
			agent.Spec.Agents = []kubeswarmv1alpha1.AgentConnection{
				{Name: "helper"},
			}
			err := k8sClient.Create(ctx, agent)
			requireError(t, err)
		})
	})

	t.Run("MCPServerAuth bearer/mtls mutual exclusivity", func(t *testing.T) {
		cleanup(t)

		t.Run("should accept bearer only", func(t *testing.T) {
			agent := validAgent(uniqueName("auth-bearer"))
			agent.Spec.Tools = &kubeswarmv1alpha1.AgentTools{
				MCP: []kubeswarmv1alpha1.MCPToolSpec{
					{
						Name: "srv", URL: "https://example.com/sse",
						Auth: &kubeswarmv1alpha1.MCPServerAuth{
							Bearer: &kubeswarmv1alpha1.BearerAuth{
								SecretKeyRef: corev1.SecretKeySelector{
									LocalObjectReference: corev1.LocalObjectReference{Name: "tok"},
									Key:                  "key",
								},
							},
						},
					},
				},
			}
			requireNoError(t, k8sClient.Create(ctx, agent))
		})

		t.Run("should reject both bearer and mtls", func(t *testing.T) {
			agent := validAgent(uniqueName("auth-both"))
			agent.Spec.Tools = &kubeswarmv1alpha1.AgentTools{
				MCP: []kubeswarmv1alpha1.MCPToolSpec{
					{
						Name: "srv", URL: "https://example.com/sse",
						Auth: &kubeswarmv1alpha1.MCPServerAuth{
							Bearer: &kubeswarmv1alpha1.BearerAuth{
								SecretKeyRef: corev1.SecretKeySelector{
									LocalObjectReference: corev1.LocalObjectReference{Name: "tok"},
									Key:                  "key",
								},
							},
							MTLS: &kubeswarmv1alpha1.MTLSAuth{
								SecretRef: corev1.LocalObjectReference{Name: "certs"},
							},
						},
					},
				},
			}
			err := k8sClient.Create(ctx, agent)
			requireError(t, err)
		})
	})

	t.Run("AgentPrompt inline/from mutual exclusivity", func(t *testing.T) {
		cleanup(t)

		t.Run("should accept inline only", func(t *testing.T) {
			agent := validAgent(uniqueName("prompt-inline"))
			requireNoError(t, k8sClient.Create(ctx, agent))
		})

		t.Run("should accept from only", func(t *testing.T) {
			agent := &kubeswarmv1alpha1.SwarmAgent{
				ObjectMeta: metav1.ObjectMeta{Name: uniqueName("prompt-from"), Namespace: namespace},
				Spec: kubeswarmv1alpha1.SwarmAgentSpec{
					Model: "claude-sonnet-4-6",
					Prompt: &kubeswarmv1alpha1.AgentPrompt{
						From: &kubeswarmv1alpha1.SystemPromptSource{
							ConfigMapKeyRef: &corev1.ConfigMapKeySelector{
								LocalObjectReference: corev1.LocalObjectReference{Name: "prompts"},
								Key:                  "system.txt",
							},
						},
					},
				},
			}
			requireNoError(t, k8sClient.Create(ctx, agent))
		})

		t.Run("should reject both inline and from", func(t *testing.T) {
			agent := &kubeswarmv1alpha1.SwarmAgent{
				ObjectMeta: metav1.ObjectMeta{Name: uniqueName("prompt-both"), Namespace: namespace},
				Spec: kubeswarmv1alpha1.SwarmAgentSpec{
					Model: "claude-sonnet-4-6",
					Prompt: &kubeswarmv1alpha1.AgentPrompt{
						Inline: "You are a test agent.",
						From: &kubeswarmv1alpha1.SystemPromptSource{
							ConfigMapKeyRef: &corev1.ConfigMapKeySelector{
								LocalObjectReference: corev1.LocalObjectReference{Name: "prompts"},
								Key:                  "system.txt",
							},
						},
					},
				},
			}
			err := k8sClient.Create(ctx, agent)
			requireError(t, err)
		})
	})

	t.Run("SystemPromptSource configMapKeyRef/secretKeyRef mutual exclusivity", func(t *testing.T) {
		cleanup(t)

		t.Run("should reject both configMapKeyRef and secretKeyRef", func(t *testing.T) {
			agent := &kubeswarmv1alpha1.SwarmAgent{
				ObjectMeta: metav1.ObjectMeta{Name: uniqueName("src-both"), Namespace: namespace},
				Spec: kubeswarmv1alpha1.SwarmAgentSpec{
					Model: "claude-sonnet-4-6",
					Prompt: &kubeswarmv1alpha1.AgentPrompt{
						From: &kubeswarmv1alpha1.SystemPromptSource{
							ConfigMapKeyRef: &corev1.ConfigMapKeySelector{
								LocalObjectReference: corev1.LocalObjectReference{Name: "cm"},
								Key:                  "k",
							},
							SecretKeyRef: &corev1.SecretKeySelector{
								LocalObjectReference: corev1.LocalObjectReference{Name: "sec"},
								Key:                  "k",
							},
						},
					},
				},
			}
			err := k8sClient.Create(ctx, agent)
			requireError(t, err)
		})
	})
}
