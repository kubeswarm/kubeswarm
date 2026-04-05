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
	"errors"
	"sync"
	"testing"
	"time"
)

// mockRedisClient implements the minimal interface needed by RedisSink.
// The RedisSink should depend on a RedisClient interface (not the concrete
// go-redis client) so that tests can provide this mock.
type mockRedisClient struct {
	mu       sync.Mutex
	calls    []xaddCall
	err      error
	errCount int // if > 0, fail this many times then succeed
	failed   int
}

type xaddCall struct {
	Stream string
	MaxLen int64
	Values map[string]any
}

func (m *mockRedisClient) XAdd(ctx context.Context, stream string, maxLen int64, values map[string]any) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.errCount > 0 && m.failed < m.errCount {
		m.failed++
		return m.err
	}

	m.calls = append(m.calls, xaddCall{
		Stream: stream,
		MaxLen: maxLen,
		Values: values,
	})
	if m.err != nil && m.errCount == 0 {
		return m.err
	}
	return nil
}

func (m *mockRedisClient) getCalls() []xaddCall {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make([]xaddCall, len(m.calls))
	copy(result, m.calls)
	return result
}

func TestRedisSink_XADDCorrectStreamKey(t *testing.T) {
	t.Parallel()

	client := &mockRedisClient{}
	sink := NewRedisSink(client)

	ev := NewEvent(ActionToolCalled, StatusSuccess, "production", "agent")
	if err := sink.Emit(context.Background(), []AuditEvent{ev}); err != nil {
		t.Fatalf("Emit: %v", err)
	}

	calls := client.getCalls()
	if len(calls) != 1 {
		t.Fatalf("expected 1 XADD call, got %d", len(calls))
	}

	wantStream := "kubeswarm:audit:production"
	if calls[0].Stream != wantStream {
		t.Errorf("stream = %q, want %q", calls[0].Stream, wantStream)
	}
}

func TestRedisSink_XADDMultipleNamespaces(t *testing.T) {
	t.Parallel()

	client := &mockRedisClient{}
	sink := NewRedisSink(client)

	events := []AuditEvent{
		NewEvent(ActionToolCalled, StatusSuccess, "ns-a", "agent"),
		NewEvent(ActionToolCalled, StatusSuccess, "ns-b", "agent"),
	}

	if err := sink.Emit(context.Background(), events); err != nil {
		t.Fatalf("Emit: %v", err)
	}

	calls := client.getCalls()
	if len(calls) != 2 {
		t.Fatalf("expected 2 XADD calls, got %d", len(calls))
	}

	if calls[0].Stream != "kubeswarm:audit:ns-a" {
		t.Errorf("call[0].Stream = %q, want %q", calls[0].Stream, "kubeswarm:audit:ns-a")
	}
	if calls[1].Stream != "kubeswarm:audit:ns-b" {
		t.Errorf("call[1].Stream = %q, want %q", calls[1].Stream, "kubeswarm:audit:ns-b")
	}
}

func TestRedisSink_MAXLENOnEveryXADD(t *testing.T) {
	t.Parallel()

	client := &mockRedisClient{}
	sink := NewRedisSink(client)

	events := []AuditEvent{
		NewEvent(ActionTaskReceived, StatusSuccess, "default", "agent"),
		NewEvent(ActionTaskCompleted, StatusSuccess, "default", "agent"),
	}

	if err := sink.Emit(context.Background(), events); err != nil {
		t.Fatalf("Emit: %v", err)
	}

	for i, call := range client.getCalls() {
		if call.MaxLen != 100000 {
			t.Errorf("call[%d].MaxLen = %d, want 100000", i, call.MaxLen)
		}
	}
}

