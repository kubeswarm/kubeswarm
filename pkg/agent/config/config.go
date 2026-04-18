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

package config

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	agenterrors "github.com/kubeswarm/kubeswarm/pkg/agent/errors"
	"github.com/kubeswarm/kubeswarm/pkg/audit"
)

// Reasoning mode constants used by providers to interpret Config.ReasoningMode.
const (
	ReasoningModeDisabled = "Disabled"
	ReasoningModeAuto     = "Auto"
	ReasoningModeExplicit = "Explicit"
)

// LoopCompressionConfig mirrors the CRD LoopCompressionConfig for in-loop context compression (RFC-0026).
// JSON tags match the CRD json tags so the operator can marshal AgentLoopPolicy directly.
type LoopCompressionConfig struct {
	ThresholdPercent    int32  `json:"thresholdPercent,omitempty"`
	PreserveRecentTurns int    `json:"preserveRecentTurns,omitempty"`
	ContextWindow       int    `json:"contextWindow,omitempty"`
	Model               string `json:"model,omitempty"`
	TimeoutSeconds      int    `json:"timeoutSeconds,omitempty"`
	Instructions        string `json:"instructions,omitempty"`
}

// ThresholdFloat returns the compression threshold as a float64 (0.0-1.0). Returns 0.75 when unset.
func (c *LoopCompressionConfig) ThresholdFloat() float64 {
	if c.ThresholdPercent <= 0 {
		return 0.75
	}
	return float64(c.ThresholdPercent) / 100.0
}

// LoopMemoryConfig mirrors the CRD AgentLoopMemory for vector memory read/write (RFC-0026).
// JSON tags match the CRD json tags so the operator can marshal AgentLoopPolicy directly.
type LoopMemoryConfig struct {
	Store                bool  `json:"store,omitempty"`
	Retrieve             bool  `json:"retrieve,omitempty"`
	TopK                 int   `json:"topK,omitempty"`
	MinSimilarityPercent int32 `json:"minSimilarityPercent,omitempty"`
	SummaryTokens        int   `json:"summaryTokens,omitempty"`
	MaxTokens            int   `json:"maxTokens,omitempty"`
	// EmbeddingModel is the model ID used to generate embeddings.
	// Injected by the operator from SwarmMemory.spec.embedding.model.
	// When empty, the runner calls provider.Embed() on the task LLM provider.
	EmbeddingModel string `json:"embeddingModel,omitempty"`
	// EmbeddingProvider selects which provider generates the embedding.
	// Injected from SwarmMemory.spec.embedding.provider ("auto", "openai", etc.).
	EmbeddingProvider string `json:"embeddingProvider,omitempty"`
}

// LoopPolicyConfig mirrors the CRD AgentLoopPolicy (RFC-0026).
// Injected by the operator as AGENT_LOOP_POLICY (JSON).
// JSON tags match the CRD json tags so the operator can marshal AgentLoopPolicy directly.
type LoopPolicyConfig struct {
	Dedup       bool                   `json:"dedup,omitempty"`
	Compression *LoopCompressionConfig `json:"compression,omitempty"`
	Memory      *LoopMemoryConfig      `json:"memory,omitempty"`
}

// AuditLogConfig mirrors the CRD AuditLogConfig for structured audit logging (RFC-0030).
// JSON tags match the audit.AuditConfig json tags so the operator can marshal directly.
type AuditLogConfig struct {
	// Mode controls the verbosity of audit logging (off, actions, verbose).
	Mode string `json:"mode,omitempty"`
	// Sink selects the output backend (stdout, redis, webhook).
	Sink string `json:"sink,omitempty"`
	// MaxDetailBytes is the maximum size for detail.input and detail.output
	// fields before truncation. 0 means unlimited.
	MaxDetailBytes int `json:"maxDetailBytes,omitempty"`
	// Redact is a list of glob patterns for field path redaction.
	Redact []string `json:"redact,omitempty"`
	// ExcludeActions is a list of action types to skip.
	ExcludeActions []string `json:"excludeActions,omitempty"`
	// RedisURL is the Redis connection URL when sink=redis.
	RedisURL string `json:"redisURL,omitempty"`
	// MaxStreamLen overrides the MAXLEN ~ cap on the audit Redis stream (default 100000).
	MaxStreamLen int64 `json:"maxStreamLen,omitempty"`
	// WebhookURL is the HTTP endpoint when sink=webhook. The agent POSTs []AuditEvent batches.
	WebhookURL string `json:"webhookURL,omitempty"`
}

