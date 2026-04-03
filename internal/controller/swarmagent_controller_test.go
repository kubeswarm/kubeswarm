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
	"context"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kubeswarmv1alpha1 "github.com/kubeswarm/kubeswarm/api/v1alpha1"
	pkgflow "github.com/kubeswarm/kubeswarm/pkg/flow"
)

// injFrag is the injection-defence fragment always appended to assembled system prompts.
var injFrag = "\n\n" + strings.TrimSpace(pkgflow.InjectionDefenceFragment)

// ---- Pure function tests ----

var _ = Describe("assembleSystemPrompt", func() {
	It("returns base unchanged when settings slice is empty", func() {
		Expect(assembleSystemPrompt("base prompt", nil, nil)).To(Equal("base prompt" + injFrag))
	})

	It("returns base unchanged when settings have no fragments", func() {
		s := kubeswarmv1alpha1.SwarmSettings{}
		Expect(assembleSystemPrompt("base prompt", []kubeswarmv1alpha1.SwarmSettings{s}, nil)).To(Equal("base prompt" + injFrag))
	})

	It("prepends Persona via deprecated PromptFragments", func() {
		s := kubeswarmv1alpha1.SwarmSettings{
			Spec: kubeswarmv1alpha1.SwarmSettingsSpec{
				PromptFragments: &kubeswarmv1alpha1.PromptFragments{Persona: "You are an expert."},
			},
		}
		result := assembleSystemPrompt("Do the thing.", []kubeswarmv1alpha1.SwarmSettings{s}, nil)
		Expect(result).To(Equal("You are an expert.\n\nDo the thing." + injFrag))
	})

	It("appends OutputRules via deprecated PromptFragments", func() {
		s := kubeswarmv1alpha1.SwarmSettings{
			Spec: kubeswarmv1alpha1.SwarmSettingsSpec{
				PromptFragments: &kubeswarmv1alpha1.PromptFragments{OutputRules: "Always cite sources."},
			},
		}
		result := assembleSystemPrompt("Do the thing.", []kubeswarmv1alpha1.SwarmSettings{s}, nil)
		Expect(result).To(Equal("Do the thing.\n\nAlways cite sources." + injFrag))
	})

	It("prepends Persona and appends OutputRules via deprecated PromptFragments", func() {
		s := kubeswarmv1alpha1.SwarmSettings{
			Spec: kubeswarmv1alpha1.SwarmSettingsSpec{
				PromptFragments: &kubeswarmv1alpha1.PromptFragments{
					Persona:     "You are an expert.",
					OutputRules: "Always cite sources.",
				},
			},
		}
		result := assembleSystemPrompt("Do the thing.", []kubeswarmv1alpha1.SwarmSettings{s}, nil)
		Expect(result).To(Equal("You are an expert.\n\nDo the thing.\n\nAlways cite sources." + injFrag))
	})

	It("applies named Fragments with prepend/append positions", func() {
		s := kubeswarmv1alpha1.SwarmSettings{
			Spec: kubeswarmv1alpha1.SwarmSettingsSpec{
				Fragments: []kubeswarmv1alpha1.PromptFragment{
					{Name: "persona", Text: "You are an expert.", Position: "prepend"},
					{Name: "rules", Text: "Always cite sources.", Position: "append"},
				},
			},
		}
		result := assembleSystemPrompt("Do the thing.", []kubeswarmv1alpha1.SwarmSettings{s}, nil)
		Expect(result).To(Equal("You are an expert.\n\nDo the thing.\n\nAlways cite sources." + injFrag))
	})

	It("last-wins when same fragment name appears in multiple settings", func() {
		s1 := kubeswarmv1alpha1.SwarmSettings{
			Spec: kubeswarmv1alpha1.SwarmSettingsSpec{
				Fragments: []kubeswarmv1alpha1.PromptFragment{
					{Name: "persona", Text: "You are a generalist.", Position: "prepend"},
				},
			},
		}
		s2 := kubeswarmv1alpha1.SwarmSettings{
			Spec: kubeswarmv1alpha1.SwarmSettingsSpec{
				Fragments: []kubeswarmv1alpha1.PromptFragment{
					{Name: "persona", Text: "You are a specialist.", Position: "prepend"},
				},
			},
		}
		result := assembleSystemPrompt("Do the thing.", []kubeswarmv1alpha1.SwarmSettings{s1, s2}, nil)
		Expect(result).To(Equal("You are a specialist.\n\nDo the thing." + injFrag))
	})

	It("appends MCP guidance section when guidance is set", func() {
		servers := []kubeswarmv1alpha1.MCPToolSpec{
			{Name: "web-search", URL: "https://search.example.com/sse", Instructions: "Use for public info only."},
		}
		result := assembleSystemPrompt("Do the thing.", nil, servers)
		Expect(result).To(Equal("Do the thing.\n\n## MCP Tool Guidance\n\n### web-search\nUse for public info only." + injFrag))
	})

	It("omits MCP guidance section when no server has guidance", func() {
		servers := []kubeswarmv1alpha1.MCPToolSpec{
			{Name: "web-search", URL: "https://search.example.com/sse"},
		}
		result := assembleSystemPrompt("Do the thing.", nil, servers)
		Expect(result).To(Equal("Do the thing." + injFrag))
	})
})

