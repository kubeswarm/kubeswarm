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
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// RemoteSpan is a simplified span representation fetched from a Jaeger-compatible HTTP API.
// It carries only the fields needed for tree rendering.
type RemoteSpan struct {
	TraceID   string
	SpanID    string
	ParentID  string // empty for root spans
	Name      string
	StartTime time.Time
	Duration  time.Duration
	Error     bool
	ErrorMsg  string
}

// FetchTraceByTaskID queries a Jaeger-compatible HTTP API for all spans tagged
// with `task.id=<taskID>` under the given service. Returns a flat list of spans
// that can be passed to PrintRemoteTraceTree.
//
// Supported backends:
//   - Jaeger  (default port :16686) — GET /api/traces?service=<svc>&tags=task.id:<id>
//   - Grafana Tempo (:3200)         — GET /api/search?tags=task.id=<id>&service.name=<svc>
//     followed by individual trace fetch at /api/traces/<traceID>
func FetchTraceByTaskID(ctx context.Context, endpoint, service, taskID string) ([]RemoteSpan, error) {
	// Normalise endpoint: strip trailing slash.
	endpoint = strings.TrimRight(endpoint, "/")

	// Try Jaeger first (it has a simpler single-request API).
	spans, err := fetchJaeger(ctx, endpoint, service, taskID)
	if err == nil {
		return spans, nil
	}

	// Fall back to Tempo search + individual trace fetch.
	return fetchTempo(ctx, endpoint, service, taskID)
}

// PrintRemoteTraceTree renders a slice of RemoteSpan as a human-readable tree.
// Format is consistent with PrintTraceTree (used by the in-process collector).
func PrintRemoteTraceTree(w io.Writer, spans []RemoteSpan) {
	if len(spans) == 0 {
		return
	}

	// Group by trace ID.
	byTrace := make(map[string][]RemoteSpan)
	for _, s := range spans {
		byTrace[s.TraceID] = append(byTrace[s.TraceID], s)
	}

	traceIDs := make([]string, 0, len(byTrace))
	for tid := range byTrace {
		traceIDs = append(traceIDs, tid)
	}
	sort.Strings(traceIDs)

	fmt.Fprintf(w, "\nTrace summary (%d trace(s)):\n", len(traceIDs)) //nolint:errcheck
	for _, tid := range traceIDs {
		traceSpans := byTrace[tid]
		shortID := tid
		if len(tid) > 16 {
			shortID = tid[:16] + "..."
		}
		fmt.Fprintf(w, "\n  Trace %s\n", shortID) //nolint:errcheck
		printRemoteSpanTree(w, traceSpans, "    ")
	}
}

func printRemoteSpanTree(w io.Writer, spans []RemoteSpan, indent string) {
	byParent := make(map[string][]RemoteSpan)
	var roots []RemoteSpan

	for _, s := range spans {
		if s.ParentID == "" {
			roots = append(roots, s)
		} else {
			byParent[s.ParentID] = append(byParent[s.ParentID], s)
		}
	}
	sort.Slice(roots, func(i, j int) bool {
		return roots[i].StartTime.Before(roots[j].StartTime)
	})

	var printSpan func(s RemoteSpan, prefix string, isLast bool)
	printSpan = func(s RemoteSpan, prefix string, isLast bool) {
		connector := "├─"
		childPrefix := prefix + "│  "
		if isLast {
			connector = "└─"
			childPrefix = prefix + "   "
		}
		status := ""
		if s.Error {
			status = " [ERROR: " + s.ErrorMsg + "]"
		}
		fmt.Fprintf(w, "%s%s %s  %s%s\n", prefix, connector, s.Name, formatDuration(s.Duration), status) //nolint:errcheck
		children := byParent[s.SpanID]
		sort.Slice(children, func(i, j int) bool {
			return children[i].StartTime.Before(children[j].StartTime)
		})
		for i, child := range children {
			printSpan(child, childPrefix, i == len(children)-1)
		}
	}
	for i, root := range roots {
		printSpan(root, indent, i == len(roots)-1)
	}
}

// --- Jaeger HTTP API ---

type jaegerResponse struct {
	Data []jaegerTrace `json:"data"`
}

type jaegerTrace struct {
	TraceID string       `json:"traceID"`
	Spans   []jaegerSpan `json:"spans"`
}

type jaegerSpan struct {
	TraceID       string            `json:"traceID"`
	SpanID        string            `json:"spanID"`
	OperationName string            `json:"operationName"`
	StartTime     int64             `json:"startTime"` // microseconds since epoch
	Duration      int64             `json:"duration"`  // microseconds
	References    []jaegerReference `json:"references"`
	Tags          []jaegerKV        `json:"tags"`
}

type jaegerReference struct {
	RefType string `json:"refType"`
	SpanID  string `json:"spanID"`
}

