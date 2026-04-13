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
	"slices"
	"sort"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kubeswarmv1alpha1 "github.com/kubeswarm/kubeswarm/api/v1alpha1"
)

const testModelGPT4o = "gpt-4o"

// ptr helpers - local to this test file

//go:fix inline
func ptrInt64(v int64) *int64 { return new(v) }

//go:fix inline
func ptrInt32(v int32) *int32 { return new(v) }

//go:fix inline
func ptrTrust(v kubeswarmv1alpha1.ToolTrustLevel) *kubeswarmv1alpha1.ToolTrustLevel { return new(v) }

// makePolicy builds a minimal SwarmPolicy with the given name and spec.
func makePolicy(name string, spec kubeswarmv1alpha1.SwarmPolicySpec) kubeswarmv1alpha1.SwarmPolicy {
	return kubeswarmv1alpha1.SwarmPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "default",
		},
		Spec: spec,
	}
}

// makeAgent builds a minimal SwarmAgent with the given name.
func makeAgent(name string) *kubeswarmv1alpha1.SwarmAgent {
	return &kubeswarmv1alpha1.SwarmAgent{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "default",
		},
		Spec: kubeswarmv1alpha1.SwarmAgentSpec{
			Model: "claude-sonnet-4-6",
		},
	}
}

// consistsOf checks that got and want contain the same elements (order-independent, no duplicates unaccounted).
func consistsOf(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("expected %v to consist of %v (len %d != %d)", got, want, len(got), len(want))
	}
	sortedGot := make([]string, len(got))
	copy(sortedGot, got)
	sort.Strings(sortedGot)
	sortedWant := make([]string, len(want))
	copy(sortedWant, want)
	sort.Strings(sortedWant)
	for i := range sortedGot {
		if sortedGot[i] != sortedWant[i] {
			t.Fatalf("expected %v to consist of %v", got, want)
		}
	}
}