// MCPServerConfig holds the connection details for one MCP tool server.
// Serialized into AGENT_MCP_SERVERS by the operator; read by the agent runtime at startup.
// Secret values are never included - only env var names and file paths are carried here.
type MCPServerConfig struct {
	Name string `json:"name"`
	URL  string `json:"url"`

	// AuthType is "none" (default), "bearer", or "mtls".
	AuthType string `json:"authType,omitempty"`

	// TokenEnvVar is the name of the environment variable containing the bearer token.
	// The operator injects a SecretKeyRef env var with this name; the agent reads it here.
	// Only set when AuthType is "bearer".
	TokenEnvVar string `json:"tokenEnvVar,omitempty"`

	// CertFile and KeyFile are absolute paths inside the pod to the mTLS client certificate
	// and private key. The operator mounts the referenced k8s Secret at these paths.
	// Only set when AuthType is "mtls".
	CertFile string `json:"certFile,omitempty"`
	KeyFile  string `json:"keyFile,omitempty"`

	// Discovery configures dynamic tool list refresh.
	Discovery *MCPDiscoveryConfigRuntime `json:"discovery,omitempty"`
}

// MCPDiscoveryConfigRuntime holds runtime discovery settings for one MCP server.
type MCPDiscoveryConfigRuntime struct {
	Dynamic             bool `json:"dynamic,omitempty"`
	PollIntervalSeconds int  `json:"pollIntervalSeconds,omitempty"`
}

// WebhookToolConfig defines an inline HTTP tool injected by the operator via AGENT_WEBHOOK_TOOLS.
type WebhookToolConfig struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	URL         string `json:"url"`
	// Method is the HTTP method (GET, POST, PUT, PATCH). Defaults to POST.
	Method string `json:"method"`
	// InputSchema is a JSON Schema string describing the tool's input parameters.
	InputSchema string `json:"inputSchema"`
}

// GatewayCapabilityConfig is one capability entry injected by the operator (RFC-0052).
type GatewayCapabilityConfig struct {
	Name          string   `json:"name"`
	Description   string   `json:"description"`
	Agent         string   `json:"agent"`
	Namespace     string   `json:"namespace"`
	Tags          []string `json:"tags"`
	ReadyReplicas int32    `json:"readyReplicas"`
}

// GatewayRuntimeConfig holds gateway dispatch settings (RFC-0052).
type GatewayRuntimeConfig struct {
	DispatchMode           string   `json:"dispatchMode"`
	DispatchTimeoutSeconds int32    `json:"dispatchTimeoutSeconds"`
	MaxDispatchDepth       int32    `json:"maxDispatchDepth"`
	MaxResultsPerSearch    int32    `json:"maxResultsPerSearch"`
	MaxDispatchCalls       int32    `json:"maxDispatchCalls"`
	MaxSearchCalls         int32    `json:"maxSearchCalls"`
	FallbackMode           string   `json:"fallbackMode"`
	FallbackAgent          string   `json:"fallbackAgent,omitempty"`
	AllowedTargets         []string `json:"allowedTargets,omitempty"`
}

