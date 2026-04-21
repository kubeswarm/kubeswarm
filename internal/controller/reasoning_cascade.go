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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1alpha1 "github.com/kubeswarm/kubeswarm/api/v1alpha1"
)

// applyReasoningCondition computes the effective reasoning config from the
// SwarmSettings cascade and per-agent spec, and sets the ReasoningActive
// status condition on swarmAgent. Extracted from Reconcile to keep the main
// reconcile loop's cyclomatic complexity under the lint ceiling.
func (r *SwarmAgentReconciler) applyReasoningCondition(
	swarmAgent *v1alpha1.SwarmAgent,
	allSettings []v1alpha1.SwarmSettings,
) {
	effective := mergeReasoningConfig(swarmAgent.Spec.Reasoning, allSettings)
	var limits *v1alpha1.GuardrailLimits
	if swarmAgent.Spec.Guardrails != nil && swarmAgent.Spec.Guardrails.Limits != nil {
		limits = swarmAgent.Spec.Guardrails.Limits
	}
	reason, condStatus := reasoningConditionReason(effective, limits)
	r.setCondition(swarmAgent, v1alpha1.ConditionReasoningActive, condStatus, reason, "")
}

// mergeReasoningConfig merges per-agent reasoning config with SwarmSettings
// cascade, applying RFC-0012 override semantics. Per-agent fields win over
// cascaded fields. Returns nil when neither source provides anything.
func mergeReasoningConfig(agentCfg *v1alpha1.ReasoningConfig, allSettings []v1alpha1.SwarmSettings) *v1alpha1.ReasoningConfig {
	out := v1alpha1.ReasoningConfig{}
	hasAny := false

	// Cascade: iterate settings in order, last non-zero value wins per field.
	for _, s := range allSettings {
		rd := s.Spec.Reasoning
		if rd == nil {
			continue
		}
		if rd.Mode != "" {
			out.Mode = rd.Mode
			hasAny = true
		}
		if rd.Effort != "" {
			out.Effort = rd.Effort
			hasAny = true
		}
		if rd.BudgetTokens != nil {
			v := *rd.BudgetTokens
			out.BudgetTokens = &v
			hasAny = true
		}
	}

	// Overlay per-agent config.
	if agentCfg != nil {
		if agentCfg.Mode != "" {
			out.Mode = agentCfg.Mode
			hasAny = true
		}
		if agentCfg.Effort != "" {
			out.Effort = agentCfg.Effort
			hasAny = true
		}
		if agentCfg.BudgetTokens != nil {
			v := *agentCfg.BudgetTokens
			out.BudgetTokens = &v
			hasAny = true
		}
	}

	if !hasAny {
		return nil
	}
	return &out
}

// reasoningConditionReason derives the ReasoningActive condition reason
// from the effective reasoning config and guardrail limits.
// Returns Disabled, ClampedByGuardrail, or Active.
func reasoningConditionReason(cfg *v1alpha1.ReasoningConfig, limits *v1alpha1.GuardrailLimits) (reason string, status metav1.ConditionStatus) {
	if cfg == nil || cfg.Mode == "" || cfg.Mode == v1alpha1.ReasoningDisabled {
		return v1alpha1.ReasoningReasonDisabled, metav1.ConditionFalse
	}
	if isClamped(cfg, limits) {
		return v1alpha1.ReasoningReasonClampedByGuardrail, metav1.ConditionTrue
	}
	return v1alpha1.ReasoningReasonActive, metav1.ConditionTrue
}

// isClamped returns true when a guardrail limit will reduce the effective
// thinking budget below what the user requested. Provider-specific details
// (OpenAI effort downgrade table, Anthropic budget_tokens) are handled at
// runtime; the reconciler only checks the generic case where budgetTokens
// exceeds the cap.
func isClamped(cfg *v1alpha1.ReasoningConfig, limits *v1alpha1.GuardrailLimits) bool {
	if cfg == nil || limits == nil || limits.MaxThinkingTokensPerCall == nil {
		return false
	}
	if cfg.BudgetTokens != nil && *cfg.BudgetTokens > *limits.MaxThinkingTokensPerCall {
		return true
	}
	return false
}
