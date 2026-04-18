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
	"encoding/json"
	"testing"

	corev1 "k8s.io/api/core/v1"

	kubeswarmv1alpha1 "github.com/kubeswarm/kubeswarm/api/v1alpha1"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

//go:fix inline
func ptr32(v int32) *int32 { return new(v) }

//go:fix inline
func ptr64(v int64) *int64 { return new(v) }

//go:fix inline
func ptrTrustLevel(v kubeswarmv1alpha1.ToolTrustLevel) *kubeswarmv1alpha1.ToolTrustLevel {
	return new(v)
}

// envMap converts a slice of EnvVar to a map for easier assertions.
func envMap(vars []corev1.EnvVar) map[string]string {
	m := make(map[string]string, len(vars))
	for _, e := range vars {
		m[e.Name] = e.Value
	}
	return m
}

// ---------------------------------------------------------------------------
// applyPolicyLimits
// ---------------------------------------------------------------------------

func TestApplyPolicyLimits_NilPolicy(t *testing.T) {
	maxTok, timeout, retries, daily := applyPolicyLimits(8000, 120, 3, int64(500_000), nil)
	if maxTok != 8000 || timeout != 120 || retries != 3 || daily != int64(500_000) {
		t.Fatalf("nil policy should pass through: got maxTok=%d timeout=%d retries=%d daily=%d",
			maxTok, timeout, retries, daily)
	}
}

func TestApplyPolicyLimits_NilLimits(t *testing.T) {
	ep := &kubeswarmv1alpha1.EffectivePolicySpec{Limits: nil}
	maxTok, timeout, retries, daily := applyPolicyLimits(8000, 120, 3, int64(500_000), ep)
	if maxTok != 8000 || timeout != 120 || retries != 3 || daily != int64(500_000) {
		t.Fatalf("nil limits should pass through: got maxTok=%d timeout=%d retries=%d daily=%d",
			maxTok, timeout, retries, daily)
	}
}

func TestApplyPolicyLimits_CeilingClampsWhenAgentExceeds(t *testing.T) {
	ep := &kubeswarmv1alpha1.EffectivePolicySpec{
		Limits: &kubeswarmv1alpha1.PolicyLimits{
			MaxTokensPerCall:  ptr32(4000),
			MaxTimeoutSeconds: ptr32(60),
			MaxDailyTokens:    ptr64(100_000),
		},
	}
	maxTok, timeout, _, daily := applyPolicyLimits(8000, 120, 3, int64(500_000), ep)
	if maxTok != 4000 {
		t.Errorf("maxTokens: want 4000, got %d", maxTok)
	}
	if timeout != 60 {
		t.Errorf("timeout: want 60, got %d", timeout)
	}
	if daily != int64(100_000) {
		t.Errorf("dailyTokenLimit: want 100000, got %d", daily)
	}
}

func TestApplyPolicyLimits_CeilingDoesNotClampWhenAgentIsLower(t *testing.T) {
	ep := &kubeswarmv1alpha1.EffectivePolicySpec{
		Limits: &kubeswarmv1alpha1.PolicyLimits{
			MaxTokensPerCall:  ptr32(10000),
			MaxTimeoutSeconds: ptr32(300),
			MaxDailyTokens:    ptr64(1_000_000),
		},
	}
	maxTok, timeout, _, daily := applyPolicyLimits(4000, 60, 3, int64(200_000), ep)
	if maxTok != 4000 {
		t.Errorf("maxTokens: want 4000 (agent lower), got %d", maxTok)
	}
	if timeout != 60 {
		t.Errorf("timeout: want 60 (agent lower), got %d", timeout)
	}
	if daily != int64(200_000) {
		t.Errorf("dailyTokenLimit: want 200000 (agent lower), got %d", daily)
	}
}

func TestApplyPolicyLimits_FloorClampsWhenAgentIsBelow(t *testing.T) {
	ep := &kubeswarmv1alpha1.EffectivePolicySpec{
		Limits: &kubeswarmv1alpha1.PolicyLimits{
			MinTimeoutSeconds: ptr32(30),
		},
	}
	_, timeout, _, _ := applyPolicyLimits(4000, 10, 3, int64(200_000), ep)
	if timeout != 30 {
		t.Errorf("timeout: want 30 (floor clamp), got %d", timeout)
	}
}

