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

package openai

import (
	"testing"

	"github.com/openai/openai-go/shared"

	"github.com/kubeswarm/kubeswarm/pkg/agent/config"
)

// TestMapReasoningEffort_ExplicitLow covers TEST-TRACKER.md item:
//
//	[x] Reasoning: Explicit effort level (OpenAI)
//
// Behaviour under test: when ReasoningMode is Explicit and ReasoningEffort is
// "Low", the provider maps it to the OpenAI SDK's ReasoningEffortLow constant.
func TestMapReasoningEffort_ExplicitLow(t *testing.T) {
	cfg := &config.Config{
		ReasoningMode:   "Explicit",
		ReasoningEffort: "Low",
	}
	got := mapReasoningEffort(cfg)
	if got != shared.ReasoningEffortLow {
		t.Errorf("mapReasoningEffort(Explicit, Low) = %q, want %q", got, shared.ReasoningEffortLow)
	}
}

func TestMapReasoningEffort_AutoMedium(t *testing.T) {
	cfg := &config.Config{
		ReasoningMode:   "Auto",
		ReasoningEffort: "Medium",
	}
	got := mapReasoningEffort(cfg)
	if got != shared.ReasoningEffortMedium {
		t.Errorf("mapReasoningEffort(Auto, Medium) = %q, want %q", got, shared.ReasoningEffortMedium)
	}
}

func TestMapReasoningEffort_ExplicitHigh(t *testing.T) {
	cfg := &config.Config{
		ReasoningMode:   "Explicit",
		ReasoningEffort: "High",
	}
	got := mapReasoningEffort(cfg)
	if got != shared.ReasoningEffortHigh {
		t.Errorf("mapReasoningEffort(Explicit, High) = %q, want %q", got, shared.ReasoningEffortHigh)
	}
}

func TestMapReasoningEffort_DisabledIgnoresEffort(t *testing.T) {
	cfg := &config.Config{
		ReasoningMode:   "Disabled",
		ReasoningEffort: "High",
	}
	got := mapReasoningEffort(cfg)
	if got != "" {
		t.Errorf("mapReasoningEffort(Disabled, High) = %q, want empty", got)
	}
}

func TestMapReasoningEffort_EmptyModeIgnoresEffort(t *testing.T) {
	cfg := &config.Config{
		ReasoningMode:   "",
		ReasoningEffort: "High",
	}
	got := mapReasoningEffort(cfg)
	if got != "" {
		t.Errorf("mapReasoningEffort('', High) = %q, want empty", got)
	}
}

func TestMapReasoningEffort_NoEffortReturnsEmpty(t *testing.T) {
	cfg := &config.Config{
		ReasoningMode:   "Auto",
		ReasoningEffort: "",
	}
	got := mapReasoningEffort(cfg)
	if got != "" {
		t.Errorf("mapReasoningEffort(Auto, '') = %q, want empty", got)
	}
}
