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
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// -----------------------------------------------------------------------------
// Auth types
// -----------------------------------------------------------------------------

// BearerAuth configures bearer token authentication for an MCP server.
// The token value is never inlined into the pod spec — it is injected as an env var.
type BearerAuth struct {
	// SecretKeyRef selects the key of a Secret containing the bearer token.
	SecretKeyRef corev1.SecretKeySelector `json:"secretKeyRef"`
}

// MTLSAuth configures mTLS client certificate authentication for an MCP server.
// The Secret is mounted as a read-only volume inside the agent pod.
// It must contain tls.crt, tls.key, and optionally ca.crt.
type MTLSAuth struct {
	// SecretRef names the Secret containing the TLS credentials.
	SecretRef LocalObjectReference `json:"secretRef"`
}

// MCPServerAuth configures authentication for a single MCP server connection.
// Exactly one of bearer or mtls must be set.
// +kubebuilder:validation:XValidation:rule="!(has(self.bearer) && has(self.mtls))",message="bearer and mtls are mutually exclusive"
type MCPServerAuth struct {
	// Bearer configures Authorization: Bearer token authentication.
	// +optional
	Bearer *BearerAuth `json:"bearer,omitempty"`
	// MTLS configures mTLS client certificate authentication.
	// +optional
	MTLS *MTLSAuth `json:"mtls,omitempty"`
}

// -----------------------------------------------------------------------------
// Tool types
// -----------------------------------------------------------------------------

// ToolTrustLevel classifies the trust level of a tool or agent connection.
// Used by the admission controller and runtime to enforce input validation policy.
// +kubebuilder:validation:Enum=internal;external;sandbox
type ToolTrustLevel string

const (
	// ToolTrustInternal is for tools within the same organisation / cluster.
	ToolTrustInternal ToolTrustLevel = "internal"
	// ToolTrustExternal is for third-party or internet-facing tools.
	ToolTrustExternal ToolTrustLevel = "external"
	// ToolTrustSandbox is for untrusted or experimental tools; enforces strictest validation.
	ToolTrustSandbox ToolTrustLevel = "sandbox"
)

// MCPHeader defines a single HTTP header sent with every request to an MCP server.
// Exactly one of value or secretKeyRef must be set.
// +kubebuilder:validation:XValidation:rule="has(self.value) != has(self.secretKeyRef)",message="exactly one of value or secretKeyRef must be set"
type MCPHeader struct {
	// Name is the HTTP header name.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
	// Value is a literal header value. Use for non-sensitive headers.
	// +optional
	Value string `json:"value,omitempty"`
	// SecretKeyRef selects a key of a Secret for sensitive header values.
	// +optional
	SecretKeyRef *corev1.SecretKeySelector `json:"secretKeyRef,omitempty"`
}

// MCPToolSpec defines one MCP server connection available to the agent.
// Exactly one of url or capabilityRef must be set.
// +kubebuilder:validation:XValidation:rule="has(self.url) != has(self.capabilityRef)",message="exactly one of url or capabilityRef must be set"
type MCPToolSpec struct {
	// Name is a unique identifier for this MCP server within the agent.
	// Used in guardrails.tools allow/deny patterns: "<name>/<tool>".
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// URL is the direct SSE endpoint of the MCP server.
	// Mutually exclusive with capabilityRef.
	// +optional
	URL string `json:"url,omitempty"`

	// CapabilityRef names a capability ID in the namespace's SwarmRegistry.
	// The operator resolves the actual MCP server URL at reconcile time.
	// Use this in shareable agent definitions so the URL is supplied per-deployment.
	// Mutually exclusive with url.
	// +optional
	CapabilityRef string `json:"capabilityRef,omitempty"`

	// Trust classifies the trust level of this MCP server.
	// Defaults to the guardrails.tools.trust.default when unset.
	// +optional
	Trust ToolTrustLevel `json:"trust,omitempty"`

	// Instructions is operational context injected into the agent's system prompt
	// for this tool server. Use for deployment-specific constraints (branch, project key,
	// environment). General tool documentation belongs in the MCP tool's own description.
	// +optional
	Instructions string `json:"instructions,omitempty"`

	// Auth configures authentication for this MCP server.
	// When not set the connection is unauthenticated.
	// +optional
	Auth *MCPServerAuth `json:"auth,omitempty"`

	// Headers is a list of HTTP headers sent with every request to this MCP server.
	// +optional
	// +listType=map
	// +listMapKey=name
	Headers []MCPHeader `json:"headers,omitempty"`
}