func TestApplyPolicyLimits_FloorDoesNotClampWhenAgentIsAbove(t *testing.T) {
	ep := &kubeswarmv1alpha1.EffectivePolicySpec{
		Limits: &kubeswarmv1alpha1.PolicyLimits{
			MinTimeoutSeconds: ptr32(30),
		},
	}
	_, timeout, _, _ := applyPolicyLimits(4000, 120, 3, int64(200_000), ep)
	if timeout != 120 {
		t.Errorf("timeout: want 120 (already above floor), got %d", timeout)
	}
}

func TestApplyPolicyLimits_DailyTokenLimitZeroGetsPolicyCeiling(t *testing.T) {
	ep := &kubeswarmv1alpha1.EffectivePolicySpec{
		Limits: &kubeswarmv1alpha1.PolicyLimits{
			MaxDailyTokens: ptr64(500_000),
		},
	}
	_, _, _, daily := applyPolicyLimits(4000, 60, 3, 0, ep)
	if daily != int64(500_000) {
		t.Errorf("dailyTokenLimit: want 500000 (agent 0 means no limit, policy should set it), got %d", daily)
	}
}

func TestApplyPolicyLimits_CeilingAndFloorSimultaneously(t *testing.T) {
	ep := &kubeswarmv1alpha1.EffectivePolicySpec{
		Limits: &kubeswarmv1alpha1.PolicyLimits{
			MaxTimeoutSeconds: ptr32(120),
			MinTimeoutSeconds: ptr32(30),
			MaxTokensPerCall:  ptr32(4000),
		},
	}
	// Timeout 10 is below floor -> 30, maxTokens 8000 exceeds ceiling -> 4000
	maxTok, timeout, retries, daily := applyPolicyLimits(8000, 10, 5, int64(200_000), ep)
	if maxTok != 4000 {
		t.Errorf("maxTokens: want 4000, got %d", maxTok)
	}
	if timeout != 30 {
		t.Errorf("timeout: want 30 (floor clamp), got %d", timeout)
	}
	// retries and daily untouched
	if retries != 5 {
		t.Errorf("retries: want 5 (pass through), got %d", retries)
	}
	if daily != int64(200_000) {
		t.Errorf("dailyTokenLimit: want 200000 (pass through), got %d", daily)
	}
}

func TestApplyPolicyLimits_OnlyOneFieldSetOthersPassThrough(t *testing.T) {
	ep := &kubeswarmv1alpha1.EffectivePolicySpec{
		Limits: &kubeswarmv1alpha1.PolicyLimits{
			MaxTokensPerCall: ptr32(2000),
			// All other fields nil
		},
	}
	maxTok, timeout, retries, daily := applyPolicyLimits(5000, 120, 3, int64(999_999), ep)
	if maxTok != 2000 {
		t.Errorf("maxTokens: want 2000 (clamped), got %d", maxTok)
	}
	if timeout != 120 {
		t.Errorf("timeout: want 120 (pass through), got %d", timeout)
	}
	if retries != 3 {
		t.Errorf("retries: want 3 (pass through), got %d", retries)
	}
	if daily != int64(999_999) {
		t.Errorf("dailyTokenLimit: want 999999 (pass through), got %d", daily)
	}
}

// ---------------------------------------------------------------------------
// applyPolicyThinkingLimits
// ---------------------------------------------------------------------------

func TestApplyPolicyThinkingLimits_NilPolicy(t *testing.T) {
	think := ptr32(16000)
	answer := ptr32(4000)
	gotThink, gotAnswer := applyPolicyThinkingLimits(think, answer, nil)
	if *gotThink != 16000 || *gotAnswer != 4000 {
		t.Fatalf("nil policy should pass through: got think=%d answer=%d", *gotThink, *gotAnswer)
	}
}

