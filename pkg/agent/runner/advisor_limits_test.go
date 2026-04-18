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
	"strings"
	"testing"

	"github.com/kubeswarm/kubeswarm/pkg/agent/config"
	"github.com/kubeswarm/kubeswarm/pkg/agent/mcp"
	"github.com/kubeswarm/kubeswarm/pkg/agent/providers"
	"github.com/kubeswarm/kubeswarm/pkg/agent/queue"
	"github.com/kubeswarm/kubeswarm/pkg/agent/runner"
)

const advisorDone = "done"

// advisorCallerProvider is a mock LLM provider that calls an advisor tool
// N times via callTool and records the results.
type advisorCallerProvider struct {
	toolName string
	calls    int
	results  []string
}

func (p *advisorCallerProvider) RunTask(
	ctx context.Context,
	_ *config.Config,
	_ queue.Task,
	_ []mcp.Tool,
	callTool func(context.Context, string, json.RawMessage) (string, error),
	_ func(string),
) (string, queue.TokenUsage, error) {
	input := json.RawMessage(`{"question":"help me"}`)
	for range p.calls {
		r, err := callTool(ctx, p.toolName, input)
		if err != nil {
			return "", queue.TokenUsage{}, err
		}
		p.results = append(p.results, r)
	}
	return advisorDone, queue.TokenUsage{}, nil
}

func (p *advisorCallerProvider) Embed(_ context.Context, _ string) ([]float32, error) {
	return nil, providers.ErrEmbeddingNotSupported
}

// TestRunner_AdvisorCallLimit_Enforced covers TEST-TRACKER.md item:
//
//	[x] A2A: per-advisor call / token limits enforced
//
// Behaviour under test: when the LLM provider calls an advisor tool more times
// than MaxCallsPerTask allows, the excess calls return a JSON error containing
// "advisor_limit_exceeded" instead of dispatching to the gateway.
func TestRunner_AdvisorCallLimit_Enforced(t *testing.T) {
	prov := &advisorCallerProvider{
		toolName: "consult_reviewer",
		calls:    3, // try 3 calls, limit is 2
	}

	cfg := &config.Config{
		Model:        "mock",
		SystemPrompt: "test",
		Advisors: []config.AdvisorConfig{
			{
				Name:            "reviewer",
				ToolName:        "consult_reviewer",
				AgentRef:        "reviewer-agent",
				MaxCallsPerTask: 2,
				TimeoutSeconds:  5,
			},
		},
		// No TaskQueueURL - calls within budget will get "advisor_unavailable",
		// calls over budget will get "advisor_limit_exceeded".
	}

	r := runner.New(cfg, newMCPManager(t), prov, nil, nil, nil)
	_, _, err := r.RunTask(context.Background(), queue.Task{ID: "adv-1", Prompt: "go"})
	if err != nil {
		t.Fatalf("RunTask error: %v", err)
	}

	if len(prov.results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(prov.results))
	}

	// First two calls should get through (and fail at gateway since no queue).
	for i := range 2 {
		if strings.Contains(prov.results[i], "advisor_limit_exceeded") {
			t.Errorf("call %d was blocked by limit, expected it to pass through", i+1)
		}
		if !strings.Contains(prov.results[i], "advisor_unavailable") {
			t.Errorf("call %d: expected advisor_unavailable (no queue), got %q", i+1, prov.results[i])
		}
	}

	// Third call should be blocked by the call limit.
	if !strings.Contains(prov.results[2], "advisor_limit_exceeded") {
		t.Errorf("call 3: expected advisor_limit_exceeded, got %q", prov.results[2])
	}
	if !strings.Contains(prov.results[2], `"limit":2`) {
		t.Errorf("call 3: expected limit=2 in response, got %q", prov.results[2])
	}
}

// TestRunner_AdvisorCallLimit_ZeroIsUnlimited verifies that MaxCallsPerTask=0
// means no call limit - all calls pass through to the gateway.
func TestRunner_AdvisorCallLimit_ZeroIsUnlimited(t *testing.T) {
	prov := &advisorCallerProvider{
		toolName: "consult_helper",
		calls:    10,
	}

	cfg := &config.Config{
		Model:        "mock",
		SystemPrompt: "test",
		Advisors: []config.AdvisorConfig{
			{
				Name:            "helper",
				ToolName:        "consult_helper",
				AgentRef:        "helper-agent",
				MaxCallsPerTask: 0, // unlimited
				TimeoutSeconds:  5,
			},
		},
	}

	r := runner.New(cfg, newMCPManager(t), prov, nil, nil, nil)
	_, _, err := r.RunTask(context.Background(), queue.Task{ID: "adv-2", Prompt: "go"})
	if err != nil {
		t.Fatalf("RunTask error: %v", err)
	}

	// None of the 10 calls should be blocked by a limit.
	for i, result := range prov.results {
		if strings.Contains(result, "advisor_limit_exceeded") {
			t.Errorf("call %d was blocked by limit with maxCalls=0", i+1)
		}
	}
}
