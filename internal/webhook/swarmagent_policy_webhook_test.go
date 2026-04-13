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
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kubeswarmv1alpha1 "github.com/kubeswarm/kubeswarm/api/v1alpha1"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func policyFakeClient(objs ...client.Object) client.Client {
	return fake.NewClientBuilder().
		WithScheme(scheme.Scheme).
		WithObjects(objs...).
		Build()
}

func makeTestPolicy(name, ns string, spec kubeswarmv1alpha1.SwarmPolicySpec) *kubeswarmv1alpha1.SwarmPolicy {
	return &kubeswarmv1alpha1.SwarmPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec:       spec,
	}
}

func makeTestAgent() *kubeswarmv1alpha1.SwarmAgent {
	return &kubeswarmv1alpha1.SwarmAgent{
		ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default"},
		Spec: kubeswarmv1alpha1.SwarmAgentSpec{
			Model:  "claude-sonnet-4-6",
			Prompt: &kubeswarmv1alpha1.AgentPrompt{Inline: "You are helpful."},
		},
	}
}

//go:fix inline
func pi64(v int64) *int64 { return new(v) }

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestPolicyWebhook_NoPolicies_Allowed(t *testing.T) {
	v := NewSwarmAgentPolicyValidator(agentDecoder(), policyFakeClient())
	agent := makeTestAgent()
	resp := v.Handle(context.Background(), buildAgentRequest(agent, nil))
	if !resp.Allowed {
		t.Fatalf("expected allowed with no policies, got denied: %s", resp.Result.Message)
	}
}

func TestPolicyWebhook_CompliantAgent_Allowed(t *testing.T) {
	pol := makeTestPolicy("baseline", "default", kubeswarmv1alpha1.SwarmPolicySpec{
		EnforcementMode: kubeswarmv1alpha1.PolicyEnforcementEnforce,
		Limits:          &kubeswarmv1alpha1.PolicyLimits{MaxDailyTokens: pi64(100000)},
	})

	v := NewSwarmAgentPolicyValidator(agentDecoder(), policyFakeClient(pol))
	agent := makeTestAgent()
	resp := v.Handle(context.Background(), buildAgentRequest(agent, nil))
	if !resp.Allowed {
		t.Fatalf("expected compliant agent allowed, got denied: %s", resp.Result.Message)
	}
}

func TestPolicyWebhook_Enforce_Denied(t *testing.T) {
	pol := makeTestPolicy("strict", "default", kubeswarmv1alpha1.SwarmPolicySpec{
		EnforcementMode: kubeswarmv1alpha1.PolicyEnforcementEnforce,
		Limits:          &kubeswarmv1alpha1.PolicyLimits{MaxDailyTokens: pi64(50000)},
	})

	v := NewSwarmAgentPolicyValidator(agentDecoder(), policyFakeClient(pol))
	agent := makeTestAgent()
	agent.Spec.Guardrails = &kubeswarmv1alpha1.AgentGuardrails{
		Limits: &kubeswarmv1alpha1.GuardrailLimits{DailyTokens: 200000},
	}

	resp := v.Handle(context.Background(), buildAgentRequest(agent, nil))
	if resp.Allowed {
		t.Fatal("expected denied in Enforce mode")
	}
	if resp.Result == nil || resp.Result.Message == "" {
		t.Fatal("expected denial message")
	}
}

func TestPolicyWebhook_Warn_AllowedWithWarnings(t *testing.T) {
	pol := makeTestPolicy("warn-pol", "default", kubeswarmv1alpha1.SwarmPolicySpec{
		EnforcementMode: kubeswarmv1alpha1.PolicyEnforcementWarn,
		Limits:          &kubeswarmv1alpha1.PolicyLimits{MaxDailyTokens: pi64(50000)},
	})

	v := NewSwarmAgentPolicyValidator(agentDecoder(), policyFakeClient(pol))
	agent := makeTestAgent()
	agent.Spec.Guardrails = &kubeswarmv1alpha1.AgentGuardrails{
		Limits: &kubeswarmv1alpha1.GuardrailLimits{DailyTokens: 200000},
	}

	resp := v.Handle(context.Background(), buildAgentRequest(agent, nil))
	if !resp.Allowed {
		t.Fatalf("expected allowed in Warn mode, got denied: %s", resp.Result.Message)
	}
	if len(resp.Warnings) == 0 {
		t.Fatal("expected warnings in Warn mode")
	}
}

