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
	"slices"
	"strings"
	"testing"

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

func TestAssembleSystemPrompt(t *testing.T) {
	t.Run("returns base unchanged when settings slice is empty", func(t *testing.T) {
		requireEqual(t, assembleSystemPrompt("base prompt", nil, nil), "base prompt"+injFrag)
	})

	t.Run("returns base unchanged when settings have no fragments", func(t *testing.T) {
		s := kubeswarmv1alpha1.SwarmSettings{}
		requireEqual(t, assembleSystemPrompt("base prompt", []kubeswarmv1alpha1.SwarmSettings{s}, nil), "base prompt"+injFrag)
	})

	t.Run("prepends Persona via deprecated PromptFragments", func(t *testing.T) {
		s := kubeswarmv1alpha1.SwarmSettings{
			Spec: kubeswarmv1alpha1.SwarmSettingsSpec{
				PromptFragments: &kubeswarmv1alpha1.PromptFragments{Persona: "You are an expert."},
			},
		}
		result := assembleSystemPrompt("Do the thing.", []kubeswarmv1alpha1.SwarmSettings{s}, nil)
		requireEqual(t, result, "You are an expert.\n\nDo the thing."+injFrag)
	})

	t.Run("appends OutputRules via deprecated PromptFragments", func(t *testing.T) {
		s := kubeswarmv1alpha1.SwarmSettings{
			Spec: kubeswarmv1alpha1.SwarmSettingsSpec{
				PromptFragments: &kubeswarmv1alpha1.PromptFragments{OutputRules: "Always cite sources."},
			},
		}
		result := assembleSystemPrompt("Do the thing.", []kubeswarmv1alpha1.SwarmSettings{s}, nil)
		requireEqual(t, result, "Do the thing.\n\nAlways cite sources."+injFrag)
	})

	t.Run("prepends Persona and appends OutputRules via deprecated PromptFragments", func(t *testing.T) {
		s := kubeswarmv1alpha1.SwarmSettings{
			Spec: kubeswarmv1alpha1.SwarmSettingsSpec{
				PromptFragments: &kubeswarmv1alpha1.PromptFragments{
					Persona:     "You are an expert.",
					OutputRules: "Always cite sources.",
				},
			},
		}
		result := assembleSystemPrompt("Do the thing.", []kubeswarmv1alpha1.SwarmSettings{s}, nil)
		requireEqual(t, result, "You are an expert.\n\nDo the thing.\n\nAlways cite sources."+injFrag)
	})

	t.Run("applies named Fragments with prepend/append positions", func(t *testing.T) {
		s := kubeswarmv1alpha1.SwarmSettings{
			Spec: kubeswarmv1alpha1.SwarmSettingsSpec{
				Fragments: []kubeswarmv1alpha1.PromptFragment{
					{Name: "persona", Text: "You are an expert.", Position: "prepend"},
					{Name: "rules", Text: "Always cite sources.", Position: "append"},
				},
			},
		}
		result := assembleSystemPrompt("Do the thing.", []kubeswarmv1alpha1.SwarmSettings{s}, nil)
		requireEqual(t, result, "You are an expert.\n\nDo the thing.\n\nAlways cite sources."+injFrag)
	})

	t.Run("last-wins when same fragment name appears in multiple settings", func(t *testing.T) {
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
		requireEqual(t, result, "You are a specialist.\n\nDo the thing."+injFrag)
	})

	t.Run("appends MCP guidance section when guidance is set", func(t *testing.T) {
		servers := []kubeswarmv1alpha1.MCPToolSpec{
			{Name: "web-search", URL: "https://search.example.com/sse", Instructions: "Use for public info only."},
		}
		result := assembleSystemPrompt("Do the thing.", nil, servers)
		requireEqual(t, result, "Do the thing.\n\n## MCP Tool Guidance\n\n### web-search\nUse for public info only."+injFrag)
	})

	t.Run("omits MCP guidance section when no server has guidance", func(t *testing.T) {
		servers := []kubeswarmv1alpha1.MCPToolSpec{
			{Name: "web-search", URL: "https://search.example.com/sse"},
		}
		result := assembleSystemPrompt("Do the thing.", nil, servers)
		requireEqual(t, result, "Do the thing."+injFrag)
	})
}

