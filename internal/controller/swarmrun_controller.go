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
	"fmt"
	"net/url"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	oteltrace "go.opentelemetry.io/otel/trace"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	kubeswarmv1alpha1 "github.com/kubeswarm/kubeswarm/api/v1alpha1"
	"github.com/kubeswarm/kubeswarm/internal/registry"
	"github.com/kubeswarm/kubeswarm/internal/routing"
	"github.com/kubeswarm/kubeswarm/pkg/agent/queue"
	"github.com/kubeswarm/kubeswarm/pkg/costs"
	"github.com/kubeswarm/kubeswarm/pkg/flow"
	"github.com/kubeswarm/kubeswarm/pkg/observability"
)

const (
	runRequeueAfter   = 5 * time.Second
	swarmRunFinalizer = "swarmrun.kubeswarm/cleanup"
)

// SwarmRunReconciler reconciles SwarmRun objects.
type SwarmRunReconciler struct {
	client.Client
	Scheme            *runtime.Scheme
	TaskQueueURL      string
	AgentTaskQueueURL string
	TaskQueue         queue.TaskQueue
	Recorder          record.EventRecorder
	NotifyDispatcher  *NotifyDispatcher
	// SemanticValidateFn makes a single-turn LLM call for semantic validation.
	// When nil, semantic validation is skipped (Phase 2 feature; wired by cmd/main.go).
	SemanticValidateFn func(ctx context.Context, model, prompt string) (string, error)
	// RouterFn makes a single-turn LLM call for routed-mode capability dispatch.
	// Uses the same signature and builder as SemanticValidateFn.
	// When nil, routed-mode runs fail immediately with RoutingFailed.
	RouterFn func(ctx context.Context, model, prompt string) (string, error)
	// CompressFn makes a single-turn LLM call to compress a step's output.
	// Uses the same signature as SemanticValidateFn.
	// When nil, contextPolicy.strategy=compress falls back to strategy=full.
	CompressFn func(ctx context.Context, model, prompt string) (string, error)
	// CostProvider translates token counts into dollar costs.
	// Defaults to costs.Default() (static pricing) when nil.
	CostProvider costs.CostProvider
	// SpendStore records historical spend for dashboards and budget enforcement.
	// When nil, spend history is not recorded (Phase 1 behaviour).
	SpendStore costs.SpendStore
	// BudgetPolicy evaluates current spend against SwarmBudget limits.
	// Defaults to costs.DefaultBudgetPolicy() when nil.
	BudgetPolicy costs.BudgetPolicy
	// Registry is the shared capability index maintained by SwarmRegistryController.
	// When nil, registryLookup steps fail immediately.
	Registry *registry.Registry
}

// +kubebuilder:rbac:groups=kubeswarm.io,resources=swarmruns,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=kubeswarm.io,resources=swarmruns/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=kubeswarm.io,resources=swarmruns/finalizers,verbs=update

