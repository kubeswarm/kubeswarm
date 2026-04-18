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
	"testing"

	admissionv1 "k8s.io/api/admission/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	kubeswarmv1alpha1 "github.com/kubeswarm/kubeswarm/api/v1alpha1"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func buildTeamPolicyRequest(team *kubeswarmv1alpha1.SwarmTeam) admission.Request {
	raw, _ := json.Marshal(team)
	return admission.Request{
		AdmissionRequest: admissionv1.AdmissionRequest{
			Object:    runtime.RawExtension{Raw: raw},
			Namespace: team.Namespace,
		},
	}
}

func makeTestSwarmPolicy(name, ns string, spec kubeswarmv1alpha1.SwarmPolicySpec) *kubeswarmv1alpha1.SwarmPolicy {
	return &kubeswarmv1alpha1.SwarmPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec:       spec,
	}
}

func minimalTeam(steps []kubeswarmv1alpha1.SwarmTeamPipelineStep) *kubeswarmv1alpha1.SwarmTeam {
	return &kubeswarmv1alpha1.SwarmTeam{
		ObjectMeta: metav1.ObjectMeta{Name: "test-team", Namespace: "default"},
		Spec: kubeswarmv1alpha1.SwarmTeamSpec{
			Roles: []kubeswarmv1alpha1.SwarmTeamRole{
				{Name: "worker", Model: "claude-sonnet-4-6"},
			},
			Pipeline: steps,
		},
	}
}

// ---------------------------------------------------------------------------
// Tests: checkStepValidationLevel pure function
// ---------------------------------------------------------------------------

func TestCheckStepValidationLevel_None_AlwaysTrue(t *testing.T) {
	cases := []struct {
		name string
		step kubeswarmv1alpha1.SwarmTeamPipelineStep
	}{
		{
			name: "nil validate",
			step: kubeswarmv1alpha1.SwarmTeamPipelineStep{Role: "worker", Validate: nil},
		},
		{
			name: "empty validate",
			step: kubeswarmv1alpha1.SwarmTeamPipelineStep{Role: "worker", Validate: &kubeswarmv1alpha1.StepValidation{}},
		},
		{
			name: "validate with patterns",
			step: kubeswarmv1alpha1.SwarmTeamPipelineStep{
				Role: "worker",
				Validate: &kubeswarmv1alpha1.StepValidation{
					RejectPatterns: []string{"(?i)ignore"},
				},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if !checkStepValidationLevel(tc.step, kubeswarmv1alpha1.PolicyOutputLevelNone) {
				t.Error("expected true for minLevel=none")
			}
		})
	}
}

func TestCheckStepValidationLevel_EmptyString_SameAsNone(t *testing.T) {
	step := kubeswarmv1alpha1.SwarmTeamPipelineStep{Role: "worker", Validate: nil}
	if !checkStepValidationLevel(step, "") {
		t.Error("expected true for empty minLevel (same as none)")
	}
}

func TestCheckStepValidationLevel_Pattern_WithRejectPatterns(t *testing.T) {
	step := kubeswarmv1alpha1.SwarmTeamPipelineStep{
		Role: "worker",
		Validate: &kubeswarmv1alpha1.StepValidation{
			RejectPatterns: []string{"(?i)ignore.*previous"},
		},
	}
	if !checkStepValidationLevel(step, kubeswarmv1alpha1.PolicyOutputLevelPattern) {
		t.Error("expected true when rejectPatterns are set")
	}
}

func TestCheckStepValidationLevel_Pattern_WithoutRejectPatterns(t *testing.T) {
	step := kubeswarmv1alpha1.SwarmTeamPipelineStep{
		Role: "worker",
		Validate: &kubeswarmv1alpha1.StepValidation{
			Schema: `{"type":"object"}`,
		},
	}
	if checkStepValidationLevel(step, kubeswarmv1alpha1.PolicyOutputLevelPattern) {
		t.Error("expected false when rejectPatterns are empty (even with schema set)")
	}
}

func TestCheckStepValidationLevel_Schema_WithSchema(t *testing.T) {
	step := kubeswarmv1alpha1.SwarmTeamPipelineStep{
		Role: "worker",
		Validate: &kubeswarmv1alpha1.StepValidation{
			Schema: `{"type":"object","required":["result"]}`,
		},
	}
	if !checkStepValidationLevel(step, kubeswarmv1alpha1.PolicyOutputLevelSchema) {
		t.Error("expected true when schema is set")
	}
}

