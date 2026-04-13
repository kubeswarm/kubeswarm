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

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// -----------------------------------------------------------------------------
// Enums
// -----------------------------------------------------------------------------

// PolicyEnforcementMode controls whether the policy rejects, warns, or only audits.
// +kubebuilder:validation:Enum=Audit;Warn;Enforce
type PolicyEnforcementMode string

const (
	// PolicyEnforcementAudit logs violations without rejecting. Default.
	PolicyEnforcementAudit PolicyEnforcementMode = "Audit"
	// PolicyEnforcementWarn returns admission warnings visible in kubectl
	// output and logs violations. Does not reject.
	PolicyEnforcementWarn PolicyEnforcementMode = "Warn"
	// PolicyEnforcementEnforce rejects non-compliant agents at admission.
	PolicyEnforcementEnforce PolicyEnforcementMode = "Enforce"
)

// PolicyOutputLevel defines the minimum validation level required.
// Ordering: semantic (strictest) > schema > pattern > none (most permissive).
// Each level is independent - schema does not require pattern.
// +kubebuilder:validation:Enum=none;pattern;schema;semantic
type PolicyOutputLevel string

const (
	PolicyOutputLevelNone     PolicyOutputLevel = "none"
	PolicyOutputLevelPattern  PolicyOutputLevel = "pattern"
	PolicyOutputLevelSchema   PolicyOutputLevel = "schema"
	PolicyOutputLevelSemantic PolicyOutputLevel = "semantic"
)

// -----------------------------------------------------------------------------
// Policy sub-types
// -----------------------------------------------------------------------------

// PolicyLimits defines ceilings and floors for agent execution parameters.
// All fields are pointers: nil means "no constraint from this policy."
// When multiple policies exist, the strictest non-nil value wins.
// All token fields refer to total tokens (input + output) unless explicitly
// suffixed. Cached/prompt-cached tokens count toward limits (conservative default).
//
// +kubebuilder:validation:XValidation:rule="!has(self.minTimeoutSeconds) || !has(self.maxTimeoutSeconds) || self.minTimeoutSeconds <= self.maxTimeoutSeconds",message="minTimeoutSeconds must not exceed maxTimeoutSeconds"
type PolicyLimits struct {
	// MaxDailyTokens is the ceiling on guardrails.limits.dailyTokens.
	// An agent requesting more is rejected (Enforce), warned (Warn),
	// or flagged (Audit). An agent omitting dailyTokens gets this as
	// the effective limit at runtime.
	// +kubebuilder:validation:Minimum=1
	// +optional
	MaxDailyTokens *int64 `json:"maxDailyTokens,omitempty"`

	// MaxTokensPerCall is the ceiling on guardrails.limits.tokensPerCall.
	// +kubebuilder:validation:Minimum=1
	// +optional
	MaxTokensPerCall *int32 `json:"maxTokensPerCall,omitempty"`

	// MaxTimeoutSeconds is the ceiling on guardrails.limits.timeoutSeconds.
	// +kubebuilder:validation:Minimum=1
	// +optional
	MaxTimeoutSeconds *int32 `json:"maxTimeoutSeconds,omitempty"`

	// MinTimeoutSeconds is the floor on guardrails.limits.timeoutSeconds.
	// Prevents agents from setting unreasonably short timeouts.
	// +kubebuilder:validation:Minimum=1
	// +optional
	MinTimeoutSeconds *int32 `json:"minTimeoutSeconds,omitempty"`

	// MaxConcurrentTasks is the ceiling on guardrails.limits.concurrentTasks.
	// +kubebuilder:validation:Minimum=1
	// +optional
	MaxConcurrentTasks *int32 `json:"maxConcurrentTasks,omitempty"`

	// MaxThinkingTokensPerCall is the ceiling on guardrails.limits.maxThinkingTokensPerCall.
	// +kubebuilder:validation:Minimum=1
	// +optional
	MaxThinkingTokensPerCall *int32 `json:"maxThinkingTokensPerCall,omitempty"`

	// MaxAnswerTokensPerCall is the ceiling on guardrails.limits.maxAnswerTokensPerCall.
	// +kubebuilder:validation:Minimum=1
	// +optional
	MaxAnswerTokensPerCall *int32 `json:"maxAnswerTokensPerCall,omitempty"`
}

