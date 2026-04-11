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
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
)

func TestStdoutSink_WritesOneJSONLinePerEvent(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	sink := NewStdoutSink(&buf)

	events := []AuditEvent{
		NewEvent(ActionTaskReceived, StatusSuccess, "default", "agent-a"),
		NewEvent(ActionToolCalled, StatusSuccess, "default", "agent-b"),
		NewEvent(ActionTaskCompleted, StatusSuccess, "default", "agent-c"),
	}

	if err := sink.Emit(context.Background(), events); err != nil {
		t.Fatalf("Emit: %v", err)
	}

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != len(events) {
		t.Fatalf("got %d lines, want %d", len(lines), len(events))
	}
}

func TestStdoutSink_EachLineIsValidJSON(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	sink := NewStdoutSink(&buf)

	events := []AuditEvent{
		NewEvent(ActionToolCalled, StatusSuccess, "default", "agent"),
		NewEvent(ActionTaskFailed, StatusError, "default", "agent"),
	}
	events[1].Error = &AuditError{Message: "boom", Code: "500", Retryable: false}

	if err := sink.Emit(context.Background(), events); err != nil {
		t.Fatalf("Emit: %v", err)
	}

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	for i, line := range lines {
		if !json.Valid([]byte(line)) {
			t.Errorf("line %d is not valid JSON: %s", i, line)
		}
	}
}

func TestStdoutSink_DeserializesBackToAuditEvent(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	sink := NewStdoutSink(&buf)

	original := NewEvent(ActionToolCalled, StatusSuccess, "ns-1", "my-agent")
	original.Team = testTeam
	original.Model = "gpt-4"

	if err := sink.Emit(context.Background(), []AuditEvent{original}); err != nil {
		t.Fatalf("Emit: %v", err)
	}

	var decoded AuditEvent
	if err := json.Unmarshal([]byte(strings.TrimSpace(buf.String())), &decoded); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}

	if decoded.EventID != original.EventID {
		t.Errorf("EventID = %q, want %q", decoded.EventID, original.EventID)
	}
	if decoded.Agent != "my-agent" {
		t.Errorf("Agent = %q, want %q", decoded.Agent, "my-agent")
	}
	if decoded.Team != testTeam {
		t.Errorf("Team = %q, want %q", decoded.Team, testTeam)
	}
}

func TestStdoutSink_ConcurrentWritesDoNotInterleave(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	sink := NewStdoutSink(&buf)

	const goroutines = 10
	const eventsPerCall = 5

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for range goroutines {
		go func() {
			defer wg.Done()
			events := make([]AuditEvent, eventsPerCall)
			for i := range events {
				events[i] = NewEvent(ActionToolCalled, StatusSuccess, "default", "agent")
			}
			_ = sink.Emit(context.Background(), events)
		}()
	}
	wg.Wait()

	// Every line must be valid JSON (no interleaved partial writes)
	output := strings.TrimSpace(buf.String())
	if output == "" {
		t.Fatal("expected output, got empty string")
	}
	lines := strings.Split(output, "\n")

	totalExpected := goroutines * eventsPerCall
	if len(lines) != totalExpected {
		t.Errorf("got %d lines, want %d", len(lines), totalExpected)
	}

	for i, line := range lines {
		if !json.Valid([]byte(line)) {
			t.Errorf("line %d is not valid JSON (possible interleaving): %s", i, line)
		}
	}
}

func TestStdoutSink_EmptyBatch(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	sink := NewStdoutSink(&buf)

	if err := sink.Emit(context.Background(), nil); err != nil {
		t.Fatalf("Emit with nil slice: %v", err)
	}
	if err := sink.Emit(context.Background(), []AuditEvent{}); err != nil {
		t.Fatalf("Emit with empty slice: %v", err)
	}

	if buf.Len() != 0 {
		t.Errorf("expected no output for empty batches, got %q", buf.String())
	}
}
