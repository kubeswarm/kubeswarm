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
	"fmt"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// MetricsSnapshot holds a point-in-time view of emitter metrics.
type MetricsSnapshot struct {
	// Dropped is the total number of events dropped due to buffer overflow.
	Dropped int64
	// BufferUtilization is the current buffer usage as a ratio from 0.0 to 1.0.
	BufferUtilization float64
}

// EmitterMetrics wraps an Emitter and registers OTel instruments for its
// dropped counter and buffer utilization. If meter is nil, no instruments are
// registered but Snapshot still works.
type EmitterMetrics struct {
	emitter   *Emitter
	namespace string
	agent     string
}

// NewEmitterMetrics creates an EmitterMetrics and registers OTel observable
// instruments on the provided meter. If meter is nil, no instruments are
// registered.
func NewEmitterMetrics(emitter *Emitter, meter metric.Meter, namespace, agent string) (*EmitterMetrics, error) {
	em := &EmitterMetrics{
		emitter:   emitter,
		namespace: namespace,
		agent:     agent,
	}

	if meter == nil {
		return em, nil
	}

	attrs := attribute.NewSet(
		attribute.String("namespace", namespace),
		attribute.String("agent", agent),
	)

	// Register the dropped events counter as an observable that reads from
	// the emitter's atomic counter.
	_, err := meter.Int64ObservableCounter(
		"kubeswarm_audit_events_dropped_total",
		metric.WithDescription("Total number of audit events dropped due to buffer overflow"),
		metric.WithInt64Callback(func(_ context.Context, o metric.Int64Observer) error {
			o.Observe(emitter.Dropped(), metric.WithAttributeSet(attrs))
			return nil
		}),
	)
	if err != nil {
		return nil, fmt.Errorf("registering dropped counter: %w", err)
	}

	// Register the buffer utilization gauge.
	_, err = meter.Float64ObservableGauge(
		"kubeswarm_audit_buffer_utilization",
		metric.WithDescription("Current audit emitter buffer utilization from 0.0 to 1.0"),
		metric.WithFloat64Callback(func(_ context.Context, o metric.Float64Observer) error {
			cap := emitter.BufferCap()
			var util float64
			if cap > 0 {
				util = float64(emitter.Buffered()) / float64(cap)
			}
			o.Observe(util, metric.WithAttributeSet(attrs))
			return nil
		}),
	)
	if err != nil {
		return nil, fmt.Errorf("registering buffer utilization gauge: %w", err)
	}

	return em, nil
}

// Snapshot returns a point-in-time snapshot of the emitter's metrics.
func (em *EmitterMetrics) Snapshot() MetricsSnapshot {
	cap := em.emitter.BufferCap()
	var util float64
	if cap > 0 {
		util = float64(em.emitter.Buffered()) / float64(cap)
	}
	return MetricsSnapshot{
		Dropped:           em.emitter.Dropped(),
		BufferUtilization: util,
	}
}
