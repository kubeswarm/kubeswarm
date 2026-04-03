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
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

const meterName = "github.com/kubeswarm/kubeswarm"

// AgentMetrics holds all OTel instruments for the agent runtime.
// Obtain one via NewAgentMetrics and reuse it for the lifetime of the process.
type AgentMetrics struct {
	taskStarted   metric.Int64Counter
	taskCompleted metric.Int64Counter
	taskFailed    metric.Int64Counter
	taskDuration  metric.Int64Histogram
	taskQueueWait metric.Int64Histogram

	llmCallDuration metric.Int64Histogram
	llmTokensInput  metric.Int64Counter
	llmTokensOutput metric.Int64Counter

	toolCallDuration metric.Int64Histogram
	toolCallErrors   metric.Int64Counter

	delegateSubmitted metric.Int64Counter
}

// NewAgentMetrics creates and registers all agent runtime instruments.
func NewAgentMetrics() (*AgentMetrics, error) {
	m := Meter(meterName)
	var err error
	am := &AgentMetrics{}

	if am.taskStarted, err = m.Int64Counter("kubeswarm.task.started",
		metric.WithDescription("Tasks pulled from queue")); err != nil {
		return nil, err
	}
	if am.taskCompleted, err = m.Int64Counter("kubeswarm.task.completed",
		metric.WithDescription("Tasks completed successfully")); err != nil {
		return nil, err
	}
	if am.taskFailed, err = m.Int64Counter("kubeswarm.task.failed",
		metric.WithDescription("Tasks that errored or hit max retries")); err != nil {
		return nil, err
	}
	if am.taskDuration, err = m.Int64Histogram("kubeswarm.task.duration",
		metric.WithDescription("End-to-end task wall time"),
		metric.WithUnit("ms")); err != nil {
		return nil, err
	}
	if am.taskQueueWait, err = m.Int64Histogram("kubeswarm.task.queue_wait",
		metric.WithDescription("Time from task enqueue to agent poll"),
		metric.WithUnit("ms")); err != nil {
		return nil, err
	}
	if am.llmCallDuration, err = m.Int64Histogram("kubeswarm.llm.call.duration",
		metric.WithDescription("Single LLM API round-trip time"),
		metric.WithUnit("ms")); err != nil {
		return nil, err
	}
	if am.llmTokensInput, err = m.Int64Counter("kubeswarm.llm.tokens.input",
		metric.WithDescription("Input tokens consumed")); err != nil {
		return nil, err
	}
	if am.llmTokensOutput, err = m.Int64Counter("kubeswarm.llm.tokens.output",
		metric.WithDescription("Output tokens produced")); err != nil {
		return nil, err
	}
	if am.toolCallDuration, err = m.Int64Histogram("kubeswarm.tool.call.duration",
		metric.WithDescription("Single tool invocation time"),
		metric.WithUnit("ms")); err != nil {
		return nil, err
	}
	if am.toolCallErrors, err = m.Int64Counter("kubeswarm.tool.call.errors",
		metric.WithDescription("Tool invocations that returned an error")); err != nil {
		return nil, err
	}
	if am.delegateSubmitted, err = m.Int64Counter("kubeswarm.delegate.submitted",
		metric.WithDescription("Tasks submitted via delegate() built-in")); err != nil {
		return nil, err
	}

	return am, nil
}

// RecordTaskStarted increments the started counter.
func (am *AgentMetrics) RecordTaskStarted(ctx context.Context, attrs ...attribute.KeyValue) {
	am.taskStarted.Add(ctx, 1, metric.WithAttributes(attrs...))
}

// RecordTaskCompleted increments the completed counter and records duration.
func (am *AgentMetrics) RecordTaskCompleted(ctx context.Context, since time.Time, attrs ...attribute.KeyValue) {
	am.taskCompleted.Add(ctx, 1, metric.WithAttributes(attrs...))
	am.taskDuration.Record(ctx, time.Since(since).Milliseconds(), metric.WithAttributes(attrs...))
}

// RecordTaskFailed increments the failed counter.
func (am *AgentMetrics) RecordTaskFailed(ctx context.Context, attrs ...attribute.KeyValue) {
	am.taskFailed.Add(ctx, 1, metric.WithAttributes(attrs...))
}

// RecordQueueWait records queue wait time parsed from the enqueued_at RFC3339 timestamp.
// If enqueuedAt is empty or unparseable, the observation is skipped.
func (am *AgentMetrics) RecordQueueWait(ctx context.Context, enqueuedAt string, attrs ...attribute.KeyValue) {
	if enqueuedAt == "" {
		return
	}
	t, err := time.Parse(time.RFC3339, enqueuedAt)
	if err != nil {
		return
	}
	am.taskQueueWait.Record(ctx, time.Since(t).Milliseconds(), metric.WithAttributes(attrs...))
}

// RecordLLMCall records a single LLM round-trip duration and token usage.
func (am *AgentMetrics) RecordLLMCall(ctx context.Context, since time.Time, inputTokens, outputTokens int64, attrs ...attribute.KeyValue) {
	opt := metric.WithAttributes(attrs...)
	am.llmCallDuration.Record(ctx, time.Since(since).Milliseconds(), opt)
	if inputTokens > 0 {
		am.llmTokensInput.Add(ctx, inputTokens, opt)
	}
	if outputTokens > 0 {
		am.llmTokensOutput.Add(ctx, outputTokens, opt)
	}
}

// RecordToolCall records a tool invocation duration and optionally an error.
func (am *AgentMetrics) RecordToolCall(ctx context.Context, since time.Time, failed bool, attrs ...attribute.KeyValue) {
	opt := metric.WithAttributes(attrs...)
	am.toolCallDuration.Record(ctx, time.Since(since).Milliseconds(), opt)
	if failed {
		am.toolCallErrors.Add(ctx, 1, opt)
	}
}

// RecordDelegate increments the delegate submission counter.
func (am *AgentMetrics) RecordDelegate(ctx context.Context, attrs ...attribute.KeyValue) {
	am.delegateSubmitted.Add(ctx, 1, metric.WithAttributes(attrs...))
}

// OperatorMetrics holds OTel instruments for the operator reconcile loops.
type OperatorMetrics struct {
	reconcileDuration metric.Int64Histogram
	reconcileErrors   metric.Int64Counter
}

// NewOperatorMetrics creates and registers all operator instruments.
func NewOperatorMetrics() (*OperatorMetrics, error) {
	m := Meter(meterName)
	var err error
	om := &OperatorMetrics{}

	if om.reconcileDuration, err = m.Int64Histogram("kubeswarm.reconcile.duration",
		metric.WithDescription("Reconcile loop latency"),
		metric.WithUnit("ms")); err != nil {
		return nil, err
	}
	if om.reconcileErrors, err = m.Int64Counter("kubeswarm.reconcile.errors",
		metric.WithDescription("Reconcile loops that returned an error")); err != nil {
		return nil, err
	}
	return om, nil
}

// RecordReconcile records reconcile latency and optionally an error.
func (om *OperatorMetrics) RecordReconcile(ctx context.Context, since time.Time, failed bool, attrs ...attribute.KeyValue) {
	opt := metric.WithAttributes(attrs...)
	om.reconcileDuration.Record(ctx, time.Since(since).Milliseconds(), opt)
	if failed {
		om.reconcileErrors.Add(ctx, 1, opt)
	}
}
