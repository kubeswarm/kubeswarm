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

package audit

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// helpers -------------------------------------------------------------------

func testEvent(id string) AuditEvent {
	return AuditEvent{
		SchemaVersion: "v1",
		EventID:       id,
		Timestamp:     time.Now().UTC().Format("2006-01-02T15:04:05.000Z"),
		Action:        ActionToolCalled,
		Status:        StatusSuccess,
		Namespace:     "default",
		Agent:         "test-agent",
	}
}

// waitFor polls cond every 10ms until it returns true or 2s elapses.
func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("timed out waiting for condition")
}

// tests ---------------------------------------------------------------------

func TestWebhookSink_BatchBySize(t *testing.T) {
	var mu sync.Mutex
	var received [][]AuditEvent

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var batch []AuditEvent
		if err := json.NewDecoder(r.Body).Decode(&batch); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		mu.Lock()
		received = append(received, batch)
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	sink := NewWebhookSink(srv.URL,
		WithBatchSize(5),
		WithFlushInterval(10*time.Second), // long interval so only size triggers flush
		WithMaxBuffer(100),
	)
	defer sink.Close()

	for i := range 5 {
		_ = sink.Emit(context.Background(), []AuditEvent{testEvent(fmt.Sprintf("evt-%d", i))})
	}

	waitFor(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(received) >= 1
	})

	mu.Lock()
	defer mu.Unlock()

	if len(received) == 0 {
		t.Fatal("expected at least one batch")
	}
	if len(received[0]) != 5 {
		t.Errorf("expected batch size 5, got %d", len(received[0]))
	}
}

func TestWebhookSink_BatchByFlushInterval(t *testing.T) {
	var mu sync.Mutex
	var received [][]AuditEvent

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var batch []AuditEvent
		if err := json.NewDecoder(r.Body).Decode(&batch); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		mu.Lock()
		received = append(received, batch)
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	sink := NewWebhookSink(srv.URL,
		WithBatchSize(100), // large batch size so only interval triggers flush
		WithFlushInterval(100*time.Millisecond),
		WithMaxBuffer(100),
	)
	defer sink.Close()

	// Send fewer events than the batch size.
	for i := range 3 {
		_ = sink.Emit(context.Background(), []AuditEvent{testEvent(fmt.Sprintf("evt-%d", i))})
	}

	waitFor(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(received) >= 1
	})

	mu.Lock()
	defer mu.Unlock()

	if len(received) == 0 {
		t.Fatal("expected at least one batch from flush interval")
	}
	total := 0
	for _, b := range received {
		total += len(b)
	}
	if total != 3 {
		t.Errorf("expected 3 total events, got %d", total)
	}
}

func TestWebhookSink_RetryOn5xx(t *testing.T) {
	var attempts atomic.Int32
	var sleepCalls []time.Duration
	var sleepMu sync.Mutex

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n := attempts.Add(1)
		if n < 3 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	sink := NewWebhookSink(srv.URL,
		WithBatchSize(1),
		WithFlushInterval(50*time.Millisecond),
		WithMaxRetries(3),
	)
	sink.sleepFn = func(d time.Duration) {
		sleepMu.Lock()
		sleepCalls = append(sleepCalls, d)
		sleepMu.Unlock()
	}
	defer sink.Close()

	_ = sink.Emit(context.Background(), []AuditEvent{testEvent("retry-evt")})

	waitFor(t, func() bool {
		return attempts.Load() >= 3
	})

	if got := attempts.Load(); got < 3 {
		t.Errorf("expected at least 3 attempts, got %d", got)
	}

	sleepMu.Lock()
	defer sleepMu.Unlock()
	if len(sleepCalls) < 2 {
		t.Fatalf("expected at least 2 backoff sleeps, got %d", len(sleepCalls))
	}
	// First backoff should be 1s, second should be 5s.
	if sleepCalls[0] != 1*time.Second {
		t.Errorf("first backoff: want 1s, got %v", sleepCalls[0])
	}
	if sleepCalls[1] != 5*time.Second {
		t.Errorf("second backoff: want 5s, got %v", sleepCalls[1])
	}
}

func TestWebhookSink_DropOn4xx(t *testing.T) {
	var attempts atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts.Add(1)
		w.WriteHeader(http.StatusBadRequest) // 400
	}))
	defer srv.Close()

	sink := NewWebhookSink(srv.URL,
		WithBatchSize(1),
		WithFlushInterval(50*time.Millisecond),
		WithMaxRetries(3),
	)
	sink.sleepFn = func(_ time.Duration) {} // no-op to speed up test
	defer sink.Close()

	_ = sink.Emit(context.Background(), []AuditEvent{testEvent("drop-evt")})

	// Give the flush loop time to process.
	time.Sleep(200 * time.Millisecond)

	if got := attempts.Load(); got != 1 {
		t.Errorf("expected exactly 1 attempt for 4xx, got %d", got)
	}
}

