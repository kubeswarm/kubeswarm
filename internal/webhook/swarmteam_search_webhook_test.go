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
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kubeswarmv1alpha1 "github.com/kubeswarm/kubeswarm/api/v1alpha1"
)

const missingRole = "no-such-role"

func searchTeam() *kubeswarmv1alpha1.SwarmTeam {
	return &kubeswarmv1alpha1.SwarmTeam{
		ObjectMeta: metav1.ObjectMeta{Name: "search-team", Namespace: "default"},
		Spec: kubeswarmv1alpha1.SwarmTeamSpec{
			Roles: []kubeswarmv1alpha1.SwarmTeamRole{
				{Name: "planner", Model: "test-model"},
				{Name: "executor", Model: "test-model"},
				{Name: "evaluator", Model: "test-model"},
			},
			Search: &kubeswarmv1alpha1.SwarmTeamSearchSpec{
				Strategy:      kubeswarmv1alpha1.SearchStrategyBFS,
				PlannerRole:   "planner",
				ExecutorRole:  "executor",
				InitialPrompt: "test",
			},
		},
	}
}

func TestValidateSearchConfig_ValidBFS_NoErrors(t *testing.T) {
	team := searchTeam()
	errs := ValidateSearchConfig(team)
	if len(errs) != 0 {
		t.Errorf("expected no errors for valid search config, got %v", errs)
	}
}

func TestValidateSearchConfig_NilSearch_NoErrors(t *testing.T) {
	team := searchTeam()
	team.Spec.Search = nil
	errs := ValidateSearchConfig(team)
	if len(errs) != 0 {
		t.Errorf("expected no errors when search is nil, got %v", errs)
	}
}

func TestValidateSearchConfig_PlannerRoleNotInRoles(t *testing.T) {
	team := searchTeam()
	team.Spec.Search.PlannerRole = missingRole
	errs := ValidateSearchConfig(team)
	if len(errs) == 0 {
		t.Fatal("expected error for plannerRole not in roles, got none")
	}
	if !strings.Contains(errs[0].Error(), "plannerRole") {
		t.Errorf("expected error mentioning plannerRole, got %v", errs[0])
	}
}

func TestValidateSearchConfig_ExecutorRoleNotInRoles(t *testing.T) {
	team := searchTeam()
	team.Spec.Search.ExecutorRole = missingRole
	errs := ValidateSearchConfig(team)
	if len(errs) == 0 {
		t.Fatal("expected error for executorRole not in roles, got none")
	}
	if !strings.Contains(errs[0].Error(), "executorRole") {
		t.Errorf("expected error mentioning executorRole, got %v", errs[0])
	}
}

func TestValidateSearchConfig_EvaluatorRoleNotInRoles(t *testing.T) {
	team := searchTeam()
	evalRole := missingRole
	team.Spec.Search.EvaluatorRole = &evalRole
	errs := ValidateSearchConfig(team)
	if len(errs) == 0 {
		t.Fatal("expected error for evaluatorRole not in roles, got none")
	}
	if !strings.Contains(errs[0].Error(), "evaluatorRole") {
		t.Errorf("expected error mentioning evaluatorRole, got %v", errs[0])
	}
}

func TestValidateSearchConfig_EvaluatorRoleNil_NoError(t *testing.T) {
	team := searchTeam()
	team.Spec.Search.EvaluatorRole = nil
	errs := ValidateSearchConfig(team)
	if len(errs) != 0 {
		t.Errorf("expected no errors when evaluatorRole is nil, got %v", errs)
	}
}

func TestValidateSearchConfig_EvaluatorRoleValid(t *testing.T) {
	team := searchTeam()
	evalRole := "evaluator"
	team.Spec.Search.EvaluatorRole = &evalRole
	errs := ValidateSearchConfig(team)
	if len(errs) != 0 {
		t.Errorf("expected no errors for valid evaluatorRole, got %v", errs)
	}
}

func TestValidateSearchConfig_MultipleRoleErrors(t *testing.T) {
	team := searchTeam()
	team.Spec.Search.PlannerRole = "missing-planner"
	team.Spec.Search.ExecutorRole = "missing-executor"
	evalRole := "missing-evaluator"
	team.Spec.Search.EvaluatorRole = &evalRole
	errs := ValidateSearchConfig(team)
	if len(errs) != 3 {
		t.Fatalf("expected 3 errors, got %d: %v", len(errs), errs)
	}
}
