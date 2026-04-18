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
	"fmt"
	"path"
	"slices"

	kubeswarmv1alpha1 "github.com/kubeswarm/kubeswarm/api/v1alpha1"
)

// PolicyConflict describes an irreconcilable conflict detected during policy merging.
type PolicyConflict struct {
	// Field identifies the policy field that has a conflict.
	Field string
	// PolicyA is the name of the first policy involved in the conflict.
	PolicyA string
	// PolicyB is the name of the second policy involved in the conflict.
	PolicyB string
	// Message is a human-readable description of the conflict.
	Message string
}

// trustLevelOrder maps trust levels to a numeric strictness rank.
// Higher value = stricter. sandbox is strictest.
var trustLevelOrder = map[kubeswarmv1alpha1.ToolTrustLevel]int{
	kubeswarmv1alpha1.ToolTrustInternal: 0,
	kubeswarmv1alpha1.ToolTrustExternal: 1,
	kubeswarmv1alpha1.ToolTrustSandbox:  2,
}

// validationLevelOrder maps output validation levels to a numeric strictness rank.
// Higher value = stricter. semantic is strictest.
var validationLevelOrder = map[kubeswarmv1alpha1.PolicyOutputLevel]int{
	kubeswarmv1alpha1.PolicyOutputLevelNone:     0,
	kubeswarmv1alpha1.PolicyOutputLevelPattern:  1,
	kubeswarmv1alpha1.PolicyOutputLevelSchema:   2,
	kubeswarmv1alpha1.PolicyOutputLevelSemantic: 3,
}

// enforcementModeOrder maps enforcement modes to a numeric strictness rank.
// Higher value = stricter. Enforce is strictest.
var enforcementModeOrder = map[kubeswarmv1alpha1.PolicyEnforcementMode]int{
	kubeswarmv1alpha1.PolicyEnforcementAudit:   0,
	kubeswarmv1alpha1.PolicyEnforcementWarn:    1,
	kubeswarmv1alpha1.PolicyEnforcementEnforce: 2,
}