func TestApplyPolicyThinkingLimits_NilLimits(t *testing.T) {
	think := ptr32(16000)
	answer := ptr32(4000)
	ep := &kubeswarmv1alpha1.EffectivePolicySpec{Limits: nil}
	gotThink, gotAnswer := applyPolicyThinkingLimits(think, answer, ep)
	if *gotThink != 16000 || *gotAnswer != 4000 {
		t.Fatalf("nil limits should pass through: got think=%d answer=%d", *gotThink, *gotAnswer)
	}
}

func TestApplyPolicyThinkingLimits_ClampsWhenAgentExceeds(t *testing.T) {
	think := ptr32(16000)
	answer := ptr32(8000)
	ep := &kubeswarmv1alpha1.EffectivePolicySpec{
		Limits: &kubeswarmv1alpha1.PolicyLimits{
			MaxThinkingTokensPerCall: ptr32(8000),
			MaxAnswerTokensPerCall:   ptr32(4000),
		},
	}
	gotThink, gotAnswer := applyPolicyThinkingLimits(think, answer, ep)
	if *gotThink != 8000 {
		t.Errorf("thinkingTokens: want 8000, got %d", *gotThink)
	}
	if *gotAnswer != 4000 {
		t.Errorf("answerTokens: want 4000, got %d", *gotAnswer)
	}
}

func TestApplyPolicyThinkingLimits_DoesNotClampWhenAgentIsLower(t *testing.T) {
	think := ptr32(4000)
	answer := ptr32(2000)
	ep := &kubeswarmv1alpha1.EffectivePolicySpec{
		Limits: &kubeswarmv1alpha1.PolicyLimits{
			MaxThinkingTokensPerCall: ptr32(16000),
			MaxAnswerTokensPerCall:   ptr32(8000),
		},
	}
	gotThink, gotAnswer := applyPolicyThinkingLimits(think, answer, ep)
	if *gotThink != 4000 {
		t.Errorf("thinkingTokens: want 4000 (agent lower), got %d", *gotThink)
	}
	if *gotAnswer != 2000 {
		t.Errorf("answerTokens: want 2000 (agent lower), got %d", *gotAnswer)
	}
}

func TestApplyPolicyThinkingLimits_NilAgentGetsSetFromPolicy(t *testing.T) {
	ep := &kubeswarmv1alpha1.EffectivePolicySpec{
		Limits: &kubeswarmv1alpha1.PolicyLimits{
			MaxThinkingTokensPerCall: ptr32(8000),
			MaxAnswerTokensPerCall:   ptr32(4000),
		},
	}
	gotThink, gotAnswer := applyPolicyThinkingLimits(nil, nil, ep)
	if gotThink == nil || *gotThink != 8000 {
		t.Errorf("thinkingTokens: want 8000 (set from policy), got %v", gotThink)
	}
	if gotAnswer == nil || *gotAnswer != 4000 {
		t.Errorf("answerTokens: want 4000 (set from policy), got %v", gotAnswer)
	}
}

func TestApplyPolicyThinkingLimits_NilAgentNilPolicyField(t *testing.T) {
	ep := &kubeswarmv1alpha1.EffectivePolicySpec{
		Limits: &kubeswarmv1alpha1.PolicyLimits{
			// Both thinking/answer nil in policy
		},
	}
	gotThink, gotAnswer := applyPolicyThinkingLimits(nil, nil, ep)
	if gotThink != nil {
		t.Errorf("thinkingTokens: want nil (no policy constraint), got %d", *gotThink)
	}
	if gotAnswer != nil {
		t.Errorf("answerTokens: want nil (no policy constraint), got %d", *gotAnswer)
	}
}

func TestApplyPolicyThinkingLimits_MixedNilAndSet(t *testing.T) {
	// Agent has thinking set but answer nil; policy has both
	think := ptr32(20000)
	ep := &kubeswarmv1alpha1.EffectivePolicySpec{
		Limits: &kubeswarmv1alpha1.PolicyLimits{
			MaxThinkingTokensPerCall: ptr32(10000),
			MaxAnswerTokensPerCall:   ptr32(5000),
		},
	}
	gotThink, gotAnswer := applyPolicyThinkingLimits(think, nil, ep)
	if *gotThink != 10000 {
		t.Errorf("thinkingTokens: want 10000 (clamped), got %d", *gotThink)
	}
	if gotAnswer == nil || *gotAnswer != 5000 {
		t.Errorf("answerTokens: want 5000 (set from policy), got %v", gotAnswer)
	}
}

