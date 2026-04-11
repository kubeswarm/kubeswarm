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
	"sync"

	"github.com/kubeswarm/kubeswarm/pkg/agent/config"
	"github.com/kubeswarm/kubeswarm/pkg/agent/mcp"
	"github.com/kubeswarm/kubeswarm/pkg/agent/providers"
	"github.com/kubeswarm/kubeswarm/pkg/agent/queue"
)

// ---------------------------------------------------------------------------
// toolCall and toolCallingProvider
// ---------------------------------------------------------------------------

// toolCall represents a single tool invocation the provider will make.
type toolCall struct {
	Name  string          // tool name to call (e.g. "spawn_and_collect", "submit_subtask", "collect_results")
	Input json.RawMessage // JSON input to pass to the tool
}

// toolCallingProvider simulates an LLM that makes tool calls during RunTask.
// It executes a pre-configured sequence of tool calls via the callTool callback,
// collects results, and synthesizes a final output - mimicking a real agentic loop.
type toolCallingProvider struct {
	// calls is the sequence of tool calls to make via the callTool callback.
	calls []toolCall
	// synthesize, if set, is called with tool results to produce the final output.
	// If nil, the final output is all tool results joined by newline.
	synthesize func(results []string) string
	// usage is the token usage to report.
	usage queue.TokenUsage
}

// RunTask implements providers.LLMProvider by iterating over the pre-configured
// tool calls, invoking each via callTool, collecting results, and returning a
// final synthesized output with the configured token usage.
func (p *toolCallingProvider) RunTask(
	ctx context.Context,
	_ *config.Config,
	_ queue.Task,
	_ []mcp.Tool,
	callTool func(context.Context, string, json.RawMessage) (string, error),
	_ func(string),
) (string, queue.TokenUsage, error) {
	var results []string
	for _, tc := range p.calls {
		result, err := callTool(ctx, tc.Name, tc.Input)
		if err != nil {
			return "", queue.TokenUsage{}, err
		}
		results = append(results, result)
	}
	var finalResult string
	if p.synthesize != nil {
		finalResult = p.synthesize(results)
	} else {
		finalResult = strings.Join(results, "\n")
	}
	return finalResult, p.usage, nil
}

// Embed implements providers.LLMProvider. toolCallingProvider does not support
// embeddings - callers should not expect embedding functionality from this mock.
func (p *toolCallingProvider) Embed(_ context.Context, _ string) ([]float32, error) {
	return nil, providers.ErrEmbeddingNotSupported
}

// ---------------------------------------------------------------------------
// ackRecord and ackTrackingQueue
// ---------------------------------------------------------------------------

// ackRecord captures a single Ack call for later assertion.
type ackRecord struct {
	Task      queue.Task
	Result    string
	Usage     queue.TokenUsage
	Artifacts map[string]string
}

// ackTrackingQueue wraps a fanoutQueue and records all Ack calls.
// Use this in tests that need to verify token usage is recorded per subtask.
type ackTrackingQueue struct {
	*fanoutQueue
	mu     sync.Mutex
	ackLog []ackRecord
}

// newAckTrackingQueue creates an ackTrackingQueue whose inner fanoutQueue
// returns output for every submitted task automatically.
func newAckTrackingQueue(output string) *ackTrackingQueue {
	return &ackTrackingQueue{fanoutQueue: newFanoutQueueWithAutoResult(output)}
}

// Ack records the call parameters and then returns nil (the inner fanoutQueue
// Ack is a no-op so no delegation is needed).
func (q *ackTrackingQueue) Ack(task queue.Task, result string, usage queue.TokenUsage, artifacts map[string]string) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.ackLog = append(q.ackLog, ackRecord{
		Task:      task,
		Result:    result,
		Usage:     usage,
		Artifacts: artifacts,
	})
	return nil
}

// ackedRecords returns a copy of all recorded Ack calls.
func (q *ackTrackingQueue) ackedRecords() []ackRecord {
	q.mu.Lock()
	defer q.mu.Unlock()
	out := make([]ackRecord, len(q.ackLog))
	copy(out, q.ackLog)
	return out
}