// WebhookToolSpec defines an inline HTTP tool available to the agent without a full MCP server.
// Use for simple single-endpoint callbacks. For rich integrations prefer an MCP server.
type WebhookToolSpec struct {
	// Name is the tool identifier exposed to the LLM. Must be unique within the agent.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// URL is the HTTP endpoint the agent calls when the LLM invokes this tool.
	// +kubebuilder:validation:Required
	URL string `json:"url"`

	// Method is the HTTP method used when calling the endpoint.
	// +kubebuilder:default=POST
	// +kubebuilder:validation:Enum=GET;POST;PUT;PATCH
	Method string `json:"method,omitempty"`

	// Description explains the tool's purpose to the LLM and to human operators.
	// +optional
	Description string `json:"description,omitempty"`

	// Trust classifies the trust level of this webhook endpoint.
	// Defaults to the guardrails.tools.trust.default when unset.
	// +optional
	Trust ToolTrustLevel `json:"trust,omitempty"`

	// Schema is a JSON Schema object describing the tool's input parameters.
	// Stored as a raw JSON/YAML object; validated by the LLM runtime.
	// +optional
	// +kubebuilder:pruning:PreserveUnknownFields
	Schema *runtime.RawExtension `json:"schema,omitempty"`
}

// AgentTools groups all tool connections available to the agent.
type AgentTools struct {
	// MCP lists MCP server connections. Each entry exposes multiple tools
	// via the Model Context Protocol SSE transport.
	// +optional
	// +listType=map
	// +listMapKey=name
	MCP []MCPToolSpec `json:"mcp,omitempty"`

	// Webhooks lists inline single-endpoint HTTP tools.
	// +optional
	// +listType=map
	// +listMapKey=name
	Webhooks []WebhookToolSpec `json:"webhooks,omitempty"`
}

// -----------------------------------------------------------------------------
// A2A (agent-to-agent) types
// -----------------------------------------------------------------------------

// AgentConnection defines another agent callable as a tool via A2A.
// Exactly one of agentRef or capabilityRef must be set.
// +kubebuilder:validation:XValidation:rule="has(self.agentRef) != has(self.capabilityRef)",message="exactly one of agentRef or capabilityRef must be set"
type AgentConnection struct {
	// Name is the local identifier for this agent connection.
	// Used in guardrails.tools allow/deny patterns: "<name>/<capability>".
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// AgentRef names a SwarmAgent in the same namespace whose MCP-exposed
	// capabilities are made available as tools. The target must have at least
	// one capability with exposeMCP: true.
	// +optional
	AgentRef *LocalObjectReference `json:"agentRef,omitempty"`

	// CapabilityRef names a capability ID in the namespace's SwarmRegistry.
	// The operator resolves the MCP gateway URL at reconcile time.
	// +optional
	CapabilityRef *LocalObjectReference `json:"capabilityRef,omitempty"`

	// Trust classifies the trust level of this agent connection.
	// Defaults to guardrails.tools.trust.default when unset.
	// +optional
	Trust ToolTrustLevel `json:"trust,omitempty"`

	// Instructions is operational context injected into the agent's system prompt
	// for calls to this agent. Use to constrain scope or set expectations.
	// +optional
	Instructions string `json:"instructions,omitempty"`
}

// -----------------------------------------------------------------------------
// Identity types
// -----------------------------------------------------------------------------

// SystemPromptSource selects a system prompt from a ConfigMap or Secret key.
// Exactly one of configMapKeyRef and secretKeyRef must be set.
// +kubebuilder:validation:XValidation:rule="has(self.configMapKeyRef) != has(self.secretKeyRef)",message="exactly one of configMapKeyRef or secretKeyRef must be set"
type SystemPromptSource struct {
	// ConfigMapKeyRef selects a key of a ConfigMap in the same namespace.
	// +optional
	ConfigMapKeyRef *corev1.ConfigMapKeySelector `json:"configMapKeyRef,omitempty"`
	// SecretKeyRef selects a key of a Secret in the same namespace.
	// Use when the prompt contains sensitive instructions.
	// +optional
	SecretKeyRef *corev1.SecretKeySelector `json:"secretKeyRef,omitempty"`
}

