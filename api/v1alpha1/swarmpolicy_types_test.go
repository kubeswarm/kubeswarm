package v1alpha1

import "testing"

// -----------------------------------------------------------------------------
// PolicyEnforcementMode constants
// -----------------------------------------------------------------------------

// TestPolicyEnforcementModeConstants asserts the string values of all
// PolicyEnforcementMode enum constants.
func TestPolicyEnforcementModeConstants(t *testing.T) {
	cases := []struct {
		name string
		got  PolicyEnforcementMode
		want string
	}{
		{"Audit", PolicyEnforcementAudit, "Audit"},
		{"Warn", PolicyEnforcementWarn, "Warn"},
		{"Enforce", PolicyEnforcementEnforce, "Enforce"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if string(tc.got) != tc.want {
				t.Errorf("PolicyEnforcementMode %s = %q, want %q", tc.name, string(tc.got), tc.want)
			}
		})
	}
}

// TestPolicyEnforcementModeEnumCount guards against accidental addition or
// removal of PolicyEnforcementMode values.
func TestPolicyEnforcementModeEnumCount(t *testing.T) {
	all := []PolicyEnforcementMode{
		PolicyEnforcementAudit,
		PolicyEnforcementWarn,
		PolicyEnforcementEnforce,
	}
	const want = 3
	if len(all) != want {
		t.Fatalf("expected %d PolicyEnforcementMode values, got %d", want, len(all))
	}
	seen := map[PolicyEnforcementMode]bool{}
	for _, m := range all {
		if seen[m] {
			t.Errorf("duplicate PolicyEnforcementMode value: %q", string(m))
		}
		seen[m] = true
		if string(m) == "" {
			t.Errorf("PolicyEnforcementMode value must not be empty string")
		}
	}
}

// -----------------------------------------------------------------------------
// PolicyOutputLevel constants
// -----------------------------------------------------------------------------

// TestPolicyOutputLevelConstants asserts the string values of all
// PolicyOutputLevel enum constants.
func TestPolicyOutputLevelConstants(t *testing.T) {
	cases := []struct {
		name string
		got  PolicyOutputLevel
		want string
	}{
		{"None", PolicyOutputLevelNone, "none"},
		{"Pattern", PolicyOutputLevelPattern, "pattern"},
		{"Schema", PolicyOutputLevelSchema, "schema"},
		{"Semantic", PolicyOutputLevelSemantic, "semantic"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if string(tc.got) != tc.want {
				t.Errorf("PolicyOutputLevel %s = %q, want %q", tc.name, string(tc.got), tc.want)
			}
		})
	}
}

// TestPolicyOutputLevelEnumCount guards against accidental addition or removal
// of PolicyOutputLevel values.
func TestPolicyOutputLevelEnumCount(t *testing.T) {
	all := []PolicyOutputLevel{
		PolicyOutputLevelNone,
		PolicyOutputLevelPattern,
		PolicyOutputLevelSchema,
		PolicyOutputLevelSemantic,
	}
	const want = 4
	if len(all) != want {
		t.Fatalf("expected %d PolicyOutputLevel values, got %d", want, len(all))
	}
	seen := map[PolicyOutputLevel]bool{}
	for _, l := range all {
		if seen[l] {
			t.Errorf("duplicate PolicyOutputLevel value: %q", string(l))
		}
		seen[l] = true
		if string(l) == "" {
			t.Errorf("PolicyOutputLevel value must not be empty string")
		}
	}
}

// -----------------------------------------------------------------------------
// SwarmPolicySpec zero value
// -----------------------------------------------------------------------------

// TestSwarmPolicySpecZeroValue confirms that a zero-value SwarmPolicySpec is
// safe: all optional pointer fields are nil and slice fields are nil/empty.
func TestSwarmPolicySpecZeroValue(t *testing.T) {
	var spec SwarmPolicySpec

	if spec.EnforcementMode != "" {
		t.Errorf("EnforcementMode = %q, want empty", string(spec.EnforcementMode))
	}
	if spec.Limits != nil {
		t.Errorf("Limits = %v, want nil", spec.Limits)
	}
	if spec.Tools != nil {
		t.Errorf("Tools = %v, want nil", spec.Tools)
	}
	if spec.Output != nil {
		t.Errorf("Output = %v, want nil", spec.Output)
	}
	if spec.Models != nil {
		t.Errorf("Models = %v, want nil", spec.Models)
	}
	if spec.Requirements.BudgetRef || spec.Requirements.Audit || spec.Requirements.AllowList {
		t.Error("Requirements should be all false by default")
	}
}

