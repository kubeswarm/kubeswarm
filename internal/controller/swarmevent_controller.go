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
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"maps"
	"text/template"
	"time"

	"github.com/robfig/cron/v3"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kubeswarmv1alpha1 "github.com/kubeswarm/kubeswarm/api/v1alpha1"
)

// SwarmEventReconciler reconciles SwarmEvent objects.
// NOTE: RBAC markers for swarmevents are in swarmagent_controller.go (controller-gen skips this file).
type SwarmEventReconciler struct {
	client.Client
	Scheme *runtime.Scheme
	// TriggerWebhookURL is the base URL exposed by the operator's webhook HTTP server.
	// Example: "http://controller.kubeswarm-system.svc.cluster.local:8092"
	// If empty, webhook-type triggers will record an empty webhookURL in status.
	TriggerWebhookURL string
	Recorder          record.EventRecorder
}

// FireContext carries information about a trigger firing event, used to resolve
// input templates on dispatched team pipelines.
type FireContext struct {
	Name      string
	FiredAt   string
	Output    string         // upstream team output (team-output type)
	Body      map[string]any // webhook request body fields
	StreamKey string         // optional Redis List key for token-level streaming (SSE mode)
}

func (r *SwarmEventReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	trigger := &kubeswarmv1alpha1.SwarmEvent{}
	if err := r.Get(ctx, req.NamespacedName, trigger); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if trigger.Spec.Suspended {
		r.setCondition(trigger, metav1.ConditionFalse, "Suspended", "trigger is suspended")
		trigger.Status.ObservedGeneration = trigger.Generation
		return ctrl.Result{}, r.Status().Update(ctx, trigger)
	}

	var result ctrl.Result
	var err error

	switch trigger.Spec.Source.Type {
	case kubeswarmv1alpha1.TriggerSourceCron:
		result, err = r.reconcileCron(ctx, trigger)
	case kubeswarmv1alpha1.TriggerSourceWebhook:
		err = r.reconcileWebhook(ctx, trigger)
	case kubeswarmv1alpha1.TriggerSourceTeamOutput:
		err = r.reconcileTeamOutput(ctx, trigger)
	default:
		r.setCondition(trigger, metav1.ConditionFalse, "InvalidSource",
			fmt.Sprintf("unknown source type %q", trigger.Spec.Source.Type))
		trigger.Status.ObservedGeneration = trigger.Generation
		return ctrl.Result{}, r.Status().Update(ctx, trigger)
	}

	if err != nil {
		logger.Error(err, "reconciling trigger")
		return ctrl.Result{}, err
	}

	// Re-fetch before status update to avoid conflicts with the webhook server
	// which may have written lastFired concurrently.
	latest := &kubeswarmv1alpha1.SwarmEvent{}
	if err := r.Get(ctx, req.NamespacedName, latest); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	latest.Status = trigger.Status
	latest.Status.ObservedGeneration = latest.Generation
	if statusErr := r.Status().Update(ctx, latest); statusErr != nil {
		return ctrl.Result{}, statusErr
	}
	return result, nil
}

// reconcileCron checks whether the cron schedule is due and fires if so.
func (r *SwarmEventReconciler) reconcileCron(ctx context.Context, trigger *kubeswarmv1alpha1.SwarmEvent) (ctrl.Result, error) {
	if trigger.Spec.Source.Cron == "" {
		r.setCondition(trigger, metav1.ConditionFalse, "InvalidCron", "spec.source.cron is required for cron type")
		return ctrl.Result{}, nil
	}

	schedule, err := cron.ParseStandard(trigger.Spec.Source.Cron)
	if err != nil {
		r.setCondition(trigger, metav1.ConditionFalse, "InvalidCron",
			fmt.Sprintf("invalid cron expression %q: %v", trigger.Spec.Source.Cron, err))
		return ctrl.Result{}, nil
	}

	now := time.Now().UTC()

	// Determine the most recent scheduled time before now.
	// We look back far enough to catch the last missed fire.
	lastScheduled := mostRecentSchedule(schedule, now, trigger.Status.LastFiredAt)

	// Fire if the last scheduled time is after the last fire (or we've never fired).
	shouldFire := lastScheduled != nil &&
		(trigger.Status.LastFiredAt == nil || lastScheduled.After(trigger.Status.LastFiredAt.Time))

	if shouldFire {
		fireCtx := FireContext{
			Name:    trigger.Name,
			FiredAt: now.Format(time.RFC3339),
		}
		if _, err := r.fire(ctx, trigger, fireCtx); err != nil {
			return ctrl.Result{}, err
		}
		nowMeta := metav1.NewTime(now)
		trigger.Status.LastFiredAt = &nowMeta
		trigger.Status.FiredCount++
	}

	// Compute and store next fire time.
	next := schedule.Next(now)
	nextMeta := metav1.NewTime(next)
	trigger.Status.NextFireAt = &nextMeta
	r.setCondition(trigger, metav1.ConditionTrue, "Active", fmt.Sprintf("next fire at %s", next.Format(time.RFC3339)))

	return ctrl.Result{RequeueAfter: time.Until(next) + time.Second}, nil
}