func TestCheckStepValidationLevel_Schema_WithoutSchema(t *testing.T) {
	step := kubeswarmv1alpha1.SwarmTeamPipelineStep{
		Role: "worker",
		Validate: &kubeswarmv1alpha1.StepValidation{
			RejectPatterns: []string{"(?i)ignore"},
		},
	}
	if checkStepValidationLevel(step, kubeswarmv1alpha1.PolicyOutputLevelSchema) {
		t.Error("expected false when schema is empty (even with rejectPatterns set)")
	}
}

func TestCheckStepValidationLevel_Semantic_WithSemantic(t *testing.T) {
	step := kubeswarmv1alpha1.SwarmTeamPipelineStep{
		Role: "worker",
		Validate: &kubeswarmv1alpha1.StepValidation{
			Semantic: "Does {{ .output }} contain a valid summary?",
		},
	}
	if !checkStepValidationLevel(step, kubeswarmv1alpha1.PolicyOutputLevelSemantic) {
		t.Error("expected true when semantic is set")
	}
}

func TestCheckStepValidationLevel_Semantic_WithoutSemantic(t *testing.T) {
	step := kubeswarmv1alpha1.SwarmTeamPipelineStep{
		Role: "worker",
		Validate: &kubeswarmv1alpha1.StepValidation{
			Schema: `{"type":"object"}`,
		},
	}
	if checkStepValidationLevel(step, kubeswarmv1alpha1.PolicyOutputLevelSemantic) {
		t.Error("expected false when semantic is empty (even with schema set)")
	}
}

