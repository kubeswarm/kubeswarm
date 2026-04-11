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
	"strings"
	"testing"
)

func TestTruncation_InputExceedsMaxDetailBytes(t *testing.T) {
	t.Parallel()

	maxBytes := 100
	tr := NewTruncator(maxBytes)

	detail := map[string]any{
		"input":  strings.Repeat("x", 200),
		"output": "short",
	}

	result, truncated := tr.Truncate(detail)

	input, ok := result["input"].(string)
	if !ok {
		t.Fatal("expected input to be a string after truncation")
	}

	if len(input) > maxBytes {
		t.Errorf("input length = %d, want <= %d", len(input), maxBytes)
	}

	if !strings.HasSuffix(input, "[truncated]") {
		t.Errorf("truncated input should end with [truncated], got %q", input[len(input)-20:])
	}

	if !truncated {
		t.Error("expected truncated = true")
	}
}

func TestTruncation_OutputExceedsMaxDetailBytes(t *testing.T) {
	t.Parallel()

	maxBytes := 100
	tr := NewTruncator(maxBytes)

	detail := map[string]any{
		"input":  "short",
		"output": strings.Repeat("y", 200),
	}

	result, truncated := tr.Truncate(detail)

	output, ok := result["output"].(string)
	if !ok {
		t.Fatal("expected output to be a string after truncation")
	}

	if len(output) > maxBytes {
		t.Errorf("output length = %d, want <= %d", len(output), maxBytes)
	}

	if !strings.HasSuffix(output, "[truncated]") {
		t.Errorf("truncated output should end with [truncated]")
	}

	if !truncated {
		t.Error("expected truncated = true")
	}
}

func TestTruncation_DetailTruncatedFieldSetWhenTruncated(t *testing.T) {
	t.Parallel()

	tr := NewTruncator(50)

	detail := map[string]any{
		"input":  strings.Repeat("z", 200),
		"output": "ok",
	}

	result, truncated := tr.Truncate(detail)

	if !truncated {
		t.Error("expected truncated = true")
	}

	// The caller should set detail.truncated = true based on the return value
	_ = result
}

func TestTruncation_ZeroMeansUnlimited(t *testing.T) {
	t.Parallel()

	tr := NewTruncator(0)

	longString := strings.Repeat("a", 100000)
	detail := map[string]any{
		"input":  longString,
		"output": longString,
	}

	result, truncated := tr.Truncate(detail)

	if truncated {
		t.Error("expected truncated = false when maxDetailBytes = 0 (unlimited)")
	}

	input, ok := result["input"].(string)
	if !ok {
		t.Fatal("expected input to remain a string")
	}
	if len(input) != 100000 {
		t.Errorf("input length = %d, want 100000 (no truncation)", len(input))
	}
}

func TestTruncation_DefaultMaxDetailBytes(t *testing.T) {
	t.Parallel()

	if DefaultMaxDetailBytes != 8192 {
		t.Errorf("DefaultMaxDetailBytes = %d, want 8192", DefaultMaxDetailBytes)
	}
}

func TestTruncation_NoTruncationWhenUnderLimit(t *testing.T) {
	t.Parallel()

	tr := NewTruncator(1000)

	detail := map[string]any{
		"input":  "short input",
		"output": "short output",
	}

	result, truncated := tr.Truncate(detail)

	if truncated {
		t.Error("expected truncated = false when content is under limit")
	}

	if result["input"] != "short input" {
		t.Errorf("input = %v, want %q", result["input"], "short input")
	}
	if result["output"] != "short output" {
		t.Errorf("output = %v, want %q", result["output"], "short output")
	}
}

func TestTruncation_BothInputAndOutputTruncated(t *testing.T) {
	t.Parallel()

	tr := NewTruncator(50)

	detail := map[string]any{
		"input":  strings.Repeat("i", 200),
		"output": strings.Repeat("o", 200),
	}

	result, truncated := tr.Truncate(detail)

	if !truncated {
		t.Error("expected truncated = true when both fields exceed limit")
	}

	input := result["input"].(string)
	output := result["output"].(string)

	if !strings.HasSuffix(input, "[truncated]") {
		t.Error("input should end with [truncated]")
	}
	if !strings.HasSuffix(output, "[truncated]") {
		t.Error("output should end with [truncated]")
	}
}

func TestTruncation_ObjectInputSerialized(t *testing.T) {
	t.Parallel()

	tr := NewTruncator(50)

	// When input is a map/object, it should be serialized to JSON before
	// checking size, then truncated if needed
	largeMap := make(map[string]any)
	for range 100 {
		largeMap[strings.Repeat("k", 10)] = strings.Repeat("v", 100)
	}

	detail := map[string]any{
		"input":  largeMap,
		"output": "ok",
	}

	result, truncated := tr.Truncate(detail)

	if !truncated {
		t.Error("expected truncation of large object input")
	}

	// After truncation, input should be a string with the truncation marker
	input, ok := result["input"].(string)
	if !ok {
		t.Fatal("expected truncated object input to be converted to string")
	}
	if !strings.HasSuffix(input, "[truncated]") {
		t.Error("truncated object input should end with [truncated]")
	}
}
