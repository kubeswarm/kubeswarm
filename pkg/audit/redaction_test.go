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
	"testing"
)

func TestRedaction_SingleLevelGlobMatchesDirectChild(t *testing.T) {
	t.Parallel()

	r := NewRedactor([]string{"detail.input.*.apiKey"})

	detail := map[string]any{
		"input": map[string]any{
			"auth": map[string]any{
				"apiKey": "secret-key-123",
				"host":   "api.example.com",
			},
		},
	}

	result := r.Redact(detail)

	input, ok := result["input"].(map[string]any)
	if !ok {
		t.Fatal("expected input to be a map")
	}
	auth, ok := input["auth"].(map[string]any)
	if !ok {
		t.Fatal("expected auth to be a map")
	}
	if auth["apiKey"] != RedactedValue {
		t.Errorf("apiKey = %v, want [REDACTED]", auth["apiKey"])
	}
	if auth["host"] != "api.example.com" {
		t.Errorf("host = %v, want api.example.com", auth["host"])
	}
}

func TestRedaction_SingleLevelGlobDoesNotMatchTwoLevelsDeep(t *testing.T) {
	t.Parallel()

	r := NewRedactor([]string{"detail.input.*.apiKey"})

	detail := map[string]any{
		"input": map[string]any{
			"nested": map[string]any{
				"auth": map[string]any{
					"apiKey": "should-not-be-redacted",
				},
			},
		},
	}

	result := r.Redact(detail)

	input := result["input"].(map[string]any)
	nested := input["nested"].(map[string]any)
	auth := nested["auth"].(map[string]any)
	if auth["apiKey"] == RedactedValue {
		t.Error("single * should not match two levels deep")
	}
}

func TestRedaction_ReplacedValueIsREDACTED(t *testing.T) {
	t.Parallel()

	r := NewRedactor([]string{"detail.input.*.password"})

	detail := map[string]any{
		"input": map[string]any{
			"db": map[string]any{
				"password": "hunter2",
			},
		},
	}

	result := r.Redact(detail)

	input := result["input"].(map[string]any)
	db := input["db"].(map[string]any)
	if db["password"] != RedactedValue {
		t.Errorf("password = %v, want [REDACTED]", db["password"])
	}
}

func TestRedaction_MultiplePatternsAllApply(t *testing.T) {
	t.Parallel()

	r := NewRedactor([]string{
		"detail.input.*.apiKey",
		"detail.input.*.password",
		"detail.output.*.secret",
	})

	detail := map[string]any{
		"input": map[string]any{
			"auth": map[string]any{
				"apiKey":   "key-1",
				"password": "pass-1",
			},
		},
		"output": map[string]any{
			"result": map[string]any{
				"secret": "classified",
				"data":   "public",
			},
		},
	}

	result := r.Redact(detail)

	input := result["input"].(map[string]any)
	auth := input["auth"].(map[string]any)
	if auth["apiKey"] != RedactedValue {
		t.Errorf("apiKey = %v, want [REDACTED]", auth["apiKey"])
	}
	if auth["password"] != RedactedValue {
		t.Errorf("password = %v, want [REDACTED]", auth["password"])
	}

	output := result["output"].(map[string]any)
	resultField := output["result"].(map[string]any)
	if resultField["secret"] != RedactedValue {
		t.Errorf("secret = %v, want [REDACTED]", resultField["secret"])
	}
	if resultField["data"] != "public" {
		t.Errorf("data = %v, want public", resultField["data"])
	}
}

func TestRedaction_EmptyPatternList(t *testing.T) {
	t.Parallel()

	r := NewRedactor(nil)

	detail := map[string]any{
		"input": map[string]any{
			"auth": map[string]any{
				"apiKey": "should-remain",
			},
		},
	}

	result := r.Redact(detail)

	input := result["input"].(map[string]any)
	auth := input["auth"].(map[string]any)
	if auth["apiKey"] != "should-remain" {
		t.Errorf("apiKey = %v, want should-remain (no redaction with empty patterns)", auth["apiKey"])
	}
}

func TestRedaction_PatternMatchesNothing(t *testing.T) {
	t.Parallel()

	r := NewRedactor([]string{"detail.input.*.nonExistentField"})

	detail := map[string]any{
		"input": map[string]any{
			"auth": map[string]any{
				"apiKey": "still-here",
			},
		},
	}

	// Should not error or panic
	result := r.Redact(detail)

	input := result["input"].(map[string]any)
	auth := input["auth"].(map[string]any)
	if auth["apiKey"] != "still-here" {
		t.Errorf("apiKey = %v, want still-here", auth["apiKey"])
	}
}

func TestRedaction_DoubleWildcardMatchesTwoLevels(t *testing.T) {
	t.Parallel()

	r := NewRedactor([]string{"detail.*.*.token"})

	detail := map[string]any{
		"input": map[string]any{
			"auth": map[string]any{
				"token": "secret-token",
			},
		},
		"output": map[string]any{
			"config": map[string]any{
				"token": "another-secret",
			},
		},
	}

	result := r.Redact(detail)

	input := result["input"].(map[string]any)
	auth := input["auth"].(map[string]any)
	if auth["token"] != RedactedValue {
		t.Errorf("input.auth.token = %v, want [REDACTED]", auth["token"])
	}

	output := result["output"].(map[string]any)
	config := output["config"].(map[string]any)
	if config["token"] != RedactedValue {
		t.Errorf("output.config.token = %v, want [REDACTED]", config["token"])
	}
}

func TestRedaction_MergePatternsAcrossLevels(t *testing.T) {
	t.Parallel()

	clusterPatterns := []string{"detail.input.*.apiKey"}
	namespacePatterns := []string{"detail.input.*.ssn"}
	agentPatterns := []string{"detail.output.*.secret"}

	merged := MergeRedactionPatterns(clusterPatterns, namespacePatterns, agentPatterns)
	r := NewRedactor(merged)

	detail := map[string]any{
		"input": map[string]any{
			"auth": map[string]any{
				"apiKey": "key-1",
			},
			"user": map[string]any{
				"ssn": "123-45-6789",
			},
		},
		"output": map[string]any{
			"result": map[string]any{
				"secret": "classified",
			},
		},
	}

	result := r.Redact(detail)

	input := result["input"].(map[string]any)
	auth := input["auth"].(map[string]any)
	if auth["apiKey"] != RedactedValue {
		t.Errorf("apiKey = %v, want [REDACTED]", auth["apiKey"])
	}
	user := input["user"].(map[string]any)
	if user["ssn"] != RedactedValue {
		t.Errorf("ssn = %v, want [REDACTED]", user["ssn"])
	}
	output := result["output"].(map[string]any)
	resultField := output["result"].(map[string]any)
	if resultField["secret"] != RedactedValue {
		t.Errorf("secret = %v, want [REDACTED]", resultField["secret"])
	}
}

func TestRedaction_RedactedBeforeSerialization(t *testing.T) {
	t.Parallel()

	// Verify redacted values never appear in the serialized output
	r := NewRedactor([]string{"detail.input.*.apiKey"})

	detail := map[string]any{
		"input": map[string]any{
			"config": map[string]any{
				"apiKey": "super-secret-key",
			},
		},
	}

	result := r.Redact(detail)

	data, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}

	serialized := string(data)
	if contains(serialized, "super-secret-key") {
		t.Error("redacted value appeared in serialized output")
	}
	if !contains(serialized, RedactedValue) {
		t.Error("expected [REDACTED] in serialized output")
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && searchString(s, substr)
}

func searchString(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
