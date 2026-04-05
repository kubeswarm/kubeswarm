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

package audit

// Mode controls the verbosity of audit logging. It is an alias used
// throughout the audit package for conciseness.
type Mode = AuditLogMode

// AuditLogMode controls the verbosity of audit logging.
type AuditLogMode string

// Supported audit log modes.
const (
	// ModeOff disables audit logging (default).
	ModeOff AuditLogMode = "off"
	// ModeActions emits structured action events (tool calls, delegations, lifecycle).
	ModeActions AuditLogMode = "actions"
	// ModeVerbose emits action events plus full LLM conversation turns.
	ModeVerbose AuditLogMode = "verbose"
)

// SinkType selects the audit event output backend.
type SinkType string

// Supported sink types.
const (
	// SinkStdout writes JSON lines to stdout.
	SinkStdout SinkType = "stdout"
	// SinkRedis writes to a Redis Stream.
	SinkRedis SinkType = "redis"
	// SinkWebhook posts batches to an HTTP endpoint.
	SinkWebhook SinkType = "webhook"
)

// AuditConfig holds the resolved audit configuration for an agent or operator.
type AuditConfig struct {
	// Mode controls the verbosity of audit logging.
	Mode AuditLogMode `json:"mode,omitempty"`
	// Sink selects the output backend.
	Sink SinkType `json:"sink,omitempty"`
	// MaxDetailBytes is the maximum size for detail.input and detail.output
	// fields before truncation. 0 means unlimited.
	MaxDetailBytes int `json:"maxDetailBytes,omitempty"`
	// Redact is a list of glob patterns for field path redaction.
	Redact []string `json:"redact,omitempty"`
	// ExcludeActions is a list of action types to skip.
	ExcludeActions []string `json:"excludeActions,omitempty"`
}

// ResolveConfig merges audit configuration from three levels of precedence:
// cluster (Helm), namespace (SwarmSettings), and agent (SwarmAgent spec).
// Most specific non-empty Mode wins. Redact patterns are merged (union,
// deduplicated). MaxDetailBytes uses the most specific non-zero value, defaulting
// to DefaultMaxDetailBytes.
func ResolveConfig(cluster, namespace, agent AuditConfig) AuditConfig {
	resolved := AuditConfig{
		Mode:           ModeOff,
		Sink:           SinkStdout,
		MaxDetailBytes: DefaultMaxDetailBytes,
	}

	// Apply cluster defaults first.
	applyConfig(&resolved, &cluster)
	// Namespace overrides cluster.
	applyConfig(&resolved, &namespace)
	// Agent overrides namespace.
	applyConfig(&resolved, &agent)

	// Merge redaction patterns from all levels.
	resolved.Redact = MergeRedactionPatterns(
		cluster.Redact,
		namespace.Redact,
		agent.Redact,
	)

	// Merge exclude actions from all levels.
	resolved.ExcludeActions = mergeStringSlices(
		cluster.ExcludeActions,
		namespace.ExcludeActions,
		agent.ExcludeActions,
	)

	return resolved
}

// applyConfig applies non-empty fields from src to dst.
func applyConfig(dst *AuditConfig, src *AuditConfig) {
	if src.Mode != "" {
		dst.Mode = src.Mode
	}
	if src.Sink != "" {
		dst.Sink = src.Sink
	}
	if src.MaxDetailBytes != 0 {
		dst.MaxDetailBytes = src.MaxDetailBytes
	}
}

// MergeRedactionPatterns merges multiple lists of redaction patterns into a
// single deduplicated list preserving order of first occurrence.
func MergeRedactionPatterns(patterns ...[]string) []string {
	seen := make(map[string]struct{})
	var merged []string
	for _, list := range patterns {
		for _, p := range list {
			if _, ok := seen[p]; !ok {
				seen[p] = struct{}{}
				merged = append(merged, p)
			}
		}
	}
	return merged
}

// mergeStringSlices merges multiple string slices into a single deduplicated list.
func mergeStringSlices(slices ...[]string) []string {
	seen := make(map[string]struct{})
	var merged []string
	for _, list := range slices {
		for _, s := range list {
			if _, ok := seen[s]; !ok {
				seen[s] = struct{}{}
				merged = append(merged, s)
			}
		}
	}
	return merged
}
