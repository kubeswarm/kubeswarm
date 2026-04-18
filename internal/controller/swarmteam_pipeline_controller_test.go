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
	"testing"

	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kubeswarmv1alpha1 "github.com/kubeswarm/kubeswarm/api/v1alpha1"
	"github.com/kubeswarm/kubeswarm/pkg/flow"
)

// ---------------------------------------------------------------------------
// SwarmTeam Pipeline Controller - integration tests
// ---------------------------------------------------------------------------

func TestSwarmTeamPipelineController(t *testing.T) {
	const (
		resourceName = "test-pipeline"
		namespace    = "default"
	)

	ctx := context.Background()
	namespacedName := types.NamespacedName{Name: resourceName, Namespace: namespace}

	cleanupTeam := func(t *testing.T) {
		t.Helper()
		team := &kubeswarmv1alpha1.SwarmTeam{}
		if err := k8sClient.Get(ctx, namespacedName, team); err == nil {
			requireNoError(t, k8sClient.Delete(ctx, team))
		}
	}

	t.Run("When a step references an unknown dependency (invalid DAG)", func(t *testing.T) {
		t.Run("should set Ready=False with reason InvalidDAG", func(t *testing.T) {
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
			requireNoError(t, k8sClient.Create(ctx, resource))
			t.Cleanup(func() { cleanupTeam(t) })

			r := &SwarmTeamReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: namespacedName})
			requireNoError(t, err)

			team := &kubeswarmv1alpha1.SwarmTeam{}
			requireNoError(t, k8sClient.Get(ctx, namespacedName, team))

			cond := apimeta.FindStatusCondition(team.Status.Conditions, "Ready")
			if cond == nil {
				t.Fatal("expected Ready condition, got nil")
			}
			requireEqual(t, cond.Status, metav1.ConditionFalse)
			requireEqual(t, cond.Reason, "InvalidDAG")
			requireContains(t, cond.Message, "nonexistent-role")
		})
	})

	t.Run("When the pipeline is valid (no task queue needed for infra reconcile)", func(t *testing.T) {
		t.Run("should set Ready=True with reason Reconciled", func(t *testing.T) {
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
			requireNoError(t, k8sClient.Create(ctx, resource))
			t.Cleanup(func() { cleanupTeam(t) })

			r := &SwarmTeamReconciler{
				Client:    k8sClient,
				Scheme:    k8sClient.Scheme(),
				TaskQueue: nil,
			}
			_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: namespacedName})
			requireNoError(t, err)

			team := &kubeswarmv1alpha1.SwarmTeam{}
			requireNoError(t, k8sClient.Get(ctx, namespacedName, team))

			cond := apimeta.FindStatusCondition(team.Status.Conditions, "Ready")
			if cond == nil {
				t.Fatal("expected Ready condition, got nil")
			}
			requireEqual(t, cond.Status, metav1.ConditionTrue)
			requireEqual(t, cond.Reason, "Reconciled")
		})
	})

	t.Run("When reconciling a nonexistent SwarmTeam", func(t *testing.T) {
		t.Run("should return without error", func(t *testing.T) {
			r := &SwarmTeamReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, err := r.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: "does-not-exist", Namespace: namespace},
			})
			requireNoError(t, err)
		})
	})

	t.Run("When a pipeline has a circular dependency", func(t *testing.T) {
		t.Run("should set Ready=False with reason InvalidDAG mentioning a cycle", func(t *testing.T) {
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
			requireNoError(t, k8sClient.Create(ctx, resource))
			t.Cleanup(func() { cleanupTeam(t) })

			r := &SwarmTeamReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: namespacedName})
			requireNoError(t, err)

			team := &kubeswarmv1alpha1.SwarmTeam{}
			requireNoError(t, k8sClient.Get(ctx, namespacedName, team))

			cond := apimeta.FindStatusCondition(team.Status.Conditions, "Ready")
			if cond == nil {
				t.Fatal("expected Ready condition, got nil")
			}
			requireEqual(t, cond.Status, metav1.ConditionFalse)
			requireEqual(t, cond.Reason, "InvalidDAG")
			requireContains(t, cond.Message, "cycle")
		})
	})
}

// ---- Pure function tests (flow package unit tests) ----