type jaegerKV struct {
	Key   string `json:"key"`
	Value any    `json:"value"`
}

func fetchJaeger(ctx context.Context, endpoint, service, taskID string) ([]RemoteSpan, error) {
	u, err := url.Parse(endpoint + "/api/traces")
	if err != nil {
		return nil, err
	}
	q := u.Query()
	q.Set("service", service)
	q.Set("tags", fmt.Sprintf(`{"task.id":"%s"}`, taskID))
	q.Set("limit", "1000")
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("jaeger returned %d", resp.StatusCode)
	}

	var jr jaegerResponse
	if err := json.NewDecoder(resp.Body).Decode(&jr); err != nil {
		return nil, err
	}

	var out []RemoteSpan
	for _, trace := range jr.Data {
		for _, s := range trace.Spans {
			rs := RemoteSpan{
				TraceID:   trace.TraceID,
				SpanID:    s.SpanID,
				Name:      s.OperationName,
				StartTime: time.UnixMicro(s.StartTime),
				Duration:  time.Duration(s.Duration) * time.Microsecond,
			}
			for _, ref := range s.References {
				if ref.RefType == "CHILD_OF" {
					rs.ParentID = ref.SpanID
					break
				}
			}
			for _, tag := range s.Tags {
				if tag.Key == "error" {
					rs.Error = true
				}
				if tag.Key == "otel.status_description" {
					if msg, ok := tag.Value.(string); ok {
						rs.ErrorMsg = msg
					}
				}
			}
			out = append(out, rs)
		}
	}
	return out, nil
}

// --- Tempo HTTP API ---

type tempoSearchResponse struct {
	Traces []tempoTraceRef `json:"traces"`
}

type tempoTraceRef struct {
	TraceID string `json:"traceID"`
}

type tempoTrace struct {
	Batches []tempoBatch `json:"batches"`
}

type tempoBatch struct {
	ScopeSpans []tempoScopeSpans `json:"scopeSpans"`
}

type tempoScopeSpans struct {
	Spans []tempoSpan `json:"spans"`
}

type tempoSpan struct {
	TraceID           string      `json:"traceId"`
	SpanID            string      `json:"spanId"`
	ParentSpanID      string      `json:"parentSpanId"`
	Name              string      `json:"name"`
	StartTimeUnixNano string      `json:"startTimeUnixNano"`
	EndTimeUnixNano   string      `json:"endTimeUnixNano"`
	Attributes        []tempoAttr `json:"attributes"`
	Status            tempoStatus `json:"status"`
}

type tempoAttr struct {
	Key   string `json:"key"`
	Value struct {
		StringValue string `json:"stringValue"`
	} `json:"value"`
}

type tempoStatus struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func fetchTempo(ctx context.Context, endpoint, service, taskID string) ([]RemoteSpan, error) {
	// Step 1: search for trace IDs.
	u, err := url.Parse(endpoint + "/api/search")
	if err != nil {
		return nil, err
	}
	q := u.Query()
	q.Set("tags", fmt.Sprintf("task.id=%s service.name=%s", taskID, service))
	q.Set("limit", "10")
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("tempo search returned %d", resp.StatusCode)
	}

	var sr tempoSearchResponse
	if err := json.NewDecoder(resp.Body).Decode(&sr); err != nil {
		return nil, err
	}

	var out []RemoteSpan
	for _, ref := range sr.Traces {
		spans, err := fetchTempoTrace(ctx, endpoint, ref.TraceID)
		if err != nil {
			continue
		}
		out = append(out, spans...)
	}
	return out, nil
}

func fetchTempoTrace(ctx context.Context, endpoint, traceID string) ([]RemoteSpan, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+"/api/traces/"+traceID, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("tempo trace fetch returned %d", resp.StatusCode)
	}

	var tt tempoTrace
	if err := json.NewDecoder(resp.Body).Decode(&tt); err != nil {
		return nil, err
	}

	var out []RemoteSpan
	for _, batch := range tt.Batches {
		for _, scope := range batch.ScopeSpans {
			for _, s := range scope.Spans {
				startNs := parseNano(s.StartTimeUnixNano)
				endNs := parseNano(s.EndTimeUnixNano)
				rs := RemoteSpan{
					TraceID:   traceID,
					SpanID:    s.SpanID,
					ParentID:  s.ParentSpanID,
					Name:      s.Name,
					StartTime: time.Unix(0, startNs),
					Duration:  time.Duration(endNs - startNs),
				}
				if s.Status.Code == 2 { // ERROR
					rs.Error = true
					rs.ErrorMsg = s.Status.Message
				}
				out = append(out, rs)
			}
		}
	}
	return out, nil
}

func parseNano(s string) int64 {
	var n int64
	fmt.Sscan(s, &n) //nolint:errcheck
	return n
}
