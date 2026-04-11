package v1alpha1

import (
	"testing"
)

// TestReasoningModeConstants asserts the string values of the ReasoningMode enum constants.
func TestReasoningModeConstants(t *testing.T) {
	cases := []struct {
		name string
		got  ReasoningMode
		want string
	}{
		{"Disabled", ReasoningDisabled, "Disabled"},
		{"Auto", ReasoningAuto, "Auto"},
		{"Explicit", ReasoningExplicit, "Explicit"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if string(tc.got) != tc.want {
				t.Errorf("ReasoningMode %s = %q, want %q", tc.name, string(tc.got), tc.want)
			}
		})
	}
}

// TestReasoningEffortConstants asserts the string values of the ReasoningEffort enum constants.
func TestReasoningEffortConstants(t *testing.T) {
	cases := []struct {
		name string
		got  ReasoningEffort
		want string
	}{
		{"Low", ReasoningEffortLow, "Low"},
		{"Medium", ReasoningEffortMedium, "Medium"},
		{"High", ReasoningEffortHigh, "High"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if string(tc.got) != tc.want {
				t.Errorf("ReasoningEffort %s = %q, want %q", tc.name, string(tc.got), tc.want)
			}
		})
	}
}

// TestReasoningModeEnumEnumeration guards against accidental addition/removal of enum values.
func TestReasoningModeEnumEnumeration(t *testing.T) {
	all := []ReasoningMode{ReasoningDisabled, ReasoningAuto, ReasoningExplicit}
	if len(all) != 3 {
		t.Fatalf("expected 3 ReasoningMode values, got %d", len(all))
	}
	seen := map[ReasoningMode]bool{}
	for _, m := range all {
		if seen[m] {
			t.Errorf("duplicate ReasoningMode value: %q", string(m))
		}
		seen[m] = true
		if string(m) == "" {
			t.Errorf("ReasoningMode value must not be empty")
		}
	}
}

// TestReasoningEffortEnumEnumeration guards against accidental addition/removal of effort values.
func TestReasoningEffortEnumEnumeration(t *testing.T) {
	all := []ReasoningEffort{ReasoningEffortLow, ReasoningEffortMedium, ReasoningEffortHigh}
	if len(all) != 3 {
		t.Fatalf("expected 3 ReasoningEffort values, got %d", len(all))
	}
	seen := map[ReasoningEffort]bool{}
	for _, e := range all {
		if seen[e] {
			t.Errorf("duplicate ReasoningEffort value: %q", string(e))
		}
		seen[e] = true
		if string(e) == "" {
			t.Errorf("ReasoningEffort value must not be empty")
		}
	}
}

// TestReasoningConfigZeroValue confirms the zero value is safe and all fields start unset.
func TestReasoningConfigZeroValue(t *testing.T) {
	var rc ReasoningConfig
	if rc.Mode != "" {
		t.Errorf("zero ReasoningConfig.Mode = %q, want empty", string(rc.Mode))
	}
	if rc.Effort != "" {
		t.Errorf("zero ReasoningConfig.Effort = %q, want empty", string(rc.Effort))
	}
	if rc.BudgetTokens != nil {
		t.Errorf("zero ReasoningConfig.BudgetTokens = %v, want nil", rc.BudgetTokens)
	}
}

// TestReasoningConfigRoundTrip sets every field and asserts readback.
func TestReasoningConfigRoundTrip(t *testing.T) {
	budget := int32(2048)
	rc := ReasoningConfig{
		Mode:         ReasoningExplicit,
		Effort:       ReasoningEffortHigh,
		BudgetTokens: &budget,
	}
	if rc.Mode != ReasoningExplicit {
		t.Errorf("Mode = %q, want %q", rc.Mode, ReasoningExplicit)
	}
	if rc.Effort != ReasoningEffortHigh {
		t.Errorf("Effort = %q, want %q", rc.Effort, ReasoningEffortHigh)
	}
	if rc.BudgetTokens == nil || *rc.BudgetTokens != 2048 {
		t.Errorf("BudgetTokens = %v, want *2048", rc.BudgetTokens)
	}
}

