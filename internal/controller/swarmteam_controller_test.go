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
	"encoding/json"
	"testing"
	"time"

	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kubeswarmv1alpha1 "github.com/kubeswarm/kubeswarm/api/v1alpha1"
)

const baseQueueURL = "redis://redis.default.svc.cluster.local:6379"

func TestSwarmTeamController(t *testing.T) {
	const namespace = "default"
	ctx := context.Background()

	newReconciler := func() *SwarmTeamReconciler {
		return &SwarmTeamReconciler{
			Client:       k8sClient,
			Scheme:       k8sClient.Scheme(),
			TaskQueueURL: baseQueueURL,
		}
	}

	reconcileTeam := func(t *testing.T, name string) (*kubeswarmv1alpha1.SwarmTeam, error) {
		nn := types.NamespacedName{Name: name, Namespace: namespace}
		_, err := newReconciler().Reconcile(ctx, reconcile.Request{NamespacedName: nn})
		if err != nil {
			return nil, err
		}
		team := &kubeswarmv1alpha1.SwarmTeam{}
		requireNoError(t, k8sClient.Get(ctx, nn, team))
		return team, nil
	}

	cleanupTeam := func(name string) {
		team := &kubeswarmv1alpha1.SwarmTeam{}
		nn := types.NamespacedName{Name: name, Namespace: namespace}
		if err := k8sClient.Get(ctx, nn, team); err == nil {
			requireNoError(t, k8sClient.Delete(ctx, team))
		}
	}

	cleanupAgent := func(name string) {
		agent := &kubeswarmv1alpha1.SwarmAgent{}
		nn := types.NamespacedName{Name: name, Namespace: namespace}
		if err := k8sClient.Get(ctx, nn, agent); err == nil {
			requireNoError(t, k8sClient.Delete(ctx, agent))
		}
	}

	// Helper: create a minimal SwarmAgent.
	createAgent := func(t *testing.T, name string) {
		replicas := int32(1)
		requireNoError(t, k8sClient.Create(ctx, &kubeswarmv1alpha1.SwarmAgent{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
			Spec: kubeswarmv1alpha1.SwarmAgentSpec{
				Model:  "claude-haiku-4-5",
				Prompt: &kubeswarmv1alpha1.AgentPrompt{Inline: "You are helpful."},
				Runtime: kubeswarmv1alpha1.AgentRuntime{
					Replicas: &replicas,
				},
			},
		}))
	}

	// -------------------------------------------------------------------------
	// Topology validation (dynamic mode)
	// -------------------------------------------------------------------------

	t.Run("When the team spec has no entry role in dynamic mode", func(t *testing.T) {
		const name = "team-no-entry"
		t.Cleanup(func() { cleanupTeam(name) })

		t.Run("should set Ready=False with reason InvalidTopology", func(t *testing.T) {
			requireNoError(t, k8sClient.Create(ctx, &kubeswarmv1alpha1.SwarmTeam{
				ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
				Spec: kubeswarmv1alpha1.SwarmTeamSpec{
					// No spec.entry and no spec.pipeline - dynamic mode with no entry = invalid
					Roles: []kubeswarmv1alpha1.SwarmTeamRole{
						{Name: "worker", SwarmAgent: "worker-agent"},
					},
				},
			}))

			team, err := reconcileTeam(t, name)
			requireNoError(t, err)

			cond := apimeta.FindStatusCondition(team.Status.Conditions, kubeswarmv1alpha1.ConditionReady)
			requireNotNil(t, cond)
			requireEqual(t, cond.Status, metav1.ConditionFalse)
			requireEqual(t, cond.Reason, "InvalidTopology")
		})
	})

	t.Run("When spec.entry references an unknown role", func(t *testing.T) {
		const name = "team-bad-entry"
		t.Cleanup(func() { cleanupTeam(name) })

		t.Run("should set Ready=False with reason InvalidTopology", func(t *testing.T) {
			requireNoError(t, k8sClient.Create(ctx, &kubeswarmv1alpha1.SwarmTeam{
				ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
				Spec: kubeswarmv1alpha1.SwarmTeamSpec{
					Entry: "ghost-role",
					Roles: []kubeswarmv1alpha1.SwarmTeamRole{
						{Name: "worker", SwarmAgent: "worker-agent"},
					},
				},
			}))

			team, err := reconcileTeam(t, name)
			requireNoError(t, err)

			cond := apimeta.FindStatusCondition(team.Status.Conditions, kubeswarmv1alpha1.ConditionReady)
			requireNotNil(t, cond)
			requireEqual(t, cond.Status, metav1.ConditionFalse)
			requireEqual(t, cond.Reason, "InvalidTopology")
		})
	})

	t.Run("When a delegate target is not a declared role", func(t *testing.T) {
		const name = "team-bad-delegate"
		t.Cleanup(func() { cleanupTeam(name) })

		t.Run("should set Ready=False with reason InvalidTopology", func(t *testing.T) {
			requireNoError(t, k8sClient.Create(ctx, &kubeswarmv1alpha1.SwarmTeam{
				ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
				Spec: kubeswarmv1alpha1.SwarmTeamSpec{
					Entry: "coordinator",
					Roles: []kubeswarmv1alpha1.SwarmTeamRole{
						{Name: "coordinator", SwarmAgent: "coord-agent", CanDelegate: []string{"ghost-role"}},
					},
				},
			}))

			team, err := reconcileTeam(t, name)
			requireNoError(t, err)

			cond := apimeta.FindStatusCondition(team.Status.Conditions, kubeswarmv1alpha1.ConditionReady)
			requireNotNil(t, cond)
			requireEqual(t, cond.Status, metav1.ConditionFalse)
			requireEqual(t, cond.Reason, "InvalidTopology")
			requireContains(t, cond.Message, "ghost-role")
		})
	})

	t.Run("When the delegation graph has a cycle", func(t *testing.T) {
		const name = "team-cycle"
		t.Cleanup(func() { cleanupTeam(name) })

		t.Run("should set Ready=False with reason InvalidTopology", func(t *testing.T) {
			requireNoError(t, k8sClient.Create(ctx, &kubeswarmv1alpha1.SwarmTeam{
				ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
				Spec: kubeswarmv1alpha1.SwarmTeamSpec{
					Entry: "a",
					Roles: []kubeswarmv1alpha1.SwarmTeamRole{
						{Name: "a", SwarmAgent: "agent-a", CanDelegate: []string{"b"}},
						{Name: "b", SwarmAgent: "agent-b", CanDelegate: []string{"a"}},
					},
				},
			}))

			team, err := reconcileTeam(t, name)
			requireNoError(t, err)

			cond := apimeta.FindStatusCondition(team.Status.Conditions, kubeswarmv1alpha1.ConditionReady)
			requireNotNil(t, cond)
			requireEqual(t, cond.Status, metav1.ConditionFalse)
			requireEqual(t, cond.Reason, "InvalidTopology")
		})
	})

	t.Run("When a role's SwarmAgent is missing", func(t *testing.T) {
		const name = "team-missing-agent"
		t.Cleanup(func() { cleanupTeam(name) })

		t.Run("should reconcile without error, recording the role with no replicas", func(t *testing.T) {
			// When an external SwarmAgent doesn't exist yet, the controller treats it
			// as still being created and records the role status without replicas.
			// This is a graceful transient state, not a hard error.
			requireNoError(t, k8sClient.Create(ctx, &kubeswarmv1alpha1.SwarmTeam{
				ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
				Spec: kubeswarmv1alpha1.SwarmTeamSpec{
					Entry: "coordinator",
					Roles: []kubeswarmv1alpha1.SwarmTeamRole{
						{Name: "coordinator", SwarmAgent: "nonexistent-agent"},
					},
				},
			}))

			team, err := reconcileTeam(t, name)
			requireNoError(t, err)
			// The team should have a role status entry for the missing agent.
			requireLen(t, team.Status.Roles, 1)
			requireEqual(t, team.Status.Roles[0].Name, "coordinator")
			requireZero(t, team.Status.Roles[0].ReadyReplicas)
		})
	})

	// -------------------------------------------------------------------------
	// Happy path (dynamic mode)
	// -------------------------------------------------------------------------

	t.Run("When a valid two-role dynamic team is reconciled", func(t *testing.T) {
		const (
			name          = "team-valid"
			coordAgent    = "team-coord-agent"
			reviewerAgent = "team-reviewer-agent"
		)

		setupValidTeam := func(t *testing.T) {
			createAgent(t, coordAgent)
			createAgent(t, reviewerAgent)

			requireNoError(t, k8sClient.Create(ctx, &kubeswarmv1alpha1.SwarmTeam{
				ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
				Spec: kubeswarmv1alpha1.SwarmTeamSpec{
					Entry: "coordinator",
					Roles: []kubeswarmv1alpha1.SwarmTeamRole{
						{Name: "coordinator", SwarmAgent: coordAgent, CanDelegate: []string{"reviewer"}},
						{Name: "reviewer", SwarmAgent: reviewerAgent},
					},
				},
			}))
		}

		cleanupValidTeam := func() {
			cleanupTeam(name)
			cleanupAgent(coordAgent)
			cleanupAgent(reviewerAgent)
		}

		t.Run("should set Ready=True and populate status", func(t *testing.T) {
			setupValidTeam(t)
			t.Cleanup(cleanupValidTeam)

			team, err := reconcileTeam(t, name)
			requireNoError(t, err)

			cond := apimeta.FindStatusCondition(team.Status.Conditions, kubeswarmv1alpha1.ConditionReady)
			requireNotNil(t, cond)
			requireEqual(t, cond.Status, metav1.ConditionTrue)
			requireEqual(t, cond.Reason, "Reconciled")

			requireEqual(t, team.Status.Phase, kubeswarmv1alpha1.SwarmTeamPhaseReady)
			requireEqual(t, team.Status.EntryRole, "coordinator")
			requireLen(t, team.Status.Roles, 2)
		})

		t.Run("should annotate each SwarmAgent with its team queue URL", func(t *testing.T) {
			setupValidTeam(t)
			t.Cleanup(cleanupValidTeam)

			_, err := reconcileTeam(t, name)
			requireNoError(t, err)

			coord := &kubeswarmv1alpha1.SwarmAgent{}
			requireNoError(t, k8sClient.Get(ctx, types.NamespacedName{Name: coordAgent, Namespace: namespace}, coord))
			queueURL := coord.Annotations["kubeswarm/team-queue-url"]
			requireContains(t, queueURL, "stream=")
			requireContains(t, queueURL, "coordinator")

			reviewer := &kubeswarmv1alpha1.SwarmAgent{}
			requireNoError(t, k8sClient.Get(ctx, types.NamespacedName{Name: reviewerAgent, Namespace: namespace}, reviewer))
			requireContains(t, reviewer.Annotations["kubeswarm/team-queue-url"], "reviewer")
		})

		t.Run("should annotate each SwarmAgent with its role name", func(t *testing.T) {
			setupValidTeam(t)
			t.Cleanup(cleanupValidTeam)

			_, err := reconcileTeam(t, name)
			requireNoError(t, err)

			coord := &kubeswarmv1alpha1.SwarmAgent{}
			requireNoError(t, k8sClient.Get(ctx, types.NamespacedName{Name: coordAgent, Namespace: namespace}, coord))
			requireEqual(t, coord.Annotations[kubeswarmv1alpha1.AnnotationTeamRole], "coordinator")
		})

		t.Run("should inject only allowed delegate routes into each agent", func(t *testing.T) {
			setupValidTeam(t)
			t.Cleanup(cleanupValidTeam)

			_, err := reconcileTeam(t, name)
			requireNoError(t, err)

			coord := &kubeswarmv1alpha1.SwarmAgent{}
			requireNoError(t, k8sClient.Get(ctx, types.NamespacedName{Name: coordAgent, Namespace: namespace}, coord))

			routesJSON := coord.Annotations[kubeswarmv1alpha1.AnnotationTeamRoutes]
			requireNotEmpty(t, routesJSON)

			var routes map[string]string
			requireNoError(t, json.Unmarshal([]byte(routesJSON), &routes))

			// coordinator delegates to reviewer only
			_, ok := routes["reviewer"]
			requireTrue(t, ok, "expected key reviewer")
			_, ok = routes["coordinator"]
			requireFalse(t, ok, "unexpected key coordinator")
		})
	})

	// -------------------------------------------------------------------------
	// Queue URL format
	// -------------------------------------------------------------------------

	t.Run("roleQueueURL format", func(t *testing.T) {
		const (
			name      = "team-qurl"
			agentName = "qurl-agent"
		)
		t.Cleanup(func() {
			cleanupTeam(name)
			cleanupAgent(agentName)
		})

		t.Run("should embed namespace.team.role in the stream query parameter", func(t *testing.T) {
			createAgent(t, agentName)
			requireNoError(t, k8sClient.Create(ctx, &kubeswarmv1alpha1.SwarmTeam{
				ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
				Spec: kubeswarmv1alpha1.SwarmTeamSpec{
					Entry: "worker",
					Roles: []kubeswarmv1alpha1.SwarmTeamRole{
						{Name: "worker", SwarmAgent: agentName},
					},
				},
			}))

			_, err := reconcileTeam(t, name)
			requireNoError(t, err)

			agent := &kubeswarmv1alpha1.SwarmAgent{}
			requireNoError(t, k8sClient.Get(ctx, types.NamespacedName{Name: agentName, Namespace: namespace}, agent))

			queueURL := agent.Annotations["kubeswarm/team-queue-url"]
			expectedStream := namespace + "." + name + ".worker"
			requireContains(t, queueURL, "stream="+expectedStream)
		})
	})

	// -------------------------------------------------------------------------
	// Nonexistent resource
	// -------------------------------------------------------------------------

	t.Run("When reconciling a nonexistent SwarmTeam", func(t *testing.T) {
		t.Run("should return without error", func(t *testing.T) {
			_, err := newReconciler().Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: "does-not-exist", Namespace: namespace},
			})
			requireNoError(t, err)
		})
	})

	// -------------------------------------------------------------------------
	// Run retention GC
	// -------------------------------------------------------------------------

	t.Run("run retention GC", func(t *testing.T) {
		const teamName = "gc-test-team"

		makeTeam := func() *kubeswarmv1alpha1.SwarmTeam {
			return &kubeswarmv1alpha1.SwarmTeam{
				ObjectMeta: metav1.ObjectMeta{Name: teamName, Namespace: namespace},
				Spec: kubeswarmv1alpha1.SwarmTeamSpec{
					Roles:    []kubeswarmv1alpha1.SwarmTeamRole{{Name: "worker", Model: "claude-haiku-4-5", Prompt: &kubeswarmv1alpha1.AgentPrompt{Inline: "You are helpful."}}},
					Pipeline: []kubeswarmv1alpha1.SwarmTeamPipelineStep{{Role: "worker"}},
				},
			}
		}

		// makeRun creates an SwarmRun and sets its phase and CompletionTime.
		// age is how long ago the run completed (0 = just now).
		makeRun := func(t *testing.T, name string, phase kubeswarmv1alpha1.SwarmRunPhase, age time.Duration) *kubeswarmv1alpha1.SwarmRun {
			run := &kubeswarmv1alpha1.SwarmRun{
				ObjectMeta: metav1.ObjectMeta{
					Name:      name,
					Namespace: namespace,
					Labels:    map[string]string{kubeswarmv1alpha1.LabelTeam: teamName},
				},
				Spec: kubeswarmv1alpha1.SwarmRunSpec{
					TeamRef:  teamName,
					Pipeline: []kubeswarmv1alpha1.SwarmTeamPipelineStep{{Role: "worker"}},
					Roles:    []kubeswarmv1alpha1.SwarmTeamRole{{Name: "worker", Model: "claude-haiku-4-5"}},
				},
			}
			requireNoError(t, k8sClient.Create(ctx, run))
			completedAt := metav1.NewTime(time.Now().Add(-age))
			run.Status.Phase = phase
			if phase == kubeswarmv1alpha1.SwarmRunPhaseSucceeded || phase == kubeswarmv1alpha1.SwarmRunPhaseFailed {
				run.Status.CompletionTime = &completedAt
			}
			requireNoError(t, k8sClient.Status().Update(ctx, run))
			return run
		}

		runExists := func(name string) bool {
			run := &kubeswarmv1alpha1.SwarmRun{}
			err := k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: namespace}, run)
			return err == nil
		}

		gcCleanup := func() {
			team := &kubeswarmv1alpha1.SwarmTeam{}
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: teamName, Namespace: namespace}, team); err == nil {
				_ = k8sClient.Delete(ctx, team)
			}
			// Clean up any remaining runs.
			var runs kubeswarmv1alpha1.SwarmRunList
			_ = k8sClient.List(ctx, &runs, client.InNamespace(namespace),
				client.MatchingLabels{kubeswarmv1alpha1.LabelTeam: teamName})
			for i := range runs.Items {
				_ = k8sClient.Delete(ctx, &runs.Items[i])
			}
		}

		t.Run("deletes succeeded runs beyond successfulRunsHistoryLimit", func(t *testing.T) {
			t.Cleanup(gcCleanup)

			limit := int32(2)
			team := makeTeam()
			team.Spec.SuccessfulRunsHistoryLimit = &limit
			requireNoError(t, k8sClient.Create(ctx, team))

			// Create 4 succeeded runs; expect only the 2 newest to survive.
			makeRun(t, "gc-suc-1", kubeswarmv1alpha1.SwarmRunPhaseSucceeded, 4*time.Hour)
			makeRun(t, "gc-suc-2", kubeswarmv1alpha1.SwarmRunPhaseSucceeded, 3*time.Hour)
			makeRun(t, "gc-suc-3", kubeswarmv1alpha1.SwarmRunPhaseSucceeded, 2*time.Hour)
			makeRun(t, "gc-suc-4", kubeswarmv1alpha1.SwarmRunPhaseSucceeded, 1*time.Hour)

			_, err := reconcileTeam(t, teamName)
			requireNoError(t, err)

			// 2 oldest should be deleted; 2 newest should survive.
			requireFalse(t, runExists("gc-suc-1"), "gc-suc-1 should be deleted")
			requireFalse(t, runExists("gc-suc-2"), "gc-suc-2 should be deleted")
			requireTrue(t, runExists("gc-suc-3"), "gc-suc-3 should survive")
			requireTrue(t, runExists("gc-suc-4"), "gc-suc-4 should survive")
		})

		t.Run("deletes failed runs beyond failedRunsHistoryLimit", func(t *testing.T) {
			t.Cleanup(gcCleanup)

			limit := int32(1)
			team := makeTeam()
			team.Spec.FailedRunsHistoryLimit = &limit
			requireNoError(t, k8sClient.Create(ctx, team))

			makeRun(t, "gc-fail-1", kubeswarmv1alpha1.SwarmRunPhaseFailed, 3*time.Hour)
			makeRun(t, "gc-fail-2", kubeswarmv1alpha1.SwarmRunPhaseFailed, 2*time.Hour)
			makeRun(t, "gc-fail-3", kubeswarmv1alpha1.SwarmRunPhaseFailed, 1*time.Hour)

			_, err := reconcileTeam(t, teamName)
			requireNoError(t, err)

			requireFalse(t, runExists("gc-fail-1"), "gc-fail-1 should be deleted")
			requireFalse(t, runExists("gc-fail-2"), "gc-fail-2 should be deleted")
			requireTrue(t, runExists("gc-fail-3"), "gc-fail-3 should survive")
		})

		t.Run("deletes completed runs older than runRetainFor regardless of count", func(t *testing.T) {
			t.Cleanup(gcCleanup)

			retain := &metav1.Duration{Duration: 2 * time.Hour}
			team := makeTeam()
			team.Spec.RunRetainFor = retain
			requireNoError(t, k8sClient.Create(ctx, team))

			makeRun(t, "gc-old-1", kubeswarmv1alpha1.SwarmRunPhaseSucceeded, 5*time.Hour)   // too old
			makeRun(t, "gc-old-2", kubeswarmv1alpha1.SwarmRunPhaseFailed, 3*time.Hour)      // too old
			makeRun(t, "gc-young-1", kubeswarmv1alpha1.SwarmRunPhaseSucceeded, 1*time.Hour) // within window
			makeRun(t, "gc-young-2", kubeswarmv1alpha1.SwarmRunPhaseFailed, 30*time.Minute) // within window

			_, err := reconcileTeam(t, teamName)
			requireNoError(t, err)

			requireFalse(t, runExists("gc-old-1"), "gc-old-1 should be deleted")
			requireFalse(t, runExists("gc-old-2"), "gc-old-2 should be deleted")
			requireTrue(t, runExists("gc-young-1"), "gc-young-1 should survive")
			requireTrue(t, runExists("gc-young-2"), "gc-young-2 should survive")
		})

		t.Run("never deletes Running or Pending runs", func(t *testing.T) {
			t.Cleanup(gcCleanup)

			limit := int32(0) // delete all completed runs
			team := makeTeam()
			team.Spec.SuccessfulRunsHistoryLimit = &limit
			team.Spec.FailedRunsHistoryLimit = &limit
			requireNoError(t, k8sClient.Create(ctx, team))

			makeRun(t, "gc-running", kubeswarmv1alpha1.SwarmRunPhaseRunning, 0)
			makeRun(t, "gc-pending", kubeswarmv1alpha1.SwarmRunPhasePending, 0)
			makeRun(t, "gc-done", kubeswarmv1alpha1.SwarmRunPhaseSucceeded, 0)

			_, err := reconcileTeam(t, teamName)
			requireNoError(t, err)

			requireTrue(t, runExists("gc-running"), "gc-running should survive")
			requireTrue(t, runExists("gc-pending"), "gc-pending should survive")
			requireFalse(t, runExists("gc-done"), "gc-done should be deleted")
		})
	})
}