// GatewayToolConfig is a tool definition injected by the operator (RFC-0052).
type GatewayToolConfig struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// AdvisorConfig holds the runtime configuration for one advisor connection (RFC-0048).
// Injected by the operator as AGENT_ADVISORS (JSON array).
type AdvisorConfig struct {
	Name                    string `json:"name"`
	ToolName                string `json:"toolName"`
	AgentRef                string `json:"agentRef"`
	RecentMessages          int32  `json:"recentMessages"`
	MaxCallsPerTask         int32  `json:"maxCallsPerTask"`
	TimeoutSeconds          int32  `json:"timeoutSeconds"`
	MaxAdvisorTokensPerTask int32  `json:"maxAdvisorTokensPerTask"`
	MaxContextBytes         int32  `json:"maxContextBytes"`
	ExcludeSystemPrompt     bool   `json:"excludeSystemPrompt"`
	Instructions            string `json:"instructions,omitempty"`
}

// Config holds all runtime configuration for an agent pod.
// All fields are populated from environment variables injected by the operator.
// Provider-specific credentials (e.g. ANTHROPIC_API_KEY) are read directly
// by each LLMProvider implementation, not stored here.
type Config struct {
	// Provider selects the LLM backend (e.g. "anthropic"). Set via AGENT_PROVIDER.
	// Defaults to "anthropic".
	Provider         string
	Model            string
	SystemPrompt     string
	MCPServers       []MCPServerConfig
	MaxTokensPerCall int
	TimeoutSeconds   int
	TaskQueueURL     string
	// StreamChannelURL is the connection URL for the StreamChannel backend.
	// Defaults to TaskQueueURL when not set, so the same backend serves both.
	StreamChannelURL string
	ValidatorPrompt  string
	// MaxRetries is the number of times a failed task is requeued before dead-lettering.
	// Set via AGENT_MAX_RETRIES. Defaults to 3.
	MaxRetries int
	// MaxConcurrentTasks is the maximum number of tasks processed in parallel.
	// Set via AGENT_MAX_CONCURRENT_TASKS. Defaults to 5.
	MaxConcurrentTasks int
	// WebhookTools is the list of inline HTTP tools injected by the operator.
	// Set via AGENT_WEBHOOK_TOOLS (JSON array).
	WebhookTools []WebhookToolConfig
	// TeamRoutes maps role names to queue URLs for the delegate() built-in tool.
	// Set via AGENT_TEAM_ROUTES (JSON object: {"role": "queue-url"}).
	// Only present when the agent is a member of an SwarmTeam.
	TeamRoutes map[string]string
	// Namespace is the Kubernetes namespace this agent pod runs in.
	// Set via AGENT_NAMESPACE (downward API). Used for OTel metric labels.
	Namespace string
	// AgentName is the name of the SwarmAgent CR this pod belongs to.
	// Set via AGENT_NAME. Used for OTel metric labels.
	AgentName string
	// TeamRole is the role this agent plays within an SwarmTeam.
	// Set via AGENT_TEAM_ROLE. Empty when not in a team.
	TeamRole string
	// TeamName is the name of the SwarmTeam this agent belongs to.
	// Set via AGENT_TEAM_NAME. Empty when not in a team.
	TeamName string
	// DailyTokenLimit is the rolling 24-hour token budget for this agent pod.
	// 0 means no limit. Set via AGENT_DAILY_TOKEN_LIMIT (injected by operator
	// from spec.guardrails.limits.dailyTokens).
	DailyTokenLimit int64
	// ExternalProviderAddr is the host:port of an external gRPC LLM provider plugin
	// (RFC-0025). When set, the agent uses GRPCProvider instead of a built-in provider.
	// Set via SWARM_PLUGIN_LLM_ADDR, injected by the operator from spec.plugins.llm.address.
	ExternalProviderAddr string
	// ExternalQueueAddr is the host:port of an external gRPC queue plugin (RFC-0025).
	// When set, TASK_QUEUE_URL is ignored and the agent uses GRPCQueue instead.
	// Set via SWARM_PLUGIN_QUEUE_ADDR, injected by the operator from spec.plugins.queue.address.
	ExternalQueueAddr string
	// ArtifactStoreURL is the connection URL for the artifact storage backend (RFC-0013).
	// When set, the agent runtime uploads step artifacts to this backend after each task.
	// Set via AGENT_ARTIFACT_STORE_URL, injected by the operator from the team's spec.artifactStore.
	// Example: "file:///data/artifacts", "s3://bucket/prefix?region=us-east-1"
	ArtifactStoreURL string
	// VectorStoreURL is the connection URL for the vector memory backend (RFC-0013).
	// When set, the agent runtime reads/writes vector embeddings for persistent memory.
	// Set via AGENT_VECTOR_STORE_URL, injected by the operator from SwarmMemory spec.
	// Example: "qdrant://qdrant.svc:6334/agent-memories"
	VectorStoreURL string
	// ArtifactDir is the local directory where agent tasks write output files.
	// After each task completes, the runner scans this directory and uploads files
	// to the ArtifactStore. Set via AGENT_ARTIFACT_DIR; defaults to /tmp/swarm-artifacts.
	ArtifactDir string
	// ReasoningMode is the RFC-0033 reasoning mode string ("Disabled"|"Auto"|"Explicit").
	// Set via AGENT_REASONING_MODE. Empty when reasoning is not configured.
	ReasoningMode string
	// ReasoningEffort is the provider-neutral effort hint ("Low"|"Medium"|"High").
	// Set via AGENT_REASONING_EFFORT. Empty when not set.
	ReasoningEffort string
	// ReasoningBudgetTokens is the Anthropic thinking-token budget.
	// Set via AGENT_REASONING_BUDGET_TOKENS. 0 when unset.
	ReasoningBudgetTokens int
	// MaxThinkingTokensPerCall is the per-turn thinking-token guardrail ceiling.
	// Set via AGENT_MAX_THINKING_TOKENS_PER_CALL. 0 when unset (no cap).
	MaxThinkingTokensPerCall int
	// MaxAnswerTokensPerCall is the per-turn answer-token guardrail ceiling.
	// Set via AGENT_MAX_ANSWER_TOKENS_PER_CALL. 0 when unset (no cap).
	MaxAnswerTokensPerCall int
	// ToolDenyPatterns is the merged tool deny list from SwarmPolicy.
	// Injected by the operator as AGENT_POLICY_TOOL_DENY (JSON array).
	// Entries are glob patterns matched against tool names at invocation time.
	ToolDenyPatterns []string
	// PolicyForceTrustLevel is the minimum trust level forced by SwarmPolicy.
	// Injected by the operator as AGENT_POLICY_FORCE_TRUST_LEVEL.
	PolicyForceTrustLevel string
	// LoopPolicy configures deep-research runtime hooks (RFC-0026).
	// Injected by the operator as AGENT_LOOP_POLICY (JSON) from spec.runtime.loop.
	// Nil when the field is unset on the SwarmAgent.
	LoopPolicy *LoopPolicyConfig
	// AuditLog configures the structured audit trail (RFC-0030).
	// Injected by the operator as AGENT_AUDIT_LOG (JSON) from the resolved audit config.
	// Nil when audit logging is disabled (mode=off or unset).
	AuditLog *AuditLogConfig
	// Advisors is the list of advisor connections for this agent (RFC-0048).
	// Injected by the operator as AGENT_ADVISORS (JSON array).
	// Nil when the agent has no advisor connections.
	Advisors []AdvisorConfig
	// GatewayCapabilities is the list of capabilities injected by the operator (RFC-0052).
	// Set via AGENT_GATEWAY_CAPABILITIES (JSON array). Nil when not a gateway agent.
	GatewayCapabilities []GatewayCapabilityConfig
	// GatewayConfig holds gateway dispatch settings (RFC-0052).
	// Set via AGENT_GATEWAY_CONFIG (JSON). Nil when not a gateway agent.
	GatewayConfig *GatewayRuntimeConfig
	// GatewayTools holds tool definitions injected by the operator (RFC-0052).
	// Set via AGENT_GATEWAY_TOOLS (JSON array). Nil when not a gateway agent.
	GatewayTools []GatewayToolConfig
	// CircuitBreaker configures the circuit breaker for tool calls.
	// Set via AGENT_CIRCUIT_BREAKER (JSON). Nil when not configured.
	CircuitBreaker *CircuitBreakerConfig
	// ArtifactSaveOutput auto-saves task output to AGENT_ARTIFACT_DIR.
	// Set via AGENT_ARTIFACT_SAVE_OUTPUT=true.
	ArtifactSaveOutput bool
	// ArtifactSaveFormat controls the output filename extension.
	// Set via AGENT_ARTIFACT_SAVE_FORMAT. Defaults to "text".
	ArtifactSaveFormat string
	// ArtifactSaveExtension is the resolved file extension (e.g. ".txt", ".json").
	// Derived from ArtifactSaveFormat via ArtifactFormatExtensions.
	ArtifactSaveExtension string
}

