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
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	corev1ac "k8s.io/client-go/applyconfigurations/core/v1"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kubeswarmv1alpha1 "github.com/kubeswarm/kubeswarm/api/v1alpha1"
	"github.com/kubeswarm/kubeswarm/pkg/observability"
)

const (
	// defaultPolicyBatchSize is the maximum number of agent status updates per reconcile cycle.
	defaultPolicyBatchSize = 50

	// fieldManager identifies this controller for server-side apply operations.
	policyFieldManager = "swarmpolicy-controller"

	labelValueTrue  = "true"
	labelValueFalse = "false"
)

// SwarmPolicyReconciler reconciles SwarmPolicy objects.
// It watches SwarmPolicy and SwarmAgent resources, computes the merged effective
// policy, evaluates agent compliance, and updates status on both resources.
type SwarmPolicyReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder events.EventRecorder

	// PolicyBatchSize limits agent status updates per reconcile cycle.
	// Defaults to defaultPolicyBatchSize (50) if zero.
	PolicyBatchSize int

	// metrics records policy violation and conflict counters.
	metrics *observability.OperatorMetrics

	// policyGovernedEnsured caches namespaces where the policy-governed label has
	// already been applied, avoiding repeated SSA PATCHes.
	policyGovernedEnsured sync.Map
}

// +kubebuilder:rbac:groups=kubeswarm.io,resources=swarmpolicies,verbs=get;list;watch
// +kubebuilder:rbac:groups=kubeswarm.io,resources=swarmpolicies/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=kubeswarm.io,resources=swarmagents,verbs=get;list;watch;patch
// +kubebuilder:rbac:groups=kubeswarm.io,resources=swarmagents/status,verbs=get;update;patch
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch
// +kubebuilder:rbac:groups="",resources=namespaces,verbs=get;patch

func (r *SwarmPolicyReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// 1. Fetch the triggering SwarmPolicy.
	policy := &kubeswarmv1alpha1.SwarmPolicy{}
	if err := r.Get(ctx, req.NamespacedName, policy); err != nil {
		if errors.IsNotFound(err) {
			// Policy was deleted - re-evaluate namespace to clean up.
			return r.reconcileNamespace(ctx, req.Namespace)
		}
		return ctrl.Result{}, err
	}

	if !policy.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, nil
	}

	logger.Info("reconciling SwarmPolicy", "policy", policy.Name)

	// 2. List all SwarmPolicies in the namespace.
	var policyList kubeswarmv1alpha1.SwarmPolicyList
	if err := r.List(ctx, &policyList, client.InNamespace(req.Namespace)); err != nil {
		return ctrl.Result{}, fmt.Errorf("listing policies: %w", err)
	}

	return r.reconcileWithPolicies(ctx, req.Namespace, policyList.Items)
}

// reconcileNamespace handles the case where a policy may have been deleted.
// It lists remaining policies and reconciles, or cleans up if none remain.
func (r *SwarmPolicyReconciler) reconcileNamespace(ctx context.Context, namespace string) (ctrl.Result, error) {
	var policyList kubeswarmv1alpha1.SwarmPolicyList
	if err := r.List(ctx, &policyList, client.InNamespace(namespace)); err != nil {
		return ctrl.Result{}, fmt.Errorf("listing policies: %w", err)
	}

	if len(policyList.Items) == 0 {
		return r.cleanupNamespace(ctx, namespace)
	}

	return r.reconcileWithPolicies(ctx, namespace, policyList.Items)
}

