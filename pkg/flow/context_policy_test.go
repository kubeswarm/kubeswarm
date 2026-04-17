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

package flow_test

import (
	"strings"
	"testing"

	kubeswarmv1alpha1 "github.com/kubeswarm/kubeswarm/api/v1alpha1"
	"github.com/kubeswarm/kubeswarm/pkg/flow"
)

// TestOutputStrategy_Full covers TEST-TRACKER.md item:
//
//	[x] Output strategy: full (verbatim)
//
// Behaviour under test: when strategy=full (or nil policy), the raw step output
// is returned verbatim with no transformation.
func TestOutputStrategy_Full(t *testing.T) {
	raw := "The analysis found 3 critical vulnerabilities."

	// Explicit strategy=full.
	result := flow.ApplyContextPolicy(raw, &kubeswarmv1alpha1.StepContextPolicy{
		Strategy: "full",
	}, "default-model")

	if result.Output != raw {
		t.Errorf("full strategy Output = %q, want verbatim %q", result.Output, raw)
	}
	if result.RawOutput != "" {
		t.Errorf("full strategy RawOutput = %q, want empty (no transformation)", result.RawOutput)
	}
	if result.NeedsCompression {
		t.Error("full strategy should not need compression")
	}

	// Nil policy should behave like full.
	nilResult := flow.ApplyContextPolicy(raw, nil, "default-model")
	if nilResult.Output != raw {
		t.Errorf("nil policy Output = %q, want verbatim %q", nilResult.Output, raw)
	}
}

// TestOutputStrategy_Compress covers TEST-TRACKER.md item:
//
//	[x] Output strategy: compress (LLM summary at target tokens)
//
// Behaviour under test: strategy=compress returns NeedsCompression=true with a
// compression prompt ready for dispatch. The output is not transformed inline -
// the caller must dispatch the LLM call separately.
func TestOutputStrategy_Compress(t *testing.T) {
	raw := "Long analysis output with many details about the system architecture..."

	result := flow.ApplyContextPolicy(raw, &kubeswarmv1alpha1.StepContextPolicy{
		Strategy: "compress",
		Compress: &kubeswarmv1alpha1.ContextCompressConfig{
			TargetTokens: 200,
			Model:        "claude-haiku-4-5-20251001",
		},
	}, "default-model")

	if !result.NeedsCompression {
		t.Fatal("compress strategy NeedsCompression = false, want true")
	}
	if result.CompressionModel != "claude-haiku-4-5-20251001" {
		t.Errorf("CompressionModel = %q, want %q", result.CompressionModel, "claude-haiku-4-5-20251001")
	}
	if result.CompressionPrompt == "" {
		t.Error("CompressionPrompt is empty, expected a ready-to-send prompt")
	}
	if !strings.Contains(result.CompressionPrompt, raw) {
		t.Error("CompressionPrompt does not contain the raw output")
	}
	if result.RawOutput != raw {
		t.Errorf("RawOutput = %q, want original %q", result.RawOutput, raw)
	}
}

// TestOutputStrategy_Compress_DefaultModel verifies that when compress.model
// is unset, the pipeline default model is used.
func TestOutputStrategy_Compress_DefaultModel(t *testing.T) {
	result := flow.ApplyContextPolicy("some output", &kubeswarmv1alpha1.StepContextPolicy{
		Strategy: "compress",
		Compress: &kubeswarmv1alpha1.ContextCompressConfig{
			TargetTokens: 100,
			// Model not set.
		},
	}, "pipeline-default-model")

	if result.CompressionModel != "pipeline-default-model" {
		t.Errorf("CompressionModel = %q, want pipeline default %q", result.CompressionModel, "pipeline-default-model")
	}
}

// TestOutputStrategy_ExtractJSONPath covers TEST-TRACKER.md item:
//
//	[x] Output strategy: extract (JSONPath)
//
// Behaviour under test: strategy=extract with a dotted key path extracts the
// matching value from JSON output.
func TestOutputStrategy_ExtractJSONPath(t *testing.T) {
	raw := `{"result": {"status": "success", "count": 42}}`

	result := flow.ApplyContextPolicy(raw, &kubeswarmv1alpha1.StepContextPolicy{
		Strategy:    "extract",
		ExtractPath: "result.status",
	}, "default-model")

	if result.Output != "success" {
		t.Errorf("extract JSONPath Output = %q, want %q", result.Output, "success")
	}
	if result.RawOutput != raw {
		t.Errorf("RawOutput = %q, want original JSON", result.RawOutput)
	}
	if result.NeedsCompression {
		t.Error("extract should not need compression")
	}
}

// TestOutputStrategy_ExtractRegexp covers TEST-TRACKER.md item:
//
//	[x] Output strategy: extract (regexp)
//
// Behaviour under test: strategy=extract with a regexp pattern extracts the
// first capture group from non-JSON output.
func TestOutputStrategy_ExtractRegexp(t *testing.T) {
	raw := "The final score is 95 out of 100 points."

	result := flow.ApplyContextPolicy(raw, &kubeswarmv1alpha1.StepContextPolicy{
		Strategy:    "extract",
		ExtractPath: `score is (\d+)`,
	}, "default-model")

	if result.Output != "95" {
		t.Errorf("extract regexp Output = %q, want %q", result.Output, "95")
	}
	if result.RawOutput != raw {
		t.Errorf("RawOutput = %q, want original", result.RawOutput)
	}
}

// TestOutputStrategy_ExtractNoPath verifies that extract with empty path
// falls back to full output.
func TestOutputStrategy_ExtractNoPath(t *testing.T) {
	raw := `{"key": "value"}`

	result := flow.ApplyContextPolicy(raw, &kubeswarmv1alpha1.StepContextPolicy{
		Strategy:    "extract",
		ExtractPath: "", // no path
	}, "default-model")

	if result.Output != raw {
		t.Errorf("extract with empty path Output = %q, want verbatim %q", result.Output, raw)
	}
}

// TestOutputStrategy_None covers TEST-TRACKER.md item:
//
//	[x] Output strategy: none (empty injection)
//
// Behaviour under test: strategy=none discards the output for downstream
// injection (Output="") but preserves the raw output in RawOutput for
// status/audit purposes.
func TestOutputStrategy_None(t *testing.T) {
	raw := "This output should be discarded for downstream steps."

	result := flow.ApplyContextPolicy(raw, &kubeswarmv1alpha1.StepContextPolicy{
		Strategy: "none",
	}, "default-model")

	if result.Output != "" {
		t.Errorf("none strategy Output = %q, want empty", result.Output)
	}
	if result.RawOutput != raw {
		t.Errorf("none strategy RawOutput = %q, want original %q", result.RawOutput, raw)
	}
	if result.NeedsCompression {
		t.Error("none strategy should not need compression")
	}
}

// TestOutputStrategy_UnknownFallsBackToFull verifies that an unknown strategy
// string falls back to full (verbatim) behavior.
func TestOutputStrategy_UnknownFallsBackToFull(t *testing.T) {
	raw := "some output"

	result := flow.ApplyContextPolicy(raw, &kubeswarmv1alpha1.StepContextPolicy{
		Strategy: "unknown-strategy",
	}, "default-model")

	if result.Output != raw {
		t.Errorf("unknown strategy Output = %q, want verbatim %q", result.Output, raw)
	}
}
