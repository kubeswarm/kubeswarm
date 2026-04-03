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
	"strconv"
	"strings"

	"github.com/tidwall/gjson"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kubeswarmv1alpha1 "github.com/kubeswarm/kubeswarm/api/v1alpha1"
)

// SetTeamCondition updates the "Ready" condition on an SwarmTeam status.
func SetTeamCondition(f *kubeswarmv1alpha1.SwarmTeam, status metav1.ConditionStatus, reason, message string) {
	apimeta.SetStatusCondition(&f.Status.Conditions, metav1.Condition{
		Type:               "Ready",
		Status:             status,
		ObservedGeneration: f.Generation,
		Reason:             reason,
		Message:            message,
	})
}

// SetRunCondition updates the "Ready" condition on an SwarmRun status.
func SetRunCondition(f *kubeswarmv1alpha1.SwarmRun, status metav1.ConditionStatus, reason, message string) {
	apimeta.SetStatusCondition(&f.Status.Conditions, metav1.Condition{
		Type:               "Ready",
		Status:             status,
		ObservedGeneration: f.Generation,
		Reason:             reason,
		Message:            message,
	})
}

// InitializeRunSteps sets up step statuses for an SwarmRun and marks it Running.
// It is a no-op if the run is already initialised (phase non-empty).
func InitializeRunSteps(f *kubeswarmv1alpha1.SwarmRun) {
	if f.Status.Phase != "" {
		return
	}
	now := metav1.Now()
	f.Status.Phase = kubeswarmv1alpha1.SwarmRunPhaseRunning
	f.Status.StartTime = &now
	f.Status.Steps = make([]kubeswarmv1alpha1.SwarmFlowStepStatus, len(f.Spec.Pipeline))
	for i, step := range f.Spec.Pipeline {
		f.Status.Steps[i] = kubeswarmv1alpha1.SwarmFlowStepStatus{
			Name:  step.Role,
			Phase: kubeswarmv1alpha1.SwarmFlowStepPhasePending,
		}
	}
	SetRunCondition(f, metav1.ConditionTrue, "Validated", "Pipeline DAG is valid; execution started")
}

// InitializeRouteStep sets up the single synthetic "route" step for a routed-mode SwarmRun.
// It is a no-op if the run is already initialised (phase non-empty).
func InitializeRouteStep(f *kubeswarmv1alpha1.SwarmRun) {
	if f.Status.Phase != "" {
		return
	}
	now := metav1.Now()
	f.Status.Phase = kubeswarmv1alpha1.SwarmRunPhaseRunning
	f.Status.StartTime = &now
	f.Status.Steps = []kubeswarmv1alpha1.SwarmFlowStepStatus{
		{Name: "route", Phase: kubeswarmv1alpha1.SwarmFlowStepPhasePending},
	}
	SetRunCondition(f, metav1.ConditionTrue, "Validated", "Routed mode; awaiting dispatch to registry")
}

// ParseRunOutputJSON parses completed SwarmRun pipeline step outputs as JSON when OutputSchema is set.
func ParseRunOutputJSON(
	f *kubeswarmv1alpha1.SwarmRun,
	statusByName map[string]*kubeswarmv1alpha1.SwarmFlowStepStatus,
) {
	schemaByName := make(map[string]string, len(f.Spec.Pipeline))
	for _, step := range f.Spec.Pipeline {
		if step.OutputSchema != "" {
			schemaByName[step.Role] = step.OutputSchema
		}
	}
	for name, st := range statusByName {
		if _, hasSchema := schemaByName[name]; !hasSchema {
			continue
		}
		if st.Phase != kubeswarmv1alpha1.SwarmFlowStepPhaseSucceeded || st.Output == "" || st.OutputJSON != "" {
			continue
		}
		if raw := ExtractJSON(st.Output); raw != "" {
			var check any
			if json.Unmarshal([]byte(raw), &check) == nil {
				st.OutputJSON = raw
			}
		}
	}
}

