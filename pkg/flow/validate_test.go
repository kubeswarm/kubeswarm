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
	"testing"

	kubeswarmv1alpha1 "github.com/kubeswarm/kubeswarm/api/v1alpha1"
)

func TestValidateStepOutput_Nil(t *testing.T) {
	passed, reason := ValidateStepOutput("anything", nil)
	if !passed || reason != "" {
		t.Errorf("nil validate: want (true, \"\"), got (%v, %q)", passed, reason)
	}
}

func TestValidateStepOutput_ContainsPass(t *testing.T) {
	v := &kubeswarmv1alpha1.StepValidation{Contains: "hello"}
	passed, reason := ValidateStepOutput("say hello world", v)
	if !passed {
		t.Errorf("expected pass, got reason: %q", reason)
	}
}

func TestValidateStepOutput_ContainsFail(t *testing.T) {
	v := &kubeswarmv1alpha1.StepValidation{Contains: "https?://"}
	passed, reason := ValidateStepOutput("no urls here", v)
	if passed {
		t.Errorf("expected fail, got pass")
	}
	if reason == "" {
		t.Errorf("expected non-empty reason on failure")
	}
}

func TestValidateStepOutput_ContainsInvalidRegex(t *testing.T) {
	v := &kubeswarmv1alpha1.StepValidation{Contains: "[invalid"}
	passed, reason := ValidateStepOutput("anything", v)
	if passed {
		t.Errorf("invalid regex should fail validation")
	}
	if reason == "" {
		t.Errorf("expected error reason for invalid regex")
	}
}

func TestValidateStepOutput_SchemaPass(t *testing.T) {
	schema := `{"type":"object","required":["name"],"properties":{"name":{"type":"string"}}}`
	v := &kubeswarmv1alpha1.StepValidation{Schema: schema}
	passed, reason := ValidateStepOutput(`{"name":"Alice"}`, v)
	if !passed {
		t.Errorf("expected pass, got reason: %q", reason)
	}
}

func TestValidateStepOutput_SchemaMissingRequired(t *testing.T) {
	schema := `{"type":"object","required":["name","score"],"properties":{"name":{"type":"string"},"score":{"type":"number"}}}`
	v := &kubeswarmv1alpha1.StepValidation{Schema: schema}
	passed, reason := ValidateStepOutput(`{"name":"Alice"}`, v)
	if passed {
		t.Errorf("expected fail due to missing required field")
	}
	if reason == "" {
		t.Errorf("expected non-empty reason")
	}
}

func TestValidateStepOutput_SchemaWrongType(t *testing.T) {
	schema := `{"type":"object","properties":{"score":{"type":"number"}}}`
	v := &kubeswarmv1alpha1.StepValidation{Schema: schema}
	passed, reason := ValidateStepOutput(`{"score":"not-a-number"}`, v)
	if passed {
		t.Errorf("expected fail due to wrong type")
	}
	if reason == "" {
		t.Errorf("expected non-empty reason")
	}
}

func TestValidateStepOutput_SchemaNotJSON(t *testing.T) {
	schema := `{"type":"object"}`
	v := &kubeswarmv1alpha1.StepValidation{Schema: schema}
	passed, reason := ValidateStepOutput("not json at all", v)
	if passed {
		t.Errorf("expected fail for non-JSON output")
	}
	if reason == "" {
		t.Errorf("expected non-empty reason")
	}
}

func TestValidateStepOutput_MultiModeAllPass(t *testing.T) {
	schema := `{"type":"object","required":["status"]}`
	v := &kubeswarmv1alpha1.StepValidation{
		Contains: "ok",
		Schema:   schema,
	}
	passed, reason := ValidateStepOutput(`{"status":"ok"}`, v)
	if !passed {
		t.Errorf("expected all checks to pass, got reason: %q", reason)
	}
}

func TestValidateStepOutput_MultiModeContainsFails(t *testing.T) {
	schema := `{"type":"object","required":["status"]}`
	v := &kubeswarmv1alpha1.StepValidation{
		Contains: "posted",
		Schema:   schema,
	}
	// Schema would pass but Contains fails — earliest check wins.
	passed, _ := ValidateStepOutput(`{"status":"ok"}`, v)
	if passed {
		t.Errorf("expected fail because contains pattern not matched")
	}
}

func TestParseSemanticResult_Valid(t *testing.T) {
	cases := []string{"VALID", "valid", "Valid", "The answer is VALID."}
	for _, c := range cases {
		passed, reason := ParseSemanticResult(c)
		if !passed {
			t.Errorf("input %q: expected pass, got reason %q", c, reason)
		}
	}
}

func TestParseSemanticResult_Invalid(t *testing.T) {
	passed, reason := ParseSemanticResult("INVALID: output contains no URLs")
	if passed {
		t.Errorf("expected fail")
	}
	if reason != "output contains no URLs" {
		t.Errorf("expected extracted reason, got %q", reason)
	}
}

func TestParseSemanticResult_InvalidNoReason(t *testing.T) {
	passed, reason := ParseSemanticResult("INVALID")
	if passed {
		t.Errorf("expected fail")
	}
	if reason == "" {
		t.Errorf("expected fallback reason")
	}
}

func TestParseSemanticResult_UnexpectedResponse(t *testing.T) {
	passed, reason := ParseSemanticResult("I'm not sure what to say here")
	if passed {
		t.Errorf("expected fail for unexpected response")
	}
	if reason == "" {
		t.Errorf("expected non-empty reason")
	}
}

func TestSemanticValidatorPrompt(t *testing.T) {
	prompt := SemanticValidatorPrompt("Does it contain URLs?", "Here is a URL: https://example.com")
	if prompt == "" {
		t.Errorf("expected non-empty prompt")
	}
	// Must contain the output and the criteria.
	if !contains(prompt, "https://example.com") {
		t.Errorf("prompt should embed the output")
	}
	if !contains(prompt, "Does it contain URLs?") {
		t.Errorf("prompt should embed the criteria")
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(s) > 0 && containsStr(s, sub))
}

func containsStr(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
