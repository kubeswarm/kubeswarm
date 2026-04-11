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

package costs

import "context"

// HardStopPolicy wraps a BudgetPolicy and checks spend store health before
// delegating. When the store is unhealthy it reads input.HardStop to decide:
// true = fail-closed (BudgetExceeded), false = fail-open (BudgetOK).
// This allows per-SwarmBudget hardStop configuration.
type HardStopPolicy struct {
	inner   BudgetPolicy
	healthy func() bool
}

// NewHardStopPolicy returns a policy that guards inner with a health check.
// The hardStop decision is read from BudgetInput.HardStop on each evaluation,
// allowing per-budget configuration.
func NewHardStopPolicy(inner BudgetPolicy, healthy func() bool) *HardStopPolicy {
	return &HardStopPolicy{
		inner:   inner,
		healthy: healthy,
	}
}

// Evaluate checks store health first. If the store is healthy, it delegates
// to the inner policy. If not, it returns a decision based on input.HardStop
// without calling the inner policy.
func (p *HardStopPolicy) Evaluate(ctx context.Context, input BudgetInput, store SpendStore) (BudgetDecision, error) {
	if p.healthy() {
		return p.inner.Evaluate(ctx, input, store)
	}

	if input.HardStop {
		return BudgetDecision{
			Status:  BudgetExceeded,
			Message: "spend store unavailable, hard stop enforced",
		}, nil
	}

	return BudgetDecision{
		Status:  BudgetOK,
		Message: "spend store unavailable, fail-open: budget enforcement paused",
	}, nil
}
