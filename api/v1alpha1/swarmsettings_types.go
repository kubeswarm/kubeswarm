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

// MemoryBackend defines the memory storage strategy for an agent.
// +kubebuilder:validation:Enum=in-context;vector-store;redis
type MemoryBackend string

const (
	MemoryBackendInContext   MemoryBackend = "in-context"
	MemoryBackendVectorStore MemoryBackend = "vector-store"
	MemoryBackendRedis       MemoryBackend = "redis"
)

// PromptFragment is one composable piece of text injected into the agent system prompt.
// Fragments from multiple SwarmSettings objects are composed using the settingsRefs list on
// SwarmAgent or SwarmTeamRole. Within a single SwarmSettings, fragments are applied in list order.
// When the same fragment name appears in multiple referenced settings, the last occurrence wins.
type PromptFragment struct {
	// Name identifies this fragment. Must be unique within the SwarmSettings object.
	// Used for override resolution when the same name appears in multiple settings.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// Text is the fragment content. May be multi-line.
	// +kubebuilder:validation:Required
	Text string `json:"text"`

	// Position controls where this fragment is injected relative to the agent's systemPrompt.
	//   prepend — inserted before systemPrompt (persona, context, constraints)
	//   append  — inserted after systemPrompt (output rules, closing instructions)
	// +kubebuilder:validation:Enum=prepend;append
	// +kubebuilder:default=append
	Position string `json:"position,omitempty"`
}

// PromptFragments holds reusable prompt components.
// Deprecated: use Fragments instead. Retained for backward compatibility.
type PromptFragments struct {
	// Persona is a persona/role description prepended to the system prompt.
	// Deprecated: define a PromptFragment with position=prepend instead.
	Persona string `json:"persona,omitempty"`
	// OutputRules defines output format constraints appended to the system prompt.
	// Deprecated: define a PromptFragment with position=append instead.
	OutputRules string `json:"outputRules,omitempty"`
}

// SwarmSettingsSecurity defines operator-level MCP security policy enforced at admission time.
// The admission webhook loads all SwarmSettings in a namespace and applies the strictest
// policy found — a single settings object with requireMCPAuth: true enforces it on all agents.
type SwarmSettingsSecurity struct {
	// MCPAllowlist is a list of URL prefixes. When set, the admission webhook rejects
	// SwarmAgent specs that reference MCP server URLs not matching any listed prefix.
	// Use this to prevent agents from calling arbitrary external MCP endpoints (T9).
	// Example: ["https://search.mcp.example.com/", "https://browser.mcp.example.com/"]
	// +optional
	MCPAllowlist []string `json:"mcpAllowlist,omitempty"`

	// RequireMCPAuth: when true, the webhook rejects SwarmAgent specs that declare MCP
	// servers without an auth configuration (spec.mcpServers[*].auth.type must not be "none").
	// Ensures no agent can call an MCP server without verified credentials.
	// +optional
	RequireMCPAuth bool `json:"requireMCPAuth,omitempty"`
}

// SwarmSettingsSpec defines the shared configuration values.
type SwarmSettingsSpec struct {
	// Temperature controls response randomness (0.0–1.0).
	// +kubebuilder:validation:Pattern=`^(0(\.[0-9]+)?|1(\.0+)?)$`
	Temperature string `json:"temperature,omitempty"`

	// OutputFormat specifies the expected output format (e.g. "structured-json").
	OutputFormat string `json:"outputFormat,omitempty"`

	// MemoryBackend defines where agent memory is stored.
	// +kubebuilder:default=in-context
	MemoryBackend MemoryBackend `json:"memoryBackend,omitempty"`

	// Fragments is an ordered list of named prompt fragments composed into the agent system prompt.
	// Fragments from all referenced SwarmSettings are applied in settingsRefs list order.
	// When the same fragment name appears in multiple settings, the last occurrence wins.
	// +optional
	Fragments []PromptFragment `json:"fragments,omitempty"`

	// PromptFragments is deprecated. Use Fragments instead.
	// When both are set, Fragments takes precedence and PromptFragments is ignored.
	// Retained for backward compatibility; will be removed in v1beta1.
	// +optional
	PromptFragments *PromptFragments `json:"promptFragments,omitempty"`

	// Security configures MCP server access policy enforced by the admission webhook.
	// The strictest policy across all referenced SwarmSettings wins.
	// +optional
	Security *SwarmSettingsSecurity `json:"security,omitempty"`
}

// SwarmSettingsStatus defines the observed state of SwarmSettings.
type SwarmSettingsStatus struct {
	// ObservedGeneration is the .metadata.generation this status reflects.
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// Conditions reflect the current state of the SwarmSettings.
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Memory",type=string,JSONPath=`.spec.memoryBackend`
// +kubebuilder:printcolumn:name="OutputFormat",type=string,JSONPath=`.spec.outputFormat`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
// +kubebuilder:resource:shortName={swcfg,swcfgs},scope=Namespaced,categories=kubeswarm

// SwarmSettings holds shared configuration consumed by SwarmAgents.
type SwarmSettings struct {
	metav1.TypeMeta `json:",inline"`

	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// +required
	Spec SwarmSettingsSpec `json:"spec"`

	// +optional
	Status SwarmSettingsStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// SwarmSettingsList contains a list of SwarmSettings.
type SwarmSettingsList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []SwarmSettings `json:"items"`
}

func init() {
	SchemeBuilder.Register(&SwarmSettings{}, &SwarmSettingsList{})
}