// reconcileWithPolicies is the core reconciliation logic.
func (r *SwarmPolicyReconciler) reconcileWithPolicies(
	ctx context.Context,
	namespace string,
	policies []kubeswarmv1alpha1.SwarmPolicy,
) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// 3. Merge all policies.
	effective, conflicts := MergePolicies(policies)

	if r.metrics != nil && len(conflicts) > 0 {
		for range conflicts {
			r.metrics.RecordPolicyConflict(ctx)
		}
	}

	// 4. List all SwarmAgents in the namespace.
	var agentList kubeswarmv1alpha1.SwarmAgentList
	if err := r.List(ctx, &agentList, client.InNamespace(namespace)); err != nil {
		return ctrl.Result{}, fmt.Errorf("listing agents: %w", err)
	}

	// 5. Evaluate compliance for each agent (batched).
	batchSize := r.policyBatchSize()
	compliantCount := 0
	updated := 0

	for i := range agentList.Items {
		agent := &agentList.Items[i]
		violations := EvaluateAgentCompliance(agent, effective)
		isCompliant := len(violations) == 0

		if isCompliant {
			compliantCount++
		} else if r.metrics != nil {
			for _, v := range violations {
				r.metrics.RecordPolicyViolation(ctx, "effective", agent.Name, v.Constraint)
			}
		}

		// Batch limit: stop updating agent statuses after batchSize.
		if updated >= batchSize {
			continue // Still count compliance, just skip status writes.
		}

		if err := r.updateAgentCompliance(ctx, agent, isCompliant, violations); err != nil {
			logger.Error(err, "failed to update agent compliance", "agent", agent.Name)
			continue
		}
		updated++
	}

	// 6. Manage namespace label.
	if err := r.ensurePolicyGovernedLabel(ctx, namespace, true); err != nil {
		logger.Error(err, "failed to set policy-governed label on namespace")
	}

	// 7. Update status on all policies.
	hasConflicts := len(conflicts) > 0
	for i := range policies {
		pol := &policies[i]
		if err := r.updatePolicyStatus(ctx, pol, effective, len(agentList.Items), compliantCount, hasConflicts, conflicts); err != nil {
			logger.Error(err, "failed to update policy status", "policy", pol.Name)
		}
	}

	// Requeue if we hit the batch limit (more agents to process).
	if updated >= batchSize && updated < len(agentList.Items) {
		logger.Info("batch limit reached, requeuing for remaining agents",
			"processed", updated, "total", len(agentList.Items))
		return ctrl.Result{RequeueAfter: time.Millisecond}, nil
	}

	return ctrl.Result{}, nil
}

// cleanupNamespace removes policy artifacts when no policies remain.
func (r *SwarmPolicyReconciler) cleanupNamespace(ctx context.Context, namespace string) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// Remove policy-governed label from namespace.
	if err := r.ensurePolicyGovernedLabel(ctx, namespace, false); err != nil {
		logger.Error(err, "failed to remove policy-governed label from namespace")
	}

	// Remove PolicyCompliant condition and label from all agents.
	var agentList kubeswarmv1alpha1.SwarmAgentList
	if err := r.List(ctx, &agentList, client.InNamespace(namespace)); err != nil {
		return ctrl.Result{}, fmt.Errorf("listing agents for cleanup: %w", err)
	}

	for i := range agentList.Items {
		agent := &agentList.Items[i]
		if err := r.removeAgentPolicyState(ctx, agent); err != nil {
			logger.Error(err, "failed to clean up agent policy state", "agent", agent.Name)
		}
	}

	return ctrl.Result{}, nil
}

// updateAgentCompliance sets the PolicyCompliant condition and label on an agent.
func (r *SwarmPolicyReconciler) updateAgentCompliance(
	ctx context.Context,
	agent *kubeswarmv1alpha1.SwarmAgent,
	isCompliant bool,
	violations []kubeswarmv1alpha1.PolicyViolation,
) error {
	// Determine desired state.
	desiredStatus := metav1.ConditionTrue
	desiredReason := "Compliant"
	desiredMsg := "Agent satisfies all policy constraints"
	if !isCompliant {
		desiredStatus = metav1.ConditionFalse
		desiredReason = "NonCompliant"
		desiredMsg = formatViolations(violations)
	}
	desiredLabel := labelValueTrue
	if !isCompliant {
		desiredLabel = labelValueFalse
	}

	// Check BEFORE mutation whether the condition and label already match.
	existingCond := apimeta.FindStatusCondition(agent.Status.Conditions, kubeswarmv1alpha1.ConditionPolicyCompliant)
	conditionUnchanged := existingCond != nil &&
		existingCond.ObservedGeneration == agent.Generation &&
		existingCond.Status == desiredStatus
	labelUnchanged := agent.Labels[kubeswarmv1alpha1.LabelPolicyCompliant] == desiredLabel

	if conditionUnchanged && labelUnchanged {
		return nil
	}

	// Mutate and write.
	setCondition(&agent.Status.Conditions, agent.Generation, kubeswarmv1alpha1.ConditionPolicyCompliant, desiredStatus, desiredReason, desiredMsg)

	if !isCompliant && r.Recorder != nil {
		r.Recorder.Eventf(agent, nil, corev1.EventTypeWarning,
			"PolicyViolation", "Compliance", "%s", desiredMsg)
	}

	if err := r.Status().Update(ctx, agent); err != nil {
		return fmt.Errorf("updating agent status: %w", err)
	}

	if !labelUnchanged {
		return r.patchAgentLabel(ctx, agent, kubeswarmv1alpha1.LabelPolicyCompliant, &desiredLabel)
	}
	return nil
}

