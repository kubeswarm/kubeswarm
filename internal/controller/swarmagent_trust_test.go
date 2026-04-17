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

package controller

import (
	"testing"

	kubeswarmv1alpha1 "github.com/kubeswarm/kubeswarm/api/v1alpha1"
)

// TestEffectiveTrust_ExplicitWins covers TEST-TRACKER.md item:
//
//	[x] Guardrail: tools.trust.default override applied
//
// Behaviour under test: when a tool or connection declares an explicit trust
// level, effectiveTrust returns it regardless of the agent's default.
func TestEffectiveTrust_ExplicitWins(t *testing.T) {
	agent := &kubeswarmv1alpha1.SwarmAgent{
		Spec: kubeswarmv1alpha1.SwarmAgentSpec{
			Guardrails: &kubeswarmv1alpha1.AgentGuardrails{
				Tools: &kubeswarmv1alpha1.ToolPermissions{
					Trust: &kubeswarmv1alpha1.ToolTrustPolicy{
						Default: kubeswarmv1alpha1.ToolTrustInternal,
					},
				},
			},
		},
	}

	got := effectiveTrust(kubeswarmv1alpha1.ToolTrustSandbox, agent)
	if got != kubeswarmv1alpha1.ToolTrustSandbox {
		t.Errorf("effectiveTrust(sandbox, default=internal) = %q, want sandbox", got)
	}
}

// TestEffectiveTrust_FallsBackToDefault verifies that when no explicit trust
// is set, the agent's guardrails.tools.trust.default is used.
func TestEffectiveTrust_FallsBackToDefault(t *testing.T) {
	agent := &kubeswarmv1alpha1.SwarmAgent{
		Spec: kubeswarmv1alpha1.SwarmAgentSpec{
			Guardrails: &kubeswarmv1alpha1.AgentGuardrails{
				Tools: &kubeswarmv1alpha1.ToolPermissions{
					Trust: &kubeswarmv1alpha1.ToolTrustPolicy{
						Default: kubeswarmv1alpha1.ToolTrustInternal,
					},
				},
			},
		},
	}

	got := effectiveTrust("", agent)
	if got != kubeswarmv1alpha1.ToolTrustInternal {
		t.Errorf("effectiveTrust('', default=internal) = %q, want internal", got)
	}
}

// TestEffectiveTrust_NoGuardrailsFallsToExternal verifies the ultimate fallback
// to "external" when the agent has no guardrails configured at all.
func TestEffectiveTrust_NoGuardrailsFallsToExternal(t *testing.T) {
	agent := &kubeswarmv1alpha1.SwarmAgent{}

	got := effectiveTrust("", agent)
	if got != kubeswarmv1alpha1.ToolTrustExternal {
		t.Errorf("effectiveTrust('', no guardrails) = %q, want external", got)
	}
}

// TestEffectiveTrust_NilTrustPolicyFallsToExternal verifies fallback when
// guardrails.tools exists but trust is nil.
func TestEffectiveTrust_NilTrustPolicyFallsToExternal(t *testing.T) {
	agent := &kubeswarmv1alpha1.SwarmAgent{
		Spec: kubeswarmv1alpha1.SwarmAgentSpec{
			Guardrails: &kubeswarmv1alpha1.AgentGuardrails{
				Tools: &kubeswarmv1alpha1.ToolPermissions{
					// Trust is nil.
				},
			},
		},
	}

	got := effectiveTrust("", agent)
	if got != kubeswarmv1alpha1.ToolTrustExternal {
		t.Errorf("effectiveTrust('', trust=nil) = %q, want external", got)
	}
}
