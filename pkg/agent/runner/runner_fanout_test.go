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
	"time"

	"github.com/kubeswarm/kubeswarm/pkg/agent/config"
	"github.com/kubeswarm/kubeswarm/pkg/agent/mcp"
	"github.com/kubeswarm/kubeswarm/pkg/agent/queue"
	"github.com/kubeswarm/kubeswarm/pkg/agent/runner"
)

const (
	collectResultsTool  = "collect_results"
	spawnAndCollectTool = "spawn_and_collect"
)

// ---------------------------------------------------------------------------
// fanoutQueue is a stubQueue that tracks submissions and returns configurable results.
// ---------------------------------------------------------------------------

// idCounter provides globally unique task IDs across all fanoutQueue instances.
var idCounter struct {
	mu sync.Mutex
	n  int
}

func nextGlobalID() string {
	idCounter.mu.Lock()
	defer idCounter.mu.Unlock()
	idCounter.n++
	return fmt.Sprintf("task-%d", idCounter.n)
}

type fanoutQueue struct {
	mu        sync.Mutex
	submitted []string                    // prompts submitted via Submit
	results   map[string]queue.TaskResult // pre-configured results keyed by task ID
	// assignedIDs tracks IDs in submission order for pre-populating results.
	assignedIDs []string
	// autoResult, when non-empty, is used as the output for every submitted task.
	autoResult *string
}

func newFanoutQueue() *fanoutQueue {
	return &fanoutQueue{results: map[string]queue.TaskResult{}}
}

// newFanoutQueueWithAutoResult creates a queue that automatically populates
// results for every submitted task with the given output.
func newFanoutQueueWithAutoResult(output string) *fanoutQueue {
	return &fanoutQueue{results: map[string]queue.TaskResult{}, autoResult: &output}
}

func (q *fanoutQueue) Submit(_ context.Context, prompt string, _ map[string]string) (string, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	id := nextGlobalID()
	q.submitted = append(q.submitted, prompt)
	q.assignedIDs = append(q.assignedIDs, id)
	if q.autoResult != nil {
		q.results[id] = queue.TaskResult{TaskID: id, Output: *q.autoResult}
	}
	return id, nil
}

func (q *fanoutQueue) Poll(_ context.Context) (*queue.Task, error) { return nil, nil }
func (q *fanoutQueue) Ack(_ queue.Task, _ string, _ queue.TokenUsage, _ map[string]string) error {
	return nil
}
func (q *fanoutQueue) Nack(_ queue.Task, _ string) error          { return nil }
func (q *fanoutQueue) Cancel(_ context.Context, _ []string) error { return nil }
func (q *fanoutQueue) Close()                                     {}

func (q *fanoutQueue) Results(_ context.Context, taskIDs []string) ([]queue.TaskResult, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	var out []queue.TaskResult
	for _, id := range taskIDs {
		if r, ok := q.results[id]; ok {
			out = append(out, r)
		}
	}
	return out, nil
}

// setResult pre-populates a result for a given task ID.
func (q *fanoutQueue) setResult(id, output string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.results[id] = queue.TaskResult{TaskID: id, Output: output}
}

// submittedPrompts returns a copy of submitted prompts.
func (q *fanoutQueue) submittedPrompts() []string {
	q.mu.Lock()
	defer q.mu.Unlock()
	out := make([]string, len(q.submitted))
	copy(out, q.submitted)
	return out
}

func newRunner(tq queue.TaskQueue, delegateQueues map[string]queue.TaskQueue) *runner.Runner {
	cfg := &config.Config{Model: "claude-sonnet-4-6", SystemPrompt: "test"}
	mgr, _ := mcp.NewManager(nil)
	return runner.New(cfg, mgr, &mockProvider{}, tq, nil, delegateQueues)
}

// ---------------------------------------------------------------------------
// collect_results tool registration
// ---------------------------------------------------------------------------