// reconcileWebhook ensures a token Secret exists and the webhook URL is in status.
// Actual firing is handled by the TriggerWebhookServer HTTP handler.
func (r *SwarmEventReconciler) reconcileWebhook(ctx context.Context, trigger *kubeswarmv1alpha1.SwarmEvent) error {
	if err := r.ensureWebhookToken(ctx, trigger); err != nil {
		return err
	}
	if r.TriggerWebhookURL != "" {
		trigger.Status.WebhookURL = fmt.Sprintf("%s/triggers/%s/%s/fire",
			r.TriggerWebhookURL, trigger.Namespace, trigger.Name)
	}
	r.setCondition(trigger, metav1.ConditionTrue, kubeswarmv1alpha1.ConditionReady, "waiting for webhook POST")
	return nil
}

// ensureWebhookToken creates a Secret containing a random token for webhook auth
// if one does not already exist.
func (r *SwarmEventReconciler) ensureWebhookToken(ctx context.Context, trigger *kubeswarmv1alpha1.SwarmEvent) error {
	secretName := trigger.Name + "-webhook-token"
	existing := &corev1.Secret{}
	err := r.Get(ctx, types.NamespacedName{Name: secretName, Namespace: trigger.Namespace}, existing)
	if err == nil {
		return nil // already exists
	}
	if !errors.IsNotFound(err) {
		return err
	}

	// Generate a cryptographically random 32-byte hex token.
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return fmt.Errorf("generating webhook token: %w", err)
	}
	token := hex.EncodeToString(raw)

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      secretName,
			Namespace: trigger.Namespace,
		},
		Data: map[string][]byte{"token": []byte(token)},
	}
	if err := ctrl.SetControllerReference(trigger, secret, r.Scheme); err != nil {
		return err
	}
	return r.Create(ctx, secret)
}

// reconcileTeamOutput checks if the watched SwarmTeam has completed and fires if so.
func (r *SwarmEventReconciler) reconcileTeamOutput(ctx context.Context, trigger *kubeswarmv1alpha1.SwarmEvent) error {
	src := trigger.Spec.Source.TeamOutput
	if src == nil || src.Name == "" {
		r.setCondition(trigger, metav1.ConditionFalse, "InvalidSource",
			"spec.source.teamOutput.name is required for team-output type")
		return nil
	}

	team := &kubeswarmv1alpha1.SwarmTeam{}
	if err := r.Get(ctx, types.NamespacedName{Name: src.Name, Namespace: trigger.Namespace}, team); err != nil {
		r.setCondition(trigger, metav1.ConditionFalse, "TeamNotFound",
			fmt.Sprintf("team %q not found: %v", src.Name, err))
		return client.IgnoreNotFound(err)
	}

	desiredPhase := src.OnPhase
	if desiredPhase == "" {
		desiredPhase = kubeswarmv1alpha1.SwarmTeamPhaseSucceeded
	}

	if team.Status.Phase != desiredPhase {
		r.setCondition(trigger, metav1.ConditionTrue, "Watching",
			fmt.Sprintf("waiting for team %q to reach phase %s (current: %s)",
				src.Name, desiredPhase, team.Status.Phase))
		return nil
	}

	// Look up the latest SwarmRun to get the output and completion time.
	latestRun := r.getLatestRunForTeam(ctx, trigger.Namespace, src.Name)

	// Don't re-fire for the same completion.
	var completionTime *metav1.Time
	if latestRun != nil {
		completionTime = latestRun.Status.CompletionTime
	}
	if completionTime != nil && trigger.Status.LastFiredAt != nil &&
		!completionTime.After(trigger.Status.LastFiredAt.Time) {
		r.setCondition(trigger, metav1.ConditionTrue, "Active", "already fired for this team completion")
		return nil
	}

	output := ""
	if latestRun != nil {
		output = latestRun.Status.Output
	}
	now := time.Now().UTC()
	fireCtx := FireContext{
		Name:    trigger.Name,
		FiredAt: now.Format(time.RFC3339),
		Output:  output,
	}
	if _, err := r.fire(ctx, trigger, fireCtx); err != nil {
		return err
	}
	nowMeta := metav1.NewTime(now)
	trigger.Status.LastFiredAt = &nowMeta
	trigger.Status.FiredCount++
	r.setCondition(trigger, metav1.ConditionTrue, "Active", "fired on team completion")
	return nil
}

