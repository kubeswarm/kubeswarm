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

// Package redis registers Redis Streams-backed implementations of TaskQueue
// and StreamChannel. Import this package with a blank import to make the
// "redis" backend available:
//
//	import _ "github.com/kubeswarm/kubeswarm/pkg/agent/queue/redis"
package redis

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	redisclient "github.com/redis/go-redis/v9"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"

	"github.com/kubeswarm/kubeswarm/pkg/agent/queue"
)

func init() {
	queue.RegisterQueue("redis", func(url string, maxRetries int) (queue.TaskQueue, error) {
		return newBackend(url, maxRetries), nil
	})
	queue.RegisterStream("redis", func(url string) (queue.StreamChannel, error) {
		return newBackend(url, 0), nil
	})
}

const (
	defaultTaskStream = "agent-tasks"
	workerGroup       = "agent-workers"
)

// resultEntry is the JSON payload stored per task in resultsHash.
type resultEntry struct {
	Result       string            `json:"result"`
	Error        string            `json:"error,omitempty"` // set when task reached dead-letter
	InputTokens  int64             `json:"input_tokens"`
	OutputTokens int64             `json:"output_tokens"`
	Artifacts    map[string]string `json:"artifacts,omitempty"`
}

// extractStreamParam parses an optional ?stream=<name> query parameter from the
// connection URL. Returns the cleaned URL (without the param) and the stream name.
// Defaults to defaultTaskStream when the parameter is absent.
// This allows per-role queue isolation for SwarmTeam without separate Redis instances.
func extractStreamParam(rawURL string) (cleanURL string, streamName string) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return rawURL, defaultTaskStream
	}
	q := u.Query()
	name := q.Get("stream")
	if name == "" {
		return rawURL, defaultTaskStream
	}
	q.Del("stream")
	u.RawQuery = q.Encode()
	return u.String(), name
}

// backend implements both queue.TaskQueue and queue.StreamChannel using a
// single Redis client. Registered separately for each interface so callers
// can wire them independently if needed.
//
// Stream names are derived from the connection URL's optional ?stream=<name>
// query parameter, allowing per-role isolation for SwarmTeam without separate
// Redis instances. Defaults to "agent-tasks" when the parameter is absent.
type backend struct {
	rdb         *redisclient.Client
	consumer    string
	maxRetries  int
	taskStream  string // e.g. "agent-tasks" or "ai-team.engineering-team.cto"
	resultsHash string // e.g. "agent-tasks-results"
	deadStream  string // e.g. "agent-tasks-dead"
}

func newBackend(addr string, maxRetries int) *backend {
	cleanURL, streamName := extractStreamParam(addr)
	opts, err := redisclient.ParseURL(cleanURL)
	if err != nil {
		opts = &redisclient.Options{Addr: cleanURL}
	}
	rdb := redisclient.NewClient(opts)
	b := &backend{
		rdb:         rdb,
		consumer:    podName(),
		maxRetries:  maxRetries,
		taskStream:  streamName,
		resultsHash: streamName + "-results",
		deadStream:  streamName + "-dead",
	}
	// Create the consumer group if it doesn't exist; "$" = only new messages.
	_ = rdb.XGroupCreateMkStream(context.Background(), b.taskStream, workerGroup, "$").Err()
	return b
}

// — TaskQueue —

func (b *backend) Submit(ctx context.Context, prompt string, meta map[string]string) (string, error) {
	values := map[string]any{
		"prompt":      prompt,
		"enqueued_at": time.Now().UTC().Format(time.RFC3339),
	}
	// Propagate W3C trace context so delegation chains stay connected across queue boundaries.
	carrier := propagation.MapCarrier{}
	otel.GetTextMapPropagator().Inject(ctx, carrier)
	for k, v := range carrier {
		values[k] = v
	}
	for k, v := range meta {
		values[k] = v
	}
	id, err := b.rdb.XAdd(ctx, &redisclient.XAddArgs{
		Stream: b.taskStream,
		Values: values,
	}).Result()
	if err != nil {
		return "", fmt.Errorf("XADD %s: %w", b.taskStream, err)
	}
	return id, nil
}