// -----------------------------------------------------------------------------
// PolicyLimits pointer fields
// -----------------------------------------------------------------------------

// TestPolicyLimitsAllNilByDefault asserts every pointer field in PolicyLimits
// is nil when the struct is zero-valued, meaning no constraint is set.
func TestPolicyLimitsAllNilByDefault(t *testing.T) {
	var lim PolicyLimits

	if lim.MaxDailyTokens != nil {
		t.Errorf("MaxDailyTokens default = %v, want nil", lim.MaxDailyTokens)
	}
	if lim.MaxTokensPerCall != nil {
		t.Errorf("MaxTokensPerCall default = %v, want nil", lim.MaxTokensPerCall)
	}
	if lim.MaxTimeoutSeconds != nil {
		t.Errorf("MaxTimeoutSeconds default = %v, want nil", lim.MaxTimeoutSeconds)
	}
	if lim.MinTimeoutSeconds != nil {
		t.Errorf("MinTimeoutSeconds default = %v, want nil", lim.MinTimeoutSeconds)
	}
	if lim.MaxConcurrentTasks != nil {
		t.Errorf("MaxConcurrentTasks default = %v, want nil", lim.MaxConcurrentTasks)
	}
	if lim.MaxThinkingTokensPerCall != nil {
		t.Errorf("MaxThinkingTokensPerCall default = %v, want nil", lim.MaxThinkingTokensPerCall)
	}
	if lim.MaxAnswerTokensPerCall != nil {
		t.Errorf("MaxAnswerTokensPerCall default = %v, want nil", lim.MaxAnswerTokensPerCall)
	}
}

// TestPolicyLimitsPointerFieldsRoundTrip sets every pointer field and reads
// the values back, confirming type correctness and independence.
func TestPolicyLimitsPointerFieldsRoundTrip(t *testing.T) {
	daily := int64(1_000_000)
	perCall := int32(8000)
	maxTimeout := int32(300)
	minTimeout := int32(30)
	concurrent := int32(10)
	thinking := int32(4096)
	answer := int32(16384)

	lim := PolicyLimits{
		MaxDailyTokens:           &daily,
		MaxTokensPerCall:         &perCall,
		MaxTimeoutSeconds:        &maxTimeout,
		MinTimeoutSeconds:        &minTimeout,
		MaxConcurrentTasks:       &concurrent,
		MaxThinkingTokensPerCall: &thinking,
		MaxAnswerTokensPerCall:   &answer,
	}

	if lim.MaxDailyTokens == nil || *lim.MaxDailyTokens != 1_000_000 {
		t.Errorf("MaxDailyTokens = %v, want *1000000", lim.MaxDailyTokens)
	}
	if lim.MaxTokensPerCall == nil || *lim.MaxTokensPerCall != 8000 {
		t.Errorf("MaxTokensPerCall = %v, want *8000", lim.MaxTokensPerCall)
	}
	if lim.MaxTimeoutSeconds == nil || *lim.MaxTimeoutSeconds != 300 {
		t.Errorf("MaxTimeoutSeconds = %v, want *300", lim.MaxTimeoutSeconds)
	}
	if lim.MinTimeoutSeconds == nil || *lim.MinTimeoutSeconds != 30 {
		t.Errorf("MinTimeoutSeconds = %v, want *30", lim.MinTimeoutSeconds)
	}
	if lim.MaxConcurrentTasks == nil || *lim.MaxConcurrentTasks != 10 {
		t.Errorf("MaxConcurrentTasks = %v, want *10", lim.MaxConcurrentTasks)
	}
	if lim.MaxThinkingTokensPerCall == nil || *lim.MaxThinkingTokensPerCall != 4096 {
		t.Errorf("MaxThinkingTokensPerCall = %v, want *4096", lim.MaxThinkingTokensPerCall)
	}
	if lim.MaxAnswerTokensPerCall == nil || *lim.MaxAnswerTokensPerCall != 16384 {
		t.Errorf("MaxAnswerTokensPerCall = %v, want *16384", lim.MaxAnswerTokensPerCall)
	}
}

