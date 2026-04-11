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
	"errors"
	"testing"

	"github.com/kubeswarm/kubeswarm/pkg/agent/config"
	"github.com/kubeswarm/kubeswarm/pkg/agent/mcp"
	"github.com/kubeswarm/kubeswarm/pkg/agent/providers"
	"github.com/kubeswarm/kubeswarm/pkg/agent/queue"
	"github.com/kubeswarm/kubeswarm/pkg/agent/runner"
)

type mockProvider struct {
	result string
	err    error
}

func (m *mockProvider) RunTask(
	_ context.Context,
	_ *config.Config,
	_ queue.Task,
	_ []mcp.Tool,
	_ func(context.Context, string, json.RawMessage) (string, error),
	_ func(string),
) (string, queue.TokenUsage, error) {
	return m.result, queue.TokenUsage{}, m.err
}

func (m *mockProvider) Embed(_ context.Context, _ string) ([]float32, error) {
	return nil, providers.ErrEmbeddingNotSupported
}

func newMCPManager(t *testing.T) *mcp.Manager {
	t.Helper()
	mgr, err := mcp.NewManager(nil)
	if err != nil {
		t.Fatalf("mcp.NewManager: %v", err)
	}
	return mgr
}

func TestRunner_RunTask_Success(t *testing.T) {
	cfg := &config.Config{Model: "claude-sonnet-4-6", SystemPrompt: "test"}
	r := runner.New(cfg, newMCPManager(t), &mockProvider{result: "done"}, nil, nil, nil)
	result, _, err := r.RunTask(context.Background(), queue.Task{ID: "1", Prompt: "hello"})
	if err != nil {
		t.Fatalf("RunTask error: %v", err)
	}
	if result != "done" {
		t.Errorf("RunTask = %q, want %q", result, "done")
	}
}

func TestRunner_RunTask_ProviderError(t *testing.T) {
	cfg := &config.Config{Model: "claude-sonnet-4-6", SystemPrompt: "test"}
	r := runner.New(cfg, newMCPManager(t), &mockProvider{err: errors.New("api error")}, nil, nil, nil)
	_, _, err := r.RunTask(context.Background(), queue.Task{ID: "2", Prompt: "hello"})
	if err == nil {
		t.Fatal("expected error from provider, got nil")
	}
}