func TestCheckStepValidationLevel_NilValidate_FalseForNonNone(t *testing.T) {
	step := kubeswarmv1alpha1.SwarmTeamPipelineStep{Role: "worker", Validate: nil}

	levels := []kubeswarmv1alpha1.PolicyOutputLevel{
		kubeswarmv1alpha1.PolicyOutputLevelPattern,
		kubeswarmv1alpha1.PolicyOutputLevelSchema,
		kubeswarmv1alpha1.PolicyOutputLevelSemantic,
	}

	for _, level := range levels {
		t.Run(string(level), func(t *testing.T) {
			if checkStepValidationLevel(step, level) {
				t.Errorf("expected false for nil Validate with minLevel=%s", level)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Tests: SwarmTeamPolicyValidator.Handle webhook handler
// ---------------------------------------------------------------------------

func TestTeamPolicyWebhook_NoPolicies_Allowed(t *testing.T) {
	v := NewSwarmTeamPolicyValidator(agentDecoder(), policyFakeClient())
	team := minimalTeam([]kubeswarmv1alpha1.SwarmTeamPipelineStep{
		{Role: "worker"},
	})

	resp := v.Handle(context.Background(), buildTeamPolicyRequest(team))
	if !resp.Allowed {
		t.Fatalf("expected allowed with no policies, got denied: %s", resp.Result.Message)
	}
}

func TestTeamPolicyWebhook_MinValidationNone_Allowed(t *testing.T) {
	pol := makeTestSwarmPolicy("permissive", "default", kubeswarmv1alpha1.SwarmPolicySpec{
		EnforcementMode: kubeswarmv1alpha1.PolicyEnforcementEnforce,
		Output: &kubeswarmv1alpha1.PolicyOutput{
			MinValidation: kubeswarmv1alpha1.PolicyOutputLevelNone,
		},
	})

	v := NewSwarmTeamPolicyValidator(agentDecoder(), policyFakeClient(pol))
	team := minimalTeam([]kubeswarmv1alpha1.SwarmTeamPipelineStep{
		{Role: "worker"}, // no validation configured
	})

	resp := v.Handle(context.Background(), buildTeamPolicyRequest(team))
	if !resp.Allowed {
		t.Fatalf("expected allowed with minValidation=none, got denied: %s", resp.Result.Message)
	}
}

func TestTeamPolicyWebhook_Pattern_Enforce_MissingRejectPatterns_Denied(t *testing.T) {
	pol := makeTestSwarmPolicy("require-pattern", "default", kubeswarmv1alpha1.SwarmPolicySpec{
		EnforcementMode: kubeswarmv1alpha1.PolicyEnforcementEnforce,
		Output: &kubeswarmv1alpha1.PolicyOutput{
			MinValidation: kubeswarmv1alpha1.PolicyOutputLevelPattern,
		},
	})

	v := NewSwarmTeamPolicyValidator(agentDecoder(), policyFakeClient(pol))
	team := minimalTeam([]kubeswarmv1alpha1.SwarmTeamPipelineStep{
		{Role: "worker", Validate: &kubeswarmv1alpha1.StepValidation{
			Schema: `{"type":"object"}`, // has schema but no rejectPatterns
		}},
	})

	resp := v.Handle(context.Background(), buildTeamPolicyRequest(team))
	if resp.Allowed {
		t.Fatal("expected denied when step missing rejectPatterns and minValidation=pattern in Enforce mode")
	}
	if resp.Result == nil || resp.Result.Message == "" {
		t.Fatal("expected denial message")
	}
}

func TestTeamPolicyWebhook_Pattern_StepHasRejectPatterns_Allowed(t *testing.T) {
	pol := makeTestSwarmPolicy("require-pattern", "default", kubeswarmv1alpha1.SwarmPolicySpec{
		EnforcementMode: kubeswarmv1alpha1.PolicyEnforcementEnforce,
		Output: &kubeswarmv1alpha1.PolicyOutput{
			MinValidation: kubeswarmv1alpha1.PolicyOutputLevelPattern,
		},
	})

	v := NewSwarmTeamPolicyValidator(agentDecoder(), policyFakeClient(pol))
	team := minimalTeam([]kubeswarmv1alpha1.SwarmTeamPipelineStep{
		{Role: "worker", Validate: &kubeswarmv1alpha1.StepValidation{
			RejectPatterns: []string{"(?i)ignore.*previous.*instructions"},
		}},
	})

	resp := v.Handle(context.Background(), buildTeamPolicyRequest(team))
	if !resp.Allowed {
		t.Fatalf("expected allowed when step has rejectPatterns, got denied: %s", resp.Result.Message)
	}
}

func TestTeamPolicyWebhook_Schema_Enforce_MissingSchema_Denied(t *testing.T) {
	pol := makeTestSwarmPolicy("require-schema", "default", kubeswarmv1alpha1.SwarmPolicySpec{
		EnforcementMode: kubeswarmv1alpha1.PolicyEnforcementEnforce,
		Output: &kubeswarmv1alpha1.PolicyOutput{
			MinValidation: kubeswarmv1alpha1.PolicyOutputLevelSchema,
		},
	})

	v := NewSwarmTeamPolicyValidator(agentDecoder(), policyFakeClient(pol))
	team := minimalTeam([]kubeswarmv1alpha1.SwarmTeamPipelineStep{
		{Role: "worker", Validate: &kubeswarmv1alpha1.StepValidation{
			RejectPatterns: []string{"(?i)ignore"},
		}},
	})

	resp := v.Handle(context.Background(), buildTeamPolicyRequest(team))
	if resp.Allowed {
		t.Fatal("expected denied when step missing schema and minValidation=schema in Enforce mode")
	}
}

func TestTeamPolicyWebhook_Semantic_Warn_MissingSemantic_AllowedWithWarnings(t *testing.T) {
	pol := makeTestSwarmPolicy("require-semantic", "default", kubeswarmv1alpha1.SwarmPolicySpec{
		EnforcementMode: kubeswarmv1alpha1.PolicyEnforcementWarn,
		Output: &kubeswarmv1alpha1.PolicyOutput{
			MinValidation: kubeswarmv1alpha1.PolicyOutputLevelSemantic,
		},
	})

	v := NewSwarmTeamPolicyValidator(agentDecoder(), policyFakeClient(pol))
	team := minimalTeam([]kubeswarmv1alpha1.SwarmTeamPipelineStep{
		{Role: "worker", Validate: &kubeswarmv1alpha1.StepValidation{
			Schema: `{"type":"object"}`, // has schema but no semantic
		}},
	})

	resp := v.Handle(context.Background(), buildTeamPolicyRequest(team))
	if !resp.Allowed {
		t.Fatalf("expected allowed in Warn mode, got denied: %s", resp.Result.Message)
	}
	if len(resp.Warnings) == 0 {
		t.Fatal("expected warnings in Warn mode for missing semantic validation")
	}
}

func TestTeamPolicyWebhook_Pattern_Audit_Missing_AllowedNoWarnings(t *testing.T) {
	pol := makeTestSwarmPolicy("audit-pattern", "default", kubeswarmv1alpha1.SwarmPolicySpec{
		EnforcementMode: kubeswarmv1alpha1.PolicyEnforcementAudit,
		Output: &kubeswarmv1alpha1.PolicyOutput{
			MinValidation: kubeswarmv1alpha1.PolicyOutputLevelPattern,
		},
	})

	v := NewSwarmTeamPolicyValidator(agentDecoder(), policyFakeClient(pol))
	team := minimalTeam([]kubeswarmv1alpha1.SwarmTeamPipelineStep{
		{Role: "worker"}, // no validation at all
	})

	resp := v.Handle(context.Background(), buildTeamPolicyRequest(team))
	if !resp.Allowed {
		t.Fatalf("expected allowed in Audit mode, got denied: %s", resp.Result.Message)
	}
	if len(resp.Warnings) != 0 {
		t.Errorf("expected no warnings in Audit mode, got %v", resp.Warnings)
	}
}

func TestTeamPolicyWebhook_NoPipelineSteps_Allowed(t *testing.T) {
	pol := makeTestSwarmPolicy("strict", "default", kubeswarmv1alpha1.SwarmPolicySpec{
		EnforcementMode: kubeswarmv1alpha1.PolicyEnforcementEnforce,
		Output: &kubeswarmv1alpha1.PolicyOutput{
			MinValidation: kubeswarmv1alpha1.PolicyOutputLevelSemantic,
		},
	})

	v := NewSwarmTeamPolicyValidator(agentDecoder(), policyFakeClient(pol))
	team := &kubeswarmv1alpha1.SwarmTeam{
		ObjectMeta: metav1.ObjectMeta{Name: "test-team", Namespace: "default"},
		Spec: kubeswarmv1alpha1.SwarmTeamSpec{
			Roles: []kubeswarmv1alpha1.SwarmTeamRole{
				{Name: "worker", Model: "claude-sonnet-4-6"},
			},
			// No pipeline steps - dynamic mode
		},
	}

	resp := v.Handle(context.Background(), buildTeamPolicyRequest(team))
	if !resp.Allowed {
		t.Fatalf("expected allowed with no pipeline steps, got denied: %s", resp.Result.Message)
	}
}

func TestTeamPolicyWebhook_MultipleSteps_OneMissingValidation_Denied(t *testing.T) {
	pol := makeTestSwarmPolicy("require-pattern", "default", kubeswarmv1alpha1.SwarmPolicySpec{
		EnforcementMode: kubeswarmv1alpha1.PolicyEnforcementEnforce,
		Output: &kubeswarmv1alpha1.PolicyOutput{
			MinValidation: kubeswarmv1alpha1.PolicyOutputLevelPattern,
		},
	})

	v := NewSwarmTeamPolicyValidator(agentDecoder(), policyFakeClient(pol))
	team := &kubeswarmv1alpha1.SwarmTeam{
		ObjectMeta: metav1.ObjectMeta{Name: "test-team", Namespace: "default"},
		Spec: kubeswarmv1alpha1.SwarmTeamSpec{
			Roles: []kubeswarmv1alpha1.SwarmTeamRole{
				{Name: "researcher", Model: "claude-sonnet-4-6"},
				{Name: "writer", Model: "claude-sonnet-4-6"},
			},
			Pipeline: []kubeswarmv1alpha1.SwarmTeamPipelineStep{
				{
					Role: "researcher",
					Validate: &kubeswarmv1alpha1.StepValidation{
						RejectPatterns: []string{"(?i)ignore"},
					},
				},
				{
					Role:      "writer",
					DependsOn: []string{"researcher"},
					// Missing rejectPatterns - violates pattern requirement
				},
			},
		},
	}

	resp := v.Handle(context.Background(), buildTeamPolicyRequest(team))
	if resp.Allowed {
		t.Fatal("expected denied when one step is missing required validation in Enforce mode")
	}
}

func TestTeamPolicyWebhook_MultiplePolicies_StrictestMinValidationWins(t *testing.T) {
	// One policy requires pattern, another requires schema.
	// Strictest (schema) should win.
	pol1 := makeTestSwarmPolicy("pattern-pol", "default", kubeswarmv1alpha1.SwarmPolicySpec{
		EnforcementMode: kubeswarmv1alpha1.PolicyEnforcementEnforce,
		Output: &kubeswarmv1alpha1.PolicyOutput{
			MinValidation: kubeswarmv1alpha1.PolicyOutputLevelPattern,
		},
	})
	pol2 := makeTestSwarmPolicy("schema-pol", "default", kubeswarmv1alpha1.SwarmPolicySpec{
		EnforcementMode: kubeswarmv1alpha1.PolicyEnforcementEnforce,
		Output: &kubeswarmv1alpha1.PolicyOutput{
			MinValidation: kubeswarmv1alpha1.PolicyOutputLevelSchema,
		},
	})

	v := NewSwarmTeamPolicyValidator(agentDecoder(), policyFakeClient(pol1, pol2))
	// Team has rejectPatterns but no schema - satisfies pattern but not schema.
	team := minimalTeam([]kubeswarmv1alpha1.SwarmTeamPipelineStep{
		{Role: "worker", Validate: &kubeswarmv1alpha1.StepValidation{
			RejectPatterns: []string{"(?i)ignore"},
		}},
	})

	resp := v.Handle(context.Background(), buildTeamPolicyRequest(team))
	if resp.Allowed {
		t.Fatal("expected denied: strictest minValidation is schema but step only has pattern")
	}
}

func TestTeamPolicyWebhook_MultiplePolicies_StrictestSatisfied_Allowed(t *testing.T) {
	pol1 := makeTestSwarmPolicy("pattern-pol", "default", kubeswarmv1alpha1.SwarmPolicySpec{
		EnforcementMode: kubeswarmv1alpha1.PolicyEnforcementEnforce,
		Output: &kubeswarmv1alpha1.PolicyOutput{
			MinValidation: kubeswarmv1alpha1.PolicyOutputLevelPattern,
		},
	})
	pol2 := makeTestSwarmPolicy("schema-pol", "default", kubeswarmv1alpha1.SwarmPolicySpec{
		EnforcementMode: kubeswarmv1alpha1.PolicyEnforcementEnforce,
		Output: &kubeswarmv1alpha1.PolicyOutput{
			MinValidation: kubeswarmv1alpha1.PolicyOutputLevelSchema,
		},
	})

	v := NewSwarmTeamPolicyValidator(agentDecoder(), policyFakeClient(pol1, pol2))
	// Team satisfies the strictest level (schema).
	team := minimalTeam([]kubeswarmv1alpha1.SwarmTeamPipelineStep{
		{Role: "worker", Validate: &kubeswarmv1alpha1.StepValidation{
			Schema: `{"type":"object","required":["result"]}`,
		}},
	})

	resp := v.Handle(context.Background(), buildTeamPolicyRequest(team))
	if !resp.Allowed {
		t.Fatalf("expected allowed when step satisfies strictest minValidation, got denied: %s", resp.Result.Message)
	}
}

func TestTeamPolicyWebhook_DifferentNamespace_NoPolicyEffect(t *testing.T) {
	// Policy in "production", team in "default" - no overlap.
	pol := makeTestSwarmPolicy("prod-pol", "production", kubeswarmv1alpha1.SwarmPolicySpec{
		EnforcementMode: kubeswarmv1alpha1.PolicyEnforcementEnforce,
		Output: &kubeswarmv1alpha1.PolicyOutput{
			MinValidation: kubeswarmv1alpha1.PolicyOutputLevelSemantic,
		},
	})

	v := NewSwarmTeamPolicyValidator(agentDecoder(), policyFakeClient(pol))
	team := minimalTeam([]kubeswarmv1alpha1.SwarmTeamPipelineStep{
		{Role: "worker"}, // no validation - would fail if policy applied
	})

	resp := v.Handle(context.Background(), buildTeamPolicyRequest(team))
	if !resp.Allowed {
		t.Fatalf("expected allowed - no policies in team's namespace, got denied: %s", resp.Result.Message)
	}
}