func (r *SwarmRunReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) { //nolint:gocyclo
	logger := log.FromContext(ctx)

	run := &kubeswarmv1alpha1.SwarmRun{}
	if err := r.Get(ctx, req.NamespacedName, run); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Handle deletion: cancel any queued tasks before allowing the object to be removed.
	if !run.DeletionTimestamp.IsZero() {
		if controllerutil.ContainsFinalizer(run, swarmRunFinalizer) {
			r.cancelRunTasks(ctx, run, logger)
			patch := client.MergeFrom(run.DeepCopy())
			controllerutil.RemoveFinalizer(run, swarmRunFinalizer)
			if err := r.Patch(ctx, run, patch); err != nil {
				return ctrl.Result{}, err
			}
		}
		return ctrl.Result{}, nil
	}

	// Ensure the cleanup finalizer is present so we can cancel tasks on deletion.
	if !controllerutil.ContainsFinalizer(run, swarmRunFinalizer) {
		patch := client.MergeFrom(run.DeepCopy())
		controllerutil.AddFinalizer(run, swarmRunFinalizer)
		if err := r.Patch(ctx, run, patch); err != nil {
			return ctrl.Result{}, err
		}
	}

	// Already terminal — remove the cleanup finalizer if still present so that
	// completed runs (and their containing namespace) can be deleted immediately
	// without waiting for the operator to be running at deletion time.
	// There are no active tasks left to cancel once a run is terminal.
	if flow.IsTerminalRunPhase(run.Status.Phase) {
		if controllerutil.ContainsFinalizer(run, swarmRunFinalizer) {
			patch := client.MergeFrom(run.DeepCopy())
			controllerutil.RemoveFinalizer(run, swarmRunFinalizer)
			if err := r.Patch(ctx, run, patch); err != nil {
				return ctrl.Result{}, err
			}
		}
		return ctrl.Result{}, nil
	}

	// No task queue configured — pipeline execution is not possible.
	if r.TaskQueue == nil {
		msg := "TASK_QUEUE_URL not set; pipeline execution requires a task queue"
		logger.Info(msg)
		flow.SetRunCondition(run, metav1.ConditionFalse, "NoTaskQueue", msg)
		return ctrl.Result{}, r.Status().Update(ctx, run)
	}

	// Agent-run mode — standalone agent invocation; skip all team/pipeline logic.
	if run.Spec.Agent != "" {
		return r.reconcileAgentRun(ctx, run, logger)
	}

	// Snapshot the parent SwarmTeam's pipeline/routing into the run spec if not already
	// present. This handles manually created SwarmRuns (kubectl apply) that only set
	// teamRef and input. SwarmEvent-created runs already have these fields populated.
	if run.Spec.TeamRef != "" && len(run.Spec.Pipeline) == 0 && run.Spec.Routing == nil {
		var team kubeswarmv1alpha1.SwarmTeam
		if err := r.Get(ctx, client.ObjectKey{Namespace: run.Namespace, Name: run.Spec.TeamRef}, &team); err != nil {
			if errors.IsNotFound(err) {
				flow.SetRunCondition(run, metav1.ConditionFalse, "TeamNotFound",
					fmt.Sprintf("SwarmTeam %q not found", run.Spec.TeamRef))
				run.Status.Phase = kubeswarmv1alpha1.SwarmRunPhaseFailed
				return ctrl.Result{}, r.Status().Update(ctx, run)
			}
			return ctrl.Result{}, fmt.Errorf("fetching team %q: %w", run.Spec.TeamRef, err)
		}
		run.Spec.TeamGeneration = team.Generation
		run.Spec.Pipeline = team.Spec.Pipeline
		run.Spec.Roles = team.Spec.Roles
		run.Spec.Routing = team.Spec.Routing
		run.Spec.DefaultContextPolicy = team.Spec.DefaultContextPolicy
		if run.Spec.Output == "" {
			run.Spec.Output = team.Spec.Output
		}
		if run.Spec.TimeoutSeconds == 0 {
			run.Spec.TimeoutSeconds = team.Spec.TimeoutSeconds
		}
		if run.Spec.MaxTokens == 0 {
			run.Spec.MaxTokens = team.Spec.MaxTokens
		}
		if err := r.Update(ctx, run); err != nil {
			return ctrl.Result{}, fmt.Errorf("snapshotting team spec into run: %w", err)
		}
	}

	// Initialize steps on first reconcile (idempotent — only populates empty slice).
	if run.Spec.Routing != nil {
		flow.InitializeRouteStep(run)
	} else {
		flow.InitializeRunSteps(run)
	}

	// Transition to Running and record start time on first active reconcile.
	if run.Status.Phase == "" || run.Status.Phase == kubeswarmv1alpha1.SwarmRunPhasePending {
		// Validate and apply defaults from the parent SwarmTeam's spec.inputs schema.
		if err := r.validateAndDefaultInputs(ctx, run); err != nil {
			flow.SetRunCondition(run, metav1.ConditionFalse, "InvalidInput", err.Error())
			run.Status.Phase = kubeswarmv1alpha1.SwarmRunPhaseFailed
			if r.Recorder != nil {
				r.Recorder.Event(run, corev1.EventTypeWarning, "InvalidInput", err.Error())
			}
			return ctrl.Result{}, r.Status().Update(ctx, run)
		}

		// Pre-run budget check — only blocks when hardStop is true.
		if blocked, msg := r.checkBudgets(ctx, run); blocked {
			flow.SetRunCondition(run, metav1.ConditionFalse, "BudgetExceeded", msg)
			run.Status.Phase = kubeswarmv1alpha1.SwarmRunPhaseFailed
			if r.Recorder != nil {
				r.Recorder.Event(run, corev1.EventTypeWarning, "BudgetExceeded", msg)
			}
			return ctrl.Result{}, r.Status().Update(ctx, run)
		}

		now := metav1.Now()
		run.Status.Phase = kubeswarmv1alpha1.SwarmRunPhaseRunning
		run.Status.StartTime = &now
		if r.Recorder != nil {
			r.Recorder.Event(run, corev1.EventTypeNormal, "RunStarted",
				fmt.Sprintf("SwarmRun %q started for team %q", run.Name, run.Spec.TeamRef))
		}
	}

	// Enforce run-level timeout.
	if flow.EnforceRunTimeout(run, metav1.Now()) {
		logger.Info("run timed out", "run", run.Name)
		flow.SetRunCondition(run, metav1.ConditionFalse, "Timeout",
			fmt.Sprintf("run exceeded timeout of %ds", run.Spec.TimeoutSeconds))
		if r.Recorder != nil {
			r.Recorder.Event(run, corev1.EventTypeWarning, "RunTimedOut",
				fmt.Sprintf("SwarmRun %q timed out after %ds", run.Name, run.Spec.TimeoutSeconds))
		}
		r.cancelRunTasks(ctx, run, logger)
		return ctrl.Result{}, r.Status().Update(ctx, run)
	}

	statusByName := flow.BuildRunStatusByName(run)

	// Collect completed task results from the queue.
	if err := r.collectResults(ctx, run, statusByName); err != nil {
		logger.Error(err, "collecting pipeline step results")
		return ctrl.Result{}, fmt.Errorf("collecting step results: %w", err)
	}

	templateData := flow.BuildRunTemplateData(run, statusByName)

	if run.Spec.Routing != nil {
		// Routed mode: single router call dispatches the task to a registry agent.
		if err := r.submitRoutedStep(ctx, run, statusByName, logger); err != nil {
			return ctrl.Result{}, err
		}
		flow.UpdateRunPipelinePhase(run, templateData)
	} else {
		// Pipeline mode: DAG-driven multi-step execution.
		flow.ParseRunOutputJSON(run, statusByName)
		flow.EvaluateRunLoops(run, statusByName, templateData)
		if err := r.submitPendingSteps(ctx, run, statusByName, templateData, logger); err != nil {
			return ctrl.Result{}, err
		}
		flow.UpdateRunPipelinePhase(run, templateData)
	}

	// Emit terminal K8s events.
	if r.Recorder != nil {
		switch run.Status.Phase {
		case kubeswarmv1alpha1.SwarmRunPhaseSucceeded:
			r.Recorder.Event(run, corev1.EventTypeNormal, "RunSucceeded",
				fmt.Sprintf("SwarmRun %q succeeded for team %q", run.Name, run.Spec.TeamRef))
		case kubeswarmv1alpha1.SwarmRunPhaseFailed:
			r.Recorder.Event(run, corev1.EventTypeWarning, "RunFailed",
				fmt.Sprintf("SwarmRun %q failed for team %q", run.Name, run.Spec.TeamRef))
		}
	}

	// Cancel queued tasks for any steps that won't execute (run is terminal).
	if flow.IsTerminalRunPhase(run.Status.Phase) {
		r.cancelRunTasks(ctx, run, logger)
	}

	// Dispatch notifications for terminal phase transitions.
	if r.NotifyDispatcher != nil && flow.IsTerminalRunPhase(run.Status.Phase) {
		r.NotifyDispatcher.DispatchRun(ctx, run)
	}

	run.Status.ObservedGeneration = run.Generation
	if err := r.Status().Update(ctx, run); err != nil {
		return ctrl.Result{}, err
	}

	if run.Status.Phase == kubeswarmv1alpha1.SwarmRunPhaseRunning {
		return ctrl.Result{RequeueAfter: runRequeueAfter}, nil
	}
	return ctrl.Result{}, nil
}

// reconcileAgentRun handles standalone agent invocations (spec.agent + spec.prompt).
// It maintains a single synthetic "agent" step in status.steps to track the taskID and result,
// reusing the existing queue infrastructure without any team/pipeline machinery.
func (r *SwarmRunReconciler) reconcileAgentRun(
	ctx context.Context,
	run *kubeswarmv1alpha1.SwarmRun,
	logger interface {
		Info(string, ...any)
		Error(error, string, ...any)
	},
) (ctrl.Result, error) {
	// Initialize the synthetic step on first reconcile.
	if len(run.Status.Steps) == 0 {
		run.Status.Steps = []kubeswarmv1alpha1.SwarmFlowStepStatus{{
			Name:  "agent",
			Phase: kubeswarmv1alpha1.SwarmFlowStepPhasePending,
		}}
	}
	st := &run.Status.Steps[0]

	// Transition to Running on first active reconcile.
	if run.Status.Phase == "" || run.Status.Phase == kubeswarmv1alpha1.SwarmRunPhasePending {
		now := metav1.Now()
		run.Status.Phase = kubeswarmv1alpha1.SwarmRunPhaseRunning
		run.Status.StartTime = &now
		if r.Recorder != nil {
			r.Recorder.Event(run, corev1.EventTypeNormal, "RunStarted",
				fmt.Sprintf("SwarmRun %q started for agent %q", run.Name, run.Spec.Agent))
		}
	}

	// Enforce run-level timeout.
	if flow.EnforceRunTimeout(run, metav1.Now()) {
		logger.Info("agent run timed out", "run", run.Name, "agent", run.Spec.Agent)
		flow.SetRunCondition(run, metav1.ConditionFalse, "Timeout",
			fmt.Sprintf("run exceeded timeout of %ds", run.Spec.TimeoutSeconds))
		if r.Recorder != nil {
			r.Recorder.Event(run, corev1.EventTypeWarning, "RunTimedOut",
				fmt.Sprintf("SwarmRun %q timed out after %ds", run.Name, run.Spec.TimeoutSeconds))
		}
		r.cancelRunTasks(ctx, run, logger)
		run.Status.ObservedGeneration = run.Generation
		return ctrl.Result{}, r.Status().Update(ctx, run)
	}

	// Submit or collect, depending on step phase.
	if err := r.driveAgentStep(ctx, run, st, logger); err != nil {
		return ctrl.Result{}, err
	}

	// Derive run-level phase from the single step and emit terminal events.
	r.finalizeAgentRun(ctx, run, st)

	// Dispatch notifications for terminal phase transitions.
	if r.NotifyDispatcher != nil && flow.IsTerminalRunPhase(run.Status.Phase) {
		r.NotifyDispatcher.DispatchRun(ctx, run)
	}

	run.Status.ObservedGeneration = run.Generation
	if err := r.Status().Update(ctx, run); err != nil {
		return ctrl.Result{}, err
	}
	if run.Status.Phase == kubeswarmv1alpha1.SwarmRunPhaseRunning {
		return ctrl.Result{RequeueAfter: runRequeueAfter}, nil
	}
	return ctrl.Result{}, nil
}