// fire dispatches all target teams for a trigger firing event.
// It respects the concurrency policy and sets owner references on created SwarmRun objects.
// Returns the list of created SwarmRun objects so callers can wait for results.
func (r *SwarmEventReconciler) fire(ctx context.Context, trigger *kubeswarmv1alpha1.SwarmEvent, fireCtx FireContext) ([]*kubeswarmv1alpha1.SwarmRun, error) {
	logger := log.FromContext(ctx)

	if trigger.Spec.ConcurrencyPolicy == kubeswarmv1alpha1.ConcurrencyForbid {
		running, err := r.hasRunningRun(ctx, trigger)
		if err != nil {
			return nil, err
		}
		if running {
			logger.Info("skipping fire: ConcurrencyPolicy=Forbid and a run is still running",
				"trigger", trigger.Name)
			return nil, nil
		}
	}

	var runs []*kubeswarmv1alpha1.SwarmRun
	for _, target := range trigger.Spec.Targets {
		targetKey := target.Team
		if target.Agent != "" {
			targetKey = target.Agent
		}
		var run *kubeswarmv1alpha1.SwarmRun
		var err error
		if target.Agent != "" {
			run, err = r.buildAgentRun(ctx, trigger, target, fireCtx)
		} else {
			run, err = r.buildRun(ctx, trigger, target, fireCtx)
		}
		if err != nil {
			return nil, fmt.Errorf("building run for target %q: %w", targetKey, err)
		}
		if err := r.Create(ctx, run); err != nil {
			return nil, fmt.Errorf("creating run for target %q: %w", targetKey, err)
		}
		logger.Info("dispatched run", "trigger", trigger.Name, "run", run.Name)
		runs = append(runs, run)
	}
	return runs, nil
}

// buildRun constructs an SwarmRun from the template team and fire context.
func (r *SwarmEventReconciler) buildRun(
	ctx context.Context,
	trigger *kubeswarmv1alpha1.SwarmEvent,
	target kubeswarmv1alpha1.SwarmEventTarget,
	fireCtx FireContext,
) (*kubeswarmv1alpha1.SwarmRun, error) {
	// Load the template team.
	tmpl := &kubeswarmv1alpha1.SwarmTeam{}
	if err := r.Get(ctx, types.NamespacedName{Name: target.Team, Namespace: trigger.Namespace}, tmpl); err != nil {
		return nil, fmt.Errorf("team template %q not found: %w", target.Team, err)
	}

	// Resolve input overrides using the fire context.
	resolvedInput := make(map[string]string, len(tmpl.Spec.Input))
	maps.Copy(resolvedInput, tmpl.Spec.Input)
	for k, expr := range target.Input {
		resolved, err := resolveTriggerTemplate(expr, fireCtx)
		if err != nil {
			return nil, fmt.Errorf("resolving input %q: %w", k, err)
		}
		resolvedInput[k] = resolved
	}

	// Generated name: <trigger>-<template>-<timestamp-suffix>
	suffix := time.Now().UTC().Format("20060102-150405")
	annotations := map[string]string{}
	if fireCtx.StreamKey != "" {
		annotations["kubeswarm/stream-key"] = fireCtx.StreamKey
	}
	run := &kubeswarmv1alpha1.SwarmRun{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-%s-%s", trigger.Name, target.Team, suffix),
			Namespace: trigger.Namespace,
			Labels: map[string]string{
				"kubeswarm/trigger":          trigger.Name,
				"kubeswarm/trigger-template": target.Team,
				"kubeswarm/team":             target.Team,
			},
			Annotations: annotations,
		},
		Spec: kubeswarmv1alpha1.SwarmRunSpec{
			TeamRef:              target.Team,
			TeamGeneration:       tmpl.Generation,
			Pipeline:             tmpl.Spec.Pipeline,
			Roles:                tmpl.Spec.Roles,
			Input:                resolvedInput,
			Output:               routedOutput(tmpl),
			TimeoutSeconds:       tmpl.Spec.TimeoutSeconds,
			MaxTokens:            tmpl.Spec.MaxTokens,
			Routing:              tmpl.Spec.Routing,
			DefaultContextPolicy: tmpl.Spec.DefaultContextPolicy,
		},
	}
	if err := ctrl.SetControllerReference(trigger, run, r.Scheme); err != nil {
		return nil, err
	}
	// Also add the SwarmTeam as a non-controller owner so the SwarmRun is garbage-
	// collected when the team is deleted (e.g. kubectl delete -f team.yaml).
	trueVal := true
	run.OwnerReferences = append(run.OwnerReferences, metav1.OwnerReference{
		APIVersion:         "kubeswarm/v1alpha1",
		Kind:               "SwarmTeam",
		Name:               tmpl.Name,
		UID:                tmpl.UID,
		Controller:         nil,
		BlockOwnerDeletion: &trueVal,
	})
	return run, nil
}

