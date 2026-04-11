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
	"net/http"
	"net/http/httptest"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kubeswarm/kubeswarm/pkg/agent/config"
	"github.com/kubeswarm/kubeswarm/pkg/agent/mcp"
)

// mcpDynamicHandler returns an http.Handler whose tool list can be changed
// between requests via the returned setter function.
func mcpDynamicHandler() (http.Handler, func(tools []map[string]any)) {
	var mu sync.Mutex
	var tools []map[string]any

	set := func(t []map[string]any) {
		mu.Lock()
		defer mu.Unlock()
		tools = t
	}

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/tools/list": //nolint:goconst
			mu.Lock()
			t := tools
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{"tools": t})
		case "/tools/call": //nolint:goconst
			_ = json.NewEncoder(w).Encode(map[string]any{
				"content": []map[string]any{
					{"type": "text", "text": "ok"},
				},
			})
		default:
			http.NotFound(w, r)
		}
	})

	return handler, set
}

func toolEntry(name string) map[string]any {
	return map[string]any{
		"name":        name,
		"description": name + " tool",
		"inputSchema": json.RawMessage(`{"type":"object"}`),
	}
}

func toolNames(tools []mcp.Tool) []string {
	names := make([]string, len(tools))
	for i, t := range tools {
		names[i] = t.Name
	}
	sort.Strings(names)
	return names
}

// TestRefreshTools_AddsNewTools verifies that when a server adds a new tool,
// RefreshTools returns it in the added slice and the tool becomes visible via Tools().
func TestRefreshTools_AddsNewTools(t *testing.T) {
	handler, setTools := mcpDynamicHandler()
	setTools([]map[string]any{toolEntry("a"), toolEntry("b")})
	srv := httptest.NewServer(handler)
	defer srv.Close()

	serverCfg := config.MCPServerConfig{Name: "srv", URL: srv.URL}
	m, err := mcp.NewManager([]config.MCPServerConfig{serverCfg})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	if len(m.Tools()) != 2 {
		t.Fatalf("initial tools count = %d, want 2", len(m.Tools()))
	}

	// Add tool "c" on the server.
	setTools([]map[string]any{toolEntry("a"), toolEntry("b"), toolEntry("c")})

	added, removed, err := m.RefreshTools(serverCfg)
	if err != nil {
		t.Fatalf("RefreshTools: %v", err)
	}

	sort.Strings(added)
	if len(added) != 1 || added[0] != "srv__c" {
		t.Errorf("added = %v, want [srv__c]", added)
	}
	if len(removed) != 0 {
		t.Errorf("removed = %v, want []", removed)
	}

	names := toolNames(m.Tools())
	if len(names) != 3 {
		t.Errorf("Tools() count = %d, want 3", len(names))
	}
}

// TestRefreshTools_RemovesOldTools verifies that when a server removes a tool,
// RefreshTools returns it in the removed slice.
func TestRefreshTools_RemovesOldTools(t *testing.T) {
	handler, setTools := mcpDynamicHandler()
	setTools([]map[string]any{toolEntry("a"), toolEntry("b")})
	srv := httptest.NewServer(handler)
	defer srv.Close()

	serverCfg := config.MCPServerConfig{Name: "srv", URL: srv.URL}
	m, err := mcp.NewManager([]config.MCPServerConfig{serverCfg})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	// Remove tool "b".
	setTools([]map[string]any{toolEntry("a")})

	added, removed, err := m.RefreshTools(serverCfg)
	if err != nil {
		t.Fatalf("RefreshTools: %v", err)
	}

	if len(added) != 0 {
		t.Errorf("added = %v, want []", added)
	}
	sort.Strings(removed)
	if len(removed) != 1 || removed[0] != "srv__b" {
		t.Errorf("removed = %v, want [srv__b]", removed)
	}

	names := toolNames(m.Tools())
	if len(names) != 1 || names[0] != "srv__a" {
		t.Errorf("Tools() = %v, want [srv__a]", names)
	}
}

// TestRefreshTools_AtomicSwap ensures concurrent readers via Tools() never see
// a partial tool list while RefreshTools is running.
func TestRefreshTools_AtomicSwap(t *testing.T) {
	handler, setTools := mcpDynamicHandler()
	setTools([]map[string]any{toolEntry("a"), toolEntry("b")})
	srv := httptest.NewServer(handler)
	defer srv.Close()

	serverCfg := config.MCPServerConfig{Name: "srv", URL: srv.URL}
	m, err := mcp.NewManager([]config.MCPServerConfig{serverCfg})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	// Valid tool list lengths: 2 (before refresh) or 4 (after refresh).
	validLengths := map[int]bool{2: true, 4: true}
	var bad atomic.Int64
	var wg sync.WaitGroup
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	// Readers: continuously call Tools() and check consistency.
	for range 4 {
		wg.Go(func() {
			for {
				select {
				case <-ctx.Done():
					return
				default:
					tools := m.Tools()
					if !validLengths[len(tools)] {
						bad.Add(1)
					}
				}
			}
		})
	}

	// Writer: swap tools between two known sets.
	setA := []map[string]any{toolEntry("a"), toolEntry("b")}
	setB := []map[string]any{toolEntry("a"), toolEntry("b"), toolEntry("c"), toolEntry("d")}
	wg.Go(func() {
		for {
			select {
			case <-ctx.Done():
				return
			default:
				setTools(setB)
				_, _, _ = m.RefreshTools(serverCfg)
				setTools(setA)
				_, _, _ = m.RefreshTools(serverCfg)
			}
		}
	})

	wg.Wait()
	if v := bad.Load(); v > 0 {
		t.Errorf("observed %d partial tool lists (non-atomic swap)", v)
	}
}