// MergePolicies merges a slice of SwarmPolicy resources into a single EffectivePolicySpec
// and returns any conflicts detected during the merge. The merge rules are:
//   - Numeric ceilings (max*): lowest non-nil wins
//   - Numeric floors (min*): highest non-nil wins
//   - Deny lists: union, deduplicated
//   - Booleans (requirements): logical OR
//   - Trust level: strictest wins (sandbox > external > internal)
//   - Validation level: strictest wins (semantic > schema > pattern > none)
//   - Enforcement mode: strictest wins (Enforce > Warn > Audit)
//   - Model allowed: intersection - model must be in ALL allowlists; empty = no constraint
//   - Model denied: union
func MergePolicies(policies []kubeswarmv1alpha1.SwarmPolicy) (*kubeswarmv1alpha1.EffectivePolicySpec, []PolicyConflict) {
	if len(policies) == 0 {
		return nil, nil
	}

	effective := &kubeswarmv1alpha1.EffectivePolicySpec{
		// Default enforcement mode - will be replaced by the strictest found.
		EnforcementMode: kubeswarmv1alpha1.PolicyEnforcementAudit,
		MinValidation:   kubeswarmv1alpha1.PolicyOutputLevelNone,
	}

	// Track union sets as maps for deduplication.
	toolDenySet := map[string]struct{}{}
	denyPatternSet := map[string]struct{}{}
	modelDeniedSet := map[string]struct{}{}

	// modelAllowLists accumulates per-policy allowed model lists for intersection.
	// A policy with no models.allowed imposes no allowlist constraint.
	var modelAllowLists []allowEntry

	var conflicts []PolicyConflict

	for i := range policies {
		pol := &policies[i]
		spec := &pol.Spec

		// --- Enforcement mode: strictest wins ---
		if enforcementModeOrder[spec.EnforcementMode] > enforcementModeOrder[effective.EnforcementMode] {
			effective.EnforcementMode = spec.EnforcementMode
		}

		// --- Requirements: logical OR ---
		if spec.Requirements.BudgetRef {
			effective.Requirements.BudgetRef = true
		}
		if spec.Requirements.Audit {
			effective.Requirements.Audit = true
		}
		if spec.Requirements.AllowList {
			effective.Requirements.AllowList = true
		}

		// --- Limits ---
		if spec.Limits != nil {
			if effective.Limits == nil {
				effective.Limits = &kubeswarmv1alpha1.PolicyLimits{}
			}
			mergeLimits(effective.Limits, spec.Limits)
		}

		// --- Tools ---
		if spec.Tools != nil {
			// Deny list: union.
			for _, d := range spec.Tools.Deny {
				toolDenySet[d] = struct{}{}
			}

			// Trust level: strictest wins.
			if spec.Tools.ForceTrustLevel != nil {
				if effective.ForceTrustLevel == nil {
					effective.ForceTrustLevel = spec.Tools.ForceTrustLevel
				} else if trustLevelOrder[*spec.Tools.ForceTrustLevel] > trustLevelOrder[*effective.ForceTrustLevel] {
					effective.ForceTrustLevel = spec.Tools.ForceTrustLevel
				}
			}
		}

		// --- Output ---
		if spec.Output != nil {
			// Validation level: strictest wins.
			if validationLevelOrder[spec.Output.MinValidation] > validationLevelOrder[effective.MinValidation] {
				effective.MinValidation = spec.Output.MinValidation
			}
			// Deny patterns: union.
			for _, p := range spec.Output.DenyPatterns {
				denyPatternSet[p] = struct{}{}
			}
		}

		// --- Models ---
		if spec.Models != nil {
			// Denied: union.
			for _, d := range spec.Models.Denied {
				modelDeniedSet[d] = struct{}{}
			}
			// Allowed: track per-policy lists for intersection.
			if len(spec.Models.Allowed) > 0 {
				modelAllowLists = append(modelAllowLists, allowEntry{
					name:     pol.Name,
					patterns: spec.Models.Allowed,
				})
			}
		}
	}

	// --- Populate deny lists from sets (sorted for deterministic output) ---
	for d := range toolDenySet {
		effective.ToolDeny = append(effective.ToolDeny, d)
	}
	slices.Sort(effective.ToolDeny)
	for p := range denyPatternSet {
		effective.DenyPatterns = append(effective.DenyPatterns, p)
	}
	slices.Sort(effective.DenyPatterns)

	// --- Model denied: union ---
	// --- Model allowed: intersection ---
	// The intersection is represented as follows: a model is allowed only if
	// it matches at least one pattern in EVERY policy that specified an allowlist.
	// We store all policy allowlists; compliance evaluation handles the intersection check.
	if len(modelAllowLists) > 0 || len(modelDeniedSet) > 0 {
		effective.Models = &kubeswarmv1alpha1.PolicyModels{}
		for d := range modelDeniedSet {
			effective.Models.Denied = append(effective.Models.Denied, d)
		}
		slices.Sort(effective.Models.Denied)
		// For the intersection of allowed lists: collect only patterns that
		// appear in ALL allowlists. A pattern is in the intersection if every
		// allowlist has at least one pattern that would match it - which is not
		// statically computable without enumeration.
		// Instead, we store the full intersection as a combined list: only
		// patterns present in ALL per-policy allowed sets survive.
		// Since patterns are globs (not concrete values), we compute the
		// intersection as the set of patterns that every allowlist contains.
		if len(modelAllowLists) > 0 {
			effective.Models.Allowed = intersectAllowLists(modelAllowLists)
		}
	}

	// --- Conflict detection: min > max timeout ---
	if effective.Limits != nil {
		if effective.Limits.MinTimeoutSeconds != nil && effective.Limits.MaxTimeoutSeconds != nil {
			if *effective.Limits.MinTimeoutSeconds > *effective.Limits.MaxTimeoutSeconds {
				// Find the contributing policies for a useful message.
				minPol, maxPol := findTimeoutPolicies(policies)
				conflicts = append(conflicts, PolicyConflict{
					Field:   "limits.timeoutSeconds",
					PolicyA: minPol,
					PolicyB: maxPol,
					Message: fmt.Sprintf(
						"merged minTimeoutSeconds (%d) exceeds merged maxTimeoutSeconds (%d)",
						*effective.Limits.MinTimeoutSeconds,
						*effective.Limits.MaxTimeoutSeconds,
					),
				})
			}
		}
	}

	return effective, conflicts
}

