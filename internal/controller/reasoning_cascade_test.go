package controller

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1alpha1 "github.com/kubeswarm/kubeswarm/api/v1alpha1"
)

var _ = Describe("mergeReasoningConfig", func() {
	It("returns nil when neither settings nor agent set anything", func() {
		out := mergeReasoningConfig(nil, nil)
		Expect(out).To(BeNil())

		out = mergeReasoningConfig(nil, []v1alpha1.SwarmSettings{
			{Spec: v1alpha1.SwarmSettingsSpec{}},
			{Spec: v1alpha1.SwarmSettingsSpec{}},
		})
		Expect(out).To(BeNil())
	})

	It("returns agent config as-is when no settings provide reasoning", func() {
		agent := &v1alpha1.ReasoningConfig{
			Mode:         v1alpha1.ReasoningExplicit,
			Effort:       v1alpha1.ReasoningEffortHigh,
			BudgetTokens: int32Ptr(4096),
		}
		out := mergeReasoningConfig(agent, nil)
		Expect(out).NotTo(BeNil())
		Expect(out.Mode).To(Equal(v1alpha1.ReasoningExplicit))
		Expect(out.Effort).To(Equal(v1alpha1.ReasoningEffortHigh))
		Expect(out.BudgetTokens).NotTo(BeNil())
		Expect(*out.BudgetTokens).To(Equal(int32(4096)))
	})

	It("returns settings cascade when agent config is nil", func() {
		settings := []v1alpha1.SwarmSettings{
			{Spec: v1alpha1.SwarmSettingsSpec{Reasoning: &v1alpha1.ReasoningDefaults{
				Mode:         v1alpha1.ReasoningAuto,
				Effort:       v1alpha1.ReasoningEffortMedium,
				BudgetTokens: int32Ptr(1024),
			}}},
		}
		out := mergeReasoningConfig(nil, settings)
		Expect(out).NotTo(BeNil())
		Expect(out.Mode).To(Equal(v1alpha1.ReasoningAuto))
		Expect(out.Effort).To(Equal(v1alpha1.ReasoningEffortMedium))
		Expect(*out.BudgetTokens).To(Equal(int32(1024)))
	})

	It("per-agent fields override cascade fields", func() {
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
		Expect(out.Mode).To(Equal(v1alpha1.ReasoningExplicit))
		Expect(out.Effort).To(Equal(v1alpha1.ReasoningEffortHigh))
		Expect(*out.BudgetTokens).To(Equal(int32(8192)))
	})

	It("per-agent Disabled overrides cascaded Auto (SC16)", func() {
		settings := []v1alpha1.SwarmSettings{
			{Spec: v1alpha1.SwarmSettingsSpec{Reasoning: &v1alpha1.ReasoningDefaults{
				Mode: v1alpha1.ReasoningAuto,
			}}},
		}
		agent := &v1alpha1.ReasoningConfig{Mode: v1alpha1.ReasoningDisabled}
		out := mergeReasoningConfig(agent, settings)
		Expect(out).NotTo(BeNil())
		Expect(out.Mode).To(Equal(v1alpha1.ReasoningDisabled))
	})

	It("later setting in allSettings wins over earlier", func() {
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
		Expect(out.Mode).To(Equal(v1alpha1.ReasoningExplicit))
		Expect(out.Effort).To(Equal(v1alpha1.ReasoningEffortHigh))
		Expect(*out.BudgetTokens).To(Equal(int32(4096)))
	})

	It("composes partial fields: settings sets Mode, agent sets BudgetTokens", func() {
		settings := []v1alpha1.SwarmSettings{
			{Spec: v1alpha1.SwarmSettingsSpec{Reasoning: &v1alpha1.ReasoningDefaults{
				Mode: v1alpha1.ReasoningAuto,
			}}},
		}
		agent := &v1alpha1.ReasoningConfig{
			BudgetTokens: int32Ptr(2048),
		}
		out := mergeReasoningConfig(agent, settings)
		Expect(out).NotTo(BeNil())
		Expect(out.Mode).To(Equal(v1alpha1.ReasoningAuto))
		Expect(out.BudgetTokens).NotTo(BeNil())
		Expect(*out.BudgetTokens).To(Equal(int32(2048)))
	})
})