// PolicyTools defines tool access policy enforced at runtime.
// Deny entries use glob patterns (not regex). Exact match or wildcard
// with `*`. Examples: "shell/*" (all shell tools), "filesystem/write_file"
// (exact tool), "*/execute_code" (tool across all namespaces).
type PolicyTools struct {
	// Deny is a deny list merged with each agent's guardrails.tools.deny.
	// Agents cannot remove entries from the policy deny list. Deny always
	// takes precedence over allow.
	// +optional
	// +kubebuilder:validation:MaxItems=100
	Deny []string `json:"deny,omitempty"`

	// ForceTrustLevel sets the minimum trust level for all agents.
	// Agents cannot use a more permissive level.
	// Ordering: sandbox (strictest) > external > internal (most permissive).
	// +kubebuilder:validation:Enum=internal;external;sandbox
	// +optional
	ForceTrustLevel *ToolTrustLevel `json:"forceTrustLevel,omitempty"`
}

// PolicyOutput defines output validation requirements.
type PolicyOutput struct {
	// MinValidation is the minimum validation level required on all
	// SwarmTeam steps referencing agents in this namespace.
	// +kubebuilder:default=none
	// +optional
	MinValidation PolicyOutputLevel `json:"minValidation,omitempty"`

	// DenyPatterns are RE2 regex patterns merged into every step's
	// rejectPatterns at runtime. Invalid regexes are rejected at admission.
	// +optional
	// +kubebuilder:validation:MaxItems=50
	DenyPatterns []string `json:"denyPatterns,omitempty"`
}

// PolicyModels restricts which models agents may use. Both fields support
// glob patterns: exact match or wildcard with `*`. Deny takes precedence
// over allow.
type PolicyModels struct {
	// Allowed is a list of glob patterns for permitted models.
	// When multiple policies specify allowed lists, the intersection is used.
	// +optional
	// +kubebuilder:validation:MaxItems=100
	Allowed []string `json:"allowed,omitempty"`

	// Denied is a list of glob patterns for forbidden models.
	// When multiple policies specify denied lists, the union is used.
	// +optional
	// +kubebuilder:validation:MaxItems=100
	Denied []string `json:"denied,omitempty"`
}

// PolicyRequirements groups boolean requirements that all agents must satisfy.
type PolicyRequirements struct {
	// BudgetRef requires all agents in the namespace to reference a SwarmBudget.
	// +optional
	BudgetRef bool `json:"budgetRef,omitempty"`

	// Audit requires all agents to have audit logging enabled.
	// +optional
	Audit bool `json:"audit,omitempty"`

	// AllowList requires all agents to have a non-empty tool allow list.
	// +optional
	AllowList bool `json:"allowList,omitempty"`
}

// -----------------------------------------------------------------------------
// Condition types and labels
// -----------------------------------------------------------------------------

// ConditionEnforcing indicates the policy is active.
const ConditionEnforcing = "Enforcing"

// ConditionConflicting indicates merged policies produce impossible constraints
// (e.g. minTimeoutSeconds > maxTimeoutSeconds across policies).
const ConditionConflicting = "Conflicting"

// ConditionPolicyCompliant is set on SwarmAgent by the SwarmPolicy controller.
// True when the agent satisfies all policy constraints in the namespace.
const ConditionPolicyCompliant = "PolicyCompliant"

const (
	// LabelPolicyCompliant is set on SwarmAgent to "true" or "false".
	// Removed when no policies exist in the namespace.
	LabelPolicyCompliant = "kubeswarm.io/policy-compliant"

	// LabelPolicyGoverned is set on namespaces that contain at least one SwarmPolicy.
	// Used by the admission webhook's namespaceSelector.
	LabelPolicyGoverned = "kubeswarm.io/policy-governed"
)

// -----------------------------------------------------------------------------
// SwarmPolicySpec
// -----------------------------------------------------------------------------

// SwarmPolicySpec defines the policy constraints.
// +kubebuilder:validation:XValidation:rule="has(self.limits) || has(self.tools) || has(self.output) || has(self.models) || self.requirements.budgetRef || self.requirements.audit || self.requirements.allowList",message="policy must define at least one constraint"
type SwarmPolicySpec struct {
	// EnforcementMode controls whether violations cause admission rejection
	// (Enforce), admission warnings (Warn), or are only logged (Audit).
	// Default: Audit.
	// +kubebuilder:default=Audit
	// +optional
	EnforcementMode PolicyEnforcementMode `json:"enforcementMode,omitempty"`

	// Limits sets ceilings and floors on agent execution parameters.
	// +optional
	Limits *PolicyLimits `json:"limits,omitempty"`

	// Tools sets tool access restrictions.
	// +optional
	Tools *PolicyTools `json:"tools,omitempty"`

	// Output sets minimum output validation requirements.
	// +optional
	Output *PolicyOutput `json:"output,omitempty"`

	// Models restricts which models agents may use.
	// +optional
	Models *PolicyModels `json:"models,omitempty"`

	// Requirements defines boolean requirements that all agents must satisfy.
	// +optional
	Requirements PolicyRequirements `json:"requirements,omitempty"`
}