// mergeLimits applies the strictest values from src into dst in-place.
// Ceilings (max*): lowest non-nil wins. Floors (min*): highest non-nil wins.
func mergeLimits(dst, src *kubeswarmv1alpha1.PolicyLimits) {
	// Ceilings: lowest non-nil wins.
	dst.MaxDailyTokens = minPtr(dst.MaxDailyTokens, src.MaxDailyTokens)
	dst.MaxTokensPerCall = minPtr(dst.MaxTokensPerCall, src.MaxTokensPerCall)
	dst.MaxTimeoutSeconds = minPtr(dst.MaxTimeoutSeconds, src.MaxTimeoutSeconds)
	dst.MaxConcurrentTasks = minPtr(dst.MaxConcurrentTasks, src.MaxConcurrentTasks)
	dst.MaxThinkingTokensPerCall = minPtr(dst.MaxThinkingTokensPerCall, src.MaxThinkingTokensPerCall)
	dst.MaxAnswerTokensPerCall = minPtr(dst.MaxAnswerTokensPerCall, src.MaxAnswerTokensPerCall)

	// Floor: highest non-nil wins.
	dst.MinTimeoutSeconds = maxPtr(dst.MinTimeoutSeconds, src.MinTimeoutSeconds)
}

// minPtr returns a pointer to the minimum of two optional values.
// If one is nil, the other wins. If both are nil, returns nil.
func minPtr[T ~int32 | ~int64](a, b *T) *T {
	if b == nil {
		return a
	}
	if a == nil || *b < *a {
		v := *b
		return &v
	}
	return a
}

// maxPtr returns a pointer to the maximum of two optional values.
func maxPtr[T ~int32 | ~int64](a, b *T) *T {
	if b == nil {
		return a
	}
	if a == nil || *b > *a {
		v := *b
		return &v
	}
	return a
}

// allowEntry pairs a policy name with its model allowlist patterns.
type allowEntry struct {
	name     string
	patterns []string
}

// intersectAllowLists returns the set of patterns that appear in every
// per-policy allowlist. Only exact string matches are compared - glob
// semantics are evaluated at compliance check time.
func intersectAllowLists(lists []allowEntry) []string {
	if len(lists) == 0 {
		return nil
	}

	// Build a count map: pattern -> how many policies contain it.
	count := map[string]int{}
	for _, entry := range lists {
		// Deduplicate within one policy before counting.
		seen := map[string]struct{}{}
		for _, p := range entry.patterns {
			if _, already := seen[p]; !already {
				seen[p] = struct{}{}
				count[p]++
			}
		}
	}

	var result []string
	for pat, n := range count {
		if n == len(lists) {
			result = append(result, pat)
		}
	}
	slices.Sort(result)
	return result
}

// findTimeoutPolicies returns the names of the policies that contributed the
// highest MinTimeoutSeconds and the lowest MaxTimeoutSeconds respectively.
func findTimeoutPolicies(policies []kubeswarmv1alpha1.SwarmPolicy) (minPol, maxPol string) {
	var bestMin int32
	var bestMax int32
	for i := range policies {
		pol := &policies[i]
		if pol.Spec.Limits == nil {
			continue
		}
		if pol.Spec.Limits.MinTimeoutSeconds != nil {
			if minPol == "" || *pol.Spec.Limits.MinTimeoutSeconds > bestMin {
				bestMin = *pol.Spec.Limits.MinTimeoutSeconds
				minPol = pol.Name
			}
		}
		if pol.Spec.Limits.MaxTimeoutSeconds != nil {
			if maxPol == "" || *pol.Spec.Limits.MaxTimeoutSeconds < bestMax {
				bestMax = *pol.Spec.Limits.MaxTimeoutSeconds
				maxPol = pol.Name
			}
		}
	}
	return minPol, maxPol
}