func TestMergePolicies(t *testing.T) {
	t.Run("returns nil effective policy for an empty list", func(t *testing.T) {
		effective, conflicts := MergePolicies(nil)
		requireNil(t, effective)
		requireLen(t, conflicts, 0)
	})

	t.Run("returns nil effective policy for an empty slice", func(t *testing.T) {
		effective, conflicts := MergePolicies([]kubeswarmv1alpha1.SwarmPolicy{})
		requireNil(t, effective)
		requireLen(t, conflicts, 0)
	})

	t.Run("returns the single policy's values unchanged", func(t *testing.T) {
		maxDaily := int64(100_000)
		pol := makePolicy("solo", kubeswarmv1alpha1.SwarmPolicySpec{
			EnforcementMode: kubeswarmv1alpha1.PolicyEnforcementEnforce,
			Limits: &kubeswarmv1alpha1.PolicyLimits{
				MaxDailyTokens: new(maxDaily),
			},
		})
		effective, conflicts := MergePolicies([]kubeswarmv1alpha1.SwarmPolicy{pol})
		requireLen(t, conflicts, 0)
		if effective == nil {
			t.Fatal("expected non-nil effective policy")
		}
		requireEqual(t, effective.EnforcementMode, kubeswarmv1alpha1.PolicyEnforcementEnforce)
		if effective.Limits == nil {
			t.Fatal("expected non-nil Limits")
		}
		requireEqual(t, *effective.Limits.MaxDailyTokens, maxDaily)
	})

	t.Run("numeric ceilings - lowest non-nil wins", func(t *testing.T) {
		t.Run("picks the lower MaxDailyTokens across two policies", func(t *testing.T) {
			high := makePolicy("high", kubeswarmv1alpha1.SwarmPolicySpec{
				Limits: &kubeswarmv1alpha1.PolicyLimits{MaxDailyTokens: ptrInt64(500_000)},
			})
			low := makePolicy("low", kubeswarmv1alpha1.SwarmPolicySpec{
				Limits: &kubeswarmv1alpha1.PolicyLimits{MaxDailyTokens: ptrInt64(100_000)},
			})
			effective, conflicts := MergePolicies([]kubeswarmv1alpha1.SwarmPolicy{high, low})
			requireLen(t, conflicts, 0)
			requireEqual(t, *effective.Limits.MaxDailyTokens, int64(100_000))
		})

		t.Run("picks the lower MaxTokensPerCall across two policies", func(t *testing.T) {
			a := makePolicy("a", kubeswarmv1alpha1.SwarmPolicySpec{
				Limits: &kubeswarmv1alpha1.PolicyLimits{MaxTokensPerCall: ptrInt32(8000)},
			})
			b := makePolicy("b", kubeswarmv1alpha1.SwarmPolicySpec{
				Limits: &kubeswarmv1alpha1.PolicyLimits{MaxTokensPerCall: ptrInt32(4000)},
			})
			effective, _ := MergePolicies([]kubeswarmv1alpha1.SwarmPolicy{a, b})
			requireEqual(t, *effective.Limits.MaxTokensPerCall, int32(4000))
		})

		t.Run("ignores nil MaxDailyTokens and uses the non-nil value", func(t *testing.T) {
			withLimit := makePolicy("with-limit", kubeswarmv1alpha1.SwarmPolicySpec{
				Limits: &kubeswarmv1alpha1.PolicyLimits{MaxDailyTokens: ptrInt64(200_000)},
			})
			noLimit := makePolicy("no-limit", kubeswarmv1alpha1.SwarmPolicySpec{
				Limits: &kubeswarmv1alpha1.PolicyLimits{MaxTokensPerCall: ptrInt32(2000)},
			})
			effective, _ := MergePolicies([]kubeswarmv1alpha1.SwarmPolicy{withLimit, noLimit})
			requireEqual(t, *effective.Limits.MaxDailyTokens, int64(200_000))
		})
	})

	t.Run("numeric floors - highest non-nil wins", func(t *testing.T) {
		t.Run("picks the higher MinTimeoutSeconds across two policies", func(t *testing.T) {
			low := makePolicy("low-floor", kubeswarmv1alpha1.SwarmPolicySpec{
				Limits: &kubeswarmv1alpha1.PolicyLimits{MinTimeoutSeconds: ptrInt32(30)},
			})
			high := makePolicy("high-floor", kubeswarmv1alpha1.SwarmPolicySpec{
				Limits: &kubeswarmv1alpha1.PolicyLimits{MinTimeoutSeconds: ptrInt32(120)},
			})
			effective, conflicts := MergePolicies([]kubeswarmv1alpha1.SwarmPolicy{low, high})
			requireLen(t, conflicts, 0)
			requireEqual(t, *effective.Limits.MinTimeoutSeconds, int32(120))
		})

		t.Run("ignores nil MinTimeoutSeconds and uses the non-nil value", func(t *testing.T) {
			withFloor := makePolicy("with-floor", kubeswarmv1alpha1.SwarmPolicySpec{
				Limits: &kubeswarmv1alpha1.PolicyLimits{MinTimeoutSeconds: ptrInt32(60)},
			})
			noFloor := makePolicy("no-floor", kubeswarmv1alpha1.SwarmPolicySpec{
				Limits: &kubeswarmv1alpha1.PolicyLimits{MaxDailyTokens: ptrInt64(50_000)},
			})
			effective, _ := MergePolicies([]kubeswarmv1alpha1.SwarmPolicy{withFloor, noFloor})
			requireEqual(t, *effective.Limits.MinTimeoutSeconds, int32(60))
		})
	})

	t.Run("deny lists - union of all", func(t *testing.T) {
		t.Run("unions tool deny lists from two policies", func(t *testing.T) {
			a := makePolicy("a", kubeswarmv1alpha1.SwarmPolicySpec{
				Tools: &kubeswarmv1alpha1.PolicyTools{Deny: []string{"shell/*"}},
			})
			b := makePolicy("b", kubeswarmv1alpha1.SwarmPolicySpec{
				Tools: &kubeswarmv1alpha1.PolicyTools{Deny: []string{"filesystem/write_file"}},
			})
			effective, _ := MergePolicies([]kubeswarmv1alpha1.SwarmPolicy{a, b})
			consistsOf(t, effective.ToolDeny, []string{"shell/*", "filesystem/write_file"})
		})

		t.Run("deduplicates tool deny entries that appear in multiple policies", func(t *testing.T) {
			a := makePolicy("a", kubeswarmv1alpha1.SwarmPolicySpec{
				Tools: &kubeswarmv1alpha1.PolicyTools{Deny: []string{"shell/*", "filesystem/delete"}},
			})
			b := makePolicy("b", kubeswarmv1alpha1.SwarmPolicySpec{
				Tools: &kubeswarmv1alpha1.PolicyTools{Deny: []string{"shell/*"}},
			})
			effective, _ := MergePolicies([]kubeswarmv1alpha1.SwarmPolicy{a, b})
			consistsOf(t, effective.ToolDeny, []string{"shell/*", "filesystem/delete"})
		})

		t.Run("unions output deny patterns from two policies", func(t *testing.T) {
			a := makePolicy("a", kubeswarmv1alpha1.SwarmPolicySpec{
				Output: &kubeswarmv1alpha1.PolicyOutput{DenyPatterns: []string{"(?i)password"}},
			})
			b := makePolicy("b", kubeswarmv1alpha1.SwarmPolicySpec{
				Output: &kubeswarmv1alpha1.PolicyOutput{DenyPatterns: []string{"(?i)secret"}},
			})
			effective, _ := MergePolicies([]kubeswarmv1alpha1.SwarmPolicy{a, b})
			consistsOf(t, effective.DenyPatterns, []string{"(?i)password", "(?i)secret"})
		})
	})

	t.Run("boolean requirements - logical OR", func(t *testing.T) {
		t.Run("sets BudgetRef to true when any policy requires it", func(t *testing.T) {
			a := makePolicy("a", kubeswarmv1alpha1.SwarmPolicySpec{
				Requirements: kubeswarmv1alpha1.PolicyRequirements{BudgetRef: false},
				Limits:       &kubeswarmv1alpha1.PolicyLimits{MaxDailyTokens: ptrInt64(1)},
			})
			b := makePolicy("b", kubeswarmv1alpha1.SwarmPolicySpec{
				Requirements: kubeswarmv1alpha1.PolicyRequirements{BudgetRef: true},
			})
			effective, _ := MergePolicies([]kubeswarmv1alpha1.SwarmPolicy{a, b})
			requireTrue(t, effective.Requirements.BudgetRef)
		})

		t.Run("sets Audit to true when any policy requires it", func(t *testing.T) {
			a := makePolicy("a", kubeswarmv1alpha1.SwarmPolicySpec{
				Requirements: kubeswarmv1alpha1.PolicyRequirements{Audit: true},
			})
			b := makePolicy("b", kubeswarmv1alpha1.SwarmPolicySpec{
				Requirements: kubeswarmv1alpha1.PolicyRequirements{Audit: false},
				Limits:       &kubeswarmv1alpha1.PolicyLimits{MaxDailyTokens: ptrInt64(1)},
			})
			effective, _ := MergePolicies([]kubeswarmv1alpha1.SwarmPolicy{a, b})
			requireTrue(t, effective.Requirements.Audit)
		})

		t.Run("leaves requirements false when no policy sets them", func(t *testing.T) {
			a := makePolicy("a", kubeswarmv1alpha1.SwarmPolicySpec{
				Limits: &kubeswarmv1alpha1.PolicyLimits{MaxDailyTokens: ptrInt64(100_000)},
			})
			effective, _ := MergePolicies([]kubeswarmv1alpha1.SwarmPolicy{a})
			requireFalse(t, effective.Requirements.BudgetRef)
			requireFalse(t, effective.Requirements.Audit)
			requireFalse(t, effective.Requirements.AllowList)
		})
	})

	t.Run("trust level - strictest wins (sandbox > external > internal)", func(t *testing.T) {
		for _, tc := range []struct {
			name     string
			a, b     kubeswarmv1alpha1.ToolTrustLevel
			expected kubeswarmv1alpha1.ToolTrustLevel
		}{
			{"sandbox beats external", kubeswarmv1alpha1.ToolTrustSandbox, kubeswarmv1alpha1.ToolTrustExternal, kubeswarmv1alpha1.ToolTrustSandbox},
			{"sandbox beats internal", kubeswarmv1alpha1.ToolTrustSandbox, kubeswarmv1alpha1.ToolTrustInternal, kubeswarmv1alpha1.ToolTrustSandbox},
			{"external beats internal", kubeswarmv1alpha1.ToolTrustExternal, kubeswarmv1alpha1.ToolTrustInternal, kubeswarmv1alpha1.ToolTrustExternal},
			{"same level stays the same", kubeswarmv1alpha1.ToolTrustExternal, kubeswarmv1alpha1.ToolTrustExternal, kubeswarmv1alpha1.ToolTrustExternal},
		} {
			t.Run(tc.name, func(t *testing.T) {
				polA := makePolicy("a", kubeswarmv1alpha1.SwarmPolicySpec{
					Tools: &kubeswarmv1alpha1.PolicyTools{ForceTrustLevel: new(tc.a)},
				})
				polB := makePolicy("b", kubeswarmv1alpha1.SwarmPolicySpec{
					Tools: &kubeswarmv1alpha1.PolicyTools{ForceTrustLevel: new(tc.b)},
				})
				effective, _ := MergePolicies([]kubeswarmv1alpha1.SwarmPolicy{polA, polB})
				if effective.ForceTrustLevel == nil {
					t.Fatal("expected non-nil ForceTrustLevel")
				}
				requireEqual(t, *effective.ForceTrustLevel, tc.expected)
			})
		}

		t.Run("uses the only non-nil trust level when the other policy has none", func(t *testing.T) {
			withTrust := makePolicy("with-trust", kubeswarmv1alpha1.SwarmPolicySpec{
				Tools: &kubeswarmv1alpha1.PolicyTools{ForceTrustLevel: ptrTrust(kubeswarmv1alpha1.ToolTrustExternal)},
			})
			noTrust := makePolicy("no-trust", kubeswarmv1alpha1.SwarmPolicySpec{
				Limits: &kubeswarmv1alpha1.PolicyLimits{MaxDailyTokens: ptrInt64(1)},
			})
			effective, _ := MergePolicies([]kubeswarmv1alpha1.SwarmPolicy{withTrust, noTrust})
			if effective.ForceTrustLevel == nil {
				t.Fatal("expected non-nil ForceTrustLevel")
			}
			requireEqual(t, *effective.ForceTrustLevel, kubeswarmv1alpha1.ToolTrustExternal)
		})
	})

	t.Run("validation level - strictest wins (semantic > schema > pattern > none)", func(t *testing.T) {
		for _, tc := range []struct {
			name     string
			a, b     kubeswarmv1alpha1.PolicyOutputLevel
			expected kubeswarmv1alpha1.PolicyOutputLevel
		}{
			{"semantic beats schema", kubeswarmv1alpha1.PolicyOutputLevelSemantic, kubeswarmv1alpha1.PolicyOutputLevelSchema, kubeswarmv1alpha1.PolicyOutputLevelSemantic},
			{"semantic beats pattern", kubeswarmv1alpha1.PolicyOutputLevelSemantic, kubeswarmv1alpha1.PolicyOutputLevelPattern, kubeswarmv1alpha1.PolicyOutputLevelSemantic},
			{"semantic beats none", kubeswarmv1alpha1.PolicyOutputLevelSemantic, kubeswarmv1alpha1.PolicyOutputLevelNone, kubeswarmv1alpha1.PolicyOutputLevelSemantic},
			{"schema beats pattern", kubeswarmv1alpha1.PolicyOutputLevelSchema, kubeswarmv1alpha1.PolicyOutputLevelPattern, kubeswarmv1alpha1.PolicyOutputLevelSchema},
			{"schema beats none", kubeswarmv1alpha1.PolicyOutputLevelSchema, kubeswarmv1alpha1.PolicyOutputLevelNone, kubeswarmv1alpha1.PolicyOutputLevelSchema},
			{"pattern beats none", kubeswarmv1alpha1.PolicyOutputLevelPattern, kubeswarmv1alpha1.PolicyOutputLevelNone, kubeswarmv1alpha1.PolicyOutputLevelPattern},
			{"same level stays the same", kubeswarmv1alpha1.PolicyOutputLevelSchema, kubeswarmv1alpha1.PolicyOutputLevelSchema, kubeswarmv1alpha1.PolicyOutputLevelSchema},
		} {
			t.Run(tc.name, func(t *testing.T) {
				polA := makePolicy("a", kubeswarmv1alpha1.SwarmPolicySpec{
					Output: &kubeswarmv1alpha1.PolicyOutput{MinValidation: tc.a},
				})
				polB := makePolicy("b", kubeswarmv1alpha1.SwarmPolicySpec{
					Output: &kubeswarmv1alpha1.PolicyOutput{MinValidation: tc.b},
				})
				effective, _ := MergePolicies([]kubeswarmv1alpha1.SwarmPolicy{polA, polB})
				requireEqual(t, effective.MinValidation, tc.expected)
			})
		}
	})

	t.Run("enforcement mode - strictest wins (Enforce > Warn > Audit)", func(t *testing.T) {
		for _, tc := range []struct {
			name     string
			a, b     kubeswarmv1alpha1.PolicyEnforcementMode
			expected kubeswarmv1alpha1.PolicyEnforcementMode
		}{
			{"Enforce beats Warn", kubeswarmv1alpha1.PolicyEnforcementEnforce, kubeswarmv1alpha1.PolicyEnforcementWarn, kubeswarmv1alpha1.PolicyEnforcementEnforce},
			{"Enforce beats Audit", kubeswarmv1alpha1.PolicyEnforcementEnforce, kubeswarmv1alpha1.PolicyEnforcementAudit, kubeswarmv1alpha1.PolicyEnforcementEnforce},
			{"Warn beats Audit", kubeswarmv1alpha1.PolicyEnforcementWarn, kubeswarmv1alpha1.PolicyEnforcementAudit, kubeswarmv1alpha1.PolicyEnforcementWarn},
			{"Audit stays Audit when both are Audit", kubeswarmv1alpha1.PolicyEnforcementAudit, kubeswarmv1alpha1.PolicyEnforcementAudit, kubeswarmv1alpha1.PolicyEnforcementAudit},
		} {
			t.Run(tc.name, func(t *testing.T) {
				polA := makePolicy("a", kubeswarmv1alpha1.SwarmPolicySpec{
					EnforcementMode: tc.a,
					Limits:          &kubeswarmv1alpha1.PolicyLimits{MaxDailyTokens: ptrInt64(1)},
				})
				polB := makePolicy("b", kubeswarmv1alpha1.SwarmPolicySpec{
					EnforcementMode: tc.b,
					Limits:          &kubeswarmv1alpha1.PolicyLimits{MaxDailyTokens: ptrInt64(2)},
				})
				effective, _ := MergePolicies([]kubeswarmv1alpha1.SwarmPolicy{polA, polB})
				requireEqual(t, effective.EnforcementMode, tc.expected)
			})
		}
	})

	t.Run("conflict detection", func(t *testing.T) {
		t.Run("reports a conflict when MinTimeoutSeconds > MaxTimeoutSeconds across policies", func(t *testing.T) {
			ceiling := makePolicy("ceiling", kubeswarmv1alpha1.SwarmPolicySpec{
				Limits: &kubeswarmv1alpha1.PolicyLimits{MaxTimeoutSeconds: ptrInt32(60)},
			})
			floor := makePolicy("floor", kubeswarmv1alpha1.SwarmPolicySpec{
				Limits: &kubeswarmv1alpha1.PolicyLimits{MinTimeoutSeconds: ptrInt32(120)},
			})
			_, conflicts := MergePolicies([]kubeswarmv1alpha1.SwarmPolicy{ceiling, floor})
			requireLen(t, conflicts, 1)
			requireEqual(t, conflicts[0].Field, "limits.timeoutSeconds")
			requireNotEmpty(t, conflicts[0].PolicyA)
			requireNotEmpty(t, conflicts[0].PolicyB)
			requireNotEmpty(t, conflicts[0].Message)
		})

		t.Run("does not report a conflict when MinTimeoutSeconds equals MaxTimeoutSeconds", func(t *testing.T) {
			ceiling := makePolicy("ceiling", kubeswarmv1alpha1.SwarmPolicySpec{
				Limits: &kubeswarmv1alpha1.PolicyLimits{MaxTimeoutSeconds: ptrInt32(60)},
			})
			floor := makePolicy("floor", kubeswarmv1alpha1.SwarmPolicySpec{
				Limits: &kubeswarmv1alpha1.PolicyLimits{MinTimeoutSeconds: ptrInt32(60)},
			})
			_, conflicts := MergePolicies([]kubeswarmv1alpha1.SwarmPolicy{ceiling, floor})
			requireLen(t, conflicts, 0)
		})
	})

	t.Run("model lists", func(t *testing.T) {
		t.Run("intersects allowed model lists across two policies", func(t *testing.T) {
			a := makePolicy("a", kubeswarmv1alpha1.SwarmPolicySpec{
				Models: &kubeswarmv1alpha1.PolicyModels{Allowed: []string{"claude-sonnet-4-6", "claude-haiku-3"}},
			})
			b := makePolicy("b", kubeswarmv1alpha1.SwarmPolicySpec{
				Models: &kubeswarmv1alpha1.PolicyModels{Allowed: []string{"claude-sonnet-4-6", testModelGPT4o}},
			})
			effective, _ := MergePolicies([]kubeswarmv1alpha1.SwarmPolicy{a, b})
			if effective.Models == nil {
				t.Fatal("expected non-nil Models")
			}
			consistsOf(t, effective.Models.Allowed, []string{"claude-sonnet-4-6"})
		})

		t.Run("results in an empty allowed list when the intersection is empty", func(t *testing.T) {
			a := makePolicy("a", kubeswarmv1alpha1.SwarmPolicySpec{
				Models: &kubeswarmv1alpha1.PolicyModels{Allowed: []string{"claude-sonnet-4-6"}},
			})
			b := makePolicy("b", kubeswarmv1alpha1.SwarmPolicySpec{
				Models: &kubeswarmv1alpha1.PolicyModels{Allowed: []string{testModelGPT4o}},
			})
			effective, _ := MergePolicies([]kubeswarmv1alpha1.SwarmPolicy{a, b})
			if effective.Models == nil {
				t.Fatal("expected non-nil Models")
			}
			requireLen(t, effective.Models.Allowed, 0)
		})

		t.Run("unions denied model lists across two policies", func(t *testing.T) {
			a := makePolicy("a", kubeswarmv1alpha1.SwarmPolicySpec{
				Models: &kubeswarmv1alpha1.PolicyModels{Denied: []string{testModelGPT4o}},
			})
			b := makePolicy("b", kubeswarmv1alpha1.SwarmPolicySpec{
				Models: &kubeswarmv1alpha1.PolicyModels{Denied: []string{"o3-mini"}},
			})
			effective, _ := MergePolicies([]kubeswarmv1alpha1.SwarmPolicy{a, b})
			if effective.Models == nil {
				t.Fatal("expected non-nil Models")
			}
			consistsOf(t, effective.Models.Denied, []string{testModelGPT4o, "o3-mini"})
		})

		t.Run("deduplicates denied model entries across policies", func(t *testing.T) {
			a := makePolicy("a", kubeswarmv1alpha1.SwarmPolicySpec{
				Models: &kubeswarmv1alpha1.PolicyModels{Denied: []string{testModelGPT4o, "o3-mini"}},
			})
			b := makePolicy("b", kubeswarmv1alpha1.SwarmPolicySpec{
				Models: &kubeswarmv1alpha1.PolicyModels{Denied: []string{testModelGPT4o}},
			})
			effective, _ := MergePolicies([]kubeswarmv1alpha1.SwarmPolicy{a, b})
			consistsOf(t, effective.Models.Denied, []string{testModelGPT4o, "o3-mini"})
		})

		t.Run("treats a policy with no Models block as no constraint on model lists", func(t *testing.T) {
			withModels := makePolicy("with-models", kubeswarmv1alpha1.SwarmPolicySpec{
				Models: &kubeswarmv1alpha1.PolicyModels{Allowed: []string{"claude-sonnet-4-6"}},
			})
			noModels := makePolicy("no-models", kubeswarmv1alpha1.SwarmPolicySpec{
				Limits: &kubeswarmv1alpha1.PolicyLimits{MaxDailyTokens: ptrInt64(50_000)},
			})
			effective, _ := MergePolicies([]kubeswarmv1alpha1.SwarmPolicy{withModels, noModels})
			consistsOf(t, effective.Models.Allowed, []string{"claude-sonnet-4-6"})
		})
	})

	t.Run("three policy merge", func(t *testing.T) {
		t.Run("correctly merges three policies applying all rules simultaneously", func(t *testing.T) {
			policyA := makePolicy("a", kubeswarmv1alpha1.SwarmPolicySpec{
				EnforcementMode: kubeswarmv1alpha1.PolicyEnforcementAudit,
				Limits: &kubeswarmv1alpha1.PolicyLimits{
					MaxDailyTokens:    ptrInt64(300_000),
					MinTimeoutSeconds: ptrInt32(30),
				},
				Tools: &kubeswarmv1alpha1.PolicyTools{
					Deny:            []string{"shell/*"},
					ForceTrustLevel: ptrTrust(kubeswarmv1alpha1.ToolTrustInternal),
				},
				Requirements: kubeswarmv1alpha1.PolicyRequirements{Audit: false},
			})
			policyB := makePolicy("b", kubeswarmv1alpha1.SwarmPolicySpec{
				EnforcementMode: kubeswarmv1alpha1.PolicyEnforcementWarn,
				Limits: &kubeswarmv1alpha1.PolicyLimits{
					MaxDailyTokens:    ptrInt64(100_000),
					MinTimeoutSeconds: ptrInt32(60),
				},
				Tools: &kubeswarmv1alpha1.PolicyTools{
					Deny:            []string{"filesystem/delete"},
					ForceTrustLevel: ptrTrust(kubeswarmv1alpha1.ToolTrustExternal),
				},
				Requirements: kubeswarmv1alpha1.PolicyRequirements{Audit: true},
			})
			policyC := makePolicy("c", kubeswarmv1alpha1.SwarmPolicySpec{
				EnforcementMode: kubeswarmv1alpha1.PolicyEnforcementEnforce,
				Limits: &kubeswarmv1alpha1.PolicyLimits{
					MaxDailyTokens:    ptrInt64(200_000),
					MinTimeoutSeconds: ptrInt32(10),
				},
				Tools: &kubeswarmv1alpha1.PolicyTools{
					Deny:            []string{"network/*"},
					ForceTrustLevel: ptrTrust(kubeswarmv1alpha1.ToolTrustSandbox),
				},
				Requirements: kubeswarmv1alpha1.PolicyRequirements{BudgetRef: true},
			})

			effective, conflicts := MergePolicies([]kubeswarmv1alpha1.SwarmPolicy{policyA, policyB, policyC})
			requireLen(t, conflicts, 0)

			// Lowest ceiling wins: 100_000
			requireEqual(t, *effective.Limits.MaxDailyTokens, int64(100_000))
			// Highest floor wins: 60
			requireEqual(t, *effective.Limits.MinTimeoutSeconds, int32(60))
			// Tool deny = union
			consistsOf(t, effective.ToolDeny, []string{"shell/*", "filesystem/delete", "network/*"})
			// Strictest trust level
			requireEqual(t, *effective.ForceTrustLevel, kubeswarmv1alpha1.ToolTrustSandbox)
			// Strictest enforcement
			requireEqual(t, effective.EnforcementMode, kubeswarmv1alpha1.PolicyEnforcementEnforce)
			// OR requirements
			requireTrue(t, effective.Requirements.Audit)
			requireTrue(t, effective.Requirements.BudgetRef)
		})
	})

	t.Run("nil sub-structs are skipped", func(t *testing.T) {
		t.Run("ignores a policy with no Limits block for limit merging", func(t *testing.T) {
			withLimits := makePolicy("with-limits", kubeswarmv1alpha1.SwarmPolicySpec{
				Limits: &kubeswarmv1alpha1.PolicyLimits{MaxDailyTokens: ptrInt64(50_000)},
			})
			noLimits := makePolicy("no-limits", kubeswarmv1alpha1.SwarmPolicySpec{
				Requirements: kubeswarmv1alpha1.PolicyRequirements{Audit: true},
			})
			effective, _ := MergePolicies([]kubeswarmv1alpha1.SwarmPolicy{withLimits, noLimits})
			if effective.Limits == nil {
				t.Fatal("expected non-nil Limits")
			}
			requireEqual(t, *effective.Limits.MaxDailyTokens, int64(50_000))
		})

		t.Run("produces nil Limits in effective policy when no policy defines any limits", func(t *testing.T) {
			a := makePolicy("a", kubeswarmv1alpha1.SwarmPolicySpec{
				Requirements: kubeswarmv1alpha1.PolicyRequirements{Audit: true},
			})
			effective, _ := MergePolicies([]kubeswarmv1alpha1.SwarmPolicy{a})
			requireNil(t, effective.Limits)
		})
	})
}

