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

package observability

import (
	"context"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

// newTestSearchMetrics installs a fresh MeterProvider backed by a ManualReader
// as the global OTel provider, then constructs a SearchMetrics bound to it.
// Returns the metrics and the reader (for collection).
func newTestSearchMetrics(t *testing.T) (*SearchMetrics, *sdkmetric.ManualReader) {
	t.Helper()
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	otel.SetMeterProvider(mp)
	t.Cleanup(func() {
		_ = mp.Shutdown(context.Background())
	})
	sm, err := NewSearchMetrics()
	if err != nil {
		t.Fatalf("NewSearchMetrics: %v", err)
	}
	return sm, reader
}

// gaugeInt64 extracts the single data-point value for an Int64 gauge metric.
// Returns (value, found).
func gaugeInt64(m *metricdata.Metrics) (int64, bool) {
	if m == nil {
		return 0, false
	}
	g, ok := m.Data.(metricdata.Gauge[int64])
	if !ok {
		return 0, false
	}
	if len(g.DataPoints) == 0 {
		return 0, false
	}
	return g.DataPoints[0].Value, true
}

func searchAttrs() []attribute.KeyValue {
	return []attribute.KeyValue{
		attribute.String("namespace", "ns"),
		attribute.String("agent", "searcher"),
	}
}

// 1. Constructor does not error.
func TestNewSearchMetrics_NoError(t *testing.T) {
	t.Parallel()
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	otel.SetMeterProvider(mp)
	t.Cleanup(func() {
		_ = mp.Shutdown(context.Background())
	})

	sm, err := NewSearchMetrics()
	if err != nil {
		t.Fatalf("NewSearchMetrics returned error: %v", err)
	}
	if sm == nil {
		t.Fatal("NewSearchMetrics returned nil")
	}
}

// 2. RecordNodesCreated increments the counter.
func TestSearchMetrics_RecordNodesCreated(t *testing.T) {
	sm, reader := newTestSearchMetrics(t)
	ctx := context.Background()

	sm.RecordNodesCreated(ctx, 3, searchAttrs()...)

	rm := collect(t, reader)
	m := findMetric(rm, "kubeswarm.search.nodes.created")
	if m == nil {
		t.Fatal("kubeswarm.search.nodes.created metric not registered")
	}
	if v, ok := sumInt64(m); !ok || v != 3 {
		t.Errorf("nodes.created: got %d ok=%v, want 3", v, ok)
	}
}

// 3. RecordNodesPruned increments the counter.
func TestSearchMetrics_RecordNodesPruned(t *testing.T) {
	sm, reader := newTestSearchMetrics(t)
	ctx := context.Background()

	sm.RecordNodesPruned(ctx, 2, searchAttrs()...)

	rm := collect(t, reader)
	m := findMetric(rm, "kubeswarm.search.nodes.pruned")
	if m == nil {
		t.Fatal("kubeswarm.search.nodes.pruned metric not registered")
	}
	if v, ok := sumInt64(m); !ok || v != 2 {
		t.Errorf("nodes.pruned: got %d ok=%v, want 2", v, ok)
	}
}

// 4. RecordNodeScore emits a histogram data point.
func TestSearchMetrics_RecordNodeScore(t *testing.T) {
	sm, reader := newTestSearchMetrics(t)
	ctx := context.Background()

	sm.RecordNodeScore(ctx, 720, searchAttrs()...)

	rm := collect(t, reader)
	m := findMetric(rm, "kubeswarm.search.node.score")
	if m == nil {
		t.Fatal("kubeswarm.search.node.score metric not registered")
	}
	h, ok := m.Data.(metricdata.Histogram[int64])
	if !ok {
		t.Fatalf("kubeswarm.search.node.score data type = %T, want Histogram[int64]", m.Data)
	}
	if len(h.DataPoints) == 0 {
		t.Fatal("kubeswarm.search.node.score has no data points")
	}
	if h.DataPoints[0].Count != 1 {
		t.Errorf("kubeswarm.search.node.score count = %d, want 1", h.DataPoints[0].Count)
	}
}

// 5. RecordIteration increments the counter.
func TestSearchMetrics_RecordIteration(t *testing.T) {
	sm, reader := newTestSearchMetrics(t)
	ctx := context.Background()

	sm.RecordIteration(ctx, searchAttrs()...)

	rm := collect(t, reader)
	m := findMetric(rm, "kubeswarm.search.iterations")
	if m == nil {
		t.Fatal("kubeswarm.search.iterations metric not registered")
	}
	if v, ok := sumInt64(m); !ok || v != 1 {
		t.Errorf("iterations: got %d ok=%v, want 1", v, ok)
	}
}

// 6. RecordBestScore records the gauge value.
func TestSearchMetrics_RecordBestScore(t *testing.T) {
	sm, reader := newTestSearchMetrics(t)
	ctx := context.Background()

	sm.RecordBestScore(ctx, 850, searchAttrs()...)

	rm := collect(t, reader)
	m := findMetric(rm, "kubeswarm.search.best_score")
	if m == nil {
		t.Fatal("kubeswarm.search.best_score metric not registered")
	}
	if v, ok := gaugeInt64(m); !ok || v != 850 {
		t.Errorf("best_score: got %d ok=%v, want 850", v, ok)
	}
}

// 7. RecordEvaluatorParseFailure increments the counter.
func TestSearchMetrics_RecordEvaluatorParseFailure(t *testing.T) {
	sm, reader := newTestSearchMetrics(t)
	ctx := context.Background()

	sm.RecordEvaluatorParseFailure(ctx, searchAttrs()...)

	rm := collect(t, reader)
	m := findMetric(rm, "kubeswarm.search.evaluator.parse_failures")
	if m == nil {
		t.Fatal("kubeswarm.search.evaluator.parse_failures metric not registered")
	}
	if v, ok := sumInt64(m); !ok || v != 1 {
		t.Errorf("evaluator.parse_failures: got %d ok=%v, want 1", v, ok)
	}
}

// 8. RecordPlannerValidationFailure increments the counter.
func TestSearchMetrics_RecordPlannerValidationFailure(t *testing.T) {
	sm, reader := newTestSearchMetrics(t)
	ctx := context.Background()

	sm.RecordPlannerValidationFailure(ctx, searchAttrs()...)

	rm := collect(t, reader)
	m := findMetric(rm, "kubeswarm.search.planner.validation_failures")
	if m == nil {
		t.Fatal("kubeswarm.search.planner.validation_failures metric not registered")
	}
	if v, ok := sumInt64(m); !ok || v != 1 {
		t.Errorf("planner.validation_failures: got %d ok=%v, want 1", v, ok)
	}
}

// 9. RecordStagnationIterations records the gauge value.
func TestSearchMetrics_RecordStagnationIterations(t *testing.T) {
	sm, reader := newTestSearchMetrics(t)
	ctx := context.Background()

	sm.RecordStagnationIterations(ctx, 3, searchAttrs()...)

	rm := collect(t, reader)
	m := findMetric(rm, "kubeswarm.search.stagnation_iterations")
	if m == nil {
		t.Fatal("kubeswarm.search.stagnation_iterations metric not registered")
	}
	if v, ok := gaugeInt64(m); !ok || v != 3 {
		t.Errorf("stagnation_iterations: got %d ok=%v, want 3", v, ok)
	}
}

// 10. RecordNodesCreated accumulates across multiple calls.
func TestSearchMetrics_Accumulates(t *testing.T) {
	sm, reader := newTestSearchMetrics(t)
	ctx := context.Background()

	sm.RecordNodesCreated(ctx, 2, searchAttrs()...)
	sm.RecordNodesCreated(ctx, 5, searchAttrs()...)

	rm := collect(t, reader)
	m := findMetric(rm, "kubeswarm.search.nodes.created")
	if m == nil {
		t.Fatal("kubeswarm.search.nodes.created metric not registered")
	}
	if v, ok := sumInt64(m); !ok || v != 7 {
		t.Errorf("nodes.created after two calls: got %d ok=%v, want 7", v, ok)
	}
}
