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

// Package queue defines the pluggable interfaces for task distribution and
// real-time token streaming used by both the operator and the agent runtime.
//
// There are two independent extension points:
//
//   - TaskQueue — work submission, polling, ack/nack, and result collection.
//   - StreamChannel — real-time token chunk delivery for SSE streaming.
//
// Both follow the same registration pattern: importing a backend package
// (e.g. queue/redis) via a blank import registers it via init().
package queue

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
)

// StreamDone is the sentinel chunk value that signals end-of-stream.
const StreamDone = "__EOF__"

// PermanentFailure is a prefix that LLM providers prepend to error messages that
// must not be retried regardless of the remaining retry budget. Examples include
// billing errors (HTTP 400 credit_balance_too_low), authentication failures (401),
// and permission errors (403) - conditions that cannot be resolved by retrying.
//
// Nack implementations skip the retry queue and dead-letter immediately when they
// see this prefix in the reason string.
const PermanentFailure = "permanent failure"

// isPermanentFailure reports whether a Nack reason indicates a condition that
// cannot be resolved by retrying. It is intentionally SDK-agnostic: it checks
// both the explicit PermanentFailure prefix (set by providers that wrap their
// errors) and the HTTP status codes that universally signal permanent failures
// across all HTTP-based LLM APIs (Anthropic, OpenAI, Gemini, and any compatible
// endpoint). 4xx codes other than 429 (rate limit) are permanent because the
// request itself is the problem - retrying the identical request never helps.
//
// Status codes checked:
//   - 400 Bad Request  - invalid request, billing exhausted, malformed input
//   - 401 Unauthorized - API key missing or revoked
//   - 403 Forbidden    - API key lacks permission for this operation
//   - 404 Not Found    - model or endpoint does not exist
//   - 422 Unprocessable - semantically invalid request
func IsPermanentFailure(reason string) bool {
	if strings.HasPrefix(reason, PermanentFailure) {
		return true
	}
	// HTTP error strings from standard API clients embed the status code as
	// a space-delimited integer, e.g. ": 400 Bad Request" or "400 Bad Request".
	// Match against the specific 4xx codes we treat as permanent.
	for _, marker := range []string{" 400 ", " 401 ", " 403 ", " 404 ", " 422 "} {
		if strings.Contains(reason, marker) {
			return true
		}
	}
	return false
}

// Task is a unit of work delivered from the queue to an agent.
type Task struct {
	ID     string
	Prompt string
	Meta   map[string]string // arbitrary key-value pairs (e.g. stream_key, attempt)
}

// TokenUsage records LLM token consumption for a completed task.
type TokenUsage struct {
	InputTokens  int64
	OutputTokens int64
}

// TaskResult holds the outcome of a completed task as reported back to the operator.
type TaskResult struct {
	TaskID    string
	Output    string
	Error     string // non-empty when the task reached the dead-letter queue
	Usage     TokenUsage
	Artifacts map[string]string // name -> URL/path; nil when no artifacts
}

// TaskQueue is the pluggable interface for work distribution.
// Implementations register themselves via RegisterQueue and are selected by
// URL scheme via NewQueue.
type TaskQueue interface {
	// Submit enqueues a task. meta carries optional key-value metadata
	// (e.g. "stream_key" for SSE streaming). Returns the assigned task ID.
	Submit(ctx context.Context, prompt string, meta map[string]string) (string, error)

	// Poll blocks briefly for the next available task.
	// Returns (nil, nil) when the queue is empty so callers can check ctx.Done().
	Poll(ctx context.Context) (*Task, error)

	// Ack marks a task as successfully completed and stores the result with token usage.
	// The full Task is required so implementations can store the result under the original
	// task ID when the task was retried (preserving the ID the flow controller tracks).
	// Returns an error if the backend operation fails — callers should log and handle it.
	Ack(task Task, result string, usage TokenUsage, artifacts map[string]string) error

	// Nack marks a task as failed. Implementations handle retry and dead-letter logic.
	// Returns an error if the backend operation fails — callers should log and handle it.
	Nack(task Task, reason string) error

	// Results returns completed task results for the given set of task IDs.
	// Used by the operator to check whether running flow steps have finished.
	Results(ctx context.Context, taskIDs []string) ([]TaskResult, error)

	// Cancel abandons the given tasks. For each task ID the implementation should:
	//   - XDEL the stream entry (prevents unpolled tasks from being picked up)
	//   - XACK the entry (removes it from the PEL if already polled)
	//   - Store a "run cancelled" error result so collectResults doesn't hang
	// Best-effort: errors are logged but do not fail the caller.
	Cancel(ctx context.Context, taskIDs []string) error

	// Close releases any resources held by the implementation.
	Close()
}

