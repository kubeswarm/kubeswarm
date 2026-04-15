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

package webhook

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kubeswarmv1alpha1 "github.com/kubeswarm/kubeswarm/api/v1alpha1"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func advisorFakeClient(objs ...client.Object) client.Client {
	s := runtime.NewScheme()
	_ = kubeswarmv1alpha1.AddToScheme(s)
	return fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(objs...).
		Build()
}

func makeAdvisorAgent(name, ns string) *kubeswarmv1alpha1.SwarmAgent {
	return &kubeswarmv1alpha1.SwarmAgent{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: kubeswarmv1alpha1.SwarmAgentSpec{
			Model:  "claude-opus-4-6",
			Prompt: &kubeswarmv1alpha1.AgentPrompt{Inline: "You are an advisor."},
		},
	}
}

func makeAdvisorAgentWithAdvisors(name, ns string) *kubeswarmv1alpha1.SwarmAgent {
	agent := makeAdvisorAgent(name, ns)
	agent.Spec.Agents = []kubeswarmv1alpha1.AgentConnection{
		{
			Name:     "nested-advisor",
			AgentRef: &corev1.LocalObjectReference{Name: "some-other"},
			Role:     kubeswarmv1alpha1.AgentConnectionRoleAdvisor,
			ContextPropagation: &kubeswarmv1alpha1.ContextPropagationConfig{
				RecentMessages: 10,
			},
		},
	}
	return agent
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestValidateAdvisorConnections(t *testing.T) {
	ctx := context.Background()

	cases := []struct {
		name      string
		agent     *kubeswarmv1alpha1.SwarmAgent
		objects   []client.Object
		wantError bool
		errorMsg  string
	}{
		{
			name: "advisor with agentRef allowed",
			agent: func() *kubeswarmv1alpha1.SwarmAgent {
				a := makeTestAgent()
				a.Spec.Agents = []kubeswarmv1alpha1.AgentConnection{
					{
						Name:     "architect",
						AgentRef: &corev1.LocalObjectReference{Name: "senior-arch"},
						Role:     kubeswarmv1alpha1.AgentConnectionRoleAdvisor,
						ContextPropagation: &kubeswarmv1alpha1.ContextPropagationConfig{
							RecentMessages: 20,
						},
					},
				}
				return a
			}(),
			objects:   []client.Object{makeAdvisorAgent("senior-arch", "default")},
			wantError: false,
		},
		{
			name: "advisor with capabilityRef rejected",
			agent: func() *kubeswarmv1alpha1.SwarmAgent {
				a := makeTestAgent()
				a.Spec.Agents = []kubeswarmv1alpha1.AgentConnection{
					{
						Name:          "architect",
						CapabilityRef: &corev1.LocalObjectReference{Name: "cap-arch"},
						Role:          kubeswarmv1alpha1.AgentConnectionRoleAdvisor,
						ContextPropagation: &kubeswarmv1alpha1.ContextPropagationConfig{
							RecentMessages: 20,
						},
					},
				}
				return a
			}(),
			wantError: true,
			errorMsg:  "advisor role requires agentRef",
		},
		{
			name: "non-advisor with contextPropagation rejected",
			agent: func() *kubeswarmv1alpha1.SwarmAgent {
				a := makeTestAgent()
				a.Spec.Agents = []kubeswarmv1alpha1.AgentConnection{
					{
						Name:     "formatter",
						AgentRef: &corev1.LocalObjectReference{Name: "fmt"},
						Role:     kubeswarmv1alpha1.AgentConnectionRoleTool,
						ContextPropagation: &kubeswarmv1alpha1.ContextPropagationConfig{
							RecentMessages: 20,
						},
					},
				}
				return a
			}(),
			wantError: true,
			errorMsg:  "contextPropagation is only valid when role is advisor",
		},
		{
			name: "self-reference rejected",
			agent: func() *kubeswarmv1alpha1.SwarmAgent {
				a := makeTestAgent() // name: "test"
				a.Spec.Agents = []kubeswarmv1alpha1.AgentConnection{
					{
						Name:     "self",
						AgentRef: &corev1.LocalObjectReference{Name: "test"},
						Role:     kubeswarmv1alpha1.AgentConnectionRoleAdvisor,
						ContextPropagation: &kubeswarmv1alpha1.ContextPropagationConfig{
							RecentMessages: 10,
						},
					},
				}
				return a
			}(),
			wantError: true,
			errorMsg:  "self-reference",
		},
		{
			name: "tool name collision with MCP server rejected",
			agent: func() *kubeswarmv1alpha1.SwarmAgent {
				a := makeTestAgent()
				a.Spec.Tools = &kubeswarmv1alpha1.AgentTools{
					MCP: []kubeswarmv1alpha1.MCPToolSpec{
						{Name: "consult_architect", URL: "http://mcp.svc:8080/sse"},
					},
				}
				a.Spec.Agents = []kubeswarmv1alpha1.AgentConnection{
					{
						Name:     "architect",
						AgentRef: &corev1.LocalObjectReference{Name: "senior-arch"},
						Role:     kubeswarmv1alpha1.AgentConnectionRoleAdvisor,
						ContextPropagation: &kubeswarmv1alpha1.ContextPropagationConfig{
							RecentMessages: 20,
						},
					},
				}
				return a
			}(),
			objects:   []client.Object{makeAdvisorAgent("senior-arch", "default")},
			wantError: true,
			errorMsg:  "conflicts with",
		},
		{
			name: "tool name collision with webhook tool rejected",
			agent: func() *kubeswarmv1alpha1.SwarmAgent {
				a := makeTestAgent()
				a.Spec.Tools = &kubeswarmv1alpha1.AgentTools{
					Webhooks: []kubeswarmv1alpha1.WebhookToolSpec{
						{Name: "consult_architect", URL: "http://hook.svc/notify"},
					},
				}
				a.Spec.Agents = []kubeswarmv1alpha1.AgentConnection{
					{
						Name:     "architect",
						AgentRef: &corev1.LocalObjectReference{Name: "senior-arch"},
						Role:     kubeswarmv1alpha1.AgentConnectionRoleAdvisor,
						ContextPropagation: &kubeswarmv1alpha1.ContextPropagationConfig{
							RecentMessages: 20,
						},
					},
				}
				return a
			}(),
			objects:   []client.Object{makeAdvisorAgent("senior-arch", "default")},
			wantError: true,
			errorMsg:  "conflicts with",
		},
		{
			name: "tool name collision between two advisors rejected",
			agent: func() *kubeswarmv1alpha1.SwarmAgent {
				a := makeTestAgent()
				a.Spec.Agents = []kubeswarmv1alpha1.AgentConnection{
					{
						Name:     "arch1",
						AgentRef: &corev1.LocalObjectReference{Name: "senior-arch"},
						Role:     kubeswarmv1alpha1.AgentConnectionRoleAdvisor,
						ContextPropagation: &kubeswarmv1alpha1.ContextPropagationConfig{
							ToolName: "ask_expert",
						},
					},
					{
						Name:     "arch2",
						AgentRef: &corev1.LocalObjectReference{Name: "junior-arch"},
						Role:     kubeswarmv1alpha1.AgentConnectionRoleAdvisor,
						ContextPropagation: &kubeswarmv1alpha1.ContextPropagationConfig{
							ToolName: "ask_expert",
						},
					},
				}
				return a
			}(),
			objects: []client.Object{
				makeAdvisorAgent("senior-arch", "default"),
				makeAdvisorAgent("junior-arch", "default"),
			},
			wantError: true,
			errorMsg:  "conflicts with",
		},
		{
			name: "advisor referencing non-existent agent rejected",
			agent: func() *kubeswarmv1alpha1.SwarmAgent {
				a := makeTestAgent()
				a.Spec.Agents = []kubeswarmv1alpha1.AgentConnection{
					{
						Name:     "ghost",
						AgentRef: &corev1.LocalObjectReference{Name: "does-not-exist"},
						Role:     kubeswarmv1alpha1.AgentConnectionRoleAdvisor,
						ContextPropagation: &kubeswarmv1alpha1.ContextPropagationConfig{
							RecentMessages: 10,
						},
					},
				}
				return a
			}(),
			wantError: true,
			errorMsg:  "not found",
		},
		{
			name: "advisor depth greater than 1 rejected",
			agent: func() *kubeswarmv1alpha1.SwarmAgent {
				a := makeTestAgent()
				a.Spec.Agents = []kubeswarmv1alpha1.AgentConnection{
					{
						Name:     "nested",
						AgentRef: &corev1.LocalObjectReference{Name: "advisor-with-advisors"},
						Role:     kubeswarmv1alpha1.AgentConnectionRoleAdvisor,
						ContextPropagation: &kubeswarmv1alpha1.ContextPropagationConfig{
							RecentMessages: 10,
						},
					},
				}
				return a
			}(),
			objects:   []client.Object{makeAdvisorAgentWithAdvisors("advisor-with-advisors", "default")},
			wantError: true,
			errorMsg:  "has advisor connections",
		},
		{
			name: "multiple distinct advisors allowed",
			agent: func() *kubeswarmv1alpha1.SwarmAgent {
				a := makeTestAgent()
				a.Spec.Agents = []kubeswarmv1alpha1.AgentConnection{
					{
						Name:     "architect",
						AgentRef: &corev1.LocalObjectReference{Name: "senior-arch"},
						Role:     kubeswarmv1alpha1.AgentConnectionRoleAdvisor,
						ContextPropagation: &kubeswarmv1alpha1.ContextPropagationConfig{
							RecentMessages: 30,
						},
					},
					{
						Name:     "security",
						AgentRef: &corev1.LocalObjectReference{Name: "sec-reviewer"},
						Role:     kubeswarmv1alpha1.AgentConnectionRoleAdvisor,
						ContextPropagation: &kubeswarmv1alpha1.ContextPropagationConfig{
							RecentMessages:  5,
							MaxCallsPerTask: 2,
							ToolName:        "review_security",
						},
					},
				}
				return a
			}(),
			objects: []client.Object{
				makeAdvisorAgent("senior-arch", "default"),
				makeAdvisorAgent("sec-reviewer", "default"),
			},
			wantError: false,
		},
		{
			name: "role tool with no contextPropagation allowed",
			agent: func() *kubeswarmv1alpha1.SwarmAgent {
				a := makeTestAgent()
				a.Spec.Agents = []kubeswarmv1alpha1.AgentConnection{
					{
						Name:     "formatter",
						AgentRef: &corev1.LocalObjectReference{Name: "fmt"},
						Role:     kubeswarmv1alpha1.AgentConnectionRoleTool,
					},
				}
				return a
			}(),
			wantError: false,
		},
		{
			name: "no agents at all allowed",
			agent: func() *kubeswarmv1alpha1.SwarmAgent {
				return makeTestAgent()
			}(),
			wantError: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := advisorFakeClient(tc.objects...)
			errs := ValidateAdvisorConnections(ctx, c, tc.agent)
			if tc.wantError {
				if len(errs) == 0 {
					t.Fatal("expected validation error, got none")
				}
				// Check that at least one error contains the expected message.
				found := false
				for _, e := range errs {
					if contains(e.Error(), tc.errorMsg) {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("expected error containing %q, got: %v", tc.errorMsg, errs)
				}
			} else {
				if len(errs) > 0 {
					t.Fatalf("expected no errors, got: %v", errs)
				}
			}
		})
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && searchString(s, substr)
}

func searchString(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