// driveAgentStep submits the prompt (Pending/WarmingUp) or polls for a result (Running).
func (r *SwarmRunReconciler) driveAgentStep(
	ctx context.Context,
	run *kubeswarmv1alpha1.SwarmRun,
	st *kubeswarmv1alpha1.SwarmFlowStepStatus,
	logger interface {
		Info(string, ...any)
		Error(error, string, ...any)
	},
) error {
	switch st.Phase {
	case kubeswarmv1alpha1.SwarmFlowStepPhasePending, kubeswarmv1alpha1.SwarmFlowStepPhaseWarmingUp:
		return r.submitAgentStep(ctx, run, st, logger)
	case kubeswarmv1alpha1.SwarmFlowStepPhaseRunning:
		return r.collectAgentResult(ctx, run, st, logger)
	}
	return nil
}

// submitAgentStep handles the Pending/WarmingUp transition for an agent-run step.
// It waits for agent pods to be ready, then submits the prompt to the agent's queue.
func (r *SwarmRunReconciler) submitAgentStep(
	ctx context.Context,
	run *kubeswarmv1alpha1.SwarmRun,
	st *kubeswarmv1alpha1.SwarmFlowStepStatus,
	logger interface{ Info(string, ...any) },
) error {
	// Scale-from-zero guard: wait until the agent has at least one ready pod.
	if ready, err := r.agentHasReadyReplicas(ctx, run.Namespace, run.Spec.Agent); err == nil && !ready {
		st.Phase = kubeswarmv1alpha1.SwarmFlowStepPhaseWarmingUp
		st.Message = fmt.Sprintf("waiting for agent %q pods to become ready", run.Spec.Agent)
		logger.Info("agent run warming up", "run", run.Name, "agent", run.Spec.Agent)
		return nil
	}
	// Pods are ready — clear any WarmingUp state before submitting.
	st.Phase = kubeswarmv1alpha1.SwarmFlowStepPhasePending
	st.Message = ""

	queueURL, err := r.agentQueueURL(ctx, run.Namespace, run.Spec.Agent)
	if err != nil {
		return fmt.Errorf("reading agent queue URL: %w", err)
	}
	q, closeQ, err := r.openQueueURL(queueURL)
	if err != nil {
		return fmt.Errorf("opening agent queue: %w", err)
	}
	taskID, err := q.Submit(ctx, run.Spec.Prompt, map[string]string{"run": run.Name})
	if closeQ != nil {
		closeQ()
	}
	if err != nil {
		return fmt.Errorf("submitting agent task: %w", err)
	}
	now := metav1.Now()
	st.Phase = kubeswarmv1alpha1.SwarmFlowStepPhaseRunning
	st.TaskID = taskID
	st.StartTime = &now
	st.ResolvedAgent = run.Spec.Agent
	logger.Info("submitted agent task", "run", run.Name, "agent", run.Spec.Agent, "taskID", taskID)
	return nil
}

// collectAgentResult polls the agent's queue for the result of the submitted task.
func (r *SwarmRunReconciler) collectAgentResult(
	ctx context.Context,
	run *kubeswarmv1alpha1.SwarmRun,
	st *kubeswarmv1alpha1.SwarmFlowStepStatus,
	logger interface{ Error(error, string, ...any) },
) error {
	queueURL, err := r.agentQueueURL(ctx, run.Namespace, run.Spec.Agent)
	if err != nil {
		return fmt.Errorf("reading agent queue URL for result: %w", err)
	}
	q, closeQ, err := r.openQueueURL(queueURL)
	if err != nil {
		return fmt.Errorf("opening agent queue for result: %w", err)
	}
	results, err := q.Results(ctx, []string{st.TaskID})
	if closeQ != nil {
		closeQ()
	}
	if err != nil {
		logger.Error(err, "polling agent task result", "run", run.Name, "taskID", st.TaskID)
		return nil // transient error — retry on next reconcile
	}
	for _, res := range results {
		r.applyAgentResult(ctx, run, st, res)
	}
	return nil
}

// applyAgentResult writes a completed task result into the step status.
func (r *SwarmRunReconciler) applyAgentResult(
	ctx context.Context,
	run *kubeswarmv1alpha1.SwarmRun,
	st *kubeswarmv1alpha1.SwarmFlowStepStatus,
	res queue.TaskResult,
) {
	now := metav1.Now()
	if res.Error != "" {
		st.Phase = kubeswarmv1alpha1.SwarmFlowStepPhaseFailed
		st.CompletionTime = &now
		st.Message = res.Error
		return
	}
	st.Output = res.Output
	if len(res.Artifacts) > 0 {
		st.Artifacts = res.Artifacts
	}
	st.Phase = kubeswarmv1alpha1.SwarmFlowStepPhaseSucceeded
	st.CompletionTime = &now
	if res.Usage.InputTokens == 0 && res.Usage.OutputTokens == 0 {
		return
	}
	st.TokenUsage = &kubeswarmv1alpha1.TokenUsage{
		InputTokens:  res.Usage.InputTokens,
		OutputTokens: res.Usage.OutputTokens,
		TotalTokens:  res.Usage.InputTokens + res.Usage.OutputTokens,
	}
	cp := r.CostProvider
	if cp == nil {
		cp = costs.Default()
	}
	model := ""
	agent := &kubeswarmv1alpha1.SwarmAgent{}
	if err := r.Get(ctx, client.ObjectKey{Namespace: run.Namespace, Name: run.Spec.Agent}, agent); err == nil {
		model = agent.Spec.Model
	}
	if model != "" {
		st.CostUSD = cp.Cost(model, res.Usage.InputTokens, res.Usage.OutputTokens)
	}
	r.recordStepSpend(ctx, run, st, model, res.Usage)
}