func TestCollectResults_PresentWithQueue(t *testing.T) {
	r := newRunner(newFanoutQueue(), nil)
	if !hasTool(r, collectResultsTool) {
		t.Fatalf("expected %q in AllTools when queue is set", collectResultsTool)
	}
}

func TestCollectResults_AbsentWithoutQueue(t *testing.T) {
	r := newRunner(nil, nil)
	if hasTool(r, collectResultsTool) {
		t.Fatalf("expected %q to be absent when queue is nil", collectResultsTool)
	}
}

// ---------------------------------------------------------------------------
// collect_results tool execution
// ---------------------------------------------------------------------------

func TestCollectResults_AllCompleted(t *testing.T) {
	q := newFanoutQueue()
	// Submit two tasks to get real IDs, then set their results.
	id1, _ := q.Submit(context.Background(), "a", nil)
	id2, _ := q.Submit(context.Background(), "b", nil)
	q.setResult(id1, "result A")
	q.setResult(id2, "result B")
	r := newRunner(q, nil)

	input, _ := json.Marshal(map[string]any{
		"task_ids":        []string{id1, id2},
		"timeout_seconds": 5,
	})
	result, err := r.CallTool(context.Background(), collectResultsTool, json.RawMessage(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var parsed struct {
		Completed map[string]string `json:"completed"`
		Pending   []string          `json:"pending"`
	}
	if err := json.Unmarshal([]byte(result), &parsed); err != nil {
		t.Fatalf("failed to parse result JSON: %v", err)
	}
	if len(parsed.Completed) != 2 {
		t.Errorf("expected 2 completed, got %d", len(parsed.Completed))
	}
	if parsed.Completed[id1] != "result A" {
		t.Errorf("task %s result = %q, want %q", id1, parsed.Completed[id1], "result A")
	}
	if parsed.Completed[id2] != "result B" {
		t.Errorf("task %s result = %q, want %q", id2, parsed.Completed[id2], "result B")
	}
	if len(parsed.Pending) != 0 {
		t.Errorf("expected 0 pending, got %v", parsed.Pending)
	}
}

func TestCollectResults_PartialTimeout(t *testing.T) {
	q := newFanoutQueue()
	id1, _ := q.Submit(context.Background(), "a", nil)
	id2, _ := q.Submit(context.Background(), "b", nil)
	q.setResult(id1, "result A")
	// id2 has no result - will timeout
	r := newRunner(q, nil)

	input, _ := json.Marshal(map[string]any{
		"task_ids":        []string{id1, id2},
		"timeout_seconds": 1,
	})
	result, err := r.CallTool(context.Background(), collectResultsTool, json.RawMessage(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var parsed struct {
		Completed map[string]string `json:"completed"`
		Pending   []string          `json:"pending"`
	}
	if err := json.Unmarshal([]byte(result), &parsed); err != nil {
		t.Fatalf("failed to parse result JSON: %v", err)
	}
	if len(parsed.Completed) != 1 {
		t.Errorf("expected 1 completed, got %d", len(parsed.Completed))
	}
	if len(parsed.Pending) != 1 || parsed.Pending[0] != id2 {
		t.Errorf("expected pending=[%s], got %v", id2, parsed.Pending)
	}
}

func TestCollectResults_EmptyTaskIDs(t *testing.T) {
	r := newRunner(newFanoutQueue(), nil)

	input := json.RawMessage(`{"task_ids":[]}`)
	result, err := r.CallTool(context.Background(), collectResultsTool, input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var parsed struct {
		Completed map[string]string `json:"completed"`
		Pending   []string          `json:"pending"`
	}
	if err := json.Unmarshal([]byte(result), &parsed); err != nil {
		t.Fatalf("failed to parse result JSON: %v", err)
	}
	if len(parsed.Completed) != 0 {
		t.Errorf("expected 0 completed, got %d", len(parsed.Completed))
	}
}

func TestCollectResults_InvalidInput(t *testing.T) {
	r := newRunner(newFanoutQueue(), nil)
	_, err := r.CallTool(context.Background(), collectResultsTool, json.RawMessage(`{invalid`))
	if err == nil {
		t.Fatal("expected error for invalid JSON input")
	}
}

func TestCollectResults_MissingTaskIDs(t *testing.T) {
	r := newRunner(newFanoutQueue(), nil)
	_, err := r.CallTool(context.Background(), collectResultsTool, json.RawMessage(`{}`))
	if err == nil {
		t.Fatal("expected error when task_ids is missing")
	}
}

func TestCollectResults_ContextCancelled(t *testing.T) {
	q := newFanoutQueue()
	id1, _ := q.Submit(context.Background(), "a", nil)
	// No result set for id1 - would normally poll until timeout
	r := newRunner(q, nil)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	input, _ := json.Marshal(map[string]any{
		"task_ids":        []string{id1},
		"timeout_seconds": 30,
	})
	result, err := r.CallTool(ctx, collectResultsTool, json.RawMessage(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var parsed struct {
		Completed map[string]string `json:"completed"`
		Pending   []string          `json:"pending"`
	}
	if err := json.Unmarshal([]byte(result), &parsed); err != nil {
		t.Fatalf("failed to parse result JSON: %v", err)
	}
	if len(parsed.Pending) != 1 {
		t.Errorf("expected 1 pending after context cancel, got %v", parsed.Pending)
	}
}

func TestCollectResults_DefaultTimeout(t *testing.T) {
	q := newFanoutQueue()
	id1, _ := q.Submit(context.Background(), "a", nil)
	q.setResult(id1, "done")
	r := newRunner(q, nil)

	// No timeout_seconds specified - should use default (120) but return immediately since result exists
	input, _ := json.Marshal(map[string]any{
		"task_ids": []string{id1},
	})
	start := time.Now()
	result, err := r.CallTool(context.Background(), collectResultsTool, input)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if elapsed > 5*time.Second {
		t.Errorf("took too long (%v) - should return immediately when results available", elapsed)
	}
	if !strings.Contains(result, "done") {
		t.Errorf("expected result to contain 'done', got: %s", result)
	}
}

// ---------------------------------------------------------------------------
// spawn_and_collect tool registration
// ---------------------------------------------------------------------------

func TestSpawnAndCollect_PresentWithQueue(t *testing.T) {
	r := newRunner(newFanoutQueue(), nil)
	if !hasTool(r, spawnAndCollectTool) {
		t.Fatalf("expected %q in AllTools when queue is set", spawnAndCollectTool)
	}
}

func TestSpawnAndCollect_AbsentWithoutQueue(t *testing.T) {
	r := newRunner(nil, nil)
	if hasTool(r, spawnAndCollectTool) {
		t.Fatalf("expected %q to be absent when queue is nil", spawnAndCollectTool)
	}
}

// ---------------------------------------------------------------------------
// spawn_and_collect tool execution
// ---------------------------------------------------------------------------

func TestSpawnAndCollect_SubmitsAndCollects(t *testing.T) {
	q := newFanoutQueueWithAutoResult("output")
	r := newRunner(q, nil)

	input := json.RawMessage(`{"tasks":[{"prompt":"research A"},{"prompt":"research B"}],"timeout_seconds":5}`)
	result, err := r.CallTool(context.Background(), spawnAndCollectTool, input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify both prompts were submitted
	prompts := q.submittedPrompts()
	if len(prompts) != 2 {
		t.Fatalf("expected 2 submissions, got %d", len(prompts))
	}

	// Verify result structure
	var parsed struct {
		Completed map[string]json.RawMessage `json:"completed"`
		Pending   []string                   `json:"pending"`
	}
	if err := json.Unmarshal([]byte(result), &parsed); err != nil {
		t.Fatalf("failed to parse result JSON: %v", err)
	}
	if len(parsed.Completed) != 2 {
		t.Errorf("expected 2 completed, got %d", len(parsed.Completed))
	}
	if len(parsed.Pending) != 0 {
		t.Errorf("expected 0 pending, got %v", parsed.Pending)
	}
}

func TestSpawnAndCollect_WithDelegation(t *testing.T) {
	selfQueue := newFanoutQueueWithAutoResult("research result")
	analystQueue := newFanoutQueueWithAutoResult("analysis result")

	r := newRunner(selfQueue, map[string]queue.TaskQueue{"analyst": analystQueue})

	input := json.RawMessage(`{
		"tasks":[
			{"prompt":"research topic X"},
			{"prompt":"analyze findings","role":"analyst"}
		],
		"timeout_seconds":5
	}`)
	result, err := r.CallTool(context.Background(), spawnAndCollectTool, input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify self-queue got the research prompt
	selfPrompts := selfQueue.submittedPrompts()
	if len(selfPrompts) != 1 || selfPrompts[0] != "research topic X" {
		t.Errorf("expected self-queue to have 'research topic X', got %v", selfPrompts)
	}

	// Verify analyst queue got the analysis prompt
	analystPrompts := analystQueue.submittedPrompts()
	if len(analystPrompts) != 1 || analystPrompts[0] != "analyze findings" {
		t.Errorf("expected analyst queue to have 'analyze findings', got %v", analystPrompts)
	}

	var parsed struct {
		Completed map[string]json.RawMessage `json:"completed"`
		Pending   []string                   `json:"pending"`
	}
	if err := json.Unmarshal([]byte(result), &parsed); err != nil {
		t.Fatalf("failed to parse result JSON: %v", err)
	}
	if len(parsed.Completed) != 2 {
		t.Errorf("expected 2 completed, got %d", len(parsed.Completed))
	}
}

func TestSpawnAndCollect_UnknownRole(t *testing.T) {
	r := newRunner(newFanoutQueue(), nil)

	input := json.RawMessage(`{"tasks":[{"prompt":"do stuff","role":"nonexistent"}],"timeout_seconds":5}`)
	_, err := r.CallTool(context.Background(), spawnAndCollectTool, input)
	if err == nil {
		t.Fatal("expected error for unknown role")
	}
	if !strings.Contains(err.Error(), "nonexistent") {
		t.Errorf("expected error to mention role name, got: %v", err)
	}
}

func TestSpawnAndCollect_EmptyTasks(t *testing.T) {
	r := newRunner(newFanoutQueue(), nil)

	input := json.RawMessage(`{"tasks":[]}`)
	result, err := r.CallTool(context.Background(), spawnAndCollectTool, input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var parsed struct {
		Completed map[string]json.RawMessage `json:"completed"`
		Pending   []string                   `json:"pending"`
	}
	if err := json.Unmarshal([]byte(result), &parsed); err != nil {
		t.Fatalf("failed to parse result JSON: %v", err)
	}
	if len(parsed.Completed) != 0 {
		t.Errorf("expected 0 completed, got %d", len(parsed.Completed))
	}
}

func TestSpawnAndCollect_EmptyPrompt(t *testing.T) {
	r := newRunner(newFanoutQueue(), nil)

	input := json.RawMessage(`{"tasks":[{"prompt":""}]}`)
	_, err := r.CallTool(context.Background(), spawnAndCollectTool, input)
	if err == nil {
		t.Fatal("expected error for empty prompt")
	}
}

func TestSpawnAndCollect_InvalidInput(t *testing.T) {
	r := newRunner(newFanoutQueue(), nil)
	_, err := r.CallTool(context.Background(), spawnAndCollectTool, json.RawMessage(`{bad`))
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func hasTool(r *runner.Runner, name string) bool {
	for _, tool := range r.AllTools() {
		if tool.Name == name {
			return true
		}
	}
	return false
}
