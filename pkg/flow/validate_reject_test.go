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

package flow

import (
	"strings"
	"testing"

	kubeswarmv1alpha1 "github.com/kubeswarm/kubeswarm/api/v1alpha1"
)

// TestValidateStepOutput_RejectPatternMatches covers TEST-TRACKER.md item:
//
//	[x] Validation: reject patterns fail the step
//
// Behaviour under test: when a step's output matches one of the configured
// rejectPatterns (regex), validation fails with an OutputRejected reason.
func TestValidateStepOutput_RejectPatternMatches(t *testing.T) {
	v := &kubeswarmv1alpha1.StepValidation{
		RejectPatterns: []string{`(?i)password\s*[:=]`, `(?i)api[_-]?key\s*[:=]`},
	}

	output := "The config has password: hunter2 in it"
	passed, reason := ValidateStepOutput(output, v)

	if passed {
		t.Fatal("expected rejection when output matches reject pattern")
	}
	if !strings.Contains(reason, "OutputRejected") {
		t.Errorf("reason = %q, expected to contain 'OutputRejected'", reason)
	}
}

// TestValidateStepOutput_RejectPatternNoMatch verifies that output not matching
// any reject pattern passes validation.
func TestValidateStepOutput_RejectPatternNoMatch(t *testing.T) {
	v := &kubeswarmv1alpha1.StepValidation{
		RejectPatterns: []string{`(?i)password\s*[:=]`},
	}

	output := "The analysis is complete. No sensitive data found."
	passed, reason := ValidateStepOutput(output, v)

	if !passed {
		t.Errorf("expected pass when no reject pattern matches, got reason: %q", reason)
	}
}

// TestValidateStepOutput_RejectPatternEvaluatedBeforeContains verifies that
// reject patterns are checked before quality checks (contains). A rejected
// output should fail even if it would pass the contains check.
func TestValidateStepOutput_RejectPatternEvaluatedBeforeContains(t *testing.T) {
	v := &kubeswarmv1alpha1.StepValidation{
		RejectPatterns: []string{`(?i)secret`},
		Contains:       "analysis",
	}

	// Output matches both the contains check AND a reject pattern.
	output := "This analysis reveals the secret API key."
	passed, reason := ValidateStepOutput(output, v)

	if passed {
		t.Fatal("reject pattern should take priority over contains check")
	}
	if !strings.Contains(reason, "OutputRejected") {
		t.Errorf("reason = %q, expected OutputRejected (not a contains failure)", reason)
	}
}

// TestValidateStepOutput_RejectPatternInvalidRegex verifies that an invalid
// reject pattern causes validation failure with a descriptive error.
func TestValidateStepOutput_RejectPatternInvalidRegex(t *testing.T) {
	v := &kubeswarmv1alpha1.StepValidation{
		RejectPatterns: []string{`[invalid`},
	}

	passed, reason := ValidateStepOutput("any output", v)
	if passed {
		t.Fatal("invalid reject pattern regex should fail validation")
	}
	if !strings.Contains(reason, "invalid rejectPattern") {
		t.Errorf("reason = %q, expected to mention invalid pattern", reason)
	}
}

// TestValidateStepOutput_EmptyRejectPatterns verifies that empty reject
// patterns list passes through.
func TestValidateStepOutput_EmptyRejectPatterns(t *testing.T) {
	v := &kubeswarmv1alpha1.StepValidation{
		RejectPatterns: []string{},
	}

	passed, _ := ValidateStepOutput("any output", v)
	if !passed {
		t.Error("empty reject patterns should pass")
	}
}
