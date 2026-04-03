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

// RegistryScope controls which SwarmAgents are indexed by an SwarmRegistry.
// +kubebuilder:validation:Enum=namespace-scoped;cluster-wide
type RegistryScope string

const (
	// RegistryScopeNamespace indexes only SwarmAgents in the same namespace (default).
	RegistryScopeNamespace RegistryScope = "namespace-scoped"
	// RegistryScopeCluster indexes all SwarmAgents cluster-wide. Requires a ClusterRole
	// that grants cross-namespace SwarmAgent reads.
	RegistryScopeCluster RegistryScope = "cluster-wide"
)

// SwarmRegistryPolicy controls delegation safety for registry-resolved steps.
type SwarmRegistryPolicy struct {
	// MaxDepth is the maximum agent-to-agent delegation depth.
	// Prevents runaway recursion.
	// +kubebuilder:default=3
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=20
	MaxDepth int `json:"maxDepth,omitempty"`

	// AllowCrossTeam permits resolution of agents managed by other SwarmTeams.
	// Default false — only agents not owned by another team's inline roles.
	// +kubebuilder:default=false
	AllowCrossTeam bool `json:"allowCrossTeam,omitempty"`
}

// MCPBinding maps a capability ID to the MCP server URL that provides it in this deployment.
// Used to resolve MCPServerSpec.capabilityRef at reconcile time so that shared agent
// definitions (e.g. from cookbook) remain URL-free.
type MCPBinding struct {
	// CapabilityID is the capability identifier to resolve.
	// Must match MCPServerSpec.capabilityRef on the referencing agent.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	CapabilityID string `json:"capabilityId"`
	// URL is the SSE endpoint of the MCP server that provides this capability.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	URL string `json:"url"`
}

// SwarmRegistrySpec defines the desired state of SwarmRegistry.
type SwarmRegistrySpec struct {
	// Scope controls which SwarmAgents are indexed.
	// namespace-scoped: only SwarmAgents in the same namespace (default).
	// cluster-wide: all SwarmAgents cluster-wide (requires ClusterRole).
	// +kubebuilder:default=namespace-scoped
	// +kubebuilder:validation:Enum=namespace-scoped;cluster-wide
	Scope RegistryScope `json:"scope,omitempty"`

	// Policy controls delegation safety.
	// +optional
	Policy *SwarmRegistryPolicy `json:"policy,omitempty"`

	// MCPBindings maps capability IDs to MCP server URLs for this deployment.
	// Agents that declare mcpServers with capabilityRef have their URLs resolved
	// from this list at reconcile time. This allows cookbook-style agent definitions
	// to remain URL-free; operators supply the bindings per namespace.
	// +optional
	MCPBindings []MCPBinding `json:"mcpBindings,omitempty"`
}

// IndexedCapability is one capability entry in the SwarmRegistry status index.
type IndexedCapability struct {
	// ID is the capability identifier.
	ID string `json:"id"`
	// Description is the human-readable description of the capability, taken from the
	// first agent that declares it. Used by the router LLM to select the right agent.
	Description string `json:"description,omitempty"`
	// Agents is the list of SwarmAgent names that advertise this capability.
	Agents []string `json:"agents,omitempty"`
	// Tags is the union of all tags declared for this capability across all agents.
	Tags []string `json:"tags,omitempty"`
}

// AgentFleetEntry is one agent's summary in the registry fleet view.
type AgentFleetEntry struct {
	// Name is the SwarmAgent name.
	Name string `json:"name"`
	// Model is the LLM model this agent is configured to use.
	Model string `json:"model"`
	// ReadyReplicas is the number of agent pods currently ready.
	ReadyReplicas int32 `json:"readyReplicas"`
	// DailyTokens is the rolling 24h token usage copied from SwarmAgent.status.
	// +optional
	DailyTokens int64 `json:"dailyTokens,omitempty"`
	// Capabilities lists the capability IDs this agent contributes to the index.
	// +optional
	Capabilities []string `json:"capabilities,omitempty"`
}

// SwarmRegistryStatus defines the observed state of SwarmRegistry.
type SwarmRegistryStatus struct {
	// IndexedAgents is the total number of SwarmAgents indexed by this registry.
	IndexedAgents int `json:"indexedAgents,omitempty"`

	// Fleet is the list of SwarmAgents currently registered with this registry,
	// with per-agent readiness and token usage. Replaces the implicit
	// "all agents in namespace" model with an explicit opt-in list.
	// +optional
	Fleet []AgentFleetEntry `json:"fleet,omitempty"`

	// LastRebuild is the time the index was last rebuilt.
	// +optional
	LastRebuild *metav1.Time `json:"lastRebuild,omitempty"`

	// Capabilities lists all capabilities indexed, with their associated agents and tags.
	// +optional
	Capabilities []IndexedCapability `json:"capabilities,omitempty"`

	// ObservedGeneration is the .metadata.generation this status reflects.
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Conditions reflect the current state of the SwarmRegistry.
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Scope",type=string,JSONPath=`.spec.scope`
// +kubebuilder:printcolumn:name="Agents",type=integer,JSONPath=`.status.indexedAgents`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
// +kubebuilder:resource:shortName={swreg,swregs},scope=Namespaced,categories=kubeswarm

// SwarmRegistry is a Kubernetes-native capability index that lets pipeline steps
// resolve an agent at runtime by what it can do rather than by a hardcoded name.
// Agents advertise capabilities via spec.capabilities on SwarmAgentSpec.
type SwarmRegistry struct {
	metav1.TypeMeta `json:",inline"`

	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// +optional
	Spec SwarmRegistrySpec `json:"spec,omitempty"`

	// +optional
	Status SwarmRegistryStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// SwarmRegistryList contains a list of SwarmRegistry.
type SwarmRegistryList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []SwarmRegistry `json:"items"`
}

func init() {
	SchemeBuilder.Register(&SwarmRegistry{}, &SwarmRegistryList{})
}