// AgentPrompt configures the agent's system prompt.
// Exactly one of inline or from must be set.
// +kubebuilder:validation:XValidation:rule="has(self.inline) != has(self.from)",message="exactly one of inline or from must be set"
type AgentPrompt struct {
	// Inline is the system prompt text written directly in the manifest.
	// For long or frequently-iterated prompts prefer from.
	// +optional
	Inline string `json:"inline,omitempty"`

	// From references a ConfigMap or Secret key whose content is used as the system prompt.
	// Updating the referenced object triggers an automatic rolling restart of agent pods.
	// +optional
	From *SystemPromptSource `json:"from,omitempty"`
}

// AgentCapability advertises one capability this agent offers to SwarmRegistry and the MCP gateway.
type AgentCapability struct {
	// Name uniquely identifies this capability. Used for registry lookups and MCP tool naming.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// Description explains the capability to human operators and LLM consumers.
	// +optional
	Description string `json:"description,omitempty"`

	// Tags enable coarse-grained filtering in registry lookups.
	// A lookup matches agents that declare ALL listed tags.
	// +optional
	Tags []string `json:"tags,omitempty"`

	// ExposeMCP registers this capability as a named tool at the MCP gateway endpoint
	// for this agent. Requires the MCP gateway to be enabled in the operator.
	// +kubebuilder:default=false
	// +optional
	ExposeMCP bool `json:"exposeMCP,omitempty"`

	// InputSchema is a JSON Schema object describing the capability's input parameters.
	// Stored as a raw YAML/JSON object; enables CRD validation and tooling introspection.
	// +optional
	// +kubebuilder:pruning:PreserveUnknownFields
	InputSchema *runtime.RawExtension `json:"inputSchema,omitempty"`

	// OutputSchema is a JSON Schema object describing the capability's output shape.
	// +optional
	// +kubebuilder:pruning:PreserveUnknownFields
	OutputSchema *runtime.RawExtension `json:"outputSchema,omitempty"`
}

// -----------------------------------------------------------------------------
// Guardrail types
// -----------------------------------------------------------------------------

// ToolTrustPolicy sets the default trust level and validation behaviour for tools.
type ToolTrustPolicy struct {
	// Default is the trust level applied to tools and agents that do not declare
	// an explicit trust field. Defaults to external.
	// +kubebuilder:default=external
	// +optional
	Default ToolTrustLevel `json:"default,omitempty"`

	// EnforceInputValidation rejects tool calls whose arguments do not match the
	// tool's declared schema when the tool's effective trust level is sandbox.
	// +optional
	EnforceInputValidation bool `json:"enforceInputValidation,omitempty"`
}

// ToolPermissions defines allow/deny lists and trust policy for tool calls.
type ToolPermissions struct {
	// Allow is an allowlist of tool calls in "<server-name>/<tool-name>" format.
	// Wildcards are supported: "filesystem/*" allows all tools from the filesystem server.
	// When set, only listed tool calls are permitted. Deny takes precedence over allow.
	// +optional
	Allow []string `json:"allow,omitempty"`

	// Deny is a denylist of tool calls in "<server-name>/<tool-name>" format.
	// Wildcards are supported: "shell/*" denies all shell tools.
	// Deny takes precedence over allow when both match.
	// +optional
	Deny []string `json:"deny,omitempty"`

	// Trust configures the default trust level and input validation policy.
	// +optional
	Trust *ToolTrustPolicy `json:"trust,omitempty"`
}

// GuardrailLimits constrains per-agent resource and cost usage.
type GuardrailLimits struct {
	// TokensPerCall is the maximum number of tokens per LLM API call.
	// +kubebuilder:default=8000
	// +optional
	TokensPerCall int `json:"tokensPerCall,omitempty"`

	// ConcurrentTasks is the maximum number of tasks processed in parallel per replica.
	// +kubebuilder:default=5
	// +optional
	ConcurrentTasks int `json:"concurrentTasks,omitempty"`

	// TimeoutSeconds is the per-task deadline in seconds.
	// +kubebuilder:default=120
	// +optional
	TimeoutSeconds int `json:"timeoutSeconds,omitempty"`

	// DailyTokens is the rolling 24-hour token budget (input + output combined).
	// When reached the operator scales replicas to 0 and sets a BudgetExceeded condition.
	// Resumes automatically when the 24-hour window rotates. Zero means no daily limit.
	// +kubebuilder:validation:Minimum=1
	// +optional
	DailyTokens int64 `json:"dailyTokens,omitempty"`

	// Retries is the number of times a failed task is requeued before dead-lettering.
	// Set to 0 to disable retries entirely.
	// +kubebuilder:default=3
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=100
	// +optional
	Retries int `json:"retries,omitempty"`
}