var _ = Describe("reasoningConditionReason", func() {
	// reasoningConditionReason no longer takes a model parameter.
	// Model-name-based capability detection was removed: the operator trusts
	// the user's mode: Auto/Explicit declaration. Provider-side conditions
	// (IgnoredModelNotCapable, RejectedModelNotCapable, FieldIgnored) are
	// set at runtime after the first LLM call, not at reconcile time.

	Context("Disabled cases", func() {
		It("returns Disabled when cfg is nil", func() {
			reason, status := reasoningConditionReason(nil, nil)
			Expect(reason).To(Equal(v1alpha1.ReasoningReasonDisabled))
			Expect(status).To(Equal(metav1.ConditionFalse))
		})
		It("returns Disabled when cfg.Mode == Disabled", func() {
			cfg := &v1alpha1.ReasoningConfig{Mode: v1alpha1.ReasoningDisabled}
			reason, status := reasoningConditionReason(cfg, nil)
			Expect(reason).To(Equal(v1alpha1.ReasoningReasonDisabled))
			Expect(status).To(Equal(metav1.ConditionFalse))
		})
		It("returns Disabled when cfg.Mode is empty string", func() {
			cfg := &v1alpha1.ReasoningConfig{}
			reason, status := reasoningConditionReason(cfg, nil)
			Expect(reason).To(Equal(v1alpha1.ReasoningReasonDisabled))
			Expect(status).To(Equal(metav1.ConditionFalse))
		})
	})

	Context("Active cases - trusts the user declaration", func() {
		It("Auto mode -> Active regardless of model name", func() {
			cfg := &v1alpha1.ReasoningConfig{Mode: v1alpha1.ReasoningAuto}
			reason, status := reasoningConditionReason(cfg, nil)
			Expect(reason).To(Equal(v1alpha1.ReasoningReasonActive))
			Expect(status).To(Equal(metav1.ConditionTrue))
		})
		It("Explicit mode -> Active regardless of model name", func() {
			cfg := &v1alpha1.ReasoningConfig{
				Mode:         v1alpha1.ReasoningExplicit,
				BudgetTokens: int32Ptr(4096),
			}
			reason, status := reasoningConditionReason(cfg, nil)
			Expect(reason).To(Equal(v1alpha1.ReasoningReasonActive))
			Expect(status).To(Equal(metav1.ConditionTrue))
		})
	})

	Context("ClampedByGuardrail cases", func() {
		It("BudgetTokens=8192 > MaxThinking=4096 -> ClampedByGuardrail", func() {
			cfg := &v1alpha1.ReasoningConfig{
				Mode:         v1alpha1.ReasoningExplicit,
				BudgetTokens: int32Ptr(8192),
			}
			limits := &v1alpha1.GuardrailLimits{MaxThinkingTokensPerCall: int32Ptr(4096)}
			reason, status := reasoningConditionReason(cfg, limits)
			Expect(reason).To(Equal(v1alpha1.ReasoningReasonClampedByGuardrail))
			Expect(status).To(Equal(metav1.ConditionTrue))
		})

		It("BudgetTokens=8192 < MaxThinking=9999 -> Active (no clamp)", func() {
			cfg := &v1alpha1.ReasoningConfig{
				Mode:         v1alpha1.ReasoningExplicit,
				BudgetTokens: int32Ptr(8192),
			}
			limits := &v1alpha1.GuardrailLimits{MaxThinkingTokensPerCall: int32Ptr(9999)}
			reason, status := reasoningConditionReason(cfg, limits)
			Expect(reason).To(Equal(v1alpha1.ReasoningReasonActive))
			Expect(status).To(Equal(metav1.ConditionTrue))
		})

		It("no BudgetTokens with MaxThinking set -> Active (nothing to clamp)", func() {
			cfg := &v1alpha1.ReasoningConfig{Mode: v1alpha1.ReasoningAuto}
			limits := &v1alpha1.GuardrailLimits{MaxThinkingTokensPerCall: int32Ptr(4096)}
			reason, status := reasoningConditionReason(cfg, limits)
			Expect(reason).To(Equal(v1alpha1.ReasoningReasonActive))
			Expect(status).To(Equal(metav1.ConditionTrue))
		})

		It("nil limits -> Active (no guardrail to clamp against)", func() {
			cfg := &v1alpha1.ReasoningConfig{
				Mode:         v1alpha1.ReasoningAuto,
				BudgetTokens: int32Ptr(100000),
			}
			reason, status := reasoningConditionReason(cfg, nil)
			Expect(reason).To(Equal(v1alpha1.ReasoningReasonActive))
			Expect(status).To(Equal(metav1.ConditionTrue))
		})
	})
})
