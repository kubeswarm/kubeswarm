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
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/kubeswarm/kubeswarm/pkg/agent/config"
	"github.com/kubeswarm/kubeswarm/pkg/agent/mcp"
	"github.com/kubeswarm/kubeswarm/pkg/agent/providers"
	"github.com/kubeswarm/kubeswarm/pkg/agent/queue"
	"github.com/kubeswarm/kubeswarm/pkg/agent/runner"
)

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func newRunnerWithProvider(p providers.LLMProvider, tq queue.TaskQueue, delegateQueues map[string]queue.TaskQueue) *runner.Runner {
	t := &testing.T{}
	t.Helper()
	cfg := &config.Config{Model: "claude-sonnet-4-6", SystemPrompt: "test"}
	mgr, _ := mcp.NewManager(nil)
	return runner.New(cfg, mgr, p, tq, nil, delegateQueues)
}

// spawnAndCollectInput builds the JSON input for spawn_and_collect with the
// given prompts all targeting the self-queue (no role).
func spawnAndCollectInput(t *testing.T, prompts []string, timeoutSecs int) json.RawMessage {
	t.Helper()
	type task struct {
		Prompt string `json:"prompt"`
	}
	type input struct {
		Tasks          []task `json:"tasks"`
		TimeoutSeconds int    `json:"timeout_seconds"`
	}
	tasks := make([]task, len(prompts))
	for i, p := range prompts {
		tasks[i] = task{Prompt: p}
	}
	raw, err := json.Marshal(input{Tasks: tasks, TimeoutSeconds: timeoutSecs})
	if err != nil {
		t.Fatalf("spawnAndCollectInput: marshal: %v", err)
	}
	return json.RawMessage(raw)
}

// submitSubtaskInput builds the JSON input for submit_subtask.
func submitSubtaskInput(t *testing.T, prompt string) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(map[string]string{"prompt": prompt})
	if err != nil {
		t.Fatalf("submitSubtaskInput: marshal: %v", err)
	}
	return json.RawMessage(raw)
}

// collectResultsInput builds the JSON input for collect_results.
func collectResultsInput(t *testing.T, taskIDs []string, timeoutSecs int) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"task_ids":        taskIDs,
		"timeout_seconds": timeoutSecs,
	})
	if err != nil {
		t.Fatalf("collectResultsInput: marshal: %v", err)
	}
	return json.RawMessage(raw)
}

// assertSpawnAndCollectResult parses the JSON returned by spawn_and_collect and
// asserts the expected counts of completed and pending entries.
func assertSpawnAndCollectResult(t *testing.T, raw string, wantCompleted, wantPending int) {
	t.Helper()
	var parsed struct {
		Completed map[string]json.RawMessage `json:"completed"`
		Pending   []string                   `json:"pending"`
	}
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		t.Fatalf("assertSpawnAndCollectResult: unmarshal %q: %v", raw, err)
	}
	if len(parsed.Completed) != wantCompleted {
		t.Errorf("completed count = %d, want %d", len(parsed.Completed), wantCompleted)
	}
	if len(parsed.Pending) != wantPending {
		t.Errorf("pending count = %d, want %d", len(parsed.Pending), wantPending)
	}
}

// assertCollectResultsResult parses the JSON returned by collect_results and
// asserts the expected counts of completed and pending entries.
func assertCollectResultsResult(t *testing.T, raw string, wantCompleted, wantPending int) {
	t.Helper()
	var parsed struct {
		Completed map[string]string `json:"completed"`
		Pending   []string          `json:"pending"`
	}
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		t.Fatalf("assertCollectResultsResult: unmarshal %q: %v", raw, err)
	}
	if len(parsed.Completed) != wantCompleted {
		t.Errorf("completed count = %d, want %d", len(parsed.Completed), wantCompleted)
	}
	if len(parsed.Pending) != wantPending {
		t.Errorf("pending count = %d, want %d", len(parsed.Pending), wantPending)
	}
}

// ---------------------------------------------------------------------------
// Integration test 1: spawn_and_collect called by the LLM with 3 subtasks
// ---------------------------------------------------------------------------

