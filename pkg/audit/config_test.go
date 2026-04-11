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

import (
	"testing"
)

func TestResolveConfig_AgentOverridesNamespace(t *testing.T) {
	t.Parallel()

	cluster := AuditConfig{Mode: ModeActions}
	namespace := AuditConfig{Mode: ModeVerbose}
	agent := AuditConfig{Mode: ModeOff}

	resolved := ResolveConfig(cluster, namespace, agent)

	if resolved.Mode != ModeOff {
		t.Errorf("Mode = %q, want %q (agent overrides namespace)", resolved.Mode, ModeOff)
	}
}

func TestResolveConfig_NamespaceOverridesCluster(t *testing.T) {
	t.Parallel()

	cluster := AuditConfig{Mode: ModeActions}
	namespace := AuditConfig{Mode: ModeVerbose}
	agent := AuditConfig{} // unset

	resolved := ResolveConfig(cluster, namespace, agent)

	if resolved.Mode != ModeVerbose {
		t.Errorf("Mode = %q, want %q (namespace overrides cluster)", resolved.Mode, ModeVerbose)
	}
}

func TestResolveConfig_UnsetInheritsFromNextLevel(t *testing.T) {
	t.Parallel()

	cluster := AuditConfig{Mode: ModeActions}
	namespace := AuditConfig{} // unset
	agent := AuditConfig{}     // unset

	resolved := ResolveConfig(cluster, namespace, agent)

	if resolved.Mode != ModeActions {
		t.Errorf("Mode = %q, want %q (inherit from cluster)", resolved.Mode, ModeActions)
	}
}

func TestResolveConfig_AllUnsetDefaultsToOff(t *testing.T) {
	t.Parallel()

	resolved := ResolveConfig(AuditConfig{}, AuditConfig{}, AuditConfig{})

	if resolved.Mode != ModeOff {
		t.Errorf("Mode = %q, want %q (default is off)", resolved.Mode, ModeOff)
	}
}

func TestResolveConfig_PrecedenceTable(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		cluster   Mode
		namespace Mode
		agent     Mode
		want      Mode
	}{
		{"off/unset/unset", ModeOff, "", "", ModeOff},
		{"off/actions/unset", ModeOff, ModeActions, "", ModeActions},
		{"off/unset/actions", ModeOff, "", ModeActions, ModeActions},
		{"actions/off/unset", ModeActions, ModeOff, "", ModeOff},
		{"actions/unset/off", ModeActions, "", ModeOff, ModeOff},
		{"actions/verbose/unset", ModeActions, ModeVerbose, "", ModeVerbose},
		{"verbose/actions/unset", ModeVerbose, ModeActions, "", ModeActions},
		{"off/actions/verbose", ModeOff, ModeActions, ModeVerbose, ModeVerbose},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cluster := AuditConfig{Mode: tt.cluster}
			namespace := AuditConfig{Mode: tt.namespace}
			agent := AuditConfig{Mode: tt.agent}

			resolved := ResolveConfig(cluster, namespace, agent)

			if resolved.Mode != tt.want {
				t.Errorf("Mode = %q, want %q", resolved.Mode, tt.want)
			}
		})
	}
}

func TestResolveConfig_RedactionRulesMergeAcrossLevels(t *testing.T) {
	t.Parallel()

	cluster := AuditConfig{
		Redact: []string{"detail.input.*.apiKey"},
	}
	namespace := AuditConfig{
		Redact: []string{"detail.input.*.ssn"},
	}
	agent := AuditConfig{
		Redact: []string{"detail.output.*.secret"},
	}

	resolved := ResolveConfig(cluster, namespace, agent)

	if len(resolved.Redact) != 3 {
		t.Fatalf("Redact has %d patterns, want 3", len(resolved.Redact))
	}

	// All three patterns should be present
	patterns := make(map[string]bool)
	for _, p := range resolved.Redact {
		patterns[p] = true
	}

	for _, want := range []string{
		"detail.input.*.apiKey",
		"detail.input.*.ssn",
		"detail.output.*.secret",
	} {
		if !patterns[want] {
			t.Errorf("missing redaction pattern: %q", want)
		}
	}
}

func TestResolveConfig_RedactionDeduplicate(t *testing.T) {
	t.Parallel()

	cluster := AuditConfig{
		Redact: []string{"detail.input.*.apiKey"},
	}
	namespace := AuditConfig{
		Redact: []string{"detail.input.*.apiKey"}, // duplicate
	}
	agent := AuditConfig{}

	resolved := ResolveConfig(cluster, namespace, agent)

	// Duplicates should be merged (not duplicated)
	count := 0
	for _, p := range resolved.Redact {
		if p == "detail.input.*.apiKey" {
			count++
		}
	}
	if count > 1 {
		t.Errorf("duplicate redaction pattern found %d times, want at most 1", count)
	}
}

func TestResolveConfig_SinkOverride(t *testing.T) {
	t.Parallel()

	cluster := AuditConfig{
		Mode: ModeActions,
		Sink: SinkStdout,
	}
	agent := AuditConfig{
		Sink: SinkRedis,
	}

	resolved := ResolveConfig(cluster, AuditConfig{}, agent)

	if resolved.Sink != SinkRedis {
		t.Errorf("Sink = %q, want %q (agent overrides cluster)", resolved.Sink, SinkRedis)
	}
}

func TestResolveConfig_MaxDetailBytesOverride(t *testing.T) {
	t.Parallel()

	cluster := AuditConfig{
		Mode:           ModeActions,
		MaxDetailBytes: 4096,
	}
	agent := AuditConfig{
		MaxDetailBytes: 16384,
	}

	resolved := ResolveConfig(cluster, AuditConfig{}, agent)

	if resolved.MaxDetailBytes != 16384 {
		t.Errorf("MaxDetailBytes = %d, want 16384 (agent overrides)", resolved.MaxDetailBytes)
	}
}

func TestModeConstants(t *testing.T) {
	t.Parallel()

	if ModeOff != "off" {
		t.Errorf("ModeOff = %q, want %q", ModeOff, "off")
	}
	if ModeActions != "actions" {
		t.Errorf("ModeActions = %q, want %q", ModeActions, "actions")
	}
	if ModeVerbose != "verbose" {
		t.Errorf("ModeVerbose = %q, want %q", ModeVerbose, "verbose")
	}
}

func TestSinkConstants(t *testing.T) {
	t.Parallel()

	if SinkStdout != "stdout" {
		t.Errorf("SinkStdout = %q, want %q", SinkStdout, "stdout")
	}
	if SinkRedis != "redis" {
		t.Errorf("SinkRedis = %q, want %q", SinkRedis, "redis")
	}
	if SinkWebhook != "webhook" {
		t.Errorf("SinkWebhook = %q, want %q", SinkWebhook, "webhook")
	}
}