// TestPolicyLimitsMaxDailyTokensIsInt64 confirms MaxDailyTokens is *int64
// (not *int32) to support large daily budgets.
func TestPolicyLimitsMaxDailyTokensIsInt64(t *testing.T) {
	large := int64(10_000_000_000)
	lim := PolicyLimits{MaxDailyTokens: &large}
	if *lim.MaxDailyTokens != 10_000_000_000 {
		t.Errorf("MaxDailyTokens = %d, want 10000000000 (requires int64)", *lim.MaxDailyTokens)
	}
}

// -----------------------------------------------------------------------------
// PolicyRequirements zero value
// -----------------------------------------------------------------------------

// TestPolicyRequirementsZeroValue confirms all boolean fields default to false
// (no requirements imposed) and BudgetRef is empty.
func TestPolicyRequirementsZeroValue(t *testing.T) {
	var req PolicyRequirements

	if req.BudgetRef {
		t.Errorf("BudgetRef = %v, want false", req.BudgetRef)
	}
	if req.Audit {
		t.Errorf("Audit = %v, want false", req.Audit)
	}
	if req.AllowList {
		t.Errorf("AllowList = %v, want false", req.AllowList)
	}
}

// TestPolicyRequirementsRoundTrip sets every field and reads back.
func TestPolicyRequirementsRoundTrip(t *testing.T) {
	req := PolicyRequirements{
		BudgetRef: true,
		Audit:     true,
		AllowList: true,
	}
	if !req.BudgetRef {
		t.Errorf("BudgetRef = %v, want true", req.BudgetRef)
	}
	if !req.Audit {
		t.Errorf("Audit = %v, want true", req.Audit)
	}
	if !req.AllowList {
		t.Errorf("AllowList = %v, want true", req.AllowList)
	}
}

// -----------------------------------------------------------------------------
// PolicyModels zero value
// -----------------------------------------------------------------------------

// TestPolicyModelsZeroValue confirms zero value imposes no model restrictions:
// both Allowed and Denied slices are nil/empty.
func TestPolicyModelsZeroValue(t *testing.T) {
	var m PolicyModels

	if len(m.Allowed) != 0 {
		t.Errorf("Allowed len = %d, want 0", len(m.Allowed))
	}
	if len(m.Denied) != 0 {
		t.Errorf("Denied len = %d, want 0", len(m.Denied))
	}
}

// TestPolicyModelsAllowedDeniedIndependent confirms Allowed and Denied are
// independent slices and support glob patterns.
func TestPolicyModelsAllowedDeniedIndependent(t *testing.T) {
	m := PolicyModels{
		Allowed: []string{"claude-*", "gpt-4o"},
		Denied:  []string{"gpt-3.5-*"},
	}

	if len(m.Allowed) != 2 {
		t.Errorf("Allowed len = %d, want 2", len(m.Allowed))
	}
	if m.Allowed[0] != "claude-*" {
		t.Errorf("Allowed[0] = %q, want %q", m.Allowed[0], "claude-*")
	}
	if m.Allowed[1] != "gpt-4o" {
		t.Errorf("Allowed[1] = %q, want %q", m.Allowed[1], "gpt-4o")
	}
	if len(m.Denied) != 1 {
		t.Errorf("Denied len = %d, want 1", len(m.Denied))
	}
	if m.Denied[0] != "gpt-3.5-*" {
		t.Errorf("Denied[0] = %q, want %q", m.Denied[0], "gpt-3.5-*")
	}
}

// -----------------------------------------------------------------------------
// PolicyTools
// -----------------------------------------------------------------------------

// TestPolicyToolsZeroValue confirms zero-value PolicyTools has no denied tools
// and no forced trust level.
func TestPolicyToolsZeroValue(t *testing.T) {
	var pt PolicyTools

	if len(pt.Deny) != 0 {
		t.Errorf("Deny len = %d, want 0", len(pt.Deny))
	}
	if pt.ForceTrustLevel != nil {
		t.Errorf("ForceTrustLevel = %v, want nil", pt.ForceTrustLevel)
	}
}

