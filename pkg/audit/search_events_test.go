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

// 1. Verify all 5 search audit action constants have correct string values.
func TestSearchAuditActionConstants(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		action Action
		want   string
	}{
		{"node created", ActionSearchNodeCreated, "search.node.created"},
		{"node scored", ActionSearchNodeScored, "search.node.scored"},
		{"node pruned", ActionSearchNodePruned, "search.node.pruned"},
		{"node eval failed", ActionSearchNodeEvalFailed, "search.node.evalFailed"},
		{"node solution", ActionSearchNodeSolution, "search.node.solution"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if string(tt.action) != tt.want {
				t.Errorf("Action = %q, want %q", tt.action, tt.want)
			}
		})
	}
}

// 2. Verify all search action constants are non-empty and unique.
func TestSearchAuditActionConstants_UniqueAndNonEmpty(t *testing.T) {
	t.Parallel()

	actions := []Action{
		ActionSearchNodeCreated,
		ActionSearchNodeScored,
		ActionSearchNodePruned,
		ActionSearchNodeEvalFailed,
		ActionSearchNodeSolution,
	}

	seen := make(map[Action]struct{})
	for _, a := range actions {
		if a == "" {
			t.Error("search action constant must not be empty")
		}
		if _, ok := seen[a]; ok {
			t.Errorf("duplicate search action constant: %q", a)
		}
		seen[a] = struct{}{}
	}
}

// 3. Verify NewEvent with a search action creates a valid event.
func TestNewSearchAuditEvent(t *testing.T) {
	t.Parallel()

	ev := NewEvent(ActionSearchNodeCreated, StatusSuccess, "default", "search-agent")

	if ev.SchemaVersion != "v1" {
		t.Errorf("SchemaVersion = %q, want %q", ev.SchemaVersion, "v1")
	}
	if ev.EventID == "" {
		t.Error("EventID must not be empty")
	}
	if ev.Timestamp == "" {
		t.Error("Timestamp must not be empty")
	}
	if ev.Action != ActionSearchNodeCreated {
		t.Errorf("Action = %q, want %q", ev.Action, ActionSearchNodeCreated)
	}
	if ev.Status != StatusSuccess {
		t.Errorf("Status = %q, want %q", ev.Status, StatusSuccess)
	}
	if ev.Namespace != "default" {
		t.Errorf("Namespace = %q, want %q", ev.Namespace, "default")
	}
	if ev.Agent != "search-agent" {
		t.Errorf("Agent = %q, want %q", ev.Agent, "search-agent")
	}
}
