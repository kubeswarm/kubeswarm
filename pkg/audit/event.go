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

// Package audit implements the structured audit trail for kubeswarm agents
// (RFC-0030). It provides event construction, pluggable sinks (stdout, Redis),
// field redaction, detail truncation, and a non-blocking emitter with bounded
// buffering.
package audit

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"time"
)

// Action represents the type of agent action being audited.
type Action string

// Supported audit action types.
const (
	ActionTaskReceived       Action = "task.received"
	ActionTaskCompleted      Action = "task.completed"
	ActionTaskFailed         Action = "task.failed"
	ActionTaskRetried        Action = "task.retried"
	ActionToolCalled         Action = "tool.called"
	ActionToolDenied         Action = "tool.denied"
	ActionDelegateSent       Action = "delegate.sent"
	ActionDelegateReceived   Action = "delegate.received"
	ActionBudgetChecked      Action = "budget.checked"
	ActionBudgetExceeded     Action = "budget.exceeded"
	ActionRunTriggered       Action = "run.triggered"
	ActionRunStepStarted     Action = "run.step.started"
	ActionRunStepCompleted   Action = "run.step.completed"
	ActionRunSucceeded       Action = "run.succeeded"
	ActionRunFailed          Action = "run.failed"
	ActionGuardrailEvaluated Action = "guardrail.evaluated"
	ActionMemoryRetrieved    Action = "memory.retrieved"
	ActionMemoryStored       Action = "memory.stored"
)

// Status represents the outcome of an audited action.
type Status string

// Supported audit status values.
const (
	StatusSuccess Status = "success"
	StatusError   Status = "error"
	StatusTimeout Status = "timeout"
	StatusDenied  Status = "denied"
)

// Trigger describes what initiated an audited action.
type Trigger string

// Supported trigger values.
const (
	TriggerUserRequest        Trigger = "user_request"
	TriggerSchedule           Trigger = "schedule"
	TriggerDelegation         Trigger = "delegation"
	TriggerRetry              Trigger = "retry"
	TriggerDependencyResolved Trigger = "dependency_resolved"
)

// AuditEvent is a single structured audit record capturing an agent action.
// Events are append-only and immutable once created.
type AuditEvent struct {
	SchemaVersion string `json:"schemaVersion"`
	EventID       string `json:"eventId"`
	Timestamp     string `json:"timestamp"`
	Action        Action `json:"action"`
	Status        Status `json:"status"`

	Namespace string `json:"namespace"`
	Agent     string `json:"agent"`
	Team      string `json:"team,omitempty"`
	RunID     string `json:"runId,omitempty"`
	TaskID    string `json:"taskId,omitempty"`

	ParentEventID string  `json:"parentEventId,omitempty"`
	Trigger       Trigger `json:"trigger,omitempty"`

	Model    string `json:"model,omitempty"`
	Provider string `json:"provider,omitempty"`

	Tokens *TokenUsage     `json:"tokens,omitempty"`
	Detail json.RawMessage `json:"detail,omitempty"`
	Timing *Timing         `json:"timing,omitempty"`
	Error  *AuditError     `json:"error,omitempty"`
	Env    Env             `json:"env,omitempty"`
}

// TokenUsage records input and output token counts for LLM-touching actions.
type TokenUsage struct {
	Input  int64 `json:"input"`
	Output int64 `json:"output"`
}

// Timing records duration breakdowns for an audited action.
type Timing struct {
	QueueMs          int64 `json:"queueMs,omitempty"`
	ExecutionMs      int64 `json:"executionMs,omitempty"`
	DownstreamWaitMs int64 `json:"downstreamWaitMs,omitempty"`
}

// AuditError captures error details when an action fails.
type AuditError struct {
	Message   string `json:"message"`
	Code      string `json:"code,omitempty"`
	Retryable bool   `json:"retryable,omitempty"`
}

// Env captures the runtime environment of the emitting process.
type Env struct {
	Service string `json:"service,omitempty"`
	Version string `json:"version,omitempty"`
	PodName string `json:"podName,omitempty"`
}

// NewEvent creates a new AuditEvent with a generated UUID v4 event ID and the
// current timestamp. The caller must populate action-specific fields after creation.
func NewEvent(action Action, status Status, namespace, agent string) AuditEvent {
	return AuditEvent{
		SchemaVersion: "v1",
		EventID:       generateUUID(),
		Timestamp:     time.Now().UTC().Format("2006-01-02T15:04:05.000Z"),
		Action:        action,
		Status:        status,
		Namespace:     namespace,
		Agent:         agent,
	}
}

// generateUUID produces a UUID v4 string using crypto/rand.
func generateUUID() string {
	var uuid [16]byte
	_, _ = rand.Read(uuid[:])
	// Set version 4.
	uuid[6] = (uuid[6] & 0x0f) | 0x40
	// Set variant bits.
	uuid[8] = (uuid[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		uuid[0:4], uuid[4:6], uuid[6:8], uuid[8:10], uuid[10:16])
}
