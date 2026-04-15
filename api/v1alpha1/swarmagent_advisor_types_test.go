package v1alpha1

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// TestAgentConnectionRoleConstants asserts the string values of the role enum.
func TestAgentConnectionRoleConstants(t *testing.T) {
	cases := []struct {
		name string
		got  AgentConnectionRole
		want string
	}{
		{"Tool", AgentConnectionRoleTool, "tool"},
		{"Advisor", AgentConnectionRoleAdvisor, "advisor"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if string(tc.got) != tc.want {
				t.Errorf("AgentConnectionRole %s = %q, want %q", tc.name, string(tc.got), tc.want)
			}
		})
	}
}

// TestAgentConnectionRoleEnumeration guards against accidental addition/removal.
func TestAgentConnectionRoleEnumeration(t *testing.T) {
	all := []AgentConnectionRole{AgentConnectionRoleTool, AgentConnectionRoleAdvisor}
	if len(all) != 2 {
		t.Fatalf("expected 2 AgentConnectionRole values, got %d", len(all))
	}
}

// TestContextPropagationConfigFields verifies all fields exist with correct types.
func TestContextPropagationConfigFields(t *testing.T) {
	cfg := ContextPropagationConfig{
		RecentMessages:          20,
		MaxCallsPerTask:         3,
		TimeoutSeconds:          60,
		MaxAdvisorTokensPerTask: 50000,
		MaxContextBytes:         262144,
		ExcludeSystemPrompt:     true,
		ToolName:                "review_security",
	}
	if cfg.RecentMessages != 20 {
		t.Errorf("RecentMessages = %d, want 20", cfg.RecentMessages)
	}
	if cfg.MaxCallsPerTask != 3 {
		t.Errorf("MaxCallsPerTask = %d, want 3", cfg.MaxCallsPerTask)
	}
	if cfg.TimeoutSeconds != 60 {
		t.Errorf("TimeoutSeconds = %d, want 60", cfg.TimeoutSeconds)
	}
	if cfg.MaxAdvisorTokensPerTask != 50000 {
		t.Errorf("MaxAdvisorTokensPerTask = %d, want 50000", cfg.MaxAdvisorTokensPerTask)
	}
	if cfg.MaxContextBytes != 262144 {
		t.Errorf("MaxContextBytes = %d, want 262144", cfg.MaxContextBytes)
	}
	if !cfg.ExcludeSystemPrompt {
		t.Error("ExcludeSystemPrompt should be true")
	}
	if cfg.ToolName != "review_security" {
		t.Errorf("ToolName = %q, want %q", cfg.ToolName, "review_security")
	}
}

// TestAgentConnectionAdvisorFields verifies Role and ContextPropagation exist on AgentConnection.
func TestAgentConnectionAdvisorFields(t *testing.T) {
	conn := AgentConnection{
		Name:     "architect",
		AgentRef: &corev1.LocalObjectReference{Name: "senior-arch"},
		Role:     AgentConnectionRoleAdvisor,
		ContextPropagation: &ContextPropagationConfig{
			RecentMessages:  30,
			MaxCallsPerTask: 5,
			TimeoutSeconds:  90,
		},
	}
	if conn.Role != AgentConnectionRoleAdvisor {
		t.Errorf("Role = %q, want %q", conn.Role, AgentConnectionRoleAdvisor)
	}
	if conn.ContextPropagation == nil {
		t.Fatal("ContextPropagation should not be nil")
	}
	if conn.ContextPropagation.RecentMessages != 30 {
		t.Errorf("RecentMessages = %d, want 30", conn.ContextPropagation.RecentMessages)
	}
}

// TestAgentConnectionDefaultRole verifies that omitting Role gives zero value.
func TestAgentConnectionDefaultRole(t *testing.T) {
	conn := AgentConnection{
		Name:     "formatter",
		AgentRef: &corev1.LocalObjectReference{Name: "fmt"},
	}
	// Zero value for AgentConnectionRole is "" - kubebuilder default is "tool" at API level.
	if conn.Role != "" {
		t.Errorf("unset Role = %q, want empty string (zero value)", conn.Role)
	}
}

// TestAdvisorConnectionStatusFields verifies all fields on the status type.
func TestAdvisorConnectionStatusFields(t *testing.T) {
	now := metav1.Now()
	s := AdvisorConnectionStatus{
		Name:               "architect",
		Ready:              true,
		ToolInjected:       true,
		ToolName:           "consult_architect",
		LastTransitionTime: now,
	}
	if s.Name != "architect" {
		t.Errorf("Name = %q, want %q", s.Name, "architect")
	}
	if !s.Ready {
		t.Error("Ready should be true")
	}
	if !s.ToolInjected {
		t.Error("ToolInjected should be true")
	}
	if s.ToolName != "consult_architect" {
		t.Errorf("ToolName = %q, want %q", s.ToolName, "consult_architect")
	}
	if s.LastTransitionTime.IsZero() {
		t.Error("LastTransitionTime should not be zero")
	}
}

// TestSwarmAgentStatusAdvisorConnections verifies the field exists on SwarmAgentStatus.
func TestSwarmAgentStatusAdvisorConnections(t *testing.T) {
	status := SwarmAgentStatus{
		AdvisorConnections: []AdvisorConnectionStatus{
			{Name: "arch", Ready: true, ToolInjected: true, ToolName: "consult_arch"},
			{Name: "sec", Ready: false, ToolInjected: false, ToolName: "review_security"},
		},
	}
	if len(status.AdvisorConnections) != 2 {
		t.Fatalf("AdvisorConnections length = %d, want 2", len(status.AdvisorConnections))
	}
	if status.AdvisorConnections[0].Name != "arch" {
		t.Errorf("first advisor name = %q, want %q", status.AdvisorConnections[0].Name, "arch")
	}
}

// TestTokenUsageModelField verifies the Model field exists on TokenUsage.
func TestTokenUsageModelField(t *testing.T) {
	usage := TokenUsage{
		InputTokens:  1000,
		OutputTokens: 500,
		TotalTokens:  1500,
		Model:        "claude-opus-4-6",
	}
	if usage.Model != "claude-opus-4-6" {
		t.Errorf("Model = %q, want %q", usage.Model, "claude-opus-4-6")
	}
}

// TestTokenUsageModelFieldEmpty verifies empty Model is valid (non-advisor calls).
func TestTokenUsageModelFieldEmpty(t *testing.T) {
	usage := TokenUsage{
		InputTokens:  1000,
		OutputTokens: 500,
		TotalTokens:  1500,
	}
	if usage.Model != "" {
		t.Errorf("Model = %q, want empty string", usage.Model)
	}
}
