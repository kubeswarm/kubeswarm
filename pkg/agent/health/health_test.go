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

package health_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/kubeswarm/kubeswarm/pkg/agent/config"
	"github.com/kubeswarm/kubeswarm/pkg/agent/health"
	"github.com/kubeswarm/kubeswarm/pkg/agent/mcp"
	"github.com/kubeswarm/kubeswarm/pkg/agent/providers"
	"github.com/kubeswarm/kubeswarm/pkg/agent/queue"
	"github.com/kubeswarm/kubeswarm/pkg/agent/runner"
)

// healthMockProvider returns a configurable result from RunTask.
type healthMockProvider struct {
	result string
	err    error
}

func (p *healthMockProvider) RunTask(
	_ context.Context,
	_ *config.Config,
	_ queue.Task,
	_ []mcp.Tool,
	_ func(context.Context, string, json.RawMessage) (string, error),
	_ func(string),
) (string, queue.TokenUsage, error) {
	return p.result, queue.TokenUsage{}, p.err
}

func (p *healthMockProvider) Embed(_ context.Context, _ string) ([]float32, error) {
	return nil, providers.ErrEmbeddingNotSupported
}

func newTestRunner(t *testing.T, result string, err error) *runner.Runner {
	t.Helper()
	mgr, mgrErr := mcp.NewManager(nil)
	if mgrErr != nil {
		t.Fatalf("mcp.NewManager: %v", mgrErr)
	}
	cfg := &config.Config{Model: "mock", SystemPrompt: "test"}
	return runner.New(cfg, mgr, &healthMockProvider{result: result, err: err}, nil, nil, nil)
}

// startProbe starts ServeProbe on a random port and returns the base URL.
func startProbe(t *testing.T, r *runner.Runner, validatorPrompt string) string {
	t.Helper()
	// Find a free port.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()
	ln.Close()

	go health.ServeProbe(addr, r, validatorPrompt)

	// Wait for the server to be ready.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 50*time.Millisecond)
		if err == nil {
			conn.Close()
			return "http://" + addr
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("probe server did not start within 2s on %s", addr)
	return ""
}

func getStatus(t *testing.T, url string) (int, string) {
	t.Helper()
	resp, err := http.Get(url) //nolint:gosec
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body)
}

// TestPingProbe_Healthz covers TEST-TRACKER.md item:
//
//	[x] Observability: ping health check probe
//
// Behaviour under test: the /healthz liveness endpoint returns 200 if the
// agent process is running. The /readyz endpoint returns 200 immediately
// when no validator prompt is set (ping mode).
func TestPingProbe_Healthz(t *testing.T) {
	r := newTestRunner(t, "", nil)
	base := startProbe(t, r, "") // empty prompt = ping mode

	// /healthz should always return 200.
	code, body := getStatus(t, base+"/healthz")
	if code != http.StatusOK {
		t.Errorf("/healthz status = %d, want 200", code)
	}
	if body != "ok" {
		t.Errorf("/healthz body = %q, want %q", body, "ok")
	}

	// /readyz in ping mode should return 200 immediately.
	code, body = getStatus(t, base+"/readyz")
	if code != http.StatusOK {
		t.Errorf("/readyz ping status = %d, want 200", code)
	}
	if body != "ready" {
		t.Errorf("/readyz ping body = %q, want %q", body, "ready")
	}
}

// TestSemanticProbe_Healthy covers TEST-TRACKER.md item:
//
//	[x] Observability: semantic health check probe
//
// Behaviour under test: when a validator prompt is set, the /readyz endpoint
// calls RunTask with that prompt and returns 200 only if the LLM response
// contains "HEALTHY".
func TestSemanticProbe_Healthy(t *testing.T) {
	r := newTestRunner(t, "System is HEALTHY and operational", nil)
	base := startProbe(t, r, "Check if you are alive. Respond with HEALTHY.")

	code, body := getStatus(t, base+"/readyz")
	if code != http.StatusOK {
		t.Errorf("/readyz semantic status = %d, want 200; body: %s", code, body)
	}
	if body != "ready" {
		t.Errorf("/readyz body = %q, want %q", body, "ready")
	}
}

// TestSemanticProbe_Unhealthy verifies that /readyz returns 503 when the LLM
// response does not contain "HEALTHY".
func TestSemanticProbe_Unhealthy(t *testing.T) {
	r := newTestRunner(t, "I am confused and cannot respond properly", nil)
	base := startProbe(t, r, "Check if you are alive.")

	code, body := getStatus(t, base+"/readyz")
	if code != http.StatusServiceUnavailable {
		t.Errorf("/readyz status = %d, want 503", code)
	}
	if body == "ready" {
		t.Error("/readyz returned 'ready' for unhealthy response")
	}
}

// TestSemanticProbe_Error verifies that /readyz returns 503 when RunTask fails.
func TestSemanticProbe_Error(t *testing.T) {
	r := newTestRunner(t, "", fmt.Errorf("LLM provider timeout"))
	base := startProbe(t, r, "Check health.")

	code, body := getStatus(t, base+"/readyz")
	if code != http.StatusServiceUnavailable {
		t.Errorf("/readyz status = %d, want 503", code)
	}
	if body == "ready" {
		t.Error("/readyz returned 'ready' when RunTask errored")
	}
}
