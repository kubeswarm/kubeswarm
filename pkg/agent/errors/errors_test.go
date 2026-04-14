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

package errors_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	agenterrors "github.com/kubeswarm/kubeswarm/pkg/agent/errors"
)

// TestErrorCode_Constants asserts all 15 error codes exist and are non-empty unique strings.
func TestErrorCode_Constants(t *testing.T) {
	all := []agenterrors.ErrorCode{
		agenterrors.ErrLLMTimeout,
		agenterrors.ErrLLMAuthFailed,
		agenterrors.ErrLLMRateLimited,
		agenterrors.ErrLLMContextExceeded,
		agenterrors.ErrLLMProviderError,
		agenterrors.ErrToolTimeout,
		agenterrors.ErrToolNotFound,
		agenterrors.ErrToolExecFailed,
		agenterrors.ErrToolInvalidArgs,
		agenterrors.ErrMemoryUnavailable,
		agenterrors.ErrMemoryQueryFailed,
		agenterrors.ErrQueueFull,
		agenterrors.ErrQueueTimeout,
		agenterrors.ErrConfigInvalid,
		agenterrors.ErrConfigMissing,
	}
	if len(all) != 15 {
		t.Fatalf("expected 15 error codes, got %d", len(all))
	}
	seen := map[agenterrors.ErrorCode]bool{}
	for _, code := range all {
		if string(code) == "" {
			t.Errorf("error code must not be empty")
		}
		if seen[code] {
			t.Errorf("duplicate error code: %q", code)
		}
		seen[code] = true
	}
}

// TestAgentError_ImplementsError asserts *AgentError satisfies the error interface.
func TestAgentError_ImplementsError(t *testing.T) {
	// Compile-time check: *AgentError implements error.
	var _ error = (*agenterrors.AgentError)(nil)

	e := &agenterrors.AgentError{
		Code:    agenterrors.ErrLLMTimeout,
		Message: "test",
	}
	got := e.Error()
	if got == "" {
		t.Fatal("Error() returned empty string")
	}
}

// TestAgentError_Unwrap asserts errors.Unwrap returns the Cause and errors.Is works through wrapping.
func TestAgentError_Unwrap(t *testing.T) {
	cause := fmt.Errorf("inner: %w", context.DeadlineExceeded)
	ae := &agenterrors.AgentError{
		Code:    agenterrors.ErrLLMTimeout,
		Message: "timed out",
		Cause:   cause,
	}
	if errors.Unwrap(ae) != cause {
		t.Errorf("Unwrap() = %v, want %v", errors.Unwrap(ae), cause)
	}
	if !errors.Is(ae, context.DeadlineExceeded) {
		t.Error("errors.Is(agentErr, context.DeadlineExceeded) should be true")
	}
}

// TestAgentError_ErrorString asserts .Error() returns a readable string containing code and message.
func TestAgentError_ErrorString(t *testing.T) {
	ae := &agenterrors.AgentError{
		Code:    agenterrors.ErrLLMTimeout,
		Message: "provider timed out after 30s",
	}
	s := ae.Error()
	if !strings.Contains(s, string(agenterrors.ErrLLMTimeout)) {
		t.Errorf("Error() = %q, want it to contain %q", s, agenterrors.ErrLLMTimeout)
	}
	if !strings.Contains(s, "provider timed out after 30s") {
		t.Errorf("Error() = %q, want it to contain the message", s)
	}
}

// TestAgentError_DefaultSuggestion asserts NewLLMError populates a non-empty default suggestion for LLMTimeout.
func TestAgentError_DefaultSuggestion(t *testing.T) {
	ae := agenterrors.NewLLMError(agenterrors.ErrLLMTimeout, "timed out", nil)
	if ae.Suggestion == "" {
		t.Error("expected non-empty default Suggestion for ErrLLMTimeout")
	}
}

// TestAgentError_Component asserts that each component group sets the correct Component string.
func TestAgentError_Component(t *testing.T) {
	cases := []struct {
		name     string
		err      *agenterrors.AgentError
		wantComp string
	}{
		{"LLM", agenterrors.NewLLMError(agenterrors.ErrLLMTimeout, "t", nil), "llm"},
		{"Tool", agenterrors.NewToolError(agenterrors.ErrToolTimeout, "t", nil), "tool"},
		{"Memory", agenterrors.NewMemoryError(agenterrors.ErrMemoryUnavailable, "m", nil), "memory"},
		{"Config", agenterrors.NewConfigError(agenterrors.ErrConfigInvalid, "c", nil), "config"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.err.Component != tc.wantComp {
				t.Errorf("Component = %q, want %q", tc.err.Component, tc.wantComp)
			}
		})
	}
}

// TestNewLLMError asserts the convenience constructor sets Component="llm".
func TestNewLLMError(t *testing.T) {
	cause := fmt.Errorf("api error")
	ae := agenterrors.NewLLMError(agenterrors.ErrLLMAuthFailed, "bad key", cause)
	if ae.Component != "llm" {
		t.Errorf("Component = %q, want %q", ae.Component, "llm")
	}
	if ae.Code != agenterrors.ErrLLMAuthFailed {
		t.Errorf("Code = %q, want %q", ae.Code, agenterrors.ErrLLMAuthFailed)
	}
	if ae.Message != "bad key" {
		t.Errorf("Message = %q, want %q", ae.Message, "bad key")
	}
	if ae.Cause != cause {
		t.Errorf("Cause = %v, want %v", ae.Cause, cause)
	}
}

// TestNewToolError asserts the convenience constructor sets Component="tool".
func TestNewToolError(t *testing.T) {
	ae := agenterrors.NewToolError(agenterrors.ErrToolNotFound, "no such tool: search", nil)
	if ae.Component != "tool" {
		t.Errorf("Component = %q, want %q", ae.Component, "tool")
	}
	if ae.Code != agenterrors.ErrToolNotFound {
		t.Errorf("Code = %q, want %q", ae.Code, agenterrors.ErrToolNotFound)
	}
}

// TestNewMemoryError asserts the convenience constructor sets Component="memory".
func TestNewMemoryError(t *testing.T) {
	ae := agenterrors.NewMemoryError(agenterrors.ErrMemoryQueryFailed, "query failed", nil)
	if ae.Component != "memory" {
		t.Errorf("Component = %q, want %q", ae.Component, "memory")
	}
	if ae.Code != agenterrors.ErrMemoryQueryFailed {
		t.Errorf("Code = %q, want %q", ae.Code, agenterrors.ErrMemoryQueryFailed)
	}
}

// TestNewConfigError asserts the convenience constructor sets Component="config".
func TestNewConfigError(t *testing.T) {
	ae := agenterrors.NewConfigError(agenterrors.ErrConfigMissing, "missing field", nil)
	if ae.Component != "config" {
		t.Errorf("Component = %q, want %q", ae.Component, "config")
	}
	if ae.Code != agenterrors.ErrConfigMissing {
		t.Errorf("Code = %q, want %q", ae.Code, agenterrors.ErrConfigMissing)
	}
}