func TestMergeSettingsEnvVars(t *testing.T) {
	t.Run("returns nil for empty settings slice", func(t *testing.T) {
		requireNil(t, mergeSettingsEnvVars(nil))
	})

	t.Run("returns nil for settings with no values", func(t *testing.T) {
		requireNil(t, mergeSettingsEnvVars([]kubeswarmv1alpha1.SwarmSettings{{}}))
	})

	t.Run("includes AGENT_TEMPERATURE when set", func(t *testing.T) {
		s := kubeswarmv1alpha1.SwarmSettings{
			Spec: kubeswarmv1alpha1.SwarmSettingsSpec{Temperature: "0.7"},
		}
		envs := mergeSettingsEnvVars([]kubeswarmv1alpha1.SwarmSettings{s})
		found := slices.Contains(envs, (corev1.EnvVar{Name: "AGENT_TEMPERATURE", Value: "0.7"}))
		requireTrue(t, found, "expected to contain element AGENT_TEMPERATURE=0.7")
	})

	t.Run("includes AGENT_OUTPUT_FORMAT when set", func(t *testing.T) {
		s := kubeswarmv1alpha1.SwarmSettings{
			Spec: kubeswarmv1alpha1.SwarmSettingsSpec{OutputFormat: "structured-json"},
		}
		envs := mergeSettingsEnvVars([]kubeswarmv1alpha1.SwarmSettings{s})
		found := slices.Contains(envs, (corev1.EnvVar{Name: "AGENT_OUTPUT_FORMAT", Value: "structured-json"}))
		requireTrue(t, found, "expected to contain element AGENT_OUTPUT_FORMAT=structured-json")
	})

	t.Run("includes AGENT_MEMORY_BACKEND when set", func(t *testing.T) {
		s := kubeswarmv1alpha1.SwarmSettings{
			Spec: kubeswarmv1alpha1.SwarmSettingsSpec{MemoryBackend: kubeswarmv1alpha1.MemoryBackendRedis},
		}
		envs := mergeSettingsEnvVars([]kubeswarmv1alpha1.SwarmSettings{s})
		found := slices.Contains(envs, (corev1.EnvVar{Name: "AGENT_MEMORY_BACKEND", Value: "redis"}))
		requireTrue(t, found, "expected to contain element AGENT_MEMORY_BACKEND=redis")
	})

	t.Run("returns all three vars when all fields are populated", func(t *testing.T) {
		s := kubeswarmv1alpha1.SwarmSettings{
			Spec: kubeswarmv1alpha1.SwarmSettingsSpec{
				Temperature:   "0.5",
				OutputFormat:  "markdown",
				MemoryBackend: kubeswarmv1alpha1.MemoryBackendInContext,
			},
		}
		envs := mergeSettingsEnvVars([]kubeswarmv1alpha1.SwarmSettings{s})
		requireLen(t, envs, 3)
	})

	t.Run("last-wins when same setting appears in multiple objects", func(t *testing.T) {
		s1 := kubeswarmv1alpha1.SwarmSettings{
			Spec: kubeswarmv1alpha1.SwarmSettingsSpec{Temperature: "0.3"},
		}
		s2 := kubeswarmv1alpha1.SwarmSettings{
			Spec: kubeswarmv1alpha1.SwarmSettingsSpec{Temperature: "0.9"},
		}
		envs := mergeSettingsEnvVars([]kubeswarmv1alpha1.SwarmSettings{s1, s2})
		found := slices.Contains(envs, (corev1.EnvVar{Name: "AGENT_TEMPERATURE", Value: "0.9"}))
		requireTrue(t, found, "expected to contain element AGENT_TEMPERATURE=0.9")
	})
}

