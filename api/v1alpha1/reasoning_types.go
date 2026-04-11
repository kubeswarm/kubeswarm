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

// ReasoningMode controls whether reasoning is enabled for the agent.
// +kubebuilder:validation:Enum=Disabled;Auto;Explicit
type ReasoningMode string

const (
	// ReasoningDisabled turns reasoning off even for reasoning-capable models.
	ReasoningDisabled ReasoningMode = "Disabled"
	// ReasoningAuto enables reasoning when the model supports it, silent no-op otherwise.
	ReasoningAuto ReasoningMode = "Auto"
	// ReasoningExplicit requires reasoning; reconcile fails on non-reasoning-capable models.
	ReasoningExplicit ReasoningMode = "Explicit"
)

// ReasoningEffort is the provider-neutral effort hint. Translated to the
// vendor's native effort value at the runtime boundary (OpenAI takes lowercase
// low/medium/high; Anthropic ignores this field).
// +kubebuilder:validation:Enum=Low;Medium;High
type ReasoningEffort string

const (
	ReasoningEffortLow    ReasoningEffort = "Low"
	ReasoningEffortMedium ReasoningEffort = "Medium"
	ReasoningEffortHigh   ReasoningEffort = "High"
)

// ReasoningConfig configures reasoning behavior for a SwarmAgent.
//
// Provider field applicability (surfaced via kubectl explain):
//   - Anthropic: BudgetTokens is honored; Effort is ignored and emits
//     ReasoningFieldIgnored if set.
//   - OpenAI o-series: Effort is honored; BudgetTokens is ignored and emits
//     ReasoningFieldIgnored if set.
//   - Non-reasoning models under mode Auto: entire block is silently ignored
//     and a ReasoningIgnored Event fires. ReasoningActive condition is set
//     to IgnoredModelNotCapable.
//   - Non-reasoning models under mode Explicit: reconcile fails.
//
// +kubebuilder:validation:XValidation:rule="self.mode != 'Disabled' || (!has(self.budgetTokens) && !has(self.effort))",message="budgetTokens and effort must not be set when mode is Disabled"
// +kubebuilder:validation:XValidation:rule="self.mode != 'Explicit' || has(self.budgetTokens) || has(self.effort)",message="mode Explicit requires at least one of budgetTokens or effort to be set"
type ReasoningConfig struct {
	// Mode controls whether reasoning is enabled. Default Disabled.
	// +kubebuilder:default=Disabled
	// +optional
	Mode ReasoningMode `json:"mode,omitempty"`

	// Effort is the provider-neutral effort hint for OpenAI o-series models.
	// Ignored by Anthropic; emits a ReasoningFieldIgnored Event on mismatch.
	// +optional
	Effort ReasoningEffort `json:"effort,omitempty"`

	// BudgetTokens is the Anthropic thinking-token budget. Clamped to
	// spec.guardrails.limits.maxThinkingTokensPerCall when both are set.
	// Ignored by OpenAI o-series; emits a ReasoningFieldIgnored Event on
	// mismatch. The upper bound of 200000 is the current Anthropic ceiling
	// and may change as vendors update their APIs; operators who need a
	// higher value should request a bump in a follow-up RFC.
	// +optional
	// +kubebuilder:validation:Minimum=1024
	// +kubebuilder:validation:Maximum=200000
	BudgetTokens *int32 `json:"budgetTokens,omitempty"`
}

// ReasoningDefaults is the SwarmSettings-facing shape of ReasoningConfig.
// It is structurally identical to ReasoningConfig except Mode has no
// kubebuilder default - an unset SwarmSettings.defaults.reasoning.mode
// means "no namespace default" and the agent-level default (Disabled)
// applies directly. Without this split, the Mode default marker on
// ReasoningConfig would cause every SwarmSettings object to implicitly
// cascade mode: Disabled even when the admin never wrote it, which would
// mask agent-level Auto overrides under the RFC-0012 cascade rules.
type ReasoningDefaults struct {
	// Mode is the namespace-wide default reasoning mode. Leave unset to
	// mean "no default" (agents use their own default of Disabled).
	// +optional
	Mode ReasoningMode `json:"mode,omitempty"`

	// Effort is the namespace-wide default OpenAI effort hint.
	// +optional
	Effort ReasoningEffort `json:"effort,omitempty"`

	// BudgetTokens is the namespace-wide default Anthropic thinking budget.
	// +optional
	// +kubebuilder:validation:Minimum=1024
	// +kubebuilder:validation:Maximum=200000
	BudgetTokens *int32 `json:"budgetTokens,omitempty"`
}

// Reasoning status condition and reasons.
const (
	// ConditionReasoningActive reports the runtime state of the reasoning
	// subsystem for this agent.
	ConditionReasoningActive = "ReasoningActive"

	// Reasons for ConditionReasoningActive
	ReasoningReasonDisabled                = "Disabled"
	ReasoningReasonActive                  = "Active"
	ReasoningReasonIgnoredModelNotCapable  = "IgnoredModelNotCapable"
	ReasoningReasonClampedByGuardrail      = "ClampedByGuardrail"
	ReasoningReasonFieldIgnored            = "FieldIgnored"
	ReasoningReasonRejectedModelNotCapable = "RejectedModelNotCapable"
)
