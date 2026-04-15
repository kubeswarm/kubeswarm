package v1alpha1

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
)

func TestSanitiseAdvisorToolName(t *testing.T) {
	cases := []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		{
			name:  "simple lowercase",
			input: "architect",
			want:  "consult_architect",
		},
		{
			name:  "mixed case with hyphen",
			input: "My-Architect",
			want:  "consult_my_architect",
		},
		{
			name:  "double hyphens collapsed",
			input: "Security--Review",
			want:  "consult_security_review",
		},
		{
			name:  "leading number preserved",
			input: "123-invalid",
			want:  "consult_123_invalid",
		},
		{
			name:  "all uppercase",
			input: "UPPER",
			want:  "consult_upper",
		},
		{
			name:  "special chars stripped",
			input: "my@agent!",
			want:  "consult_myagent",
		},
		{
			name:  "underscores preserved",
			input: "my_agent",
			want:  "consult_my_agent",
		},
		{
			name:  "dots and spaces stripped",
			input: "my.agent name",
			want:  "consult_myagentname",
		},
		{
			name:    "empty after sanitisation",
			input:   "@#$%",
			wantErr: true,
		},
		{
			name:    "empty input",
			input:   "",
			wantErr: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := SanitiseAdvisorToolName(tc.input)
			if tc.wantErr {
				if err == nil {
					t.Errorf("SanitiseAdvisorToolName(%q) = %q, want error", tc.input, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("SanitiseAdvisorToolName(%q) unexpected error: %v", tc.input, err)
			}
			if got != tc.want {
				t.Errorf("SanitiseAdvisorToolName(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

func TestResolveAdvisorToolName(t *testing.T) {
	cases := []struct {
		name    string
		conn    AgentConnection
		want    string
		wantErr bool
	}{
		{
			name: "override via ToolName",
			conn: AgentConnection{
				Name:     "architect",
				AgentRef: &corev1.LocalObjectReference{Name: "arch"},
				Role:     AgentConnectionRoleAdvisor,
				ContextPropagation: &ContextPropagationConfig{
					ToolName: "ask_architect",
				},
			},
			want: "ask_architect",
		},
		{
			name: "derived from connection name",
			conn: AgentConnection{
				Name:               "My-Security-Review",
				AgentRef:           &corev1.LocalObjectReference{Name: "sec"},
				Role:               AgentConnectionRoleAdvisor,
				ContextPropagation: &ContextPropagationConfig{},
			},
			want: "consult_my_security_review",
		},
		{
			name: "derived when contextPropagation nil",
			conn: AgentConnection{
				Name:     "helper",
				AgentRef: &corev1.LocalObjectReference{Name: "h"},
				Role:     AgentConnectionRoleAdvisor,
			},
			want: "consult_helper",
		},
		{
			name: "empty ToolName falls back to derived",
			conn: AgentConnection{
				Name:     "planner",
				AgentRef: &corev1.LocalObjectReference{Name: "p"},
				Role:     AgentConnectionRoleAdvisor,
				ContextPropagation: &ContextPropagationConfig{
					ToolName: "",
				},
			},
			want: "consult_planner",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ResolveAdvisorToolName(tc.conn)
			if tc.wantErr {
				if err == nil {
					t.Errorf("ResolveAdvisorToolName() = %q, want error", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ResolveAdvisorToolName() unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("ResolveAdvisorToolName() = %q, want %q", got, tc.want)
			}
		})
	}
}