func TestBuildVectorStoreMemoryEnvVars(t *testing.T) {
	t.Run("returns nil for nil config", func(t *testing.T) {
		requireNil(t, buildVectorStoreMemoryEnvVars(nil))
	})

	t.Run("sets provider, endpoint, and collection", func(t *testing.T) {
		vs := &kubeswarmv1alpha1.VectorStoreMemoryConfig{
			Provider:   kubeswarmv1alpha1.VectorStoreProviderQdrant,
			Endpoint:   "http://qdrant:6333",
			Collection: "agent-memories",
		}
		envs := buildVectorStoreMemoryEnvVars(vs)

		found := slices.Contains(envs, (corev1.EnvVar{Name: "AGENT_MEMORY_VECTOR_STORE_PROVIDER", Value: "qdrant"}))
		requireTrue(t, found, "expected AGENT_MEMORY_VECTOR_STORE_PROVIDER=qdrant")

		found = slices.Contains(envs, (corev1.EnvVar{Name: "AGENT_MEMORY_VECTOR_STORE_ENDPOINT", Value: "http://qdrant:6333"}))
		requireTrue(t, found, "expected AGENT_MEMORY_VECTOR_STORE_ENDPOINT=http://qdrant:6333")

		found = slices.Contains(envs, (corev1.EnvVar{Name: "AGENT_MEMORY_VECTOR_STORE_COLLECTION", Value: "agent-memories"}))
		requireTrue(t, found, "expected AGENT_MEMORY_VECTOR_STORE_COLLECTION=agent-memories")
	})

	t.Run("injects VECTOR_STORE_API_KEY from SecretRef when set", func(t *testing.T) {
		vs := &kubeswarmv1alpha1.VectorStoreMemoryConfig{
			Provider:  kubeswarmv1alpha1.VectorStoreProviderPgvector,
			Endpoint:  "postgres://pgvector:5432/vectors",
			SecretRef: &corev1.LocalObjectReference{Name: "vs-secret"},
		}
		envs := buildVectorStoreMemoryEnvVars(vs)
		apiKeyEnv := findEnvVar(envs, "AGENT_MEMORY_VECTOR_STORE_API_KEY")
		requireNotNil(t, apiKeyEnv)
		requireEqual(t, apiKeyEnv.ValueFrom.SecretKeyRef.Name, "vs-secret")
		requireEqual(t, apiKeyEnv.ValueFrom.SecretKeyRef.Key, "VECTOR_STORE_API_KEY")
	})

	t.Run("does not inject API key env when SecretRef is nil", func(t *testing.T) {
		vs := &kubeswarmv1alpha1.VectorStoreMemoryConfig{
			Provider: kubeswarmv1alpha1.VectorStoreProviderQdrant,
			Endpoint: "http://qdrant:6333",
		}
		envs := buildVectorStoreMemoryEnvVars(vs)
		requireNil(t, findEnvVar(envs, "AGENT_MEMORY_VECTOR_STORE_API_KEY"))
	})

	t.Run("includes TTL env when TTLSeconds > 0", func(t *testing.T) {
		vs := &kubeswarmv1alpha1.VectorStoreMemoryConfig{
			Provider:   kubeswarmv1alpha1.VectorStoreProviderPgvector,
			Endpoint:   "postgres://pgvector:5432/vectors",
			TTLSeconds: 3600,
		}
		envs := buildVectorStoreMemoryEnvVars(vs)
		found := slices.Contains(envs, (corev1.EnvVar{Name: "AGENT_MEMORY_VECTOR_STORE_TTL", Value: "3600"}))
		requireTrue(t, found, "expected AGENT_MEMORY_VECTOR_STORE_TTL=3600")
	})

	t.Run("does not include TTL env when TTLSeconds is 0", func(t *testing.T) {
		vs := &kubeswarmv1alpha1.VectorStoreMemoryConfig{
			Provider: kubeswarmv1alpha1.VectorStoreProviderQdrant,
			Endpoint: "http://qdrant:6333",
		}
		envs := buildVectorStoreMemoryEnvVars(vs)
		requireNil(t, findEnvVar(envs, "AGENT_MEMORY_VECTOR_STORE_TTL"))
	})
}

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