// removeAgentPolicyState removes the PolicyCompliant condition and label from an agent.
func (r *SwarmPolicyReconciler) removeAgentPolicyState(ctx context.Context, agent *kubeswarmv1alpha1.SwarmAgent) error {
	// Remove condition if present.
	changed := apimeta.RemoveStatusCondition(&agent.Status.Conditions, kubeswarmv1alpha1.ConditionPolicyCompliant)
	if changed {
		if err := r.Status().Update(ctx, agent); err != nil {
			return fmt.Errorf("removing agent condition: %w", err)
		}
	}

	// Remove label if present.
	if _, hasLabel := agent.Labels[kubeswarmv1alpha1.LabelPolicyCompliant]; hasLabel {
		return r.patchAgentLabel(ctx, agent, kubeswarmv1alpha1.LabelPolicyCompliant, nil)
	}
	return nil
}

// patchAgentLabel sets or removes a label on an agent via merge patch.
// Pass nil value to remove the label.
func (r *SwarmPolicyReconciler) patchAgentLabel(ctx context.Context, agent *kubeswarmv1alpha1.SwarmAgent, key string, value *string) error {
	var patchJSON string
	if value != nil {
		patchJSON = fmt.Sprintf(`{"metadata":{"labels":{%q:%q}}}`, key, *value)
	} else {
		patchJSON = fmt.Sprintf(`{"metadata":{"labels":{%q:null}}}`, key)
	}
	patch := client.RawPatch(types.MergePatchType, []byte(patchJSON))
	return r.Patch(ctx, agent, patch)
}

// updatePolicyStatus sets conditions and compliance counts on a SwarmPolicy.
func (r *SwarmPolicyReconciler) updatePolicyStatus(
	ctx context.Context,
	policy *kubeswarmv1alpha1.SwarmPolicy,
	effective *kubeswarmv1alpha1.EffectivePolicySpec,
	agentCount, compliantCount int,
	hasConflicts bool,
	conflicts []PolicyConflict,
) error {
	oldStatus := policy.Status.DeepCopy()

	policy.Status.AgentCount = agentCount
	policy.Status.CompliantCount = compliantCount
	policy.Status.EffectivePolicy = sanitizeEffectivePolicy(effective)
	policy.Status.ObservedGeneration = policy.Generation

	// Enforcing condition: True when the policy is active.
	setCondition(&policy.Status.Conditions, policy.Generation, kubeswarmv1alpha1.ConditionEnforcing, metav1.ConditionTrue, "Active",
		fmt.Sprintf("Policy active, %d/%d agents compliant", compliantCount, agentCount))

	// Conflicting condition.
	if hasConflicts {
		msg := formatConflicts(conflicts)
		setCondition(&policy.Status.Conditions, policy.Generation, kubeswarmv1alpha1.ConditionConflicting, metav1.ConditionTrue, "ImpossibleConstraints", msg)
	} else {
		setCondition(&policy.Status.Conditions, policy.Generation, kubeswarmv1alpha1.ConditionConflicting, metav1.ConditionFalse, "NoConflicts", "No conflicting constraints detected")
	}

	if reflect.DeepEqual(oldStatus, &policy.Status) {
		return nil
	}
	return r.Status().Update(ctx, policy)
}

