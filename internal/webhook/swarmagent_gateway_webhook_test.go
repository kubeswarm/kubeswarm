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

package webhook

import (
	"context"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kubeswarmv1alpha1 "github.com/kubeswarm/kubeswarm/api/v1alpha1"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func gatewayAgent() *kubeswarmv1alpha1.SwarmAgent {
	return &kubeswarmv1alpha1.SwarmAgent{
		ObjectMeta: metav1.ObjectMeta{Name: "gw-agent", Namespace: "default"},
		Spec: kubeswarmv1alpha1.SwarmAgentSpec{
			Model:  "claude-sonnet-4-6",
			Prompt: &kubeswarmv1alpha1.AgentPrompt{Inline: "You are a gateway agent."},
			Gateway: &kubeswarmv1alpha1.GatewayConfig{
				RegistryRef:  corev1.LocalObjectReference{Name: "my-registry"},
				DispatchMode: kubeswarmv1alpha1.GatewayDispatchEnabled,
			},
		},
	}
}

func hasError(errs []error, substr string) bool {
	for _, e := range errs {
		if strings.Contains(e.Error(), substr) {
			return true
		}
	}
	return false
}

func hasWarning(warnings []string, substr string) bool {
	for _, w := range warnings {
		if strings.Contains(w, substr) {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestValidateGatewayConfig_ValidConfig_NoErrors(t *testing.T) {
	agent := gatewayAgent()
	errs, warnings := ValidateGatewayConfig(context.Background(), fakeClient(), agent)
	if len(errs) != 0 {
		t.Errorf("expected no errors for valid gateway config, got %v", errs)
	}
	if len(warnings) != 0 {
		t.Errorf("expected no warnings for valid gateway config, got %v", warnings)
	}
}

func TestValidateGatewayConfig_NilGateway_NoErrors(t *testing.T) {
	agent := &kubeswarmv1alpha1.SwarmAgent{
		ObjectMeta: metav1.ObjectMeta{Name: "plain-agent", Namespace: "default"},
		Spec: kubeswarmv1alpha1.SwarmAgentSpec{
			Model:  "claude-sonnet-4-6",
			Prompt: &kubeswarmv1alpha1.AgentPrompt{Inline: "You are a regular agent."},
		},
	}
	errs, warnings := ValidateGatewayConfig(context.Background(), fakeClient(), agent)
	if len(errs) != 0 {
		t.Errorf("expected no errors when spec.gateway is nil, got %v", errs)
	}
	if len(warnings) != 0 {
		t.Errorf("expected no warnings when spec.gateway is nil, got %v", warnings)
	}
}

func TestValidateGatewayConfig_RejectsGatewayWithTeamRoleAnnotation(t *testing.T) {
	agent := gatewayAgent()
	agent.Annotations = map[string]string{
		"kubeswarm.io/team-role": "coordinator",
	}
	errs, _ := ValidateGatewayConfig(context.Background(), fakeClient(), agent)
	if len(errs) == 0 {
		t.Fatal("expected error when gateway agent has team-role annotation, got none")
	}
	if !hasError(errs, "team-role") {
		t.Errorf("expected error mentioning team-role, got %v", errs)
	}
}

func TestValidateGatewayConfig_RejectsMCPToolNamedRegistrySearch(t *testing.T) {
	agent := gatewayAgent()
	agent.Spec.Tools = &kubeswarmv1alpha1.AgentTools{
		MCP: []kubeswarmv1alpha1.MCPToolSpec{
			{Name: "registry_search", URL: "https://example.com/sse"},
		},
	}
	errs, _ := ValidateGatewayConfig(context.Background(), fakeClient(), agent)
	if len(errs) == 0 {
		t.Fatal("expected error when MCP tool named registry_search, got none")
	}
	if !hasError(errs, "registry_search") {
		t.Errorf("expected error mentioning registry_search, got %v", errs)
	}
}

func TestValidateGatewayConfig_RejectsMCPToolNamedDispatch(t *testing.T) {
	agent := gatewayAgent()
	agent.Spec.Tools = &kubeswarmv1alpha1.AgentTools{
		MCP: []kubeswarmv1alpha1.MCPToolSpec{
			{Name: "dispatch", URL: "https://example.com/sse"},
		},
	}
	errs, _ := ValidateGatewayConfig(context.Background(), fakeClient(), agent)
	if len(errs) == 0 {
		t.Fatal("expected error when MCP tool named dispatch, got none")
	}
	if !hasError(errs, "dispatch") {
		t.Errorf("expected error mentioning dispatch, got %v", errs)
	}
}

func TestValidateGatewayConfig_RejectsWebhookToolNamedRegistrySearch(t *testing.T) {
	agent := gatewayAgent()
	agent.Spec.Tools = &kubeswarmv1alpha1.AgentTools{
		Webhooks: []kubeswarmv1alpha1.WebhookToolSpec{
			{Name: "registry_search", URL: "https://example.com/hook", Method: "POST"},
		},
	}
	errs, _ := ValidateGatewayConfig(context.Background(), fakeClient(), agent)
	if len(errs) == 0 {
		t.Fatal("expected error when webhook tool named registry_search, got none")
	}
	if !hasError(errs, "registry_search") {
		t.Errorf("expected error mentioning registry_search, got %v", errs)
	}
}

func TestValidateGatewayConfig_RejectsReservedToolNamesWithoutGateway(t *testing.T) {
	// Reserved tool names must be rejected regardless of whether the agent has
	// a gateway block - otherwise a tool named "dispatch" could be smuggled in
	// on a non-gateway agent and break admission the moment a gateway is added.
	cases := []struct {
		name     string
		toolName string
	}{
		{"MCP registry_search", "registry_search"},
		{"MCP dispatch", "dispatch"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			agent := &kubeswarmv1alpha1.SwarmAgent{
				ObjectMeta: metav1.ObjectMeta{Name: "plain", Namespace: "default"},
				Spec: kubeswarmv1alpha1.SwarmAgentSpec{
					Model: "claude-sonnet-4-6",
					Tools: &kubeswarmv1alpha1.AgentTools{
						MCP: []kubeswarmv1alpha1.MCPToolSpec{
							{Name: tc.toolName, URL: "https://example.com/sse"},
						},
					},
				},
			}
			errs, _ := ValidateGatewayConfig(context.Background(), fakeClient(), agent)
			if len(errs) == 0 {
				t.Fatalf("expected error when non-gateway agent ships tool named %q, got none", tc.toolName)
			}
			if !hasError(errs, tc.toolName) {
				t.Errorf("expected error mentioning %q, got %v", tc.toolName, errs)
			}
		})
	}
}

func TestValidateGatewayConfig_RejectsInvalidFilterByTags(t *testing.T) {
	cases := []struct {
		name string
		tag  string
	}{
		{"UpperCase", "Code"},
		{"StartsWithDigit", "1bad"},
		{"SpecialChars", "no_underscores"},
		{"Empty", ""},
		{"SpaceInTag", "has space"},
		{"TrailingHyphen", "code-"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			agent := gatewayAgent()
			agent.Spec.Gateway.FilterByTags = []string{tc.tag}
			errs, _ := ValidateGatewayConfig(context.Background(), fakeClient(), agent)
			if len(errs) == 0 {
				t.Errorf("expected error for invalid filterByTags entry %q, got none", tc.tag)
			}
			if !hasError(errs, "filterByTags") {
				t.Errorf("expected error mentioning filterByTags, got %v", errs)
			}
		})
	}
}

func TestValidateGatewayConfig_RejectsInvalidAllowedTargets(t *testing.T) {
	cases := []struct {
		name   string
		target string
	}{
		{"UpperCase", "MyAgent"},
		{"TrailingHyphen", "agent-"},
		{"LeadingHyphen", "-agent"},
		{"Underscore", "my_agent"},
		{"Empty", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			agent := gatewayAgent()
			agent.Spec.Gateway.AllowedTargets = []string{tc.target}
			errs, _ := ValidateGatewayConfig(context.Background(), fakeClient(), agent)
			if len(errs) == 0 {
				t.Errorf("expected error for invalid allowedTargets entry %q, got none", tc.target)
			}
			if !hasError(errs, "allowedTargets") {
				t.Errorf("expected error mentioning allowedTargets, got %v", errs)
			}
		})
	}
}

func TestValidateGatewayConfig_RejectsOverlongAllowedTarget(t *testing.T) {
	agent := gatewayAgent()
	// 64 lowercase letters - one over the DNS-1123 label length cap.
	var overlong strings.Builder
	for range 64 {
		overlong.WriteString("a")
	}
	agent.Spec.Gateway.AllowedTargets = []string{overlong.String()}
	errs, _ := ValidateGatewayConfig(context.Background(), fakeClient(), agent)
	if len(errs) == 0 {
		t.Fatal("expected error for allowedTargets entry longer than 63 chars, got none")
	}
	if !hasError(errs, "exceeds") {
		t.Errorf("expected length-exceeded error, got %v", errs)
	}
}

func TestValidateGatewayConfig_WarnsAllowGatewayTargetsWithEmptyAllowedTargets(t *testing.T) {
	agent := gatewayAgent()
	agent.Spec.Gateway.AllowGatewayTargets = true
	agent.Spec.Gateway.AllowedTargets = nil // empty
	errs, warnings := ValidateGatewayConfig(context.Background(), fakeClient(), agent)
	if len(errs) != 0 {
		t.Errorf("expected no errors (warning only), got %v", errs)
	}
	if len(warnings) == 0 {
		t.Fatal("expected warning when allowGatewayTargets is true and allowedTargets is empty, got none")
	}
	if !hasWarning(warnings, "allowGatewayTargets") {
		t.Errorf("expected warning mentioning allowGatewayTargets, got %v", warnings)
	}
}

func TestValidateGatewayConfig_AcceptsValidFilterByTags(t *testing.T) {
	cases := []struct {
		name string
		tag  string
	}{
		{"Simple", "code"},
		{"WithHyphen", "code-review"},
		{"WithDigits", "v2-api"},
		{"SingleChar", "a"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			agent := gatewayAgent()
			agent.Spec.Gateway.FilterByTags = []string{tc.tag}
			errs, _ := ValidateGatewayConfig(context.Background(), fakeClient(), agent)
			if len(errs) != 0 {
				t.Errorf("expected no errors for valid filterByTags entry %q, got %v", tc.tag, errs)
			}
		})
	}
}

func TestValidateGatewayConfig_AcceptsValidAllowedTargets(t *testing.T) {
	cases := []struct {
		name   string
		target string
	}{
		{"Simple", "agent"},
		{"WithHyphen", "code-reviewer"},
		{"WithDigits", "agent-v2"},
		{"SingleChar", "a"},
		{"AllDigits", "42"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			agent := gatewayAgent()
			agent.Spec.Gateway.AllowedTargets = []string{tc.target}
			errs, _ := ValidateGatewayConfig(context.Background(), fakeClient(), agent)
			if len(errs) != 0 {
				t.Errorf("expected no errors for valid allowedTargets entry %q, got %v", tc.target, errs)
			}
		})
	}
}
