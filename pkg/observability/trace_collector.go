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
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"time"

	metricnoop "go.opentelemetry.io/otel/metric/noop"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// SpanCollector is an in-memory OTel span exporter used by `swarm run --trace`
// to capture spans locally without requiring a Tempo/Jaeger backend.
// Implements sdktrace.SpanExporter.
type SpanCollector struct {
	mu    sync.Mutex
	spans []sdktrace.ReadOnlySpan
}

var _ sdktrace.SpanExporter = (*SpanCollector)(nil)

// ExportSpans accumulates completed spans in memory.
func (c *SpanCollector) ExportSpans(_ context.Context, spans []sdktrace.ReadOnlySpan) error {
	c.mu.Lock()
	c.spans = append(c.spans, spans...)
	c.mu.Unlock()
	return nil
}

// Shutdown is a no-op — all spans are in memory.
func (c *SpanCollector) Shutdown(_ context.Context) error { return nil }

// Spans returns a snapshot of all collected spans.
func (c *SpanCollector) Spans() []sdktrace.ReadOnlySpan {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]sdktrace.ReadOnlySpan, len(c.spans))
	copy(out, c.spans)
	return out
}

// InitCollector sets up an in-memory TracerProvider for local `swarm run --trace` mode.
// Metrics are no-op (not needed for single local runs).
// Returns the collector (call .Spans() after shutdown to get all spans) and a shutdown func.
func InitCollector(ctx context.Context, serviceName string) (*SpanCollector, func(), error) {
	res, err := buildResource(ctx, serviceName)
	if err != nil {
		return nil, func() {}, err
	}

	col := &SpanCollector{}
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithSyncer(col), // synchronous so spans are flushed before shutdown
		sdktrace.WithResource(res),
	)
	install(tp, metricnoop.NewMeterProvider())

	return col, func() { _ = tp.Shutdown(context.Background()) }, nil
}

// PrintTraceTree renders the collected spans as a human-readable tree to w.
// Spans are grouped by trace, then printed parent-first with indentation.
// Format mirrors the --watch step output for visual consistency.
func PrintTraceTree(w io.Writer, spans []sdktrace.ReadOnlySpan) {
	if len(spans) == 0 {
		return
	}

	// Group spans by trace ID.
	byTrace := make(map[string][]sdktrace.ReadOnlySpan)
	for _, s := range spans {
		tid := s.SpanContext().TraceID().String()
		byTrace[tid] = append(byTrace[tid], s)
	}

	// Sort trace IDs for deterministic output.
	traceIDs := make([]string, 0, len(byTrace))
	for tid := range byTrace {
		traceIDs = append(traceIDs, tid)
	}
	sort.Strings(traceIDs)

	fmt.Fprintf(w, "\nTrace summary (%d trace(s)):\n", len(traceIDs)) //nolint:errcheck
	for _, tid := range traceIDs {
		traceSpans := byTrace[tid]
		fmt.Fprintf(w, "\n  Trace %s\n", tid[:16]+"...") //nolint:errcheck
		printSpanTree(w, traceSpans, "    ")
	}
}

// printSpanTree renders a single trace's spans as a tree.
func printSpanTree(w io.Writer, spans []sdktrace.ReadOnlySpan, indent string) {
	// Build parent→children index.
	byParent := make(map[string][]sdktrace.ReadOnlySpan)
	roots := []sdktrace.ReadOnlySpan{}

	for _, s := range spans {
		pid := s.Parent().SpanID().String()
		if !s.Parent().IsValid() {
			roots = append(roots, s)
		} else {
			byParent[pid] = append(byParent[pid], s)
		}
	}

	// Sort roots by start time.
	sort.Slice(roots, func(i, j int) bool {
		return roots[i].StartTime().Before(roots[j].StartTime())
	})

	var printSpan func(s sdktrace.ReadOnlySpan, prefix string, isLast bool)
	printSpan = func(s sdktrace.ReadOnlySpan, prefix string, isLast bool) {
		connector := "├─"
		childPrefix := prefix + "│  "
		if isLast {
			connector = "└─"
			childPrefix = prefix + "   "
		}

		dur := s.EndTime().Sub(s.StartTime())
		durStr := formatDuration(dur)

		status := ""
		if s.Status().Code.String() == "Error" {
			status = " [ERROR: " + s.Status().Description + "]"
		}

		fmt.Fprintf(w, "%s%s %s  %s%s\n", prefix, connector, s.Name(), durStr, status) //nolint:errcheck

		children := byParent[s.SpanContext().SpanID().String()]
		sort.Slice(children, func(i, j int) bool {
			return children[i].StartTime().Before(children[j].StartTime())
		})
		for i, child := range children {
			printSpan(child, childPrefix, i == len(children)-1)
		}
	}

	for i, root := range roots {
		printSpan(root, indent, i == len(roots)-1)
	}
}

func formatDuration(d time.Duration) string {
	if d < time.Millisecond {
		return fmt.Sprintf("%dμs", d.Microseconds())
	}
	if d < time.Second {
		return fmt.Sprintf("%dms", d.Milliseconds())
	}
	return strings.TrimRight(fmt.Sprintf("%.2fs", d.Seconds()), "0")
}