// ArtifactFormatExtensions maps format names to file extensions.
// Mirrors kubeswarmv1alpha1.ArtifactFormatExtension for use in the runtime
// module which cannot import the CRD types.
var ArtifactFormatExtensions = map[string]string{
	"text":     ".txt",
	"json":     ".json",
	"markdown": ".md",
	"yaml":     ".yaml",
}

// CircuitBreakerConfig holds the parsed circuit breaker settings.
type CircuitBreakerConfig struct {
	FailureThreshold int `json:"failureThreshold"`
	CooldownSeconds  int `json:"cooldownSeconds"`
	HalfOpenMaxCalls int `json:"halfOpenMaxCalls"`
}

// Load reads agent configuration from environment variables.
func Load() (*Config, error) {
	model, err := requireEnv("AGENT_MODEL")
	if err != nil {
		return nil, err
	}
	systemPrompt, err := loadSystemPrompt()
	if err != nil {
		return nil, err
	}
	queueURL, err := requireEnv("TASK_QUEUE_URL")
	if err != nil {
		return nil, err
	}

	streamURL := os.Getenv("STREAM_CHANNEL_URL")
	if streamURL == "" {
		streamURL = queueURL
	}

	cfg := &Config{
		Provider:           os.Getenv("AGENT_PROVIDER"), // auto-detected from model if empty
		Model:              model,
		SystemPrompt:       systemPrompt,
		TaskQueueURL:       queueURL,
		StreamChannelURL:   streamURL,
		MaxTokensPerCall:   8000,
		TimeoutSeconds:     120,
		MaxRetries:         3,
		MaxConcurrentTasks: 5,
		ValidatorPrompt:    os.Getenv("AGENT_VALIDATOR_PROMPT"),
	}

	if err := applyNumericEnvs(cfg); err != nil {
		return nil, err
	}

	if err := applyJSONEnvs(cfg); err != nil {
		return nil, err
	}

	lp, err := parseLoopPolicy()
	if err != nil {
		return nil, err
	}
	cfg.LoopPolicy = lp

	al, err := parseAuditLog()
	if err != nil {
		return nil, err
	}
	cfg.AuditLog = al

	cfg.Namespace = os.Getenv("AGENT_NAMESPACE")
	cfg.AgentName = os.Getenv("AGENT_NAME")
	cfg.TeamRole = os.Getenv("AGENT_TEAM_ROLE")
	cfg.TeamName = os.Getenv("AGENT_TEAM_NAME")
	cfg.ExternalProviderAddr = os.Getenv("SWARM_PLUGIN_LLM_ADDR")
	cfg.ExternalQueueAddr = os.Getenv("SWARM_PLUGIN_QUEUE_ADDR")
	cfg.ArtifactStoreURL = os.Getenv("AGENT_ARTIFACT_STORE_URL")
	cfg.VectorStoreURL = os.Getenv("AGENT_VECTOR_STORE_URL")
	cfg.ArtifactDir = envOrDefault("AGENT_ARTIFACT_DIR", "/tmp/swarm-artifacts")

	cfg.ReasoningMode = os.Getenv("AGENT_REASONING_MODE")
	cfg.ReasoningEffort = os.Getenv("AGENT_REASONING_EFFORT")
	budgetTokens, err := optionalIntEnv("AGENT_REASONING_BUDGET_TOKENS")
	if err != nil {
		return nil, err
	}
	cfg.ReasoningBudgetTokens = budgetTokens
	maxThinking, err := optionalIntEnv("AGENT_MAX_THINKING_TOKENS_PER_CALL")
	if err != nil {
		return nil, err
	}
	cfg.MaxThinkingTokensPerCall = maxThinking
	maxAnswer, err := optionalIntEnv("AGENT_MAX_ANSWER_TOKENS_PER_CALL")
	if err != nil {
		return nil, err
	}
	cfg.MaxAnswerTokensPerCall = maxAnswer

	if v := os.Getenv("AGENT_DAILY_TOKEN_LIMIT"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return nil, agenterrors.NewConfigError(agenterrors.ErrConfigInvalid, fmt.Sprintf("invalid AGENT_DAILY_TOKEN_LIMIT %q", v), err)
		}
		if n < 0 {
			return nil, agenterrors.NewConfigError(agenterrors.ErrConfigInvalid, fmt.Sprintf("AGENT_DAILY_TOKEN_LIMIT must be non-negative, got %d", n), nil)
		}
		cfg.DailyTokenLimit = n
	}

	cfg.PolicyForceTrustLevel = os.Getenv("AGENT_POLICY_FORCE_TRUST_LEVEL")

	// Parse advisor connections (RFC-0048).
	if v := os.Getenv("AGENT_ADVISORS"); v != "" {
		var advisors []AdvisorConfig
		if err := json.Unmarshal([]byte(v), &advisors); err != nil {
			return nil, agenterrors.NewConfigError(agenterrors.ErrConfigInvalid, "invalid AGENT_ADVISORS JSON", err)
		}
		cfg.Advisors = advisors
	}

	// Parse gateway configuration (RFC-0052).
	if v := os.Getenv("AGENT_GATEWAY_CAPABILITIES"); v != "" {
		var caps []GatewayCapabilityConfig
		if err := json.Unmarshal([]byte(v), &caps); err != nil {
			return nil, agenterrors.NewConfigError(agenterrors.ErrConfigInvalid, "invalid AGENT_GATEWAY_CAPABILITIES JSON", err)
		}
		cfg.GatewayCapabilities = caps
	}
	if v := os.Getenv("AGENT_GATEWAY_CONFIG"); v != "" {
		var gwCfg GatewayRuntimeConfig
		if err := json.Unmarshal([]byte(v), &gwCfg); err != nil {
			return nil, agenterrors.NewConfigError(agenterrors.ErrConfigInvalid, "invalid AGENT_GATEWAY_CONFIG JSON", err)
		}
		cfg.GatewayConfig = &gwCfg
	}
	if v := os.Getenv("AGENT_GATEWAY_TOOLS"); v != "" {
		var tools []GatewayToolConfig
		if err := json.Unmarshal([]byte(v), &tools); err != nil {
			return nil, agenterrors.NewConfigError(agenterrors.ErrConfigInvalid, "invalid AGENT_GATEWAY_TOOLS JSON", err)
		}
		cfg.GatewayTools = tools
	}

	if os.Getenv("AGENT_ARTIFACT_SAVE_OUTPUT") == "true" {
		cfg.ArtifactSaveOutput = true
		cfg.ArtifactSaveFormat = os.Getenv("AGENT_ARTIFACT_SAVE_FORMAT")
		if cfg.ArtifactSaveFormat == "" {
			cfg.ArtifactSaveFormat = "text"
		}
		cfg.ArtifactSaveExtension = ArtifactFormatExtensions[cfg.ArtifactSaveFormat]
		if cfg.ArtifactSaveExtension == "" {
			cfg.ArtifactSaveExtension = ".txt" // safe fallback
		}
	}

	if v := os.Getenv("AGENT_CIRCUIT_BREAKER"); v != "" {
		var cb CircuitBreakerConfig
		if err := json.Unmarshal([]byte(v), &cb); err != nil {
			return nil, agenterrors.NewConfigError(agenterrors.ErrConfigInvalid, "invalid AGENT_CIRCUIT_BREAKER JSON", err)
		}
		cfg.CircuitBreaker = &cb
	}

	return cfg, nil
}