func TestSwarmAgentControllerReconcileDailyBudget(t *testing.T) {
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

	t.Run("when no daily limit is configured", func(t *testing.T) {
		const agentName = "budget-agent-nolimit"
		t.Cleanup(func() { cleanupAgent(agentName) })

		t.Run("returns zero requeue duration and clears any stale BudgetExceeded condition", func(t *testing.T) {
			agent := &kubeswarmv1alpha1.SwarmAgent{
				ObjectMeta: metav1.ObjectMeta{Name: agentName, Namespace: namespace},
				Spec: kubeswarmv1alpha1.SwarmAgentSpec{
					Model:  "claude-sonnet-4-20250514",
					Prompt: &kubeswarmv1alpha1.AgentPrompt{Inline: "you are a test agent"},
				},
			}
			requireNoError(t, k8sClient.Create(ctx, agent))
			// Pre-set a stale condition to verify it gets cleared.
			agent.Status.Conditions = []metav1.Condition{{
				Type:               kubeswarmv1alpha1.ConditionBudgetExceeded,
				Status:             metav1.ConditionTrue,
				Reason:             "DailyLimitReached",
				Message:            "old",
				LastTransitionTime: metav1.Now(),
			}}
			requeue, err := newAgentReconciler().reconcileDailyBudget(ctx, agent)
			requireNoError(t, err)
			requireZero(t, requeue)
			requireFalse(t, apimeta.IsStatusConditionTrue(agent.Status.Conditions, kubeswarmv1alpha1.ConditionBudgetExceeded))
		})
	})

	t.Run("when daily limit is set and usage is under it", func(t *testing.T) {
		const agentName = "budget-agent-under"
		t.Cleanup(func() { cleanupAgent(agentName) })

		t.Run("returns zero requeue and no BudgetExceeded condition", func(t *testing.T) {
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
			requireNoError(t, k8sClient.Create(ctx, agent))

			requeue, err := newAgentReconciler().reconcileDailyBudget(ctx, agent)
			requireNoError(t, err)
			requireZero(t, requeue)
			requireFalse(t, apimeta.IsStatusConditionTrue(agent.Status.Conditions, kubeswarmv1alpha1.ConditionBudgetExceeded))
		})
	})

	t.Run("when daily limit is exceeded", func(t *testing.T) {
		const agentName = "budget-agent-over"
		const runName = "budget-test-run"
		t.Cleanup(func() {
			cleanupAgent(agentName)
			run := &kubeswarmv1alpha1.SwarmRun{}
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: runName, Namespace: namespace}, run); err == nil {
				_ = k8sClient.Delete(ctx, run)
			}
		})

		t.Run("sets BudgetExceeded condition and returns a requeue duration", func(t *testing.T) {
			limit := int64(10) // very low - easily exceeded
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
			requireNoError(t, k8sClient.Create(ctx, agent))

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
			requireNoError(t, k8sClient.Create(ctx, run))
			run.Status.Steps = []kubeswarmv1alpha1.PipelineStepStatus{{
				Name:           "step1",
				Phase:          kubeswarmv1alpha1.PipelineStepPhaseSucceeded,
				CompletionTime: &completionTime,
				TokenUsage: &kubeswarmv1alpha1.TokenUsage{
					InputTokens:  100,
					OutputTokens: 100,
					TotalTokens:  200,
				},
			}}
			requireNoError(t, k8sClient.Status().Update(ctx, run))

			requeue, err := newAgentReconciler().reconcileDailyBudget(ctx, agent)
			requireNoError(t, err)
			requireTrue(t, apimeta.IsStatusConditionTrue(agent.Status.Conditions, kubeswarmv1alpha1.ConditionBudgetExceeded))
			requireGreaterThan(t, requeue, 0)
		})
	})
}

// ---- resolveSystemPrompt integration tests ----