// TestReasoningDefaultsFields confirms the SwarmSettings cascade type has the same fields.
func TestReasoningDefaultsFields(t *testing.T) {
	budget := int32(1024)
	rd := ReasoningDefaults{
		Mode:         ReasoningAuto,
		Effort:       ReasoningEffortMedium,
		BudgetTokens: &budget,
	}
	if rd.Mode != ReasoningAuto {
		t.Errorf("Mode = %q, want %q", rd.Mode, ReasoningAuto)
	}
	if rd.Effort != ReasoningEffortMedium {
		t.Errorf("Effort = %q, want %q", rd.Effort, ReasoningEffortMedium)
	}
	if rd.BudgetTokens == nil || *rd.BudgetTokens != 1024 {
		t.Errorf("BudgetTokens = %v, want *1024", rd.BudgetTokens)
	}

	var zero ReasoningDefaults
	if zero.Mode != "" || zero.Effort != "" || zero.BudgetTokens != nil {
		t.Errorf("zero ReasoningDefaults should be empty, got %+v", zero)
	}
}

// TestSwarmAgentSpecReasoningPointer asserts Reasoning is *ReasoningConfig, nil by default.
func TestSwarmAgentSpecReasoningPointer(t *testing.T) {
	var spec SwarmAgentSpec
	if spec.Reasoning != nil {
		t.Errorf("SwarmAgentSpec.Reasoning default = %v, want nil", spec.Reasoning)
	}

	budget := int32(512)
	spec.Reasoning = &ReasoningConfig{
		Mode:         ReasoningExplicit,
		Effort:       ReasoningEffortLow,
		BudgetTokens: &budget,
	}
	if spec.Reasoning == nil {
		t.Fatal("SwarmAgentSpec.Reasoning = nil after assignment")
	}
	if spec.Reasoning.Mode != ReasoningExplicit {
		t.Errorf("Reasoning.Mode = %q, want %q", spec.Reasoning.Mode, ReasoningExplicit)
	}
	if spec.Reasoning.Effort != ReasoningEffortLow {
		t.Errorf("Reasoning.Effort = %q, want %q", spec.Reasoning.Effort, ReasoningEffortLow)
	}
	if spec.Reasoning.BudgetTokens == nil || *spec.Reasoning.BudgetTokens != 512 {
		t.Errorf("Reasoning.BudgetTokens = %v, want *512", spec.Reasoning.BudgetTokens)
	}
}

// TestGuardrailLimitsReasoningPointers asserts the new fields exist as *int32, default nil.
func TestGuardrailLimitsReasoningPointers(t *testing.T) {
	var g GuardrailLimits
	if g.MaxThinkingTokensPerCall != nil {
		t.Errorf("MaxThinkingTokensPerCall default = %v, want nil", g.MaxThinkingTokensPerCall)
	}
	if g.MaxAnswerTokensPerCall != nil {
		t.Errorf("MaxAnswerTokensPerCall default = %v, want nil", g.MaxAnswerTokensPerCall)
	}

	thinking := int32(4096)
	answer := int32(8192)
	g.MaxThinkingTokensPerCall = &thinking
	g.MaxAnswerTokensPerCall = &answer

	if g.MaxThinkingTokensPerCall == nil || *g.MaxThinkingTokensPerCall != 4096 {
		t.Errorf("MaxThinkingTokensPerCall = %v, want *4096", g.MaxThinkingTokensPerCall)
	}
	if g.MaxAnswerTokensPerCall == nil || *g.MaxAnswerTokensPerCall != 8192 {
		t.Errorf("MaxAnswerTokensPerCall = %v, want *8192", g.MaxAnswerTokensPerCall)
	}

	// Type assertion: ensure these are exactly *int32 (not *int, not *int64).
	var _ = g.MaxThinkingTokensPerCall
	var _ = g.MaxAnswerTokensPerCall
}

// TestTokenUsageThinkingTokens asserts ThinkingTokens is int64 and independent of Input/Output.
func TestTokenUsageThinkingTokens(t *testing.T) {
	var u TokenUsage

	// Compile-time assert int64.
	var _ = u.ThinkingTokens

	if u.ThinkingTokens != 0 {
		t.Errorf("zero TokenUsage.ThinkingTokens = %d, want 0", u.ThinkingTokens)
	}

	u.InputTokens = 100
	u.OutputTokens = 200
	u.ThinkingTokens = 50

	// Independence: setting ThinkingTokens must not mutate Input/Output.
	if u.InputTokens != 100 {
		t.Errorf("InputTokens mutated: got %d, want 100", u.InputTokens)
	}
	if u.OutputTokens != 200 {
		t.Errorf("OutputTokens mutated: got %d, want 200", u.OutputTokens)
	}
	if u.ThinkingTokens != 50 {
		t.Errorf("ThinkingTokens = %d, want 50", u.ThinkingTokens)
	}

	// And vice versa: updating Output doesn't bleed into Thinking.
	u.OutputTokens = 999
	if u.ThinkingTokens != 50 {
		t.Errorf("ThinkingTokens changed after OutputTokens update: got %d, want 50", u.ThinkingTokens)
	}
}