// TaskTimeout returns the per-task deadline derived from cfg.
func TaskTimeout(cfg *Config) time.Duration {
	return time.Duration(cfg.TimeoutSeconds) * time.Second
}

func applyNumericEnvs(cfg *Config) error {
	if v := os.Getenv("AGENT_MAX_TOKENS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return agenterrors.NewConfigError(agenterrors.ErrConfigInvalid, fmt.Sprintf("invalid AGENT_MAX_TOKENS %q", v), err)
		}
		if n < 1 || n > 200_000 {
			return agenterrors.NewConfigError(agenterrors.ErrConfigInvalid, fmt.Sprintf("AGENT_MAX_TOKENS must be between 1 and 200000, got %d", n), nil)
		}
		cfg.MaxTokensPerCall = n
	}
	if v := os.Getenv("AGENT_TIMEOUT_SECONDS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return agenterrors.NewConfigError(agenterrors.ErrConfigInvalid, fmt.Sprintf("invalid AGENT_TIMEOUT_SECONDS %q", v), err)
		}
		if n < 1 || n > 86_400 {
			return agenterrors.NewConfigError(agenterrors.ErrConfigInvalid, fmt.Sprintf("AGENT_TIMEOUT_SECONDS must be between 1 and 86400, got %d", n), nil)
		}
		cfg.TimeoutSeconds = n
	}
	if v := os.Getenv("AGENT_MAX_RETRIES"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return agenterrors.NewConfigError(agenterrors.ErrConfigInvalid, fmt.Sprintf("invalid AGENT_MAX_RETRIES %q", v), err)
		}
		if n < 0 || n > 100 {
			return agenterrors.NewConfigError(agenterrors.ErrConfigInvalid, fmt.Sprintf("AGENT_MAX_RETRIES must be between 0 and 100, got %d", n), nil)
		}
		cfg.MaxRetries = n
	}
	if v := os.Getenv("AGENT_MAX_CONCURRENT_TASKS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return agenterrors.NewConfigError(agenterrors.ErrConfigInvalid, fmt.Sprintf("invalid AGENT_MAX_CONCURRENT_TASKS %q", v), err)
		}
		if n < 1 || n > 1000 {
			return agenterrors.NewConfigError(agenterrors.ErrConfigInvalid, fmt.Sprintf("AGENT_MAX_CONCURRENT_TASKS must be between 1 and 1000, got %d", n), nil)
		}
		cfg.MaxConcurrentTasks = n
	}
	return nil
}

