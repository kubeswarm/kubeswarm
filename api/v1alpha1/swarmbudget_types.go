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

// BudgetStatus is the phase summary for an SwarmBudget.
// +kubebuilder:validation:Enum=OK;Warning;Exceeded
type BudgetStatus string

const (
	BudgetStatusOK       BudgetStatus = "OK"
	BudgetStatusWarning  BudgetStatus = "Warning"
	BudgetStatusExceeded BudgetStatus = "Exceeded"
)

// SwarmBudgetSelector scopes a budget to a subset of resources.
type SwarmBudgetSelector struct {
	// Namespace scopes this budget to a single namespace.
	// Empty means all namespaces in the cluster.
	// +optional
	Namespace string `json:"namespace,omitempty"`

	// Team scopes this budget to a single SwarmTeam by name.
	// Empty means all teams in the selected namespace(s).
	// +optional
	Team string `json:"team,omitempty"`

	// MatchLabels selects SwarmTeams by label. Applied in addition to Namespace/Team.
	// +optional
	MatchLabels map[string]string `json:"matchLabels,omitempty"`
}

// SwarmBudgetSpec defines the desired state of an SwarmBudget.
type SwarmBudgetSpec struct {
	// Selector scopes this budget to matching resources.
	// +kubebuilder:validation:Required
	Selector SwarmBudgetSelector `json:"selector"`

	// Period is the rolling window for spend accumulation.
	// +kubebuilder:validation:Enum=daily;weekly;monthly
	// +kubebuilder:default=monthly
	// +optional
	Period string `json:"period,omitempty"`

	// Limit is the maximum spend in the configured currency for one period.
	// Value is a decimal string (e.g., "100.00").
	// +kubebuilder:validation:Pattern=`^[0-9]+(\.[0-9]+)?$`
	Limit string `json:"limit"`

	// Currency is the ISO 4217 currency code. Must match the operator's CostProvider.
	// +kubebuilder:default=USD
	// +optional
	Currency string `json:"currency,omitempty"`

	// WarnAt is the percentage of the limit at which a BudgetWarning notification fires (0–100).
	// Zero disables warnings. Default: 80.
	// +kubebuilder:default=80
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=100
	// +optional
	WarnAt int `json:"warnAt,omitempty"`

	// HardStop blocks new SwarmRuns when the limit is exceeded.
	// When false (default), runs continue but a BudgetExceeded notification fires.
	// When true, new SwarmRuns fail immediately with BudgetExceeded before any tokens are spent.
	// +kubebuilder:default=false
	// +optional
	HardStop bool `json:"hardStop,omitempty"`

	// NotifyRef references an SwarmNotify policy for budget alerts.
	// Fires BudgetWarning (at warnAt%) and BudgetExceeded (at 100%).
	// +optional
	NotifyRef *LocalObjectReference `json:"notifyRef,omitempty"`
}

// SwarmBudgetStatus defines the observed state of an SwarmBudget.
type SwarmBudgetStatus struct {
	// Phase summarises the current budget state.
	// +kubebuilder:validation:Enum=OK;Warning;Exceeded
	Phase BudgetStatus `json:"phase,omitempty"`

	// SpentUSD is the total spend in the current period window as a decimal string.
	SpentUSD string `json:"spentUSD,omitempty"`

	// PctUsed is SpentUSD / Limit as a percentage string (0-100).
	PctUsed string `json:"pctUsed,omitempty"`

	// PeriodStart is the start of the current budget window.
	// +optional
	PeriodStart *metav1.Time `json:"periodStart,omitempty"`

	// LastUpdated is when the status was last recalculated.
	// +optional
	LastUpdated *metav1.Time `json:"lastUpdated,omitempty"`

	// ObservedGeneration is the .metadata.generation this status reflects.
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Conditions reflect the current state of the budget.
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Spent",type=number,JSONPath=`.status.spentUSD`
// +kubebuilder:printcolumn:name="Limit",type=number,JSONPath=`.spec.limit`
// +kubebuilder:printcolumn:name="Used%",type=number,JSONPath=`.status.pctUsed`
// +kubebuilder:printcolumn:name="Period",type=string,JSONPath=`.spec.period`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
// +kubebuilder:resource:shortName={swbgt,swbgts},scope=Namespaced,categories=kubeswarm

// SwarmBudget defines a spend limit for one or more SwarmTeams.
// The SwarmBudgetController recalculates status every 5 minutes by querying the
// configured SpendStore. When hardStop is true, the SwarmRunReconciler blocks new
// runs that would violate the budget.
type SwarmBudget struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// +required
	Spec SwarmBudgetSpec `json:"spec"`

	// +optional
	Status SwarmBudgetStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// SwarmBudgetList contains a list of SwarmBudget.
type SwarmBudgetList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []SwarmBudget `json:"items"`
}

func init() {
	SchemeBuilder.Register(&SwarmBudget{}, &SwarmBudgetList{})
}