// -----------------------------------------------------------------------------
// Compliance and provenance types
// -----------------------------------------------------------------------------

// PolicyViolation describes one specific constraint an agent violates.
type PolicyViolation struct {
	// Constraint identifies the violated policy field.
	Constraint string `json:"constraint"`

	// PolicyName is the SwarmPolicy that defines the constraint.
	PolicyName string `json:"policyName"`

	// Message is a human-readable description of the violation.
	Message string `json:"message"`
}

// NOTE: GuardrailProvenance and EffectiveGuardrailEntry are defined in the RFC
// but deferred to Phase 4 when provenance tracking on SwarmAgent status is implemented.

// -----------------------------------------------------------------------------
// EffectivePolicySpec
// -----------------------------------------------------------------------------

// EffectivePolicySpec is the merged result of all SwarmPolicies in the namespace.
// Read-only, computed by the controller.
type EffectivePolicySpec struct {
	// Limits is the merged ceiling/floor result.
	// +optional
	Limits *PolicyLimits `json:"limits,omitempty"`

	// ToolDeny is the union of all policy deny lists.
	// +optional
	// +kubebuilder:validation:MaxItems=100
	ToolDeny []string `json:"toolDeny,omitempty"`

	// ForceTrustLevel is the strictest trust level across all policies.
	// +optional
	ForceTrustLevel *ToolTrustLevel `json:"forceTrustLevel,omitempty"`

	// MinValidation is the strictest validation level across all policies.
	MinValidation PolicyOutputLevel `json:"minValidation,omitempty"`

	// DenyPatterns is the union of all policy output deny patterns.
	// +optional
	// +kubebuilder:validation:MaxItems=50
	DenyPatterns []string `json:"denyPatterns,omitempty"`

	// Models is the merged model restriction.
	// +optional
	Models *PolicyModels `json:"models,omitempty"`

	// Requirements is the merged boolean requirements (OR across policies).
	Requirements PolicyRequirements `json:"requirements,omitempty"`

	// EnforcementMode is the strictest mode across all policies.
	// Enforce > Warn > Audit.
	EnforcementMode PolicyEnforcementMode `json:"enforcementMode,omitempty"`
}

// -----------------------------------------------------------------------------
// SwarmPolicyStatus
// -----------------------------------------------------------------------------

// SwarmPolicyStatus reports the compliance state of agents in the namespace.
type SwarmPolicyStatus struct {
	// AgentCount is the total number of SwarmAgents in the namespace.
	AgentCount int `json:"agentCount"`

	// CompliantCount is the number of agents satisfying all policy constraints.
	CompliantCount int `json:"compliantCount"`

	// EffectivePolicy is the merged result of all SwarmPolicies in the namespace.
	// +optional
	EffectivePolicy *EffectivePolicySpec `json:"effectivePolicy,omitempty"`

	// ObservedGeneration is the .metadata.generation this status reflects.
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Conditions reflect the policy controller's state.
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// -----------------------------------------------------------------------------
// Root types
// -----------------------------------------------------------------------------

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName={swpol,swpols},categories=kubeswarm
// +kubebuilder:printcolumn:name="Mode",type=string,JSONPath=`.spec.enforcementMode`
// +kubebuilder:printcolumn:name="Agents",type=integer,JSONPath=`.status.agentCount`
// +kubebuilder:printcolumn:name="Compliant",type=integer,JSONPath=`.status.compliantCount`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// SwarmPolicy defines platform-level guardrails enforced on all SwarmAgents
// in the namespace. Agent authors cannot weaken policy constraints.
type SwarmPolicy struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              SwarmPolicySpec   `json:"spec"`
	Status            SwarmPolicyStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// SwarmPolicyList contains a list of SwarmPolicy.
type SwarmPolicyList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []SwarmPolicy `json:"items"`
}

func init() {
	SchemeBuilder.Register(&SwarmPolicy{}, &SwarmPolicyList{})
}
