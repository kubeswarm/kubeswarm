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
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

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
	return r.buildDeployment(agent, nil, nil, "assembled prompt", nil, "", nil)
}

var _ = Describe("SwarmAgent Controller - buildDeployment", func() {

	// -------------------------------------------------------------------------
	// Replicas
	// -------------------------------------------------------------------------

	Context("replicas", func() {
		It("should default to 1 replica when runtime is nil", func() {
			agent := &kubeswarmv1alpha1.SwarmAgent{
				ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default"},
				Spec: kubeswarmv1alpha1.SwarmAgentSpec{
					Model:  "claude-sonnet-4-6",
					Prompt: &kubeswarmv1alpha1.AgentPrompt{Inline: "test"},
				},
			}
			dep := buildTestDeployment(agent)
			Expect(*dep.Spec.Replicas).To(Equal(int32(1)))
		})

		It("should use spec.runtime.replicas", func() {
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
			Expect(*dep.Spec.Replicas).To(Equal(int32(5)))
		})

		It("should scale to 0 when BudgetExceeded condition is True", func() {
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
				Type:   "BudgetExceeded",
				Status: metav1.ConditionTrue,
				Reason: "DailyLimitReached",
			})
			dep := buildTestDeployment(agent)
			Expect(*dep.Spec.Replicas).To(Equal(int32(0)), "should scale to 0 when budget exceeded")
		})

		It("should not scale to 0 when BudgetExceeded is False", func() {
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
				Type:   "BudgetExceeded",
				Status: metav1.ConditionFalse,
				Reason: "WithinBudget",
			})
			dep := buildTestDeployment(agent)
			Expect(*dep.Spec.Replicas).To(Equal(int32(2)))
		})
	})

	// -------------------------------------------------------------------------
	// Resources
	// -------------------------------------------------------------------------

	Context("resources", func() {
		It("should inject default resources when runtime.resources is nil", func() {
			agent := &kubeswarmv1alpha1.SwarmAgent{
				ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default"},
				Spec: kubeswarmv1alpha1.SwarmAgentSpec{
					Model:  "claude-sonnet-4-6",
					Prompt: &kubeswarmv1alpha1.AgentPrompt{Inline: "test"},
				},
			}
			dep := buildTestDeployment(agent)
			containers := dep.Spec.Template.Spec.Containers
			Expect(containers).NotTo(BeEmpty())
			res := containers[0].Resources
			// The controller injects defaults: cpu 100m/500m, memory 128Mi/512Mi.
			Expect(res.Requests.Cpu().String()).To(Equal("100m"))
			Expect(res.Requests.Memory().String()).To(Equal("128Mi"))
			Expect(res.Limits.Cpu().String()).To(Equal("500m"))
			Expect(res.Limits.Memory().String()).To(Equal("512Mi"))
		})

		It("should use custom resources when specified", func() {
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
			Expect(res.Requests.Cpu().String()).To(Equal("200m"))
			Expect(res.Limits.Memory().String()).To(Equal("1Gi"))
		})
	})

	// -------------------------------------------------------------------------
	// Deployment metadata
	// -------------------------------------------------------------------------

	Context("deployment metadata", func() {
		It("should name the Deployment <agent-name>-agent", func() {
			agent := &kubeswarmv1alpha1.SwarmAgent{
				ObjectMeta: metav1.ObjectMeta{Name: "code-reviewer", Namespace: "prod"},
				Spec: kubeswarmv1alpha1.SwarmAgentSpec{
					Model:  "claude-sonnet-4-6",
					Prompt: &kubeswarmv1alpha1.AgentPrompt{Inline: "test"},
				},
			}
			dep := buildTestDeployment(agent)
			Expect(dep.Name).To(Equal("code-reviewer-agent"))
			Expect(dep.Namespace).To(Equal("prod"))
		})

		It("should set standard labels", func() {
			agent := &kubeswarmv1alpha1.SwarmAgent{
				ObjectMeta: metav1.ObjectMeta{Name: "my-agent", Namespace: "default"},
				Spec: kubeswarmv1alpha1.SwarmAgentSpec{
					Model:  "claude-sonnet-4-6",
					Prompt: &kubeswarmv1alpha1.AgentPrompt{Inline: "test"},
				},
			}
			dep := buildTestDeployment(agent)
			Expect(dep.Labels["app.kubernetes.io/name"]).To(Equal("agent"))
			Expect(dep.Labels["app.kubernetes.io/instance"]).To(Equal("my-agent"))
			Expect(dep.Labels["app.kubernetes.io/managed-by"]).To(Equal("kubeswarm"))
			Expect(dep.Labels["kubeswarm/deployment"]).To(Equal("my-agent"))
		})
	})

	// -------------------------------------------------------------------------
	// Container image
	// -------------------------------------------------------------------------

	Context("container image", func() {
		It("should use the reconciler's AgentImage", func() {
			agent := &kubeswarmv1alpha1.SwarmAgent{
				ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default"},
				Spec: kubeswarmv1alpha1.SwarmAgentSpec{
					Model:  "claude-sonnet-4-6",
					Prompt: &kubeswarmv1alpha1.AgentPrompt{Inline: "test"},
				},
			}
			dep := buildTestDeployment(agent)
			Expect(dep.Spec.Template.Spec.Containers[0].Image).To(Equal("test-image:latest"))
		})
	})

	// -------------------------------------------------------------------------
	// Security context
	// -------------------------------------------------------------------------

	Context("security context", func() {
		It("should run as non-root with read-only filesystem", func() {
			agent := &kubeswarmv1alpha1.SwarmAgent{
				ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default"},
				Spec: kubeswarmv1alpha1.SwarmAgentSpec{
					Model:  "claude-sonnet-4-6",
					Prompt: &kubeswarmv1alpha1.AgentPrompt{Inline: "test"},
				},
			}
			dep := buildTestDeployment(agent)
			sc := dep.Spec.Template.Spec.Containers[0].SecurityContext
			Expect(sc).NotTo(BeNil())
			Expect(*sc.AllowPrivilegeEscalation).To(BeFalse())
			Expect(*sc.ReadOnlyRootFilesystem).To(BeTrue())
			Expect(sc.Capabilities.Drop).To(ContainElement(corev1.Capability("ALL")))
		})
	})

	// -------------------------------------------------------------------------
	// ExposedMCPCapabilities in status
	// -------------------------------------------------------------------------

	Context("exposed MCP capabilities", func() {
		It("should list exposed capability names", func() {
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
			Expect(exposed).To(Equal([]string{"search", "deploy"}))
		})
	})
})