// claimIdleAfter is the minimum idle time before a pending message is
// reclaimed from a dead consumer. Set to 2 minutes — long enough to avoid
// stealing in-flight tasks, short enough to recover from pod restarts quickly.
const claimIdleAfter = 2 * time.Minute

func (b *backend) Poll(ctx context.Context) (*queue.Task, error) {
	// Before reading new messages, reclaim any that have been idle (owned by a
	// dead consumer) for longer than claimIdleAfter. This handles the case where
	// a pod was deleted mid-task and never had a chance to Nack.
	if msg := b.claimStale(ctx); msg != nil {
		return msg, nil
	}

	results, err := b.rdb.XReadGroup(ctx, &redisclient.XReadGroupArgs{
		Group:    workerGroup,
		Consumer: b.consumer,
		Streams:  []string{b.taskStream, ">"},
		Count:    1,
		Block:    2 * time.Second,
	}).Result()
	if err == redisclient.Nil {
		return nil, nil
	}
	if err != nil {
		// If the consumer group doesn't exist (stream not yet created, or Redis
		// restarted and lost state), recreate it and return empty — the next Poll
		// call will succeed once the group exists.
		if strings.Contains(err.Error(), "NOGROUP") {
			_ = b.rdb.XGroupCreateMkStream(ctx, b.taskStream, workerGroup, "0").Err()
			return nil, nil
		}
		return nil, err
	}
	if len(results) == 0 || len(results[0].Messages) == 0 {
		return nil, nil
	}
	msg := results[0].Messages[0]
	return msgToTask(msg), nil
}

// claimStale uses XAUTOCLAIM to take ownership of any pending message that has
// been idle for longer than claimIdleAfter. Returns nil when there is nothing
// to reclaim.
func (b *backend) claimStale(ctx context.Context) *queue.Task {
	claimCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	res, _, err := b.rdb.XAutoClaim(claimCtx, &redisclient.XAutoClaimArgs{
		Stream:   b.taskStream,
		Group:    workerGroup,
		Consumer: b.consumer,
		MinIdle:  claimIdleAfter,
		Start:    "0-0",
		Count:    1,
	}).Result()
	if err != nil || len(res) == 0 {
		return nil
	}
	return msgToTask(res[0])
}

func msgToTask(msg redisclient.XMessage) *queue.Task {
	prompt, _ := msg.Values["prompt"].(string)
	meta := make(map[string]string)
	for k, v := range msg.Values {
		if k == "prompt" {
			continue
		}
		if s, ok := v.(string); ok {
			meta[k] = s
		}
	}
	return &queue.Task{ID: msg.ID, Prompt: prompt, Meta: meta}
}

// ackTimeout is the maximum time allowed for Ack/Nack Redis writes.
// These run after task completion so the task context may have already expired.
const ackTimeout = 15 * time.Second

func (b *backend) Ack(task queue.Task, result string, usage queue.TokenUsage, artifacts map[string]string) error {
	ctx, cancel := context.WithTimeout(context.Background(), ackTimeout)
	defer cancel()

	if err := b.rdb.XAck(ctx, b.taskStream, workerGroup, task.ID).Err(); err != nil {
		return fmt.Errorf("XAck task %s: %w", task.ID, err)
	}
	entry, err := json.Marshal(resultEntry{
		Result:       result,
		InputTokens:  usage.InputTokens,
		OutputTokens: usage.OutputTokens,
		Artifacts:    artifacts,
	})
	if err != nil {
		return fmt.Errorf("marshal result for task %s: %w", task.ID, err)
	}
	// Store result under the original task ID (the one the flow controller tracks).
	// When a task is retried via Nack, the retry gets a new stream ID but the flow
	// controller still waits for the original ID. Storing under the original ID
	// ensures collectResults finds the result regardless of how many retries occurred.
	storeID := task.ID
	if origID := task.Meta["original_task_id"]; origID != "" {
		storeID = origID
	}
	if err := b.rdb.HSet(ctx, b.resultsHash, storeID, entry).Err(); err != nil {
		return fmt.Errorf("HSet result for task %s: %w", storeID, err)
	}
	return nil
}

