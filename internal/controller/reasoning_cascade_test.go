package controller

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1alpha1 "github.com/kubeswarm/kubeswarm/api/v1alpha1"
)

func TestMergeReasoningConfig(t *testing.T) {
	t.Run("returns nil when neither settings nor agent set anything", func(t *testing.T) {
		out := mergeReasoningConfig(nil, nil)
		requireNil(t, out)

		out = mergeReasoningConfig(nil, []v1alpha1.SwarmSettings{
			{Spec: v1alpha1.SwarmSettingsSpec{}},
			{Spec: v1alpha1.SwarmSettingsSpec{}},
		})
		requireNil(t, out)
	})

	t.Run("returns agent config as-is when no settings provide reasoning", func(t *testing.T) {
		agent := &v1alpha1.ReasoningConfig{
			Mode:         v1alpha1.ReasoningExplicit,
			Effort:       v1alpha1.ReasoningEffortHigh,
			BudgetTokens: int32Ptr(4096),
		}
		out := mergeReasoningConfig(agent, nil)
		if out == nil {
			t.Fatal("expected non-nil output")
		}
		requireEqual(t, out.Mode, v1alpha1.ReasoningExplicit)
		requireEqual(t, out.Effort, v1alpha1.ReasoningEffortHigh)
		if out.BudgetTokens == nil {
			t.Fatal("expected non-nil BudgetTokens")
		}
		requireEqual(t, *out.BudgetTokens, int32(4096))
	})

	t.Run("returns settings cascade when agent config is nil", func(t *testing.T) {
		settings := []v1alpha1.SwarmSettings{
			{Spec: v1alpha1.SwarmSettingsSpec{Reasoning: &v1alpha1.ReasoningDefaults{
				Mode:         v1alpha1.ReasoningAuto,
				Effort:       v1alpha1.ReasoningEffortMedium,
				BudgetTokens: int32Ptr(1024),
			}}},
		}
		out := mergeReasoningConfig(nil, settings)
		if out == nil {
			t.Fatal("expected non-nil output")
		}
		requireEqual(t, out.Mode, v1alpha1.ReasoningAuto)
		requireEqual(t, out.Effort, v1alpha1.ReasoningEffortMedium)
		requireEqual(t, *out.BudgetTokens, int32(1024))
	})

	t.Run("per-agent fields override cascade fields", func(t *testing.T) {
		settings := []v1alpha1.SwarmSettings{
			{Spec: v1alpha1.SwarmSettingsSpec{Reasoning: &v1alpha1.ReasoningDefaults{
				Mode:         v1alpha1.ReasoningAuto,
				Effort:       v1alpha1.ReasoningEffortLow,
				BudgetTokens: int32Ptr(1024),
			}}},
		}
		agent := &v1alpha1.ReasoningConfig{
			Mode:         v1alpha1.ReasoningExplicit,
			Effort:       v1alpha1.ReasoningEffortHigh,
			BudgetTokens: int32Ptr(8192),
		}
		out := mergeReasoningConfig(agent, settings)
		requireEqual(t, out.Mode, v1alpha1.ReasoningExplicit)
		requireEqual(t, out.Effort, v1alpha1.ReasoningEffortHigh)
		requireEqual(t, *out.BudgetTokens, int32(8192))
	})

	t.Run("per-agent Disabled overrides cascaded Auto (SC16)", func(t *testing.T) {
		settings := []v1alpha1.SwarmSettings{
			{Spec: v1alpha1.SwarmSettingsSpec{Reasoning: &v1alpha1.ReasoningDefaults{
				Mode: v1alpha1.ReasoningAuto,
			}}},
		}
		agent := &v1alpha1.ReasoningConfig{Mode: v1alpha1.ReasoningDisabled}
		out := mergeReasoningConfig(agent, settings)
		if out == nil {
			t.Fatal("expected non-nil output")
		}
		requireEqual(t, out.Mode, v1alpha1.ReasoningDisabled)
	})

	t.Run("later setting in allSettings wins over earlier", func(t *testing.T) {
		settings := []v1alpha1.SwarmSettings{
			{Spec: v1alpha1.SwarmSettingsSpec{Reasoning: &v1alpha1.ReasoningDefaults{
				Mode:         v1alpha1.ReasoningAuto,
				Effort:       v1alpha1.ReasoningEffortLow,
				BudgetTokens: int32Ptr(512),
			}}},
			{Spec: v1alpha1.SwarmSettingsSpec{Reasoning: &v1alpha1.ReasoningDefaults{
				Mode:         v1alpha1.ReasoningExplicit,
				Effort:       v1alpha1.ReasoningEffortHigh,
				BudgetTokens: int32Ptr(4096),
			}}},
		}
		out := mergeReasoningConfig(nil, settings)
		requireEqual(t, out.Mode, v1alpha1.ReasoningExplicit)
		requireEqual(t, out.Effort, v1alpha1.ReasoningEffortHigh)
		requireEqual(t, *out.BudgetTokens, int32(4096))
	})

	t.Run("composes partial fields: settings sets Mode, agent sets BudgetTokens", func(t *testing.T) {
		settings := []v1alpha1.SwarmSettings{
			{Spec: v1alpha1.SwarmSettingsSpec{Reasoning: &v1alpha1.ReasoningDefaults{
				Mode: v1alpha1.ReasoningAuto,
			}}},
		}
		agent := &v1alpha1.ReasoningConfig{
			BudgetTokens: int32Ptr(2048),
		}
		out := mergeReasoningConfig(agent, settings)
		if out == nil {
			t.Fatal("expected non-nil output")
		}
		requireEqual(t, out.Mode, v1alpha1.ReasoningAuto)
		if out.BudgetTokens == nil {
			t.Fatal("expected non-nil BudgetTokens")
		}
		requireEqual(t, *out.BudgetTokens, int32(2048))
	})
}

