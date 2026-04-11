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
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// --- Mock SpendStore ---

type mockSpendStore struct {
	mu           sync.Mutex
	recordCalls  []SpendEntry
	totalResult  float64
	totalErr     error
	rollupResult []RollupEntry
	rollupErr    error
}

func (m *mockSpendStore) Record(_ context.Context, entry SpendEntry) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.recordCalls = append(m.recordCalls, entry)
	return nil
}

func (m *mockSpendStore) Rollup(_ context.Context, _ SpendScope, _ Period, _ time.Time) ([]RollupEntry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.rollupResult, m.rollupErr
}

func (m *mockSpendStore) Total(_ context.Context, _ SpendScope, _ time.Time) (float64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.totalResult, m.totalErr
}

func (m *mockSpendStore) getRecordCalls() []SpendEntry {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make([]SpendEntry, len(m.recordCalls))
	copy(result, m.recordCalls)
	return result
}

// --- Tests ---

func TestResilientSpendStore_FactorySucceeds_DelegatesToRealStore(t *testing.T) {
	t.Parallel()

	real := &mockSpendStore{totalResult: 42.5}
	factory := func() (SpendStore, error) { return real, nil }

	r := NewResilientSpendStore(factory)
	defer r.Close()

	ctx := context.Background()

	// Record should delegate to real store
	entry := SpendEntry{CostUSD: 1.23, Namespace: "default"}
	if err := r.Record(ctx, entry); err != nil {
		t.Fatalf("Record() = %v, want nil", err)
	}

	calls := real.getRecordCalls()
	if len(calls) != 1 {
		t.Fatalf("real store got %d Record calls, want 1", len(calls))
	}
	if calls[0].CostUSD != 1.23 {
		t.Errorf("CostUSD = %f, want 1.23", calls[0].CostUSD)
	}

	// Total should delegate to real store
	total, err := r.Total(ctx, SpendScope{}, time.Time{})
	if err != nil {
		t.Fatalf("Total() error = %v", err)
	}
	if total != 42.5 {
		t.Errorf("Total() = %f, want 42.5", total)
	}
}

func TestResilientSpendStore_FactoryFails_UsesNoop(t *testing.T) {
	t.Parallel()

	factory := func() (SpendStore, error) {
		return nil, errors.New("connection refused")
	}

	r := NewResilientSpendStore(factory)
	defer r.Close()

	ctx := context.Background()

	// Record should return nil (noop behavior)
	err := r.Record(ctx, SpendEntry{CostUSD: 1.0})
	if err != nil {
		t.Errorf("Record() = %v, want nil (noop)", err)
	}

	// Total should return 0 (noop behavior)
	total, err := r.Total(ctx, SpendScope{}, time.Time{})
	if err != nil {
		t.Errorf("Total() error = %v, want nil", err)
	}
	if total != 0 {
		t.Errorf("Total() = %f, want 0 (noop)", total)
	}

	// Rollup should return nil (noop behavior)
	rollup, err := r.Rollup(ctx, SpendScope{}, PeriodDay, time.Time{})
	if err != nil {
		t.Errorf("Rollup() error = %v, want nil", err)
	}
	if rollup != nil {
		t.Errorf("Rollup() = %v, want nil (noop)", rollup)
	}
}

