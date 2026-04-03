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

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	kubeswarmv1alpha1 "github.com/kubeswarm/kubeswarm/api/v1alpha1"
)

var _ = Describe("SwarmAgent CRD validation", func() {
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

	AfterEach(func() {
		// Best-effort cleanup of any agents created during test.
		list := &kubeswarmv1alpha1.SwarmAgentList{}
		if err := k8sClient.List(ctx, list); err == nil {
			for i := range list.Items {
				_ = k8sClient.Delete(ctx, &list.Items[i])
			}
		}
	})

	// -------------------------------------------------------------------------
	// Happy path
	// -------------------------------------------------------------------------

	Context("valid minimal spec", func() {
		It("should accept a SwarmAgent with model + inline prompt", func() {
			agent := validAgent(uniqueName("valid-minimal"))
			Expect(k8sClient.Create(ctx, agent)).To(Succeed())
		})
	})

	Context("valid full spec", func() {
		It("should accept a SwarmAgent with all sections populated", func() {
			replicas := int32(2)
			agent := validAgent(uniqueName("valid-full"))
			agent.Spec.Runtime = &kubeswarmv1alpha1.AgentRuntime{
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
						Default:                "external",
						EnforceInputValidation: true,
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
				Logging: &kubeswarmv1alpha1.AgentLogging{Level: "info", ToolCalls: true},
				Metrics: &kubeswarmv1alpha1.AgentMetrics{Enabled: true},
			}
			Expect(k8sClient.Create(ctx, agent)).To(Succeed())
		})
	})

	// -------------------------------------------------------------------------
	// Enum validation
	// -------------------------------------------------------------------------

	Context("trust level enum", func() {
		It("should accept valid trust levels", func() {
			for _, trust := range []kubeswarmv1alpha1.ToolTrustLevel{"internal", "external", "sandbox"} {
				agent := validAgent(uniqueName("trust"))
				agent.Spec.Tools = &kubeswarmv1alpha1.AgentTools{
					MCP: []kubeswarmv1alpha1.MCPToolSpec{
						{Name: "srv", URL: "https://example.com/sse", Trust: trust},
					},
				}
				Expect(k8sClient.Create(ctx, agent)).To(Succeed(), "trust=%s", trust)
			}
		})

		It("should reject invalid trust level", func() {
			agent := validAgent(uniqueName("bad-trust"))
			agent.Spec.Tools = &kubeswarmv1alpha1.AgentTools{
				MCP: []kubeswarmv1alpha1.MCPToolSpec{
					{Name: "srv", URL: "https://example.com/sse", Trust: "bogus"},
				},
			}
			err := k8sClient.Create(ctx, agent)
			Expect(err).To(HaveOccurred())
		})
	})

	Context("networkPolicy enum", func() {
		It("should accept valid values", func() {
			for _, np := range []kubeswarmv1alpha1.NetworkPolicyMode{"default", "strict", "disabled"} {
				agent := validAgent(uniqueName("np"))
				agent.Spec.NetworkPolicy = np
				Expect(k8sClient.Create(ctx, agent)).To(Succeed(), "networkPolicy=%s", np)
			}
		})

		It("should reject invalid networkPolicy", func() {
			agent := validAgent(uniqueName("bad-np"))
			agent.Spec.NetworkPolicy = "open"
			err := k8sClient.Create(ctx, agent)
			Expect(err).To(HaveOccurred())
		})
	})

	Context("healthCheck type enum", func() {
		It("should accept semantic and ping", func() {
			for _, hcType := range []string{"semantic", "ping"} {
				agent := validAgent(uniqueName("hc"))
				agent.Spec.Observability = &kubeswarmv1alpha1.AgentObservability{
					HealthCheck: &kubeswarmv1alpha1.AgentHealthCheck{Type: hcType},
				}
				Expect(k8sClient.Create(ctx, agent)).To(Succeed(), "type=%s", hcType)
			}
		})

		It("should reject invalid healthCheck type", func() {
			agent := validAgent(uniqueName("bad-hc"))
			agent.Spec.Observability = &kubeswarmv1alpha1.AgentObservability{
				HealthCheck: &kubeswarmv1alpha1.AgentHealthCheck{Type: "tcp"},
			}
			err := k8sClient.Create(ctx, agent)
			Expect(err).To(HaveOccurred())
		})
	})

	Context("logging level enum", func() {
		It("should accept valid levels", func() {
			for _, level := range []string{"debug", "info", "warn", "error"} {
				agent := validAgent(uniqueName("log"))
				agent.Spec.Observability = &kubeswarmv1alpha1.AgentObservability{
					Logging: &kubeswarmv1alpha1.AgentLogging{Level: level},
				}
				Expect(k8sClient.Create(ctx, agent)).To(Succeed(), "level=%s", level)
			}
		})

		It("should reject invalid logging level", func() {
			agent := validAgent(uniqueName("bad-log"))
			agent.Spec.Observability = &kubeswarmv1alpha1.AgentObservability{
				Logging: &kubeswarmv1alpha1.AgentLogging{Level: "trace"},
			}
			err := k8sClient.Create(ctx, agent)
			Expect(err).To(HaveOccurred())
		})
	})

	Context("webhook method enum", func() {
		It("should accept valid methods", func() {
			for _, method := range []string{"GET", "POST", "PUT", "PATCH"} {
				agent := validAgent(uniqueName("wh"))
				agent.Spec.Tools = &kubeswarmv1alpha1.AgentTools{
					Webhooks: []kubeswarmv1alpha1.WebhookToolSpec{
						{Name: "hook", URL: "https://example.com/hook", Method: method},
					},
				}
				Expect(k8sClient.Create(ctx, agent)).To(Succeed(), "method=%s", method)
			}
		})

		It("should reject invalid webhook method", func() {
			agent := validAgent(uniqueName("bad-wh"))
			agent.Spec.Tools = &kubeswarmv1alpha1.AgentTools{
				Webhooks: []kubeswarmv1alpha1.WebhookToolSpec{
					{Name: "hook", URL: "https://example.com/hook", Method: "DELETE"},
				},
			}
			err := k8sClient.Create(ctx, agent)
			Expect(err).To(HaveOccurred())
		})
	})

	// -------------------------------------------------------------------------
	// Numeric range validation
	// -------------------------------------------------------------------------

	Context("replicas range", func() {
		It("should reject replicas above 50", func() {
			over := int32(51)
			agent := validAgent(uniqueName("rep-high"))
			agent.Spec.Runtime = &kubeswarmv1alpha1.AgentRuntime{Replicas: &over}
			err := k8sClient.Create(ctx, agent)
			Expect(err).To(HaveOccurred())
		})

		It("should accept replicas at 0", func() {
			zero := int32(0)
			agent := validAgent(uniqueName("rep-zero"))
			agent.Spec.Runtime = &kubeswarmv1alpha1.AgentRuntime{Replicas: &zero}
			Expect(k8sClient.Create(ctx, agent)).To(Succeed())
		})
	})

	Context("retries range", func() {
		It("should reject retries above 100", func() {
			agent := validAgent(uniqueName("ret-high"))
			agent.Spec.Guardrails = &kubeswarmv1alpha1.AgentGuardrails{
				Limits: &kubeswarmv1alpha1.GuardrailLimits{Retries: 101},
			}
			err := k8sClient.Create(ctx, agent)
			Expect(err).To(HaveOccurred())
		})

		It("should accept retries at 0", func() {
			agent := validAgent(uniqueName("ret-zero"))
			agent.Spec.Guardrails = &kubeswarmv1alpha1.AgentGuardrails{
				Limits: &kubeswarmv1alpha1.GuardrailLimits{Retries: 0},
			}
			Expect(k8sClient.Create(ctx, agent)).To(Succeed())
		})
	})

	// -------------------------------------------------------------------------
	// CEL mutual exclusivity rules
	// -------------------------------------------------------------------------

	Context("MCPToolSpec url/capabilityRef mutual exclusivity", func() {
		It("should accept url only", func() {
			agent := validAgent(uniqueName("mcp-url"))
			agent.Spec.Tools = &kubeswarmv1alpha1.AgentTools{
				MCP: []kubeswarmv1alpha1.MCPToolSpec{
					{Name: "srv", URL: "https://example.com/sse"},
				},
			}
			Expect(k8sClient.Create(ctx, agent)).To(Succeed())
		})

		It("should accept capabilityRef only", func() {
			agent := validAgent(uniqueName("mcp-cap"))
			agent.Spec.Tools = &kubeswarmv1alpha1.AgentTools{
				MCP: []kubeswarmv1alpha1.MCPToolSpec{
					{Name: "srv", CapabilityRef: "code-search"},
				},
			}
			Expect(k8sClient.Create(ctx, agent)).To(Succeed())
		})

		It("should reject both url and capabilityRef", func() {
			agent := validAgent(uniqueName("mcp-both"))
			agent.Spec.Tools = &kubeswarmv1alpha1.AgentTools{
				MCP: []kubeswarmv1alpha1.MCPToolSpec{
					{Name: "srv", URL: "https://example.com/sse", CapabilityRef: "code-search"},
				},
			}
			err := k8sClient.Create(ctx, agent)
			Expect(err).To(HaveOccurred())
		})
	})

	Context("AgentConnection agentRef/capabilityRef mutual exclusivity", func() {
		It("should accept agentRef only", func() {
			agent := validAgent(uniqueName("conn-agent"))
			agent.Spec.Agents = []kubeswarmv1alpha1.AgentConnection{
				{Name: "helper", AgentRef: &kubeswarmv1alpha1.LocalObjectReference{Name: "other-agent"}},
			}
			Expect(k8sClient.Create(ctx, agent)).To(Succeed())
		})

		It("should accept capabilityRef only", func() {
			agent := validAgent(uniqueName("conn-cap"))
			agent.Spec.Agents = []kubeswarmv1alpha1.AgentConnection{
				{Name: "helper", CapabilityRef: &kubeswarmv1alpha1.LocalObjectReference{Name: "search"}},
			}
			Expect(k8sClient.Create(ctx, agent)).To(Succeed())
		})

		It("should reject both agentRef and capabilityRef", func() {
			agent := validAgent(uniqueName("conn-both"))
			agent.Spec.Agents = []kubeswarmv1alpha1.AgentConnection{
				{
					Name:          "helper",
					AgentRef:      &kubeswarmv1alpha1.LocalObjectReference{Name: "other"},
					CapabilityRef: &kubeswarmv1alpha1.LocalObjectReference{Name: "search"},
				},
			}
			err := k8sClient.Create(ctx, agent)
			Expect(err).To(HaveOccurred())
		})

		It("should reject neither agentRef nor capabilityRef", func() {
			agent := validAgent(uniqueName("conn-none"))
			agent.Spec.Agents = []kubeswarmv1alpha1.AgentConnection{
				{Name: "helper"},
			}
			err := k8sClient.Create(ctx, agent)
			Expect(err).To(HaveOccurred())
		})
	})

	Context("MCPServerAuth bearer/mtls mutual exclusivity", func() {
		It("should accept bearer only", func() {
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
			Expect(k8sClient.Create(ctx, agent)).To(Succeed())
		})

		It("should reject both bearer and mtls", func() {
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
								SecretRef: kubeswarmv1alpha1.LocalObjectReference{Name: "certs"},
							},
						},
					},
				},
			}
			err := k8sClient.Create(ctx, agent)
			Expect(err).To(HaveOccurred())
		})
	})

	Context("AgentPrompt inline/from mutual exclusivity", func() {
		It("should accept inline only", func() {
			agent := validAgent(uniqueName("prompt-inline"))
			Expect(k8sClient.Create(ctx, agent)).To(Succeed())
		})

		It("should accept from only", func() {
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
			Expect(k8sClient.Create(ctx, agent)).To(Succeed())
		})

		It("should reject both inline and from", func() {
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
			Expect(err).To(HaveOccurred())
		})
	})

	Context("SystemPromptSource configMapKeyRef/secretKeyRef mutual exclusivity", func() {
		It("should reject both configMapKeyRef and secretKeyRef", func() {
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
			Expect(err).To(HaveOccurred())
		})
	})
})
