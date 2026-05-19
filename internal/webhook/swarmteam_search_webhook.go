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
	"fmt"

	kubeswarmv1alpha1 "github.com/kubeswarm/kubeswarm/api/v1alpha1"
)

// ValidateSearchConfig validates the search configuration on a SwarmTeam.
// Returns a slice of blocking errors. Role reference checks are performed here
// instead of CEL because roles.exists() traversal exceeds the CRD cost budget.
func ValidateSearchConfig(team *kubeswarmv1alpha1.SwarmTeam) []error {
	search := team.Spec.Search
	if search == nil {
		return nil
	}

	var errs []error

	roleNames := make(map[string]bool, len(team.Spec.Roles))
	for _, r := range team.Spec.Roles {
		roleNames[r.Name] = true
	}

	if !roleNames[search.PlannerRole] {
		errs = append(errs, fmt.Errorf(
			"spec.search.plannerRole %q must reference an entry in spec.roles", search.PlannerRole))
	}

	if !roleNames[search.ExecutorRole] {
		errs = append(errs, fmt.Errorf(
			"spec.search.executorRole %q must reference an entry in spec.roles", search.ExecutorRole))
	}

	if search.EvaluatorRole != nil && !roleNames[*search.EvaluatorRole] {
		errs = append(errs, fmt.Errorf(
			"spec.search.evaluatorRole %q must reference an entry in spec.roles", *search.EvaluatorRole))
	}

	return errs
}
