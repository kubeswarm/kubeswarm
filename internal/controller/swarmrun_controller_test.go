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
	"fmt"
	"sync"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kubeswarmv1alpha1 "github.com/kubeswarm/kubeswarm/api/v1alpha1"
	"github.com/kubeswarm/kubeswarm/pkg/agent/queue"
)

// ---------------------------------------------------------------------------
// Fake task queue for SwarmRun controller tests.
// ---------------------------------------------------------------------------

type fakeRunQueue struct {
	mu       sync.Mutex
	tasks    map[string]string // taskID → output
	counters map[string]int    // prompt → submit count
	nextID   int
	err      error // if set, Submit returns this error
}

func newFakeRunQueue() *fakeRunQueue {
	return &fakeRunQueue{
		tasks:    map[string]string{},
		counters: map[string]int{},
	}
}

func (q *fakeRunQueue) Submit(_ context.Context, prompt string, _ map[string]string) (string, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.err != nil {
		return "", q.err
	}
	q.nextID++
	id := fmt.Sprintf("task-%d", q.nextID)
	q.tasks[id] = "output for: " + prompt
	q.counters[prompt]++
	return id, nil
}

func (q *fakeRunQueue) Poll(_ context.Context) (*queue.Task, error) { return nil, nil }

func (q *fakeRunQueue) Ack(_ queue.Task, _ string, _ queue.TokenUsage, _ map[string]string) error {
	return nil
}

func (q *fakeRunQueue) Nack(_ queue.Task, _ string) error { return nil }

func (q *fakeRunQueue) Results(_ context.Context, taskIDs []string) ([]queue.TaskResult, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	var out []queue.TaskResult
	for _, id := range taskIDs {
		if output, ok := q.tasks[id]; ok {
			out = append(out, queue.TaskResult{
				TaskID: id,
				Output: output,
				Usage:  queue.TokenUsage{InputTokens: 10, OutputTokens: 20},
			})
		}
	}
	return out, nil
}

func (q *fakeRunQueue) Cancel(_ context.Context, _ []string) error { return nil }
func (q *fakeRunQueue) Close()                                     {}

// ---------------------------------------------------------------------------
// SwarmRun controller integration tests.
// ---------------------------------------------------------------------------

