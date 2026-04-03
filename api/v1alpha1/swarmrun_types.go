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

// SwarmRunPhase describes the overall execution state of an SwarmRun.
// +kubebuilder:validation:Enum=Pending;Running;Succeeded;Failed
type SwarmRunPhase string

const (
	SwarmRunPhasePending   SwarmRunPhase = "Pending"
	SwarmRunPhaseRunning   SwarmRunPhase = "Running"
	SwarmRunPhaseSucceeded SwarmRunPhase = "Succeeded"
	SwarmRunPhaseFailed    SwarmRunPhase = "Failed"
)

// SwarmRunSpec is an immutable snapshot of everything needed to execute one run.
// Exactly one of TeamRef or Agent must be set.
// For team runs it is populated at trigger time from the parent SwarmTeam spec.
// For standalone agent runs only Agent and Prompt are required.
//
// +kubebuilder:validation:XValidation:rule="has(self.teamRef) != has(self.agent)",message="exactly one of teamRef or agent must be set"
type SwarmRunSpec struct {
	// TeamRef is the name of the SwarmTeam that owns this run.
	// Exactly one of TeamRef or Agent must be set.
	// +optional
	TeamRef string `json:"teamRef,omitempty"`

	// Agent is the name of the SwarmAgent to invoke for a standalone run.
	// Exactly one of TeamRef or Agent must be set.
	// +optional
	Agent string `json:"agent,omitempty"`

	// Prompt is the task text submitted to the agent for a standalone run.
	// Required when Agent is set.
	// +optional
	Prompt string `json:"prompt,omitempty"`

	// TeamGeneration is the SwarmTeam spec.generation at the time this run was
	// created. Allows correlating a run with the exact team spec that was in effect.
	// Only set for team runs.
	// +optional
	TeamGeneration int64 `json:"teamGeneration,omitempty"`

	// Input is the resolved input map for this run: team default inputs merged with
	// any per-trigger overrides supplied via swarm trigger --input or SwarmEvent.
	// Step inputs reference these values via "{{ .input.<key> }}".
	// +optional
	Input map[string]string `json:"input,omitempty"`

	// Pipeline is a snapshot of the SwarmTeam pipeline DAG at trigger time.
	// Empty for routed-mode runs.
	// +optional
	Pipeline []SwarmTeamPipelineStep `json:"pipeline,omitempty"`

	// DefaultContextPolicy is a snapshot of the team's defaultContextPolicy at trigger time.
	// Applied to non-adjacent step references; per-step contextPolicy takes precedence.
	// +optional
	DefaultContextPolicy *StepContextPolicy `json:"defaultContextPolicy,omitempty"`

	// Roles is a snapshot of the SwarmTeam role definitions at trigger time.
	// Empty for routed-mode runs.
	// +optional
	Roles []SwarmTeamRole `json:"roles,omitempty"`

	// Output is a Go template expression that selects the final run result.
	// Example: "{{ .steps.summarize.output }}"
	// For routed-mode runs this defaults to "{{ .steps.route.output }}" at trigger time.
	// +optional
	Output string `json:"output,omitempty"`

	// Routing is a snapshot of the SwarmTeam routing config at trigger time.
	// Set when the team operates in routed mode. Mutually exclusive with Pipeline.
	// +optional
	Routing *SwarmTeamRoutingSpec `json:"routing,omitempty"`

	// TimeoutSeconds is the maximum wall-clock seconds this run may take.
	// Zero means no timeout.
	// +kubebuilder:validation:Minimum=1
	// +optional
	TimeoutSeconds int `json:"timeoutSeconds,omitempty"`

	// MaxTokens is the total token budget for this run across all steps.
	// Zero means no limit.
	// +kubebuilder:validation:Minimum=1
	// +optional
	MaxTokens int64 `json:"maxTokens,omitempty"`
}

// SwarmRunStatus defines the observed execution state of an SwarmRun.
type SwarmRunStatus struct {
	// Phase is the overall execution state.
	Phase SwarmRunPhase `json:"phase,omitempty"`

	// Steps holds the per-step execution state for this run, including full
	// step outputs. Unlike SwarmTeam.Status, this is never reset — it is the
	// permanent record of what happened during this run.
	Steps []SwarmFlowStepStatus `json:"steps,omitempty"`

	// Output is the resolved final pipeline output once phase is Succeeded.
	Output string `json:"output,omitempty"`

	// StartTime is when this run began executing.
	StartTime *metav1.Time `json:"startTime,omitempty"`

	// CompletionTime is when this run reached a terminal phase (Succeeded or Failed).
	CompletionTime *metav1.Time `json:"completionTime,omitempty"`

	// TotalTokenUsage is the sum of token usage across all steps in this run.
	TotalTokenUsage *TokenUsage `json:"totalTokenUsage,omitempty"`

	// TotalCostUSD is the estimated total dollar cost of this run, summed across
	// all steps using the operator's configured CostProvider.
	TotalCostUSD float64 `json:"totalCostUSD,omitempty"`

	// ObservedGeneration is the .metadata.generation this status reflects.
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Conditions reflect the current state of the SwarmRun.
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Team",type=string,JSONPath=`.spec.teamRef`,priority=0
// +kubebuilder:printcolumn:name="Agent",type=string,JSONPath=`.spec.agent`,priority=0
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Tokens",type=integer,JSONPath=`.status.totalTokenUsage.totalTokens`
// +kubebuilder:printcolumn:name="Cost($)",type=number,JSONPath=`.status.totalCostUSD`
// +kubebuilder:printcolumn:name="Started",type=date,JSONPath=`.status.startTime`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
// +kubebuilder:resource:shortName={swrun,swruns},scope=Namespaced,categories=kubeswarm

// SwarmRun is an immutable execution record. It covers both standalone agent
// invocations (spec.agent + spec.prompt) and team pipeline runs (spec.teamRef).
// Created automatically by SwarmEvent or directly via kubectl apply.
type SwarmRun struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// +required
	Spec SwarmRunSpec `json:"spec"`

	// +optional
	Status SwarmRunStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// SwarmRunList contains a list of SwarmRun.
type SwarmRunList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []SwarmRun `json:"items"`
}

func init() {
	SchemeBuilder.Register(&SwarmRun{}, &SwarmRunList{})
}