// TestIntegration_SpawnAndCollect_ThreeSubtasks verifies the end-to-end flow
// where an LLM calls spawn_and_collect with 3 prompts, the subtasks auto-complete,
// and the provider synthesizes the results into a final answer.
func TestIntegration_SpawnAndCollect_ThreeSubtasks(t *testing.T) {
	const subtaskOutput = "subtask result"
	q := newFanoutQueueWithAutoResult(subtaskOutput)

	prompts := []string{"research topic A", "research topic B", "research topic C"}
	toolInput := spawnAndCollectInput(t, prompts, 5)

	provider := &toolCallingProvider{
		calls: []toolCall{
			{Name: "spawn_and_collect", Input: toolInput},
		},
		synthesize: func(results []string) string {
			// results[0] is the JSON from spawn_and_collect
			return "synthesized: " + results[0]
		},
		usage: queue.TokenUsage{InputTokens: 100, OutputTokens: 50},
	}

	r := newRunnerWithProvider(provider, q, nil)
	finalResult, usage, err := r.RunTask(context.Background(), queue.Task{ID: "parent-1", Prompt: "research all topics"})
	if err != nil {
		t.Fatalf("RunTask: %v", err)
	}

	// Verify 3 subtasks were submitted.
	submitted := q.submittedPrompts()
	if len(submitted) != 3 {
		t.Fatalf("expected 3 submitted prompts, got %d: %v", len(submitted), submitted)
	}
	for i, want := range prompts {
		if submitted[i] != want {
			t.Errorf("submitted[%d] = %q, want %q", i, submitted[i], want)
		}
	}

	// Verify the final result includes the synthesis prefix.
	if !strings.HasPrefix(finalResult, "synthesized:") {
		t.Errorf("finalResult = %q, want prefix %q", finalResult, "synthesized:")
	}

	// Verify the embedded spawn_and_collect JSON shows 3 completed entries.
	// Strip the "synthesized: " prefix to get the raw JSON.
	rawJSON := strings.TrimPrefix(finalResult, "synthesized: ")
	assertSpawnAndCollectResult(t, rawJSON, 3, 0)

	// Verify token usage is propagated correctly.
	if usage.InputTokens != 100 {
		t.Errorf("usage.InputTokens = %d, want 100", usage.InputTokens)
	}
	if usage.OutputTokens != 50 {
		t.Errorf("usage.OutputTokens = %d, want 50", usage.OutputTokens)
	}
}

// ---------------------------------------------------------------------------
// Integration test 2: submit_subtask x3 then collect_results
// ---------------------------------------------------------------------------

// TestIntegration_SubmitSubtaskThenCollectResults verifies the two-step flow
// where an LLM calls submit_subtask three times, records the returned IDs,
// then calls collect_results with all three IDs to retrieve the outputs.
func TestIntegration_SubmitSubtaskThenCollectResults(t *testing.T) {
	const subtaskOutput = "worker output"
	q := newFanoutQueueWithAutoResult(subtaskOutput)

	// The provider's agentic loop: submit 3 tasks, then collect all 3.
	// We need to build a provider that performs this multi-step interaction.
	submittedIDs := make([]string, 0, 3)
	var idsMu sync.Mutex

	// Build a provider that mimics: submit x3, then collect.
	provider := &multiStepProvider{
		steps: func(ctx context.Context, callTool func(context.Context, string, json.RawMessage) (string, error)) (string, error) {
			prompts := []string{"subtask one", "subtask two", "subtask three"}

			// Submit each subtask.
			for _, prompt := range prompts {
				input := submitSubtaskInput(t, prompt)
				result, err := callTool(ctx, "submit_subtask", input)
				if err != nil {
					return "", fmt.Errorf("submit_subtask: %w", err)
				}
				// Parse the task ID from "subtask submitted with id: <id>".
				id := strings.TrimPrefix(result, "subtask submitted with id: ")
				idsMu.Lock()
				submittedIDs = append(submittedIDs, id)
				idsMu.Unlock()
			}

			// Collect all results.
			idsMu.Lock()
			ids := make([]string, len(submittedIDs))
			copy(ids, submittedIDs)
			idsMu.Unlock()

			collectInput := collectResultsInput(t, ids, 5)
			collectResult, err := callTool(ctx, "collect_results", collectInput)
			if err != nil {
				return "", fmt.Errorf("collect_results: %w", err)
			}
			return "final: " + collectResult, nil
		},
		usage: queue.TokenUsage{InputTokens: 200, OutputTokens: 80},
	}

	r := newRunnerWithProvider(provider, q, nil)
	finalResult, usage, err := r.RunTask(context.Background(), queue.Task{ID: "parent-2", Prompt: "process all tasks"})
	if err != nil {
		t.Fatalf("RunTask: %v", err)
	}

	// Verify 3 prompts were submitted to the queue.
	submitted := q.submittedPrompts()
	if len(submitted) != 3 {
		t.Fatalf("expected 3 submitted prompts, got %d: %v", len(submitted), submitted)
	}

	// Verify the final result contains the collect_results JSON.
	if !strings.HasPrefix(finalResult, "final: ") {
		t.Errorf("finalResult = %q, want prefix %q", finalResult, "final: ")
	}
	rawJSON := strings.TrimPrefix(finalResult, "final: ")
	assertCollectResultsResult(t, rawJSON, 3, 0)

	// Verify all collected results contain the expected subtask output.
	var parsed struct {
		Completed map[string]string `json:"completed"`
	}
	if err := json.Unmarshal([]byte(rawJSON), &parsed); err != nil {
		t.Fatalf("unmarshal collect result: %v", err)
	}
	for id, output := range parsed.Completed {
		if output != subtaskOutput {
			t.Errorf("completed[%s] = %q, want %q", id, output, subtaskOutput)
		}
	}

	// Verify token usage.
	if usage.InputTokens != 200 {
		t.Errorf("usage.InputTokens = %d, want 200", usage.InputTokens)
	}
	if usage.OutputTokens != 80 {
		t.Errorf("usage.OutputTokens = %d, want 80", usage.OutputTokens)
	}
}

