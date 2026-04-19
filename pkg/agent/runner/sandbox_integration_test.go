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
	"strings"
	"testing"

	"github.com/kubeswarm/kubeswarm/pkg/agent/config"
	"github.com/kubeswarm/kubeswarm/pkg/agent/mcp"
	"github.com/kubeswarm/kubeswarm/pkg/agent/providers"
	"github.com/kubeswarm/kubeswarm/pkg/agent/queue"
	"github.com/kubeswarm/kubeswarm/pkg/agent/runner"
)

// mcpToolServerWithResult returns an httptest.Server that serves a single tool
// "echo" via the MCP HTTP protocol. The tool always returns the given result text.
func mcpToolServerWithResult(t *testing.T, result string) *httptest.Server {
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
			_ = json.NewEncoder(w).Encode(map[string]any{
				"content": []map[string]any{
					{"type": "text", "text": result},
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// sandboxCallerProvider is a mock LLM provider that calls a tool once via
// callTool and records both the raw result and any metadata about it.
type sandboxCallerProvider struct {
	toolName     string
	input        json.RawMessage
	result       string // result returned by callTool
	recallTool   string // if non-empty, call sandbox_recall with this ID after first call
	recallResult string // result from sandbox_recall
}

func (p *sandboxCallerProvider) RunTask(
	ctx context.Context,
	_ *config.Config,
	_ queue.Task,
	_ []mcp.Tool,
	callTool func(context.Context, string, json.RawMessage) (string, error),
	_ func(string),
) (string, queue.TokenUsage, error) {
	result, err := callTool(ctx, p.toolName, p.input)
	if err != nil {
		return "", queue.TokenUsage{}, err
	}
	p.result = result

	// If instructed, call sandbox_recall to retrieve the full result.
	if p.recallTool != "" {
		recallInput, _ := json.Marshal(map[string]string{"id": p.recallTool})
		recallResult, err := callTool(ctx, "sandbox_recall", recallInput)
		if err != nil {
			return "", queue.TokenUsage{}, err
		}
		p.recallResult = recallResult
	}

	return advisorDone, queue.TokenUsage{}, nil
}

func (p *sandboxCallerProvider) Embed(_ context.Context, _ string) ([]float32, error) {
	return nil, providers.ErrEmbeddingNotSupported
}

// sandboxRecallProvider calls a tool (expecting sandboxing), then parses the
// sandbox ID from the digest and calls sandbox_recall to retrieve the full result.
type sandboxRecallProvider struct {
	toolName     string
	input        json.RawMessage
	firstResult  string // digest from first call
	recallResult string // full result from sandbox_recall
}

func (p *sandboxRecallProvider) RunTask(
	ctx context.Context,
	_ *config.Config,
	_ queue.Task,
	_ []mcp.Tool,
	callTool func(context.Context, string, json.RawMessage) (string, error),
	_ func(string),
) (string, queue.TokenUsage, error) {
	// First call - should be sandboxed.
	result, err := callTool(ctx, p.toolName, p.input)
	if err != nil {
		return "", queue.TokenUsage{}, err
	}
	p.firstResult = result

	// Extract sandbox ID from the digest. The digest should contain "[sandboxed:result-N]".
	id := extractSandboxID(result)
	if id == "" {
		// Not sandboxed - return as-is.
		return advisorDone, queue.TokenUsage{}, nil
	}

	// Call sandbox_recall with the extracted ID.
	recallInput, _ := json.Marshal(map[string]string{"id": id})
	recallResult, err := callTool(ctx, "sandbox_recall", recallInput)
	if err != nil {
		return "", queue.TokenUsage{}, err
	}
	p.recallResult = recallResult

	return advisorDone, queue.TokenUsage{}, nil
}

func (p *sandboxRecallProvider) Embed(_ context.Context, _ string) ([]float32, error) {
	return nil, providers.ErrEmbeddingNotSupported
}

// extractSandboxID finds a sandbox ID in a digest string.
// Looks for "[sandboxed:result-N]" pattern.
func extractSandboxID(digest string) string {
	const prefix = "[sandboxed:"
	_, after, ok := strings.Cut(digest, prefix)
	if !ok {
		return ""
	}
	rest := after
	before, _, ok := strings.Cut(rest, "]")
	if !ok {
		return ""
	}
	return before
}

// TestRunner_Sandbox_LargeResultSandboxed verifies that when sandbox is enabled
// and a tool returns a result larger than the threshold, the provider sees a
// digest containing "[sandboxed:" and "sandbox_recall" instead of the raw result.
func TestRunner_Sandbox_LargeResultSandboxed(t *testing.T) {
	largeResult := strings.Repeat("x", 3000) // 3KB, well over 2KB threshold
	srv := mcpToolServerWithResult(t, largeResult)

	serverCfg := config.MCPServerConfig{Name: "srv", URL: srv.URL}
	mgr, err := mcp.NewManager([]config.MCPServerConfig{serverCfg})
	if err != nil {
		t.Fatalf("mcp.NewManager: %v", err)
	}

	prov := &sandboxCallerProvider{
		toolName: "srv__echo",
		input:    json.RawMessage(`{}`),
	}

	cfg := &config.Config{
		Model:        "mock",
		SystemPrompt: "test",
		LoopPolicy: &config.LoopPolicyConfig{
			Sandbox: &config.LoopSandboxConfig{
				ThresholdBytes: 2048,
				PreviewBytes:   200,
				MaxTotalBytes:  1000000,
			},
		},
	}

	r := runner.New(cfg, mgr, prov, nil, nil, nil)
	_, _, err = r.RunTask(context.Background(), queue.Task{ID: "sandbox-1", Prompt: "go"})
	if err != nil {
		t.Fatalf("RunTask error: %v", err)
	}

	// Provider should have received a digest, not the raw result.
	if !strings.Contains(prov.result, "[sandboxed:") {
		t.Errorf("result missing [sandboxed: marker, got:\n%s", prov.result)
	}
	if !strings.Contains(prov.result, "sandbox_recall") {
		t.Errorf("result missing sandbox_recall instruction, got:\n%s", prov.result)
	}
	// The raw large result should NOT appear in full.
	if strings.Contains(prov.result, largeResult) {
		t.Error("result contains the full raw large result - sandbox did not replace it")
	}
}

// TestRunner_Sandbox_SmallResultPassesThrough verifies that when sandbox is
// enabled but a tool returns a result smaller than the threshold, the provider
// sees the raw result unchanged (no digest).
func TestRunner_Sandbox_SmallResultPassesThrough(t *testing.T) {
	smallResult := "hello world" // well under 2KB
	srv := mcpToolServerWithResult(t, smallResult)

	serverCfg := config.MCPServerConfig{Name: "srv", URL: srv.URL}
	mgr, err := mcp.NewManager([]config.MCPServerConfig{serverCfg})
	if err != nil {
		t.Fatalf("mcp.NewManager: %v", err)
	}

	prov := &sandboxCallerProvider{
		toolName: "srv__echo",
		input:    json.RawMessage(`{}`),
	}

	cfg := &config.Config{
		Model:        "mock",
		SystemPrompt: "test",
		LoopPolicy: &config.LoopPolicyConfig{
			Sandbox: &config.LoopSandboxConfig{
				ThresholdBytes: 2048,
				PreviewBytes:   200,
				MaxTotalBytes:  1000000,
			},
		},
	}

	r := runner.New(cfg, mgr, prov, nil, nil, nil)
	_, _, err = r.RunTask(context.Background(), queue.Task{ID: "sandbox-2", Prompt: "go"})
	if err != nil {
		t.Fatalf("RunTask error: %v", err)
	}

	// Provider should see the raw result, not a digest.
	if strings.Contains(prov.result, "[sandboxed:") {
		t.Errorf("small result was sandboxed unexpectedly, got:\n%s", prov.result)
	}
	if !strings.Contains(prov.result, smallResult) {
		t.Errorf("result does not contain raw small result %q, got:\n%s", smallResult, prov.result)
	}
}

// TestRunner_Sandbox_RecallReturnsFullResult verifies the full sandbox lifecycle:
// a tool returns a large result (sandboxed), then the provider calls sandbox_recall
// with the ID and receives the full original result.
func TestRunner_Sandbox_RecallReturnsFullResult(t *testing.T) {
	largeResult := strings.Repeat("important data: ", 200) // ~3200 bytes
	srv := mcpToolServerWithResult(t, largeResult)

	serverCfg := config.MCPServerConfig{Name: "srv", URL: srv.URL}
	mgr, err := mcp.NewManager([]config.MCPServerConfig{serverCfg})
	if err != nil {
		t.Fatalf("mcp.NewManager: %v", err)
	}

	prov := &sandboxRecallProvider{
		toolName: "srv__echo",
		input:    json.RawMessage(`{}`),
	}

	cfg := &config.Config{
		Model:        "mock",
		SystemPrompt: "test",
		LoopPolicy: &config.LoopPolicyConfig{
			Sandbox: &config.LoopSandboxConfig{
				ThresholdBytes: 2048,
				PreviewBytes:   200,
				MaxTotalBytes:  1000000,
			},
		},
	}

	r := runner.New(cfg, mgr, prov, nil, nil, nil)
	_, _, err = r.RunTask(context.Background(), queue.Task{ID: "sandbox-3", Prompt: "go"})
	if err != nil {
		t.Fatalf("RunTask error: %v", err)
	}

	// First call should have been sandboxed.
	if !strings.Contains(prov.firstResult, "[sandboxed:") {
		t.Fatalf("first result was not sandboxed, got:\n%s", prov.firstResult)
	}

	// sandbox_recall should return the full original result.
	if prov.recallResult != largeResult {
		t.Errorf("sandbox_recall returned %d bytes, want %d bytes",
			len(prov.recallResult), len(largeResult))
	}
}

// TestRunner_Sandbox_DisabledByDefault verifies that when no sandbox config is
// set on LoopPolicy, all results pass through unchanged - even large ones.
func TestRunner_Sandbox_DisabledByDefault(t *testing.T) {
	largeResult := strings.Repeat("x", 5000) // 5KB
	srv := mcpToolServerWithResult(t, largeResult)

	serverCfg := config.MCPServerConfig{Name: "srv", URL: srv.URL}
	mgr, err := mcp.NewManager([]config.MCPServerConfig{serverCfg})
	if err != nil {
		t.Fatalf("mcp.NewManager: %v", err)
	}

	prov := &sandboxCallerProvider{
		toolName: "srv__echo",
		input:    json.RawMessage(`{}`),
	}

	cfg := &config.Config{
		Model:        "mock",
		SystemPrompt: "test",
		LoopPolicy:   &config.LoopPolicyConfig{
			// Sandbox is nil - not configured.
		},
	}

	r := runner.New(cfg, mgr, prov, nil, nil, nil)
	_, _, err = r.RunTask(context.Background(), queue.Task{ID: "sandbox-4", Prompt: "go"})
	if err != nil {
		t.Fatalf("RunTask error: %v", err)
	}

	// Without sandbox config, even large results should pass through raw.
	if strings.Contains(prov.result, "[sandboxed:") {
		t.Errorf("result was sandboxed despite no sandbox config, got:\n%s", prov.result)
	}
	if !strings.Contains(prov.result, largeResult) {
		t.Errorf("result does not contain the full raw large result")
	}
}
