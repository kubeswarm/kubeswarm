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
	"time"

	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	kubeswarmv1alpha1 "github.com/kubeswarm/kubeswarm/api/v1alpha1"
)

const baseQueueURL = "redis://redis.default.svc.cluster.local:6379"

var _ = Describe("SwarmTeam Controller", func() {
	const namespace = "default"
	ctx := context.Background()

	newReconciler := func() *SwarmTeamReconciler {
		return &SwarmTeamReconciler{
			Client:       k8sClient,
			Scheme:       k8sClient.Scheme(),
			TaskQueueURL: baseQueueURL,
		}
	}

	reconcileTeam := func(name string) (*kubeswarmv1alpha1.SwarmTeam, error) {
		nn := types.NamespacedName{Name: name, Namespace: namespace}
		_, err := newReconciler().Reconcile(ctx, reconcile.Request{NamespacedName: nn})
		if err != nil {
			return nil, err
		}
		team := &kubeswarmv1alpha1.SwarmTeam{}
		Expect(k8sClient.Get(ctx, nn, team)).To(Succeed())
		return team, nil
	}

	cleanupTeam := func(name string) {
		team := &kubeswarmv1alpha1.SwarmTeam{}
		nn := types.NamespacedName{Name: name, Namespace: namespace}
		if err := k8sClient.Get(ctx, nn, team); err == nil {
			Expect(k8sClient.Delete(ctx, team)).To(Succeed())
		}
	}

	cleanupAgent := func(name string) {
		agent := &kubeswarmv1alpha1.SwarmAgent{}
		nn := types.NamespacedName{Name: name, Namespace: namespace}
		if err := k8sClient.Get(ctx, nn, agent); err == nil {
			Expect(k8sClient.Delete(ctx, agent)).To(Succeed())
		}
	}

	// Helper: create a minimal SwarmAgent.
	createAgent := func(name string) {
		replicas := int32(1)
		Expect(k8sClient.Create(ctx, &kubeswarmv1alpha1.SwarmAgent{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
			Spec: kubeswarmv1alpha1.SwarmAgentSpec{
				Model:  "claude-haiku-4-5",
				Prompt: &kubeswarmv1alpha1.AgentPrompt{Inline: "You are helpful."},
				Runtime: kubeswarmv1alpha1.AgentRuntime{
					Replicas: &replicas,
				},
			},
		})).To(Succeed())
	}

	// -------------------------------------------------------------------------
	// Topology validation (dynamic mode)
	// -------------------------------------------------------------------------

	Context("When the team spec has no entry role in dynamic mode", func() {
		const name = "team-no-entry"
		AfterEach(func() { cleanupTeam(name) })

		It("should set Ready=False with reason InvalidTopology", func() {
			Expect(k8sClient.Create(ctx, &kubeswarmv1alpha1.SwarmTeam{
				ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
				Spec: kubeswarmv1alpha1.SwarmTeamSpec{
					// No spec.entry and no spec.pipeline → dynamic mode with no entry = invalid
					Roles: []kubeswarmv1alpha1.SwarmTeamRole{
						{Name: "worker", SwarmAgent: "worker-agent"},
					},
				},
			})).To(Succeed())

			team, err := reconcileTeam(name)
			Expect(err).NotTo(HaveOccurred())

			cond := apimeta.FindStatusCondition(team.Status.Conditions, kubeswarmv1alpha1.ConditionReady)
			Expect(cond).NotTo(BeNil())
			Expect(cond.Status).To(Equal(metav1.ConditionFalse))
			Expect(cond.Reason).To(Equal("InvalidTopology"))
		})
	})

	Context("When spec.entry references an unknown role", func() {
		const name = "team-bad-entry"
		AfterEach(func() { cleanupTeam(name) })

		It("should set Ready=False with reason InvalidTopology", func() {
			Expect(k8sClient.Create(ctx, &kubeswarmv1alpha1.SwarmTeam{
				ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
				Spec: kubeswarmv1alpha1.SwarmTeamSpec{
					Entry: "ghost-role",
					Roles: []kubeswarmv1alpha1.SwarmTeamRole{
						{Name: "worker", SwarmAgent: "worker-agent"},
					},
				},
			})).To(Succeed())

			team, err := reconcileTeam(name)
			Expect(err).NotTo(HaveOccurred())

			cond := apimeta.FindStatusCondition(team.Status.Conditions, kubeswarmv1alpha1.ConditionReady)
			Expect(cond).NotTo(BeNil())
			Expect(cond.Status).To(Equal(metav1.ConditionFalse))
			Expect(cond.Reason).To(Equal("InvalidTopology"))
		})
	})

	Context("When a delegate target is not a declared role", func() {
		const name = "team-bad-delegate"
		AfterEach(func() { cleanupTeam(name) })

		It("should set Ready=False with reason InvalidTopology", func() {
			Expect(k8sClient.Create(ctx, &kubeswarmv1alpha1.SwarmTeam{
				ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
				Spec: kubeswarmv1alpha1.SwarmTeamSpec{
					Entry: "coordinator",
					Roles: []kubeswarmv1alpha1.SwarmTeamRole{
						{Name: "coordinator", SwarmAgent: "coord-agent", CanDelegate: []string{"ghost-role"}},
					},
				},
			})).To(Succeed())

			team, err := reconcileTeam(name)
			Expect(err).NotTo(HaveOccurred())

			cond := apimeta.FindStatusCondition(team.Status.Conditions, kubeswarmv1alpha1.ConditionReady)
			Expect(cond).NotTo(BeNil())
			Expect(cond.Status).To(Equal(metav1.ConditionFalse))
			Expect(cond.Reason).To(Equal("InvalidTopology"))
			Expect(cond.Message).To(ContainSubstring("ghost-role"))
		})
	})

	Context("When the delegation graph has a cycle", func() {
		const name = "team-cycle"
		AfterEach(func() { cleanupTeam(name) })

		It("should set Ready=False with reason InvalidTopology", func() {
			Expect(k8sClient.Create(ctx, &kubeswarmv1alpha1.SwarmTeam{
				ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
				Spec: kubeswarmv1alpha1.SwarmTeamSpec{
					Entry: "a",
					Roles: []kubeswarmv1alpha1.SwarmTeamRole{
						{Name: "a", SwarmAgent: "agent-a", CanDelegate: []string{"b"}},
						{Name: "b", SwarmAgent: "agent-b", CanDelegate: []string{"a"}},
					},
				},
			})).To(Succeed())

			team, err := reconcileTeam(name)
			Expect(err).NotTo(HaveOccurred())

			cond := apimeta.FindStatusCondition(team.Status.Conditions, kubeswarmv1alpha1.ConditionReady)
			Expect(cond).NotTo(BeNil())
			Expect(cond.Status).To(Equal(metav1.ConditionFalse))
			Expect(cond.Reason).To(Equal("InvalidTopology"))
		})
	})

	Context("When a role's SwarmAgent is missing", func() {
		const name = "team-missing-agent"
		AfterEach(func() { cleanupTeam(name) })

		It("should reconcile without error, recording the role with no replicas", func() {
			// When an external SwarmAgent doesn't exist yet, the controller treats it
			// as still being created and records the role status without replicas.
			// This is a graceful transient state, not a hard error.
			Expect(k8sClient.Create(ctx, &kubeswarmv1alpha1.SwarmTeam{
				ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
				Spec: kubeswarmv1alpha1.SwarmTeamSpec{
					Entry: "coordinator",
					Roles: []kubeswarmv1alpha1.SwarmTeamRole{
						{Name: "coordinator", SwarmAgent: "nonexistent-agent"},
					},
				},
			})).To(Succeed())

			team, err := reconcileTeam(name)
			Expect(err).NotTo(HaveOccurred())
			// The team should have a role status entry for the missing agent.
			Expect(team.Status.Roles).To(HaveLen(1))
			Expect(team.Status.Roles[0].Name).To(Equal("coordinator"))
			Expect(team.Status.Roles[0].ReadyReplicas).To(BeZero())
		})
	})

	// -------------------------------------------------------------------------
	// Happy path (dynamic mode)
	// -------------------------------------------------------------------------

	Context("When a valid two-role dynamic team is reconciled", func() {
		const (
			name          = "team-valid"
			coordAgent    = "team-coord-agent"
			reviewerAgent = "team-reviewer-agent"
		)
		AfterEach(func() {
			cleanupTeam(name)
			cleanupAgent(coordAgent)
			cleanupAgent(reviewerAgent)
		})

		BeforeEach(func() {
			createAgent(coordAgent)
			createAgent(reviewerAgent)

			Expect(k8sClient.Create(ctx, &kubeswarmv1alpha1.SwarmTeam{
				ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
				Spec: kubeswarmv1alpha1.SwarmTeamSpec{
					Entry: "coordinator",
					Roles: []kubeswarmv1alpha1.SwarmTeamRole{
						{Name: "coordinator", SwarmAgent: coordAgent, CanDelegate: []string{"reviewer"}},
						{Name: "reviewer", SwarmAgent: reviewerAgent},
					},
				},
			})).To(Succeed())
		})

		It("should set Ready=True and populate status", func() {
			team, err := reconcileTeam(name)
			Expect(err).NotTo(HaveOccurred())

			cond := apimeta.FindStatusCondition(team.Status.Conditions, kubeswarmv1alpha1.ConditionReady)
			Expect(cond).NotTo(BeNil())
			Expect(cond.Status).To(Equal(metav1.ConditionTrue))
			Expect(cond.Reason).To(Equal("Reconciled"))

			Expect(team.Status.Phase).To(Equal(kubeswarmv1alpha1.SwarmTeamPhaseReady))
			Expect(team.Status.EntryRole).To(Equal("coordinator"))
			Expect(team.Status.Roles).To(HaveLen(2))
		})

		It("should annotate each SwarmAgent with its team queue URL", func() {
			_, err := reconcileTeam(name)
			Expect(err).NotTo(HaveOccurred())

			coord := &kubeswarmv1alpha1.SwarmAgent{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: coordAgent, Namespace: namespace}, coord)).To(Succeed())
			queueURL := coord.Annotations["kubeswarm/team-queue-url"]
			Expect(queueURL).To(ContainSubstring("stream="))
			Expect(queueURL).To(ContainSubstring("coordinator"))

			reviewer := &kubeswarmv1alpha1.SwarmAgent{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: reviewerAgent, Namespace: namespace}, reviewer)).To(Succeed())
			Expect(reviewer.Annotations["kubeswarm/team-queue-url"]).To(ContainSubstring("reviewer"))
		})

		It("should annotate each SwarmAgent with its role name", func() {
			_, err := reconcileTeam(name)
			Expect(err).NotTo(HaveOccurred())

			coord := &kubeswarmv1alpha1.SwarmAgent{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: coordAgent, Namespace: namespace}, coord)).To(Succeed())
			Expect(coord.Annotations["kubeswarm/team-role"]).To(Equal("coordinator"))
		})

		It("should inject only allowed delegate routes into each agent", func() {
			_, err := reconcileTeam(name)
			Expect(err).NotTo(HaveOccurred())

			coord := &kubeswarmv1alpha1.SwarmAgent{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: coordAgent, Namespace: namespace}, coord)).To(Succeed())

			routesJSON := coord.Annotations["kubeswarm/team-routes"]
			Expect(routesJSON).NotTo(BeEmpty())

			var routes map[string]string
			Expect(json.Unmarshal([]byte(routesJSON), &routes)).To(Succeed())

			// coordinator delegates to reviewer only
			Expect(routes).To(HaveKey("reviewer"))
			Expect(routes).NotTo(HaveKey("coordinator"))
		})

	})

	// -------------------------------------------------------------------------
	// Queue URL format
	// -------------------------------------------------------------------------

	Context("roleQueueURL format", func() {
		const (
			name      = "team-qurl"
			agentName = "qurl-agent"
		)
		AfterEach(func() {
			cleanupTeam(name)
			cleanupAgent(agentName)
		})

		It("should embed namespace.team.role in the stream query parameter", func() {
			createAgent(agentName)
			Expect(k8sClient.Create(ctx, &kubeswarmv1alpha1.SwarmTeam{
				ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
				Spec: kubeswarmv1alpha1.SwarmTeamSpec{
					Entry: "worker",
					Roles: []kubeswarmv1alpha1.SwarmTeamRole{
						{Name: "worker", SwarmAgent: agentName},
					},
				},
			})).To(Succeed())

			_, err := reconcileTeam(name)
			Expect(err).NotTo(HaveOccurred())

			agent := &kubeswarmv1alpha1.SwarmAgent{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: agentName, Namespace: namespace}, agent)).To(Succeed())

			queueURL := agent.Annotations["kubeswarm/team-queue-url"]
			expectedStream := namespace + "." + name + ".worker"
			Expect(queueURL).To(ContainSubstring("stream=" + expectedStream))
		})
	})

	// -------------------------------------------------------------------------
	// Nonexistent resource
	// -------------------------------------------------------------------------

	Context("When reconciling a nonexistent SwarmTeam", func() {
		It("should return without error", func() {
			_, err := newReconciler().Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: "does-not-exist", Namespace: namespace},
			})
			Expect(err).NotTo(HaveOccurred())
		})
	})

	// -------------------------------------------------------------------------
	// Run retention GC
	// -------------------------------------------------------------------------

	Describe("run retention GC", func() {
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
		makeRun := func(name string, phase kubeswarmv1alpha1.SwarmRunPhase, age time.Duration) *kubeswarmv1alpha1.SwarmRun {
			run := &kubeswarmv1alpha1.SwarmRun{
				ObjectMeta: metav1.ObjectMeta{
					Name:      name,
					Namespace: namespace,
					Labels:    map[string]string{"kubeswarm/team": teamName},
				},
				Spec: kubeswarmv1alpha1.SwarmRunSpec{
					TeamRef:  teamName,
					Pipeline: []kubeswarmv1alpha1.SwarmTeamPipelineStep{{Role: "worker"}},
					Roles:    []kubeswarmv1alpha1.SwarmTeamRole{{Name: "worker", Model: "claude-haiku-4-5"}},
				},
			}
			Expect(k8sClient.Create(ctx, run)).To(Succeed())
			completedAt := metav1.NewTime(time.Now().Add(-age))
			run.Status.Phase = phase
			if phase == kubeswarmv1alpha1.SwarmRunPhaseSucceeded || phase == kubeswarmv1alpha1.SwarmRunPhaseFailed {
				run.Status.CompletionTime = &completedAt
			}
			Expect(k8sClient.Status().Update(ctx, run)).To(Succeed())
			return run
		}

		runExists := func(name string) bool {
			run := &kubeswarmv1alpha1.SwarmRun{}
			err := k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: namespace}, run)
			return err == nil
		}

		AfterEach(func() {
			team := &kubeswarmv1alpha1.SwarmTeam{}
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: teamName, Namespace: namespace}, team); err == nil {
				_ = k8sClient.Delete(ctx, team)
			}
			// Clean up any remaining runs.
			var runs kubeswarmv1alpha1.SwarmRunList
			_ = k8sClient.List(ctx, &runs, client.InNamespace(namespace),
				client.MatchingLabels{"kubeswarm/team": teamName})
			for i := range runs.Items {
				_ = k8sClient.Delete(ctx, &runs.Items[i])
			}
		})

		It("deletes succeeded runs beyond successfulRunsHistoryLimit", func() {
			limit := int32(2)
			team := makeTeam()
			team.Spec.SuccessfulRunsHistoryLimit = &limit
			Expect(k8sClient.Create(ctx, team)).To(Succeed())

			// Create 4 succeeded runs; expect only the 2 newest to survive.
			makeRun("gc-suc-1", kubeswarmv1alpha1.SwarmRunPhaseSucceeded, 4*time.Hour)
			makeRun("gc-suc-2", kubeswarmv1alpha1.SwarmRunPhaseSucceeded, 3*time.Hour)
			makeRun("gc-suc-3", kubeswarmv1alpha1.SwarmRunPhaseSucceeded, 2*time.Hour)
			makeRun("gc-suc-4", kubeswarmv1alpha1.SwarmRunPhaseSucceeded, 1*time.Hour)

			_, err := reconcileTeam(teamName)
			Expect(err).NotTo(HaveOccurred())

			// 2 oldest should be deleted; 2 newest should survive.
			Expect(runExists("gc-suc-1")).To(BeFalse())
			Expect(runExists("gc-suc-2")).To(BeFalse())
			Expect(runExists("gc-suc-3")).To(BeTrue())
			Expect(runExists("gc-suc-4")).To(BeTrue())
		})

		It("deletes failed runs beyond failedRunsHistoryLimit", func() {
			limit := int32(1)
			team := makeTeam()
			team.Spec.FailedRunsHistoryLimit = &limit
			Expect(k8sClient.Create(ctx, team)).To(Succeed())

			makeRun("gc-fail-1", kubeswarmv1alpha1.SwarmRunPhaseFailed, 3*time.Hour)
			makeRun("gc-fail-2", kubeswarmv1alpha1.SwarmRunPhaseFailed, 2*time.Hour)
			makeRun("gc-fail-3", kubeswarmv1alpha1.SwarmRunPhaseFailed, 1*time.Hour)

			_, err := reconcileTeam(teamName)
			Expect(err).NotTo(HaveOccurred())

			Expect(runExists("gc-fail-1")).To(BeFalse())
			Expect(runExists("gc-fail-2")).To(BeFalse())
			Expect(runExists("gc-fail-3")).To(BeTrue())
		})

		It("deletes completed runs older than runRetainFor regardless of count", func() {
			retain := &metav1.Duration{Duration: 2 * time.Hour}
			team := makeTeam()
			team.Spec.RunRetainFor = retain
			Expect(k8sClient.Create(ctx, team)).To(Succeed())

			makeRun("gc-old-1", kubeswarmv1alpha1.SwarmRunPhaseSucceeded, 5*time.Hour)   // too old
			makeRun("gc-old-2", kubeswarmv1alpha1.SwarmRunPhaseFailed, 3*time.Hour)      // too old
			makeRun("gc-young-1", kubeswarmv1alpha1.SwarmRunPhaseSucceeded, 1*time.Hour) // within window
			makeRun("gc-young-2", kubeswarmv1alpha1.SwarmRunPhaseFailed, 30*time.Minute) // within window

			_, err := reconcileTeam(teamName)
			Expect(err).NotTo(HaveOccurred())

			Expect(runExists("gc-old-1")).To(BeFalse())
			Expect(runExists("gc-old-2")).To(BeFalse())
			Expect(runExists("gc-young-1")).To(BeTrue())
			Expect(runExists("gc-young-2")).To(BeTrue())
		})

		It("never deletes Running or Pending runs", func() {
			limit := int32(0) // delete all completed runs
			team := makeTeam()
			team.Spec.SuccessfulRunsHistoryLimit = &limit
			team.Spec.FailedRunsHistoryLimit = &limit
			Expect(k8sClient.Create(ctx, team)).To(Succeed())

			makeRun("gc-running", kubeswarmv1alpha1.SwarmRunPhaseRunning, 0)
			makeRun("gc-pending", kubeswarmv1alpha1.SwarmRunPhasePending, 0)
			makeRun("gc-done", kubeswarmv1alpha1.SwarmRunPhaseSucceeded, 0)

			_, err := reconcileTeam(teamName)
			Expect(err).NotTo(HaveOccurred())

			Expect(runExists("gc-running")).To(BeTrue())
			Expect(runExists("gc-pending")).To(BeTrue())
			Expect(runExists("gc-done")).To(BeFalse())
		})
	})
})