// AgentGuardrails groups tool permissions, budget reference, and execution limits.
type AgentGuardrails struct {
	// Tools configures allow/deny lists and trust policy for tool calls.
	// +optional
	Tools *ToolPermissions `json:"tools,omitempty"`

	// BudgetRef references a SwarmBudget that governs token spend for this agent.
	// When the budget is exhausted new tasks are rejected.
	// +optional
	BudgetRef *LocalObjectReference `json:"budgetRef,omitempty"`

	// Limits constrains per-agent resource and cost usage.
	// +optional
	Limits *GuardrailLimits `json:"limits,omitempty"`
}

// -----------------------------------------------------------------------------
// Runtime types
// -----------------------------------------------------------------------------

// SwarmAgentAutoscaling configures KEDA-based autoscaling for the agent's deployment.
// Requires KEDA v2 installed in the cluster.
// When set on an agent, runtime.replicas is ignored.
type SwarmAgentAutoscaling struct {
	// MinReplicas is the minimum replica count (idle floor).
	// +kubebuilder:default=1
	// +kubebuilder:validation:Minimum=0
	// +optional
	MinReplicas *int32 `json:"minReplicas,omitempty"`

	// MaxReplicas is the maximum replica count.
	// +kubebuilder:default=10
	// +kubebuilder:validation:Minimum=1
	// +optional
	MaxReplicas *int32 `json:"maxReplicas,omitempty"`

	// TargetPendingTasks is the number of pending queue entries per replica used
	// as the KEDA scale trigger. Scale-up fires when pending tasks exceed this threshold.
	// +kubebuilder:default=5
	// +kubebuilder:validation:Minimum=1
	// +optional
	TargetPendingTasks *int32 `json:"targetPendingTasks,omitempty"`
}

// LoopCompressionConfig controls when and how the runner compresses accumulated context.
type LoopCompressionConfig struct {
	// Threshold is the fraction of the resolved context window at which compression
	// is triggered. Must be between 0.5 and 0.95.
	// +kubebuilder:default=0.75
	// +kubebuilder:validation:Minimum=0.5
	// +kubebuilder:validation:Maximum=0.95
	// +optional
	Threshold float64 `json:"threshold,omitempty"`

	// PreserveRecentTurns is the number of most recent turns kept verbatim during
	// compression. The system prompt is always preserved.
	// +kubebuilder:default=4
	// +optional
	PreserveRecentTurns int `json:"preserveRecentTurns,omitempty"`

	// Model is the model used for the compression call.
	// A cheap fast model is recommended (e.g. claude-haiku-4-5-20251001).
	// Defaults to claude-haiku-4-5-20251001 when unset.
	// +optional
	Model string `json:"model,omitempty"`

	// TimeoutSeconds is the maximum time allowed for the compression model call.
	// If exceeded compression is skipped and a CompressionTimeout warning event is recorded.
	// +kubebuilder:default=30
	// +optional
	TimeoutSeconds int `json:"timeoutSeconds,omitempty"`

	// ContextWindow explicitly sets the model's context window size in tokens.
	// When set, overrides provider metadata and the built-in model map.
	// Useful for custom or private model endpoints.
	// +optional
	ContextWindow int `json:"contextWindow,omitempty"`

	// Instructions overrides the built-in system prompt for the compression call.
	// +optional
	Instructions string `json:"instructions,omitempty"`
}