// TestRefreshTools_NoChange verifies that when the server returns the same tools,
// both added and removed are empty.
func TestRefreshTools_NoChange(t *testing.T) {
	handler, setTools := mcpDynamicHandler()
	setTools([]map[string]any{toolEntry("a"), toolEntry("b")})
	srv := httptest.NewServer(handler)
	defer srv.Close()

	serverCfg := config.MCPServerConfig{Name: "srv", URL: srv.URL}
	m, err := mcp.NewManager([]config.MCPServerConfig{serverCfg})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	added, removed, err := m.RefreshTools(serverCfg)
	if err != nil {
		t.Fatalf("RefreshTools: %v", err)
	}

	if len(added) != 0 {
		t.Errorf("added = %v, want []", added)
	}
	if len(removed) != 0 {
		t.Errorf("removed = %v, want []", removed)
	}
}

// TestTools_ThreadSafe exercises concurrent Tools() and CallTool() calls
// alongside RefreshTools() to verify there are no data races.
// Run with: go test -race ./pkg/agent/mcp/...
func TestTools_ThreadSafe(t *testing.T) {
	handler, setTools := mcpDynamicHandler()
	setTools([]map[string]any{toolEntry("echo")})
	srv := httptest.NewServer(handler)
	defer srv.Close()

	serverCfg := config.MCPServerConfig{Name: "srv", URL: srv.URL}
	m, err := mcp.NewManager([]config.MCPServerConfig{serverCfg})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	var wg sync.WaitGroup

	// Reader goroutine calling Tools().
	wg.Go(func() {
		for {
			select {
			case <-ctx.Done():
				return
			default:
				_ = m.Tools()
			}
		}
	})

	// Reader goroutine calling CallTool().
	wg.Go(func() {
		for {
			select {
			case <-ctx.Done():
				return
			default:
				_, _ = m.CallTool(ctx, "srv__echo", json.RawMessage(`{}`))
			}
		}
	})

	// Writer goroutine calling RefreshTools().
	wg.Go(func() {
		sets := [][]map[string]any{
			{toolEntry("echo")},
			{toolEntry("echo"), toolEntry("search")},
		}
		i := 0
		for {
			select {
			case <-ctx.Done():
				return
			default:
				setTools(sets[i%2])
				_, _, _ = m.RefreshTools(serverCfg)
				i++
			}
		}
	})

	wg.Wait()
	// If -race detects issues, the test binary will exit non-zero.
}

// TestStartPolling_RefreshesOnInterval verifies that StartPolling periodically
// calls RefreshTools and picks up tool changes from the server.
func TestStartPolling_RefreshesOnInterval(t *testing.T) {
	handler, setTools := mcpDynamicHandler()
	setTools([]map[string]any{toolEntry("a")})
	srv := httptest.NewServer(handler)
	defer srv.Close()

	serverCfg := config.MCPServerConfig{Name: "srv", URL: srv.URL}
	m, err := mcp.NewManager([]config.MCPServerConfig{serverCfg})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	if len(m.Tools()) != 1 {
		t.Fatalf("initial tools = %d, want 1", len(m.Tools()))
	}

	ctx := t.Context()

	// Start polling with a very short interval.
	m.StartPolling(ctx, []config.MCPServerConfig{serverCfg})
	defer m.Stop()

	// Change the server's tool list.
	setTools([]map[string]any{toolEntry("a"), toolEntry("b")})

	// Wait for the poller to pick up the change (with generous timeout).
	deadline := time.After(2 * time.Second)
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-deadline:
			t.Fatalf("Tools() did not reflect server change within timeout; got %d tools, want 2", len(m.Tools()))
		case <-ticker.C:
			if len(m.Tools()) == 2 {
				return // success
			}
		}
	}
}

// TestStop_CancelsPolling verifies that after calling Stop(), no more refresh
// cycles occur even when the server's tool list changes.
func TestStop_CancelsPolling(t *testing.T) {
	var listCalls atomic.Int64
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/tools/list" { //nolint:goconst
			listCalls.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"tools": []map[string]any{toolEntry("a")},
			})
			return
		}
		http.NotFound(w, r)
	})
	srv := httptest.NewServer(handler)
	defer srv.Close()

	serverCfg := config.MCPServerConfig{Name: "srv", URL: srv.URL}
	m, err := mcp.NewManager([]config.MCPServerConfig{serverCfg})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	ctx := t.Context()

	m.StartPolling(ctx, []config.MCPServerConfig{serverCfg})

	// Let a few poll cycles run.
	time.Sleep(100 * time.Millisecond)

	m.Stop()

	// Record the call count right after stop.
	countAfterStop := listCalls.Load()

	// Wait and verify no more calls arrive.
	time.Sleep(200 * time.Millisecond)
	countLater := listCalls.Load()

	if countLater > countAfterStop+1 {
		// Allow at most 1 extra call that may have been in-flight when Stop was called.
		t.Errorf("polling continued after Stop(): calls after stop = %d, calls later = %d", countAfterStop, countLater)
	}
}
