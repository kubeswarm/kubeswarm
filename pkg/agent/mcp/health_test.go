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

package mcp_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/kubeswarm/kubeswarm/pkg/agent/config"
	"github.com/kubeswarm/kubeswarm/pkg/agent/mcp"
)

const testServerURL = "http://server1:8080"

// ---------------------------------------------------------------------------
// Part 1: HealthTracker unit tests
// ---------------------------------------------------------------------------

// TestHealthTracker_NewIsEmpty - new tracker has no health data.
func TestHealthTracker_NewIsEmpty(t *testing.T) {
	ht := mcp.NewHealthTracker(20)
	all := ht.AllHealth()
	if len(all) != 0 {
		t.Errorf("AllHealth() len = %d, want 0", len(all))
	}
	h := ht.Health("http://nonexistent:8080")
	if h.Successes != 0 || h.Failures != 0 {
		t.Errorf("Health for unknown server: Successes=%d Failures=%d, want 0/0", h.Successes, h.Failures)
	}
}

// TestRecordSuccess_TracksCount - 5 successes -> Health() returns Successes=5, Failures=0, Up=true.
func TestRecordSuccess_TracksCount(t *testing.T) {
	ht := mcp.NewHealthTracker(20)
	url := testServerURL
	for range 5 {
		ht.RecordSuccess(url, 10*time.Millisecond)
	}
	h := ht.Health(url)
	if h.Successes != 5 {
		t.Errorf("Successes = %d, want 5", h.Successes)
	}
	if h.Failures != 0 {
		t.Errorf("Failures = %d, want 0", h.Failures)
	}
	if !h.Up {
		t.Error("Up = false, want true")
	}
}

// TestRecordFailure_TracksCount - 3 failures -> Failures=3, LastError is set.
func TestRecordFailure_TracksCount(t *testing.T) {
	ht := mcp.NewHealthTracker(20)
	url := testServerURL
	testErr := errors.New("connection refused")
	for range 3 {
		ht.RecordFailure(url, 5*time.Millisecond, testErr)
	}
	h := ht.Health(url)
	if h.Failures != 3 {
		t.Errorf("Failures = %d, want 3", h.Failures)
	}
	if h.LastError == nil {
		t.Error("LastError = nil, want non-nil")
	}
}

// TestRecordSuccess_TracksLatency - record 3 calls with 10ms, 20ms, 30ms -> AvgLatencyMs=20.
func TestRecordSuccess_TracksLatency(t *testing.T) {
	ht := mcp.NewHealthTracker(20)
	url := testServerURL
	ht.RecordSuccess(url, 10*time.Millisecond)
	ht.RecordSuccess(url, 20*time.Millisecond)
	ht.RecordSuccess(url, 30*time.Millisecond)
	h := ht.Health(url)
	if h.AvgLatencyMs != 20 {
		t.Errorf("AvgLatencyMs = %d, want 20", h.AvgLatencyMs)
	}
}

// TestHealth_RollingWindow - window=5, record 10 successes -> only last 5 counted.
func TestHealth_RollingWindow(t *testing.T) {
	ht := mcp.NewHealthTracker(5)
	url := testServerURL
	for range 10 {
		ht.RecordSuccess(url, 10*time.Millisecond)
	}
	h := ht.Health(url)
	if h.Successes != 5 {
		t.Errorf("Successes = %d, want 5 (rolling window)", h.Successes)
	}
}

// TestHealth_UpFalseWhenMostlyFailing - 1 success + 4 failures (80% failure rate) -> Up=false.
func TestHealth_UpFalseWhenMostlyFailing(t *testing.T) {
	ht := mcp.NewHealthTracker(5)
	url := testServerURL
	ht.RecordSuccess(url, 10*time.Millisecond)
	for range 4 {
		ht.RecordFailure(url, 5*time.Millisecond, errors.New("fail"))
	}
	h := ht.Health(url)
	if h.Up {
		t.Error("Up = true, want false (80% failure rate)")
	}
}

// TestHealth_UpTrueWhenMostlySucceeding - 4 successes + 1 failure (20% failure rate) -> Up=true.
func TestHealth_UpTrueWhenMostlySucceeding(t *testing.T) {
	ht := mcp.NewHealthTracker(5)
	url := testServerURL
	for range 4 {
		ht.RecordSuccess(url, 10*time.Millisecond)
	}
	ht.RecordFailure(url, 5*time.Millisecond, errors.New("transient"))
	h := ht.Health(url)
	if !h.Up {
		t.Error("Up = false, want true (20% failure rate)")
	}
}

// TestOverallStatus_AllHealthy - all servers Up -> "AllHealthy".
func TestOverallStatus_AllHealthy(t *testing.T) {
	ht := mcp.NewHealthTracker(20)
	ht.RecordSuccess("http://s1:8080", 10*time.Millisecond)
	ht.RecordSuccess("http://s2:8080", 10*time.Millisecond)

	status := ht.OverallStatus()
	if status != "AllHealthy" {
		t.Errorf("OverallStatus = %q, want %q", status, "AllHealthy")
	}
}

