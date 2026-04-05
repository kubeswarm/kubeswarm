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
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// slowSink simulates a sink that takes a long time to write.
type slowSink struct {
	delay    time.Duration
	received [][]AuditEvent
	mu       sync.Mutex
}

func (s *slowSink) Emit(_ context.Context, events []AuditEvent) error {
	time.Sleep(s.delay)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.received = append(s.received, events)
	return nil
}

func (s *slowSink) allEvents() []AuditEvent { //nolint:unused // kept for debugging
	s.mu.Lock()
	defer s.mu.Unlock()
	all := make([]AuditEvent, 0, len(s.received)*2)
	for _, batch := range s.received {
		all = append(all, batch...)
	}
	return all
}

// countingSink counts how many events it receives.
type countingSink struct {
	count atomic.Int64
	mu    sync.Mutex
	all   []AuditEvent
}

func (c *countingSink) Emit(_ context.Context, events []AuditEvent) error {
	c.count.Add(int64(len(events)))
	c.mu.Lock()
	defer c.mu.Unlock()
	c.all = append(c.all, events...)
	return nil
}

func makeEvent() AuditEvent {
	return NewEvent(ActionToolCalled, StatusSuccess, "default", "test-agent")
}

func TestEmitter_EmitDoesNotBlock(t *testing.T) {
	t.Parallel()

	sink := &slowSink{delay: 5 * time.Second}
	em := NewEmitter(sink, DefaultBufferSize)
	defer em.Close(context.Background())

	done := make(chan struct{})
	go func() {
		// Emit should return immediately even though sink is slow
		em.Emit(makeEvent())
		close(done)
	}()

	select {
	case <-done:
		// OK - Emit returned without blocking
	case <-time.After(100 * time.Millisecond):
		t.Fatal("Emit blocked for more than 100ms - non-blocking guarantee violated")
	}
}

func TestEmitter_BufferDropsOldestWhenFull(t *testing.T) {
	t.Parallel()

	// Use a tiny buffer to make it easy to fill
	bufSize := 4
	sink := &slowSink{delay: 10 * time.Second} // sink that never finishes in time
	em := NewEmitter(sink, bufSize)

	// Emit more events than buffer can hold
	for i := 0; i < bufSize+10; i++ {
		em.Emit(makeEvent())
	}

	// Check that drops were counted
	dropped := em.Dropped()
	if dropped == 0 {
		t.Error("expected some events to be dropped when buffer is full")
	}

	// Buffer should not exceed capacity
	buffered := em.Buffered()
	if buffered > bufSize {
		t.Errorf("buffered = %d, want <= %d", buffered, bufSize)
	}

	em.Close(context.Background())
}

func TestEmitter_DefaultBufferSize(t *testing.T) {
	t.Parallel()
	if DefaultBufferSize != 1024 {
		t.Errorf("DefaultBufferSize = %d, want 1024", DefaultBufferSize)
	}
}

func TestEmitter_GracefulShutdownFlushesBuffer(t *testing.T) {
	t.Parallel()

	sink := &countingSink{}
	em := NewEmitter(sink, DefaultBufferSize)

	n := 50
	for range n {
		em.Emit(makeEvent())
	}

	// Close with a generous deadline - should flush all buffered events
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	em.Close(ctx)

	got := int(sink.count.Load())
	if got != n {
		t.Errorf("sink received %d events after shutdown, want %d", got, n)
	}
}

func TestEmitter_GracefulShutdownRespectsDeadline(t *testing.T) {
	t.Parallel()

	// Sink that blocks forever
	sink := &slowSink{delay: 1 * time.Minute}
	em := NewEmitter(sink, DefaultBufferSize)

	for range 100 {
		em.Emit(makeEvent())
	}

	// Close with a very short deadline
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	start := time.Now()
	em.Close(ctx)
	elapsed := time.Since(start)

	// Should return within a reasonable time, not block for the full sink delay
	if elapsed > 1*time.Second {
		t.Errorf("Close took %v, expected it to respect the short deadline", elapsed)
	}
}

func TestEmitter_DroppedEventsAreCounted(t *testing.T) {
	t.Parallel()

	bufSize := 2
	// Use a sink that blocks so the buffer fills up
	sink := &slowSink{delay: 10 * time.Second}
	em := NewEmitter(sink, bufSize)

	// Emit many events to overflow the buffer
	for range 20 {
		em.Emit(makeEvent())
	}

	dropped := em.Dropped()
	if dropped == 0 {
		t.Error("expected dropped count > 0 when buffer overflows")
	}

	em.Close(context.Background())
}

func TestEmitter_ModeOff_DoesNotEmit(t *testing.T) {
	t.Parallel()

	sink := &countingSink{}
	em := NewEmitter(sink, DefaultBufferSize)
	em.SetMode(ModeOff)

	em.Emit(makeEvent())
	em.Emit(makeEvent())

	// Give the emitter loop a moment to process (it should not forward anything)
	time.Sleep(50 * time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()
	em.Close(ctx)

	if got := int(sink.count.Load()); got != 0 {
		t.Errorf("mode=off emitter forwarded %d events to sink, want 0", got)
	}
}

func TestEmitter_NilSink_DoesNotPanic(t *testing.T) {
	t.Parallel()

	em := NewEmitter(nil, DefaultBufferSize)

	// Should not panic
	em.Emit(makeEvent())
	em.Emit(makeEvent())

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()
	em.Close(ctx)
}

func TestEmitter_ConcurrentEmit(t *testing.T) {
	t.Parallel()

	sink := &countingSink{}
	em := NewEmitter(sink, DefaultBufferSize)

	const goroutines = 20
	const eventsPerGoroutine = 50

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for range goroutines {
		go func() {
			defer wg.Done()
			for range eventsPerGoroutine {
				em.Emit(makeEvent())
			}
		}()
	}
	wg.Wait()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	em.Close(ctx)

	total := goroutines * eventsPerGoroutine
	got := int(sink.count.Load()) + int(em.Dropped())
	if got != total {
		t.Errorf("emitted + dropped = %d, want %d", got, total)
	}
}