func TestRedisSink_TimeoutAfter100ms(t *testing.T) {
	t.Parallel()

	// Create a client that blocks until context is cancelled
	client := &mockRedisClient{}
	blockingClient := &blockingRedisClient{inner: client, delay: 5 * time.Second}
	sink := NewRedisSink(blockingClient)

	ev := NewEvent(ActionToolCalled, StatusSuccess, "default", "agent")

	start := time.Now()
	err := sink.Emit(context.Background(), []AuditEvent{ev})
	elapsed := time.Since(start)

	// Should timeout around 100ms, not wait for the full 5s delay
	if elapsed > 500*time.Millisecond {
		t.Errorf("Emit took %v, expected timeout around 100ms", elapsed)
	}

	// Should return a timeout-related error
	if err == nil {
		t.Error("expected timeout error, got nil")
	}
}

// blockingRedisClient wraps a mock and adds an artificial delay.
type blockingRedisClient struct {
	inner *mockRedisClient
	delay time.Duration
}

func (b *blockingRedisClient) XAdd(ctx context.Context, stream string, maxLen int64, values map[string]any) error {
	select {
	case <-time.After(b.delay):
		return b.inner.XAdd(ctx, stream, maxLen, values)
	case <-ctx.Done():
		return ctx.Err()
	}
}

func TestRedisSink_CircuitBreakerOpensAfter5Failures(t *testing.T) {
	t.Parallel()

	client := &mockRedisClient{
		err: errors.New("connection refused"),
	}
	sink := NewRedisSink(client)

	ev := NewEvent(ActionToolCalled, StatusSuccess, "default", "agent")

	// Trigger 5 consecutive failures to open the circuit breaker
	for range 5 {
		_ = sink.Emit(context.Background(), []AuditEvent{ev})
	}

	// The 6th call should fail fast (circuit open) without calling the client
	client.mu.Lock()
	callsBefore := len(client.calls)
	client.mu.Unlock()

	err := sink.Emit(context.Background(), []AuditEvent{ev})

	client.mu.Lock()
	callsAfter := len(client.calls)
	client.mu.Unlock()

	if err == nil {
		t.Error("expected error when circuit breaker is open")
	}

	// Circuit breaker should prevent the actual Redis call
	if callsAfter != callsBefore {
		t.Error("circuit breaker is open but a Redis call was still made")
	}
}

func TestRedisSink_CircuitBreakerClosesAfterCooldownAndSuccess(t *testing.T) {
	t.Parallel()

	// Fail 5 times then succeed - simulates recovery
	client := &mockRedisClient{
		err:      errors.New("connection refused"),
		errCount: 5,
	}
	sink := NewRedisSinkWithOptions(client, RedisSinkOptions{
		CircuitBreakerCooldown: 50 * time.Millisecond, // short cooldown for test
	})

	ev := NewEvent(ActionToolCalled, StatusSuccess, "default", "agent")

	// Trigger 5 failures to open circuit
	for range 5 {
		_ = sink.Emit(context.Background(), []AuditEvent{ev})
	}

	// Wait for cooldown
	time.Sleep(100 * time.Millisecond)

	// Probe should succeed (errCount exhausted) and close the circuit
	err := sink.Emit(context.Background(), []AuditEvent{ev})
	if err != nil {
		t.Errorf("expected probe to succeed after cooldown, got: %v", err)
	}

	// Subsequent calls should also succeed
	err = sink.Emit(context.Background(), []AuditEvent{ev})
	if err != nil {
		t.Errorf("expected success after circuit closed, got: %v", err)
	}
}

func TestRedisSink_EmptyBatch(t *testing.T) {
	t.Parallel()

	client := &mockRedisClient{}
	sink := NewRedisSink(client)

	if err := sink.Emit(context.Background(), nil); err != nil {
		t.Fatalf("Emit nil: %v", err)
	}
	if err := sink.Emit(context.Background(), []AuditEvent{}); err != nil {
		t.Fatalf("Emit empty: %v", err)
	}

	if len(client.getCalls()) != 0 {
		t.Error("expected no XADD calls for empty batch")
	}
}

func TestRedisSink_ContextCancellation(t *testing.T) {
	t.Parallel()

	client := &mockRedisClient{}
	sink := NewRedisSink(client)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	ev := NewEvent(ActionToolCalled, StatusSuccess, "default", "agent")
	err := sink.Emit(ctx, []AuditEvent{ev})

	if err == nil {
		t.Error("expected error when context is already cancelled")
	}
}
