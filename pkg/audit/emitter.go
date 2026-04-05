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
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// DefaultBufferSize is the default bounded channel size for the emitter.
	DefaultBufferSize = 1024
)

// noopSink is used when no sink is provided.
type noopSink struct{}

func (noopSink) Emit(_ context.Context, _ []AuditEvent) error { return nil }

// Emitter accepts audit events via a non-blocking Emit call and dispatches
// them to an AuditSink on a background goroutine. When the internal buffer is
// full, the oldest event is dropped and a counter is incremented.
// Safe for concurrent use.
type Emitter struct {
	sink      AuditSink
	ch        chan AuditEvent
	mode      atomic.Value // stores AuditLogMode
	dropped   atomic.Int64
	closeOnce sync.Once
	done      chan struct{}
}

// NewEmitter creates an Emitter with the given sink and buffer size.
// It starts a background goroutine that reads from the buffer and dispatches
// to the sink. If bufferSize is <= 0, DefaultBufferSize is used.
// If sink is nil, a no-op sink is used.
func NewEmitter(sink AuditSink, bufferSize int) *Emitter {
	if bufferSize <= 0 {
		bufferSize = DefaultBufferSize
	}
	if sink == nil {
		sink = noopSink{}
	}

	e := &Emitter{
		sink: sink,
		ch:   make(chan AuditEvent, bufferSize),
		done: make(chan struct{}),
	}
	e.mode.Store(ModeActions) // default to actions mode

	go e.loop()
	return e
}

// SetMode changes the audit mode. When set to ModeOff, subsequent Emit calls
// are no-ops.
func (e *Emitter) SetMode(m AuditLogMode) {
	e.mode.Store(m)
}

// Emit enqueues an audit event for asynchronous dispatch. It never blocks.
// If the mode is "off", the event is silently dropped.
// If the buffer is full, the oldest event is evicted to make room.
func (e *Emitter) Emit(event AuditEvent) {
	if e.mode.Load().(AuditLogMode) == ModeOff {
		return
	}

	// Non-blocking send: if the buffer is full, drop the oldest event.
	select {
	case e.ch <- event:
	default:
		// Buffer full - evict oldest to make room.
		select {
		case <-e.ch:
			total := e.dropped.Add(1)
			if total == 1 || total%100 == 0 {
				slog.Warn("audit events dropped due to buffer overflow",
					"total_dropped", total, "buffer_size", cap(e.ch))
			}
		default:
		}
		// Try again after eviction.
		select {
		case e.ch <- event:
		default:
			// Still full (concurrent writers) - drop the new event.
			total := e.dropped.Add(1)
			if total == 1 || total%100 == 0 {
				slog.Warn("audit events dropped due to buffer overflow",
					"total_dropped", total, "buffer_size", cap(e.ch))
			}
		}
	}
}

// Dropped returns the total number of events dropped due to buffer overflow.
func (e *Emitter) Dropped() int64 {
	return e.dropped.Load()
}

// Buffered returns the number of events currently in the buffer.
func (e *Emitter) Buffered() int {
	return len(e.ch)
}

// Close gracefully shuts down the emitter. It closes the channel and waits
// for remaining events to flush, respecting the provided context deadline.
// Any events still in the buffer after the deadline are dropped.
func (e *Emitter) Close(ctx context.Context) {
	e.closeOnce.Do(func() {
		close(e.ch)
		select {
		case <-e.done:
		case <-ctx.Done():
		}
	})
}

// loop is the background goroutine that reads events from the channel and
// dispatches them to the sink one at a time.
func (e *Emitter) loop() {
	defer close(e.done)

	for event := range e.ch {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		if err := e.sink.Emit(ctx, []AuditEvent{event}); err != nil {
			slog.Warn("audit sink emit failed", "error", err, "action", string(event.Action))
		}
		cancel()
	}
}