func TestIsTruthy(t *testing.T) {
	for _, tc := range []struct {
		name     string
		input    string
		expected bool
	}{
		{"empty string is falsy", "", false},
		{"false is falsy", "false", false},
		{"FALSE is falsy", "FALSE", false},
		{"0 is falsy", "0", false},
		{"no is falsy", "no", false},
		{"true is truthy", "true", true},
		{"1 is truthy", "1", true},
		{"yes is truthy", "yes", true},
		{"non-empty string is truthy", "some output", true},
		{"whitespace-only false is falsy", "  false  ", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			requireEqual(t, flow.IsTruthy(tc.input), tc.expected)
		})
	}
}

func TestFlowDepsSucceeded(t *testing.T) {
	t.Run("returns true when all deps are Succeeded", func(t *testing.T) {
		statusByName := map[string]*kubeswarmv1alpha1.PipelineStepStatus{
			"a": {Phase: kubeswarmv1alpha1.PipelineStepPhaseSucceeded},
		}
		requireTrue(t, flow.DepsSucceeded([]string{"a"}, statusByName))
	})

	t.Run("returns true when a dep is Skipped", func(t *testing.T) {
		statusByName := map[string]*kubeswarmv1alpha1.PipelineStepStatus{
			"a": {Phase: kubeswarmv1alpha1.PipelineStepPhaseSkipped},
		}
		requireTrue(t, flow.DepsSucceeded([]string{"a"}, statusByName))
	})

	t.Run("returns false when a dep is still Running", func(t *testing.T) {
		statusByName := map[string]*kubeswarmv1alpha1.PipelineStepStatus{
			"a": {Phase: kubeswarmv1alpha1.PipelineStepPhaseRunning},
		}
		requireFalse(t, flow.DepsSucceeded([]string{"a"}, statusByName))
	})

	t.Run("returns false when a dep is missing from status", func(t *testing.T) {
		requireFalse(t, flow.DepsSucceeded([]string{"missing"}, map[string]*kubeswarmv1alpha1.PipelineStepStatus{}))
	})
}

// ---------------------------------------------------------------------------
// SwarmTeam Pipeline Controller - infrastructure reconcile tests
// (Pipeline execution has moved to SwarmRun controller; see swarmrun_controller_test.go)
// ---------------------------------------------------------------------------

func TestSwarmTeamPipelineInfraReconcile(t *testing.T) {
	const namespace = "default"
	ctx := context.Background()

	t.Run("When a pipeline team is created", func(t *testing.T) {
		const teamName = "team-infra-test"
		teamNN := types.NamespacedName{Name: teamName, Namespace: namespace}

		t.Run("should create SwarmAgent CRs for inline roles and set phase to Ready", func(t *testing.T) {
			requireNoError(t, k8sClient.Create(ctx, &kubeswarmv1alpha1.SwarmTeam{
				ObjectMeta: metav1.ObjectMeta{Name: teamName, Namespace: namespace},
				Spec: kubeswarmv1alpha1.SwarmTeamSpec{
					Roles: []kubeswarmv1alpha1.SwarmTeamRole{
						{Name: "worker", Model: "claude-haiku-4-5", Prompt: &kubeswarmv1alpha1.AgentPrompt{Inline: "You are helpful."}},
					},
					Pipeline: []kubeswarmv1alpha1.SwarmTeamPipelineStep{
						{Role: "worker"},
					},
				},
			}))

			t.Cleanup(func() {
				tm := &kubeswarmv1alpha1.SwarmTeam{}
				if err := k8sClient.Get(ctx, teamNN, tm); err == nil {
					requireNoError(t, k8sClient.Delete(ctx, tm))
				}
				agent := &kubeswarmv1alpha1.SwarmAgent{}
				if err := k8sClient.Get(ctx, types.NamespacedName{Name: teamName + "-worker", Namespace: namespace}, agent); err == nil {
					requireNoError(t, k8sClient.Delete(ctx, agent))
				}
			})

			r := &SwarmTeamReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: teamNN})
			requireNoError(t, err)

			tm := &kubeswarmv1alpha1.SwarmTeam{}
			requireNoError(t, k8sClient.Get(ctx, teamNN, tm))
			requireEqual(t, tm.Status.Phase, kubeswarmv1alpha1.SwarmTeamPhaseReady)

			// SwarmAgent CR should be auto-created for the inline role.
			agent := &kubeswarmv1alpha1.SwarmAgent{}
			requireNoError(t, k8sClient.Get(ctx, types.NamespacedName{
				Name:      teamName + "-worker",
				Namespace: namespace,
			}, agent))
			requireEqual(t, agent.Spec.Model, "claude-haiku-4-5")
		})
	})
}