var _ = Describe("mergeSettingsEnvVars", func() {
	It("returns nil for empty settings slice", func() {
		Expect(mergeSettingsEnvVars(nil)).To(BeNil())
	})

	It("returns nil for settings with no values", func() {
		Expect(mergeSettingsEnvVars([]kubeswarmv1alpha1.SwarmSettings{{}})).To(BeNil())
	})

	It("includes AGENT_TEMPERATURE when set", func() {
		s := kubeswarmv1alpha1.SwarmSettings{
			Spec: kubeswarmv1alpha1.SwarmSettingsSpec{Temperature: "0.7"},
		}
		envs := mergeSettingsEnvVars([]kubeswarmv1alpha1.SwarmSettings{s})
		Expect(envs).To(ContainElement(corev1.EnvVar{Name: "AGENT_TEMPERATURE", Value: "0.7"}))
	})

	It("includes AGENT_OUTPUT_FORMAT when set", func() {
		s := kubeswarmv1alpha1.SwarmSettings{
			Spec: kubeswarmv1alpha1.SwarmSettingsSpec{OutputFormat: "structured-json"},
		}
		envs := mergeSettingsEnvVars([]kubeswarmv1alpha1.SwarmSettings{s})
		Expect(envs).To(ContainElement(corev1.EnvVar{Name: "AGENT_OUTPUT_FORMAT", Value: "structured-json"}))
	})

	It("includes AGENT_MEMORY_BACKEND when set", func() {
		s := kubeswarmv1alpha1.SwarmSettings{
			Spec: kubeswarmv1alpha1.SwarmSettingsSpec{MemoryBackend: kubeswarmv1alpha1.MemoryBackendRedis},
		}
		envs := mergeSettingsEnvVars([]kubeswarmv1alpha1.SwarmSettings{s})
		Expect(envs).To(ContainElement(corev1.EnvVar{Name: "AGENT_MEMORY_BACKEND", Value: "redis"}))
	})

	It("returns all three vars when all fields are populated", func() {
		s := kubeswarmv1alpha1.SwarmSettings{
			Spec: kubeswarmv1alpha1.SwarmSettingsSpec{
				Temperature:   "0.5",
				OutputFormat:  "markdown",
				MemoryBackend: kubeswarmv1alpha1.MemoryBackendInContext,
			},
		}
		envs := mergeSettingsEnvVars([]kubeswarmv1alpha1.SwarmSettings{s})
		Expect(envs).To(HaveLen(3))
	})

	It("last-wins when same setting appears in multiple objects", func() {
		s1 := kubeswarmv1alpha1.SwarmSettings{
			Spec: kubeswarmv1alpha1.SwarmSettingsSpec{Temperature: "0.3"},
		}
		s2 := kubeswarmv1alpha1.SwarmSettings{
			Spec: kubeswarmv1alpha1.SwarmSettingsSpec{Temperature: "0.9"},
		}
		envs := mergeSettingsEnvVars([]kubeswarmv1alpha1.SwarmSettings{s1, s2})
		Expect(envs).To(ContainElement(corev1.EnvVar{Name: "AGENT_TEMPERATURE", Value: "0.9"}))
	})
})

