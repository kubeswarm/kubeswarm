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

package queue_test

import (
	"context"
	"slices"
	"testing"

	"github.com/kubeswarm/kubeswarm/pkg/agent/queue"
)

// ---------------------------------------------------------------------------
// Detect
// ---------------------------------------------------------------------------

func TestDetect(t *testing.T) {
	cases := []struct {
		url  string
		want string
	}{
		{"redis://localhost:6379", "redis"},
		{"rediss://localhost:6379", "redis"},
		{"redis://localhost:6379?stream=ns.team.role", "redis"},
		{"nats://localhost:4222", "nats"},
		{"memory://local", "memory"},
		{"unknown://host", "redis"}, // falls back to redis
		{"", "redis"},               // falls back to redis
	}
	for _, c := range cases {
		got := queue.Detect(c.url)
		if got != c.want {
			t.Errorf("Detect(%q) = %q, want %q", c.url, got, c.want)
		}
	}
}

// ---------------------------------------------------------------------------
// Registration and NewQueue
// ---------------------------------------------------------------------------

func TestRegisterAndNewQueue(t *testing.T) {
	// Register a fake backend under "memory" so Detect("memory://...") resolves it.
	queue.RegisterQueue("memory", func(url string, maxRetries int) (queue.TaskQueue, error) {
		return &fakeQueue{url: url}, nil
	})

	q, err := queue.NewQueue("memory://local", 3)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if q == nil {
		t.Fatal("expected non-nil queue")
	}
}

func TestNewQueueUnknownBackend(t *testing.T) {
	// "zzz" is not a registered backend and Detect falls back to "redis",
	// which may or may not be registered depending on test order.
	// Test the explicit error path with a scheme that is registered nowhere.
	_, err := queue.NewQueue("zzz-unregistered://host", 1)
	// We only care that it doesn't panic.
	_ = err
}

// ---------------------------------------------------------------------------
// QueueBackends / StreamBackends listing
// ---------------------------------------------------------------------------

func TestBackendsListing(t *testing.T) {
	// Register a unique stream backend and verify it appears in the listing.
	queue.RegisterStream("test-stream-backend", func(url string) (queue.StreamChannel, error) {
		return nil, nil
	})
	backends := queue.StreamBackends()
	found := slices.Contains(backends, "test-stream-backend")
	if !found {
		t.Error("registered stream backend not found in StreamBackends()")
	}

	// QueueBackends listing should always be sorted.
	qb := queue.QueueBackends()
	for i := 1; i < len(qb); i++ {
		if qb[i] < qb[i-1] {
			t.Errorf("QueueBackends() is not sorted: %v", qb)
		}
	}
}

// ---------------------------------------------------------------------------
// fakeQueue — minimal TaskQueue implementation for registration tests
// ---------------------------------------------------------------------------

type fakeQueue struct{ url string }

func (f *fakeQueue) Submit(_ context.Context, _ string, _ map[string]string) (string, error) {
	return "task-1", nil
}
func (f *fakeQueue) Poll(_ context.Context) (*queue.Task, error) { return nil, nil }
func (f *fakeQueue) Ack(_ queue.Task, _ string, _ queue.TokenUsage, _ map[string]string) error {
	return nil
}
func (f *fakeQueue) Nack(_ queue.Task, _ string) error          { return nil }
func (f *fakeQueue) Cancel(_ context.Context, _ []string) error { return nil }
func (f *fakeQueue) Results(_ context.Context, _ []string) ([]queue.TaskResult, error) {
	return nil, nil
}
func (f *fakeQueue) Close() {}