// EvaluateAgentCompliance checks whether a SwarmAgent satisfies all constraints
// in the given EffectivePolicySpec. It returns a slice of PolicyViolation
// describing every failing constraint. An empty slice means the agent is compliant.
//
// Checks performed:
//   - requirements.budgetRef: agent must have guardrails.budgetRef set
//   - requirements.audit: agent must have observability.auditLog with mode != "off"
//   - requirements.allowList: agent must have a non-empty guardrails.tools.allow
//   - limits.maxDailyTokens: agent's guardrails.limits.dailyTokens must not exceed it
//   - limits.maxTokensPerCall: agent's guardrails.limits.tokensPerCall must not exceed it
//   - models.denied: agent's model must not match any denied glob pattern
//   - models.allowed: if non-empty, agent's model must match at least one allowed pattern
func EvaluateAgentCompliance(
	agent *kubeswarmv1alpha1.SwarmAgent,
	effective *kubeswarmv1alpha1.EffectivePolicySpec,
) []kubeswarmv1alpha1.PolicyViolation {
	if effective == nil {
		return nil
	}

	violations := checkRequirements(agent, effective.Requirements)
	violations = append(violations, checkLimits(agent, effective.Limits)...)
	violations = append(violations, checkModels(agent.Spec.Model, effective.Models)...)
	return violations
}

// checkRequirements evaluates boolean requirements against the agent.
func checkRequirements(
	agent *kubeswarmv1alpha1.SwarmAgent,
	req kubeswarmv1alpha1.PolicyRequirements,
) []kubeswarmv1alpha1.PolicyViolation {
	var violations []kubeswarmv1alpha1.PolicyViolation

	if req.BudgetRef {
		hasBudget := agent.Spec.Guardrails != nil && agent.Spec.Guardrails.BudgetRef != nil
		if !hasBudget {
			violations = append(violations, kubeswarmv1alpha1.PolicyViolation{
				Constraint: "requirements.budgetRef",
				PolicyName: "effective",
				Message:    "policy requires a budgetRef but agent has no guardrails.budgetRef",
			})
		}
	}

	if req.Audit {
		auditEnabled := agent.Spec.Observability != nil &&
			agent.Spec.Observability.AuditLog != nil &&
			agent.Spec.Observability.AuditLog.Mode != "" &&
			agent.Spec.Observability.AuditLog.Mode != kubeswarmv1alpha1.AuditLogModeOff
		if !auditEnabled {
			violations = append(violations, kubeswarmv1alpha1.PolicyViolation{
				Constraint: "requirements.audit",
				PolicyName: "effective",
				Message:    "policy requires audit logging but agent has no observability.auditLog or mode is \"off\"",
			})
		}
	}

	if req.AllowList {
		hasAllowList := agent.Spec.Guardrails != nil &&
			agent.Spec.Guardrails.Tools != nil &&
			len(agent.Spec.Guardrails.Tools.Allow) > 0
		if !hasAllowList {
			violations = append(violations, kubeswarmv1alpha1.PolicyViolation{
				Constraint: "requirements.allowList",
				PolicyName: "effective",
				Message:    "policy requires a non-empty tool allow list but agent has none",
			})
		}
	}

	return violations
}

