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
	"encoding/json"
	"strings"
	"testing"
	"time"
)

const testTeam = "my-team"

func TestNewEvent_SchemaVersion(t *testing.T) {
	t.Parallel()
	ev := NewEvent(ActionToolCalled, StatusSuccess, "default", "my-agent")
	if ev.SchemaVersion != "v1" {
		t.Errorf("SchemaVersion = %q, want %q", ev.SchemaVersion, "v1")
	}
}

func TestNewEvent_UniqueEventIDs(t *testing.T) {
	t.Parallel()
	seen := make(map[string]struct{})
	for i := range 100 {
		ev := NewEvent(ActionToolCalled, StatusSuccess, "default", "agent")
		if _, ok := seen[ev.EventID]; ok {
			t.Fatalf("duplicate eventId on iteration %d: %s", i, ev.EventID)
		}
		seen[ev.EventID] = struct{}{}
	}
}

func TestNewEvent_TimestampISO8601WithMilliseconds(t *testing.T) {
	t.Parallel()
	ev := NewEvent(ActionTaskReceived, StatusSuccess, "default", "agent")

	// Must parse as ISO 8601 / RFC 3339
	parsed, err := time.Parse(time.RFC3339Nano, ev.Timestamp)
	if err != nil {
		t.Fatalf("timestamp %q is not valid RFC3339Nano: %v", ev.Timestamp, err)
	}

	// Must include millisecond precision (contains a dot separator)
	if !strings.Contains(ev.Timestamp, ".") {
		t.Errorf("timestamp %q should include milliseconds", ev.Timestamp)
	}

	// Timestamp should be recent (within 5 seconds of now)
	if time.Since(parsed) > 5*time.Second {
		t.Errorf("timestamp %v is too old", parsed)
	}
}

func TestNewEvent_RequiredFieldsPopulated(t *testing.T) {
	t.Parallel()
	ev := NewEvent(ActionToolCalled, StatusSuccess, "prod", "coordinator")

	if ev.Action != ActionToolCalled {
		t.Errorf("Action = %q, want %q", ev.Action, ActionToolCalled)
	}
	if ev.Status != StatusSuccess {
		t.Errorf("Status = %q, want %q", ev.Status, StatusSuccess)
	}
	if ev.Namespace != "prod" {
		t.Errorf("Namespace = %q, want %q", ev.Namespace, "prod")
	}
	if ev.Agent != "coordinator" {
		t.Errorf("Agent = %q, want %q", ev.Agent, "coordinator")
	}
}

func TestAuditEvent_JSONSerialization_OmitsErrorWhenNil(t *testing.T) {
	t.Parallel()
	ev := NewEvent(ActionToolCalled, StatusSuccess, "default", "agent")
	ev.Error = nil

	data, err := json.Marshal(ev)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}

	// The "error" field should not appear in the JSON output
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	if _, ok := raw["error"]; ok {
		t.Error("JSON output should omit error field when nil")
	}
}

func TestAuditEvent_JSONSerialization_IncludesErrorWhenPresent(t *testing.T) {
	t.Parallel()
	ev := NewEvent(ActionTaskFailed, StatusError, "default", "agent")
	ev.Error = &AuditError{
		Message:   "connection refused",
		Code:      "ECONNREFUSED",
		Retryable: true,
	}

	data, err := json.Marshal(ev)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	if _, ok := raw["error"]; !ok {
		t.Error("JSON output should include error field when present")
	}
}

func TestAuditEvent_JSONRoundTrip(t *testing.T) {
	t.Parallel()
	ev := NewEvent(ActionToolCalled, StatusSuccess, "default", "agent")
	ev.Team = testTeam
	ev.RunID = "run-1"
	ev.TaskID = "task-1"
	ev.ParentEventID = "evt-parent"
	ev.Trigger = TriggerDelegation
	ev.Model = "qwen2.5:7b"
	ev.Provider = "openai"
	ev.Tokens = &TokenUsage{Input: 100, Output: 50}
	ev.Detail = json.RawMessage(`{"tool":"spawn_and_collect"}`)
	ev.Timing = &Timing{QueueMs: 10, ExecutionMs: 200, DownstreamWaitMs: 30}
	ev.Env = Env{Service: "agent-runner", Version: "0.1.0", PodName: "pod-abc"}

	data, err := json.Marshal(ev)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}

	var decoded AuditEvent
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}

	if decoded.EventID != ev.EventID {
		t.Errorf("EventID mismatch: got %q, want %q", decoded.EventID, ev.EventID)
	}
	if decoded.SchemaVersion != "v1" {
		t.Errorf("SchemaVersion mismatch: got %q, want %q", decoded.SchemaVersion, "v1")
	}
	if decoded.Team != testTeam {
		t.Errorf("Team mismatch: got %q, want %q", decoded.Team, testTeam)
	}
	if decoded.Tokens == nil || decoded.Tokens.Input != 100 || decoded.Tokens.Output != 50 {
		t.Errorf("Tokens mismatch: got %+v", decoded.Tokens)
	}
}

func TestActionTypeConstants(t *testing.T) {
	t.Parallel()

	actions := []Action{
		ActionTaskReceived,
		ActionTaskCompleted,
		ActionTaskFailed,
		ActionTaskRetried,
		ActionToolCalled,
		ActionToolDenied,
		ActionDelegateSent,
		ActionDelegateReceived,
		ActionBudgetChecked,
		ActionBudgetExceeded,
		ActionRunTriggered,
		ActionRunStepStarted,
		ActionRunStepCompleted,
		ActionRunSucceeded,
		ActionRunFailed,
		ActionGuardrailEvaluated,
		ActionMemoryRetrieved,
		ActionMemoryStored,
	}

	seen := make(map[Action]struct{})
	for _, a := range actions {
		if a == "" {
			t.Error("action constant must not be empty")
		}
		if _, ok := seen[a]; ok {
			t.Errorf("duplicate action constant: %q", a)
		}
		seen[a] = struct{}{}
	}
}

func TestStatusConstants(t *testing.T) {
	t.Parallel()

	statuses := []Status{
		StatusSuccess,
		StatusError,
		StatusTimeout,
		StatusDenied,
	}

	for _, s := range statuses {
		if s == "" {
			t.Error("status constant must not be empty")
		}
	}
}

func TestTriggerConstants(t *testing.T) {
	t.Parallel()

	triggers := []Trigger{
		TriggerUserRequest,
		TriggerSchedule,
		TriggerDelegation,
		TriggerRetry,
		TriggerDependencyResolved,
	}

	for _, tr := range triggers {
		if tr == "" {
			t.Error("trigger constant must not be empty")
		}
	}
}

func TestAuditEvent_JSONOmitsEmptyOptionalFields(t *testing.T) {
	t.Parallel()
	ev := NewEvent(ActionTaskReceived, StatusSuccess, "default", "agent")

	data, err := json.Marshal(ev)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}

	// Optional fields should be omitted when zero-value
	for _, field := range []string{"team", "runId", "taskId", "parentEventId", "trigger", "model", "provider", "tokens", "timing"} {
		if _, ok := raw[field]; ok {
			t.Errorf("expected field %q to be omitted when empty, but it was present", field)
		}
	}
}