var _ = Describe("buildVectorStoreMemoryEnvVars", func() {
	It("returns nil for nil config", func() {
		Expect(buildVectorStoreMemoryEnvVars(nil)).To(BeNil())
	})

	It("sets provider, endpoint, and collection", func() {
		vs := &kubeswarmv1alpha1.VectorStoreMemoryConfig{
			Provider:   kubeswarmv1alpha1.VectorStoreProviderQdrant,
			Endpoint:   "http://qdrant:6333",
			Collection: "agent-memories",
		}
		envs := buildVectorStoreMemoryEnvVars(vs)
		Expect(envs).To(ContainElement(corev1.EnvVar{
			Name: "AGENT_MEMORY_VECTOR_STORE_PROVIDER", Value: "qdrant",
		}))
		Expect(envs).To(ContainElement(corev1.EnvVar{
			Name: "AGENT_MEMORY_VECTOR_STORE_ENDPOINT", Value: "http://qdrant:6333",
		}))
		Expect(envs).To(ContainElement(corev1.EnvVar{
			Name: "AGENT_MEMORY_VECTOR_STORE_COLLECTION", Value: "agent-memories",
		}))
	})

	It("injects VECTOR_STORE_API_KEY from SecretRef when set", func() {
		vs := &kubeswarmv1alpha1.VectorStoreMemoryConfig{
			Provider:  kubeswarmv1alpha1.VectorStoreProviderPinecone,
			Endpoint:  "https://pinecone.io",
			SecretRef: &kubeswarmv1alpha1.LocalObjectReference{Name: "vs-secret"},
		}
		envs := buildVectorStoreMemoryEnvVars(vs)
		apiKeyEnv := findEnvVar(envs, "AGENT_MEMORY_VECTOR_STORE_API_KEY")
		Expect(apiKeyEnv).NotTo(BeNil())
		Expect(apiKeyEnv.ValueFrom.SecretKeyRef.Name).To(Equal("vs-secret"))
		Expect(apiKeyEnv.ValueFrom.SecretKeyRef.Key).To(Equal("VECTOR_STORE_API_KEY"))
	})

	It("does not inject API key env when SecretRef is nil", func() {
		vs := &kubeswarmv1alpha1.VectorStoreMemoryConfig{
			Provider: kubeswarmv1alpha1.VectorStoreProviderQdrant,
			Endpoint: "http://qdrant:6333",
		}
		envs := buildVectorStoreMemoryEnvVars(vs)
		Expect(findEnvVar(envs, "AGENT_MEMORY_VECTOR_STORE_API_KEY")).To(BeNil())
	})

	It("includes TTL env when TTLSeconds > 0", func() {
		vs := &kubeswarmv1alpha1.VectorStoreMemoryConfig{
			Provider:   kubeswarmv1alpha1.VectorStoreProviderWeaviate,
			Endpoint:   "http://weaviate:8080",
			TTLSeconds: 3600,
		}
		envs := buildVectorStoreMemoryEnvVars(vs)
		Expect(envs).To(ContainElement(corev1.EnvVar{
			Name: "AGENT_MEMORY_VECTOR_STORE_TTL", Value: "3600",
		}))
	})

	It("does not include TTL env when TTLSeconds is 0", func() {
		vs := &kubeswarmv1alpha1.VectorStoreMemoryConfig{
			Provider: kubeswarmv1alpha1.VectorStoreProviderQdrant,
			Endpoint: "http://qdrant:6333",
		}
		envs := buildVectorStoreMemoryEnvVars(vs)
		Expect(findEnvVar(envs, "AGENT_MEMORY_VECTOR_STORE_TTL")).To(BeNil())
	})
})

// findEnvVar returns the EnvVar with the given name, or nil if not found.
func findEnvVar(envs []corev1.EnvVar, name string) *corev1.EnvVar {
	for i := range envs {
		if envs[i].Name == name {
			return &envs[i]
		}
	}
	return nil
}

// ---- reconcileDailyBudget integration tests ----