// ensurePolicyGovernedLabel sets or removes the policy-governed label on a namespace
// using server-side apply.
func (r *SwarmPolicyReconciler) ensurePolicyGovernedLabel(ctx context.Context, namespace string, governed bool) error {
	if governed {
		// Fast path: skip SSA if already applied for this namespace.
		if _, ok := r.policyGovernedEnsured.Load(namespace); ok {
			return nil
		}
		nsApply := corev1ac.Namespace(namespace).WithLabels(map[string]string{
			kubeswarmv1alpha1.LabelPolicyGoverned: labelValueTrue,
		})
		force := true
		if err := r.Apply(ctx, nsApply, &client.ApplyOptions{FieldManager: policyFieldManager, Force: &force}); err != nil {
			return err
		}
		r.policyGovernedEnsured.Store(namespace, struct{}{})
		return nil
	}

	// Remove: patch with null and clear cache.
	r.policyGovernedEnsured.Delete(namespace)
	ns := &corev1.Namespace{}
	ns.Name = namespace
	patchJSON := fmt.Sprintf(`{"metadata":{"labels":{%q:null}}}`, kubeswarmv1alpha1.LabelPolicyGoverned)
	patch := client.RawPatch(types.MergePatchType, []byte(patchJSON))
	return r.Patch(ctx, ns, patch)
}

// policyBatchSize returns the configured batch size or the default.
func (r *SwarmPolicyReconciler) policyBatchSize() int {
	if r.PolicyBatchSize > 0 {
		return r.PolicyBatchSize
	}
	return defaultPolicyBatchSize
}

// agentToPolicy maps a SwarmAgent change to all SwarmPolicies in the same namespace.
func (r *SwarmPolicyReconciler) agentToPolicy(ctx context.Context, obj client.Object) []reconcile.Request {
	var policyList kubeswarmv1alpha1.SwarmPolicyList
	if err := r.List(ctx, &policyList, client.InNamespace(obj.GetNamespace())); err != nil {
		log.FromContext(ctx).Error(err, "failed to list policies for agent watch")
		return nil
	}

	requests := make([]reconcile.Request, 0, len(policyList.Items))
	for _, pol := range policyList.Items {
		requests = append(requests, reconcile.Request{
			NamespacedName: types.NamespacedName{
				Namespace: pol.Namespace,
				Name:      pol.Name,
			},
		})
	}
	return requests
}

// SetupWithManager sets up the controller with the Manager.
func (r *SwarmPolicyReconciler) SetupWithManager(mgr ctrl.Manager) error {
	r.metrics = getOperatorMetrics()
	return ctrl.NewControllerManagedBy(mgr).
		For(&kubeswarmv1alpha1.SwarmPolicy{}).
		Watches(
			&kubeswarmv1alpha1.SwarmAgent{},
			handler.EnqueueRequestsFromMapFunc(r.agentToPolicy),
		).
		Named("swarmpolicy").
		Complete(WithMetrics(r, "swarmpolicy"))
}

// formatViolations builds a human-readable summary of policy violations.
func formatViolations(violations []kubeswarmv1alpha1.PolicyViolation) string {
	if len(violations) == 0 {
		return ""
	}
	// Sort for deterministic output.
	sort.Slice(violations, func(i, j int) bool {
		return violations[i].Constraint < violations[j].Constraint
	})
	msgs := make([]string, 0, len(violations))
	for _, v := range violations {
		msgs = append(msgs, v.Message)
	}
	return strings.Join(msgs, "; ")
}

// formatConflicts builds a human-readable summary of policy conflicts.
func formatConflicts(conflicts []PolicyConflict) string {
	msgs := make([]string, 0, len(conflicts))
	for _, c := range conflicts {
		msgs = append(msgs, fmt.Sprintf("%s: %s (policies: %s, %s)", c.Field, c.Message, c.PolicyA, c.PolicyB))
	}
	return strings.Join(msgs, "; ")
}

// sanitizeEffectivePolicy returns a copy of the effective policy with conflicting
// fields cleared so that CEL validation on PolicyLimits (minTimeout <= maxTimeout)
// does not reject the status update. The conflict is already reported via the
// Conflicting condition.
func sanitizeEffectivePolicy(ep *kubeswarmv1alpha1.EffectivePolicySpec) *kubeswarmv1alpha1.EffectivePolicySpec {
	if ep == nil {
		return nil
	}
	out := ep.DeepCopy()
	if out.Limits != nil &&
		out.Limits.MinTimeoutSeconds != nil &&
		out.Limits.MaxTimeoutSeconds != nil &&
		*out.Limits.MinTimeoutSeconds > *out.Limits.MaxTimeoutSeconds {
		// Clear both so the status write succeeds. The Conflicting condition
		// already captures the details.
		out.Limits.MinTimeoutSeconds = nil
		out.Limits.MaxTimeoutSeconds = nil
	}
	return out
}