func TestSwarmAgentControllerResolveSystemPrompt(t *testing.T) {
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

	t.Run("when no systemPromptRef is set", func(t *testing.T) {
		const agentName = "resolve-prompt-inline"
		t.Cleanup(func() { cleanupAgent(agentName) })

		t.Run("returns spec.systemPrompt directly", func(t *testing.T) {
			agent := &kubeswarmv1alpha1.SwarmAgent{
				ObjectMeta: metav1.ObjectMeta{Name: agentName, Namespace: namespace},
				Spec: kubeswarmv1alpha1.SwarmAgentSpec{
					Model:  "claude-sonnet-4-20250514",
					Prompt: &kubeswarmv1alpha1.AgentPrompt{Inline: "you are an inline agent"},
				},
			}
			requireNoError(t, k8sClient.Create(ctx, agent))

			prompt, err := newAgentReconciler().resolveSystemPrompt(ctx, agent)
			requireNoError(t, err)
			requireEqual(t, prompt, "you are an inline agent")
		})
	})

	t.Run("when systemPromptRef points to a ConfigMap", func(t *testing.T) {
		const agentName = "resolve-prompt-cm"
		const cmName = "resolve-prompt-cm-data"
		t.Cleanup(func() {
			cleanupAgent(agentName)
			cm := &corev1.ConfigMap{}
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: cmName, Namespace: namespace}, cm); err == nil {
				_ = k8sClient.Delete(ctx, cm)
			}
		})

		t.Run("reads the prompt from the ConfigMap key", func(t *testing.T) {
			cm := &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{Name: cmName, Namespace: namespace},
				Data:       map[string]string{"prompt.txt": "you are from a configmap"},
			}
			requireNoError(t, k8sClient.Create(ctx, cm))

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
			requireNoError(t, k8sClient.Create(ctx, agent))

			prompt, err := newAgentReconciler().resolveSystemPrompt(ctx, agent)
			requireNoError(t, err)
			requireEqual(t, prompt, "you are from a configmap")
		})

		t.Run("returns error when the ConfigMap key does not exist", func(t *testing.T) {
			cm := &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{Name: cmName + "-missing-key", Namespace: namespace},
				Data:       map[string]string{"other.txt": "data"},
			}
			requireNoError(t, k8sClient.Create(ctx, cm))
			t.Cleanup(func() {
				_ = k8sClient.Delete(ctx, cm)
			})

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
			requireNoError(t, k8sClient.Create(ctx, agent))
			t.Cleanup(func() {
				_ = k8sClient.Delete(ctx, agent)
			})

			_, err := newAgentReconciler().resolveSystemPrompt(ctx, agent)
			requireError(t, err)
			requireContains(t, err.Error(), "not found")
		})
	})

	t.Run("when systemPromptRef points to a Secret", func(t *testing.T) {
		const agentName = "resolve-prompt-secret"
		const secretName = "resolve-prompt-secret-data"
		t.Cleanup(func() {
			cleanupAgent(agentName)
			sec := &corev1.Secret{}
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: secretName, Namespace: namespace}, sec); err == nil {
				_ = k8sClient.Delete(ctx, sec)
			}
		})

		t.Run("reads the prompt from the Secret key", func(t *testing.T) {
			sec := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: secretName, Namespace: namespace},
				Data:       map[string][]byte{"prompt": []byte("you are from a secret")},
			}
			requireNoError(t, k8sClient.Create(ctx, sec))

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
			requireNoError(t, k8sClient.Create(ctx, agent))

			prompt, err := newAgentReconciler().resolveSystemPrompt(ctx, agent)
			requireNoError(t, err)
			requireEqual(t, prompt, "you are from a secret")
		})
	})
}

// ---- SwarmAgent full Reconcile smoke test ----

func TestSwarmAgentControllerReconcile(t *testing.T) {
	const namespace = "default"
	ctx := context.Background()

	newAgentReconciler := func() *SwarmAgentReconciler {
		return &SwarmAgentReconciler{
			Client:     k8sClient,
			Scheme:     k8sClient.Scheme(),
			AgentImage: "test-image:latest",
		}
	}

	t.Run("returns nil for a nonexistent SwarmAgent", func(t *testing.T) {
		nn := types.NamespacedName{Name: "does-not-exist-agent", Namespace: namespace}
		_, err := newAgentReconciler().Reconcile(ctx, reconcile.Request{NamespacedName: nn})
		requireNoError(t, err)
	})

	t.Run("basic reconcile", func(t *testing.T) {
		const agentName = "basic-reconcile-agent"
		t.Cleanup(func() {
			agent := &kubeswarmv1alpha1.SwarmAgent{}
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: agentName, Namespace: namespace}, agent); err == nil {
				_ = k8sClient.Delete(ctx, agent)
			}
		})

		t.Run("creates a Deployment for the agent without error", func(t *testing.T) {
			agent := &kubeswarmv1alpha1.SwarmAgent{
				ObjectMeta: metav1.ObjectMeta{Name: agentName, Namespace: namespace},
				Spec: kubeswarmv1alpha1.SwarmAgentSpec{
					Model:  "claude-sonnet-4-20250514",
					Prompt: &kubeswarmv1alpha1.AgentPrompt{Inline: "you are a basic test agent"},
				},
			}
			requireNoError(t, k8sClient.Create(ctx, agent))

			nn := types.NamespacedName{Name: agentName, Namespace: namespace}
			_, err := newAgentReconciler().Reconcile(ctx, reconcile.Request{NamespacedName: nn})
			requireNoError(t, err)
		})
	})
}