func TestReasoningConditionReason(t *testing.T) {
	t.Run("Disabled cases", func(t *testing.T) {
		t.Run("returns Disabled when cfg is nil", func(t *testing.T) {
			reason, status := reasoningConditionReason(nil, nil)
			requireEqual(t, reason, v1alpha1.ReasoningReasonDisabled)
			requireEqual(t, status, metav1.ConditionFalse)
		})
		t.Run("returns Disabled when cfg.Mode == Disabled", func(t *testing.T) {
			cfg := &v1alpha1.ReasoningConfig{Mode: v1alpha1.ReasoningDisabled}
			reason, status := reasoningConditionReason(cfg, nil)
			requireEqual(t, reason, v1alpha1.ReasoningReasonDisabled)
			requireEqual(t, status, metav1.ConditionFalse)
		})
		t.Run("returns Disabled when cfg.Mode is empty string", func(t *testing.T) {
			cfg := &v1alpha1.ReasoningConfig{}
			reason, status := reasoningConditionReason(cfg, nil)
			requireEqual(t, reason, v1alpha1.ReasoningReasonDisabled)
			requireEqual(t, status, metav1.ConditionFalse)
		})
	})

	t.Run("Active cases - trusts the user declaration", func(t *testing.T) {
		t.Run("Auto mode -> Active regardless of model name", func(t *testing.T) {
			cfg := &v1alpha1.ReasoningConfig{Mode: v1alpha1.ReasoningAuto}
			reason, status := reasoningConditionReason(cfg, nil)
			requireEqual(t, reason, v1alpha1.ReasoningReasonActive)
			requireEqual(t, status, metav1.ConditionTrue)
		})
		t.Run("Explicit mode -> Active regardless of model name", func(t *testing.T) {
			cfg := &v1alpha1.ReasoningConfig{
				Mode:         v1alpha1.ReasoningExplicit,
				BudgetTokens: int32Ptr(4096),
			}
			reason, status := reasoningConditionReason(cfg, nil)
			requireEqual(t, reason, v1alpha1.ReasoningReasonActive)
			requireEqual(t, status, metav1.ConditionTrue)
		})
	})

	t.Run("ClampedByGuardrail cases", func(t *testing.T) {
		t.Run("BudgetTokens=8192 > MaxThinking=4096 -> ClampedByGuardrail", func(t *testing.T) {
			cfg := &v1alpha1.ReasoningConfig{
				Mode:         v1alpha1.ReasoningExplicit,
				BudgetTokens: int32Ptr(8192),
			}
			limits := &v1alpha1.GuardrailLimits{MaxThinkingTokensPerCall: int32Ptr(4096)}
			reason, status := reasoningConditionReason(cfg, limits)
			requireEqual(t, reason, v1alpha1.ReasoningReasonClampedByGuardrail)
			requireEqual(t, status, metav1.ConditionTrue)
		})

		t.Run("BudgetTokens=8192 < MaxThinking=9999 -> Active (no clamp)", func(t *testing.T) {
			cfg := &v1alpha1.ReasoningConfig{
				Mode:         v1alpha1.ReasoningExplicit,
				BudgetTokens: int32Ptr(8192),
			}
			limits := &v1alpha1.GuardrailLimits{MaxThinkingTokensPerCall: int32Ptr(9999)}
			reason, status := reasoningConditionReason(cfg, limits)
			requireEqual(t, reason, v1alpha1.ReasoningReasonActive)
			requireEqual(t, status, metav1.ConditionTrue)
		})

		t.Run("no BudgetTokens with MaxThinking set -> Active (nothing to clamp)", func(t *testing.T) {
			cfg := &v1alpha1.ReasoningConfig{Mode: v1alpha1.ReasoningAuto}
			limits := &v1alpha1.GuardrailLimits{MaxThinkingTokensPerCall: int32Ptr(4096)}
			reason, status := reasoningConditionReason(cfg, limits)
			requireEqual(t, reason, v1alpha1.ReasoningReasonActive)
			requireEqual(t, status, metav1.ConditionTrue)
		})

		t.Run("nil limits -> Active (no guardrail to clamp against)", func(t *testing.T) {
			cfg := &v1alpha1.ReasoningConfig{
				Mode:         v1alpha1.ReasoningAuto,
				BudgetTokens: int32Ptr(100000),
			}
			reason, status := reasoningConditionReason(cfg, nil)
			requireEqual(t, reason, v1alpha1.ReasoningReasonActive)
			requireEqual(t, status, metav1.ConditionTrue)
		})
	})
}