func TestEvaluateAgentCompliance(t *testing.T) {
	t.Run("returns violations for every requirement when the agent has no guardrails", func(t *testing.T) {
		agent := makeAgent("bare-agent")

		effective := &kubeswarmv1alpha1.EffectivePolicySpec{
			Requirements: kubeswarmv1alpha1.PolicyRequirements{
				BudgetRef: true,
				Audit:     false,
				AllowList: false,
			},
		}
		violations := EvaluateAgentCompliance(agent, effective)
		requireLen(t, violations, 1)
		requireEqual(t, violations[0].Constraint, "requirements.budgetRef")
	})

	t.Run("returns no violations when agent is within all limits", func(t *testing.T) {
		agent := makeAgent("compliant")
		agent.Spec.Guardrails = &kubeswarmv1alpha1.AgentGuardrails{
			Limits: &kubeswarmv1alpha1.GuardrailLimits{
				DailyTokens:   50_000,
				TokensPerCall: 2000,
			},
		}

		effective := &kubeswarmv1alpha1.EffectivePolicySpec{
			Limits: &kubeswarmv1alpha1.PolicyLimits{
				MaxDailyTokens:   ptrInt64(100_000),
				MaxTokensPerCall: ptrInt32(4000),
			},
		}
		violations := EvaluateAgentCompliance(agent, effective)
		requireLen(t, violations, 0)
	})

	t.Run("returns a violation when agent DailyTokens exceeds MaxDailyTokens", func(t *testing.T) {
		agent := makeAgent("over-budget")
		agent.Spec.Guardrails = &kubeswarmv1alpha1.AgentGuardrails{
			Limits: &kubeswarmv1alpha1.GuardrailLimits{
				DailyTokens: 200_000,
			},
		}

		effective := &kubeswarmv1alpha1.EffectivePolicySpec{
			Limits: &kubeswarmv1alpha1.PolicyLimits{
				MaxDailyTokens: ptrInt64(100_000),
			},
		}
		violations := EvaluateAgentCompliance(agent, effective)
		requireLen(t, violations, 1)
		requireEqual(t, violations[0].Constraint, "limits.dailyTokens")
	})

	t.Run("returns a violation when budgetRef is required but agent has none", func(t *testing.T) {
		agent := makeAgent("no-budget")
		agent.Spec.Guardrails = &kubeswarmv1alpha1.AgentGuardrails{
			Limits: &kubeswarmv1alpha1.GuardrailLimits{DailyTokens: 1000},
		}

		effective := &kubeswarmv1alpha1.EffectivePolicySpec{
			Requirements: kubeswarmv1alpha1.PolicyRequirements{BudgetRef: true},
		}
		violations := EvaluateAgentCompliance(agent, effective)
		if len(violations) == 0 {
			t.Fatal("expected at least one violation")
		}
		constraints := make([]string, len(violations))
		for i, v := range violations {
			constraints[i] = v.Constraint
		}
		found := slices.Contains(constraints, "requirements.budgetRef")
		requireTrue(t, found, "expected constraint requirements.budgetRef")
	})

	t.Run("returns no compliance violation for a denied tool (tool deny is runtime enforcement)", func(t *testing.T) {
		agent := makeAgent("tool-user")
		agent.Spec.Guardrails = &kubeswarmv1alpha1.AgentGuardrails{
			Limits: &kubeswarmv1alpha1.GuardrailLimits{DailyTokens: 10_000},
		}

		effective := &kubeswarmv1alpha1.EffectivePolicySpec{
			ToolDeny: []string{"shell/*"},
		}
		violations := EvaluateAgentCompliance(agent, effective)
		requireLen(t, violations, 0)
	})

	t.Run("returns a violation when agent model is in the denied model list", func(t *testing.T) {
		agent := makeAgent("bad-model")
		agent.Spec.Model = testModelGPT4o

		effective := &kubeswarmv1alpha1.EffectivePolicySpec{
			Models: &kubeswarmv1alpha1.PolicyModels{
				Denied: []string{testModelGPT4o},
			},
		}
		violations := EvaluateAgentCompliance(agent, effective)
		if len(violations) == 0 {
			t.Fatal("expected at least one violation")
		}
		constraints := make([]string, len(violations))
		for i, v := range violations {
			constraints[i] = v.Constraint
		}
		found := slices.Contains(constraints, "models.denied")
		requireTrue(t, found, "expected constraint models.denied")
	})

	t.Run("returns no violation when agent model is in the allowed model list", func(t *testing.T) {
		agent := makeAgent("allowed-model")
		agent.Spec.Model = "claude-sonnet-4-6"

		effective := &kubeswarmv1alpha1.EffectivePolicySpec{
			Models: &kubeswarmv1alpha1.PolicyModels{
				Allowed: []string{"claude-sonnet-4-6", "claude-haiku-3"},
			},
		}
		violations := EvaluateAgentCompliance(agent, effective)
		requireLen(t, violations, 0)
	})

	t.Run("returns a violation when agent model is not in the allowed model list", func(t *testing.T) {
		agent := makeAgent("unlisted-model")
		agent.Spec.Model = testModelGPT4o

		effective := &kubeswarmv1alpha1.EffectivePolicySpec{
			Models: &kubeswarmv1alpha1.PolicyModels{
				Allowed: []string{"claude-sonnet-4-6", "claude-haiku-3"},
			},
		}
		violations := EvaluateAgentCompliance(agent, effective)
		if len(violations) == 0 {
			t.Fatal("expected at least one violation")
		}
		constraints := make([]string, len(violations))
		for i, v := range violations {
			constraints[i] = v.Constraint
		}
		found := slices.Contains(constraints, "models.allowed")
		requireTrue(t, found, "expected constraint models.allowed")
	})

	t.Run("returns multiple violations for an agent that violates several constraints", func(t *testing.T) {
		agent := makeAgent("violator")
		agent.Spec.Model = testModelGPT4o
		agent.Spec.Guardrails = &kubeswarmv1alpha1.AgentGuardrails{
			Limits: &kubeswarmv1alpha1.GuardrailLimits{
				DailyTokens:   500_000,
				TokensPerCall: 16_000,
			},
			// BudgetRef intentionally absent
		}

		effective := &kubeswarmv1alpha1.EffectivePolicySpec{
			Limits: &kubeswarmv1alpha1.PolicyLimits{
				MaxDailyTokens:   ptrInt64(100_000),
				MaxTokensPerCall: ptrInt32(8000),
			},
			Models: &kubeswarmv1alpha1.PolicyModels{
				Denied: []string{testModelGPT4o},
			},
			Requirements: kubeswarmv1alpha1.PolicyRequirements{
				BudgetRef: true,
			},
		}
		violations := EvaluateAgentCompliance(agent, effective)
		if len(violations) < 3 {
			t.Fatalf("expected at least 3 violations, got %d", len(violations))
		}

		constraints := make([]string, len(violations))
		for i, v := range violations {
			constraints[i] = v.Constraint
		}
		for _, want := range []string{"limits.dailyTokens", "limits.tokensPerCall", "models.denied"} {
			found := slices.Contains(constraints, want)
			requireTrue(t, found, "expected constraint "+want)
		}
	})

	t.Run("returns no violations for an agent with no guardrails when effective policy is empty", func(t *testing.T) {
		agent := makeAgent("bare")
		effective := &kubeswarmv1alpha1.EffectivePolicySpec{}
		violations := EvaluateAgentCompliance(agent, effective)
		requireLen(t, violations, 0)
	})

	t.Run("returns a violation when agent TimeoutSeconds exceeds MaxTimeoutSeconds", func(t *testing.T) {
		agent := makeAgent("long-timeout")
		agent.Spec.Guardrails = &kubeswarmv1alpha1.AgentGuardrails{
			Limits: &kubeswarmv1alpha1.GuardrailLimits{
				TimeoutSeconds: 600,
			},
		}

		effective := &kubeswarmv1alpha1.EffectivePolicySpec{
			Limits: &kubeswarmv1alpha1.PolicyLimits{
				MaxTimeoutSeconds: ptrInt32(300),
			},
		}
		violations := EvaluateAgentCompliance(agent, effective)
		if len(violations) == 0 {
			t.Fatal("expected at least one violation")
		}
		constraints := make([]string, len(violations))
		for i, v := range violations {
			constraints[i] = v.Constraint
		}
		found := slices.Contains(constraints, "limits.timeoutSeconds")
		requireTrue(t, found, "expected constraint limits.timeoutSeconds")
	})

	t.Run("returns a violation when agent TimeoutSeconds is below MinTimeoutSeconds", func(t *testing.T) {
		agent := makeAgent("short-timeout")
		agent.Spec.Guardrails = &kubeswarmv1alpha1.AgentGuardrails{
			Limits: &kubeswarmv1alpha1.GuardrailLimits{
				TimeoutSeconds: 10,
			},
		}

		effective := &kubeswarmv1alpha1.EffectivePolicySpec{
			Limits: &kubeswarmv1alpha1.PolicyLimits{
				MinTimeoutSeconds: ptrInt32(60),
			},
		}
		violations := EvaluateAgentCompliance(agent, effective)
		if len(violations) == 0 {
			t.Fatal("expected at least one violation")
		}
		constraints := make([]string, len(violations))
		for i, v := range violations {
			constraints[i] = v.Constraint
		}
		found := slices.Contains(constraints, "limits.timeoutSeconds")
		requireTrue(t, found, "expected constraint limits.timeoutSeconds")
	})

	t.Run("each violation carries a non-empty PolicyName and Message", func(t *testing.T) {
		agent := makeAgent("bad-agent")
		agent.Spec.Guardrails = &kubeswarmv1alpha1.AgentGuardrails{
			Limits: &kubeswarmv1alpha1.GuardrailLimits{DailyTokens: 999_999},
		}

		effective := &kubeswarmv1alpha1.EffectivePolicySpec{
			Limits: &kubeswarmv1alpha1.PolicyLimits{
				MaxDailyTokens: ptrInt64(1000),
			},
		}
		violations := EvaluateAgentCompliance(agent, effective)
		if len(violations) == 0 {
			t.Fatal("expected at least one violation")
		}
		for _, v := range violations {
			requireNotEmpty(t, v.Constraint)
			requireNotEmpty(t, v.Message)
		}
	})

	t.Run("budgetRef requirement is satisfied when guardrails BudgetRef is set", func(t *testing.T) {
		agent := makeAgent("budgeted")
		agent.Spec.Guardrails = &kubeswarmv1alpha1.AgentGuardrails{
			BudgetRef: &corev1.LocalObjectReference{Name: "my-budget"},
		}

		effective := &kubeswarmv1alpha1.EffectivePolicySpec{
			Requirements: kubeswarmv1alpha1.PolicyRequirements{BudgetRef: true},
		}
		violations := EvaluateAgentCompliance(agent, effective)
		requireLen(t, violations, 0)
	})
}