func (b *backend) Nack(task queue.Task, reason string) error {
	ctx, cancel := context.WithTimeout(context.Background(), ackTimeout)
	defer cancel()

	attempt := 0
	if a, err := strconv.Atoi(task.Meta["attempt"]); err == nil {
		attempt = a
	}

	// Always acknowledge so the message leaves the PEL.
	if err := b.rdb.XAck(ctx, b.taskStream, workerGroup, task.ID).Err(); err != nil {
		return fmt.Errorf("XAck (nack) task %s: %w", task.ID, err)
	}

	// Stale tasks and permanent failures must not be retried.
	// - Stale: the task's age already exceeds the step timeout; re-queuing just produces
	//   another stale entry on every attempt.
	// - Permanent failure: a non-retriable error (billing exhausted, bad API key, unknown
	//   model, etc.) that cannot be resolved by retrying. queue.IsPermanentFailure detects
	//   these from the reason string using explicit provider prefixes and HTTP status codes.
	isNonRetriable := strings.HasPrefix(reason, "stale task") || queue.IsPermanentFailure(reason)
	if attempt < b.maxRetries && !isNonRetriable {
		// Determine the original task ID to carry through the retry chain.
		// If this task is already a retry, its original_task_id is already set;
		// otherwise, the current task.ID is the original.
		origID := task.ID
		if existing := task.Meta["original_task_id"]; existing != "" {
			origID = existing
		}

		// Do not re-queue if the original task was cancelled. Cancel() writes
		// "run cancelled" to the results hash under the original task ID. Retries
		// that reach Nack after cancellation would otherwise keep re-queuing
		// indefinitely until the stale-task timeout expires.
		if raw, err := b.rdb.HGet(ctx, b.resultsHash, origID).Result(); err == nil {
			var entry resultEntry
			if json.Unmarshal([]byte(raw), &entry) == nil && entry.Error == "run cancelled" {
				return nil
			}
		}

		values := map[string]any{
			"prompt":           task.Prompt,
			"attempt":          strconv.Itoa(attempt + 1),
			"original_task_id": origID,
		}
		for k, v := range task.Meta {
			if k != "attempt" && k != "original_task_id" {
				values[k] = v
			}
		}
		if err := b.rdb.XAdd(ctx, &redisclient.XAddArgs{Stream: b.taskStream, Values: values}).Err(); err != nil {
			return fmt.Errorf("XAdd retry for task %s: %w", task.ID, err)
		}
		return nil
	}

	// Final failure — dead-letter.
	if err := b.rdb.XAdd(ctx, &redisclient.XAddArgs{
		Stream: b.deadStream,
		Values: map[string]any{
			"task_id": task.ID,
			"prompt":  task.Prompt,
			"error":   reason,
			"attempt": strconv.Itoa(attempt),
		},
	}).Err(); err != nil {
		return fmt.Errorf("XAdd dead-letter for task %s: %w", task.ID, err)
	}

	// Write error entry to resultsHash under the original task ID so the
	// flow controller can see the failure and mark the step Failed instead
	// of hanging in Running forever.
	storeID := task.ID
	if origID := task.Meta["original_task_id"]; origID != "" {
		storeID = origID
	}
	entry, _ := json.Marshal(resultEntry{Error: reason})
	if err := b.rdb.HSet(ctx, b.resultsHash, storeID, entry).Err(); err != nil {
		return fmt.Errorf("HSet error result for task %s: %w", storeID, err)
	}
	return nil
}