func applyJSONEnvs(cfg *Config) error {
	if raw := os.Getenv("AGENT_MCP_SERVERS"); raw != "" {
		if err := json.Unmarshal([]byte(raw), &cfg.MCPServers); err != nil {
			return agenterrors.NewConfigError(agenterrors.ErrConfigInvalid, "invalid AGENT_MCP_SERVERS JSON", err)
		}
	}
	if raw := os.Getenv("AGENT_WEBHOOK_TOOLS"); raw != "" {
		if err := json.Unmarshal([]byte(raw), &cfg.WebhookTools); err != nil {
			return agenterrors.NewConfigError(agenterrors.ErrConfigInvalid, "invalid AGENT_WEBHOOK_TOOLS JSON", err)
		}
	}
	if raw := os.Getenv("AGENT_TEAM_ROUTES"); raw != "" {
		if err := json.Unmarshal([]byte(raw), &cfg.TeamRoutes); err != nil {
			return agenterrors.NewConfigError(agenterrors.ErrConfigInvalid, "invalid AGENT_TEAM_ROUTES JSON", err)
		}
	}
	if raw := os.Getenv("AGENT_POLICY_TOOL_DENY"); raw != "" {
		if err := json.Unmarshal([]byte(raw), &cfg.ToolDenyPatterns); err != nil {
			return agenterrors.NewConfigError(agenterrors.ErrConfigInvalid, "invalid AGENT_POLICY_TOOL_DENY JSON", err)
		}
	}
	return nil
}

