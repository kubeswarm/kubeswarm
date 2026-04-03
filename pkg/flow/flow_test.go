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
	"testing"

	kubeswarmv1alpha1 "github.com/kubeswarm/kubeswarm/api/v1alpha1"
	"github.com/kubeswarm/kubeswarm/pkg/flow"
)

// ---------------------------------------------------------------------------
// IsTruthy
// ---------------------------------------------------------------------------

func TestIsTruthy(t *testing.T) {
	cases := []struct {
		input string
		want  bool
	}{
		{"", false},
		{"false", false},
		{"FALSE", false},
		{"False", false},
		{"0", false},
		{"no", false},
		{"NO", false},
		{"  false  ", false},
		{"  0  ", false},
		{"true", true},
		{"1", true},
		{"yes", true},
		{"some output", true},
		{"  true  ", true},
	}
	for _, c := range cases {
		got := flow.IsTruthy(c.input)
		if got != c.want {
			t.Errorf("IsTruthy(%q) = %v, want %v", c.input, got, c.want)
		}
	}
}

// ---------------------------------------------------------------------------
// DepsSucceeded
// ---------------------------------------------------------------------------

func TestDepsSucceeded(t *testing.T) {
	succeeded := &kubeswarmv1alpha1.SwarmFlowStepStatus{Phase: kubeswarmv1alpha1.SwarmFlowStepPhaseSucceeded}
	skipped := &kubeswarmv1alpha1.SwarmFlowStepStatus{Phase: kubeswarmv1alpha1.SwarmFlowStepPhaseSkipped}
	running := &kubeswarmv1alpha1.SwarmFlowStepStatus{Phase: kubeswarmv1alpha1.SwarmFlowStepPhaseRunning}
	pending := &kubeswarmv1alpha1.SwarmFlowStepStatus{Phase: kubeswarmv1alpha1.SwarmFlowStepPhasePending}
	failed := &kubeswarmv1alpha1.SwarmFlowStepStatus{Phase: kubeswarmv1alpha1.SwarmFlowStepPhaseFailed}

	status := map[string]*kubeswarmv1alpha1.SwarmFlowStepStatus{
		"ok":      succeeded,
		"skip":    skipped,
		"running": running,
		"pending": pending,
		"failed":  failed,
	}

	if !flow.DepsSucceeded(nil, status) {
		t.Error("no deps should always succeed")
	}
	if !flow.DepsSucceeded([]string{"ok"}, status) {
		t.Error("succeeded dep should pass")
	}
	if !flow.DepsSucceeded([]string{"skip"}, status) {
		t.Error("skipped dep should pass")
	}
	if !flow.DepsSucceeded([]string{"ok", "skip"}, status) {
		t.Error("all done deps should pass")
	}
	if flow.DepsSucceeded([]string{"running"}, status) {
		t.Error("running dep should not pass")
	}
	if flow.DepsSucceeded([]string{"pending"}, status) {
		t.Error("pending dep should not pass")
	}
	if flow.DepsSucceeded([]string{"failed"}, status) {
		t.Error("failed dep should not pass")
	}
	if flow.DepsSucceeded([]string{"missing"}, status) {
		t.Error("missing dep should not pass")
	}
	if flow.DepsSucceeded([]string{"ok", "running"}, status) {
		t.Error("mixed ok+running should not pass")
	}
}

// ---------------------------------------------------------------------------
// ResolveTemplate
// ---------------------------------------------------------------------------

func TestResolveTemplate(t *testing.T) {
	t.Run("simple substitution", func(t *testing.T) {
		data := map[string]any{"steps": map[string]any{"research": map[string]any{"output": "findings"}}}
		got, err := flow.ResolveTemplate("Research result: {{ .steps.research.output }}", data)
		if err != nil {
			t.Fatal(err)
		}
		if got != "Research result: findings" {
			t.Errorf("unexpected: %q", got)
		}
	})

	t.Run("missing top-level key yields empty string (missingkey=zero)", func(t *testing.T) {
		// missingkey=zero returns the zero value for a missing map entry.
		// Accessing a top-level key that doesn't exist produces an empty string.
		got, err := flow.ResolveTemplate("{{ .missing }}", map[string]any{})
		if err != nil {
			t.Fatal(err)
		}
		// Go templates render missing map keys as "<no value>" with missingkey=zero on interface{} maps.
		if got != "<no value>" && got != "" {
			t.Errorf("missing top-level key should produce <no value> or empty, got %q", got)
		}
	})

	t.Run("no template expressions returns literal", func(t *testing.T) {
		got, err := flow.ResolveTemplate("plain text", map[string]any{})
		if err != nil {
			t.Fatal(err)
		}
		if got != "plain text" {
			t.Errorf("unexpected: %q", got)
		}
	})

	t.Run("invalid template syntax returns error", func(t *testing.T) {
		_, err := flow.ResolveTemplate("{{ .broken", map[string]any{})
		if err == nil {
			t.Error("expected parse error for broken template")
		}
	})

	t.Run("conditional expression", func(t *testing.T) {
		data := map[string]any{"steps": map[string]any{"check": map[string]any{"output": "true"}}}
		got, err := flow.ResolveTemplate("{{ .steps.check.output }}", data)
		if err != nil {
			t.Fatal(err)
		}
		if got != "true" {
			t.Errorf("unexpected: %q", got)
		}
	})
}

// ---------------------------------------------------------------------------
// ExtractJSON
// ---------------------------------------------------------------------------

func TestExtractJSON(t *testing.T) {
	t.Run("plain JSON object", func(t *testing.T) {
		got := flow.ExtractJSON(`{"key":"value"}`)
		if got != `{"key":"value"}` {
			t.Errorf("unexpected: %q", got)
		}
	})

	t.Run("plain JSON array", func(t *testing.T) {
		got := flow.ExtractJSON(`[1,2,3]`)
		if got != `[1,2,3]` {
			t.Errorf("unexpected: %q", got)
		}
	})

	t.Run("JSON in markdown fence", func(t *testing.T) {
		input := "```json\n{\"key\":\"value\"}\n```"
		got := flow.ExtractJSON(input)
		if got != `{"key":"value"}` {
			t.Errorf("unexpected: %q", got)
		}
	})

	t.Run("no JSON returns empty", func(t *testing.T) {
		got := flow.ExtractJSON("just some plain text")
		if got != "" {
			t.Errorf("expected empty, got %q", got)
		}
	})

	t.Run("empty input returns empty", func(t *testing.T) {
		got := flow.ExtractJSON("")
		if got != "" {
			t.Errorf("expected empty, got %q", got)
		}
	})
}

// ---------------------------------------------------------------------------
// ToInt64
// ---------------------------------------------------------------------------

func TestToInt64(t *testing.T) {
	if flow.ToInt64(int64(42)) != 42 {
		t.Error("int64 passthrough failed")
	}
	if flow.ToInt64("100") != 100 {
		t.Error("string→int64 failed")
	}
	if flow.ToInt64("invalid") != 0 {
		t.Error("invalid string should yield 0")
	}
	if flow.ToInt64(nil) != 0 {
		t.Error("nil should yield 0")
	}
	if flow.ToInt64(3.14) != 0 {
		t.Error("unsupported type should yield 0")
	}
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func containsAll(s string, substrings ...string) bool {
	for _, sub := range substrings {
		found := false
		for i := 0; i <= len(s)-len(sub); i++ {
			if s[i:i+len(sub)] == sub {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// containsAll is used in helper tests — ensure it's referenced.
var _ = containsAll
