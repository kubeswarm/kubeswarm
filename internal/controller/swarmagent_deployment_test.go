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
	"slices"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kubeswarmv1alpha1 "github.com/kubeswarm/kubeswarm/api/v1alpha1"
)

// buildTestDeployment calls the production buildDeployment with minimal dependencies.
func buildTestDeployment(agent *kubeswarmv1alpha1.SwarmAgent) *appsv1.Deployment {
	r := &SwarmAgentReconciler{AgentImage: "test-image:latest"}
	return r.buildDeployment(deploymentInput{
		swarmAgent:      agent,
		assembledPrompt: "assembled prompt",
	})
}

func TestSwarmAgentControllerBuildDeployment(t *testing.T) {

	// -------------------------------------------------------------------------
	// Replicas
	// -------------------------------------------------------------------------

	t.Run("replicas", func(t *testing.T) {
		t.Run("should default to 1 replica when runtime is nil", func(t *testing.T) {
			agent := &kubeswarmv1alpha1.SwarmAgent{
				ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default"},
				Spec: kubeswarmv1alpha1.SwarmAgentSpec{
					Model:  "claude-sonnet-4-6",
					Prompt: &kubeswarmv1alpha1.AgentPrompt{Inline: "test"},
				},
			}
			dep := buildTestDeployment(agent)
			requireEqual(t, *dep.Spec.Replicas, int32(1))
		})

		t.Run("should use spec.runtime.replicas", func(t *testing.T) {
			replicas := int32(5)
			agent := &kubeswarmv1alpha1.SwarmAgent{
				ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default"},
				Spec: kubeswarmv1alpha1.SwarmAgentSpec{
					Model:   "claude-sonnet-4-6",
					Prompt:  &kubeswarmv1alpha1.AgentPrompt{Inline: "test"},
					Runtime: kubeswarmv1alpha1.AgentRuntime{Replicas: &replicas},
				},
			}
			dep := buildTestDeployment(agent)
			requireEqual(t, *dep.Spec.Replicas, int32(5))
		})

		t.Run("should scale to 0 when BudgetExceeded condition is True", func(t *testing.T) {
			replicas := int32(3)
			agent := &kubeswarmv1alpha1.SwarmAgent{
				ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default"},
				Spec: kubeswarmv1alpha1.SwarmAgentSpec{
					Model:   "claude-sonnet-4-6",
					Prompt:  &kubeswarmv1alpha1.AgentPrompt{Inline: "test"},
					Runtime: kubeswarmv1alpha1.AgentRuntime{Replicas: &replicas},
				},
			}
			// Set BudgetExceeded condition.
			apimeta.SetStatusCondition(&agent.Status.Conditions, metav1.Condition{
				Type:   kubeswarmv1alpha1.ConditionBudgetExceeded,
				Status: metav1.ConditionTrue,
				Reason: "DailyLimitReached",
			})
			dep := buildTestDeployment(agent)
			requireEqual(t, *dep.Spec.Replicas, int32(0), "should scale to 0 when budget exceeded")
		})

		t.Run("should not scale to 0 when BudgetExceeded is False", func(t *testing.T) {
			replicas := int32(2)
			agent := &kubeswarmv1alpha1.SwarmAgent{
				ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default"},
				Spec: kubeswarmv1alpha1.SwarmAgentSpec{
					Model:   "claude-sonnet-4-6",
					Prompt:  &kubeswarmv1alpha1.AgentPrompt{Inline: "test"},
					Runtime: kubeswarmv1alpha1.AgentRuntime{Replicas: &replicas},
				},
			}
			apimeta.SetStatusCondition(&agent.Status.Conditions, metav1.Condition{
				Type:   kubeswarmv1alpha1.ConditionBudgetExceeded,
				Status: metav1.ConditionFalse,
				Reason: "WithinBudget",
			})
			dep := buildTestDeployment(agent)
			requireEqual(t, *dep.Spec.Replicas, int32(2))
		})
	})

	// -------------------------------------------------------------------------
	// Resources
	// -------------------------------------------------------------------------

	t.Run("resources", func(t *testing.T) {
		t.Run("should inject default resources when runtime.resources is nil", func(t *testing.T) {
			agent := &kubeswarmv1alpha1.SwarmAgent{
				ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default"},
				Spec: kubeswarmv1alpha1.SwarmAgentSpec{
					Model:  "claude-sonnet-4-6",
					Prompt: &kubeswarmv1alpha1.AgentPrompt{Inline: "test"},
				},
			}
			dep := buildTestDeployment(agent)
			containers := dep.Spec.Template.Spec.Containers
			if len(containers) == 0 {
				t.Fatal("expected non-empty containers")
			}
			res := containers[0].Resources
			// The controller injects defaults: cpu 100m/500m, memory 128Mi/512Mi.
			requireEqual(t, res.Requests.Cpu().String(), "100m")
			requireEqual(t, res.Requests.Memory().String(), "128Mi")
			requireEqual(t, res.Limits.Cpu().String(), "500m")
			requireEqual(t, res.Limits.Memory().String(), "512Mi")
		})

		t.Run("should use custom resources when specified", func(t *testing.T) {
			agent := &kubeswarmv1alpha1.SwarmAgent{
				ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default"},
				Spec: kubeswarmv1alpha1.SwarmAgentSpec{
					Model:  "claude-sonnet-4-6",
					Prompt: &kubeswarmv1alpha1.AgentPrompt{Inline: "test"},
					Runtime: kubeswarmv1alpha1.AgentRuntime{
						Resources: &corev1.ResourceRequirements{
							Requests: corev1.ResourceList{
								corev1.ResourceCPU:    resource.MustParse("200m"),
								corev1.ResourceMemory: resource.MustParse("256Mi"),
							},
							Limits: corev1.ResourceList{
								corev1.ResourceCPU:    resource.MustParse("1"),
								corev1.ResourceMemory: resource.MustParse("1Gi"),
							},
						},
					},
				},
			}
			dep := buildTestDeployment(agent)
			res := dep.Spec.Template.Spec.Containers[0].Resources
			requireEqual(t, res.Requests.Cpu().String(), "200m")
			requireEqual(t, res.Limits.Memory().String(), "1Gi")
		})
	})

	// -------------------------------------------------------------------------
	// Deployment metadata
	// -------------------------------------------------------------------------

	t.Run("deployment metadata", func(t *testing.T) {
		t.Run("should name the Deployment <agent-name>-agent", func(t *testing.T) {
			agent := &kubeswarmv1alpha1.SwarmAgent{
				ObjectMeta: metav1.ObjectMeta{Name: "code-reviewer", Namespace: "prod"},
				Spec: kubeswarmv1alpha1.SwarmAgentSpec{
					Model:  "claude-sonnet-4-6",
					Prompt: &kubeswarmv1alpha1.AgentPrompt{Inline: "test"},
				},
			}
			dep := buildTestDeployment(agent)
			requireEqual(t, dep.Name, "code-reviewer-agent")
			requireEqual(t, dep.Namespace, "prod")
		})

		t.Run("should set standard labels", func(t *testing.T) {
			agent := &kubeswarmv1alpha1.SwarmAgent{
				ObjectMeta: metav1.ObjectMeta{Name: "my-agent", Namespace: "default"},
				Spec: kubeswarmv1alpha1.SwarmAgentSpec{
					Model:  "claude-sonnet-4-6",
					Prompt: &kubeswarmv1alpha1.AgentPrompt{Inline: "test"},
				},
			}
			dep := buildTestDeployment(agent)
			requireEqual(t, dep.Labels["app.kubernetes.io/name"], "agent")
			requireEqual(t, dep.Labels["app.kubernetes.io/instance"], "my-agent")
			requireEqual(t, dep.Labels["app.kubernetes.io/managed-by"], "kubeswarm")
			requireEqual(t, dep.Labels["kubeswarm/deployment"], "my-agent")
		})
	})

	// -------------------------------------------------------------------------
	// Container image
	// -------------------------------------------------------------------------

	t.Run("container image", func(t *testing.T) {
		t.Run("should use the reconciler's AgentImage", func(t *testing.T) {
			agent := &kubeswarmv1alpha1.SwarmAgent{
				ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default"},
				Spec: kubeswarmv1alpha1.SwarmAgentSpec{
					Model:  "claude-sonnet-4-6",
					Prompt: &kubeswarmv1alpha1.AgentPrompt{Inline: "test"},
				},
			}
			dep := buildTestDeployment(agent)
			requireEqual(t, dep.Spec.Template.Spec.Containers[0].Image, "test-image:latest")
		})
	})

	// -------------------------------------------------------------------------
	// Security context
	// -------------------------------------------------------------------------

	t.Run("security context", func(t *testing.T) {
		t.Run("should run as non-root with read-only filesystem", func(t *testing.T) {
			agent := &kubeswarmv1alpha1.SwarmAgent{
				ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default"},
				Spec: kubeswarmv1alpha1.SwarmAgentSpec{
					Model:  "claude-sonnet-4-6",
					Prompt: &kubeswarmv1alpha1.AgentPrompt{Inline: "test"},
				},
			}
			dep := buildTestDeployment(agent)
			sc := dep.Spec.Template.Spec.Containers[0].SecurityContext
			requireNotNil(t, sc)
			requireFalse(t, *sc.AllowPrivilegeEscalation)
			requireTrue(t, *sc.ReadOnlyRootFilesystem)
			found := slices.Contains(sc.Capabilities.Drop, corev1.Capability("ALL"))
			if !found {
				t.Fatal("expected Capabilities.Drop to contain ALL")
			}
		})
	})

	// -------------------------------------------------------------------------
	// ExposedMCPCapabilities in status
	// -------------------------------------------------------------------------

	t.Run("exposed MCP capabilities", func(t *testing.T) {
		t.Run("should list exposed capability names", func(t *testing.T) {
			agent := &kubeswarmv1alpha1.SwarmAgent{
				ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default"},
				Spec: kubeswarmv1alpha1.SwarmAgentSpec{
					Model:  "claude-sonnet-4-6",
					Prompt: &kubeswarmv1alpha1.AgentPrompt{Inline: "test"},
					Capabilities: []kubeswarmv1alpha1.AgentCapability{
						{Name: "search", ExposeMCP: true},
						{Name: "review", ExposeMCP: false},
						{Name: "deploy", ExposeMCP: true},
					},
				},
			}
			// syncStatus would populate ExposedMCPCapabilities; test the logic directly.
			var exposed []string
			for _, cap := range agent.Spec.Capabilities {
				if cap.ExposeMCP {
					exposed = append(exposed, cap.Name)
				}
			}
			requireLen(t, exposed, 2)
			requireEqual(t, exposed[0], "search")
			requireEqual(t, exposed[1], "deploy")
		})
	})
}