// AgentLoopMemory configures vector memory read/write within the tool-use loop.
// Requires a SwarmMemory with a vector backend referenced via ref.
type AgentLoopMemory struct {
	// Ref references the SwarmMemory object providing the vector backend.
	// +optional
	Ref *LocalObjectReference `json:"ref,omitempty"`

	// Store enables writing a summary of each tool result to the vector store after execution.
	// +optional
	Store bool `json:"store,omitempty"`

	// Retrieve enables fetching similar prior findings from the vector store before each tool call.
	// Findings are injected as a <swarm:prior-findings> block with the tool result.
	// +optional
	Retrieve bool `json:"retrieve,omitempty"`

	// TopK is the maximum number of prior findings injected per tool call.
	// +kubebuilder:default=3
	// +optional
	TopK int `json:"topK,omitempty"`

	// MinSimilarity is the minimum cosine similarity score for a retrieved finding to be injected.
	// Findings below this threshold are silently dropped.
	// +kubebuilder:default=0.70
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=1
	// +optional
	MinSimilarity float64 `json:"minSimilarity,omitempty"`

	// SummaryTokens is the advisory token limit for per-result summaries stored in the vector store.
	// When > 0 a cheap model call produces the summary before storing.
	// When 0 the raw result is truncated to MaxTokens instead.
	// +kubebuilder:default=256
	// +optional
	SummaryTokens int `json:"summaryTokens,omitempty"`

	// MaxTokens is the hard truncation limit applied when SummaryTokens is 0.
	// +kubebuilder:default=1024
	// +optional
	MaxTokens int `json:"maxTokens,omitempty"`
}

// AgentLoopPolicy configures the agent runner's agentic loop behaviour.
type AgentLoopPolicy struct {
	// Dedup skips tool calls whose fingerprint (tool name + args hash) was already
	// executed in the current task. The dedup set is task-local and discarded on completion.
	// +optional
	Dedup bool `json:"dedup,omitempty"`

	// Compression configures in-loop context compression. When set, the runner summarises
	// older conversation turns when accumulated tokens exceed the threshold, allowing the
	// agent to run beyond the model's context window.
	// +optional
	Compression *LoopCompressionConfig `json:"compression,omitempty"`

	// Memory configures vector memory read/write during the tool-use loop.
	// Requires a SwarmMemory with a vector backend referenced via memory.ref.
	// +optional
	Memory *AgentLoopMemory `json:"memory,omitempty"`
}

// AgentRuntime groups all execution concerns for the agent deployment.
type AgentRuntime struct {
	// Replicas is the number of agent instances to run.
	// Ignored when autoscaling is set; autoscaling.minReplicas acts as the floor.
	// +kubebuilder:default=1
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=50
	// +optional
	Replicas *int32 `json:"replicas,omitempty"`

	// Autoscaling configures KEDA-based autoscaling. When set, replicas is ignored.
	// Requires KEDA v2 installed in the cluster.
	// +optional
	Autoscaling *SwarmAgentAutoscaling `json:"autoscaling,omitempty"`

	// Resources sets CPU and memory requests/limits for agent pods.
	// When not set the operator injects safe defaults:
	//   requests: cpu=100m, memory=128Mi
	//   limits:   cpu=500m, memory=512Mi, ephemeral-storage=256Mi
	// +optional
	Resources *corev1.ResourceRequirements `json:"resources,omitempty"`

	// Loop configures deep-research runtime features: semantic dedup, in-loop context
	// compression, and vector memory read/write. All features are disabled by default.
	// +optional
	Loop *AgentLoopPolicy `json:"loop,omitempty"`
}

// -----------------------------------------------------------------------------
// Infrastructure types
// -----------------------------------------------------------------------------

// NetworkPolicyMode controls how the operator generates a NetworkPolicy for agent pods.
// +kubebuilder:validation:Enum=default;strict;disabled
type NetworkPolicyMode string

const (
	// NetworkPolicyModeDefault allows DNS, Redis, and open HTTPS egress.
	NetworkPolicyModeDefault NetworkPolicyMode = "default"
	// NetworkPolicyModeStrict allows DNS and Redis egress only; HTTPS egress is restricted
	// to the resolved IPs of declared MCP servers.
	NetworkPolicyModeStrict NetworkPolicyMode = "strict"
	// NetworkPolicyModeDisabled skips NetworkPolicy generation entirely.
	// Use when the cluster CNI (Cilium, Calico) manages network policy externally.
	NetworkPolicyModeDisabled NetworkPolicyMode = "disabled"
)

// PluginTLSConfig references a Secret containing TLS credentials for a gRPC plugin.
// The Secret must contain tls.crt, tls.key, and ca.crt.
type PluginTLSConfig struct {
	// SecretRef names the Secret containing the TLS credentials.
	SecretRef LocalObjectReference `json:"secretRef"`
}