var _ = Describe("SwarmRun Controller", func() {
	const namespace = "default"

	ctx := context.Background()

	reconcileRun := func(runName string, q queue.TaskQueue) (*kubeswarmv1alpha1.SwarmRun, error) {
		r := &SwarmRunReconciler{
			Client:    k8sClient,
			Scheme:    k8sClient.Scheme(),
			TaskQueue: q,
		}
		_, err := r.Reconcile(ctx, reconcile.Request{
			NamespacedName: types.NamespacedName{Name: runName, Namespace: namespace},
		})
		if err != nil {
			return nil, err
		}
		run := &kubeswarmv1alpha1.SwarmRun{}
		if getErr := k8sClient.Get(ctx, types.NamespacedName{Name: runName, Namespace: namespace}, run); getErr != nil {
			return nil, getErr
		}
		return run, nil
	}

	deleteRun := func(name string) {
		run := &kubeswarmv1alpha1.SwarmRun{}
		if err := k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: namespace}, run); err == nil {
			_ = k8sClient.Delete(ctx, run)
		}
	}

	newRun := func(name, teamRef string, pipeline []kubeswarmv1alpha1.SwarmTeamPipelineStep, roles []kubeswarmv1alpha1.SwarmTeamRole) *kubeswarmv1alpha1.SwarmRun {
		return &kubeswarmv1alpha1.SwarmRun{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: namespace,
				Labels:    map[string]string{"kubeswarm/team": teamRef},
			},
			Spec: kubeswarmv1alpha1.SwarmRunSpec{
				TeamRef:  teamRef,
				Pipeline: pipeline,
				Roles:    roles,
			},
		}
	}

	Context("when no task queue is configured", func() {
		const runName = "swarmrun-no-queue"
		AfterEach(func() { deleteRun(runName) })

		It("should set NoTaskQueue condition and leave phase empty", func() {
			run := newRun(runName, "my-team",
				[]kubeswarmv1alpha1.SwarmTeamPipelineStep{{Role: "worker"}},
				[]kubeswarmv1alpha1.SwarmTeamRole{{Name: "worker", Model: "claude-haiku-4-5"}},
			)
			Expect(k8sClient.Create(ctx, run)).To(Succeed())

			result, err := reconcileRun(runName, nil)
			Expect(err).NotTo(HaveOccurred())
			Expect(result.Status.Phase).To(BeEmpty())
			cond := findRunCond(result, "Ready")
			Expect(cond).NotTo(BeNil())
			Expect(cond.Reason).To(Equal("NoTaskQueue"))
		})
	})

	Context("single-step linear pipeline", func() {
		const runName = "swarmrun-linear"
		AfterEach(func() { deleteRun(runName) })

		It("should submit the step on first reconcile and succeed after result is collected", func() {
			fq := newFakeRunQueue()
			run := newRun(runName, "my-team",
				[]kubeswarmv1alpha1.SwarmTeamPipelineStep{{Role: "worker"}},
				[]kubeswarmv1alpha1.SwarmTeamRole{{Name: "worker", Model: "claude-haiku-4-5"}},
			)
			Expect(k8sClient.Create(ctx, run)).To(Succeed())

			// First reconcile: steps initialized, task submitted, phase = Running.
			result, err := reconcileRun(runName, fq)
			Expect(err).NotTo(HaveOccurred())
			Expect(result.Status.Phase).To(Equal(kubeswarmv1alpha1.SwarmRunPhaseRunning))
			Expect(result.Status.Steps).To(HaveLen(1))
			Expect(result.Status.Steps[0].Phase).To(Equal(kubeswarmv1alpha1.SwarmFlowStepPhaseRunning))
			Expect(result.Status.Steps[0].TaskID).NotTo(BeEmpty())

			// Second reconcile: result collected, phase = Succeeded.
			result, err = reconcileRun(runName, fq)
			Expect(err).NotTo(HaveOccurred())
			Expect(result.Status.Phase).To(Equal(kubeswarmv1alpha1.SwarmRunPhaseSucceeded))
			Expect(result.Status.Steps[0].Phase).To(Equal(kubeswarmv1alpha1.SwarmFlowStepPhaseSucceeded))
			Expect(result.Status.Steps[0].Output).To(ContainSubstring("output for:"))
			Expect(result.Status.TotalTokenUsage).NotTo(BeNil())
			Expect(result.Status.TotalTokenUsage.TotalTokens).To(BeNumerically(">", 0))
		})
	})

	Context("two-step pipeline with dependency", func() {
		const runName = "swarmrun-two-step"
		AfterEach(func() { deleteRun(runName) })

		It("should run steps in order and succeed", func() {
			fq := newFakeRunQueue()
			run := newRun(runName, "my-team",
				[]kubeswarmv1alpha1.SwarmTeamPipelineStep{
					{Role: "step-a"},
					{Role: "step-b", DependsOn: []string{"step-a"}},
				},
				[]kubeswarmv1alpha1.SwarmTeamRole{
					{Name: "step-a", Model: "claude-haiku-4-5"},
					{Name: "step-b", Model: "claude-haiku-4-5"},
				},
			)
			Expect(k8sClient.Create(ctx, run)).To(Succeed())

			// Reconcile 1: step-a submitted, step-b still pending.
			result, err := reconcileRun(runName, fq)
			Expect(err).NotTo(HaveOccurred())
			Expect(result.Status.Phase).To(Equal(kubeswarmv1alpha1.SwarmRunPhaseRunning))
			Expect(findRunStep(result, "step-a").Phase).To(Equal(kubeswarmv1alpha1.SwarmFlowStepPhaseRunning))
			Expect(findRunStep(result, "step-b").Phase).To(Equal(kubeswarmv1alpha1.SwarmFlowStepPhasePending))

			// Reconcile 2: step-a result collected, step-b submitted.
			result, err = reconcileRun(runName, fq)
			Expect(err).NotTo(HaveOccurred())
			Expect(findRunStep(result, "step-a").Phase).To(Equal(kubeswarmv1alpha1.SwarmFlowStepPhaseSucceeded))
			Expect(findRunStep(result, "step-b").Phase).To(Equal(kubeswarmv1alpha1.SwarmFlowStepPhaseRunning))

			// Reconcile 3: step-b result collected, run succeeds.
			result, err = reconcileRun(runName, fq)
			Expect(err).NotTo(HaveOccurred())
			Expect(result.Status.Phase).To(Equal(kubeswarmv1alpha1.SwarmRunPhaseSucceeded))
			Expect(findRunStep(result, "step-b").Phase).To(Equal(kubeswarmv1alpha1.SwarmFlowStepPhaseSucceeded))
		})
	})

	Context("run timeout enforcement", func() {
		const runName = "swarmrun-timeout"
		AfterEach(func() { deleteRun(runName) })

		It("should fail the run when timeout is exceeded", func() {
			fq := newFakeRunQueue()
			run := newRun(runName, "my-team",
				[]kubeswarmv1alpha1.SwarmTeamPipelineStep{{Role: "slow"}},
				[]kubeswarmv1alpha1.SwarmTeamRole{{Name: "slow", Model: "claude-haiku-4-5"}},
			)
			run.Spec.TimeoutSeconds = 1
			Expect(k8sClient.Create(ctx, run)).To(Succeed())

			// First reconcile starts the run and submits the step.
			_, err := reconcileRun(runName, fq)
			Expect(err).NotTo(HaveOccurred())

			// Manually backdate StartTime to simulate timeout.
			fetched := &kubeswarmv1alpha1.SwarmRun{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: runName, Namespace: namespace}, fetched)).To(Succeed())
			past := metav1.NewTime(time.Now().Add(-10 * time.Second))
			fetched.Status.StartTime = &past
			Expect(k8sClient.Status().Update(ctx, fetched)).To(Succeed())

			// Second reconcile should detect timeout.
			result, err := reconcileRun(runName, fq)
			Expect(err).NotTo(HaveOccurred())
			Expect(result.Status.Phase).To(Equal(kubeswarmv1alpha1.SwarmRunPhaseFailed))
			cond := findRunCond(result, "Ready")
			Expect(cond).NotTo(BeNil())
			Expect(cond.Reason).To(Equal("Timeout"))
		})
	})

	Context("terminal run", func() {
		const runName = "swarmrun-terminal"
		AfterEach(func() { deleteRun(runName) })

		It("should be a no-op when the run is already Succeeded", func() {
			fq := newFakeRunQueue()
			run := newRun(runName, "my-team",
				[]kubeswarmv1alpha1.SwarmTeamPipelineStep{{Role: "worker"}},
				[]kubeswarmv1alpha1.SwarmTeamRole{{Name: "worker", Model: "claude-haiku-4-5"}},
			)
			Expect(k8sClient.Create(ctx, run)).To(Succeed())
			run.Status.Phase = kubeswarmv1alpha1.SwarmRunPhaseSucceeded
			Expect(k8sClient.Status().Update(ctx, run)).To(Succeed())

			result, err := reconcileRun(runName, fq)
			Expect(err).NotTo(HaveOccurred())
			Expect(result.Status.Phase).To(Equal(kubeswarmv1alpha1.SwarmRunPhaseSucceeded))
			// Queue was not touched (no tasks submitted).
			Expect(fq.nextID).To(Equal(0))
		})
	})

	Context("step if-condition skipping", func() {
		const runName = "swarmrun-skip"
		AfterEach(func() { deleteRun(runName) })

		It("should skip a step whose if condition is false", func() {
			fq := newFakeRunQueue()
			run := newRun(runName, "my-team",
				[]kubeswarmv1alpha1.SwarmTeamPipelineStep{
					{Role: "step-a"},
					{Role: "step-b", DependsOn: []string{"step-a"}, If: "false"},
				},
				[]kubeswarmv1alpha1.SwarmTeamRole{
					{Name: "step-a", Model: "claude-haiku-4-5"},
					{Name: "step-b", Model: "claude-haiku-4-5"},
				},
			)
			Expect(k8sClient.Create(ctx, run)).To(Succeed())

			// Reconcile 1: step-a submitted.
			_, err := reconcileRun(runName, fq)
			Expect(err).NotTo(HaveOccurred())

			// Reconcile 2: step-a done, step-b evaluated and skipped, run succeeds.
			result, err := reconcileRun(runName, fq)
			Expect(err).NotTo(HaveOccurred())
			Expect(result.Status.Phase).To(Equal(kubeswarmv1alpha1.SwarmRunPhaseSucceeded))
			Expect(findRunStep(result, "step-b").Phase).To(Equal(kubeswarmv1alpha1.SwarmFlowStepPhaseSkipped))
		})
	})
})

// ---------------------------------------------------------------------------
// Test helpers.
// ---------------------------------------------------------------------------

func findRunStep(run *kubeswarmv1alpha1.SwarmRun, name string) *kubeswarmv1alpha1.SwarmFlowStepStatus {
	for i := range run.Status.Steps {
		if run.Status.Steps[i].Name == name {
			return &run.Status.Steps[i]
		}
	}
	return &kubeswarmv1alpha1.SwarmFlowStepStatus{Name: name, Phase: ""}
}

func findRunCond(run *kubeswarmv1alpha1.SwarmRun, condType string) *metav1.Condition {
	for i := range run.Status.Conditions {
		if run.Status.Conditions[i].Type == condType {
			return &run.Status.Conditions[i]
		}
	}
	return nil
}