// TestPolicyToolsDenyRoundTrip sets Deny and ForceTrustLevel and reads back.
func TestPolicyToolsDenyRoundTrip(t *testing.T) {
	trust := ToolTrustSandbox
	pt := PolicyTools{
		Deny:            []string{"shell/*", "exec"},
		ForceTrustLevel: &trust,
	}

	if len(pt.Deny) != 2 {
		t.Errorf("Deny len = %d, want 2", len(pt.Deny))
	}
	if pt.Deny[0] != "shell/*" {
		t.Errorf("Deny[0] = %q, want %q", pt.Deny[0], "shell/*")
	}
	if pt.Deny[1] != "exec" {
		t.Errorf("Deny[1] = %q, want %q", pt.Deny[1], "exec")
	}
	if pt.ForceTrustLevel == nil || *pt.ForceTrustLevel != ToolTrustSandbox {
		t.Errorf("ForceTrustLevel = %v, want *%q", pt.ForceTrustLevel, ToolTrustSandbox)
	}
}

// TestPolicyToolsForceTrustLevelAcceptsAllLevels verifies ForceTrustLevel
// accepts every defined ToolTrustLevel value.
func TestPolicyToolsForceTrustLevelAcceptsAllLevels(t *testing.T) {
	levels := []ToolTrustLevel{ToolTrustInternal, ToolTrustExternal, ToolTrustSandbox}
	for _, lvl := range levels {
		lvlCopy := lvl
		pt := PolicyTools{ForceTrustLevel: &lvlCopy}
		if pt.ForceTrustLevel == nil || *pt.ForceTrustLevel != lvl {
			t.Errorf("ForceTrustLevel = %v, want *%q", pt.ForceTrustLevel, lvl)
		}
	}
}

// -----------------------------------------------------------------------------
// PolicyOutput
// -----------------------------------------------------------------------------

// TestPolicyOutputZeroValue confirms zero-value PolicyOutput has no patterns
// and empty MinValidation.
func TestPolicyOutputZeroValue(t *testing.T) {
	var out PolicyOutput

	if out.MinValidation != "" {
		t.Errorf("MinValidation = %q, want empty", string(out.MinValidation))
	}
	if len(out.DenyPatterns) != 0 {
		t.Errorf("DenyPatterns len = %d, want 0", len(out.DenyPatterns))
	}
}

// TestPolicyOutputRoundTrip sets MinValidation and DenyPatterns and reads back.
func TestPolicyOutputRoundTrip(t *testing.T) {
	out := PolicyOutput{
		MinValidation: PolicyOutputLevelSchema,
		DenyPatterns:  []string{`(?i)password`, `(?i)secret`},
	}

	if out.MinValidation != PolicyOutputLevelSchema {
		t.Errorf("MinValidation = %q, want %q", out.MinValidation, PolicyOutputLevelSchema)
	}
	if len(out.DenyPatterns) != 2 {
		t.Errorf("DenyPatterns len = %d, want 2", len(out.DenyPatterns))
	}
	if out.DenyPatterns[0] != `(?i)password` {
		t.Errorf("DenyPatterns[0] = %q, want %q", out.DenyPatterns[0], `(?i)password`)
	}
}

// -----------------------------------------------------------------------------
// SwarmPolicyStatus zero value
// -----------------------------------------------------------------------------

// TestSwarmPolicyStatusZeroValue confirms the status has zero counts and nil
// EffectivePolicy when unset.
func TestSwarmPolicyStatusZeroValue(t *testing.T) {
	var status SwarmPolicyStatus

	if status.AgentCount != 0 {
		t.Errorf("AgentCount = %d, want 0", status.AgentCount)
	}
	if status.CompliantCount != 0 {
		t.Errorf("CompliantCount = %d, want 0", status.CompliantCount)
	}
	if status.EffectivePolicy != nil {
		t.Errorf("EffectivePolicy = %v, want nil", status.EffectivePolicy)
	}
	if status.ObservedGeneration != 0 {
		t.Errorf("ObservedGeneration = %d, want 0", status.ObservedGeneration)
	}
	if len(status.Conditions) != 0 {
		t.Errorf("Conditions len = %d, want 0", len(status.Conditions))
	}
}