// ---------------------------------------------------------------------------
// buildPolicyEnvVars
// ---------------------------------------------------------------------------

func TestBuildPolicyEnvVars_NilEffectivePolicy(t *testing.T) {
	vars := buildPolicyEnvVars(nil)
	if len(vars) != 0 {
		t.Fatalf("nil ep should return empty slice, got %d vars", len(vars))
	}
}

func TestBuildPolicyEnvVars_EmptyEffectivePolicy(t *testing.T) {
	ep := &kubeswarmv1alpha1.EffectivePolicySpec{}
	vars := buildPolicyEnvVars(ep)
	if len(vars) != 0 {
		t.Fatalf("empty ep should return empty slice, got %d vars", len(vars))
	}
}

func TestBuildPolicyEnvVars_ToolDeny(t *testing.T) {
	ep := &kubeswarmv1alpha1.EffectivePolicySpec{
		ToolDeny: []string{"shell/*", "filesystem/write_file"},
	}
	m := envMap(buildPolicyEnvVars(ep))
	raw, ok := m["AGENT_POLICY_TOOL_DENY"]
	if !ok {
		t.Fatal("expected AGENT_POLICY_TOOL_DENY to be set")
	}
	var got []string
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatalf("AGENT_POLICY_TOOL_DENY is not valid JSON: %v", err)
	}
	if len(got) != 2 || got[0] != "shell/*" || got[1] != "filesystem/write_file" {
		t.Errorf("unexpected AGENT_POLICY_TOOL_DENY: %v", got)
	}
}

func TestBuildPolicyEnvVars_ForceTrustLevel(t *testing.T) {
	ep := &kubeswarmv1alpha1.EffectivePolicySpec{
		ForceTrustLevel: ptrTrustLevel(kubeswarmv1alpha1.ToolTrustSandbox),
	}
	m := envMap(buildPolicyEnvVars(ep))
	val, ok := m["AGENT_POLICY_FORCE_TRUST_LEVEL"]
	if !ok {
		t.Fatal("expected AGENT_POLICY_FORCE_TRUST_LEVEL to be set")
	}
	if val != "sandbox" {
		t.Errorf("want sandbox, got %s", val)
	}
}

func TestBuildPolicyEnvVars_DenyPatterns_NotInjected_WithValues(t *testing.T) {
	// AGENT_POLICY_DENY_PATTERNS is not consumed by the agent binary yet.
	ep := &kubeswarmv1alpha1.EffectivePolicySpec{
		DenyPatterns: []string{`(?i)secret`, `password\s*=`},
	}
	m := envMap(buildPolicyEnvVars(ep))
	if _, ok := m["AGENT_POLICY_DENY_PATTERNS"]; ok {
		t.Fatal("AGENT_POLICY_DENY_PATTERNS should not be injected (not consumed by agent)")
	}
	// Verify that patterns are kept in the struct for merge/compliance evaluation.
	if len(ep.DenyPatterns) != 2 {
		t.Errorf("expected 2 patterns in spec, got %d", len(ep.DenyPatterns))
	}
}

func TestBuildPolicyEnvVars_MinValidation_NotInjected(t *testing.T) {
	// AGENT_POLICY_MIN_VALIDATION is not consumed by the agent binary yet.
	// Verify it is NOT injected to avoid dead env vars in pod specs.
	ep := &kubeswarmv1alpha1.EffectivePolicySpec{
		MinValidation: kubeswarmv1alpha1.PolicyOutputLevelPattern,
	}
	m := envMap(buildPolicyEnvVars(ep))
	if _, ok := m["AGENT_POLICY_MIN_VALIDATION"]; ok {
		t.Error("AGENT_POLICY_MIN_VALIDATION should not be injected (not consumed by agent)")
	}
}

