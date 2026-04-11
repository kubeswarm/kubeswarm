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
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

// newTestMetrics installs a fresh MeterProvider backed by a ManualReader
// as the global OTel provider, then constructs an AgentMetrics bound to it.
// Returns the metrics, the reader (for collection), and a cleanup func.
func newTestMetrics(t *testing.T) (*AgentMetrics, *sdkmetric.ManualReader) {
	t.Helper()
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	otel.SetMeterProvider(mp)
	t.Cleanup(func() {
		_ = mp.Shutdown(context.Background())
	})
	am, err := NewAgentMetrics()
	if err != nil {
		t.Fatalf("NewAgentMetrics: %v", err)
	}
	return am, reader
}

func collect(t *testing.T, reader *sdkmetric.ManualReader) metricdata.ResourceMetrics {
	t.Helper()
	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("collect: %v", err)
	}
	return rm
}

// findMetric returns the metric with the given name, or nil.
func findMetric(rm metricdata.ResourceMetrics, name string) *metricdata.Metrics {
	for i := range rm.ScopeMetrics {
		for j := range rm.ScopeMetrics[i].Metrics {
			m := &rm.ScopeMetrics[i].Metrics[j]
			if m.Name == name {
				return m
			}
		}
	}
	return nil
}

// sumInt64 extracts the single data-point sum for an Int64 sum metric.
// Returns (sum, found). If there are multiple points, returns the first.
func sumInt64(m *metricdata.Metrics) (int64, bool) {
	if m == nil {
		return 0, false
	}
	s, ok := m.Data.(metricdata.Sum[int64])
	if !ok {
		return 0, false
	}
	if len(s.DataPoints) == 0 {
		return 0, false
	}
	return s.DataPoints[0].Value, true
}

// sumInt64ForAttr returns the sum for a data point that contains the given
// attribute key/value.
func sumInt64ForAttr(m *metricdata.Metrics, key, want string) (int64, bool) {
	if m == nil {
		return 0, false
	}
	s, ok := m.Data.(metricdata.Sum[int64])
	if !ok {
		return 0, false
	}
	for _, dp := range s.DataPoints {
		if v, ok := dp.Attributes.Value(attribute.Key(key)); ok && v.AsString() == want {
			return dp.Value, true
		}
	}
	return 0, false
}

func baseAttrs() []attribute.KeyValue {
	return []attribute.KeyValue{
		attribute.String("namespace", "ns"),
		attribute.String("agent", "a1"),
		attribute.String("model", "claude-sonnet-4-5"),
		attribute.String("provider", "anthropic"),
	}
}

// 1. Registration: all 5 new reasoning instruments + thinking tokens are present
// after NewAgentMetrics, once a first observation is made so they show up in
// the reader snapshot.
func TestAgentMetrics_NewAgentMetricsRegisters(t *testing.T) {
	am, reader := newTestMetrics(t)
	ctx := context.Background()
	attrs := baseAttrs()

	// Touch each instrument so it appears in collected data.
	am.RecordReasoningCall(ctx, 1, attrs...)
	am.RecordReasoningClamped(ctx, "anthropic_budget", attrs...)
	am.RecordReasoningIgnored(ctx, attrs...)
	am.RecordReasoningRejected(ctx, attrs...)

	rm := collect(t, reader)
	want := []string{
		"kubeswarm.llm.thinking_tokens",
		"kubeswarm.llm.reasoning.calls",
		"kubeswarm.llm.reasoning.clamped",
		"kubeswarm.llm.reasoning.ignored",
		"kubeswarm.llm.reasoning.rejected",
	}
	for _, name := range want {
		if findMetric(rm, name) == nil {
			t.Errorf("expected metric %q to be registered", name)
		}
	}
}

// 2. RecordReasoningCall increments both the call counter and thinking tokens.
func TestRecordReasoningCall_IncrementsCounter(t *testing.T) {
	am, reader := newTestMetrics(t)
	am.RecordReasoningCall(context.Background(), 1024, baseAttrs()...)

	rm := collect(t, reader)
	if v, ok := sumInt64(findMetric(rm, "kubeswarm.llm.reasoning.calls")); !ok || v != 1 {
		t.Errorf("reasoning.calls: got %d ok=%v, want 1", v, ok)
	}
	if v, ok := sumInt64(findMetric(rm, "kubeswarm.llm.thinking_tokens")); !ok || v != 1024 {
		t.Errorf("thinking_tokens: got %d ok=%v, want 1024", v, ok)
	}
}