// finalizeAgentRun sets the run-level phase from the single step's phase and emits K8s events.
func (r *SwarmRunReconciler) finalizeAgentRun(
	ctx context.Context,
	run *kubeswarmv1alpha1.SwarmRun,
	st *kubeswarmv1alpha1.SwarmFlowStepStatus,
) {
	switch st.Phase {
	case kubeswarmv1alpha1.SwarmFlowStepPhaseSucceeded:
		now := metav1.Now()
		run.Status.Phase = kubeswarmv1alpha1.SwarmRunPhaseSucceeded
		run.Status.Output = st.Output
		run.Status.CompletionTime = &now
		if st.TokenUsage != nil {
			run.Status.TotalTokenUsage = st.TokenUsage
			run.Status.TotalCostUSD = st.CostUSD
		}
		flow.SetRunCondition(run, metav1.ConditionTrue, "Succeeded", "agent run completed successfully")
		if r.Recorder != nil {
			r.Recorder.Event(run, corev1.EventTypeNormal, "RunSucceeded",
				fmt.Sprintf("SwarmRun %q succeeded for agent %q", run.Name, run.Spec.Agent))
		}
	case kubeswarmv1alpha1.SwarmFlowStepPhaseFailed:
		now := metav1.Now()
		run.Status.Phase = kubeswarmv1alpha1.SwarmRunPhaseFailed
		run.Status.CompletionTime = &now
		flow.SetRunCondition(run, metav1.ConditionFalse, "AgentFailed", st.Message)
		if r.Recorder != nil {
			r.Recorder.Event(run, corev1.EventTypeWarning, "RunFailed",
				fmt.Sprintf("SwarmRun %q failed for agent %q: %s", run.Name, run.Spec.Agent, st.Message))
		}
	}
	_ = ctx // ctx unused after refactor; kept in signature for consistency with other finalizers
}

// submitPendingSteps enqueues tasks for every pipeline step whose dependencies have all succeeded.
func (r *SwarmRunReconciler) submitPendingSteps(
	ctx context.Context,
	run *kubeswarmv1alpha1.SwarmRun,
	statusByName map[string]*kubeswarmv1alpha1.SwarmFlowStepStatus,
	templateData map[string]any,
	logger interface {
		Info(string, ...any)
		Error(error, string, ...any)
	},
) error {
	for _, step := range run.Spec.Pipeline {
		st := statusByName[step.Role]
		if st == nil {
			continue
		}
		// Re-submit Running steps that have no taskID — this happens when the operator
		// crashed after setting phase=Running but before the status write persisted the taskID.
		// Without a taskID, collectResults cannot poll for the result and the step is stuck.
		// Resetting to Pending allows the next iteration to re-submit cleanly.
		if st.Phase == kubeswarmv1alpha1.SwarmFlowStepPhaseRunning && st.TaskID == "" {
			logger.Info("step is Running but has no taskID (operator restart?); resetting to Pending", "step", step.Role)
			st.Phase = kubeswarmv1alpha1.SwarmFlowStepPhasePending
			st.StartTime = nil
		}
		if st.Phase != kubeswarmv1alpha1.SwarmFlowStepPhasePending &&
			st.Phase != kubeswarmv1alpha1.SwarmFlowStepPhaseWarmingUp {
			continue
		}
		if !flow.DepsSucceeded(step.DependsOn, statusByName) {
			continue
		}

		// Build consumer-specific template data: apply defaultContextPolicy to
		// non-adjacent producer outputs when the pipeline has a default set.
		stepTemplateData := templateData
		if run.Spec.DefaultContextPolicy != nil {
			model := r.resolveStepModel(ctx, run, step.Role)
			stepTemplateData = flow.ApplyDefaultContextPolicy(
				templateData,
				step.Role,
				run.Spec.Pipeline,
				run.Spec.DefaultContextPolicy,
				statusByName,
				func(model, prompt string) (string, error) {
					if r.CompressFn == nil {
						return "", fmt.Errorf("CompressFn not configured")
					}
					return r.CompressFn(ctx, model, prompt)
				},
				model,
			)
		}

		// Evaluate `if` condition — skip for WarmingUp (already evaluated).
		if step.If != "" && st.Phase == kubeswarmv1alpha1.SwarmFlowStepPhasePending {
			condResult, err := flow.ResolveTemplate(step.If, stepTemplateData)
			if err != nil || !flow.IsTruthy(condResult) {
				now := metav1.Now()
				st.Phase = kubeswarmv1alpha1.SwarmFlowStepPhaseSkipped
				st.CompletionTime = &now
				st.Message = "skipped: if condition evaluated to false"
				logger.Info("skipping step", "step", step.Role, "condition", step.If)
				continue
			}
		}

		// Resolve prompt from step inputs.
		prompt, err := flow.ResolveTeamPrompt(step, stepTemplateData)
		if err != nil {
			logger.Error(err, "resolving step inputs", "step", step.Role)
			now := metav1.Now()
			st.Phase = kubeswarmv1alpha1.SwarmFlowStepPhaseFailed
			st.CompletionTime = &now
			st.Message = fmt.Sprintf("input template error: %v", err)
			continue
		}

		// Resolve agent: reuse already-resolved agent (WarmingUp) to avoid re-querying
		// registry round-robin which could pick a different agent than the first resolution.
		agentName := st.ResolvedAgent
		if agentName == "" {
			var resolvedByRegistry bool
			var lookupErr error
			agentName, resolvedByRegistry, lookupErr = r.resolveAgentForStep(ctx, run, step, st)
			if lookupErr != nil {
				logger.Error(lookupErr, "registry lookup failed", "step", step.Role)
				continue
			}
			if resolvedByRegistry {
				st.ResolvedAgent = agentName
			}
		}

		// Pod readiness gate: if the assigned agent has no ready replicas (e.g. just scaled
		// from zero by the team autoscaler), wait in WarmingUp phase until pods are available.
		if ready, readyErr := r.agentHasReadyReplicas(ctx, run.Namespace, agentName); readyErr == nil && !ready {
			st.Phase = kubeswarmv1alpha1.SwarmFlowStepPhaseWarmingUp
			st.Message = fmt.Sprintf("waiting for agent %q pods to become ready", agentName)
			logger.Info("step warming up", "step", step.Role, "agent", agentName)
			continue
		}
		// Warmed up — clear warming message before transition to Running.
		if st.Phase == kubeswarmv1alpha1.SwarmFlowStepPhaseWarmingUp {
			st.Message = ""
		}

		// Resolve the queue for this step's SwarmAgent.
		submitQueue, closeQueue, err := r.resolveStepQueue(ctx, run.Namespace, run.Spec.TeamRef, agentName, step.Role)
		if err != nil {
			logger.Error(err, "resolving step queue", "step", step.Role)
			return fmt.Errorf("resolving queue for step %q: %w", step.Role, err)
		}

		taskID, err := submitQueue.Submit(ctx, prompt, map[string]string{
			"run_name":  run.Name,
			"step_name": step.Role,
		})
		if closeQueue != nil {
			closeQueue()
		}
		if err != nil {
			logger.Error(err, "submitting task to queue", "step", step.Role)
			return fmt.Errorf("submitting task for step %q: %w", step.Role, err)
		}

		now := metav1.Now()
		st.Phase = kubeswarmv1alpha1.SwarmFlowStepPhaseRunning
		st.TaskID = taskID
		st.StartTime = &now
		logger.Info("submitted task", "step", step.Role, "taskID", taskID)
	}
	return nil
}

