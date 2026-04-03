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

package runner_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kubeswarm/kubeswarm/pkg/agent/config"
	"github.com/kubeswarm/kubeswarm/pkg/agent/mcp"
	"github.com/kubeswarm/kubeswarm/pkg/agent/queue"
	"github.com/kubeswarm/kubeswarm/pkg/agent/runner"
)

// stubQueue is a no-op TaskQueue implementation for tests.
type stubQueue struct{}

func (s *stubQueue) Submit(_ context.Context, _ string, _ map[string]string) (string, error) {
	return "stub-id", nil
}
func (s *stubQueue) Poll(_ context.Context) (*queue.Task, error) { return nil, nil }
func (s *stubQueue) Ack(_ queue.Task, _ string, _ queue.TokenUsage, _ map[string]string) error {
	return nil
}
func (s *stubQueue) Nack(_ queue.Task, _ string) error          { return nil }
func (s *stubQueue) Cancel(_ context.Context, _ []string) error { return nil }
func (s *stubQueue) Results(_ context.Context, _ []string) ([]queue.TaskResult, error) {
	return nil, nil
}
func (s *stubQueue) Close() {}

const submitSubtaskTool = "submit_subtask"

// TestRunner_WebhookTool_Dispatch verifies that the runner calls an inline webhook
// tool's URL when invoked and returns the response body.
func TestRunner_WebhookTool_Dispatch(t *testing.T) {
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"headlines":["AI advances","New research"]}`))
	}))
	defer srv.Close()

	cfg := &config.Config{
		Model:        "claude-sonnet-4-6",
		SystemPrompt: "test",
		WebhookTools: []config.WebhookToolConfig{
			{Name: "fetch_news", Description: "Get news", URL: srv.URL, Method: "POST"},
		},
	}
	mgr, _ := mcp.NewManager(nil)
	r := runner.New(cfg, mgr, &mockProvider{result: "ok"}, nil, nil, nil)

	found := false
	for _, tool := range r.AllTools() {
		if tool.Name == "fetch_news" {
			found = true
		}
	}
	if !found {
		t.Fatal("fetch_news not in AllTools")
	}

	input := json.RawMessage(`{"topic":"AI","limit":5}`)
	result, err := r.CallTool(context.Background(), "fetch_news", input)
	if err != nil {
		t.Fatalf("CallTool error: %v", err)
	}
	if !strings.Contains(result, "AI advances") {
		t.Errorf("unexpected result: %q", result)
	}
	if !strings.Contains(gotBody, "topic") {
		t.Errorf("server did not receive input body, got: %q", gotBody)
	}
}

// TestRunner_WebhookTool_ServerError verifies that a non-2xx response is surfaced as an error.
func TestRunner_WebhookTool_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "service unavailable", http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	cfg := &config.Config{
		Model:        "claude-sonnet-4-6",
		SystemPrompt: "test",
		WebhookTools: []config.WebhookToolConfig{
			{Name: "broken_tool", URL: srv.URL, Method: "POST"},
		},
	}
	mgr, _ := mcp.NewManager(nil)
	r := runner.New(cfg, mgr, &mockProvider{}, nil, nil, nil)

	_, err := r.CallTool(context.Background(), "broken_tool", json.RawMessage(`{}`))
	if err == nil {
		t.Fatal("expected error for non-2xx response, got nil")
	}
	if !strings.Contains(err.Error(), "503") {
		t.Errorf("expected 503 in error, got: %v", err)
	}
}

// TestRunner_SubmitSubtask_WithQueue verifies submit_subtask appears in AllTools when a queue is set.
func TestRunner_SubmitSubtask_WithQueue(t *testing.T) {
	q := &stubQueue{}
	cfg := &config.Config{Model: "claude-sonnet-4-6", SystemPrompt: "test"}
	mgr, _ := mcp.NewManager(nil)
	r := runner.New(cfg, mgr, &mockProvider{}, q, nil, nil)

	found := false
	for _, tool := range r.AllTools() {
		if tool.Name == submitSubtaskTool {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected %q in AllTools when queue is set, got: %v", submitSubtaskTool, r.AllTools())
	}
}

// TestRunner_SubmitSubtask_NotPresent_WithoutQueue verifies submit_subtask is absent when queue is nil.
func TestRunner_SubmitSubtask_NotPresent_WithoutQueue(t *testing.T) {
	cfg := &config.Config{Model: "claude-sonnet-4-6", SystemPrompt: "test"}
	mgr, _ := mcp.NewManager(nil)
	r := runner.New(cfg, mgr, &mockProvider{}, nil, nil, nil)

	for _, tool := range r.AllTools() {
		if tool.Name == submitSubtaskTool {
			t.Fatalf("expected %q to be absent when queue is nil", submitSubtaskTool)
		}
	}
}

// TestRunner_CallTool_UnknownTool verifies that calling an unknown tool returns an error.
func TestRunner_CallTool_UnknownTool(t *testing.T) {
	cfg := &config.Config{Model: "claude-sonnet-4-6", SystemPrompt: "test"}
	mgr, _ := mcp.NewManager(nil)
	r := runner.New(cfg, mgr, &mockProvider{}, nil, nil, nil)

	_, err := r.CallTool(context.Background(), "nonexistent_tool", json.RawMessage(`{}`))
	if err == nil {
		t.Fatal("expected error for unknown tool, got nil")
	}
}