func parseLoopPolicy() (*LoopPolicyConfig, error) {
	raw := os.Getenv("AGENT_LOOP_POLICY")
	if raw == "" {
		return nil, nil
	}
	var lp LoopPolicyConfig
	if err := json.Unmarshal([]byte(raw), &lp); err != nil {
		return nil, agenterrors.NewConfigError(agenterrors.ErrConfigInvalid, "invalid AGENT_LOOP_POLICY JSON", err)
	}
	// Merge embedding config injected separately from SwarmMemory.spec.embedding.
	// These override whatever the operator serialised into AGENT_LOOP_POLICY so the
	// memory hook uses the correct embedding model regardless of the task LLM.
	if embModel := os.Getenv("AGENT_EMBEDDING_MODEL"); embModel != "" {
		if lp.Memory == nil {
			lp.Memory = &LoopMemoryConfig{}
		}
		lp.Memory.EmbeddingModel = embModel
		lp.Memory.EmbeddingProvider = os.Getenv("AGENT_EMBEDDING_PROVIDER")
	}
	return &lp, nil
}

func parseAuditLog() (*AuditLogConfig, error) {
	raw := os.Getenv("AGENT_AUDIT_LOG")
	if raw == "" {
		return nil, nil
	}
	var al AuditLogConfig
	if err := json.Unmarshal([]byte(raw), &al); err != nil {
		return nil, agenterrors.NewConfigError(agenterrors.ErrConfigInvalid, "invalid AGENT_AUDIT_LOG JSON", err)
	}
	if al.Mode == "" || al.Mode == string(audit.ModeOff) {
		return nil, nil
	}
	return &al, nil
}