// collectResults fetches completed task results from the queue for all in-flight pipeline steps.
func (r *SwarmRunReconciler) collectResults(
	ctx context.Context,
	run *kubeswarmv1alpha1.SwarmRun,
	statusByName map[string]*kubeswarmv1alpha1.SwarmFlowStepStatus,
) error {
	type queueGroup struct {
		taskIDs []string
		waiting map[string]*kubeswarmv1alpha1.SwarmFlowStepStatus
	}
	byQueue := map[string]*queueGroup{}

	for _, st := range statusByName {
		if st.Phase != kubeswarmv1alpha1.SwarmFlowStepPhaseRunning || st.TaskID == "" {
			continue
		}
		// Use the registry-resolved agent name when available so we poll the correct
		// per-agent stream instead of the role-keyed stream.
		agentName := st.ResolvedAgent
		if agentName == "" {
			agentName = r.resolveRoleAgent(run, st.Name)
		}
		queueURL, err := r.agentQueueURL(ctx, run.Namespace, agentName)
		if err != nil {
			return err
		}
		if queueURL == "" {
			queueURL = r.computeRoleQueueURL(run.Namespace, run.Spec.TeamRef, st.Name)
		}
		if g, ok := byQueue[queueURL]; ok {
			g.taskIDs = append(g.taskIDs, st.TaskID)
			g.waiting[st.TaskID] = st
		} else {
			byQueue[queueURL] = &queueGroup{
				taskIDs: []string{st.TaskID},
				waiting: map[string]*kubeswarmv1alpha1.SwarmFlowStepStatus{st.TaskID: st},
			}
		}
	}

	// Build a role → validate config map once for all queue groups.
	validateByRole := make(map[string]*kubeswarmv1alpha1.StepValidation, len(run.Spec.Pipeline))
	for i := range run.Spec.Pipeline {
		if run.Spec.Pipeline[i].Validate != nil {
			validateByRole[run.Spec.Pipeline[i].Role] = run.Spec.Pipeline[i].Validate
		}
	}

	// Build a role → contextPolicy map once for all queue groups.
	contextPolicyByRole := make(map[string]*kubeswarmv1alpha1.StepContextPolicy, len(run.Spec.Pipeline))
	for i := range run.Spec.Pipeline {
		if run.Spec.Pipeline[i].ContextPolicy != nil {
			contextPolicyByRole[run.Spec.Pipeline[i].Role] = run.Spec.Pipeline[i].ContextPolicy
		}
	}

	for queueURL, grp := range byQueue {
		q, closeQ, err := r.openQueueURL(queueURL)
		if err != nil {
			return fmt.Errorf("opening queue %q for result collection: %w", queueURL, err)
		}
		results, err := q.Results(ctx, grp.taskIDs)
		if closeQ != nil {
			closeQ()
		}
		if err != nil {
			return err
		}

		for _, res := range results {
			st, ok := grp.waiting[res.TaskID]
			if !ok {
				continue
			}
			now := metav1.Now()
			if res.Error != "" {
				st.Phase = kubeswarmv1alpha1.SwarmFlowStepPhaseFailed
				st.CompletionTime = &now
				st.Message = res.Error
				continue
			}

			// Agent succeeded — apply validation before marking the step final.
			st.Output = res.Output
			if len(res.Artifacts) > 0 {
				st.Artifacts = res.Artifacts
			}
			if res.Usage.InputTokens > 0 || res.Usage.OutputTokens > 0 {
				st.TokenUsage = &kubeswarmv1alpha1.TokenUsage{
					InputTokens:  res.Usage.InputTokens,
					OutputTokens: res.Usage.OutputTokens,
					TotalTokens:  res.Usage.InputTokens + res.Usage.OutputTokens,
				}
				// Translate token usage to dollar cost using the configured CostProvider.
				cp := r.CostProvider
				if cp == nil {
					cp = costs.Default()
				}
				model := r.resolveStepModel(ctx, run, st.Name)
				if model != "" {
					st.CostUSD = cp.Cost(model, res.Usage.InputTokens, res.Usage.OutputTokens)
				}
				// Always record spend so token counts reach the budget dashboard even when
				// the model cannot be resolved (CostUSD will be 0 but tokens are preserved).
				r.recordStepSpend(ctx, run, st, model, res.Usage)
			}

			if failed := r.runStepValidation(ctx, run, st, validateByRole[st.Name], &now); failed {
				continue
			}

			// Apply context policy — compress, extract, or clear output before
			// downstream steps see it via "{{ .steps.<name>.output }}".
			r.applyStepContextPolicy(ctx, run, st, contextPolicyByRole[st.Name])

			st.Phase = kubeswarmv1alpha1.SwarmFlowStepPhaseSucceeded
			st.CompletionTime = &now
		}
	}
	return nil
}

// runStepValidation runs rule-based and semantic validation for a completed step.
// Returns true when validation failed and the caller should skip to the next result.
func (r *SwarmRunReconciler) runStepValidation(
	ctx context.Context,
	run *kubeswarmv1alpha1.SwarmRun,
	st *kubeswarmv1alpha1.SwarmFlowStepStatus,
	v *kubeswarmv1alpha1.StepValidation,
	now *metav1.Time,
) bool {
	if v == nil {
		return false
	}
	passed, reason := flow.ValidateStepOutput(st.Output, v)
	if !passed {
		r.Recorder.Event(run, corev1.EventTypeWarning, "StepValidationFailed",
			fmt.Sprintf("step %q validation failed (attempt %d): %s", st.Name, st.ValidationAttempts+1, reason))
		r.applyValidationFailure(st, v, reason, now)
		return true
	}
	r.Recorder.Event(run, corev1.EventTypeNormal, "StepValidationPassed",
		fmt.Sprintf("step %q validation passed", st.Name))

	if v.Semantic == "" || r.SemanticValidateFn == nil {
		return false
	}
	semModel := v.SemanticModel
	if semModel == "" {
		semModel = r.resolveStepModel(ctx, run, st.Name)
	}
	if semModel == "" {
		return false
	}
	prompt := flow.SemanticValidatorPrompt(v.Semantic, st.Output)
	response, semErr := r.SemanticValidateFn(ctx, semModel, prompt)
	var semPassed bool
	var semReason string
	if semErr != nil {
		semPassed = false
		semReason = fmt.Sprintf("semantic validator error: %v", semErr)
	} else {
		semPassed, semReason = flow.ParseSemanticResult(response)
	}
	if !semPassed {
		r.Recorder.Event(run, corev1.EventTypeWarning, "StepValidationFailed",
			fmt.Sprintf("step %q semantic validation failed (attempt %d): %s", st.Name, st.ValidationAttempts+1, semReason))
		r.applyValidationFailure(st, v, semReason, now)
		return true
	}
	r.Recorder.Event(run, corev1.EventTypeNormal, "StepValidationPassed",
		fmt.Sprintf("step %q semantic validation passed", st.Name))
	return false
}

// applyStepContextPolicy applies a per-step context policy to st after the agent completes.
// When policy is nil, the output is left unchanged (strategy=full behaviour).
func (r *SwarmRunReconciler) applyStepContextPolicy(
	ctx context.Context,
	run *kubeswarmv1alpha1.SwarmRun,
	st *kubeswarmv1alpha1.SwarmFlowStepStatus,
	policy *kubeswarmv1alpha1.StepContextPolicy,
) {
	if policy == nil {
		return
	}
	pipelineModel := r.resolveStepModel(ctx, run, st.Name)
	result := flow.ApplyContextPolicy(st.Output, policy, pipelineModel)
	if !result.NeedsCompression {
		st.Output = result.Output
		st.RawOutput = result.RawOutput
		return
	}
	if r.CompressFn == nil {
		r.Recorder.Event(run, corev1.EventTypeWarning, "ContextCompressionSkipped",
			fmt.Sprintf("step %q has strategy=compress but CompressFn is not configured; using full output", st.Name))
		return
	}
	compressed, compErr := r.CompressFn(ctx, result.CompressionModel, result.CompressionPrompt)
	if compErr != nil {
		r.Recorder.Event(run, corev1.EventTypeWarning, "ContextCompressionFailed",
			fmt.Sprintf("step %q compression failed, using full output: %v", st.Name, compErr))
		return
	}
	flow.ApplyCompressionResult(st, compressed)
	st.RawOutput = result.RawOutput
	r.Recorder.Event(run, corev1.EventTypeNormal, "ContextCompressed",
		fmt.Sprintf("step %q compressed to ~%d tokens", st.Name, st.CompressionTokens))
}

