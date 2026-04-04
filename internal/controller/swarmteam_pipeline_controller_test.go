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

	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	kubeswarmv1alpha1 "github.com/kubeswarm/kubeswarm/api/v1alpha1"
	"github.com/kubeswarm/kubeswarm/pkg/flow"
)

// ---------------------------------------------------------------------------
// SwarmTeam Pipeline Controller — integration tests (replaces SwarmFlowReconciler tests)
// ---------------------------------------------------------------------------

var _ = Describe("SwarmTeam Pipeline Controller", func() {
	const (
		resourceName = "test-pipeline"
		namespace    = "default"
	)

	ctx := context.Background()
	namespacedName := types.NamespacedName{Name: resourceName, Namespace: namespace}

	AfterEach(func() {
		team := &kubeswarmv1alpha1.SwarmTeam{}
		if err := k8sClient.Get(ctx, namespacedName, team); err == nil {
			Expect(k8sClient.Delete(ctx, team)).To(Succeed())
		}
	})

	Context("When a step references an unknown dependency (invalid DAG)", func() {
		BeforeEach(func() {
			By("creating a team pipeline where a step depends on a role that does not exist")
			resource := &kubeswarmv1alpha1.SwarmTeam{
				ObjectMeta: metav1.ObjectMeta{
					Name:      resourceName,
					Namespace: namespace,
				},
				Spec: kubeswarmv1alpha1.SwarmTeamSpec{
					Roles: []kubeswarmv1alpha1.SwarmTeamRole{
						{Name: "summarizer", Model: "claude-haiku-4-5"},
					},
					Pipeline: []kubeswarmv1alpha1.SwarmTeamPipelineStep{
						{
							Role:      "summarizer",
							DependsOn: []string{"nonexistent-role"},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, resource)).To(Succeed())
		})

		It("should set Ready=False with reason InvalidDAG", func() {
			By("running the reconciler")
			r := &SwarmTeamReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: namespacedName})
			Expect(err).NotTo(HaveOccurred())

			By("fetching the updated team status")
			team := &kubeswarmv1alpha1.SwarmTeam{}
			Expect(k8sClient.Get(ctx, namespacedName, team)).To(Succeed())

			cond := apimeta.FindStatusCondition(team.Status.Conditions, "Ready")
			Expect(cond).NotTo(BeNil())
			Expect(cond.Status).To(Equal(metav1.ConditionFalse))
			Expect(cond.Reason).To(Equal("InvalidDAG"))
			Expect(cond.Message).To(ContainSubstring("nonexistent-role"))
		})
	})

	Context("When the pipeline is valid (no task queue needed for infra reconcile)", func() {
		BeforeEach(func() {
			By("creating an SwarmTeam with a valid inline-role pipeline")
			resource := &kubeswarmv1alpha1.SwarmTeam{
				ObjectMeta: metav1.ObjectMeta{
					Name:      resourceName,
					Namespace: namespace,
				},
				Spec: kubeswarmv1alpha1.SwarmTeamSpec{
					Roles: []kubeswarmv1alpha1.SwarmTeamRole{
						{Name: "researcher", Model: "claude-haiku-4-5", Prompt: &kubeswarmv1alpha1.AgentPrompt{Inline: "You are helpful."}},
					},
					Pipeline: []kubeswarmv1alpha1.SwarmTeamPipelineStep{
						{Role: "researcher"},
					},
				},
			}
			Expect(k8sClient.Create(ctx, resource)).To(Succeed())
		})

		It("should set Ready=True with reason Reconciled (infra only, no task queue needed)", func() {
			By("running the reconciler — pipeline infra reconcile does not require a task queue")
			r := &SwarmTeamReconciler{
				Client:    k8sClient,
				Scheme:    k8sClient.Scheme(),
				TaskQueue: nil,
			}
			_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: namespacedName})
			Expect(err).NotTo(HaveOccurred())

			team := &kubeswarmv1alpha1.SwarmTeam{}
			Expect(k8sClient.Get(ctx, namespacedName, team)).To(Succeed())

			cond := apimeta.FindStatusCondition(team.Status.Conditions, "Ready")
			Expect(cond).NotTo(BeNil())
			Expect(cond.Status).To(Equal(metav1.ConditionTrue))
			Expect(cond.Reason).To(Equal("Reconciled"))
		})
	})

	Context("When reconciling a nonexistent SwarmTeam", func() {
		It("should return without error", func() {
			r := &SwarmTeamReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, err := r.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: "does-not-exist", Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())
		})
	})

	Context("When a pipeline has a circular dependency", func() {
		BeforeEach(func() {
			By("creating a pipeline where role A depends on role B which depends on role A")
			resource := &kubeswarmv1alpha1.SwarmTeam{
				ObjectMeta: metav1.ObjectMeta{
					Name:      resourceName,
					Namespace: namespace,
				},
				Spec: kubeswarmv1alpha1.SwarmTeamSpec{
					Roles: []kubeswarmv1alpha1.SwarmTeamRole{
						{Name: "step-a", Model: "claude-haiku-4-5"},
						{Name: "step-b", Model: "claude-haiku-4-5"},
					},
					Pipeline: []kubeswarmv1alpha1.SwarmTeamPipelineStep{
						{Role: "step-a", DependsOn: []string{"step-b"}},
						{Role: "step-b", DependsOn: []string{"step-a"}},
					},
				},
			}
			Expect(k8sClient.Create(ctx, resource)).To(Succeed())
		})

		It("should set Ready=False with reason InvalidDAG mentioning a cycle", func() {
			r := &SwarmTeamReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: namespacedName})
			Expect(err).NotTo(HaveOccurred())

			team := &kubeswarmv1alpha1.SwarmTeam{}
			Expect(k8sClient.Get(ctx, namespacedName, team)).To(Succeed())

			cond := apimeta.FindStatusCondition(team.Status.Conditions, "Ready")
			Expect(cond).NotTo(BeNil())
			Expect(cond.Status).To(Equal(metav1.ConditionFalse))
			Expect(cond.Reason).To(Equal("InvalidDAG"))
			Expect(cond.Message).To(ContainSubstring("cycle"))
		})
	})
})