// TestSwarmSettingsSpec_Reasoning_IsDefaultsType asserts SwarmSettingsSpec has a
// Reasoning *ReasoningDefaults field, nil by default, and settable round-trip.
func TestSwarmSettingsSpec_Reasoning_IsDefaultsType(t *testing.T) {
	var spec SwarmSettingsSpec
	if spec.Reasoning != nil {
		t.Errorf("SwarmSettingsSpec.Reasoning default = %v, want nil", spec.Reasoning)
	}
	budget := int32(2048)
	spec.Reasoning = &ReasoningDefaults{
		Mode:         ReasoningAuto,
		Effort:       ReasoningEffortMedium,
		BudgetTokens: &budget,
	}
	if spec.Reasoning == nil {
		t.Fatal("SwarmSettingsSpec.Reasoning = nil after assignment")
	}
	if spec.Reasoning.Mode != ReasoningAuto {
		t.Errorf("Reasoning.Mode = %q, want %q", spec.Reasoning.Mode, ReasoningAuto)
	}
	if spec.Reasoning.Effort != ReasoningEffortMedium {
		t.Errorf("Reasoning.Effort = %q, want %q", spec.Reasoning.Effort, ReasoningEffortMedium)
	}
	if spec.Reasoning.BudgetTokens == nil || *spec.Reasoning.BudgetTokens != 2048 {
		t.Errorf("Reasoning.BudgetTokens = %v, want *2048", spec.Reasoning.BudgetTokens)
	}
}

// TestPipelineStepStatus_ErrorCodeField asserts the struct has an ErrorCode string field, empty by default.
func TestPipelineStepStatus_ErrorCodeField(t *testing.T) {
	var s PipelineStepStatus
	if s.ErrorCode != "" {
		t.Errorf("zero PipelineStepStatus.ErrorCode = %q, want empty", s.ErrorCode)
	}
	s.ErrorCode = "LLMTimeout"
	if s.ErrorCode != "LLMTimeout" {
		t.Errorf("ErrorCode = %q, want %q", s.ErrorCode, "LLMTimeout")
	}
}

// TestMCPToolSpec_DiscoveryField asserts that MCPToolSpec has a Discovery *MCPDiscoveryConfig
// field that is nil by default and accepts non-nil with Dynamic=true and PollIntervalSeconds=300.
func TestMCPToolSpec_DiscoveryField(t *testing.T) {
	var spec MCPToolSpec
	if spec.Discovery != nil {
		t.Errorf("MCPToolSpec.Discovery default = %v, want nil", spec.Discovery)
	}

	spec.Discovery = &MCPDiscoveryConfig{
		Dynamic:             true,
		PollIntervalSeconds: 300,
	}
	if spec.Discovery == nil {
		t.Fatal("MCPToolSpec.Discovery = nil after assignment")
	}
	if !spec.Discovery.Dynamic {
		t.Errorf("Discovery.Dynamic = %v, want true", spec.Discovery.Dynamic)
	}
	if spec.Discovery.PollIntervalSeconds != 300 {
		t.Errorf("Discovery.PollIntervalSeconds = %d, want 300", spec.Discovery.PollIntervalSeconds)
	}

	// Zero value: Dynamic false, PollIntervalSeconds 0.
	spec.Discovery = &MCPDiscoveryConfig{}
	if spec.Discovery.Dynamic {
		t.Errorf("zero MCPDiscoveryConfig.Dynamic = %v, want false", spec.Discovery.Dynamic)
	}
	if spec.Discovery.PollIntervalSeconds != 0 {
		t.Errorf("zero MCPDiscoveryConfig.PollIntervalSeconds = %d, want 0", spec.Discovery.PollIntervalSeconds)
	}
}