func TestBuildPolicyEnvVars_MaxDailyTokens_NotInjected(t *testing.T) {
	// AGENT_POLICY_MAX_DAILY_TOKENS is not consumed by the agent binary yet.
	ep := &kubeswarmv1alpha1.EffectivePolicySpec{
		Limits: &kubeswarmv1alpha1.PolicyLimits{
			MaxDailyTokens: ptr64(1_000_000),
		},
	}
	m := envMap(buildPolicyEnvVars(ep))
	if _, ok := m["AGENT_POLICY_MAX_DAILY_TOKENS"]; ok {
		t.Error("AGENT_POLICY_MAX_DAILY_TOKENS should not be injected (not consumed by agent)")
	}
}

func TestBuildPolicyEnvVars_DenyPatterns_NotInjected(t *testing.T) {
	// AGENT_POLICY_DENY_PATTERNS is not consumed by the agent binary yet.
	ep := &kubeswarmv1alpha1.EffectivePolicySpec{
		DenyPatterns: []string{`(?i)token`},
	}
	m := envMap(buildPolicyEnvVars(ep))
	if _, ok := m["AGENT_POLICY_DENY_PATTERNS"]; ok {
		t.Error("AGENT_POLICY_DENY_PATTERNS should not be injected (not consumed by agent)")
	}
}

func TestBuildPolicyEnvVars_MultipleFieldsSet(t *testing.T) {
	ep := &kubeswarmv1alpha1.EffectivePolicySpec{
		ToolDeny:        []string{"shell/*"},
		ForceTrustLevel: ptrTrustLevel(kubeswarmv1alpha1.ToolTrustExternal),
		MinValidation:   kubeswarmv1alpha1.PolicyOutputLevelSchema,
		DenyPatterns:    []string{`(?i)token`},
		Limits: &kubeswarmv1alpha1.PolicyLimits{
			MaxDailyTokens: ptr64(500_000),
		},
	}
	vars := buildPolicyEnvVars(ep)
	m := envMap(vars)

	// Only env vars consumed by the agent binary should be injected.
	expectedKeys := []string{
		"AGENT_POLICY_TOOL_DENY",
		"AGENT_POLICY_FORCE_TRUST_LEVEL",
	}
	for _, key := range expectedKeys {
		if _, ok := m[key]; !ok {
			t.Errorf("expected %s to be present", key)
		}
	}
	if len(vars) != len(expectedKeys) {
		t.Errorf("expected exactly %d env vars, got %d", len(expectedKeys), len(vars))
	}
}

func TestBuildPolicyEnvVars_EmptyToolDeny_Omitted(t *testing.T) {
	ep := &kubeswarmv1alpha1.EffectivePolicySpec{
		ToolDeny: []string{},
	}
	m := envMap(buildPolicyEnvVars(ep))
	if _, ok := m["AGENT_POLICY_TOOL_DENY"]; ok {
		t.Error("AGENT_POLICY_TOOL_DENY should be omitted for empty slice")
	}
}

func TestBuildPolicyEnvVars_EmptyDenyPatterns_Omitted(t *testing.T) {
	ep := &kubeswarmv1alpha1.EffectivePolicySpec{
		DenyPatterns: []string{},
	}
	m := envMap(buildPolicyEnvVars(ep))
	if _, ok := m["AGENT_POLICY_DENY_PATTERNS"]; ok {
		t.Error("AGENT_POLICY_DENY_PATTERNS should be omitted for empty slice")
	}
}

func TestBuildPolicyEnvVars_NilForceTrustLevel_Omitted(t *testing.T) {
	ep := &kubeswarmv1alpha1.EffectivePolicySpec{
		ForceTrustLevel: nil,
	}
	m := envMap(buildPolicyEnvVars(ep))
	if _, ok := m["AGENT_POLICY_FORCE_TRUST_LEVEL"]; ok {
		t.Error("AGENT_POLICY_FORCE_TRUST_LEVEL should be omitted when nil")
	}
}