// agentHasReadyReplicas returns true when the named SwarmAgent has at least one ready replica,
// or when the agent cannot be found (non-blocking — don't gate on missing agents).
// Returns false when the agent exists but has no ready pods yet (scale-from-zero warm-up).
func (r *SwarmRunReconciler) agentHasReadyReplicas(ctx context.Context, namespace, agentName string) (bool, error) {
	agent := &kubeswarmv1alpha1.SwarmAgent{}
	if err := r.Get(ctx, client.ObjectKey{Namespace: namespace, Name: agentName}, agent); err != nil {
		return true, client.IgnoreNotFound(err) // not found → don't gate
	}
	return agent.Status.ReadyReplicas > 0, nil
}

// resolveStepModel returns the model string for the SwarmAgent backing a pipeline step role.
// It first checks the immutable spec snapshot (run.Spec.Roles), which works even after the
// SwarmAgent is deleted or restarted. Falls back to a live SwarmAgent lookup for SwarmAgent-ref
// roles that don't carry an inline model. Returns empty string only as a last resort.
func (r *SwarmRunReconciler) resolveStepModel(ctx context.Context, run *kubeswarmv1alpha1.SwarmRun, roleName string) string {
	// Prefer the model from the immutable run spec snapshot to avoid a live lookup.
	for _, role := range run.Spec.Roles {
		if role.Name == roleName && role.Model != "" {
			return role.Model
		}
	}
	// SwarmAgent-ref roles don't store the model in the snapshot; fall back to live lookup.
	agentName := r.resolveRoleAgent(run, roleName)
	agent := &kubeswarmv1alpha1.SwarmAgent{}
	if err := r.Get(ctx, client.ObjectKey{Namespace: run.Namespace, Name: agentName}, agent); err != nil {
		return ""
	}
	return agent.Spec.Model
}

// resolveRoleAgent returns the SwarmAgent name for a pipeline step role.
// Inline roles use "{teamRef}-{role}"; explicit SwarmAgent-ref roles use the ref name.
func (r *SwarmRunReconciler) resolveRoleAgent(run *kubeswarmv1alpha1.SwarmRun, roleName string) string {
	for _, role := range run.Spec.Roles {
		if role.Name == roleName {
			if role.SwarmAgent != "" {
				return role.SwarmAgent
			}
			return run.Spec.TeamRef + "-" + roleName
		}
	}
	return run.Spec.TeamRef + "-" + roleName
}

// resolveStepQueue returns the task queue for a pipeline step, preferring the SwarmAgent's
// annotation URL and falling back to computing from the base URL.
func (r *SwarmRunReconciler) resolveStepQueue(
	ctx context.Context,
	namespace, teamName, agentName, roleName string,
) (queue.TaskQueue, func(), error) {
	queueURL, err := r.agentQueueURL(ctx, namespace, agentName)
	if err != nil {
		return nil, nil, err
	}
	if queueURL == "" {
		queueURL = r.computeRoleQueueURL(namespace, teamName, roleName)
	}
	return r.openQueueURL(queueURL)
}

// agentQueueURL reads the queue URL annotation from an SwarmAgent.
func (r *SwarmRunReconciler) agentQueueURL(ctx context.Context, namespace, agentName string) (string, error) {
	if agentName == "" {
		return "", nil
	}
	agent := &kubeswarmv1alpha1.SwarmAgent{}
	if err := r.Get(ctx, client.ObjectKey{Namespace: namespace, Name: agentName}, agent); err != nil {
		if errors.IsNotFound(err) {
			return "", nil
		}
		return "", err
	}
	return agent.Annotations[annotationTeamQueueURL], nil
}

// computeRoleQueueURL builds the per-role queue URL by appending the stream param to the base URL.
func (r *SwarmRunReconciler) computeRoleQueueURL(namespace, teamName, roleName string) string {
	base := r.AgentTaskQueueURL
	if base == "" {
		base = r.TaskQueueURL
	}
	if base == "" {
		return ""
	}
	streamName := fmt.Sprintf("%s.%s.%s", namespace, teamName, roleName)
	u, err := url.Parse(base)
	if err != nil {
		return base
	}
	q := u.Query()
	q.Set("stream", streamName)
	u.RawQuery = q.Encode()
	return u.String()
}

// openQueueURL opens a TaskQueue for the given URL, or returns the shared queue when URL is empty.
func (r *SwarmRunReconciler) openQueueURL(queueURL string) (queue.TaskQueue, func(), error) {
	if queueURL == "" {
		return r.TaskQueue, nil, nil
	}
	q, err := queue.NewQueue(queueURL, 3)
	if err != nil {
		return nil, nil, fmt.Errorf("opening queue %q: %w", queueURL, err)
	}
	return q, func() { q.Close() }, nil
}

// applyValidationFailure handles a validation failure for a single pipeline step.
// It increments ValidationAttempts and either resets the step to Pending (retry)
// or marks it Failed (terminal), depending on OnFailure and MaxRetries.
func (r *SwarmRunReconciler) applyValidationFailure(
	st *kubeswarmv1alpha1.SwarmFlowStepStatus,
	v *kubeswarmv1alpha1.StepValidation,
	reason string,
	now *metav1.Time,
) {
	st.ValidationAttempts++
	st.ValidationMessage = reason

	if v.OnFailure == "retry" && st.ValidationAttempts < v.MaxRetries {
		// Reset to Pending for re-execution on the next reconcile.
		st.Phase = kubeswarmv1alpha1.SwarmFlowStepPhasePending
		st.Output = ""
		st.OutputJSON = ""
		st.TaskID = ""
		st.TokenUsage = nil
		st.CompletionTime = nil
		return
	}

	// Terminal failure.
	st.Phase = kubeswarmv1alpha1.SwarmFlowStepPhaseFailed
	st.CompletionTime = now
	st.Message = fmt.Sprintf("validation failed: %s", reason)
}