// PluginEndpoint defines a gRPC plugin connection address and optional TLS config.
type PluginEndpoint struct {
	// Address is the host:port of the gRPC plugin server.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Address string `json:"address"`

	// TLS configures mTLS for the gRPC connection.
	// When not set the connection is plaintext.
	// +optional
	TLS *PluginTLSConfig `json:"tls,omitempty"`
}

// AgentPlugins configures external gRPC plugin overrides for the LLM provider and task queue.
// These are escape hatches for environments where the built-in providers are insufficient.
// See RFC-0025.
type AgentPlugins struct {
	// LLM is the host:port of an external gRPC LLM provider plugin.
	// When set the agent uses the gRPC adapter instead of the built-in provider.
	// +optional
	LLM *PluginEndpoint `json:"llm,omitempty"`

	// Queue is the host:port of an external gRPC task queue plugin.
	// When set TASK_QUEUE_URL is ignored and the agent uses the gRPC queue adapter.
	// +optional
	Queue *PluginEndpoint `json:"queue,omitempty"`
}

// AgentInfrastructure groups cluster integration concerns for the agent.
type AgentInfrastructure struct {
	// RegistryRef names the SwarmRegistry this agent registers into.
	// Defaults to "default". Omit to opt out of all registry indexing.
	// +optional
	RegistryRef *LocalObjectReference `json:"registryRef,omitempty"`

	// NetworkPolicy controls the NetworkPolicy generated for agent pods.
	// default: DNS + Redis + open HTTPS egress.
	// strict:  DNS + Redis; HTTPS egress restricted to declared MCP server IPs.
	// disabled: no NetworkPolicy generated (use when CNI manages policy externally).
	// +kubebuilder:default=default
	// +optional
	NetworkPolicy NetworkPolicyMode `json:"networkPolicy,omitempty"`

	// APIKeyRef injects an LLM provider API key from a native Kubernetes Secret.
	// The key is set as the environment variable named by the Secret key
	// (e.g. key "ANTHROPIC_API_KEY" in Secret "my-keys" sets ANTHROPIC_API_KEY).
	// For multiple keys or complex setups, use envFrom instead.
	// +optional
	APIKeyRef *corev1.SecretKeySelector `json:"apiKeyRef,omitempty"`

	// EnvFrom injects environment variables from Secrets or ConfigMaps into agent pods.
	// Entries listed here take precedence over the global kubeswarm-api-keys Secret.
	// +optional
	EnvFrom []corev1.EnvFromSource `json:"envFrom,omitempty"`

	// Plugins configures external gRPC provider or queue overrides (RFC-0025).
	// +optional
	Plugins *AgentPlugins `json:"plugins,omitempty"`
}

// -----------------------------------------------------------------------------
// Observability types
// -----------------------------------------------------------------------------

// HealthCheckType is the strategy used to evaluate agent health.
// +kubebuilder:validation:Enum=semantic;ping
type HealthCheckType string

const (
	// HealthCheckSemantic sends a prompt to the agent and evaluates the response via LLM.
	HealthCheckSemantic HealthCheckType = "semantic"
	// HealthCheckPing sends an HTTP request to the agent's health endpoint.
	HealthCheckPing HealthCheckType = "ping"
)

// AgentHealthCheck defines how agent health is evaluated.
type AgentHealthCheck struct {
	// Type is the probe strategy.
	// +kubebuilder:default=semantic
	Type HealthCheckType `json:"type"`

	// IntervalSeconds is how often the probe runs.
	// +kubebuilder:default=30
	// +optional
	IntervalSeconds int `json:"intervalSeconds,omitempty"`

	// Prompt is the message sent when type is semantic.
	// +optional
	Prompt string `json:"prompt,omitempty"`

	// NotifyRef references a SwarmNotify policy used for AgentDegraded events.
	// +optional
	NotifyRef *LocalObjectReference `json:"notifyRef,omitempty"`
}

// LogRedactionPolicy controls what is scrubbed from agent runtime logs.
type LogRedactionPolicy struct {
	// Secrets scrubs values sourced from secretKeyRef from tool call args and results.
	// +kubebuilder:default=true
	// +optional
	Secrets bool `json:"secrets,omitempty"`

	// PII scrubs common PII patterns (email addresses, IP addresses, phone numbers)
	// from tool call args and results.
	// +optional
	PII bool `json:"pii,omitempty"`
}

// LogLevel is the minimum log level emitted by the agent runtime.
// +kubebuilder:validation:Enum=debug;info;warn;error
type LogLevel string