// ---- Pure function tests (flow package unit tests) ----

var _ = Describe("isTruthy", func() {
	DescribeTable("evaluates strings correctly",
		func(input string, expected bool) {
			Expect(flow.IsTruthy(input)).To(Equal(expected))
		},
		Entry("empty string is falsy", "", false),
		Entry("false is falsy", "false", false),
		Entry("FALSE is falsy", "FALSE", false),
		Entry("0 is falsy", "0", false),
		Entry("no is falsy", "no", false),
		Entry("true is truthy", "true", true),
		Entry("1 is truthy", "1", true),
		Entry("yes is truthy", "yes", true),
		Entry("non-empty string is truthy", "some output", true),
		Entry("whitespace-only false is falsy", "  false  ", false),
	)
})

var _ = Describe("flow package unit tests", func() {
	Describe("DepsSucceeded", func() {
		It("returns true when all deps are Succeeded", func() {
			statusByName := map[string]*kubeswarmv1alpha1.SwarmFlowStepStatus{
				"a": {Phase: kubeswarmv1alpha1.SwarmFlowStepPhaseSucceeded},
			}
			Expect(flow.DepsSucceeded([]string{"a"}, statusByName)).To(BeTrue())
		})

		It("returns true when a dep is Skipped", func() {
			statusByName := map[string]*kubeswarmv1alpha1.SwarmFlowStepStatus{
				"a": {Phase: kubeswarmv1alpha1.SwarmFlowStepPhaseSkipped},
			}
			Expect(flow.DepsSucceeded([]string{"a"}, statusByName)).To(BeTrue())
		})

		It("returns false when a dep is still Running", func() {
			statusByName := map[string]*kubeswarmv1alpha1.SwarmFlowStepStatus{
				"a": {Phase: kubeswarmv1alpha1.SwarmFlowStepPhaseRunning},
			}
			Expect(flow.DepsSucceeded([]string{"a"}, statusByName)).To(BeFalse())
		})

		It("returns false when a dep is missing from status", func() {
			Expect(flow.DepsSucceeded([]string{"missing"}, map[string]*kubeswarmv1alpha1.SwarmFlowStepStatus{})).To(BeFalse())
		})
	})
})

// ---------------------------------------------------------------------------
// SwarmTeam Pipeline Controller — infrastructure reconcile tests
// (Pipeline execution has moved to SwarmRun controller; see swarmrun_controller_test.go)
// ---------------------------------------------------------------------------

var _ = Describe("SwarmTeam Pipeline Controller — infrastructure reconcile", func() {
	const namespace = "default"
	ctx := context.Background()

	Context("When a pipeline team is created", func() {
		const teamName = "team-infra-test"
		teamNN := types.NamespacedName{Name: teamName, Namespace: namespace}

		BeforeEach(func() {
			Expect(k8sClient.Create(ctx, &kubeswarmv1alpha1.SwarmTeam{
				ObjectMeta: metav1.ObjectMeta{Name: teamName, Namespace: namespace},
				Spec: kubeswarmv1alpha1.SwarmTeamSpec{
					Roles: []kubeswarmv1alpha1.SwarmTeamRole{
						{Name: "worker", Model: "claude-haiku-4-5", Prompt: &kubeswarmv1alpha1.AgentPrompt{Inline: "You are helpful."}},
					},
					Pipeline: []kubeswarmv1alpha1.SwarmTeamPipelineStep{
						{Role: "worker"},
					},
				},
			})).To(Succeed())
		})

		AfterEach(func() {
			t := &kubeswarmv1alpha1.SwarmTeam{}
			if err := k8sClient.Get(ctx, teamNN, t); err == nil {
				Expect(k8sClient.Delete(ctx, t)).To(Succeed())
			}
			agent := &kubeswarmv1alpha1.SwarmAgent{}
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: teamName + "-worker", Namespace: namespace}, agent); err == nil {
				Expect(k8sClient.Delete(ctx, agent)).To(Succeed())
			}
		})

		It("should create SwarmAgent CRs for inline roles and set phase to Ready", func() {
			r := &SwarmTeamReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: teamNN})
			Expect(err).NotTo(HaveOccurred())

			t := &kubeswarmv1alpha1.SwarmTeam{}
			Expect(k8sClient.Get(ctx, teamNN, t)).To(Succeed())
			Expect(t.Status.Phase).To(Equal(kubeswarmv1alpha1.SwarmTeamPhaseReady))

			// SwarmAgent CR should be auto-created for the inline role.
			agent := &kubeswarmv1alpha1.SwarmAgent{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      teamName + "-worker",
				Namespace: namespace,
			}, agent)).To(Succeed())
			Expect(agent.Spec.Model).To(Equal("claude-haiku-4-5"))
		})
	})
})