// 3. Zero thinking tokens: call counter still increments, thinking stays 0.
func TestRecordReasoningCall_ZeroThinkingTokensStillIncrementsCallCounter(t *testing.T) {
	am, reader := newTestMetrics(t)
	am.RecordReasoningCall(context.Background(), 0, baseAttrs()...)

	rm := collect(t, reader)
	if v, ok := sumInt64(findMetric(rm, "kubeswarm.llm.reasoning.calls")); !ok || v != 1 {
		t.Errorf("reasoning.calls: got %d ok=%v, want 1", v, ok)
	}
	// thinking_tokens should either be absent or be 0.
	if m := findMetric(rm, "kubeswarm.llm.thinking_tokens"); m != nil {
		if v, ok := sumInt64(m); ok && v != 0 {
			t.Errorf("thinking_tokens: got %d, want 0", v)
		}
	}
}

// 4. RecordReasoningClamped carries its reason label.
func TestRecordReasoningClamped_CarriesReason(t *testing.T) {
	am, reader := newTestMetrics(t)
	am.RecordReasoningClamped(context.Background(), "anthropic_budget", baseAttrs()...)

	rm := collect(t, reader)
	m := findMetric(rm, "kubeswarm.llm.reasoning.clamped")
	if m == nil {
		t.Fatal("kubeswarm.llm.reasoning.clamped not registered")
	}
	if v, ok := sumInt64ForAttr(m, "reason", "anthropic_budget"); !ok || v != 1 {
		t.Errorf("clamped[reason=anthropic_budget]: got %d ok=%v, want 1", v, ok)
	}
}

// 5. RecordReasoningIgnored: single call increments.
func TestRecordReasoningIgnored(t *testing.T) {
	am, reader := newTestMetrics(t)
	am.RecordReasoningIgnored(context.Background(), baseAttrs()...)

	rm := collect(t, reader)
	if v, ok := sumInt64(findMetric(rm, "kubeswarm.llm.reasoning.ignored")); !ok || v != 1 {
		t.Errorf("reasoning.ignored: got %d ok=%v, want 1", v, ok)
	}
}

// 6. RecordReasoningRejected: single call increments.
func TestRecordReasoningRejected(t *testing.T) {
	am, reader := newTestMetrics(t)
	am.RecordReasoningRejected(context.Background(), baseAttrs()...)

	rm := collect(t, reader)
	if v, ok := sumInt64(findMetric(rm, "kubeswarm.llm.reasoning.rejected")); !ok || v != 1 {
		t.Errorf("reasoning.rejected: got %d ok=%v, want 1", v, ok)
	}
}

// 7. RecordLLMCall now takes thinkingTokens; positive values update all three
// token counters.
func TestRecordLLMCall_WithThinkingTokens(t *testing.T) {
	am, reader := newTestMetrics(t)
	am.RecordLLMCall(context.Background(), time.Now(), 100, 50, 200, baseAttrs()...)

	rm := collect(t, reader)
	if v, ok := sumInt64(findMetric(rm, "kubeswarm.llm.tokens.input")); !ok || v != 100 {
		t.Errorf("tokens.input: got %d ok=%v, want 100", v, ok)
	}
	if v, ok := sumInt64(findMetric(rm, "kubeswarm.llm.tokens.output")); !ok || v != 50 {
		t.Errorf("tokens.output: got %d ok=%v, want 50", v, ok)
	}
	if v, ok := sumInt64(findMetric(rm, "kubeswarm.llm.thinking_tokens")); !ok || v != 200 {
		t.Errorf("thinking_tokens: got %d ok=%v, want 200", v, ok)
	}
}

// 8. Backwards-compatible call pattern: callers that still pass 0 for thinking
// tokens do not bump the thinking counter, but still record input/output.
func TestRecordLLMCall_BackwardsCompatibleCallers(t *testing.T) {
	am, reader := newTestMetrics(t)
	am.RecordLLMCall(context.Background(), time.Now(), 10, 20, 0, baseAttrs()...)

	rm := collect(t, reader)
	if v, ok := sumInt64(findMetric(rm, "kubeswarm.llm.tokens.input")); !ok || v != 10 {
		t.Errorf("tokens.input: got %d ok=%v, want 10", v, ok)
	}
	if v, ok := sumInt64(findMetric(rm, "kubeswarm.llm.tokens.output")); !ok || v != 20 {
		t.Errorf("tokens.output: got %d ok=%v, want 20", v, ok)
	}
	if m := findMetric(rm, "kubeswarm.llm.thinking_tokens"); m != nil {
		if v, ok := sumInt64(m); ok && v != 0 {
			t.Errorf("thinking_tokens: got %d, want 0 (no emission)", v)
		}
	}
}

