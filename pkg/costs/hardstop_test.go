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
	"sync/atomic"
	"testing"
	"time"
)

// --- Mock BudgetPolicy ---

type mockBudgetPolicy struct {
	decision  BudgetDecision
	err       error
	callCount atomic.Int32
}

func (m *mockBudgetPolicy) Evaluate(_ context.Context, _ BudgetInput, _ SpendStore) (BudgetDecision, error) {
	m.callCount.Add(1)
	return m.decision, m.err
}

// --- Mock SpendStore for HardStop tests ---

type stubSpendStore struct {
	totalResult float64
}

func (s *stubSpendStore) Record(_ context.Context, _ SpendEntry) error { return nil }
func (s *stubSpendStore) Rollup(_ context.Context, _ SpendScope, _ Period, _ time.Time) ([]RollupEntry, error) {
	return nil, nil
}
func (s *stubSpendStore) Total(_ context.Context, _ SpendScope, _ time.Time) (float64, error) {
	return s.totalResult, nil
}

// --- Tests ---

func TestHardStopPolicy_Healthy_HardStopTrue_DelegatesToInner(t *testing.T) {
	t.Parallel()

	inner := &mockBudgetPolicy{
		decision: BudgetDecision{Status: BudgetOK, Message: "all good"},
	}
	p := NewHardStopPolicy(inner, func() bool { return true })
	input := BudgetInput{Namespace: "default", Period: "daily", Limit: 100.0, HardStop: true}

	got, err := p.Evaluate(context.Background(), input, &stubSpendStore{})
	if err != nil {
		t.Fatalf("Evaluate() error = %v", err)
	}
	if got.Status != BudgetOK {
		t.Errorf("Status = %q, want %q", got.Status, BudgetOK)
	}
	if got.Message != "all good" {
		t.Errorf("Message = %q, want %q", got.Message, "all good")
	}
	if inner.callCount.Load() != 1 {
		t.Errorf("inner policy called %d times, want 1", inner.callCount.Load())
	}
}

func TestHardStopPolicy_Healthy_HardStopFalse_DelegatesToInner(t *testing.T) {
	t.Parallel()

	inner := &mockBudgetPolicy{
		decision: BudgetDecision{Status: BudgetWarning, Message: "spending fast"},
	}
	p := NewHardStopPolicy(inner, func() bool { return true })
	input := BudgetInput{Namespace: "default", Period: "daily", Limit: 100.0, HardStop: false}

	got, err := p.Evaluate(context.Background(), input, &stubSpendStore{})
	if err != nil {
		t.Fatalf("Evaluate() error = %v", err)
	}
	if got.Status != BudgetWarning {
		t.Errorf("Status = %q, want %q", got.Status, BudgetWarning)
	}
	if inner.callCount.Load() != 1 {
		t.Errorf("inner policy called %d times, want 1", inner.callCount.Load())
	}
}

func TestHardStopPolicy_Unhealthy_HardStopTrue_ReturnsExceeded(t *testing.T) {
	t.Parallel()

	inner := &mockBudgetPolicy{decision: BudgetDecision{Status: BudgetOK}}
	p := NewHardStopPolicy(inner, func() bool { return false })
	input := BudgetInput{Namespace: "default", Period: "daily", Limit: 100.0, HardStop: true}

	got, err := p.Evaluate(context.Background(), input, &stubSpendStore{})
	if err != nil {
		t.Fatalf("Evaluate() error = %v", err)
	}
	if got.Status != BudgetExceeded {
		t.Errorf("Status = %q, want %q", got.Status, BudgetExceeded)
	}
}

func TestHardStopPolicy_Unhealthy_HardStopFalse_ReturnsOK(t *testing.T) {
	t.Parallel()

	inner := &mockBudgetPolicy{decision: BudgetDecision{Status: BudgetExceeded}}
	p := NewHardStopPolicy(inner, func() bool { return false })
	input := BudgetInput{Namespace: "default", Period: "daily", Limit: 100.0, HardStop: false}

	got, err := p.Evaluate(context.Background(), input, &stubSpendStore{})
	if err != nil {
		t.Fatalf("Evaluate() error = %v", err)
	}
	if got.Status != BudgetOK {
		t.Errorf("Status = %q, want %q (fail-open)", got.Status, BudgetOK)
	}
}