// EvaluateRunLoops checks every Succeeded SwarmRun pipeline step that has a Loop spec.
func EvaluateRunLoops(
	f *kubeswarmv1alpha1.SwarmRun,
	statusByName map[string]*kubeswarmv1alpha1.SwarmFlowStepStatus,
	templateData map[string]any,
) {
	for _, step := range f.Spec.Pipeline {
		if step.Loop == nil {
			continue
		}
		st := statusByName[step.Role]
		if st == nil || st.Phase != kubeswarmv1alpha1.SwarmFlowStepPhaseSucceeded {
			continue
		}
		maxIter := step.Loop.MaxIterations
		if maxIter <= 0 {
			maxIter = 10
		}
		if st.Iterations >= maxIter {
			continue
		}
		condResult, err := ResolveTemplate(step.Loop.Condition, templateData)
		if err != nil || !IsTruthy(condResult) {
			continue
		}
		st.Iterations++
		st.Phase = kubeswarmv1alpha1.SwarmFlowStepPhasePending
		st.TaskID = ""
		st.Output = ""
		st.OutputJSON = ""
		st.StartTime = nil
		st.CompletionTime = nil
		st.Message = fmt.Sprintf("loop iteration %d/%d", st.Iterations, maxIter)
	}
}

// ExtractJSON returns the first JSON object or array found in s.
// It handles two cases:
//  1. The whole string is valid JSON — returned as-is.
//  2. JSON is wrapped in a markdown code fence (```json ... ``` or ``` ... ```) —
//     the fenced block is extracted and returned.
func ExtractJSON(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	var check any
	if json.Unmarshal([]byte(s), &check) == nil {
		return s
	}
	for line := range strings.SplitSeq(s, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "```") {
			continue
		}
		if strings.HasPrefix(line, "{") || strings.HasPrefix(line, "[") {
			start := strings.Index(s, line)
			if start < 0 {
				break
			}
			end := strings.Index(s[start:], "\n```")
			if end < 0 {
				candidate := strings.TrimSpace(s[start:])
				if json.Unmarshal([]byte(candidate), &check) == nil {
					return candidate
				}
				break
			}
			candidate := strings.TrimSpace(s[start : start+end])
			if json.Unmarshal([]byte(candidate), &check) == nil {
				return candidate
			}
			break
		}
	}
	return ""
}

// DefaultCompressionPrompt is the built-in system prompt used when
// contextPolicy.strategy=compress and no custom prompt is configured.
const DefaultCompressionPrompt = `You are a pipeline context compressor. Your only job is to distil the following step output into a compact, factually accurate summary that preserves the key findings, decisions, and data a downstream agent will need.

Rules:
- Do not add interpretation or opinion.
- Do not include tool call traces, intermediate reasoning, or retry attempts.
- Preserve specific values: numbers, names, URLs, identifiers, code snippets.
- Output plain text. No headers. No bullet lists unless the original used them.`

// ContextStrategyNone is returned by ApplyContextPolicy when strategy=none.
const ContextStrategyNone = "none"

// ApplyContextPolicyResult holds the outcome of applying a context policy.
type ApplyContextPolicyResult struct {
	// Output is the policy-processed value to store in status.Output.
	Output string
	// RawOutput is the original value; non-empty only for compress/extract strategies.
	RawOutput string
	// NeedsCompression is true when strategy=compress; the caller must call CompressFn
	// and then call ApplyCompressionResult.
	NeedsCompression bool
	// CompressionModel is the model to use for compression (resolved from policy or pipeline default).
	CompressionModel string
	// CompressionPrompt is the full user prompt to send to the compression LLM.
	CompressionPrompt string
}