const (
	LogLevelDebug LogLevel = "debug"
	LogLevelInfo  LogLevel = "info"
	LogLevelWarn  LogLevel = "warn"
	LogLevelError LogLevel = "error"
)

// AgentLogging controls structured log emission from the agent runtime.
// All logs are emitted as JSON via slog.
// +kubebuilder:validation:XValidation:rule="!has(self.llmTurns) || !self.llmTurns || (has(self.redaction) && self.redaction.secrets)",message="redaction.secrets must be true when llmTurns is enabled"
type AgentLogging struct {
	// Level is the minimum log level emitted.
	// +kubebuilder:default=info
	// +optional
	Level LogLevel `json:"level,omitempty"`

	// ToolCalls enables structured logging of tool invocations: tool name, args, and result.
	// Emits log lines with msg="tool_call". Disabled by default to avoid noisy logs.
	// +optional
	ToolCalls bool `json:"toolCalls,omitempty"`

	// LLMTurns enables logging of the full LLM message history per task.
	// Verbose and potentially sensitive — enable only for debugging.
	// +optional
	LLMTurns bool `json:"llmTurns,omitempty"`

	// Redaction controls scrubbing of sensitive values from log output.
	// +optional
	Redaction *LogRedactionPolicy `json:"redaction,omitempty"`
}

// AgentMetrics controls Prometheus-compatible metrics exposure for the agent runtime.
type AgentMetrics struct {
	// Enabled exposes a /metrics endpoint on the agent pod for Prometheus scraping.
	// +kubebuilder:default=false
	// +optional
	Enabled bool `json:"enabled,omitempty"`
}

// AgentObservability groups health check, logging, and metrics configuration.
type AgentObservability struct {
	// HealthCheck defines how agent health is evaluated and how degraded agents are alerted.
	// +optional
	HealthCheck *AgentHealthCheck `json:"healthCheck,omitempty"`

	// Logging controls structured log emission from the agent runtime.
	// +optional
	Logging *AgentLogging `json:"logging,omitempty"`

	// Metrics controls Prometheus metrics exposure.
	// +optional
	Metrics *AgentMetrics `json:"metrics,omitempty"`
}

// -----------------------------------------------------------------------------
// LocalObjectReference
// -----------------------------------------------------------------------------

// LocalObjectReference is an alias for corev1.LocalObjectReference.
// Kept as a type alias for documentation clarity in kubeswarm CRDs.
type LocalObjectReference = corev1.LocalObjectReference

// -----------------------------------------------------------------------------
// SwarmAgentSpec / Status
// -----------------------------------------------------------------------------

// SwarmAgentSpec defines the desired state of a SwarmAgent.
type SwarmAgentSpec struct {

	// --- Identity: what the agent IS ---

	// Model is the LLM model ID (e.g. "claude-sonnet-4-6").
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Model string `json:"model"`

	// Prompt configures the agent's system prompt.
	// +kubebuilder:validation:Required
	Prompt *AgentPrompt `json:"prompt"`

	// Settings references SwarmSettings objects whose fragments are composed into
	// this agent's system prompt, in list order. Last occurrence wins for duplicate keys.
	// +optional
	// +listType=map
	// +listMapKey=name
	Settings []LocalObjectReference `json:"settings,omitempty"`

	// Capabilities advertises what this agent can do to SwarmRegistry and the MCP gateway.
	// Agents without capabilities are invisible to registry lookups.
	// +optional
	// +listType=map
	// +listMapKey=name
	Capabilities []AgentCapability `json:"capabilities,omitempty"`

	// --- Tools: what the agent can USE ---

	// Tools groups MCP server connections and inline webhook tools.
	// +optional
	Tools *AgentTools `json:"tools,omitempty"`

	// --- Agents: other agents callable as tools via A2A ---

	// Agents lists other SwarmAgent or registry capabilities callable as tools via A2A.
	// +optional
	// +listType=map
	// +listMapKey=name
	Agents []AgentConnection `json:"agents,omitempty"`

	// --- Guardrails: safety + cost controls ---

	// Guardrails groups tool permissions, budget enforcement, and execution limits.
	// +optional
	Guardrails *AgentGuardrails `json:"guardrails,omitempty"`

	// --- Runtime: how the agent runs ---

	// Runtime groups replica count, autoscaling, resources, and loop policy.
	// +kubebuilder:default={}
	Runtime AgentRuntime `json:"runtime,omitempty"`

	// --- Infrastructure ---

	// Infrastructure groups cluster integration concerns: registry, network policy,
	// API key injection, environment variables, and gRPC plugin overrides.
	// +optional
	Infrastructure *AgentInfrastructure `json:"infrastructure,omitempty"`

	// --- Observability ---

	// Observability groups health check, logging, and metrics configuration.
	// +optional
	Observability *AgentObservability `json:"observability,omitempty"`
}

