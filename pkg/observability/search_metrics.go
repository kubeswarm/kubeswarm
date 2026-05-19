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

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// SearchMetrics holds all OTel instruments for search tree orchestration (RFC-0050).
// Obtain one via NewSearchMetrics and reuse it for the lifetime of the process.
type SearchMetrics struct {
	nodesCreated              metric.Int64Counter
	nodesPruned               metric.Int64Counter
	nodeScore                 metric.Int64Histogram
	iterations                metric.Int64Counter
	bestScore                 metric.Int64Gauge
	evaluatorParseFailures    metric.Int64Counter
	plannerValidationFailures metric.Int64Counter
	stagnationIterations      metric.Int64Gauge
}

// NewSearchMetrics creates and registers all search tree orchestration instruments.
func NewSearchMetrics() (*SearchMetrics, error) {
	m := Meter(meterName)
	var err error
	sm := &SearchMetrics{}

	if sm.nodesCreated, err = m.Int64Counter("kubeswarm.search.nodes.created",
		metric.WithDescription("Search tree nodes created")); err != nil {
		return nil, err
	}
	if sm.nodesPruned, err = m.Int64Counter("kubeswarm.search.nodes.pruned",
		metric.WithDescription("Search tree nodes pruned")); err != nil {
		return nil, err
	}
	if sm.nodeScore, err = m.Int64Histogram("kubeswarm.search.node.score",
		metric.WithDescription("Search node score distribution"),
		metric.WithUnit("millis")); err != nil {
		return nil, err
	}
	if sm.iterations, err = m.Int64Counter("kubeswarm.search.iterations",
		metric.WithDescription("Search planner iterations")); err != nil {
		return nil, err
	}
	if sm.bestScore, err = m.Int64Gauge("kubeswarm.search.best_score",
		metric.WithDescription("Current best search node score"),
		metric.WithUnit("millis")); err != nil {
		return nil, err
	}
	if sm.evaluatorParseFailures, err = m.Int64Counter("kubeswarm.search.evaluator.parse_failures",
		metric.WithDescription("Evaluator output parse failures")); err != nil {
		return nil, err
	}
	if sm.plannerValidationFailures, err = m.Int64Counter("kubeswarm.search.planner.validation_failures",
		metric.WithDescription("Planner output validation failures")); err != nil {
		return nil, err
	}
	if sm.stagnationIterations, err = m.Int64Gauge("kubeswarm.search.stagnation_iterations",
		metric.WithDescription("Consecutive iterations without score improvement")); err != nil {
		return nil, err
	}

	return sm, nil
}

// RecordNodesCreated increments the nodes created counter.
func (sm *SearchMetrics) RecordNodesCreated(ctx context.Context, count int64, attrs ...attribute.KeyValue) {
	sm.nodesCreated.Add(ctx, count, metric.WithAttributes(attrs...))
}

// RecordNodesPruned increments the nodes pruned counter.
func (sm *SearchMetrics) RecordNodesPruned(ctx context.Context, count int64, attrs ...attribute.KeyValue) {
	sm.nodesPruned.Add(ctx, count, metric.WithAttributes(attrs...))
}

// RecordNodeScore records a search node score observation.
func (sm *SearchMetrics) RecordNodeScore(ctx context.Context, scoreMillis int64, attrs ...attribute.KeyValue) {
	sm.nodeScore.Record(ctx, scoreMillis, metric.WithAttributes(attrs...))
}

// RecordIteration increments the planner iteration counter.
func (sm *SearchMetrics) RecordIteration(ctx context.Context, attrs ...attribute.KeyValue) {
	sm.iterations.Add(ctx, 1, metric.WithAttributes(attrs...))
}

// RecordBestScore records the current best search node score.
func (sm *SearchMetrics) RecordBestScore(ctx context.Context, scoreMillis int64, attrs ...attribute.KeyValue) {
	sm.bestScore.Record(ctx, scoreMillis, metric.WithAttributes(attrs...))
}

// RecordEvaluatorParseFailure increments the evaluator parse failure counter.
func (sm *SearchMetrics) RecordEvaluatorParseFailure(ctx context.Context, attrs ...attribute.KeyValue) {
	sm.evaluatorParseFailures.Add(ctx, 1, metric.WithAttributes(attrs...))
}

// RecordPlannerValidationFailure increments the planner validation failure counter.
func (sm *SearchMetrics) RecordPlannerValidationFailure(ctx context.Context, attrs ...attribute.KeyValue) {
	sm.plannerValidationFailures.Add(ctx, 1, metric.WithAttributes(attrs...))
}

// RecordStagnationIterations records the current stagnation iteration count.
func (sm *SearchMetrics) RecordStagnationIterations(ctx context.Context, count int64, attrs ...attribute.KeyValue) {
	sm.stagnationIterations.Record(ctx, count, metric.WithAttributes(attrs...))
}
