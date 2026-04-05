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

package audit

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"
)

const (
	// defaultWriteTimeout is the per-XADD context timeout.
	defaultWriteTimeout = 100 * time.Millisecond

	// defaultMaxLen is the safety cap passed as MAXLEN ~ to every XADD.
	defaultMaxLen int64 = 100_000

	// defaultCircuitBreakerThreshold is the number of consecutive failures
	// before the circuit breaker opens.
	defaultCircuitBreakerThreshold = 5

	// defaultCircuitBreakerCooldown is how long the circuit stays open before probing.
	defaultCircuitBreakerCooldown = 30 * time.Second
)

// RedisClient is the subset of Redis operations used by RedisSink.
// Accepting an interface allows callers to supply a mock or a cluster client.
type RedisClient interface {
	XAdd(ctx context.Context, stream string, maxLen int64, values map[string]any) error
}

// RedisSinkOptions configures optional RedisSink parameters.
type RedisSinkOptions struct {
	// WriteTimeout overrides the per-XADD timeout (default 100ms).
	WriteTimeout time.Duration
	// MaxLen overrides the MAXLEN ~ cap (default 100000).
	MaxLen int64
	// CircuitBreakerThreshold overrides the failure count to open the circuit (default 5).
	CircuitBreakerThreshold int
	// CircuitBreakerCooldown overrides the cooldown before probing (default 30s).
	CircuitBreakerCooldown time.Duration
}

// RedisSink writes audit events to a Redis Stream using XADD with MAXLEN.
// The stream key is derived from the event's namespace: kubeswarm:audit:<namespace>.
// It implements a circuit breaker that opens after consecutive failures and
// probes after a cooldown period. Safe for concurrent use.
type RedisSink struct {
	client RedisClient
	opts   RedisSinkOptions

	mu               sync.Mutex
	consecutiveFails int
	circuitOpen      bool
	circuitOpenedAt  time.Time
}

// NewRedisSink creates a RedisSink with default options.
func NewRedisSink(client RedisClient) *RedisSink {
	return NewRedisSinkWithOptions(client, RedisSinkOptions{})
}

// NewRedisSinkWithOptions creates a RedisSink with the given options.
func NewRedisSinkWithOptions(client RedisClient, opts RedisSinkOptions) *RedisSink {
	if opts.WriteTimeout <= 0 {
		opts.WriteTimeout = defaultWriteTimeout
	}
	if opts.MaxLen <= 0 {
		opts.MaxLen = defaultMaxLen
	}
	if opts.CircuitBreakerThreshold <= 0 {
		opts.CircuitBreakerThreshold = defaultCircuitBreakerThreshold
	}
	if opts.CircuitBreakerCooldown <= 0 {
		opts.CircuitBreakerCooldown = defaultCircuitBreakerCooldown
	}
	return &RedisSink{
		client: client,
		opts:   opts,
	}
}

// Emit writes each event to the Redis Stream via XADD. Each event is serialized
// as a single "data" field. MAXLEN is applied on every call to cap unbounded
// growth. The stream key is derived from each event's Namespace field.
//
// When the circuit breaker is open, Emit returns an error immediately unless the
// cooldown has elapsed, in which case a single probe write is attempted.
func (s *RedisSink) Emit(ctx context.Context, events []AuditEvent) error {
	for i := range events {
		if err := s.emitOne(ctx, &events[i]); err != nil {
			return err
		}
	}
	return nil
}

func (s *RedisSink) emitOne(ctx context.Context, event *AuditEvent) error {
	// Check for already-cancelled context.
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("audit redis sink: %w", err)
	}

	s.mu.Lock()
	if s.circuitOpen {
		if time.Since(s.circuitOpenedAt) < s.opts.CircuitBreakerCooldown {
			s.mu.Unlock()
			return fmt.Errorf("audit redis sink: circuit breaker open")
		}
		// Cooldown elapsed - allow a probe attempt.
	}
	s.mu.Unlock()

	data, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("audit redis sink: marshal: %w", err)
	}

	streamKey := fmt.Sprintf("kubeswarm:audit:%s", event.Namespace)

	writeCtx, cancel := context.WithTimeout(ctx, s.opts.WriteTimeout)
	defer cancel()

	err = s.client.XAdd(writeCtx, streamKey, s.opts.MaxLen, map[string]any{
		"data": string(data),
	})

	s.mu.Lock()
	defer s.mu.Unlock()

	if err != nil {
		s.consecutiveFails++
		if s.consecutiveFails >= s.opts.CircuitBreakerThreshold {
			s.circuitOpen = true
			s.circuitOpenedAt = time.Now()
		}
		return fmt.Errorf("audit redis sink: xadd: %w", err)
	}

	// Success - reset circuit breaker state.
	s.consecutiveFails = 0
	s.circuitOpen = false
	return nil
}
