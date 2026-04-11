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

	llmCallDuration   metric.Int64Histogram
	llmTokensInput    metric.Int64Counter
	llmTokensOutput   metric.Int64Counter
	llmThinkingTokens metric.Int64Counter

	reasoningCalls    metric.Int64Counter
	reasoningClamped  metric.Int64Counter
	reasoningIgnored  metric.Int64Counter
	reasoningRejected metric.Int64Counter

	costEstimationDegraded metric.Int64Gauge

	toolCallDuration metric.Int64Histogram
	toolCallErrors   metric.Int64Counter

	mcpCallDuration metric.Int64Histogram
	mcpCallErrors   metric.Int64Counter

	delegateSubmitted metric.Int64Counter

	toolRefreshes metric.Int64Counter

	agentErrors metric.Int64Counter

	circuitRejected metric.Int64Counter
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
	if am.llmThinkingTokens, err = m.Int64Counter("kubeswarm.llm.thinking_tokens",
		metric.WithDescription("Thinking tokens consumed by reasoning-capable models")); err != nil {
		return nil, err
	}
	if am.reasoningCalls, err = m.Int64Counter("kubeswarm.llm.reasoning.calls",
		metric.WithDescription("LLM calls that had reasoning enabled")); err != nil {
		return nil, err
	}
	if am.reasoningClamped, err = m.Int64Counter("kubeswarm.llm.reasoning.clamped",
		metric.WithDescription("Reasoning calls where runtime clamped budget or effort")); err != nil {
		return nil, err
	}
	if am.reasoningIgnored, err = m.Int64Counter("kubeswarm.llm.reasoning.ignored",
		metric.WithDescription("Reasoning Auto mode calls on non-reasoning models")); err != nil {
		return nil, err
	}
	if am.reasoningRejected, err = m.Int64Counter("kubeswarm.llm.reasoning.rejected",
		metric.WithDescription("Reasoning Explicit mode reconciles rejected")); err != nil {
		return nil, err
	}
	if am.costEstimationDegraded, err = m.Int64Gauge("kubeswarm.cost.estimation_degraded",
		metric.WithDescription("Gauge asserted when thinking-token pricing is degraded")); err != nil {
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
	if am.mcpCallDuration, err = m.Int64Histogram("kubeswarm.mcp.call.duration",
		metric.WithDescription("MCP server tool call round-trip time"),
		metric.WithUnit("ms")); err != nil {
		return nil, err
	}
	if am.mcpCallErrors, err = m.Int64Counter("kubeswarm.mcp.call.errors",
		metric.WithDescription("MCP server tool calls that returned an error")); err != nil {
		return nil, err
	}
	if am.delegateSubmitted, err = m.Int64Counter("kubeswarm.delegate.submitted",
		metric.WithDescription("Tasks submitted via delegate() built-in")); err != nil {
		return nil, err
	}
	if am.toolRefreshes, err = m.Int64Counter("kubeswarm.mcp.tools.refreshed",
		metric.WithDescription("MCP tool list refreshes")); err != nil {
		return nil, err
	}
	if am.agentErrors, err = m.Int64Counter("kubeswarm.agent.errors",
		metric.WithDescription("Agent runtime errors by code")); err != nil {
		return nil, err
	}
	if am.circuitRejected, err = m.Int64Counter("kubeswarm.circuit.rejected",
		metric.WithDescription("Calls rejected by the circuit breaker")); err != nil {
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
// thinkingTokens is the count of reasoning/thinking tokens consumed during the
// call; pass 0 when the provider does not report a thinking-token count.
func (am *AgentMetrics) RecordLLMCall(ctx context.Context, since time.Time, inputTokens, outputTokens, thinkingTokens int64, attrs ...attribute.KeyValue) {
	opt := metric.WithAttributes(attrs...)
	am.llmCallDuration.Record(ctx, time.Since(since).Milliseconds(), opt)
	if inputTokens > 0 {
		am.llmTokensInput.Add(ctx, inputTokens, opt)
	}
	if outputTokens > 0 {
		am.llmTokensOutput.Add(ctx, outputTokens, opt)
	}
	if thinkingTokens > 0 {
		am.llmThinkingTokens.Add(ctx, thinkingTokens, opt)
	}
}

// RecordReasoningCall increments the reasoning call counter and the thinking
// tokens counter. Called once per LLM turn that had reasoning enabled.
// Pass thinkingTokens=0 when the provider did not report a count.
func (am *AgentMetrics) RecordReasoningCall(ctx context.Context, thinkingTokens int64, attrs ...attribute.KeyValue) {
	opt := metric.WithAttributes(attrs...)
	am.reasoningCalls.Add(ctx, 1, opt)
	if thinkingTokens > 0 {
		am.llmThinkingTokens.Add(ctx, thinkingTokens, opt)
	}
}

// RecordReasoningClamped increments the clamp counter with a reason label.
// reason is typically "anthropic_budget" or "openai_effort" per DD3.
func (am *AgentMetrics) RecordReasoningClamped(ctx context.Context, reason string, attrs ...attribute.KeyValue) {
	all := append([]attribute.KeyValue{attribute.String("reason", reason)}, attrs...)
	am.reasoningClamped.Add(ctx, 1, metric.WithAttributes(all...))
}

// RecordReasoningIgnored increments the ignored counter for Auto-mode reasoning
// requests on non-reasoning models.
func (am *AgentMetrics) RecordReasoningIgnored(ctx context.Context, attrs ...attribute.KeyValue) {
	am.reasoningIgnored.Add(ctx, 1, metric.WithAttributes(attrs...))
}

// RecordReasoningRejected increments the rejected counter for Explicit-mode
// reconciles that were rejected.
func (am *AgentMetrics) RecordReasoningRejected(ctx context.Context, attrs ...attribute.KeyValue) {
	am.reasoningRejected.Add(ctx, 1, metric.WithAttributes(attrs...))
}

// AssertCostEstimationDegraded sets the degraded gauge to 1 with a reason label.
// Re-asserted on every reconcile so dashboards scraping after warmup see it.
func (am *AgentMetrics) AssertCostEstimationDegraded(ctx context.Context, reason string) {
	am.costEstimationDegraded.Record(ctx, 1, metric.WithAttributes(attribute.String("reason", reason)))
}

// RecordToolCall records a tool invocation duration and optionally an error.
func (am *AgentMetrics) RecordToolCall(ctx context.Context, since time.Time, failed bool, attrs ...attribute.KeyValue) {
	opt := metric.WithAttributes(attrs...)
	am.toolCallDuration.Record(ctx, time.Since(since).Milliseconds(), opt)
	if failed {
		am.toolCallErrors.Add(ctx, 1, opt)
	}
}

// RecordMCPCallDuration records a single MCP server tool call duration.
func (am *AgentMetrics) RecordMCPCallDuration(ctx context.Context, latency time.Duration, attrs ...attribute.KeyValue) {
	am.mcpCallDuration.Record(ctx, latency.Milliseconds(), metric.WithAttributes(attrs...))
}

// RecordMCPCallError increments the MCP call error counter.
func (am *AgentMetrics) RecordMCPCallError(ctx context.Context, attrs ...attribute.KeyValue) {
	am.mcpCallErrors.Add(ctx, 1, metric.WithAttributes(attrs...))
}

// RecordDelegate increments the delegate submission counter.
func (am *AgentMetrics) RecordDelegate(ctx context.Context, attrs ...attribute.KeyValue) {
	am.delegateSubmitted.Add(ctx, 1, metric.WithAttributes(attrs...))
}

// RecordToolRefresh increments the MCP tool list refresh counter.
func (am *AgentMetrics) RecordToolRefresh(ctx context.Context, attrs ...attribute.KeyValue) {
	am.toolRefreshes.Add(ctx, 1, metric.WithAttributes(attrs...))
}

// RecordAgentError increments the agent error counter with the given error code label.
func (am *AgentMetrics) RecordAgentError(ctx context.Context, code string, attrs ...attribute.KeyValue) {
	all := append([]attribute.KeyValue{attribute.String("code", code)}, attrs...)
	am.agentErrors.Add(ctx, 1, metric.WithAttributes(all...))
}

// RecordCircuitRejected increments the circuit breaker rejection counter.
func (am *AgentMetrics) RecordCircuitRejected(ctx context.Context, attrs ...attribute.KeyValue) {
	am.circuitRejected.Add(ctx, 1, metric.WithAttributes(attrs...))
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