// loadSystemPrompt reads the system prompt from a file (AGENT_SYSTEM_PROMPT_PATH)
// or falls back to the AGENT_SYSTEM_PROMPT env var for backward compatibility.
func loadSystemPrompt() (string, error) {
	if path := os.Getenv("AGENT_SYSTEM_PROMPT_PATH"); path != "" {
		data, err := os.ReadFile(path) //nolint:gosec // path is a trusted operator-injected mount path
		if err != nil {
			return "", agenterrors.NewConfigError(agenterrors.ErrConfigInvalid, fmt.Sprintf("reading system prompt from %s", path), err)
		}
		prompt := strings.TrimSpace(string(data))
		if prompt == "" {
			return "", agenterrors.NewConfigError(agenterrors.ErrConfigInvalid, fmt.Sprintf("system prompt file %s is empty", path), nil)
		}
		return prompt, nil
	}
	return requireEnv("AGENT_SYSTEM_PROMPT")
}

func requireEnv(key string) (string, error) {
	v := os.Getenv(key)
	if v == "" {
		return "", agenterrors.NewConfigError(agenterrors.ErrConfigMissing, fmt.Sprintf("required env var %s is not set", key), nil)
	}
	return v, nil
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// optionalIntEnv reads an optional integer env var. An unset or empty
// value returns (0, nil). A malformed value returns (0, error).
func optionalIntEnv(key string) (int, error) {
	v := os.Getenv(key)
	if v == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, agenterrors.NewConfigError(agenterrors.ErrConfigInvalid, fmt.Sprintf("invalid %s %q", key, v), err)
	}
	return n, nil
}