// SwarmAgentMCPStatus reports the last observed health of one MCP server.
type SwarmAgentMCPStatus struct {
	// Name matches MCPToolSpec.name.
	Name string `json:"name"`
	// URL is the MCP server endpoint that was probed.
	URL string `json:"url"`
	// Healthy is true when the last probe received a non-5xx HTTP response.
	// Nil means the server has not been probed yet.
	// +optional
	Healthy *bool `json:"healthy,omitempty"`
	// Message holds error detail when Healthy is false.
	// +optional
	Message string `json:"message,omitempty"`
	// LastCheck is when the probe was last run.
	// +optional
	LastCheck *metav1.Time `json:"lastCheck,omitempty"`
}

// SwarmAgentStatus defines the observed state of SwarmAgent.
type SwarmAgentStatus struct {
	// ReadyReplicas is the number of agent pods ready to accept tasks.
	ReadyReplicas int32 `json:"readyReplicas,omitempty"`
	// Replicas is the total number of agent pods (ready or not).
	Replicas int32 `json:"replicas,omitempty"`
	// DesiredReplicas is the autoscaling-computed target replica count.
	// Nil for standalone agents not managed by a team autoscaler.
	// +optional
	DesiredReplicas *int32 `json:"desiredReplicas,omitempty"`
	// PendingTasks is the current number of tasks waiting in the queue for this agent.
	// +optional
	PendingTasks *int32 `json:"pendingTasks,omitempty"`
	// ObservedGeneration is the .metadata.generation this status reflects.
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// DailyTokenUsage is the sum of tokens consumed in the rolling 24-hour window.
	// Populated only when guardrails.limits.dailyTokens is set.
	// +optional
	DailyTokenUsage *TokenUsage `json:"dailyTokenUsage,omitempty"`
	// ToolConnections reports the last observed connectivity state of each configured MCP server.
	// +optional
	// +listType=map
	// +listMapKey=name
	ToolConnections []SwarmAgentMCPStatus `json:"toolConnections,omitempty"`
	// SystemPromptHash is the SHA-256 hex digest of the resolved system prompt last applied.
	// +optional
	SystemPromptHash string `json:"systemPromptHash,omitempty"`
	// ExposedMCPCapabilities lists the capability names currently registered at the MCP gateway.
	// +optional
	ExposedMCPCapabilities []string `json:"exposedMCPCapabilities,omitempty"`
	// Conditions reflect the current state of the SwarmAgent.
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:subresource:scale:specpath=.spec.runtime.replicas,statuspath=.status.replicas
// +kubebuilder:printcolumn:name="Model",type=string,JSONPath=`.spec.model`
// +kubebuilder:printcolumn:name="Replicas",type=integer,JSONPath=`.spec.runtime.replicas`
// +kubebuilder:printcolumn:name="Ready",type=integer,JSONPath=`.status.readyReplicas`
// +kubebuilder:printcolumn:name="Pending",type=integer,JSONPath=`.status.pendingTasks`,priority=1
// +kubebuilder:printcolumn:name="Tokens(24h)",type=integer,JSONPath=`.status.dailyTokenUsage.totalTokens`,priority=1
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
// +kubebuilder:resource:scope=Namespaced,shortName={swagent,swagents},categories=kubeswarm

// SwarmAgent manages a pool of LLM agent instances.
type SwarmAgent struct {
	metav1.TypeMeta `json:",inline"`

	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// +required
	Spec SwarmAgentSpec `json:"spec"`

	// +optional
	Status SwarmAgentStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// SwarmAgentList contains a list of SwarmAgent.
type SwarmAgentList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []SwarmAgent `json:"items"`
}

func init() {
	SchemeBuilder.Register(&SwarmAgent{}, &SwarmAgentList{})
}

// Ensure resource.Quantity is imported (used transitively by corev1.ResourceRequirements).
var _ = resource.MustParse
