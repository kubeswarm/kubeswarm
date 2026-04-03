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

import (
	"context"
	"fmt"
	"time"
)

// BudgetDecision is the result of a budget policy evaluation.
type BudgetDecision struct {
	// Status is the outcome: OK, Warning, or Exceeded.
	Status BudgetStatus
	// SpentUSD is the total spend for the period.
	SpentUSD float64
	// LimitUSD is the configured limit.
	LimitUSD float64
	// PctUsed is SpentUSD / LimitUSD expressed as a value from 0 to 100+.
	PctUsed float64
	// Message is a human-readable summary.
	Message string
}

// BudgetStatus mirrors kubeswarmv1alpha1.BudgetStatus to avoid an import cycle.
type BudgetStatus string

const (
	BudgetOK       BudgetStatus = "OK"
	BudgetWarning  BudgetStatus = "Warning"
	BudgetExceeded BudgetStatus = "Exceeded"
)

// BudgetInput contains everything needed to evaluate a budget, without
// importing the API types (avoids import cycles).
type BudgetInput struct {
	// Selector mirrors SwarmBudgetSelector fields.
	Namespace string
	Team      string

	// Period is "daily", "weekly", or "monthly".
	Period string

	// Limit is the maximum spend for the period.
	Limit float64

	// WarnAt is the warn threshold as a percentage (0–100).
	WarnAt int
}

// BudgetPolicy evaluates current spend against a budget limit.
type BudgetPolicy interface {
	// Evaluate returns a decision based on current SpendStore data.
	// store and scope are provided by the caller; the policy calls store.Total.
	Evaluate(ctx context.Context, input BudgetInput, store SpendStore) (BudgetDecision, error)
}

// StandardBudgetPolicy is the built-in budget policy. It calls SpendStore.Total
// for the current period window and compares it against the limit.
type StandardBudgetPolicy struct{}

// Evaluate checks current spend for the period and returns OK / Warning / Exceeded.
func (p *StandardBudgetPolicy) Evaluate(ctx context.Context, input BudgetInput, store SpendStore) (BudgetDecision, error) {
	periodStart := periodWindowStart(input.Period)

	scope := SpendScope{
		Namespace: input.Namespace,
		Team:      input.Team,
	}
	spent, err := store.Total(ctx, scope, periodStart)
	if err != nil {
		return BudgetDecision{}, fmt.Errorf("budget policy: querying spend total: %w", err)
	}

	pct := 0.0
	if input.Limit > 0 {
		pct = (spent / input.Limit) * 100
	}

	decision := BudgetDecision{
		SpentUSD: spent,
		LimitUSD: input.Limit,
		PctUsed:  pct,
	}

	switch {
	case input.Limit > 0 && spent >= input.Limit:
		decision.Status = BudgetExceeded
		decision.Message = fmt.Sprintf("budget exceeded: spent $%.4f of $%.2f limit (%.1f%%)", spent, input.Limit, pct)
	case input.WarnAt > 0 && pct >= float64(input.WarnAt):
		decision.Status = BudgetWarning
		decision.Message = fmt.Sprintf("budget warning: spent $%.4f of $%.2f limit (%.1f%%)", spent, input.Limit, pct)
	default:
		decision.Status = BudgetOK
		decision.Message = fmt.Sprintf("budget OK: spent $%.4f of $%.2f limit (%.1f%%)", spent, input.Limit, pct)
	}
	return decision, nil
}

// PeriodWindowStart returns the start of the current budget window for the given period string.
// Accepts "daily"/"day", "weekly"/"week", "monthly"/"month".
func PeriodWindowStart(period string) time.Time {
	return periodWindowStart(period)
}

func periodWindowStart(period string) time.Time {
	now := time.Now().UTC()
	switch period {
	case "weekly", "week":
		return TruncateToPeriod(now, PeriodWeek)
	case "monthly", "month":
		return TruncateToPeriod(now, PeriodMonth)
	default: // daily / day
		return TruncateToPeriod(now, PeriodDay)
	}
}

// DefaultBudgetPolicy returns the standard built-in policy.
func DefaultBudgetPolicy() BudgetPolicy {
	return &StandardBudgetPolicy{}
}