var _ = Describe("SwarmAgent Controller — reconcileDailyBudget", func() {
	const namespace = "default"
	ctx := context.Background()

	newAgentReconciler := func() *SwarmAgentReconciler {
		return &SwarmAgentReconciler{
			Client:     k8sClient,
			Scheme:     k8sClient.Scheme(),
			AgentImage: "test-image:latest",
		}
	}

	cleanupAgent := func(name string) {
		agent := &kubeswarmv1alpha1.SwarmAgent{}
		nn := types.NamespacedName{Name: name, Namespace: namespace}
		if err := k8sClient.Get(ctx, nn, agent); err == nil {
			_ = k8sClient.Delete(ctx, agent)
		}
	}

	Context("when no daily limit is configured", func() {
		const agentName = "budget-agent-nolimit"
		AfterEach(func() { cleanupAgent(agentName) })

		It("returns zero requeue duration and clears any stale BudgetExceeded condition", func() {
			agent := &kubeswarmv1alpha1.SwarmAgent{
				ObjectMeta: metav1.ObjectMeta{Name: agentName, Namespace: namespace},
				Spec: kubeswarmv1alpha1.SwarmAgentSpec{
					Model:  "claude-sonnet-4-20250514",
					Prompt: &kubeswarmv1alpha1.AgentPrompt{Inline: "you are a test agent"},
				},
			}
			Expect(k8sClient.Create(ctx, agent)).To(Succeed())
			// Pre-set a stale condition to verify it gets cleared.
			agent.Status.Conditions = []metav1.Condition{{
				Type:               "BudgetExceeded",
				Status:             metav1.ConditionTrue,
				Reason:             "DailyLimitReached",
				Message:            "old",
				LastTransitionTime: metav1.Now(),
			}}
			requeue, err := newAgentReconciler().reconcileDailyBudget(ctx, agent)
			Expect(err).NotTo(HaveOccurred())
			Expect(requeue).To(BeZero())
			Expect(apimeta.IsStatusConditionTrue(agent.Status.Conditions, "BudgetExceeded")).To(BeFalse())
		})
	})

	Context("when daily limit is set and usage is under it", func() {
		const agentName = "budget-agent-under"
		AfterEach(func() { cleanupAgent(agentName) })

		It("returns zero requeue and no BudgetExceeded condition", func() {
			limit := int64(100000)
			agent := &kubeswarmv1alpha1.SwarmAgent{
				ObjectMeta: metav1.ObjectMeta{Name: agentName, Namespace: namespace},
				Spec: kubeswarmv1alpha1.SwarmAgentSpec{
					Model:  "claude-sonnet-4-20250514",
					Prompt: &kubeswarmv1alpha1.AgentPrompt{Inline: "you are a test agent"},
					Guardrails: &kubeswarmv1alpha1.AgentGuardrails{
						Limits: &kubeswarmv1alpha1.GuardrailLimits{DailyTokens: limit},
					},
				},
			}
			Expect(k8sClient.Create(ctx, agent)).To(Succeed())

			requeue, err := newAgentReconciler().reconcileDailyBudget(ctx, agent)
			Expect(err).NotTo(HaveOccurred())
			Expect(requeue).To(BeZero())
			Expect(apimeta.IsStatusConditionTrue(agent.Status.Conditions, "BudgetExceeded")).To(BeFalse())
		})
	})

	Context("when daily limit is exceeded", func() {
		const agentName = "budget-agent-over"
		const runName = "budget-test-run"
		AfterEach(func() {
			cleanupAgent(agentName)
			run := &kubeswarmv1alpha1.SwarmRun{}
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: runName, Namespace: namespace}, run); err == nil {
				_ = k8sClient.Delete(ctx, run)
			}
		})

		It("sets BudgetExceeded condition and returns a requeue duration", func() {
			limit := int64(10) // very low — easily exceeded
			agent := &kubeswarmv1alpha1.SwarmAgent{
				ObjectMeta: metav1.ObjectMeta{Name: agentName, Namespace: namespace},
				Spec: kubeswarmv1alpha1.SwarmAgentSpec{
					Model:  "claude-sonnet-4-20250514",
					Prompt: &kubeswarmv1alpha1.AgentPrompt{Inline: "you are a test agent"},
					Guardrails: &kubeswarmv1alpha1.AgentGuardrails{
						Limits: &kubeswarmv1alpha1.GuardrailLimits{DailyTokens: limit},
					},
				},
			}
			Expect(k8sClient.Create(ctx, agent)).To(Succeed())

			// Create an SwarmRun whose step attributes usage to agentName.
			completionTime := metav1.Now()
			run := &kubeswarmv1alpha1.SwarmRun{
				ObjectMeta: metav1.ObjectMeta{Name: runName, Namespace: namespace},
				Spec: kubeswarmv1alpha1.SwarmRunSpec{
					TeamRef: "budget-test-team",
					Roles: []kubeswarmv1alpha1.SwarmTeamRole{
						{Name: "step1", SwarmAgent: agentName},
					},
					Pipeline: []kubeswarmv1alpha1.SwarmTeamPipelineStep{
						{Role: "step1"},
					},
				},
			}
			Expect(k8sClient.Create(ctx, run)).To(Succeed())
			run.Status.Steps = []kubeswarmv1alpha1.SwarmFlowStepStatus{{
				Name:           "step1",
				Phase:          kubeswarmv1alpha1.SwarmFlowStepPhaseSucceeded,
				CompletionTime: &completionTime,
				TokenUsage: &kubeswarmv1alpha1.TokenUsage{
					InputTokens:  100,
					OutputTokens: 100,
					TotalTokens:  200,
				},
			}}
			Expect(k8sClient.Status().Update(ctx, run)).To(Succeed())

			requeue, err := newAgentReconciler().reconcileDailyBudget(ctx, agent)
			Expect(err).NotTo(HaveOccurred())
			Expect(apimeta.IsStatusConditionTrue(agent.Status.Conditions, "BudgetExceeded")).To(BeTrue())
			Expect(requeue).To(BeNumerically(">", 0))
		})
	})
})