func TestWebhookSink_429RespectsRetryAfter(t *testing.T) {
	var attempts atomic.Int32
	var sleepCalls []time.Duration
	var sleepMu sync.Mutex

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n := attempts.Add(1)
		if n == 1 {
			w.Header().Set("Retry-After", "10")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	sink := NewWebhookSink(srv.URL,
		WithBatchSize(1),
		WithFlushInterval(50*time.Millisecond),
		WithMaxRetries(3),
	)
	sink.sleepFn = func(d time.Duration) {
		sleepMu.Lock()
		sleepCalls = append(sleepCalls, d)
		sleepMu.Unlock()
	}
	defer sink.Close()

	_ = sink.Emit(context.Background(), []AuditEvent{testEvent("rate-limited-evt")})

	waitFor(t, func() bool {
		return attempts.Load() >= 2
	})

	sleepMu.Lock()
	defer sleepMu.Unlock()
	if len(sleepCalls) == 0 {
		t.Fatal("expected at least one sleep call for Retry-After")
	}
	if sleepCalls[0] != 10*time.Second {
		t.Errorf("expected Retry-After delay of 10s, got %v", sleepCalls[0])
	}
}

func TestWebhookSink_429RetryAfterClamped(t *testing.T) {
	var attempts atomic.Int32
	var sleepCalls []time.Duration
	var sleepMu sync.Mutex

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n := attempts.Add(1)
		if n == 1 {
			w.Header().Set("Retry-After", "300") // exceeds 60s cap
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	sink := NewWebhookSink(srv.URL,
		WithBatchSize(1),
		WithFlushInterval(50*time.Millisecond),
		WithMaxRetries(3),
	)
	sink.sleepFn = func(d time.Duration) {
		sleepMu.Lock()
		sleepCalls = append(sleepCalls, d)
		sleepMu.Unlock()
	}
	defer sink.Close()

	_ = sink.Emit(context.Background(), []AuditEvent{testEvent("clamped-evt")})

	waitFor(t, func() bool {
		return attempts.Load() >= 2
	})

	sleepMu.Lock()
	defer sleepMu.Unlock()
	if len(sleepCalls) == 0 {
		t.Fatal("expected at least one sleep call")
	}
	if sleepCalls[0] != 60*time.Second {
		t.Errorf("expected clamped delay of 60s, got %v", sleepCalls[0])
	}
}

func TestWebhookSink_BufferCapEvictsOldest(t *testing.T) {
	var mu sync.Mutex
	var received [][]AuditEvent

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var batch []AuditEvent
		if err := json.NewDecoder(r.Body).Decode(&batch); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		mu.Lock()
		received = append(received, batch)
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	maxBuf := 5
	sink := NewWebhookSink(srv.URL,
		WithBatchSize(100), // large so we flush only on interval
		WithFlushInterval(100*time.Millisecond),
		WithMaxBuffer(maxBuf),
	)
	defer sink.Close()

	// Emit more events than the buffer cap.
	for i := range 10 {
		_ = sink.Emit(context.Background(), []AuditEvent{testEvent(fmt.Sprintf("evt-%d", i))})
	}

	waitFor(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(received) >= 1
	})

	mu.Lock()
	defer mu.Unlock()

	total := 0
	for _, b := range received {
		total += len(b)
	}
	ids := make([]string, 0, total)
	for _, b := range received {
		for _, e := range b {
			ids = append(ids, e.EventID)
		}
	}

	if total > maxBuf {
		t.Errorf("expected at most %d events, got %d", maxBuf, total)
	}

	// The oldest events (evt-0 through evt-4) should have been evicted.
	// The remaining events should be evt-5 through evt-9.
	for _, id := range ids {
		for i := range 5 {
			if id == fmt.Sprintf("evt-%d", i) {
				t.Errorf("expected evt-%d to be evicted, but it was delivered", i)
			}
		}
	}
}

func TestWebhookSink_CloseFlushesRemaining(t *testing.T) {
	var mu sync.Mutex
	var received [][]AuditEvent

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var batch []AuditEvent
		if err := json.NewDecoder(r.Body).Decode(&batch); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		mu.Lock()
		received = append(received, batch)
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	sink := NewWebhookSink(srv.URL,
		WithBatchSize(100),
		WithFlushInterval(1*time.Hour), // very long, so only Close triggers flush
		WithMaxBuffer(100),
	)

	for i := range 3 {
		_ = sink.Emit(context.Background(), []AuditEvent{testEvent(fmt.Sprintf("evt-%d", i))})
	}

	// Close should flush remaining events.
	sink.Close()

	mu.Lock()
	defer mu.Unlock()

	total := 0
	for _, b := range received {
		total += len(b)
	}
	if total != 3 {
		t.Errorf("expected 3 events flushed on Close, got %d", total)
	}
}

func TestWebhookSink_EmitAfterCloseReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	sink := NewWebhookSink(srv.URL,
		WithBatchSize(10),
		WithFlushInterval(100*time.Millisecond),
	)
	sink.Close()

	err := sink.Emit(context.Background(), []AuditEvent{testEvent("after-close")})
	if err == nil {
		t.Error("expected error on Emit after Close")
	}
}

func TestWebhookSink_ContentTypeHeader(t *testing.T) {
	var contentType string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		contentType = r.Header.Get("Content-Type")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	sink := NewWebhookSink(srv.URL,
		WithBatchSize(1),
		WithFlushInterval(50*time.Millisecond),
	)
	defer sink.Close()

	_ = sink.Emit(context.Background(), []AuditEvent{testEvent("hdr-evt")})

	waitFor(t, func() bool {
		return contentType != ""
	})

	if contentType != "application/json" {
		t.Errorf("expected Content-Type application/json, got %q", contentType)
	}
}
