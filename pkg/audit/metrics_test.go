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
	"math"
	"testing"
	"time"

	"go.opentelemetry.io/otel/metric/noop"
)

func TestEmitterMetrics_Snapshot_DroppedCount(t *testing.T) {
	t.Parallel()

	// Create a tiny buffer and a slow sink to force drops
	bufSize := 2
	sink := &slowSink{delay: 10 * time.Second}
	em := NewEmitter(sink, bufSize)

	// Emit enough events to cause drops
	for range 20 {
		em.Emit(makeEvent())
	}

	meter := noop.NewMeterProvider().Meter("test")
	metrics, err := NewEmitterMetrics(em, meter, "default", "test-agent")
	if err != nil {
		t.Fatalf("NewEmitterMetrics() error = %v", err)
	}

	snap := metrics.Snapshot()
	if snap.Dropped != em.Dropped() {
		t.Errorf("Snapshot().Dropped = %d, want %d (from emitter)", snap.Dropped, em.Dropped())
	}
	if snap.Dropped == 0 {
		t.Error("expected Dropped > 0 after overflow")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	em.Close(ctx)
}

func TestEmitterMetrics_Snapshot_BufferUtilization_Empty(t *testing.T) {
	t.Parallel()

	sink := &countingSink{}
	em := NewEmitter(sink, DefaultBufferSize)

	// Let the emitter drain any events
	time.Sleep(50 * time.Millisecond)

	meter := noop.NewMeterProvider().Meter("test")
	metrics, err := NewEmitterMetrics(em, meter, "default", "test-agent")
	if err != nil {
		t.Fatalf("NewEmitterMetrics() error = %v", err)
	}

	snap := metrics.Snapshot()
	if snap.BufferUtilization != 0.0 {
		t.Errorf("BufferUtilization = %f, want 0.0 when buffer is empty", snap.BufferUtilization)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()
	em.Close(ctx)
}

func TestEmitterMetrics_Snapshot_BufferUtilization_ApproachesFull(t *testing.T) {
	t.Parallel()

	bufSize := 16
	// Use a slow sink so events stay in the buffer
	sink := &slowSink{delay: 10 * time.Second}
	em := NewEmitter(sink, bufSize)

	// Fill the buffer
	for range bufSize + 5 {
		em.Emit(makeEvent())
	}

	meter := noop.NewMeterProvider().Meter("test")
	metrics, err := NewEmitterMetrics(em, meter, "default", "test-agent")
	if err != nil {
		t.Fatalf("NewEmitterMetrics() error = %v", err)
	}

	snap := metrics.Snapshot()

	// Buffer should have events (the loop may have drained a batch already).
	if snap.BufferUtilization < 0.1 {
		t.Errorf("BufferUtilization = %f, want >= 0.1 when buffer has events", snap.BufferUtilization)
	}
	if snap.BufferUtilization > 1.0 {
		t.Errorf("BufferUtilization = %f, must not exceed 1.0", snap.BufferUtilization)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	em.Close(ctx)
}

func TestEmitterMetrics_Snapshot_BufferUtilization_Calculation(t *testing.T) {
	t.Parallel()

	bufSize := 100
	// Slow sink ensures events accumulate in buffer
	sink := &slowSink{delay: 10 * time.Second}
	em := NewEmitter(sink, bufSize)

	// Emit a known fraction of the buffer
	for range 50 {
		em.Emit(makeEvent())
	}

	// Give a moment for events to settle in the channel
	time.Sleep(20 * time.Millisecond)

	meter := noop.NewMeterProvider().Meter("test")
	metrics, err := NewEmitterMetrics(em, meter, "default", "test-agent")
	if err != nil {
		t.Fatalf("NewEmitterMetrics() error = %v", err)
	}

	snap := metrics.Snapshot()

	// Utilization should be approximately buffered/capacity
	expected := float64(em.Buffered()) / float64(bufSize)
	if math.Abs(snap.BufferUtilization-expected) > 0.05 {
		t.Errorf("BufferUtilization = %f, want approximately %f", snap.BufferUtilization, expected)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	em.Close(ctx)
}

func TestEmitterMetrics_Snapshot_DroppedMatchesEmitter(t *testing.T) {
	t.Parallel()

	bufSize := 4
	sink := &slowSink{delay: 10 * time.Second}
	em := NewEmitter(sink, bufSize)

	for range 30 {
		em.Emit(makeEvent())
	}

	meter := noop.NewMeterProvider().Meter("test")
	metrics, err := NewEmitterMetrics(em, meter, "default", "test-agent")
	if err != nil {
		t.Fatalf("NewEmitterMetrics() error = %v", err)
	}

	snap := metrics.Snapshot()
	if snap.Dropped != em.Dropped() {
		t.Errorf("Snapshot().Dropped = %d, emitter.Dropped() = %d - must match", snap.Dropped, em.Dropped())
	}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	em.Close(ctx)
}

func TestEmitterMetrics_NilMeter_NoPanic(t *testing.T) {
	t.Parallel()

	sink := &countingSink{}
	em := NewEmitter(sink, DefaultBufferSize)
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
		defer cancel()
		em.Close(ctx)
	}()

	// nil meter should not panic
	metrics, err := NewEmitterMetrics(em, nil, "default", "test-agent")
	if err != nil {
		t.Fatalf("NewEmitterMetrics(nil meter) error = %v", err)
	}

	// Snapshot should still work
	snap := metrics.Snapshot()
	if snap.Dropped != 0 {
		t.Errorf("Dropped = %d, want 0", snap.Dropped)
	}
	if snap.BufferUtilization != 0.0 {
		t.Errorf("BufferUtilization = %f, want 0.0", snap.BufferUtilization)
	}
}
