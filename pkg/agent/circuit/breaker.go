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
	"errors"
	"sync"
	"time"
)

// State represents the circuit breaker state.
type State string

const (
	StateClosed   State = "closed"
	StateOpen     State = "open"
	StateHalfOpen State = "half_open"
)

// ErrCircuitOpen is returned by Allow() when the circuit is open.
var ErrCircuitOpen = errors.New("circuit breaker is open")

// Breaker is a thread-safe circuit breaker with three states.
type Breaker struct {
	mu                  sync.Mutex
	state               State
	failureThreshold    int
	halfOpenMaxCalls    int
	cooldown            time.Duration
	consecutiveFailures int
	halfOpenCalls       int // calls allowed through in half-open
	lastFailureTime     time.Time
}

// NewBreaker creates a circuit breaker.
func NewBreaker(failureThreshold, halfOpenMaxCalls int, cooldown time.Duration) *Breaker {
	return &Breaker{
		state:            StateClosed,
		failureThreshold: failureThreshold,
		halfOpenMaxCalls: halfOpenMaxCalls,
		cooldown:         cooldown,
	}
}

// Allow checks if a call is allowed. Returns nil if yes, ErrCircuitOpen if no.
func (b *Breaker) Allow() error {
	b.mu.Lock()
	defer b.mu.Unlock()

	switch b.state {
	case StateClosed:
		return nil
	case StateOpen:
		// Check if cooldown has elapsed -> transition to half-open
		if time.Since(b.lastFailureTime) >= b.cooldown {
			b.state = StateHalfOpen
			b.halfOpenCalls = 0
			return nil // allow the first probe
		}
		return ErrCircuitOpen
	case StateHalfOpen:
		if b.halfOpenCalls < b.halfOpenMaxCalls {
			b.halfOpenCalls++
			return nil
		}
		return ErrCircuitOpen
	}
	return nil
}

// RecordSuccess marks a call as successful.
func (b *Breaker) RecordSuccess() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.consecutiveFailures = 0
	if b.state == StateHalfOpen {
		b.state = StateClosed
		b.halfOpenCalls = 0
	}
}

// RecordFailure marks a call as failed.
func (b *Breaker) RecordFailure() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.consecutiveFailures++
	b.lastFailureTime = time.Now()

	switch b.state {
	case StateClosed:
		if b.consecutiveFailures >= b.failureThreshold {
			b.state = StateOpen
		}
	case StateHalfOpen:
		b.state = StateOpen
		b.halfOpenCalls = 0
	}
}

// State returns the current state.
func (b *Breaker) State() State {
	b.mu.Lock()
	defer b.mu.Unlock()
	// Check for open->half-open transition
	if b.state == StateOpen && time.Since(b.lastFailureTime) >= b.cooldown {
		b.state = StateHalfOpen
		b.halfOpenCalls = 0
	}
	return b.state
}

// ConsecutiveFailures returns the current failure count.
func (b *Breaker) ConsecutiveFailures() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.consecutiveFailures
}