// validateAndDefaultInputs checks run.Spec.Input against the parent SwarmTeam's
// spec.inputs schema. It:
//   - Fails immediately if a required parameter is absent.
//   - Writes defaults into run.Spec.Input for optional parameters not provided.
//
// If the parent SwarmTeam is not found, or has no spec.inputs defined, this is a no-op.
func (r *SwarmRunReconciler) validateAndDefaultInputs(ctx context.Context, run *kubeswarmv1alpha1.SwarmRun) error {
	if run.Spec.TeamRef == "" {
		return nil
	}

	team := &kubeswarmv1alpha1.SwarmTeam{}
	if err := r.Get(ctx, client.ObjectKey{Name: run.Spec.TeamRef, Namespace: run.Namespace}, team); err != nil {
		if errors.IsNotFound(err) {
			return nil // team deleted mid-flight — let the run fail naturally
		}
		return fmt.Errorf("fetching parent SwarmTeam %q: %w", run.Spec.TeamRef, err)
	}

	if len(team.Spec.Inputs) == 0 {
		return nil
	}

	if run.Spec.Input == nil {
		run.Spec.Input = make(map[string]string)
	}

	for _, param := range team.Spec.Inputs {
		_, provided := run.Spec.Input[param.Name]
		if !provided {
			if param.Required {
				return fmt.Errorf("required input parameter %q is missing", param.Name)
			}
			if param.Default != "" {
				run.Spec.Input[param.Name] = param.Default
			}
		}
	}

	return nil
}

// cancelRunTasks cancels all queued tasks for steps that are still Running or Pending.
// Called when a run enters a terminal phase (timeout, failure) so agents don't waste
// compute on tasks that will never be collected.
func (r *SwarmRunReconciler) cancelRunTasks(
	ctx context.Context,
	run *kubeswarmv1alpha1.SwarmRun,
	logger interface{ Error(error, string, ...any) },
) {
	// Group task IDs by their queue URL so we open each queue at most once.
	byQueue := map[string][]string{}
	for _, st := range run.Status.Steps {
		if st.TaskID == "" {
			continue
		}
		if st.Phase != kubeswarmv1alpha1.SwarmFlowStepPhaseRunning &&
			st.Phase != kubeswarmv1alpha1.SwarmFlowStepPhasePending &&
			st.Phase != kubeswarmv1alpha1.SwarmFlowStepPhaseWarmingUp {
			continue
		}
		agentName := st.ResolvedAgent
		if agentName == "" {
			if run.Spec.Agent != "" {
				// Agent-run mode: the agent is directly named in the spec.
				agentName = run.Spec.Agent
			} else {
				agentName = r.resolveRoleAgent(run, st.Name)
			}
		}
		queueURL, err := r.agentQueueURL(ctx, run.Namespace, agentName)
		if err != nil || queueURL == "" {
			if run.Spec.Agent == "" {
				queueURL = r.computeRoleQueueURL(run.Namespace, run.Spec.TeamRef, st.Name)
			}
		}
		if queueURL == "" {
			continue
		}
		byQueue[queueURL] = append(byQueue[queueURL], st.TaskID)
	}

	for queueURL, taskIDs := range byQueue {
		q, closeQ, err := r.openQueueURL(queueURL)
		if err != nil {
			logger.Error(err, "opening queue to cancel tasks", "queue", queueURL)
			continue
		}
		if err := q.Cancel(ctx, taskIDs); err != nil {
			logger.Error(err, "cancelling run tasks", "queue", queueURL, "tasks", taskIDs)
		}
		if closeQ != nil {
			closeQ()
		}
	}
}

// checkBudgets evaluates all SwarmBudgets that match this run's team/namespace.
// Returns (true, message) if any hardStop budget is exceeded — the run should be blocked.
// Best-effort: query errors are logged and do not block the run.
func (r *SwarmRunReconciler) checkBudgets(ctx context.Context, run *kubeswarmv1alpha1.SwarmRun) (bool, string) {
	if r.SpendStore == nil {
		return false, ""
	}

	policy := r.BudgetPolicy
	if policy == nil {
		policy = costs.DefaultBudgetPolicy()
	}

	var budgetList kubeswarmv1alpha1.SwarmBudgetList
	if err := r.List(ctx, &budgetList); err != nil {
		log.FromContext(ctx).Error(err, "listing SwarmBudgets for pre-run check")
		return false, ""
	}

	for i := range budgetList.Items {
		b := &budgetList.Items[i]
		if !budgetMatchesRun(b, run) {
			continue
		}
		if !b.Spec.HardStop {
			continue
		}
		input := costs.BudgetInput{
			Namespace: run.Namespace,
			Team:      run.Spec.TeamRef,
			Period:    b.Spec.Period,
			Limit:     b.Spec.Limit,
			WarnAt:    b.Spec.WarnAt,
		}
		decision, err := policy.Evaluate(ctx, input, r.SpendStore)
		if err != nil {
			log.FromContext(ctx).Error(err, "evaluating budget for pre-run check", "budget", b.Name)
			continue
		}
		if decision.Status == costs.BudgetExceeded {
			return true, fmt.Sprintf("budget %q exceeded: %s", b.Name, decision.Message)
		}
	}
	return false, ""
}

// budgetMatchesRun returns true when the SwarmBudget selector applies to the given run.
func budgetMatchesRun(b *kubeswarmv1alpha1.SwarmBudget, run *kubeswarmv1alpha1.SwarmRun) bool {
	sel := b.Spec.Selector
	// Namespace filter: budget's selector namespace must match run's namespace,
	// or be empty (all namespaces).
	if sel.Namespace != "" && sel.Namespace != run.Namespace {
		return false
	}
	// Team filter.
	if sel.Team != "" && sel.Team != run.Spec.TeamRef {
		return false
	}
	return true
}

// resolveAgentForStep returns the SwarmAgent name for a pipeline step.
// When the step has no RegistryLookup, it falls back to resolveRoleAgent.
// When the step has a RegistryLookup, it queries the in-process registry.
// Returns (agentName, resolvedByRegistry, error).
// On lookup failure the step status is updated to Failed before returning.
func (r *SwarmRunReconciler) resolveAgentForStep(
	ctx context.Context,
	run *kubeswarmv1alpha1.SwarmRun,
	step kubeswarmv1alpha1.SwarmTeamPipelineStep,
	st *kubeswarmv1alpha1.SwarmFlowStepStatus,
) (string, bool, error) {
	// Find the pipeline step spec to get RegistryLookup.
	var lookup *kubeswarmv1alpha1.RegistryLookupSpec
	for i := range run.Spec.Pipeline {
		if run.Spec.Pipeline[i].Role == step.Role {
			lookup = run.Spec.Pipeline[i].RegistryLookup
			break
		}
	}

	if lookup == nil {
		return r.resolveRoleAgent(run, step.Role), false, nil
	}

	// Enforce maxDepth from the referenced registry policy.
	reg := r.findRegistry(ctx, run.Namespace, lookup.RegistryRef)
	if reg != nil && reg.Spec.Policy != nil {
		maxDepth := reg.Spec.Policy.MaxDepth
		if maxDepth > 0 {
			// Count current delegation depth via resolved steps in this run.
			depth := 0
			for _, s := range run.Status.Steps {
				if s.ResolvedAgent != "" {
					depth++
				}
			}
			if depth >= maxDepth {
				now := metav1.Now()
				st.Phase = kubeswarmv1alpha1.SwarmFlowStepPhaseFailed
				st.CompletionTime = &now
				st.Message = fmt.Sprintf("MaxDelegationDepthExceeded: delegation depth %d exceeds maxDepth %d", depth, maxDepth)
				return "", false, fmt.Errorf("step %q: %s", step.Role, st.Message)
			}
		}
	}

	// Resolve via the in-process registry.
	if r.Registry == nil {
		now := metav1.Now()
		st.Phase = kubeswarmv1alpha1.SwarmFlowStepPhaseFailed
		st.CompletionTime = &now
		st.Message = "RegistryLookupFailed: no Registry configured in operator"
		return "", false, fmt.Errorf("step %q: %s", step.Role, st.Message)
	}

	strategy := lookup.Strategy
	if strategy == "" {
		strategy = kubeswarmv1alpha1.RegistryLookupStrategyLeastBusy
	}

	agentName := r.Registry.Resolve(registry.ResolveRequest{
		Capability: lookup.Capability,
		Tags:       lookup.Tags,
		Strategy:   strategy,
	})

	if agentName == "" {
		if lookup.Fallback != "" {
			// Fallback is used as-is — may be a role name or an SwarmAgent name.
			return lookup.Fallback, false, nil
		}
		now := metav1.Now()
		st.Phase = kubeswarmv1alpha1.SwarmFlowStepPhaseFailed
		st.CompletionTime = &now
		st.Message = fmt.Sprintf("RegistryLookupFailed: no agent found for capability %q with tags %v", lookup.Capability, lookup.Tags)
		return "", false, fmt.Errorf("step %q: %s", step.Role, st.Message)
	}

	return agentName, true, nil
}