// ApplyContextPolicy processes a completed step's raw output according to its
// contextPolicy spec. For strategy=compress, it returns NeedsCompression=true
// and a ready-to-send prompt; the caller dispatches the LLM call and then calls
// ApplyCompressionResult. For all other strategies the result is fully resolved.
//
// pipelineDefaultModel is used as the compression model when policy.compress.model is unset.
// If rawOutput is empty, the function is a no-op and returns rawOutput unchanged.
func ApplyContextPolicy(
	rawOutput string,
	policy *kubeswarmv1alpha1.StepContextPolicy,
	pipelineDefaultModel string,
) ApplyContextPolicyResult {
	if policy == nil || policy.Strategy == "" || policy.Strategy == "full" {
		return ApplyContextPolicyResult{Output: rawOutput}
	}

	switch policy.Strategy {
	case "none":
		return ApplyContextPolicyResult{Output: "", RawOutput: rawOutput}

	case "extract":
		if policy.Extract == nil || policy.Extract.Path == "" {
			return ApplyContextPolicyResult{Output: rawOutput}
		}
		extracted := applyExtract(rawOutput, policy.Extract.Path)
		return ApplyContextPolicyResult{Output: extracted, RawOutput: rawOutput}

	case "compress":
		if rawOutput == "" {
			return ApplyContextPolicyResult{Output: rawOutput}
		}
		model := pipelineDefaultModel
		if policy.Compress != nil && policy.Compress.Model != "" {
			model = policy.Compress.Model
		}
		prompt := buildCompressionPrompt(rawOutput, policy.Compress)
		return ApplyContextPolicyResult{
			Output:            rawOutput, // will be replaced after compression
			RawOutput:         rawOutput,
			NeedsCompression:  true,
			CompressionModel:  model,
			CompressionPrompt: prompt,
		}
	}

	// Unknown strategy — fall back to full.
	return ApplyContextPolicyResult{Output: rawOutput}
}

// ApplyCompressionResult stores the compression LLM response into the step status.
// Call this after the compression LLM call returns.
func ApplyCompressionResult(st *kubeswarmv1alpha1.SwarmFlowStepStatus, compressed string) {
	st.Output = compressed
	st.CompressionTokens = approximateTokens(compressed)
}

// applyExtract evaluates path against output. It tries JSONPath (via gjson) first
// when the output is valid JSON; otherwise it falls back to a Go regexp with the
// first capture group. On failure it returns the original output unchanged.
func applyExtract(output, path string) string {
	trimmed := strings.TrimSpace(output)

	// Try gjson (JSONPath-like) when output looks like JSON.
	if len(trimmed) > 0 && (trimmed[0] == '{' || trimmed[0] == '[') {
		if gjson.Valid(trimmed) {
			result := gjson.Get(trimmed, path)
			if result.Exists() {
				return result.String()
			}
			// JSONPath matched nothing — fall through to regexp.
		}
	}

	// Try regexp: return first capture group, or whole match if no groups.
	re, err := regexp.Compile(path)
	if err != nil {
		return output // invalid regexp — return original
	}
	matches := re.FindStringSubmatch(output)
	if len(matches) == 0 {
		return output // no match — return original
	}
	if len(matches) > 1 {
		return matches[1] // first capture group
	}
	return matches[0] // whole match
}

// buildCompressionPrompt assembles the user prompt for the compression LLM call.
func buildCompressionPrompt(rawOutput string, cfg *kubeswarmv1alpha1.ContextCompressConfig) string {
	systemHint := DefaultCompressionPrompt
	var targetLine string

	if cfg != nil {
		if cfg.Prompt != "" {
			systemHint = cfg.Prompt
		}
		if cfg.TargetTokens > 0 {
			targetLine = fmt.Sprintf("\n- Stay within approximately %d tokens.", cfg.TargetTokens)
		}
	}

	return systemHint + targetLine + "\n\nStep output to compress:\n" + rawOutput
}

// approximateTokens returns a rough token estimate (chars/4) for a string.
// Used only for the CompressionTokens field; not billing-critical.
func approximateTokens(s string) int {
	return (len(s) + 3) / 4
}

// ToInt64 coerces a Redis value (string or int64) to int64.
func ToInt64(v any) int64 {
	switch val := v.(type) {
	case int64:
		return val
	case string:
		n, _ := strconv.ParseInt(val, 10, 64)
		return n
	}
	return 0
}