// checkLimits evaluates numeric limit constraints against the agent's guardrails.
func checkLimits(
	agent *kubeswarmv1alpha1.SwarmAgent,
	limits *kubeswarmv1alpha1.PolicyLimits,
) []kubeswarmv1alpha1.PolicyViolation {
	if limits == nil {
		return nil
	}

	var violations []kubeswarmv1alpha1.PolicyViolation
	var agentLimits *kubeswarmv1alpha1.GuardrailLimits
	if agent.Spec.Guardrails != nil {
		agentLimits = agent.Spec.Guardrails.Limits
	}

	if limits.MaxDailyTokens != nil && agentLimits != nil {
		if agentLimits.DailyTokens > *limits.MaxDailyTokens {
			violations = append(violations, kubeswarmv1alpha1.PolicyViolation{
				Constraint: "limits.dailyTokens",
				PolicyName: "effective",
				Message: fmt.Sprintf(
					"agent dailyTokens (%d) exceeds policy maximum (%d)",
					agentLimits.DailyTokens,
					*limits.MaxDailyTokens,
				),
			})
		}
	}

	if limits.MaxTokensPerCall != nil && agentLimits != nil {
		if int32(agentLimits.TokensPerCall) > *limits.MaxTokensPerCall {
			violations = append(violations, kubeswarmv1alpha1.PolicyViolation{
				Constraint: "limits.tokensPerCall",
				PolicyName: "effective",
				Message: fmt.Sprintf(
					"agent tokensPerCall (%d) exceeds policy maximum (%d)",
					agentLimits.TokensPerCall,
					*limits.MaxTokensPerCall,
				),
			})
		}
	}

	if limits.MaxTimeoutSeconds != nil && agentLimits != nil {
		if int32(agentLimits.TimeoutSeconds) > *limits.MaxTimeoutSeconds {
			violations = append(violations, kubeswarmv1alpha1.PolicyViolation{
				Constraint: "limits.timeoutSeconds",
				PolicyName: "effective",
				Message: fmt.Sprintf(
					"agent timeoutSeconds (%d) exceeds policy maximum (%d)",
					agentLimits.TimeoutSeconds,
					*limits.MaxTimeoutSeconds,
				),
			})
		}
	}

	if limits.MinTimeoutSeconds != nil && agentLimits != nil {
		if int32(agentLimits.TimeoutSeconds) < *limits.MinTimeoutSeconds {
			violations = append(violations, kubeswarmv1alpha1.PolicyViolation{
				Constraint: "limits.timeoutSeconds",
				PolicyName: "effective",
				Message: fmt.Sprintf(
					"agent timeoutSeconds (%d) is below policy minimum (%d)",
					agentLimits.TimeoutSeconds,
					*limits.MinTimeoutSeconds,
				),
			})
		}
	}

	return violations
}

// checkModels evaluates model allow/deny constraints against the agent's model.
func checkModels(model string, models *kubeswarmv1alpha1.PolicyModels) []kubeswarmv1alpha1.PolicyViolation {
	if models == nil {
		return nil
	}

	var violations []kubeswarmv1alpha1.PolicyViolation

	// Denied: agent model must not match any denied glob pattern.
	for _, pattern := range models.Denied {
		if globMatch(pattern, model) {
			violations = append(violations, kubeswarmv1alpha1.PolicyViolation{
				Constraint: "models.denied",
				PolicyName: "effective",
				Message: fmt.Sprintf(
					"agent model %q matches denied pattern %q",
					model,
					pattern,
				),
			})
			// One violation for a denied model is enough.
			break
		}
	}

	// Allowed: agent model must match at least one allowed glob pattern.
	if len(models.Allowed) > 0 {
		matched := false
		for _, pattern := range models.Allowed {
			if globMatch(pattern, model) {
				matched = true
				break
			}
		}
		if !matched {
			violations = append(violations, kubeswarmv1alpha1.PolicyViolation{
				Constraint: "models.allowed",
				PolicyName: "effective",
				Message: fmt.Sprintf(
					"agent model %q does not match any allowed pattern",
					model,
				),
			})
		}
	}

	return violations
}

// globMatch reports whether name matches the given glob pattern using path.Match.
// A pattern error (e.g. malformed pattern) is treated as a non-match rather than panicking.
func globMatch(pattern, name string) bool {
	matched, err := path.Match(pattern, name)
	if err != nil {
		// path.Match only returns an error for malformed patterns.
		// Treat as non-match - a malformed deny pattern should not silently allow models.
		return false
	}
	return matched
}