// findRegistry fetches the SwarmRegistry CR referenced by a lookup, or the first one in the namespace.
// Returns nil if none is found (non-fatal — caller decides how to handle missing registry).
func (r *SwarmRunReconciler) findRegistry(
	ctx context.Context,
	namespace string,
	ref *kubeswarmv1alpha1.LocalObjectReference,
) *kubeswarmv1alpha1.SwarmRegistry {
	reg := &kubeswarmv1alpha1.SwarmRegistry{}
	if ref != nil && ref.Name != "" {
		if err := r.Get(ctx, client.ObjectKey{Namespace: namespace, Name: ref.Name}, reg); err != nil {
			return nil
		}
		return reg
	}
	// Default: first registry in the namespace.
	var regList kubeswarmv1alpha1.SwarmRegistryList
	if err := r.List(ctx, &regList, client.InNamespace(namespace)); err != nil || len(regList.Items) == 0 {
		return nil
	}
	return &regList.Items[0]
}

// submitRoutedStep handles the routing decision and task dispatch for a routed-mode run.
// It expects a single synthetic "route" step in statusByName. When the step is Pending,
// it calls the router LLM, resolves the agent, and submits the task to its queue.
// Routing failures are recorded as step-level failures rather than controller errors
// so the run reaches a terminal phase cleanly.
func (r *SwarmRunReconciler) submitRoutedStep(
	ctx context.Context,
	run *kubeswarmv1alpha1.SwarmRun,
	statusByName map[string]*kubeswarmv1alpha1.SwarmFlowStepStatus,
	logger interface {
		Info(string, ...any)
		Error(error, string, ...any)
	},
) error {
	st := statusByName["route"]
	if st == nil || st.Phase != kubeswarmv1alpha1.SwarmFlowStepPhasePending {
		return nil
	}

	ctx, span := observability.Tracer("swarm-operator").Start(ctx, "swarmteam.route",
		oteltrace.WithAttributes(
			attribute.String("run.name", run.Name),
			attribute.String("run.namespace", run.Namespace),
			attribute.String("team", run.Spec.TeamRef),
		))
	defer span.End()

	if r.RouterFn == nil {
		now := metav1.Now()
		st.Phase = kubeswarmv1alpha1.SwarmFlowStepPhaseFailed
		st.CompletionTime = &now
		st.Message = "RoutingFailed: RouterFn not configured in operator"
		return nil
	}
	if r.Registry == nil {
		now := metav1.Now()
		st.Phase = kubeswarmv1alpha1.SwarmFlowStepPhaseFailed
		st.CompletionTime = &now
		st.Message = "RoutingFailed: Registry not configured in operator"
		return nil
	}

	result, err := routing.Route(ctx, r.Registry, run.Spec.Routing, run.Spec.Input, r.RouterFn)
	if err != nil {
		now := metav1.Now()
		st.Phase = kubeswarmv1alpha1.SwarmFlowStepPhaseFailed
		st.CompletionTime = &now
		st.Message = fmt.Sprintf("RoutingFailed: %v", err)
		logger.Error(err, "routing failed", "run", run.Name)
		span.RecordError(err)
		span.SetStatus(codes.Error, st.Message)
		if r.Recorder != nil {
			r.Recorder.Event(run, corev1.EventTypeWarning, "RoutingFailed", st.Message)
		}
		return nil
	}

	span.SetAttributes(
		attribute.String("capability", result.Capability),
		attribute.String("agent", result.AgentName),
	)

	st.ResolvedAgent = result.AgentName
	st.SelectedCapability = result.Capability
	st.RoutingReason = result.Reason
	logger.Info("task routed", "run", run.Name, "agent", result.AgentName, "capability", result.Capability)

	if r.Recorder != nil {
		r.Recorder.Event(run, corev1.EventTypeNormal, "TaskRouted",
			fmt.Sprintf("routed to agent %q via capability %q: %s", result.AgentName, result.Capability, result.Reason))
	}

	// Build the prompt from the full input map.
	inputBytes, _ := json.Marshal(run.Spec.Input)
	prompt := string(inputBytes)

	submitQueue, closeQueue, err := r.resolveStepQueue(ctx, run.Namespace, run.Spec.TeamRef, result.AgentName, "route")
	if err != nil {
		return fmt.Errorf("resolving queue for routed step: %w", err)
	}

	taskID, err := submitQueue.Submit(ctx, prompt, map[string]string{
		"capability":     result.Capability,
		"routing_reason": result.Reason,
	})
	if closeQueue != nil {
		closeQueue()
	}
	if err != nil {
		return fmt.Errorf("submitting routed task: %w", err)
	}

	now := metav1.Now()
	st.Phase = kubeswarmv1alpha1.SwarmFlowStepPhaseRunning
	st.TaskID = taskID
	st.StartTime = &now
	logger.Info("routed task submitted", "run", run.Name, "taskID", taskID, "agent", result.AgentName)
	return nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *SwarmRunReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&kubeswarmv1alpha1.SwarmRun{}).
		Named("swarmrun").
		Complete(WithMetrics(r, "swarmrun"))
}

// recordStepSpend records the cost of a completed step in the SpendStore (best-effort).
func (r *SwarmRunReconciler) recordStepSpend(
	ctx context.Context,
	run *kubeswarmv1alpha1.SwarmRun,
	st *kubeswarmv1alpha1.SwarmFlowStepStatus,
	model string,
	usage queue.TokenUsage,
) {
	if r.SpendStore == nil {
		return
	}
	if st.CostUSD == 0 && usage.InputTokens == 0 && usage.OutputTokens == 0 {
		return
	}
	entry := costs.SpendEntry{
		Timestamp:    time.Now(),
		Namespace:    run.Namespace,
		Team:         run.Spec.TeamRef,
		RunName:      run.Name,
		StepName:     st.Name,
		Model:        model,
		InputTokens:  usage.InputTokens,
		OutputTokens: usage.OutputTokens,
		CostUSD:      st.CostUSD,
	}
	if err := r.SpendStore.Record(ctx, entry); err != nil {
		log.FromContext(ctx).Error(err, "recording spend entry", "run", run.Name, "step", st.Name)
	}
}