// buildAgentRun constructs an SwarmRun for a standalone agent target.
// The prompt expression is evaluated against the fire context.
func (r *SwarmEventReconciler) buildAgentRun(
	ctx context.Context,
	trigger *kubeswarmv1alpha1.SwarmEvent,
	target kubeswarmv1alpha1.SwarmEventTarget,
	fireCtx FireContext,
) (*kubeswarmv1alpha1.SwarmRun, error) {
	// Resolve the prompt template using the fire context.
	prompt, err := resolveTriggerTemplate(target.Prompt, fireCtx)
	if err != nil {
		return nil, fmt.Errorf("resolving prompt for agent %q: %w", target.Agent, err)
	}

	// Look up the SwarmAgent to snapshot its timeout setting.
	agent := &kubeswarmv1alpha1.SwarmAgent{}
	timeoutSeconds := 0
	if getErr := r.Get(ctx, types.NamespacedName{Name: target.Agent, Namespace: trigger.Namespace}, agent); getErr == nil {
		if agent.Spec.Guardrails != nil && agent.Spec.Guardrails.Limits != nil {
			timeoutSeconds = agent.Spec.Guardrails.Limits.TimeoutSeconds
		}
	}

	suffix := time.Now().UTC().Format("20060102-150405")
	annotations := map[string]string{}
	if fireCtx.StreamKey != "" {
		annotations["kubeswarm/stream-key"] = fireCtx.StreamKey
	}
	run := &kubeswarmv1alpha1.SwarmRun{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-%s-%s", trigger.Name, target.Agent, suffix),
			Namespace: trigger.Namespace,
			Labels: map[string]string{
				"kubeswarm/trigger": trigger.Name,
				"kubeswarm/agent":   target.Agent,
			},
			Annotations: annotations,
		},
		Spec: kubeswarmv1alpha1.SwarmRunSpec{
			Agent:          target.Agent,
			Prompt:         prompt,
			TimeoutSeconds: timeoutSeconds,
		},
	}
	if err := ctrl.SetControllerReference(trigger, run, r.Scheme); err != nil {
		return nil, err
	}
	return run, nil
}

// routedOutput returns the output template expression for the SwarmRun.
// For routed-mode teams it defaults to "{{ .steps.route.output }}" so the run's output
// field is automatically populated with the dispatched agent's response.
func routedOutput(team *kubeswarmv1alpha1.SwarmTeam) string {
	if team.Spec.Output != "" {
		return team.Spec.Output
	}
	if team.Spec.Routing != nil {
		return "{{ .steps.route.output }}"
	}
	return ""
}

// hasRunningRun returns true if any SwarmRun owned by this trigger is still running or pending.
func (r *SwarmEventReconciler) hasRunningRun(ctx context.Context, trigger *kubeswarmv1alpha1.SwarmEvent) (bool, error) {
	list := &kubeswarmv1alpha1.SwarmRunList{}
	if err := r.List(ctx, list,
		client.InNamespace(trigger.Namespace),
		client.MatchingLabels{"kubeswarm/trigger": trigger.Name},
	); err != nil {
		return false, err
	}
	for _, run := range list.Items {
		if run.Status.Phase == "" ||
			run.Status.Phase == kubeswarmv1alpha1.SwarmRunPhasePending ||
			run.Status.Phase == kubeswarmv1alpha1.SwarmRunPhaseRunning {
			return true, nil
		}
	}
	return false, nil
}

