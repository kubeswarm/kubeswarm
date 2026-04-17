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
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/kubeswarm/kubeswarm/pkg/agent/config"
	"github.com/kubeswarm/kubeswarm/pkg/agent/mcp"
	"github.com/kubeswarm/kubeswarm/pkg/agent/providers"
	"github.com/kubeswarm/kubeswarm/pkg/agent/queue"
	"github.com/kubeswarm/kubeswarm/pkg/agent/runner"
)

const dedupSkippedMsg = "[skipped: duplicate tool call]"

// mcpToolServer returns an httptest.Server that serves a single tool "echo"
// via the MCP HTTP protocol. onCall is invoked on each /tools/call request.
func mcpToolServer(t *testing.T, onCall func()) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/tools/list":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"tools": []map[string]any{{
					"name":        "echo",
					"description": "echo tool",
					"inputSchema": json.RawMessage(`{"type":"object"}`),
				}},
			})
		case "/tools/call":
			if onCall != nil {
				onCall()
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"content": []map[string]any{
					{"type": "text", "text": "ok"},
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// dedupProvider is a mock LLM provider that calls the same tool twice with
// identical arguments during a single RunTask invocation. It records the
// results returned by each call so the test can verify dedup behavior.
type dedupProvider struct {
	toolName string
	input    json.RawMessage
	results  [2]string
	calls    atomic.Int64
}

func (p *dedupProvider) RunTask(
	ctx context.Context,
	_ *config.Config,
	_ queue.Task,
	_ []mcp.Tool,
	callTool func(context.Context, string, json.RawMessage) (string, error),
	_ func(string),
) (string, queue.TokenUsage, error) {
	// First call - should execute.
	r1, err := callTool(ctx, p.toolName, p.input)
	if err != nil {
		return "", queue.TokenUsage{}, err
	}
	p.results[0] = r1
	p.calls.Add(1)

	// Second call - identical, should be deduped.
	r2, err := callTool(ctx, p.toolName, p.input)
	if err != nil {
		return "", queue.TokenUsage{}, err
	}
	p.results[1] = r2
	p.calls.Add(1)

	return advisorDone, queue.TokenUsage{}, nil
}

func (p *dedupProvider) Embed(_ context.Context, _ string) ([]float32, error) {
	return nil, providers.ErrEmbeddingNotSupported
}

// TestRunner_Dedup_IdenticalToolCall covers TEST-TRACKER.md item:
//
//	[x] Tool: dedup identical tool call within task returns cached result
//
// Behaviour under test: when LoopPolicy.Dedup is enabled and the LLM provider
// invokes the same tool with the same arguments twice in one task, the second
// call returns dedupSkippedMsg without hitting the MCP server.
func TestRunner_Dedup_IdenticalToolCall(t *testing.T) {
	// Set up a mock MCP server that counts /tools/call invocations.
	var mcpCalls atomic.Int64
	srv := mcpToolServer(t, func() {
		mcpCalls.Add(1)
	})

	serverCfg := config.MCPServerConfig{Name: "srv", URL: srv.URL}
	mgr, err := mcp.NewManager([]config.MCPServerConfig{serverCfg})
	if err != nil {
		t.Fatalf("mcp.NewManager: %v", err)
	}

	prov := &dedupProvider{
		toolName: "srv__echo",
		input:    json.RawMessage(`{"msg": "hello"}`),
	}

	cfg := &config.Config{
		Model:        "mock",
		SystemPrompt: "test",
		LoopPolicy: &config.LoopPolicyConfig{
			Dedup: true,
		},
	}

	r := runner.New(cfg, mgr, prov, nil, nil, nil)
	_, _, err = r.RunTask(context.Background(), queue.Task{ID: "dedup-1", Prompt: "go"})
	if err != nil {
		t.Fatalf("RunTask error: %v", err)
	}

	// First call should have returned the real MCP result.
	if prov.results[0] == dedupSkippedMsg {
		t.Error("first call was incorrectly deduped")
	}

	// Second call should have been deduped.
	if prov.results[1] != dedupSkippedMsg {
		t.Errorf("second call result = %q, want %q", prov.results[1], dedupSkippedMsg)
	}

	// MCP server should have been called exactly once.
	if got := mcpCalls.Load(); got != 1 {
		t.Errorf("MCP /tools/call invocations = %d, want 1", got)
	}
}

// TestRunner_Dedup_DifferentInputNotDeduped verifies that tool calls with the
// same tool name but different arguments are NOT deduped.
func TestRunner_Dedup_DifferentInputNotDeduped(t *testing.T) {
	var mcpCalls atomic.Int64
	srv := mcpToolServer(t, func() {
		mcpCalls.Add(1)
	})

	serverCfg := config.MCPServerConfig{Name: "srv", URL: srv.URL}
	mgr, err := mcp.NewManager([]config.MCPServerConfig{serverCfg})
	if err != nil {
		t.Fatalf("mcp.NewManager: %v", err)
	}

	// Provider that calls the same tool with different args.
	prov := &twoCallProvider{
		toolName: "srv__echo",
		input1:   json.RawMessage(`{"msg": "hello"}`),
		input2:   json.RawMessage(`{"msg": "world"}`),
	}

	cfg := &config.Config{
		Model:        "mock",
		SystemPrompt: "test",
		LoopPolicy: &config.LoopPolicyConfig{
			Dedup: true,
		},
	}

	r := runner.New(cfg, mgr, prov, nil, nil, nil)
	_, _, err = r.RunTask(context.Background(), queue.Task{ID: "dedup-2", Prompt: "go"})
	if err != nil {
		t.Fatalf("RunTask error: %v", err)
	}

	// Both calls should execute - different inputs.
	if prov.results[0] == dedupSkippedMsg {
		t.Error("first call was incorrectly deduped")
	}
	if prov.results[1] == dedupSkippedMsg {
		t.Error("second call was incorrectly deduped despite different input")
	}

	// MCP server should have been called twice.
	if got := mcpCalls.Load(); got != 2 {
		t.Errorf("MCP /tools/call invocations = %d, want 2", got)
	}
}

// TestRunner_Dedup_DisabledAllowsDuplicates verifies that when LoopPolicy.Dedup
// is false, identical tool calls are executed normally.
func TestRunner_Dedup_DisabledAllowsDuplicates(t *testing.T) {
	var mcpCalls atomic.Int64
	srv := mcpToolServer(t, func() {
		mcpCalls.Add(1)
	})

	serverCfg := config.MCPServerConfig{Name: "srv", URL: srv.URL}
	mgr, err := mcp.NewManager([]config.MCPServerConfig{serverCfg})
	if err != nil {
		t.Fatalf("mcp.NewManager: %v", err)
	}

	prov := &dedupProvider{
		toolName: "srv__echo",
		input:    json.RawMessage(`{"msg": "hello"}`),
	}

	cfg := &config.Config{
		Model:        "mock",
		SystemPrompt: "test",
		// No LoopPolicy - dedup disabled.
	}

	r := runner.New(cfg, mgr, prov, nil, nil, nil)
	_, _, err = r.RunTask(context.Background(), queue.Task{ID: "dedup-3", Prompt: "go"})
	if err != nil {
		t.Fatalf("RunTask error: %v", err)
	}

	// Both calls should execute when dedup is disabled.
	if prov.results[0] == dedupSkippedMsg {
		t.Error("first call was incorrectly deduped")
	}
	if prov.results[1] == dedupSkippedMsg {
		t.Error("second call was deduped even though dedup is disabled")
	}

	if got := mcpCalls.Load(); got != 2 {
		t.Errorf("MCP /tools/call invocations = %d, want 2 (dedup disabled)", got)
	}
}

// twoCallProvider calls the same tool twice with different inputs.
type twoCallProvider struct {
	toolName string
	input1   json.RawMessage
	input2   json.RawMessage
	results  [2]string
}

func (p *twoCallProvider) RunTask(
	ctx context.Context,
	_ *config.Config,
	_ queue.Task,
	_ []mcp.Tool,
	callTool func(context.Context, string, json.RawMessage) (string, error),
	_ func(string),
) (string, queue.TokenUsage, error) {
	r1, err := callTool(ctx, p.toolName, p.input1)
	if err != nil {
		return "", queue.TokenUsage{}, err
	}
	p.results[0] = r1

	r2, err := callTool(ctx, p.toolName, p.input2)
	if err != nil {
		return "", queue.TokenUsage{}, err
	}
	p.results[1] = r2

	return advisorDone, queue.TokenUsage{}, nil
}

func (p *twoCallProvider) Embed(_ context.Context, _ string) ([]float32, error) {
	return nil, providers.ErrEmbeddingNotSupported
}