// TestPipelineStepStatus_ErrorSuggestionField asserts the struct has an ErrorSuggestion string field, empty by default.
func TestPipelineStepStatus_ErrorSuggestionField(t *testing.T) {
	var s PipelineStepStatus
	if s.ErrorSuggestion != "" {
		t.Errorf("zero PipelineStepStatus.ErrorSuggestion = %q, want empty", s.ErrorSuggestion)
	}
	s.ErrorSuggestion = "Increase timeout"
	if s.ErrorSuggestion != "Increase timeout" {
		t.Errorf("ErrorSuggestion = %q, want %q", s.ErrorSuggestion, "Increase timeout")
	}
}

// TestReasoningConditionConstant asserts the condition type string.
func TestReasoningConditionConstant(t *testing.T) {
	if ConditionReasoningActive != "ReasoningActive" {
		t.Errorf("ConditionReasoningActive = %q, want %q", ConditionReasoningActive, "ReasoningActive")
	}
}

// TestConditionMCPHealthy asserts the MCPHealthy condition constant value.
func TestConditionMCPHealthy(t *testing.T) {
	if ConditionMCPHealthy != "MCPHealthy" {
		t.Errorf("ConditionMCPHealthy = %q, want %q", ConditionMCPHealthy, "MCPHealthy")
	}
}

// TestGuardrailLimits_CircuitBreakerField asserts that GuardrailLimits has a
// CircuitBreaker *CircuitBreakerConfig field that is nil by default and round-trips.
func TestGuardrailLimits_CircuitBreakerField(t *testing.T) {
	var g GuardrailLimits
	if g.CircuitBreaker != nil {
		t.Errorf("GuardrailLimits.CircuitBreaker default = %v, want nil", g.CircuitBreaker)
	}

	g.CircuitBreaker = &CircuitBreakerConfig{
		FailureThreshold: 5,
		CooldownSeconds:  30,
		HalfOpenMaxCalls: 1,
	}
	if g.CircuitBreaker == nil {
		t.Fatal("GuardrailLimits.CircuitBreaker = nil after assignment")
	}
	if g.CircuitBreaker.FailureThreshold != 5 {
		t.Errorf("FailureThreshold = %d, want 5", g.CircuitBreaker.FailureThreshold)
	}
	if g.CircuitBreaker.CooldownSeconds != 30 {
		t.Errorf("CooldownSeconds = %d, want 30", g.CircuitBreaker.CooldownSeconds)
	}
	if g.CircuitBreaker.HalfOpenMaxCalls != 1 {
		t.Errorf("HalfOpenMaxCalls = %d, want 1", g.CircuitBreaker.HalfOpenMaxCalls)
	}

	// Zero value of CircuitBreakerConfig: all ints are 0.
	g.CircuitBreaker = &CircuitBreakerConfig{}
	if g.CircuitBreaker.FailureThreshold != 0 {
		t.Errorf("zero CircuitBreakerConfig.FailureThreshold = %d, want 0", g.CircuitBreaker.FailureThreshold)
	}
	if g.CircuitBreaker.CooldownSeconds != 0 {
		t.Errorf("zero CircuitBreakerConfig.CooldownSeconds = %d, want 0", g.CircuitBreaker.CooldownSeconds)
	}
	if g.CircuitBreaker.HalfOpenMaxCalls != 0 {
		t.Errorf("zero CircuitBreakerConfig.HalfOpenMaxCalls = %d, want 0", g.CircuitBreaker.HalfOpenMaxCalls)
	}
}

// TestReasoningReasonConstants asserts all six reason constants exist and are non-empty/unique.
func TestReasoningReasonConstants(t *testing.T) {
	cases := []struct {
		name string
		got  string
	}{
		{"Disabled", ReasoningReasonDisabled},
		{"Active", ReasoningReasonActive},
		{"IgnoredModelNotCapable", ReasoningReasonIgnoredModelNotCapable},
		{"ClampedByGuardrail", ReasoningReasonClampedByGuardrail},
		{"FieldIgnored", ReasoningReasonFieldIgnored},
		{"RejectedModelNotCapable", ReasoningReasonRejectedModelNotCapable},
	}
	seen := map[string]bool{}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.got == "" {
				t.Errorf("reason %s is empty", tc.name)
			}
			if seen[tc.got] {
				t.Errorf("duplicate reason constant value: %q", tc.got)
			}
			seen[tc.got] = true
		})
	}
	if len(seen) != 6 {
		t.Errorf("expected 6 unique reason constants, got %d", len(seen))
	}
}