// resolveTriggerTemplate evaluates a Go template expression against the FireContext.
func resolveTriggerTemplate(expr string, fireCtx FireContext) (string, error) {
	tmpl, err := template.New("").Option("missingkey=zero").Parse(expr)
	if err != nil {
		return expr, nil // not a template — return as-is
	}
	data := map[string]any{
		"trigger": map[string]any{
			"name":    fireCtx.Name,
			"firedAt": fireCtx.FiredAt,
			"output":  fireCtx.Output,
			"body":    fireCtx.Body,
		},
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// mostRecentSchedule returns the most recent time before now that the schedule was due,
// looking back at most 24 hours. Returns nil if no scheduled time is found.
func mostRecentSchedule(schedule cron.Schedule, now time.Time, lastFired *metav1.Time) *time.Time {
	// Look back from whichever is earlier: lastFired or 24h ago.
	lookback := now.Add(-24 * time.Hour)
	if lastFired != nil && lastFired.After(lookback) {
		lookback = lastFired.Time
	}

	// Step forward through schedule times until we pass now.
	t := lookback
	var last *time.Time
	for {
		next := schedule.Next(t)
		if next.After(now) {
			break
		}
		last = &next
		t = next
	}
	return last
}

// getLatestRunForTeam returns the most recently created SwarmRun for a team, or nil if none exist.
func (r *SwarmEventReconciler) getLatestRunForTeam(ctx context.Context, namespace, teamName string) *kubeswarmv1alpha1.SwarmRun {
	var runs kubeswarmv1alpha1.SwarmRunList
	if err := r.List(ctx, &runs,
		client.InNamespace(namespace),
		client.MatchingLabels{"kubeswarm/team": teamName},
	); err != nil {
		return nil
	}
	var latest *kubeswarmv1alpha1.SwarmRun
	for i := range runs.Items {
		run := &runs.Items[i]
		if latest == nil || run.CreationTimestamp.After(latest.CreationTimestamp.Time) {
			latest = run
		}
	}
	return latest
}

func (r *SwarmEventReconciler) setCondition(
	trigger *kubeswarmv1alpha1.SwarmEvent,
	status metav1.ConditionStatus,
	reason, message string,
) {
	now := metav1.Now()
	condType := kubeswarmv1alpha1.ConditionReady
	for i, c := range trigger.Status.Conditions {
		if c.Type == condType {
			trigger.Status.Conditions[i].Status = status
			trigger.Status.Conditions[i].Reason = reason
			trigger.Status.Conditions[i].Message = message
			trigger.Status.Conditions[i].LastTransitionTime = now
			return
		}
	}
	trigger.Status.Conditions = append(trigger.Status.Conditions, metav1.Condition{
		Type:               condType,
		Status:             status,
		Reason:             reason,
		Message:            message,
		LastTransitionTime: now,
	})
}

// SetupWithManager registers the controller and sets up watches.
func (r *SwarmEventReconciler) SetupWithManager(mgr ctrl.Manager) error {
	// teamToTriggers maps a team change to all team-output triggers watching it.
	teamToTriggers := func(ctx context.Context, obj client.Object) []reconcile.Request {
		team, ok := obj.(*kubeswarmv1alpha1.SwarmTeam)
		if !ok {
			return nil
		}
		// Only react to terminal phases.
		if team.Status.Phase != kubeswarmv1alpha1.SwarmTeamPhaseSucceeded &&
			team.Status.Phase != kubeswarmv1alpha1.SwarmTeamPhaseFailed {
			return nil
		}

		var events kubeswarmv1alpha1.SwarmEventList
		if err := r.List(ctx, &events, client.InNamespace(team.Namespace)); err != nil {
			return nil
		}

		var reqs []reconcile.Request
		for _, t := range events.Items {
			if t.Spec.Source.Type != kubeswarmv1alpha1.TriggerSourceTeamOutput {
				continue
			}
			if t.Spec.Source.TeamOutput == nil {
				continue
			}
			if t.Spec.Source.TeamOutput.Name == team.Name {
				reqs = append(reqs, reconcile.Request{
					NamespacedName: types.NamespacedName{
						Name:      t.Name,
						Namespace: t.Namespace,
					},
				})
			}
		}
		return reqs
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&kubeswarmv1alpha1.SwarmEvent{}).
		Owns(&kubeswarmv1alpha1.SwarmRun{}).
		Watches(
			&kubeswarmv1alpha1.SwarmTeam{},
			handler.EnqueueRequestsFromMapFunc(teamToTriggers),
		).
		Complete(WithMetrics(r, "swarmevent"))
}