// ---------------------------------------------------------------------------
// Integration test 3: partial results when some subtasks never complete
// ---------------------------------------------------------------------------

// TestIntegration_SpawnAndCollect_PartialResultsOnTimeout verifies that when
// some subtasks do not complete within the timeout, the runner returns partial
// results with the completed subset and lists the still-pending task IDs.
func TestIntegration_SpawnAndCollect_PartialResultsOnTimeout(t *testing.T) {
	// Queue with NO auto-results - we'll manually set results for only some tasks.
	q := newFanoutQueue()

	// Provider calls spawn_and_collect with 3 tasks, short timeout.
	type taskSpec struct {
		Prompt string `json:"prompt"`
	}
	type spawnInput struct {
		Tasks          []taskSpec `json:"tasks"`
		TimeoutSeconds int        `json:"timeout_seconds"`
	}
	rawInput, err := json.Marshal(spawnInput{
		Tasks: []taskSpec{
			{Prompt: "task alpha"},
			{Prompt: "task beta"},
			{Prompt: "task gamma"},
		},
		TimeoutSeconds: 1, // short timeout - some will not complete
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var capturedResult string
	provider := &toolCallingProvider{
		calls: []toolCall{
			{Name: "spawn_and_collect", Input: json.RawMessage(rawInput)},
		},
		synthesize: func(results []string) string {
			capturedResult = results[0]
			return results[0]
		},
	}

	_ = newRunnerWithProvider(provider, q, nil) // unused; r2 below drives the actual run

	// Pre-populate result for the FIRST submitted task only. We do this by
	// running the submit calls ahead, capturing the IDs, then only setting one.
	// Since fanoutQueue assigns IDs sequentially, we pre-submit to learn them.
	id1, _ := q.Submit(context.Background(), "pre-probe-1", nil)
	id2, _ := q.Submit(context.Background(), "pre-probe-2", nil)
	id3, _ := q.Submit(context.Background(), "pre-probe-3", nil)
	// Only id1 gets a result; id2 and id3 will timeout.
	q.setResult(id1, "alpha done")
	_ = id2
	_ = id3

	// Reset the queue state for the actual RunTask by creating a fresh queue
	// that returns a result only for tasks submitted in a specific order.
	freshQ := newFanoutQueue()
	// The provider will submit 3 tasks. We want the first one to have a result.
	// We use a queue that sets a result for the first submitted task automatically.
	firstResultQ := &firstTaskResultQueue{
		fanoutQueue: newFanoutQueue(),
		result:      "alpha done",
		resultCount: 1,
	}

	r2 := newRunnerWithProvider(provider, firstResultQ, nil)
	_, _, err = r2.RunTask(context.Background(), queue.Task{ID: "parent-3", Prompt: "run partial"})
	if err != nil {
		t.Fatalf("RunTask: %v", err)
	}

	// The spawn_and_collect result should have 1 completed and 2 pending.
	assertSpawnAndCollectResult(t, capturedResult, 1, 2)

	_ = freshQ // suppress unused warning
}

// firstTaskResultQueue is a fanoutQueue variant that auto-assigns results only
// for the first N submitted tasks.
type firstTaskResultQueue struct {
	*fanoutQueue
	mu          sync.Mutex
	result      string
	resultCount int
	submitted   int
}

func (q *firstTaskResultQueue) Submit(ctx context.Context, prompt string, meta map[string]string) (string, error) {
	id, err := q.fanoutQueue.Submit(ctx, prompt, meta)
	if err != nil {
		return id, err
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	q.submitted++
	if q.submitted <= q.resultCount {
		q.setResult(id, q.result)
	}
	return id, nil
}

// ---------------------------------------------------------------------------
// Integration test 4: token usage tracked per subtask execution via Ack
// ---------------------------------------------------------------------------

// TestIntegration_TokenUsageTrackedOnAck verifies that when RunTask completes
// and the caller calls Ack with the returned TokenUsage, the usage is correctly
// passed through. This simulates the agent worker loop that calls r.RunTask and
// then q.Ack(task, result, usage, nil) with the returned usage.
func TestIntegration_TokenUsageTrackedOnAck(t *testing.T) {
	const subtaskOutput = "analysis complete"
	q := newAckTrackingQueue(subtaskOutput)

	expectedUsage := queue.TokenUsage{InputTokens: 512, OutputTokens: 256}

	provider := &toolCallingProvider{
		calls: []toolCall{
			{Name: "spawn_and_collect", Input: spawnAndCollectInput(t, []string{"analyze A", "analyze B", "analyze C"}, 5)},
		},
		synthesize: func(results []string) string {
			return "final synthesis"
		},
		usage: expectedUsage,
	}

	r := newRunnerWithProvider(provider, q, nil)
	task := queue.Task{ID: "subtask-usage-1", Prompt: "analyze everything"}

	result, usage, err := r.RunTask(context.Background(), task)
	if err != nil {
		t.Fatalf("RunTask: %v", err)
	}

	// Simulate what the agent worker does: Ack with the returned usage.
	if err := q.Ack(task, result, usage, nil); err != nil {
		t.Fatalf("Ack: %v", err)
	}

	// Verify the Ack was recorded with the correct token usage.
	records := q.ackedRecords()
	if len(records) != 1 {
		t.Fatalf("expected 1 Ack record, got %d", len(records))
	}

	rec := records[0]
	if rec.Task.ID != task.ID {
		t.Errorf("Ack task.ID = %q, want %q", rec.Task.ID, task.ID)
	}
	if rec.Result != result {
		t.Errorf("Ack result = %q, want %q", rec.Result, result)
	}
	if rec.Usage.InputTokens != expectedUsage.InputTokens {
		t.Errorf("Ack usage.InputTokens = %d, want %d", rec.Usage.InputTokens, expectedUsage.InputTokens)
	}
	if rec.Usage.OutputTokens != expectedUsage.OutputTokens {
		t.Errorf("Ack usage.OutputTokens = %d, want %d", rec.Usage.OutputTokens, expectedUsage.OutputTokens)
	}

	// Verify the inner subtasks were submitted (3 prompts for spawn_and_collect).
	submitted := q.submittedPrompts()
	if len(submitted) != 3 {
		t.Errorf("expected 3 inner submitted prompts, got %d", len(submitted))
	}
}

// ---------------------------------------------------------------------------
// Integration test 5: spawn_and_collect with role-based delegation
// ---------------------------------------------------------------------------

// TestIntegration_SpawnAndCollect_WithRoleDelegation verifies that when the
// LLM calls spawn_and_collect with tasks targeting named roles, each task is
// routed to the correct delegate queue.
func TestIntegration_SpawnAndCollect_WithRoleDelegation(t *testing.T) {
	selfQ := newFanoutQueueWithAutoResult("self result")
	analystQ := newFanoutQueueWithAutoResult("analyst result")
	writerQ := newFanoutQueueWithAutoResult("writer result")

	type taskSpec struct {
		Prompt string `json:"prompt"`
		Role   string `json:"role,omitempty"`
	}
	type spawnInput struct {
		Tasks          []taskSpec `json:"tasks"`
		TimeoutSeconds int        `json:"timeout_seconds"`
	}
	rawInput, err := json.Marshal(spawnInput{
		Tasks: []taskSpec{
			{Prompt: "research the topic"},            // self queue
			{Prompt: "analyze data", Role: "analyst"}, // analyst queue
			{Prompt: "write report", Role: "writer"},  // writer queue
		},
		TimeoutSeconds: 5,
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var capturedResult string
	provider := &toolCallingProvider{
		calls: []toolCall{
			{Name: "spawn_and_collect", Input: json.RawMessage(rawInput)},
		},
		synthesize: func(results []string) string {
			capturedResult = results[0]
			return results[0]
		},
	}

	delegateQueues := map[string]queue.TaskQueue{
		"analyst": analystQ,
		"writer":  writerQ,
	}

	r := newRunnerWithProvider(provider, selfQ, delegateQueues)
	_, _, err = r.RunTask(context.Background(), queue.Task{ID: "parent-5", Prompt: "coordinate work"})
	if err != nil {
		t.Fatalf("RunTask: %v", err)
	}

	// Verify routing: self queue should have 1 prompt, each delegate queue 1 each.
	selfPrompts := selfQ.submittedPrompts()
	if len(selfPrompts) != 1 || selfPrompts[0] != "research the topic" {
		t.Errorf("self queue prompts = %v, want [\"research the topic\"]", selfPrompts)
	}

	analystPrompts := analystQ.submittedPrompts()
	if len(analystPrompts) != 1 || analystPrompts[0] != "analyze data" {
		t.Errorf("analyst queue prompts = %v, want [\"analyze data\"]", analystPrompts)
	}

	writerPrompts := writerQ.submittedPrompts()
	if len(writerPrompts) != 1 || writerPrompts[0] != "write report" {
		t.Errorf("writer queue prompts = %v, want [\"write report\"]", writerPrompts)
	}

	// All 3 tasks should complete.
	assertSpawnAndCollectResult(t, capturedResult, 3, 0)
}

// ---------------------------------------------------------------------------
// Integration test 6: LLM provider error propagates through RunTask
// ---------------------------------------------------------------------------

// TestIntegration_SpawnAndCollect_ProviderToolCallError verifies that when the
// LLM calls an unknown tool, the error propagates correctly through RunTask.
func TestIntegration_SpawnAndCollect_ProviderToolCallError(t *testing.T) {
	q := newFanoutQueueWithAutoResult("output")

	provider := &toolCallingProvider{
		calls: []toolCall{
			{Name: "nonexistent_tool", Input: json.RawMessage(`{}`)},
		},
	}

	r := newRunnerWithProvider(provider, q, nil)
	_, _, err := r.RunTask(context.Background(), queue.Task{ID: "err-1", Prompt: "do something"})
	if err == nil {
		t.Fatal("expected error when LLM calls unknown tool, got nil")
	}
}

// ---------------------------------------------------------------------------
// multiStepProvider - a provider that executes a custom multi-step function
// ---------------------------------------------------------------------------

// multiStepProvider is an LLMProvider whose RunTask executes a supplied steps
// function, enabling tests to script arbitrary sequences of tool calls that
// mimic real agentic loops.
type multiStepProvider struct {
	steps func(ctx context.Context, callTool func(context.Context, string, json.RawMessage) (string, error)) (string, error)
	usage queue.TokenUsage
}

func (p *multiStepProvider) RunTask(
	ctx context.Context,
	_ *config.Config,
	_ queue.Task,
	_ []mcp.Tool,
	callTool func(context.Context, string, json.RawMessage) (string, error),
	_ func(string),
) (string, queue.TokenUsage, error) {
	result, err := p.steps(ctx, callTool)
	return result, p.usage, err
}

func (p *multiStepProvider) Embed(_ context.Context, _ string) ([]float32, error) {
	return nil, providers.ErrEmbeddingNotSupported
}
