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

	corev1 "k8s.io/api/core/v1"

	kubeswarmv1alpha1 "github.com/kubeswarm/kubeswarm/api/v1alpha1"
)

// applyPolicyLimits clamps agent guardrail values against the effective policy.
// Ceiling fields use min(agent, policy); floor fields use max(agent, policy).
// If ep or ep.Limits is nil, values are returned unchanged.
func applyPolicyLimits(maxTokens, timeoutSecs, maxRetries int, dailyTokenLimit int64, ep *kubeswarmv1alpha1.EffectivePolicySpec) (int, int, int, int64) {
	if ep == nil || ep.Limits == nil {
		return maxTokens, timeoutSecs, maxRetries, dailyTokenLimit
	}
	l := ep.Limits

	// Ceiling: MaxTokensPerCall clamps maxTokens.
	if l.MaxTokensPerCall != nil && maxTokens > int(*l.MaxTokensPerCall) {
		maxTokens = int(*l.MaxTokensPerCall)
	}

	// Ceiling: MaxTimeoutSeconds clamps timeoutSecs.
	if l.MaxTimeoutSeconds != nil && timeoutSecs > int(*l.MaxTimeoutSeconds) {
		timeoutSecs = int(*l.MaxTimeoutSeconds)
	}

	// Floor: MinTimeoutSeconds clamps timeoutSecs.
	if l.MinTimeoutSeconds != nil && timeoutSecs < int(*l.MinTimeoutSeconds) {
		timeoutSecs = int(*l.MinTimeoutSeconds)
	}

	// Ceiling: MaxDailyTokens clamps dailyTokenLimit.
	// Agent value of 0 means "no limit" - policy ceiling applies.
	if l.MaxDailyTokens != nil {
		if dailyTokenLimit == 0 || dailyTokenLimit > *l.MaxDailyTokens {
			dailyTokenLimit = *l.MaxDailyTokens
		}
	}

	return maxTokens, timeoutSecs, maxRetries, dailyTokenLimit
}

// applyPolicyThinkingLimits clamps thinking and answer token caps against the
// effective policy. If the agent value is nil, the policy value is used as the
// default. If the agent value exceeds the policy ceiling, it is clamped.
// If the agent value is lower, it is left unchanged.
func applyPolicyThinkingLimits(thinkingTokens, answerTokens *int32, ep *kubeswarmv1alpha1.EffectivePolicySpec) (*int32, *int32) {
	if ep == nil || ep.Limits == nil {
		return thinkingTokens, answerTokens
	}
	l := ep.Limits

	thinkingTokens = clampOptionalInt32(thinkingTokens, l.MaxThinkingTokensPerCall)
	answerTokens = clampOptionalInt32(answerTokens, l.MaxAnswerTokensPerCall)

	return thinkingTokens, answerTokens
}

// clampOptionalInt32 applies a ceiling from policy to an agent value.
// If agent is nil, the policy value is returned as the default.
// If agent exceeds policy, it is clamped. Otherwise agent is unchanged.
func clampOptionalInt32(agent *int32, policy *int32) *int32 {
	if policy == nil {
		return agent
	}
	if agent == nil {
		v := *policy
		return &v
	}
	if *agent > *policy {
		v := *policy
		return &v
	}
	return agent
}

// buildPolicyEnvVars returns environment variables for injection into agent pods
// based on the effective policy. Returns nil when ep is nil.
func buildPolicyEnvVars(ep *kubeswarmv1alpha1.EffectivePolicySpec) []corev1.EnvVar {
	if ep == nil {
		return nil
	}

	var envs []corev1.EnvVar

	if len(ep.ToolDeny) > 0 {
		data, _ := json.Marshal(ep.ToolDeny)
		envs = append(envs, corev1.EnvVar{
			Name:  "AGENT_POLICY_TOOL_DENY",
			Value: string(data),
		})
	}

	if ep.ForceTrustLevel != nil {
		envs = append(envs, corev1.EnvVar{
			Name:  "AGENT_POLICY_FORCE_TRUST_LEVEL",
			Value: string(*ep.ForceTrustLevel),
		})
	}

	return envs
}