// TestOverallStatus_Degraded - some servers have elevated errors but still Up -> "Degraded".
func TestOverallStatus_Degraded(t *testing.T) {
	ht := mcp.NewHealthTracker(10)

	// s1: fully healthy
	for range 10 {
		ht.RecordSuccess("http://s1:8080", 10*time.Millisecond)
	}

	// s2: 30% failure rate (3 failures, 7 successes) - still Up but degraded
	for range 7 {
		ht.RecordSuccess("http://s2:8080", 10*time.Millisecond)
	}
	for range 3 {
		ht.RecordFailure("http://s2:8080", 5*time.Millisecond, errors.New("timeout"))
	}

	status := ht.OverallStatus()
	if status != "Degraded" {
		t.Errorf("OverallStatus = %q, want %q", status, "Degraded")
	}
}

// TestOverallStatus_Unreachable - at least one server has Up=false -> "Unreachable".
func TestOverallStatus_Unreachable(t *testing.T) {
	ht := mcp.NewHealthTracker(5)

	// s1: healthy
	for range 5 {
		ht.RecordSuccess("http://s1:8080", 10*time.Millisecond)
	}

	// s2: 100% failure -> Up=false
	for range 5 {
		ht.RecordFailure("http://s2:8080", 5*time.Millisecond, errors.New("unreachable"))
	}

	status := ht.OverallStatus()
	if status != "Unreachable" {
		t.Errorf("OverallStatus = %q, want %q", status, "Unreachable")
	}
}

// TestHealthTracker_ThreadSafe - concurrent RecordSuccess/RecordFailure/Health calls with -race.
func TestHealthTracker_ThreadSafe(t *testing.T) {
	ht := mcp.NewHealthTracker(20)
	url := testServerURL

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	var wg sync.WaitGroup

	// Writer: RecordSuccess
	wg.Go(func() {
		for {
			select {
			case <-ctx.Done():
				return
			default:
				ht.RecordSuccess(url, 10*time.Millisecond)
			}
		}
	})

	// Writer: RecordFailure
	wg.Go(func() {
		for {
			select {
			case <-ctx.Done():
				return
			default:
				ht.RecordFailure(url, 5*time.Millisecond, errors.New("fail"))
			}
		}
	})

	// Reader: Health
	wg.Go(func() {
		for {
			select {
			case <-ctx.Done():
				return
			default:
				_ = ht.Health(url)
			}
		}
	})

	// Reader: AllHealth
	wg.Go(func() {
		for {
			select {
			case <-ctx.Done():
				return
			default:
				_ = ht.AllHealth()
			}
		}
	})

	// Reader: OverallStatus
	wg.Go(func() {
		for {
			select {
			case <-ctx.Done():
				return
			default:
				_ = ht.OverallStatus()
			}
		}
	})

	wg.Wait()
	// If -race detects issues, the test binary will exit non-zero.
}

// ---------------------------------------------------------------------------
// Part 4: Integration with CallTool
// ---------------------------------------------------------------------------

// mcpHealthHandler returns a handler that serves tools/list and tools/call,
// with configurable tools/call behavior.
func mcpHealthHandler(callHandler http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/tools/list": //nolint:goconst
			_ = json.NewEncoder(w).Encode(map[string]any{
				"tools": []map[string]any{
					{
						"name":        "ping",
						"description": "ping tool",
						"inputSchema": json.RawMessage(`{"type":"object"}`),
					},
				},
			})
		case "/tools/call": //nolint:goconst
			callHandler(w, r)
		default:
			http.NotFound(w, r)
		}
	})
}

// TestCallTool_RecordsHealthOnSuccess - after a successful CallTool, the health
// tracker shows 1 success for that server.
func TestCallTool_RecordsHealthOnSuccess(t *testing.T) {
	srv := httptest.NewServer(mcpHealthHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"content": []map[string]any{
				{"type": "text", "text": "pong"},
			},
		})
	})))
	defer srv.Close()

	serverCfg := config.MCPServerConfig{Name: "hsrv", URL: srv.URL}
	m, err := mcp.NewManager([]config.MCPServerConfig{serverCfg})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	_, err = m.CallTool(context.Background(), "hsrv__ping", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}

	ht := m.HealthTracker()
	if ht == nil {
		t.Fatal("HealthTracker() = nil, want non-nil")
	}

	h := ht.Health(srv.URL)
	if h.Successes != 1 {
		t.Errorf("Successes = %d, want 1", h.Successes)
	}
	if h.Failures != 0 {
		t.Errorf("Failures = %d, want 0", h.Failures)
	}
}

// TestCallTool_RecordsHealthOnFailure - after a failed CallTool (server returns 500),
// health tracker shows 1 failure.
func TestCallTool_RecordsHealthOnFailure(t *testing.T) {
	srv := httptest.NewServer(mcpHealthHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})))
	defer srv.Close()

	serverCfg := config.MCPServerConfig{Name: "hsrv", URL: srv.URL}
	m, err := mcp.NewManager([]config.MCPServerConfig{serverCfg})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	_, err = m.CallTool(context.Background(), "hsrv__ping", json.RawMessage(`{}`))
	// The call may or may not return an error depending on implementation,
	// but the health tracker should record a failure either way.
	_ = err

	ht := m.HealthTracker()
	if ht == nil {
		t.Fatal("HealthTracker() = nil, want non-nil")
	}

	h := ht.Health(srv.URL)
	if h.Failures != 1 {
		t.Errorf("Failures = %d, want 1", h.Failures)
	}
}