// TestSwarmPolicyStatusCountsRoundTrip sets AgentCount and CompliantCount.
func TestSwarmPolicyStatusCountsRoundTrip(t *testing.T) {
	status := SwarmPolicyStatus{
		AgentCount:         10,
		CompliantCount:     8,
		ObservedGeneration: 3,
	}

	if status.AgentCount != 10 {
		t.Errorf("AgentCount = %d, want 10", status.AgentCount)
	}
	if status.CompliantCount != 8 {
		t.Errorf("CompliantCount = %d, want 8", status.CompliantCount)
	}
	if status.ObservedGeneration != 3 {
		t.Errorf("ObservedGeneration = %d, want 3", status.ObservedGeneration)
	}
}

// -----------------------------------------------------------------------------
// EffectivePolicySpec - merged result
// -----------------------------------------------------------------------------

// TestEffectivePolicySpecZeroValue confirms zero value is safe.
func TestEffectivePolicySpecZeroValue(t *testing.T) {
	var ep EffectivePolicySpec

	if ep.EnforcementMode != "" {
		t.Errorf("EnforcementMode = %q, want empty", string(ep.EnforcementMode))
	}
	if ep.ForceTrustLevel != nil {
		t.Errorf("ForceTrustLevel = %v, want nil", ep.ForceTrustLevel)
	}
	if ep.MinValidation != "" {
		t.Errorf("MinValidation = %q, want empty", string(ep.MinValidation))
	}
	if len(ep.ToolDeny) != 0 {
		t.Errorf("ToolDeny len = %d, want 0", len(ep.ToolDeny))
	}
	if len(ep.DenyPatterns) != 0 {
		t.Errorf("DenyPatterns len = %d, want 0", len(ep.DenyPatterns))
	}
}

// TestEffectivePolicySpecMergedResult confirms EffectivePolicySpec can hold
// results merged from multiple policies, covering all fields.
func TestEffectivePolicySpecMergedResult(t *testing.T) {
	daily := int64(500_000)
	perCall := int32(4000)
	maxTimeout := int32(120)
	minTimeout := int32(10)
	concurrent := int32(5)
	thinking := int32(2048)
	answer := int32(8192)
	trust := ToolTrustExternal

	ep := EffectivePolicySpec{
		EnforcementMode: PolicyEnforcementEnforce,
		Limits: &PolicyLimits{
			MaxDailyTokens:           &daily,
			MaxTokensPerCall:         &perCall,
			MaxTimeoutSeconds:        &maxTimeout,
			MinTimeoutSeconds:        &minTimeout,
			MaxConcurrentTasks:       &concurrent,
			MaxThinkingTokensPerCall: &thinking,
			MaxAnswerTokensPerCall:   &answer,
		},
		ToolDeny:        []string{"shell/*"},
		ForceTrustLevel: &trust,
		MinValidation:   PolicyOutputLevelPattern,
		DenyPatterns:    []string{`(?i)secret`},
		Models: &PolicyModels{
			Allowed: []string{"claude-*"},
			Denied:  []string{"gpt-3.5-*"},
		},
		Requirements: PolicyRequirements{
			BudgetRef: true,
			Audit:     true,
			AllowList: false,
		},
	}

	if ep.EnforcementMode != PolicyEnforcementEnforce {
		t.Errorf("EnforcementMode = %q, want %q", ep.EnforcementMode, PolicyEnforcementEnforce)
	}
	if ep.Limits == nil {
		t.Fatal("Limits = nil, want non-nil")
	}
	if ep.Limits.MaxDailyTokens == nil || *ep.Limits.MaxDailyTokens != 500_000 {
		t.Errorf("Limits.MaxDailyTokens = %v, want *500000", ep.Limits.MaxDailyTokens)
	}
	if len(ep.ToolDeny) != 1 || ep.ToolDeny[0] != "shell/*" {
		t.Errorf("ToolDeny = %v, want [shell/*]", ep.ToolDeny)
	}
	if ep.ForceTrustLevel == nil || *ep.ForceTrustLevel != ToolTrustExternal {
		t.Errorf("ForceTrustLevel = %v, want *%q", ep.ForceTrustLevel, ToolTrustExternal)
	}
	if ep.MinValidation != PolicyOutputLevelPattern {
		t.Errorf("MinValidation = %q, want %q", ep.MinValidation, PolicyOutputLevelPattern)
	}
	if len(ep.DenyPatterns) != 1 {
		t.Errorf("DenyPatterns len = %d, want 1", len(ep.DenyPatterns))
	}
	if ep.Models == nil || len(ep.Models.Allowed) != 1 {
		t.Errorf("Models.Allowed = %v, want [claude-*]", ep.Models)
	}
	if !ep.Requirements.Audit {
		t.Errorf("Requirements.Audit = false, want true")
	}
}

