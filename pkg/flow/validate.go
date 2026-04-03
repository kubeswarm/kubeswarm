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
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	kubeswarmv1alpha1 "github.com/kubeswarm/kubeswarm/api/v1alpha1"
)

// ValidateStepOutput runs the contains and schema checks from v against output.
// Semantic validation is intentionally excluded here — it requires an LLM call
// and is handled separately by the caller (goroutine in controller, inline in swarm run).
//
// Returns (true, "") when all configured checks pass.
// Returns (false, reason) on the first failing check.
// If v is nil or no non-semantic checks are configured, returns (true, "").
func ValidateStepOutput(output string, v *kubeswarmv1alpha1.StepValidation) (bool, string) {
	if v == nil {
		return true, ""
	}

	// RejectPatterns — security gate, evaluated first before quality checks.
	// A match immediately fails the step with reason OutputRejected.
	for _, pat := range v.RejectPatterns {
		re, err := regexp.Compile(pat)
		if err != nil {
			return false, fmt.Sprintf("invalid rejectPattern %q: %v", pat, err)
		}
		if re.MatchString(output) {
			return false, fmt.Sprintf("OutputRejected: output matched injection-defence pattern %q", pat)
		}
	}

	// Contains — cheapest quality check first.
	if v.Contains != "" {
		ok, reason := validateContains(output, v.Contains)
		if !ok {
			return false, reason
		}
	}

	// Schema — more expensive, but still no LLM call.
	if v.Schema != "" {
		ok, reason := validateJSONSchema(output, v.Schema)
		if !ok {
			return false, reason
		}
	}

	return true, ""
}

// SemanticValidatorPrompt builds the prompt sent to the LLM for semantic validation.
// validatorTmpl is the user-supplied criteria from StepValidation.Semantic.
// output is the step's raw output text.
func SemanticValidatorPrompt(validatorTmpl, output string) string {
	return fmt.Sprintf(`You are a strict output validator.

Step output:
"""
%s
"""

Validation criteria:
%s

Respond with exactly one word: VALID or INVALID.
If INVALID, add a colon and a one-sentence reason: INVALID: <reason>`, output, validatorTmpl)
}

// ParseSemanticResult interprets the LLM response from a semantic validation call.
// Returns (true, "") when the response contains VALID (case-insensitive).
// Returns (false, reason) when INVALID is found; reason is extracted after the colon.
// Returns (false, "unexpected response: <resp>") when neither keyword is found.
func ParseSemanticResult(response string) (bool, string) {
	upper := strings.ToUpper(response)
	if strings.Contains(upper, "VALID") && !strings.Contains(upper, "INVALID") {
		return true, ""
	}
	if idx := strings.Index(upper, "INVALID"); idx != -1 {
		// Try to extract the reason after "INVALID:"
		rest := response[idx+len("INVALID"):]
		rest = strings.TrimLeft(rest, ": \t")
		if rest == "" {
			rest = "validation failed"
		}
		// Take only the first line of the reason.
		if nl := strings.IndexByte(rest, '\n'); nl != -1 {
			rest = rest[:nl]
		}
		return false, rest
	}
	// Neither keyword — treat as failure with raw response snippet.
	snippet := response
	if len(snippet) > 200 {
		snippet = snippet[:200] + "..."
	}
	return false, fmt.Sprintf("unexpected validator response: %s", snippet)
}

// validateContains checks that pattern (RE2 regex) matches somewhere in output.
func validateContains(output, pattern string) (bool, string) {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return false, fmt.Sprintf("invalid contains pattern %q: %v", pattern, err)
	}
	if !re.MatchString(output) {
		return false, fmt.Sprintf("output does not match pattern %q", pattern)
	}
	return true, ""
}

// validateJSONSchema checks that output is valid JSON and satisfies schemaStr.
// Only top-level required fields and property type constraints are validated —
// this is an intentionally lightweight check using encoding/json only.
func validateJSONSchema(output, schemaStr string) (bool, string) {
	// Parse the output as JSON.
	var doc any
	if err := json.Unmarshal([]byte(output), &doc); err != nil {
		return false, fmt.Sprintf("output is not valid JSON: %v", err)
	}

	// Parse the schema.
	var schema map[string]any
	if err := json.Unmarshal([]byte(schemaStr), &schema); err != nil {
		return false, fmt.Sprintf("invalid JSON schema: %v", err)
	}

	docMap, isObj := doc.(map[string]any)

	// Check required fields.
	if req, ok := schema["required"]; ok {
		reqList, ok := req.([]any)
		if ok {
			for _, r := range reqList {
				fieldName, ok := r.(string)
				if !ok {
					continue
				}
				if !isObj {
					return false, "output is not a JSON object but schema requires fields"
				}
				if _, exists := docMap[fieldName]; !exists {
					return false, fmt.Sprintf("required field %q is missing from output", fieldName)
				}
			}
		}
	}

	// Check top-level property type constraints.
	if props, ok := schema["properties"]; ok {
		propsMap, ok := props.(map[string]any)
		if ok && isObj {
			for fieldName, propDef := range propsMap {
				propDefMap, ok := propDef.(map[string]any)
				if !ok {
					continue
				}
				expectedType, ok := propDefMap["type"].(string)
				if !ok {
					continue
				}
				val, exists := docMap[fieldName]
				if !exists {
					continue // required check already handled above
				}
				if !jsonTypeMatches(val, expectedType) {
					return false, fmt.Sprintf("field %q has wrong type: expected %s", fieldName, expectedType)
				}
			}
		}
	}

	return true, ""
}

// jsonTypeMatches returns true when the Go value v matches the JSON Schema type string.
func jsonTypeMatches(v any, typ string) bool {
	switch typ {
	case "string":
		_, ok := v.(string)
		return ok
	case "number":
		_, ok := v.(float64)
		return ok
	case "integer":
		f, ok := v.(float64)
		return ok && f == float64(int64(f))
	case "boolean":
		_, ok := v.(bool)
		return ok
	case "array":
		_, ok := v.([]any)
		return ok
	case "object":
		_, ok := v.(map[string]any)
		return ok
	case "null":
		return v == nil
	}
	return true // unknown type — don't block
}