// ---- resolveSystemPrompt integration tests ----

var _ = Describe("SwarmAgent Controller — resolveSystemPrompt", func() {
	const namespace = "default"
	ctx := context.Background()

	newAgentReconciler := func() *SwarmAgentReconciler {
		return &SwarmAgentReconciler{
			Client:     k8sClient,
			Scheme:     k8sClient.Scheme(),
			AgentImage: "test-image:latest",
		}
	}

	cleanupAgent := func(name string) {
		agent := &kubeswarmv1alpha1.SwarmAgent{}
		nn := types.NamespacedName{Name: name, Namespace: namespace}
		if err := k8sClient.Get(ctx, nn, agent); err == nil {
			_ = k8sClient.Delete(ctx, agent)
		}
	}

	Context("when no systemPromptRef is set", func() {
		const agentName = "resolve-prompt-inline"
		AfterEach(func() { cleanupAgent(agentName) })

		It("returns spec.systemPrompt directly", func() {
			agent := &kubeswarmv1alpha1.SwarmAgent{
				ObjectMeta: metav1.ObjectMeta{Name: agentName, Namespace: namespace},
				Spec: kubeswarmv1alpha1.SwarmAgentSpec{
					Model:  "claude-sonnet-4-20250514",
					Prompt: &kubeswarmv1alpha1.AgentPrompt{Inline: "you are an inline agent"},
				},
			}
			Expect(k8sClient.Create(ctx, agent)).To(Succeed())

			prompt, err := newAgentReconciler().resolveSystemPrompt(ctx, agent)
			Expect(err).NotTo(HaveOccurred())
			Expect(prompt).To(Equal("you are an inline agent"))
		})
	})

	Context("when systemPromptRef points to a ConfigMap", func() {
		const agentName = "resolve-prompt-cm"
		const cmName = "resolve-prompt-cm-data"
		AfterEach(func() {
			cleanupAgent(agentName)
			cm := &corev1.ConfigMap{}
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: cmName, Namespace: namespace}, cm); err == nil {
				_ = k8sClient.Delete(ctx, cm)
			}
		})

		It("reads the prompt from the ConfigMap key", func() {
			cm := &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{Name: cmName, Namespace: namespace},
				Data:       map[string]string{"prompt.txt": "you are from a configmap"},
			}
			Expect(k8sClient.Create(ctx, cm)).To(Succeed())

			agent := &kubeswarmv1alpha1.SwarmAgent{
				ObjectMeta: metav1.ObjectMeta{Name: agentName, Namespace: namespace},
				Spec: kubeswarmv1alpha1.SwarmAgentSpec{
					Model: "claude-sonnet-4-20250514",
					Prompt: &kubeswarmv1alpha1.AgentPrompt{
						From: &kubeswarmv1alpha1.SystemPromptSource{
							ConfigMapKeyRef: &corev1.ConfigMapKeySelector{
								LocalObjectReference: corev1.LocalObjectReference{Name: cmName},
								Key:                  "prompt.txt",
							},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, agent)).To(Succeed())

			prompt, err := newAgentReconciler().resolveSystemPrompt(ctx, agent)
			Expect(err).NotTo(HaveOccurred())
			Expect(prompt).To(Equal("you are from a configmap"))
		})

		It("returns error when the ConfigMap key does not exist", func() {
			cm := &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{Name: cmName + "-missing-key", Namespace: namespace},
				Data:       map[string]string{"other.txt": "data"},
			}
			Expect(k8sClient.Create(ctx, cm)).To(Succeed())
			defer func() {
				_ = k8sClient.Delete(ctx, cm)
			}()

			agent := &kubeswarmv1alpha1.SwarmAgent{
				ObjectMeta: metav1.ObjectMeta{Name: agentName + "-mk", Namespace: namespace},
				Spec: kubeswarmv1alpha1.SwarmAgentSpec{
					Model: "claude-sonnet-4-20250514",
					Prompt: &kubeswarmv1alpha1.AgentPrompt{
						From: &kubeswarmv1alpha1.SystemPromptSource{
							ConfigMapKeyRef: &corev1.ConfigMapKeySelector{
								LocalObjectReference: corev1.LocalObjectReference{Name: cm.Name},
								Key:                  "nonexistent",
							},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, agent)).To(Succeed())
			defer func() {
				_ = k8sClient.Delete(ctx, agent)
			}()

			_, err := newAgentReconciler().resolveSystemPrompt(ctx, agent)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("not found"))
		})
	})

	Context("when systemPromptRef points to a Secret", func() {
		const agentName = "resolve-prompt-secret"
		const secretName = "resolve-prompt-secret-data"
		AfterEach(func() {
			cleanupAgent(agentName)
			sec := &corev1.Secret{}
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: secretName, Namespace: namespace}, sec); err == nil {
				_ = k8sClient.Delete(ctx, sec)
			}
		})

		It("reads the prompt from the Secret key", func() {
			sec := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: secretName, Namespace: namespace},
				Data:       map[string][]byte{"prompt": []byte("you are from a secret")},
			}
			Expect(k8sClient.Create(ctx, sec)).To(Succeed())

			agent := &kubeswarmv1alpha1.SwarmAgent{
				ObjectMeta: metav1.ObjectMeta{Name: agentName, Namespace: namespace},
				Spec: kubeswarmv1alpha1.SwarmAgentSpec{
					Model: "claude-sonnet-4-20250514",
					Prompt: &kubeswarmv1alpha1.AgentPrompt{
						From: &kubeswarmv1alpha1.SystemPromptSource{
							SecretKeyRef: &corev1.SecretKeySelector{
								LocalObjectReference: corev1.LocalObjectReference{Name: secretName},
								Key:                  "prompt",
							},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, agent)).To(Succeed())

			prompt, err := newAgentReconciler().resolveSystemPrompt(ctx, agent)
			Expect(err).NotTo(HaveOccurred())
			Expect(prompt).To(Equal("you are from a secret"))
		})
	})
})