// StreamChannel is the pluggable interface for real-time token delivery.
// It is intentionally separate from TaskQueue so the two can use different
// backends (e.g. Kafka for tasks, Redis for streaming).
type StreamChannel interface {
	// Publish appends a token chunk to the named channel.
	// Returns an error if the backend write fails — callers should log and continue.
	Publish(key, chunk string) error

	// Done signals end-of-stream and schedules cleanup on the named channel.
	// Returns an error if the backend write fails.
	Done(key string) error

	// Read blocks briefly for the next token chunk on the named channel.
	// Returns ("", nil) when no chunk is available yet, (StreamDone, nil) at end-of-stream.
	Read(ctx context.Context, key string) (string, error)
}

// QueueFactory constructs a TaskQueue from a connection URL and max retry count.
type QueueFactory func(url string, maxRetries int) (TaskQueue, error)

// StreamFactory constructs a StreamChannel from a connection URL.
type StreamFactory func(url string) (StreamChannel, error)

var (
	qmu       sync.RWMutex
	qBackends = map[string]QueueFactory{}

	smu       sync.RWMutex
	sBackends = map[string]StreamFactory{}
)

// RegisterQueue makes a TaskQueue backend available under the given name.
// It is typically called from an init() function in the backend package.
func RegisterQueue(name string, f QueueFactory) {
	qmu.Lock()
	defer qmu.Unlock()
	qBackends[name] = f
}

// RegisterStream makes a StreamChannel backend available under the given name.
// It is typically called from an init() function in the backend package.
func RegisterStream(name string, f StreamFactory) {
	smu.Lock()
	defer smu.Unlock()
	sBackends[name] = f
}

// NewQueue creates a TaskQueue by inferring the backend from the URL scheme.
func NewQueue(url string, maxRetries int) (TaskQueue, error) {
	name := Detect(url)
	qmu.RLock()
	f, ok := qBackends[name]
	qmu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("unknown task queue backend %q; available: %s", name, strings.Join(QueueBackends(), ", "))
	}
	return f(url, maxRetries)
}

// NewStream creates a StreamChannel by inferring the backend from the URL scheme.
func NewStream(url string) (StreamChannel, error) {
	name := Detect(url)
	smu.RLock()
	f, ok := sBackends[name]
	smu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("unknown stream channel backend %q; available: %s", name, strings.Join(StreamBackends(), ", "))
	}
	return f(url)
}

// Detect infers a backend name from a connection URL scheme.
// Falls back to "redis" for unrecognised schemes.
func Detect(url string) string {
	switch {
	case strings.HasPrefix(url, "redis"):
		return "redis"
	case strings.HasPrefix(url, "nats"):
		return "nats"
	case strings.HasPrefix(url, "memory"):
		return "memory"
	default:
		return "redis"
	}
}

// QueueBackends returns the names of all registered TaskQueue backends, sorted.
func QueueBackends() []string {
	qmu.RLock()
	defer qmu.RUnlock()
	return sortedKeys(qBackends)
}

// StreamBackends returns the names of all registered StreamChannel backends, sorted.
func StreamBackends() []string {
	smu.RLock()
	defer smu.RUnlock()
	return sortedKeys(sBackends)
}

func sortedKeys[V any](m map[string]V) []string {
	names := make([]string, 0, len(m))
	for k := range m {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
}