func TestHardStopPolicy_Unhealthy_HardStopTrue_InnerNotCalled(t *testing.T) {
	t.Parallel()

	inner := &mockBudgetPolicy{decision: BudgetDecision{Status: BudgetOK}}
	p := NewHardStopPolicy(inner, func() bool { return false })
	input := BudgetInput{Namespace: "default", Period: "daily", Limit: 100.0, HardStop: true}

	_, _ = p.Evaluate(context.Background(), input, &stubSpendStore{})
	if inner.callCount.Load() != 0 {
		t.Errorf("inner called %d times, want 0 when store is unhealthy", inner.callCount.Load())
	}
}

func TestHardStopPolicy_Unhealthy_HardStopFalse_InnerNotCalled(t *testing.T) {
	t.Parallel()

	inner := &mockBudgetPolicy{decision: BudgetDecision{Status: BudgetOK}}
	p := NewHardStopPolicy(inner, func() bool { return false })
	input := BudgetInput{Namespace: "default", Period: "daily", Limit: 100.0, HardStop: false}

	_, _ = p.Evaluate(context.Background(), input, &stubSpendStore{})
	if inner.callCount.Load() != 0 {
		t.Errorf("inner called %d times, want 0 when store is unhealthy (fail-open)", inner.callCount.Load())
	}
}

func TestHardStopPolicy_StoreRecovers_DelegatesToInnerAgain(t *testing.T) {
	t.Parallel()

	inner := &mockBudgetPolicy{
		decision: BudgetDecision{Status: BudgetOK, Message: "resumed"},
	}
	var isHealthy atomic.Bool
	isHealthy.Store(false)

	p := NewHardStopPolicy(inner, func() bool { return isHealthy.Load() })
	input := BudgetInput{Namespace: "default", Period: "daily", Limit: 100.0, HardStop: true}

	// Unhealthy -> Exceeded
	got, _ := p.Evaluate(context.Background(), input, &stubSpendStore{})
	if got.Status != BudgetExceeded {
		t.Errorf("unhealthy: Status = %q, want %q", got.Status, BudgetExceeded)
	}

	// Recover
	isHealthy.Store(true)
	got, _ = p.Evaluate(context.Background(), input, &stubSpendStore{})
	if got.Status != BudgetOK {
		t.Errorf("recovered: Status = %q, want %q", got.Status, BudgetOK)
	}
	if inner.callCount.Load() != 1 {
		t.Errorf("recovered: inner called %d times, want 1", inner.callCount.Load())
	}
}

func TestHardStopPolicy_PerBudgetHardStop(t *testing.T) {
	t.Parallel()

	inner := &mockBudgetPolicy{decision: BudgetDecision{Status: BudgetOK}}
	p := NewHardStopPolicy(inner, func() bool { return false }) // store unhealthy

	// Budget A: hardStop=true -> Exceeded
	inputA := BudgetInput{Namespace: "a", Period: "daily", Limit: 100.0, HardStop: true}
	gotA, _ := p.Evaluate(context.Background(), inputA, &stubSpendStore{})
	if gotA.Status != BudgetExceeded {
		t.Errorf("hardStop=true budget: Status = %q, want %q", gotA.Status, BudgetExceeded)
	}

	// Budget B: hardStop=false -> OK (fail-open)
	inputB := BudgetInput{Namespace: "b", Period: "daily", Limit: 100.0, HardStop: false}
	gotB, _ := p.Evaluate(context.Background(), inputB, &stubSpendStore{})
	if gotB.Status != BudgetOK {
		t.Errorf("hardStop=false budget: Status = %q, want %q", gotB.Status, BudgetOK)
	}
}