// ---- SwarmAgent full Reconcile smoke test ----

var _ = Describe("SwarmAgent Controller — Reconcile", func() {
	const namespace = "default"
	ctx := context.Background()

	newAgentReconciler := func() *SwarmAgentReconciler {
		return &SwarmAgentReconciler{
			Client:     k8sClient,
			Scheme:     k8sClient.Scheme(),
			AgentImage: "test-image:latest",
		}
	}

	It("returns nil for a nonexistent SwarmAgent", func() {
		nn := types.NamespacedName{Name: "does-not-exist-agent", Namespace: namespace}
		_, err := newAgentReconciler().Reconcile(ctx, reconcile.Request{NamespacedName: nn})
		Expect(err).NotTo(HaveOccurred())
	})

	Context("basic reconcile", func() {
		const agentName = "basic-reconcile-agent"
		AfterEach(func() {
			agent := &kubeswarmv1alpha1.SwarmAgent{}
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: agentName, Namespace: namespace}, agent); err == nil {
				_ = k8sClient.Delete(ctx, agent)
			}
		})

		It("creates a Deployment for the agent without error", func() {
			agent := &kubeswarmv1alpha1.SwarmAgent{
				ObjectMeta: metav1.ObjectMeta{Name: agentName, Namespace: namespace},
				Spec: kubeswarmv1alpha1.SwarmAgentSpec{
					Model:  "claude-sonnet-4-20250514",
					Prompt: &kubeswarmv1alpha1.AgentPrompt{Inline: "you are a basic test agent"},
				},
			}
			Expect(k8sClient.Create(ctx, agent)).To(Succeed())

			nn := types.NamespacedName{Name: agentName, Namespace: namespace}
			_, err := newAgentReconciler().Reconcile(ctx, reconcile.Request{NamespacedName: nn})
			Expect(err).NotTo(HaveOccurred())
		})
	})
})
