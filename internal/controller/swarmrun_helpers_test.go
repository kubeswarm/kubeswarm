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

package controller

import (
	"strings"
	"testing"

	kubeswarmv1alpha1 "github.com/kubeswarm/kubeswarm/api/v1alpha1"
)

// TestParseAgentErrorFields_ValidCode covers TEST-TRACKER.md items:
//
//	[x] Status: steps[].errorCode populated on failure
//	[x] Status: steps[].errorSuggestion populated on failure
//
// Behaviour under test: when an agent error message uses the "[ErrorCode] message"
// format, parseAgentErrorFields extracts the code and returns the matching suggestion.
func TestParseAgentErrorFields_ValidCode(t *testing.T) {
	code, suggestion := parseAgentErrorFields("[LLMTimeout] request timed out after 30s")
	if code != "LLMTimeout" {
		t.Errorf("code = %q, want %q", code, "LLMTimeout")
	}
	if suggestion == "" {
		t.Error("suggestion is empty for known error code LLMTimeout")
	}
	if !strings.Contains(suggestion, "timeoutSeconds") {
		t.Errorf("suggestion = %q, expected to mention timeoutSeconds", suggestion)
	}
}

func TestParseAgentErrorFields_UnknownCode(t *testing.T) {
	code, suggestion := parseAgentErrorFields("[UnknownXYZ] something broke")
	if code != "UnknownXYZ" {
		t.Errorf("code = %q, want %q", code, "UnknownXYZ")
	}
	if suggestion != "" {
		t.Errorf("suggestion = %q for unknown code, want empty", suggestion)
	}
}

func TestParseAgentErrorFields_NoCodeFormat(t *testing.T) {
	code, suggestion := parseAgentErrorFields("plain error message without brackets")
	if code != "" {
		t.Errorf("code = %q for plain message, want empty", code)
	}
	if suggestion != "" {
		t.Errorf("suggestion = %q for plain message, want empty", suggestion)
	}
}

func TestParseAgentErrorFields_AllKnownCodes(t *testing.T) {
	codes := []string{
		"LLMTimeout", "LLMAuthFailed", "LLMRateLimited", "LLMContextExceeded",
		"LLMProviderError", "ToolTimeout", "ToolNotFound", "ToolExecutionFailed",
		"ToolInvalidArgs", "ToolDenied", "MemoryUnavailable", "MemoryQueryFailed",
		"QueueFull", "QueueTimeout", "ConfigInvalid", "ConfigMissing",
	}
	for _, c := range codes {
		code, suggestion := parseAgentErrorFields("[" + c + "] test message")
		if code != c {
			t.Errorf("code = %q, want %q", code, c)
		}
		if suggestion == "" {
			t.Errorf("suggestion is empty for known code %q", c)
		}
	}
}

// TestTruncateOutput covers TEST-TRACKER.md item:
//
//	[x] Pipeline: maxOutputBytes truncation enforced
//
// Behaviour under test: when a step's output exceeds maxOutputBytes, the controller
// truncates it and appends a "[truncated]" marker.
func TestTruncateOutput(t *testing.T) {
	t.Run("under limit unchanged", func(t *testing.T) {
		got := truncateOutput("short", 100)
		if got != "short" {
			t.Errorf("got %q, want %q", got, "short")
		}
	})

	t.Run("at limit unchanged", func(t *testing.T) {
		input := strings.Repeat("x", 100)
		got := truncateOutput(input, 100)
		if got != input {
			t.Errorf("got len=%d, want len=100", len(got))
		}
	})

	t.Run("over limit truncated", func(t *testing.T) {
		input := strings.Repeat("x", 200)
		got := truncateOutput(input, 100)
		if len(got) > 100+len(" [truncated]") {
			t.Errorf("got len=%d, expected around 112", len(got))
		}
		if !strings.HasSuffix(got, "[truncated]") {
			t.Errorf("got %q, expected [truncated] suffix", got)
		}
	})

	t.Run("zero limit is unlimited", func(t *testing.T) {
		input := strings.Repeat("x", 10000)
		got := truncateOutput(input, 0)
		if got != input {
			t.Error("zero maxBytes should not truncate")
		}
	})
}

// TestSumRunTokens covers TEST-TRACKER.md item:
//
//	[x] Run-level maxTokens cap enforced
//
// Behaviour under test: sumRunTokens correctly sums TotalTokens across all
// completed steps, ignoring steps with nil TokenUsage.
func TestSumRunTokens(t *testing.T) {
	run := &kubeswarmv1alpha1.SwarmRun{
		Status: kubeswarmv1alpha1.SwarmRunStatus{
			Steps: []kubeswarmv1alpha1.PipelineStepStatus{
				{Name: "step-a", TokenUsage: &kubeswarmv1alpha1.TokenUsage{TotalTokens: 500}},
				{Name: "step-b", TokenUsage: nil}, // not yet completed
				{Name: "step-c", TokenUsage: &kubeswarmv1alpha1.TokenUsage{TotalTokens: 300}},
			},
		},
	}

	got := sumRunTokens(run)
	if got != 800 {
		t.Errorf("sumRunTokens = %d, want 800", got)
	}
}

func TestSumRunTokens_Empty(t *testing.T) {
	run := &kubeswarmv1alpha1.SwarmRun{}
	got := sumRunTokens(run)
	if got != 0 {
		t.Errorf("sumRunTokens = %d, want 0", got)
	}
}
