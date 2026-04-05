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
	"strconv"
	"time"

	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	kubeswarmv1alpha1 "github.com/kubeswarm/kubeswarm/api/v1alpha1"
	"github.com/kubeswarm/kubeswarm/pkg/audit"
	"github.com/kubeswarm/kubeswarm/pkg/costs"
)

const budgetRequeueAfter = 5 * time.Minute

// SwarmBudgetReconciler reconciles SwarmBudget objects.
// It runs every 5 minutes per budget to refresh spend totals from the SpendStore
// and fire notifications when thresholds are crossed.
type SwarmBudgetReconciler struct {
	client.Client
	Scheme           *runtime.Scheme
	SpendStore       costs.SpendStore
	BudgetPolicy     costs.BudgetPolicy
	NotifyDispatcher *NotifyDispatcher
	// AuditEmitter emits structured audit events (RFC-0030).
	// When nil, audit logging is disabled.
	AuditEmitter *audit.Emitter
}

// +kubebuilder:rbac:groups=kubeswarm.io,resources=swarmbudgets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=kubeswarm.io,resources=swarmbudgets/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=kubeswarm.io,resources=swarmbudgets/finalizers,verbs=update

func (r *SwarmBudgetReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	budget := &kubeswarmv1alpha1.SwarmBudget{}
	if err := r.Get(ctx, req.NamespacedName, budget); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// No spend store → just requeue; don't update status.
	if r.SpendStore == nil {
		return ctrl.Result{RequeueAfter: budgetRequeueAfter}, nil
	}

	policy := r.BudgetPolicy
	if policy == nil {
		policy = costs.DefaultBudgetPolicy()
	}

	// Scope: use the budget's own namespace as default if selector namespace is empty.
	ns := budget.Spec.Selector.Namespace
	if ns == "" {
		ns = budget.Namespace
	}

	limitFloat, err := strconv.ParseFloat(budget.Spec.Limit, 64)
	if err != nil {
		logger.Error(err, "parsing budget limit", "budget", req.Name, "limit", budget.Spec.Limit)
		return ctrl.Result{RequeueAfter: budgetRequeueAfter}, nil
	}
	input := costs.BudgetInput{
		Namespace: ns,
		Team:      budget.Spec.Selector.Team,
		Period:    budget.Spec.Period,
		Limit:     limitFloat,
		WarnAt:    budget.Spec.WarnAt,
	}

	decision, err := policy.Evaluate(ctx, input, r.SpendStore)
	if err != nil {
		logger.Error(err, "evaluating budget policy", "budget", req.Name)
		return ctrl.Result{RequeueAfter: budgetRequeueAfter}, nil
	}

	// Audit: budget.checked / budget.exceeded (RFC-0030).
	if r.AuditEmitter != nil {
		action := audit.ActionBudgetChecked
		status := audit.StatusSuccess
		if decision.Status == costs.BudgetExceeded {
			action = audit.ActionBudgetExceeded
			status = audit.StatusDenied
		}
		evt := audit.NewEvent(action, status, budget.Namespace, "")
		evt.Env = audit.Env{Service: "operator"}
		detailData, _ := json.Marshal(map[string]any{
			"budget":   budget.Name,
			"spentUSD": decision.SpentUSD,
			"pctUsed":  decision.PctUsed,
			"message":  decision.Message,
		})
		evt.Detail = detailData
		r.AuditEmitter.Emit(evt)
	}

	// Update status.
	now := metav1.Now()
	prevPhase := budget.Status.Phase

	budget.Status.Phase = kubeswarmv1alpha1.BudgetStatus(decision.Status)
	budget.Status.SpentUSD = fmt.Sprintf("%.6f", decision.SpentUSD)
	budget.Status.PctUsed = fmt.Sprintf("%.6f", decision.PctUsed)
	budget.Status.LastUpdated = &now
	budget.Status.ObservedGeneration = budget.Generation

	// Set period start if not already set or if it should roll over.
	periodStart := costs.PeriodWindowStart(budget.Spec.Period)
	if budget.Status.PeriodStart == nil || budget.Status.PeriodStart.Before(&metav1.Time{Time: periodStart}) {
		t := metav1.NewTime(periodStart)
		budget.Status.PeriodStart = &t
	}

	setBudgetCondition(budget, decision)

	if err := r.Status().Update(ctx, budget); err != nil {
		return ctrl.Result{}, fmt.Errorf("updating budget status: %w", err)
	}

	// Fire notifications on phase transitions.
	if r.NotifyDispatcher != nil && budget.Spec.NotifyRef != nil {
		r.maybeNotify(ctx, budget, prevPhase, decision)
	}

	logger.V(1).Info("budget reconciled",
		"budget", req.Name, "phase", budget.Status.Phase,
		"spentUSD", decision.SpentUSD, "pct", decision.PctUsed)

	return ctrl.Result{RequeueAfter: budgetRequeueAfter}, nil
}

// maybeNotify fires a notification if the budget crossed a threshold.
func (r *SwarmBudgetReconciler) maybeNotify(
	ctx context.Context,
	budget *kubeswarmv1alpha1.SwarmBudget,
	prevPhase kubeswarmv1alpha1.BudgetStatus,
	decision costs.BudgetDecision,
) {
	newPhase := kubeswarmv1alpha1.BudgetStatus(decision.Status)
	if newPhase == prevPhase {
		return // no transition, no notification
	}

	var event kubeswarmv1alpha1.NotifyEvent
	switch newPhase {
	case kubeswarmv1alpha1.BudgetStatusExceeded:
		event = kubeswarmv1alpha1.NotifyOnBudgetExceeded
	case kubeswarmv1alpha1.BudgetStatusWarning:
		event = kubeswarmv1alpha1.NotifyOnBudgetWarning
	default:
		return
	}

	policy := &kubeswarmv1alpha1.SwarmNotify{}
	if err := r.Get(ctx, types.NamespacedName{
		Namespace: budget.Namespace,
		Name:      budget.Spec.NotifyRef.Name,
	}, policy); err != nil {
		log.FromContext(ctx).Error(err, "fetching SwarmNotify for budget", "budget", budget.Name)
		return
	}

	r.NotifyDispatcher.DispatchBudget(ctx, budget, decision, event, policy)
}

// setBudgetCondition sets the Ready condition on the SwarmBudget based on the decision.
func setBudgetCondition(budget *kubeswarmv1alpha1.SwarmBudget, decision costs.BudgetDecision) {
	var status metav1.ConditionStatus
	var reason string
	switch decision.Status {
	case costs.BudgetExceeded:
		status = metav1.ConditionFalse
		reason = "BudgetExceeded"
	case costs.BudgetWarning:
		status = metav1.ConditionFalse
		reason = "BudgetWarning"
	default:
		status = metav1.ConditionTrue
		reason = "OK"
	}
	cond := metav1.Condition{
		Type:               "Ready",
		Status:             status,
		Reason:             reason,
		Message:            decision.Message,
		ObservedGeneration: budget.Generation,
	}
	apimeta.SetStatusCondition(&budget.Status.Conditions, cond)
}

func (r *SwarmBudgetReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&kubeswarmv1alpha1.SwarmBudget{}).
		Complete(r)
}