func (b *backend) Results(ctx context.Context, taskIDs []string) ([]queue.TaskResult, error) {
	if len(taskIDs) == 0 {
		return nil, nil
	}
	// HMGET fetches only the requested task IDs in a single round-trip — O(N) where
	// N is len(taskIDs), not the total number of completed tasks.
	vals, err := b.rdb.HMGet(ctx, b.resultsHash, taskIDs...).Result()
	if err != nil {
		return nil, fmt.Errorf("HMGet results: %w", err)
	}
	var out []queue.TaskResult
	for i, v := range vals {
		if v == nil {
			continue // task not yet completed
		}
		s, ok := v.(string)
		if !ok {
			continue
		}
		var entry resultEntry
		if err := json.Unmarshal([]byte(s), &entry); err != nil {
			continue
		}
		out = append(out, queue.TaskResult{
			TaskID:    taskIDs[i],
			Output:    entry.Result,
			Error:     entry.Error,
			Usage:     queue.TokenUsage{InputTokens: entry.InputTokens, OutputTokens: entry.OutputTokens},
			Artifacts: entry.Artifacts,
		})
	}
	return out, nil
}

func (b *backend) Cancel(ctx context.Context, taskIDs []string) error {
	if len(taskIDs) == 0 {
		return nil
	}
	cancelCtx, cancel := context.WithTimeout(ctx, ackTimeout)
	defer cancel()

	cancelledEntry, _ := json.Marshal(resultEntry{Error: "run cancelled"})

	var firstErr error
	for _, id := range taskIDs {
		// Remove from stream (unpolled tasks — no-op if already consumed).
		if err := b.rdb.XDel(cancelCtx, b.taskStream, id).Err(); err != nil {
			firstErr = fmt.Errorf("XDel task %s: %w", id, err)
		}
		// Remove from PEL (already-polled tasks — idempotent if not in PEL).
		if err := b.rdb.XAck(cancelCtx, b.taskStream, workerGroup, id).Err(); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("XAck task %s: %w", id, err)
		}
		// Store a cancelled result so collectResults doesn't wait indefinitely.
		if err := b.rdb.HSet(cancelCtx, b.resultsHash, id, cancelledEntry).Err(); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("HSet cancel result for task %s: %w", id, err)
		}
	}
	return firstErr
}

func (b *backend) Close() {
	_ = b.rdb.Close()
}

// — StreamChannel —

const (
	publishTimeout = 5 * time.Second
	// maxPublishDepth is the maximum number of unconsumed chunks allowed in a
	// stream key's Redis List. Publish returns an error (and drops the chunk)
	// when this limit is reached, preventing unbounded memory growth on slow
	// SSE clients. The limit is deliberately generous — it only kicks in when
	// a consumer has fallen far behind.
	maxPublishDepth = 10_000
)

func (b *backend) Publish(key, chunk string) error {
	ctx, cancel := context.WithTimeout(context.Background(), publishTimeout)
	defer cancel()

	// Backpressure: refuse to enqueue when the list is already full.
	depth, err := b.rdb.LLen(ctx, key).Result()
	if err != nil {
		return fmt.Errorf("LLen stream %s: %w", key, err)
	}
	if depth >= maxPublishDepth {
		return fmt.Errorf("stream %s is full (%d/%d); dropping chunk", key, depth, maxPublishDepth)
	}

	if err := b.rdb.RPush(ctx, key, chunk).Err(); err != nil {
		return fmt.Errorf("RPush stream %s: %w", key, err)
	}
	return nil
}

func (b *backend) Done(key string) error {
	ctx, cancel := context.WithTimeout(context.Background(), publishTimeout)
	defer cancel()
	if err := b.rdb.RPush(ctx, key, queue.StreamDone).Err(); err != nil {
		return fmt.Errorf("RPush EOF stream %s: %w", key, err)
	}
	if err := b.rdb.Expire(ctx, key, 5*time.Minute).Err(); err != nil {
		return fmt.Errorf("expire stream %s: %w", key, err)
	}
	return nil
}

func (b *backend) Read(ctx context.Context, key string) (string, error) {
	vals, err := b.rdb.BLPop(ctx, 1*time.Second, key).Result()
	if err == redisclient.Nil {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if len(vals) < 2 {
		return "", nil
	}
	return vals[1], nil
}

// — helpers —

func podName() string {
	if name := os.Getenv("POD_NAME"); name != "" {
		return name
	}
	return "agent-local"
}