func TestPolicyWebhook_Audit_AllowedNoWarnings(t *testing.T) {
	pol := makeTestPolicy("audit-pol", "default", kubeswarmv1alpha1.SwarmPolicySpec{
		EnforcementMode: kubeswarmv1alpha1.PolicyEnforcementAudit,
		Limits:          &kubeswarmv1alpha1.PolicyLimits{MaxDailyTokens: pi64(50000)},
	})

	v := NewSwarmAgentPolicyValidator(agentDecoder(), policyFakeClient(pol))
	agent := makeTestAgent()
	agent.Spec.Guardrails = &kubeswarmv1alpha1.AgentGuardrails{
		Limits: &kubeswarmv1alpha1.GuardrailLimits{DailyTokens: 200000},
	}

	resp := v.Handle(context.Background(), buildAgentRequest(agent, nil))
	if !resp.Allowed {
		t.Fatalf("expected allowed in Audit mode, got denied: %s", resp.Result.Message)
	}
	// Audit mode: no admission warnings (violations logged server-side only).
	if len(resp.Warnings) != 0 {
		t.Errorf("expected no warnings in Audit mode, got %v", resp.Warnings)
	}
}

func TestPolicyWebhook_ModelDenied_Enforce(t *testing.T) {
	pol := makeTestPolicy("deny-model", "default", kubeswarmv1alpha1.SwarmPolicySpec{
		EnforcementMode: kubeswarmv1alpha1.PolicyEnforcementEnforce,
		Models:          &kubeswarmv1alpha1.PolicyModels{Denied: []string{"gpt-*"}},
	})

	v := NewSwarmAgentPolicyValidator(agentDecoder(), policyFakeClient(pol))
	agent := makeTestAgent()
	agent.Spec.Model = "gpt-4o-mini"

	resp := v.Handle(context.Background(), buildAgentRequest(agent, nil))
	if resp.Allowed {
		t.Fatal("expected denied for denied model in Enforce mode")
	}
}

func TestPolicyWebhook_RequirementBudgetRef_Enforce(t *testing.T) {
	pol := makeTestPolicy("require-budget", "default", kubeswarmv1alpha1.SwarmPolicySpec{
		EnforcementMode: kubeswarmv1alpha1.PolicyEnforcementEnforce,
		Requirements:    kubeswarmv1alpha1.PolicyRequirements{BudgetRef: true},
	})

	v := NewSwarmAgentPolicyValidator(agentDecoder(), policyFakeClient(pol))
	agent := makeTestAgent()

	resp := v.Handle(context.Background(), buildAgentRequest(agent, nil))
	if resp.Allowed {
		t.Fatal("expected denied for missing budgetRef in Enforce mode")
	}
}

func TestPolicyWebhook_MultiplePolicies_StrictestModeWins(t *testing.T) {
	// Audit policy + Enforce policy -> effective mode is Enforce.
	pol1 := makeTestPolicy("audit", "default", kubeswarmv1alpha1.SwarmPolicySpec{
		EnforcementMode: kubeswarmv1alpha1.PolicyEnforcementAudit,
		Limits:          &kubeswarmv1alpha1.PolicyLimits{MaxDailyTokens: pi64(100000)},
	})
	pol2 := makeTestPolicy("enforce", "default", kubeswarmv1alpha1.SwarmPolicySpec{
		EnforcementMode: kubeswarmv1alpha1.PolicyEnforcementEnforce,
		Limits:          &kubeswarmv1alpha1.PolicyLimits{MaxDailyTokens: pi64(50000)},
	})

	v := NewSwarmAgentPolicyValidator(agentDecoder(), policyFakeClient(pol1, pol2))
	agent := makeTestAgent()
	agent.Spec.Guardrails = &kubeswarmv1alpha1.AgentGuardrails{
		Limits: &kubeswarmv1alpha1.GuardrailLimits{DailyTokens: 75000},
	}

	resp := v.Handle(context.Background(), buildAgentRequest(agent, nil))
	if resp.Allowed {
		t.Fatal("expected denied: strictest mode is Enforce and agent exceeds 50000 ceiling")
	}
}

func TestPolicyWebhook_DifferentNamespace_NoPolicies(t *testing.T) {
	// Policy in "production" namespace, agent in "default" - no overlap.
	pol := makeTestPolicy("prod-pol", "production", kubeswarmv1alpha1.SwarmPolicySpec{
		EnforcementMode: kubeswarmv1alpha1.PolicyEnforcementEnforce,
		Requirements:    kubeswarmv1alpha1.PolicyRequirements{BudgetRef: true},
	})

	v := NewSwarmAgentPolicyValidator(agentDecoder(), policyFakeClient(pol))
	agent := makeTestAgent() // Different namespace.

	resp := v.Handle(context.Background(), buildAgentRequest(agent, nil))
	if !resp.Allowed {
		t.Fatalf("expected allowed - no policies in agent's namespace, got denied: %s", resp.Result.Message)
	}
}
