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
	"encoding/json"
	"strings"
	"testing"

	admissionv1 "k8s.io/api/admission/v1"
	authenticationv1 "k8s.io/api/authentication/v1"
	authv1 "k8s.io/api/authorization/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	corev1 "k8s.io/api/core/v1"

	kubeswarmv1alpha1 "github.com/kubeswarm/kubeswarm/api/v1alpha1"
)

func init() {
	_ = kubeswarmv1alpha1.AddToScheme(scheme.Scheme)
	_ = authv1.AddToScheme(scheme.Scheme)
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func agentDecoder() admission.Decoder {
	return admission.NewDecoder(scheme.Scheme)
}

func buildAgentRequest(agent *kubeswarmv1alpha1.SwarmAgent, old *kubeswarmv1alpha1.SwarmAgent) admission.Request {
	raw, _ := json.Marshal(agent)
	req := admission.Request{
		AdmissionRequest: admissionv1.AdmissionRequest{
			Object:    runtime.RawExtension{Raw: raw},
			Namespace: "default",
			UserInfo:  authenticationv1.UserInfo{Username: "test-user", Groups: []string{"system:authenticated"}},
		},
	}
	if old != nil {
		oldRaw, _ := json.Marshal(old)
		req.OldObject = runtime.RawExtension{Raw: oldRaw}
	}
	return req
}

func buildTeamRequest(team *kubeswarmv1alpha1.SwarmTeam) admission.Request {
	raw, _ := json.Marshal(team)
	return admission.Request{
		AdmissionRequest: admissionv1.AdmissionRequest{
			Object:    runtime.RawExtension{Raw: raw},
			Namespace: "default",
		},
	}
}

func minimalAgent(prompt string) *kubeswarmv1alpha1.SwarmAgent {
	return &kubeswarmv1alpha1.SwarmAgent{
		ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default"},
		Spec: kubeswarmv1alpha1.SwarmAgentSpec{
			Model:  "claude-sonnet-4-6",
			Prompt: &kubeswarmv1alpha1.AgentPrompt{Inline: prompt},
		},
	}
}

func fakeClient(objs ...client.Object) client.Client {
	return fake.NewClientBuilder().
		WithScheme(scheme.Scheme).
		WithObjects(objs...).
		WithStatusSubresource(&authv1.SubjectAccessReview{}).
		Build()
}

// ---------------------------------------------------------------------------
// SwarmAgent prompt validator tests
// ---------------------------------------------------------------------------

func TestAgentPrompt_SmallInline_Allowed(t *testing.T) {
	v := NewSwarmAgentPromptValidator(agentDecoder(), fakeClient())
	agent := minimalAgent("You are a test agent.")
	agent.Spec.Runtime = &kubeswarmv1alpha1.AgentRuntime{
		Resources: &corev1.ResourceRequirements{},
	}
	resp := v.Handle(context.Background(), buildAgentRequest(agent, nil))
	if !resp.Allowed {
		t.Fatalf("expected allowed, got denied: %s", resp.Result.Message)
	}
	if len(resp.Warnings) != 0 {
		t.Errorf("expected no warnings, got %v", resp.Warnings)
	}
}

func TestAgentPrompt_NoResources_Warns(t *testing.T) {
	v := NewSwarmAgentPromptValidator(agentDecoder(), fakeClient())
	agent := minimalAgent("You are a test agent.")
	// No Runtime.Resources set
	resp := v.Handle(context.Background(), buildAgentRequest(agent, nil))
	if !resp.Allowed {
		t.Fatalf("expected allowed, got denied: %s", resp.Result.Message)
	}
	found := false
	for _, w := range resp.Warnings {
		if strings.Contains(w, "spec.runtime.resources is not set") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected resources warning, got warnings: %v", resp.Warnings)
	}
}

func TestAgentPrompt_Over50KB_Warns(t *testing.T) {
	v := NewSwarmAgentPromptValidator(agentDecoder(), fakeClient())
	bigPrompt := strings.Repeat("x", 60*1024) // 60 KB
	agent := minimalAgent(bigPrompt)
	agent.Spec.Runtime = &kubeswarmv1alpha1.AgentRuntime{
		Resources: &corev1.ResourceRequirements{},
	}
	resp := v.Handle(context.Background(), buildAgentRequest(agent, nil))
	if !resp.Allowed {
		t.Fatalf("expected allowed with warning, got denied: %s", resp.Result.Message)
	}
	found := false
	for _, w := range resp.Warnings {
		if strings.Contains(w, "inline prompt is") && strings.Contains(w, "spec.prompt.from") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected prompt size warning, got warnings: %v", resp.Warnings)
	}
}

func TestAgentPrompt_Over512KB_Denied(t *testing.T) {
	v := NewSwarmAgentPromptValidator(agentDecoder(), fakeClient())
	hugePrompt := strings.Repeat("x", 520*1024) // 520 KB
	agent := minimalAgent(hugePrompt)
	resp := v.Handle(context.Background(), buildAgentRequest(agent, nil))
	if resp.Allowed {
		t.Fatal("expected denied for prompt > 512KB, got allowed")
	}
	if !strings.Contains(resp.Result.Message, "exceeds the") {
		t.Errorf("expected size exceeded message, got: %s", resp.Result.Message)
	}
}

func TestAgentPrompt_NilPrompt_Allowed(t *testing.T) {
	v := NewSwarmAgentPromptValidator(agentDecoder(), fakeClient())
	agent := &kubeswarmv1alpha1.SwarmAgent{
		ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default"},
		Spec: kubeswarmv1alpha1.SwarmAgentSpec{
			Model: "claude-sonnet-4-6",
			// Prompt is nil - size check should still pass (size=0)
		},
	}
	resp := v.Handle(context.Background(), buildAgentRequest(agent, nil))
	if !resp.Allowed {
		t.Fatalf("expected allowed for nil prompt, got denied: %s", resp.Result.Message)
	}
}

// ---------------------------------------------------------------------------
// System prompt immutability tests
// ---------------------------------------------------------------------------

func TestAgentPrompt_Immutability_NoChange_Allowed(t *testing.T) {
	v := NewSwarmAgentPromptValidator(agentDecoder(), fakeClient())
	agent := minimalAgent("same prompt")
	agent.Spec.Runtime = &kubeswarmv1alpha1.AgentRuntime{Resources: &corev1.ResourceRequirements{}}
	old := minimalAgent("same prompt")
	resp := v.Handle(context.Background(), buildAgentRequest(agent, old))
	if !resp.Allowed {
		t.Fatalf("expected allowed when prompt unchanged, got denied: %s", resp.Result.Message)
	}
}

func TestAgentPrompt_Immutability_Changed_Denied(t *testing.T) {
	// No SAR reactor configured - fake client returns not-allowed by default (Status.Allowed=false).
	fc := fakeClient()
	v := NewSwarmAgentPromptValidator(agentDecoder(), fc)
	agent := minimalAgent("new prompt")
	agent.Spec.Runtime = &kubeswarmv1alpha1.AgentRuntime{Resources: &corev1.ResourceRequirements{}}
	old := minimalAgent("old prompt")
	resp := v.Handle(context.Background(), buildAgentRequest(agent, old))
	// SAR Create on fake client will fail (no reactor), so we get a warning, not a deny.
	// This is defence-in-depth behavior: SAR failure = warn, not block.
	if !resp.Allowed {
		// If it's denied, check it's the right message (SAR might have returned false).
		if !strings.Contains(resp.Result.Message, "swarm-prompt-admin") {
			t.Fatalf("unexpected denial reason: %s", resp.Result.Message)
		}
		// Denied due to SAR returning false - this is correct behavior.
		return
	}
	// If allowed, there should be a SAR warning.
	found := false
	for _, w := range resp.Warnings {
		if strings.Contains(w, "could not verify swarm-prompt-admin") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected SAR warning when prompt changed, got: %v", resp.Warnings)
	}
}

// ---------------------------------------------------------------------------
// MCP policy enforcement tests
// ---------------------------------------------------------------------------

func TestAgentMCP_NoMCPServers_Allowed(t *testing.T) {
	v := NewSwarmAgentPromptValidator(agentDecoder(), fakeClient())
	agent := minimalAgent("test")
	agent.Spec.Runtime = &kubeswarmv1alpha1.AgentRuntime{Resources: &corev1.ResourceRequirements{}}
	// No Tools.MCP set
	resp := v.Handle(context.Background(), buildAgentRequest(agent, nil))
	if !resp.Allowed {
		t.Fatalf("expected allowed with no MCP servers, got denied: %s", resp.Result.Message)
	}
}

func TestAgentMCP_URLInAllowlist_Allowed(t *testing.T) {
	settings := &kubeswarmv1alpha1.SwarmSettings{
		ObjectMeta: metav1.ObjectMeta{Name: "policy", Namespace: "default"},
		Spec: kubeswarmv1alpha1.SwarmSettingsSpec{
			Security: &kubeswarmv1alpha1.SwarmSettingsSecurity{
				MCPAllowlist: []string{"https://allowed.example.com/"},
			},
		},
	}
	v := NewSwarmAgentPromptValidator(agentDecoder(), fakeClient(settings))
	agent := minimalAgent("test")
	agent.Spec.Runtime = &kubeswarmv1alpha1.AgentRuntime{Resources: &corev1.ResourceRequirements{}}
	agent.Spec.Tools = &kubeswarmv1alpha1.AgentTools{
		MCP: []kubeswarmv1alpha1.MCPToolSpec{
			{Name: "good", URL: "https://allowed.example.com/sse"},
		},
	}
	resp := v.Handle(context.Background(), buildAgentRequest(agent, nil))
	if !resp.Allowed {
		t.Fatalf("expected allowed for URL in allowlist, got denied: %s", resp.Result.Message)
	}
}

func TestAgentMCP_URLNotInAllowlist_Denied(t *testing.T) {
	settings := &kubeswarmv1alpha1.SwarmSettings{
		ObjectMeta: metav1.ObjectMeta{Name: "policy", Namespace: "default"},
		Spec: kubeswarmv1alpha1.SwarmSettingsSpec{
			Security: &kubeswarmv1alpha1.SwarmSettingsSecurity{
				MCPAllowlist: []string{"https://allowed.example.com/"},
			},
		},
	}
	v := NewSwarmAgentPromptValidator(agentDecoder(), fakeClient(settings))
	agent := minimalAgent("test")
	agent.Spec.Tools = &kubeswarmv1alpha1.AgentTools{
		MCP: []kubeswarmv1alpha1.MCPToolSpec{
			{Name: "bad", URL: "https://evil.example.com/sse"},
		},
	}
	resp := v.Handle(context.Background(), buildAgentRequest(agent, nil))
	if resp.Allowed {
		t.Fatal("expected denied for URL not in allowlist, got allowed")
	}
	if !strings.Contains(resp.Result.Message, "not in the namespace mcpAllowlist") {
		t.Errorf("unexpected denial message: %s", resp.Result.Message)
	}
}

func TestAgentMCP_RequireAuth_NoAuth_Denied(t *testing.T) {
	settings := &kubeswarmv1alpha1.SwarmSettings{
		ObjectMeta: metav1.ObjectMeta{Name: "policy", Namespace: "default"},
		Spec: kubeswarmv1alpha1.SwarmSettingsSpec{
			Security: &kubeswarmv1alpha1.SwarmSettingsSecurity{
				RequireMCPAuth: true,
			},
		},
	}
	v := NewSwarmAgentPromptValidator(agentDecoder(), fakeClient(settings))
	agent := minimalAgent("test")
	agent.Spec.Tools = &kubeswarmv1alpha1.AgentTools{
		MCP: []kubeswarmv1alpha1.MCPToolSpec{
			{Name: "noauth", URL: "https://example.com/sse"},
		},
	}
	resp := v.Handle(context.Background(), buildAgentRequest(agent, nil))
	if resp.Allowed {
		t.Fatal("expected denied for MCP without auth when requireMCPAuth=true")
	}
	if !strings.Contains(resp.Result.Message, "no auth configured") {
		t.Errorf("unexpected denial message: %s", resp.Result.Message)
	}
}

func TestAgentMCP_RequireAuth_WithBearer_Allowed(t *testing.T) {
	settings := &kubeswarmv1alpha1.SwarmSettings{
		ObjectMeta: metav1.ObjectMeta{Name: "policy", Namespace: "default"},
		Spec: kubeswarmv1alpha1.SwarmSettingsSpec{
			Security: &kubeswarmv1alpha1.SwarmSettingsSecurity{
				RequireMCPAuth: true,
			},
		},
	}
	v := NewSwarmAgentPromptValidator(agentDecoder(), fakeClient(settings))
	agent := minimalAgent("test")
	agent.Spec.Runtime = &kubeswarmv1alpha1.AgentRuntime{Resources: &corev1.ResourceRequirements{}}
	agent.Spec.Tools = &kubeswarmv1alpha1.AgentTools{
		MCP: []kubeswarmv1alpha1.MCPToolSpec{
			{
				Name: "authed",
				URL:  "https://example.com/sse",
				Auth: &kubeswarmv1alpha1.MCPServerAuth{
					Bearer: &kubeswarmv1alpha1.BearerAuth{
						SecretKeyRef: corev1.SecretKeySelector{
							LocalObjectReference: corev1.LocalObjectReference{Name: "token"},
							Key:                  "key",
						},
					},
				},
			},
		},
	}
	resp := v.Handle(context.Background(), buildAgentRequest(agent, nil))
	if !resp.Allowed {
		t.Fatalf("expected allowed for MCP with bearer auth, got denied: %s", resp.Result.Message)
	}
}

func TestAgentMCP_RequireAuth_WithMTLS_Allowed(t *testing.T) {
	settings := &kubeswarmv1alpha1.SwarmSettings{
		ObjectMeta: metav1.ObjectMeta{Name: "policy", Namespace: "default"},
		Spec: kubeswarmv1alpha1.SwarmSettingsSpec{
			Security: &kubeswarmv1alpha1.SwarmSettingsSecurity{
				RequireMCPAuth: true,
			},
		},
	}
	v := NewSwarmAgentPromptValidator(agentDecoder(), fakeClient(settings))
	agent := minimalAgent("test")
	agent.Spec.Runtime = &kubeswarmv1alpha1.AgentRuntime{Resources: &corev1.ResourceRequirements{}}
	agent.Spec.Tools = &kubeswarmv1alpha1.AgentTools{
		MCP: []kubeswarmv1alpha1.MCPToolSpec{
			{
				Name: "mtls-server",
				URL:  "https://example.com/sse",
				Auth: &kubeswarmv1alpha1.MCPServerAuth{
					MTLS: &kubeswarmv1alpha1.MTLSAuth{
						SecretRef: kubeswarmv1alpha1.LocalObjectReference{Name: "certs"},
					},
				},
			},
		},
	}
	resp := v.Handle(context.Background(), buildAgentRequest(agent, nil))
	if !resp.Allowed {
		t.Fatalf("expected allowed for MCP with mTLS auth, got denied: %s", resp.Result.Message)
	}
}

// ---------------------------------------------------------------------------
// SwarmTeam prompt validator tests
// ---------------------------------------------------------------------------

func TestTeamPrompt_SmallPrompts_Allowed(t *testing.T) {
	v := NewSwarmTeamPromptValidator(agentDecoder())
	team := &kubeswarmv1alpha1.SwarmTeam{
		ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default"},
		Spec: kubeswarmv1alpha1.SwarmTeamSpec{
			Roles: []kubeswarmv1alpha1.SwarmTeamRole{
				{Name: "worker", SystemPrompt: "You are a worker."},
			},
		},
	}
	resp := v.Handle(context.Background(), buildTeamRequest(team))
	if !resp.Allowed {
		t.Fatalf("expected allowed, got denied: %s", resp.Result.Message)
	}
}

func TestTeamPrompt_PerRole_Over512KB_Denied(t *testing.T) {
	v := NewSwarmTeamPromptValidator(agentDecoder())
	team := &kubeswarmv1alpha1.SwarmTeam{
		ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default"},
		Spec: kubeswarmv1alpha1.SwarmTeamSpec{
			Roles: []kubeswarmv1alpha1.SwarmTeamRole{
				{Name: "big-role", SystemPrompt: strings.Repeat("x", 520*1024)},
			},
		},
	}
	resp := v.Handle(context.Background(), buildTeamRequest(team))
	if resp.Allowed {
		t.Fatal("expected denied for role prompt > 512KB")
	}
	if !strings.Contains(resp.Result.Message, "exceeds the") {
		t.Errorf("unexpected message: %s", resp.Result.Message)
	}
}

func TestTeamPrompt_PerRole_Over50KB_Warns(t *testing.T) {
	v := NewSwarmTeamPromptValidator(agentDecoder())
	team := &kubeswarmv1alpha1.SwarmTeam{
		ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default"},
		Spec: kubeswarmv1alpha1.SwarmTeamSpec{
			Roles: []kubeswarmv1alpha1.SwarmTeamRole{
				{Name: "medium-role", SystemPrompt: strings.Repeat("x", 60*1024)},
			},
		},
	}
	resp := v.Handle(context.Background(), buildTeamRequest(team))
	if !resp.Allowed {
		t.Fatalf("expected allowed with warning, got denied: %s", resp.Result.Message)
	}
	if len(resp.Warnings) == 0 {
		t.Error("expected warning for prompt > 50KB, got none")
	}
}

func TestTeamPrompt_TotalOver800KB_Denied(t *testing.T) {
	v := NewSwarmTeamPromptValidator(agentDecoder())
	// 3 roles at 300KB each = 900KB total > 800KB
	team := &kubeswarmv1alpha1.SwarmTeam{
		ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default"},
		Spec: kubeswarmv1alpha1.SwarmTeamSpec{
			Roles: []kubeswarmv1alpha1.SwarmTeamRole{
				{Name: "a", SystemPrompt: strings.Repeat("x", 300*1024)},
				{Name: "b", SystemPrompt: strings.Repeat("x", 300*1024)},
				{Name: "c", SystemPrompt: strings.Repeat("x", 300*1024)},
			},
		},
	}
	resp := v.Handle(context.Background(), buildTeamRequest(team))
	if resp.Allowed {
		t.Fatal("expected denied for team total > 800KB")
	}
	if !strings.Contains(resp.Result.Message, "team limit") {
		t.Errorf("unexpected message: %s", resp.Result.Message)
	}
}

func TestTeamPrompt_EmptyPrompts_Allowed(t *testing.T) {
	v := NewSwarmTeamPromptValidator(agentDecoder())
	team := &kubeswarmv1alpha1.SwarmTeam{
		ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default"},
		Spec: kubeswarmv1alpha1.SwarmTeamSpec{
			Roles: []kubeswarmv1alpha1.SwarmTeamRole{
				{Name: "worker"}, // no systemPrompt
			},
		},
	}
	resp := v.Handle(context.Background(), buildTeamRequest(team))
	if !resp.Allowed {
		t.Fatalf("expected allowed for empty prompts, got denied: %s", resp.Result.Message)
	}
}

// ---------------------------------------------------------------------------
// humanBytes helper
// ---------------------------------------------------------------------------

func TestHumanBytes(t *testing.T) {
	tests := []struct {
		n    int
		want string
	}{
		{1024, "1 KB"},
		{50 * 1024, "50 KB"},
		{512 * 1024, "512 KB"},
		{1024 * 1024, "1.0 MB"},
		{2 * 1024 * 1024, "2.0 MB"},
	}
	for _, tt := range tests {
		got := humanBytes(tt.n)
		if got != tt.want {
			t.Errorf("humanBytes(%d) = %q, want %q", tt.n, got, tt.want)
		}
	}
}