// 9. RecordToolRefresh increments the kubeswarm.mcp.tools.refreshed counter.
func TestRecordToolRefresh_IncrementsCounter(t *testing.T) {
	am, reader := newTestMetrics(t)
	ctx := context.Background()
	am.RecordToolRefresh(ctx, baseAttrs()...)

	rm := collect(t, reader)
	m := findMetric(rm, "kubeswarm.mcp.tools.refreshed")
	if m == nil {
		t.Fatal("kubeswarm.mcp.tools.refreshed metric not registered")
	}
	if v, ok := sumInt64(m); !ok || v != 1 {
		t.Errorf("mcp.tools.refreshed: got %d ok=%v, want 1", v, ok)
	}

	// Call again and verify it accumulates.
	am.RecordToolRefresh(ctx, baseAttrs()...)
	rm = collect(t, reader)
	m = findMetric(rm, "kubeswarm.mcp.tools.refreshed")
	if v, ok := sumInt64(m); !ok || v != 2 {
		t.Errorf("mcp.tools.refreshed after 2 calls: got %d ok=%v, want 2", v, ok)
	}
}

// 10. RecordMCPCallDuration emits kubeswarm.mcp.call.duration histogram.
func TestRecordMCPCallDuration_EmitsHistogram(t *testing.T) {
	am, reader := newTestMetrics(t)
	ctx := context.Background()
	attrs := append(baseAttrs(),
		attribute.String("server", "mcp-tools"),
		attribute.String("tool", "search"),
	)
	am.RecordMCPCallDuration(ctx, 50*time.Millisecond, attrs...)

	rm := collect(t, reader)
	m := findMetric(rm, "kubeswarm.mcp.call.duration")
	if m == nil {
		t.Fatal("kubeswarm.mcp.call.duration metric not registered")
	}
	h, ok := m.Data.(metricdata.Histogram[int64])
	if !ok {
		t.Fatalf("kubeswarm.mcp.call.duration data type = %T, want Histogram[int64]", m.Data)
	}
	if len(h.DataPoints) == 0 {
		t.Fatal("kubeswarm.mcp.call.duration has no data points")
	}
	if h.DataPoints[0].Count != 1 {
		t.Errorf("kubeswarm.mcp.call.duration count = %d, want 1", h.DataPoints[0].Count)
	}
}

// 11. RecordMCPCallError increments kubeswarm.mcp.call.errors counter.
func TestRecordMCPCallError_IncrementsCounter(t *testing.T) {
	am, reader := newTestMetrics(t)
	ctx := context.Background()
	attrs := append(baseAttrs(),
		attribute.String("server", "mcp-tools"),
		attribute.String("tool", "search"),
		attribute.String("error_type", "timeout"),
	)
	am.RecordMCPCallError(ctx, attrs...)

	rm := collect(t, reader)
	m := findMetric(rm, "kubeswarm.mcp.call.errors")
	if m == nil {
		t.Fatal("kubeswarm.mcp.call.errors metric not registered")
	}
	if v, ok := sumInt64(m); !ok || v != 1 {
		t.Errorf("mcp.call.errors: got %d ok=%v, want 1", v, ok)
	}
}

// 12. RecordAgentError increments the kubeswarm.agent.errors counter with a code label.
func TestRecordAgentError_IncrementsWithCode(t *testing.T) {
	am, reader := newTestMetrics(t)
	ctx := context.Background()
	am.RecordAgentError(ctx, "LLMTimeout", baseAttrs()...)

	rm := collect(t, reader)
	m := findMetric(rm, "kubeswarm.agent.errors")
	if m == nil {
		t.Fatal("kubeswarm.agent.errors metric not registered")
	}
	if v, ok := sumInt64ForAttr(m, "code", "LLMTimeout"); !ok || v != 1 {
		t.Errorf("agent.errors[code=LLMTimeout]: got %d ok=%v, want 1", v, ok)
	}
}

// 13. RecordCircuitRejected increments the kubeswarm.circuit.rejected counter.
func TestRecordCircuitRejected_IncrementsCounter(t *testing.T) {
	am, reader := newTestMetrics(t)
	ctx := context.Background()
	attrs := append(baseAttrs(),
		attribute.String("endpoint", "https://api.example.com/v1"),
	)
	am.RecordCircuitRejected(ctx, attrs...)

	rm := collect(t, reader)
	m := findMetric(rm, "kubeswarm.circuit.rejected")
	if m == nil {
		t.Fatal("kubeswarm.circuit.rejected metric not registered")
	}
	if v, ok := sumInt64(m); !ok || v != 1 {
		t.Errorf("circuit.rejected: got %d ok=%v, want 1", v, ok)
	}

	// Call again and verify it accumulates.
	am.RecordCircuitRejected(ctx, attrs...)
	rm = collect(t, reader)
	m = findMetric(rm, "kubeswarm.circuit.rejected")
	if v, ok := sumInt64(m); !ok || v != 2 {
		t.Errorf("circuit.rejected after 2 calls: got %d ok=%v, want 2", v, ok)
	}
}
