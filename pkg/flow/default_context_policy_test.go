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

// TestDefaultContextPolicy_AppliedToNonAdjacent covers TEST-TRACKER.md item:
//
//	[x] DefaultContextPolicy: applied to non-adjacent step refs
//
// Behaviour under test: when a pipeline-level defaultContextPolicy is set with
// strategy=none, non-adjacent step outputs are replaced with empty strings,
// while adjacent (direct predecessor) outputs are left unchanged.
func TestDefaultContextPolicy_AppliedToNonAdjacent(t *testing.T) {
	// Pipeline: step-a -> step-b -> step-c
	// step-c depends on step-b (adjacent), step-a is non-adjacent.
	pipeline := []kubeswarmv1alpha1.SwarmTeamPipelineStep{
		{Role: "step-a"},
		{Role: "step-b"},
		{Role: "step-c", DependsOn: []string{"step-b"}},
	}

	templateData := map[string]any{
		"steps": map[string]any{
			"step-a": map[string]any{
				"output": "output from step-a (non-adjacent to step-c)",
			},
			"step-b": map[string]any{
				"output": "output from step-b (adjacent to step-c)",
			},
		},
	}

	statusByName := map[string]*kubeswarmv1alpha1.PipelineStepStatus{
		"step-a": {Output: "output from step-a (non-adjacent to step-c)"},
		"step-b": {Output: "output from step-b (adjacent to step-c)"},
	}

	defaultPolicy := &kubeswarmv1alpha1.StepContextPolicy{
		Strategy: "none",
	}

	result := flow.ApplyDefaultContextPolicy(flow.ContextPolicyParams{
		TemplateData:         templateData,
		ConsumerRole:         "step-c",
		Pipeline:             pipeline,
		DefaultPolicy:        defaultPolicy,
		StatusByName:         statusByName,
		PipelineDefaultModel: "default-model",
	})

	steps := result["steps"].(map[string]any)

	// step-b is adjacent to step-c - output should be untouched.
	stepB := steps["step-b"].(map[string]any)
	if stepB["output"] != "output from step-b (adjacent to step-c)" {
		t.Errorf("adjacent step-b output was modified: %q", stepB["output"])
	}

	// step-a is non-adjacent - with strategy=none, output should be empty
	// (wrapped in the swarm:step-output envelope).
	stepA := steps["step-a"].(map[string]any)
	stepAOutput, _ := stepA["output"].(string)
	// strategy=none returns "" from ApplyContextPolicy, which gets wrapped.
	// The wrap produces <swarm:step-output name="step-a">...\n\n... but
	// the content inside should be empty.
	if strings.Contains(stepAOutput, "non-adjacent") {
		t.Errorf("non-adjacent step-a output should not contain original content, got: %q", stepAOutput)
	}
}

// TestDefaultContextPolicy_NilPolicyPassesThrough verifies that when no
// defaultContextPolicy is set, all step outputs pass through unchanged.
func TestDefaultContextPolicy_NilPolicyPassesThrough(t *testing.T) {
	pipeline := []kubeswarmv1alpha1.SwarmTeamPipelineStep{
		{Role: "step-a"},
		{Role: "step-b"},
	}

	original := map[string]any{
		"steps": map[string]any{
			"step-a": map[string]any{
				"output": "original output",
			},
		},
	}

	result := flow.ApplyDefaultContextPolicy(flow.ContextPolicyParams{
		TemplateData: original,
		ConsumerRole: "step-b",
		Pipeline:     pipeline,
	})

	// Should return the same data.
	steps := result["steps"].(map[string]any)
	stepA := steps["step-a"].(map[string]any)
	if stepA["output"] != "original output" {
		t.Errorf("nil policy should not modify output, got: %q", stepA["output"])
	}
}

// TestDefaultContextPolicy_CompressCallsFn verifies that strategy=compress
// on the default policy invokes the compress function for non-adjacent steps.
func TestDefaultContextPolicy_CompressCallsFn(t *testing.T) {
	pipeline := []kubeswarmv1alpha1.SwarmTeamPipelineStep{
		{Role: "step-a"},
		{Role: "step-b"},
		{Role: "step-c", DependsOn: []string{"step-b"}},
	}

	templateData := map[string]any{
		"steps": map[string]any{
			"step-a": map[string]any{
				"output": "long output from step-a",
			},
			"step-b": map[string]any{
				"output": "output from step-b",
			},
		},
	}

	statusByName := map[string]*kubeswarmv1alpha1.PipelineStepStatus{
		"step-a": {Output: "long output from step-a"},
		"step-b": {Output: "output from step-b"},
	}

	defaultPolicy := &kubeswarmv1alpha1.StepContextPolicy{
		Strategy: "compress",
		Compress: &kubeswarmv1alpha1.ContextCompressConfig{
			TargetTokens: 50,
		},
	}

	compressCalled := false
	compressFn := func(model, prompt string) (string, error) {
		compressCalled = true
		return "compressed version of step-a", nil
	}

	result := flow.ApplyDefaultContextPolicy(flow.ContextPolicyParams{
		TemplateData:         templateData,
		ConsumerRole:         "step-c",
		Pipeline:             pipeline,
		DefaultPolicy:        defaultPolicy,
		StatusByName:         statusByName,
		CompressFn:           compressFn,
		PipelineDefaultModel: "default-model",
	})

	if !compressCalled {
		t.Fatal("compressFn was not called for non-adjacent step")
	}

	steps := result["steps"].(map[string]any)
	stepA := steps["step-a"].(map[string]any)
	stepAOutput, _ := stepA["output"].(string)
	if !strings.Contains(stepAOutput, "compressed version") {
		t.Errorf("non-adjacent step-a should have compressed output, got: %q", stepAOutput)
	}
}