func TestResilientSpendStore_FactoryFailsThenSucceeds_SwapsToRealStore(t *testing.T) {
	t.Parallel()

	var attempts atomic.Int32
	real := &mockSpendStore{totalResult: 99.9}

	factory := func() (SpendStore, error) {
		n := attempts.Add(1)
		if n <= 2 {
			return nil, errors.New("not ready yet")
		}
		return real, nil
	}

	r := NewResilientSpendStore(factory, WithRetryInterval(10*time.Millisecond))
	defer r.Close()

	// Wait for background retry to succeed
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if r.IsHealthy() {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	if !r.IsHealthy() {
		t.Fatal("store did not become healthy after retries")
	}

	ctx := context.Background()
	total, err := r.Total(ctx, SpendScope{}, time.Time{})
	if err != nil {
		t.Fatalf("Total() error = %v", err)
	}
	if total != 99.9 {
		t.Errorf("Total() = %f, want 99.9 (from real store)", total)
	}
}

func TestResilientSpendStore_IsHealthy_FalseWhenNoop_TrueWhenReal(t *testing.T) {
	t.Parallel()

	factory := func() (SpendStore, error) {
		return nil, errors.New("unavailable")
	}

	r := NewResilientSpendStore(factory, WithRetryInterval(1*time.Hour)) // no retry during test
	defer r.Close()

	if r.IsHealthy() {
		t.Error("IsHealthy() = true, want false when using noop")
	}
}

func TestResilientSpendStore_IsHealthy_TrueWhenFactorySucceeds(t *testing.T) {
	t.Parallel()

	real := &mockSpendStore{}
	factory := func() (SpendStore, error) { return real, nil }

	r := NewResilientSpendStore(factory)
	defer r.Close()

	if !r.IsHealthy() {
		t.Error("IsHealthy() = false, want true when real store is connected")
	}
}

func TestResilientSpendStore_ThreadSafeDuringSwap(t *testing.T) {
	t.Parallel()

	var attempts atomic.Int32
	real := &mockSpendStore{totalResult: 10.0}

	factory := func() (SpendStore, error) {
		n := attempts.Add(1)
		if n <= 3 {
			return nil, errors.New("not ready")
		}
		return real, nil
	}

	r := NewResilientSpendStore(factory, WithRetryInterval(5*time.Millisecond))
	defer r.Close()

	ctx := context.Background()
	const goroutines = 20
	const opsPerGoroutine = 50

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for range goroutines {
		go func() {
			defer wg.Done()
			for range opsPerGoroutine {
				_ = r.Record(ctx, SpendEntry{CostUSD: 0.01})
				_, _ = r.Total(ctx, SpendScope{}, time.Time{})
				_, _ = r.Rollup(ctx, SpendScope{}, PeriodDay, time.Time{})
			}
		}()
	}
	wg.Wait()
	// Test passes if no panic or data race (run with -race)
}

func TestResilientSpendStore_Close_StopsBackgroundRetry(t *testing.T) {
	t.Parallel()

	var attempts atomic.Int32
	factory := func() (SpendStore, error) {
		attempts.Add(1)
		return nil, errors.New("always fail")
	}

	r := NewResilientSpendStore(factory, WithRetryInterval(5*time.Millisecond))

	// Let some retries happen
	time.Sleep(50 * time.Millisecond)
	r.Close()

	countAtClose := attempts.Load()

	// Wait and verify no more retries happen
	time.Sleep(50 * time.Millisecond)
	countAfterClose := attempts.Load()

	if countAfterClose > countAtClose+1 {
		t.Errorf("retries continued after Close: at close=%d, after=%d", countAtClose, countAfterClose)
	}
}

func TestResilientSpendStore_ConcurrentRecordDuringSwap_NoPanic(t *testing.T) {
	t.Parallel()

	var attempts atomic.Int32
	real := &mockSpendStore{}

	factory := func() (SpendStore, error) {
		n := attempts.Add(1)
		if n <= 5 {
			return nil, errors.New("not ready")
		}
		return real, nil
	}

	r := NewResilientSpendStore(factory, WithRetryInterval(2*time.Millisecond))
	defer r.Close()

	ctx := context.Background()
	const goroutines = 30

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for range goroutines {
		go func() {
			defer wg.Done()
			for range 100 {
				_ = r.Record(ctx, SpendEntry{CostUSD: 0.01})
				time.Sleep(time.Millisecond)
			}
		}()
	}
	wg.Wait()
	// Success = no panic or race condition
}

func TestResilientSpendStore_AfterSwap_AllCallsGoToRealStore(t *testing.T) {
	t.Parallel()

	var attempts atomic.Int32
	real := &mockSpendStore{totalResult: 55.5}

	factory := func() (SpendStore, error) {
		n := attempts.Add(1)
		if n <= 1 {
			return nil, errors.New("first attempt fails")
		}
		return real, nil
	}

	r := NewResilientSpendStore(factory, WithRetryInterval(10*time.Millisecond))
	defer r.Close()

	// Wait for swap
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if r.IsHealthy() {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	if !r.IsHealthy() {
		t.Fatal("store never became healthy")
	}

	ctx := context.Background()

	// Verify Record goes to real store
	_ = r.Record(ctx, SpendEntry{CostUSD: 7.77, Namespace: "ns"})
	calls := real.getRecordCalls()
	if len(calls) == 0 {
		t.Error("Record did not delegate to real store after swap")
	}

	// Verify Total goes to real store
	total, err := r.Total(ctx, SpendScope{}, time.Time{})
	if err != nil {
		t.Fatalf("Total() error = %v", err)
	}
	if total != 55.5 {
		t.Errorf("Total() = %f, want 55.5", total)
	}
}
