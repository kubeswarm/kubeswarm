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

package circuit

import (
	"sync"
	"testing"
	"time"
)

// TestNewBreaker_StartsClosed verifies a new breaker is in StateClosed.
func TestNewBreaker_StartsClosed(t *testing.T) {
	b := NewBreaker(5, 1, 10*time.Millisecond)
	if b.State() != StateClosed {
		t.Errorf("State() = %q, want %q", b.State(), StateClosed)
	}
}

// TestBreaker_AllowInClosed verifies Allow() returns nil in closed state.
func TestBreaker_AllowInClosed(t *testing.T) {
	b := NewBreaker(5, 1, 10*time.Millisecond)
	if err := b.Allow(); err != nil {
		t.Errorf("Allow() in closed state = %v, want nil", err)
	}
}

// TestBreaker_OpensAfterThreshold verifies the breaker opens after reaching the failure threshold.
func TestBreaker_OpensAfterThreshold(t *testing.T) {
	threshold := 5
	b := NewBreaker(threshold, 1, 50*time.Millisecond)

	for range threshold {
		b.RecordFailure()
	}

	if b.State() != StateOpen {
		t.Errorf("State() after %d failures = %q, want %q", threshold, b.State(), StateOpen)
	}
	if err := b.Allow(); err != ErrCircuitOpen {
		t.Errorf("Allow() in open state = %v, want %v", err, ErrCircuitOpen)
	}
}

// TestBreaker_StaysClosedBelowThreshold verifies the breaker stays closed when failures are below threshold.
func TestBreaker_StaysClosedBelowThreshold(t *testing.T) {
	threshold := 5
	b := NewBreaker(threshold, 1, 50*time.Millisecond)

	for i := 0; i < threshold-1; i++ {
		b.RecordFailure()
	}

	if b.State() != StateClosed {
		t.Errorf("State() after %d failures = %q, want %q", threshold-1, b.State(), StateClosed)
	}
	if err := b.Allow(); err != nil {
		t.Errorf("Allow() below threshold = %v, want nil", err)
	}
}

// TestBreaker_SuccessResetsFailureCount verifies that a success resets the consecutive failure counter.
func TestBreaker_SuccessResetsFailureCount(t *testing.T) {
	b := NewBreaker(5, 1, 50*time.Millisecond)

	b.RecordFailure()
	b.RecordFailure()
	b.RecordFailure()
	b.RecordSuccess()

	if b.ConsecutiveFailures() != 0 {
		t.Errorf("ConsecutiveFailures() after success = %d, want 0", b.ConsecutiveFailures())
	}
	if b.State() != StateClosed {
		t.Errorf("State() after success = %q, want %q", b.State(), StateClosed)
	}
}

// TestBreaker_TransitionsToHalfOpen verifies the breaker transitions from open to half-open after cooldown.
func TestBreaker_TransitionsToHalfOpen(t *testing.T) {
	cooldown := 10 * time.Millisecond
	b := NewBreaker(2, 1, cooldown)

	// Trip the breaker.
	b.RecordFailure()
	b.RecordFailure()
	if b.State() != StateOpen {
		t.Fatalf("State() after failures = %q, want %q", b.State(), StateOpen)
	}

	// Wait for cooldown to elapse.
	deadline := time.Now().Add(200 * time.Millisecond)
	for time.Now().Before(deadline) {
		if b.State() == StateHalfOpen {
			break
		}
		time.Sleep(1 * time.Millisecond)
	}

	if b.State() != StateHalfOpen {
		t.Errorf("State() after cooldown = %q, want %q", b.State(), StateHalfOpen)
	}
	// One probe call should be allowed.
	if err := b.Allow(); err != nil {
		t.Errorf("Allow() in half-open = %v, want nil", err)
	}
}