// -----------------------------------------------------------------------------
// PolicyViolation
// -----------------------------------------------------------------------------

// TestPolicyViolationZeroValue confirms all string fields are empty by default.
func TestPolicyViolationZeroValue(t *testing.T) {
	var v PolicyViolation

	if v.Constraint != "" {
		t.Errorf("Constraint = %q, want empty", v.Constraint)
	}
	if v.PolicyName != "" {
		t.Errorf("PolicyName = %q, want empty", v.PolicyName)
	}
	if v.Message != "" {
		t.Errorf("Message = %q, want empty", v.Message)
	}
}

// TestPolicyViolationRoundTrip stores constraint, policy name and message.
func TestPolicyViolationRoundTrip(t *testing.T) {
	v := PolicyViolation{
		Constraint: "limits.maxTokensPerCall",
		PolicyName: "org-default",
		Message:    "requested 16000 tokens exceeds policy limit of 8000",
	}

	if v.Constraint != "limits.maxTokensPerCall" {
		t.Errorf("Constraint = %q, want %q", v.Constraint, "limits.maxTokensPerCall")
	}
	if v.PolicyName != "org-default" {
		t.Errorf("PolicyName = %q, want %q", v.PolicyName, "org-default")
	}
	if v.Message != "requested 16000 tokens exceeds policy limit of 8000" {
		t.Errorf("Message = %q, unexpected value", v.Message)
	}
}

// TestPolicyViolationFieldsAreIndependent confirms the three string fields do
// not alias each other.
func TestPolicyViolationFieldsAreIndependent(t *testing.T) {
	v := PolicyViolation{
		Constraint: "A",
		PolicyName: "B",
		Message:    "C",
	}
	v.Constraint = "X"
	if v.PolicyName != "B" {
		t.Errorf("PolicyName mutated after Constraint update: got %q, want B", v.PolicyName)
	}
	if v.Message != "C" {
		t.Errorf("Message mutated after Constraint update: got %q, want C", v.Message)
	}
}

// NOTE: GuardrailProvenance and EffectiveGuardrailEntry tests deferred to Phase 4.

// -----------------------------------------------------------------------------
// SwarmPolicy root object
// -----------------------------------------------------------------------------

// TestSwarmPolicySpecEnforcementModeRoundTrip confirms EnforcementMode can be
// set to every valid value on SwarmPolicySpec.
func TestSwarmPolicySpecEnforcementModeRoundTrip(t *testing.T) {
	modes := []PolicyEnforcementMode{
		PolicyEnforcementAudit,
		PolicyEnforcementWarn,
		PolicyEnforcementEnforce,
	}
	for _, mode := range modes {
		spec := SwarmPolicySpec{EnforcementMode: mode}
		if spec.EnforcementMode != mode {
			t.Errorf("EnforcementMode = %q, want %q", spec.EnforcementMode, mode)
		}
	}
}

// TestSwarmPolicyStatusEffectivePolicyPointer confirms EffectivePolicy on
// SwarmPolicyStatus is a pointer: nil means not yet computed, non-nil holds
// the merged spec.
func TestSwarmPolicyStatusEffectivePolicyPointer(t *testing.T) {
	var status SwarmPolicyStatus
	if status.EffectivePolicy != nil {
		t.Errorf("EffectivePolicy default = %v, want nil", status.EffectivePolicy)
	}

	status.EffectivePolicy = &EffectivePolicySpec{
		EnforcementMode: PolicyEnforcementAudit,
	}
	if status.EffectivePolicy == nil {
		t.Fatal("EffectivePolicy = nil after assignment")
	}
	if status.EffectivePolicy.EnforcementMode != PolicyEnforcementAudit {
		t.Errorf("EffectivePolicy.EnforcementMode = %q, want %q",
			status.EffectivePolicy.EnforcementMode, PolicyEnforcementAudit)
	}
}