// TestBreaker_HalfOpenLimitsProbes verifies that half-open limits the number of probe calls.
func TestBreaker_HalfOpenLimitsProbes(t *testing.T) {
	cooldown := 10 * time.Millisecond
	b := NewBreaker(1, 1, cooldown)

	// Trip the breaker.
	b.RecordFailure()

	// Wait for half-open.
	deadline := time.Now().Add(200 * time.Millisecond)
	for time.Now().Before(deadline) {
		if b.State() == StateHalfOpen {
			break
		}
		time.Sleep(1 * time.Millisecond)
	}
	if b.State() != StateHalfOpen {
		t.Fatalf("State() = %q, want %q", b.State(), StateHalfOpen)
	}

	// First probe allowed.
	if err := b.Allow(); err != nil {
		t.Errorf("first Allow() in half-open = %v, want nil", err)
	}

	// Second probe should be rejected.
	if err := b.Allow(); err != ErrCircuitOpen {
		t.Errorf("second Allow() in half-open = %v, want %v", err, ErrCircuitOpen)
	}
}

// TestBreaker_HalfOpenSuccessCloses verifies that a success in half-open closes the breaker.
func TestBreaker_HalfOpenSuccessCloses(t *testing.T) {
	cooldown := 10 * time.Millisecond
	b := NewBreaker(1, 1, cooldown)

	// Trip the breaker.
	b.RecordFailure()

	// Wait for half-open.
	deadline := time.Now().Add(200 * time.Millisecond)
	for time.Now().Before(deadline) {
		if b.State() == StateHalfOpen {
			break
		}
		time.Sleep(1 * time.Millisecond)
	}
	if b.State() != StateHalfOpen {
		t.Fatalf("State() = %q, want %q", b.State(), StateHalfOpen)
	}

	b.RecordSuccess()

	if b.State() != StateClosed {
		t.Errorf("State() after success in half-open = %q, want %q", b.State(), StateClosed)
	}
	if err := b.Allow(); err != nil {
		t.Errorf("Allow() after closing = %v, want nil", err)
	}
}

// TestBreaker_HalfOpenFailureReopens verifies that a failure in half-open re-opens the breaker.
func TestBreaker_HalfOpenFailureReopens(t *testing.T) {
	cooldown := 10 * time.Millisecond
	b := NewBreaker(1, 1, cooldown)

	// Trip the breaker.
	b.RecordFailure()

	// Wait for half-open.
	deadline := time.Now().Add(200 * time.Millisecond)
	for time.Now().Before(deadline) {
		if b.State() == StateHalfOpen {
			break
		}
		time.Sleep(1 * time.Millisecond)
	}
	if b.State() != StateHalfOpen {
		t.Fatalf("State() = %q, want %q", b.State(), StateHalfOpen)
	}

	b.RecordFailure()

	if b.State() != StateOpen {
		t.Errorf("State() after failure in half-open = %q, want %q", b.State(), StateOpen)
	}
	if err := b.Allow(); err != ErrCircuitOpen {
		t.Errorf("Allow() after re-open = %v, want %v", err, ErrCircuitOpen)
	}
}

// TestBreaker_ThreadSafe exercises concurrent Allow/RecordSuccess/RecordFailure with -race.
func TestBreaker_ThreadSafe(t *testing.T) {
	b := NewBreaker(10, 2, 10*time.Millisecond)

	var wg sync.WaitGroup
	const goroutines = 20
	const iterations = 100

	wg.Add(goroutines)
	for g := range goroutines {
		go func(id int) {
			defer wg.Done()
			for range iterations {
				switch id % 3 {
				case 0:
					_ = b.Allow()
				case 1:
					b.RecordSuccess()
				case 2:
					b.RecordFailure()
				}
			}
		}(g)
	}
	wg.Wait()

	// No assertions beyond surviving without race or panic.
	// State must be one of the three valid values.
	s := b.State()
	if s != StateClosed && s != StateOpen && s != StateHalfOpen {
		t.Errorf("State() = %q, want one of closed/open/half_open", s)
	}
}
