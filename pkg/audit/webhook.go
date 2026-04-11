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
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"time"
)

const (
	// defaultWebhookBatchSize is the maximum number of events per POST request.
	defaultWebhookBatchSize = 100

	// defaultWebhookFlushInterval is the maximum time to wait before flushing
	// a partial batch.
	defaultWebhookFlushInterval = 5 * time.Second

	// defaultWebhookMaxBuffer is the maximum number of events held in-memory
	// before oldest-first eviction begins.
	defaultWebhookMaxBuffer = 1000

	// defaultWebhookMaxRetries is the maximum number of delivery attempts per batch.
	defaultWebhookMaxRetries = 3

	// defaultWebhookMaxRetryAfter is the upper bound for honoring a 429
	// Retry-After header.
	defaultWebhookMaxRetryAfter = 60 * time.Second

	// defaultWebhookCloseTimeout is the deadline for flushing remaining events
	// during Close.
	defaultWebhookCloseTimeout = 5 * time.Second
)

// webhookBackoffSchedule defines the exponential backoff delays per retry attempt.
var webhookBackoffSchedule = []time.Duration{1 * time.Second, 5 * time.Second, 30 * time.Second}

// WebhookOption is a functional option for configuring a WebhookSink.
type WebhookOption func(*WebhookSink)

// WithHTTPClient sets a custom HTTP client for the webhook sink.
func WithHTTPClient(c *http.Client) WebhookOption {
	return func(s *WebhookSink) {
		if c != nil {
			s.client = c
		}
	}
}

// WithBatchSize sets the maximum number of events per POST request.
func WithBatchSize(n int) WebhookOption {
	return func(s *WebhookSink) {
		if n > 0 {
			s.batchSize = n
		}
	}
}

// WithFlushInterval sets the maximum time to wait before flushing a partial batch.
func WithFlushInterval(d time.Duration) WebhookOption {
	return func(s *WebhookSink) {
		if d > 0 {
			s.flushInterval = d
		}
	}
}

// WithMaxBuffer sets the maximum number of events held in the in-memory buffer.
func WithMaxBuffer(n int) WebhookOption {
	return func(s *WebhookSink) {
		if n > 0 {
			s.maxBuffer = n
		}
	}
}

// WithMaxRetries sets the maximum number of delivery attempts per batch.
func WithMaxRetries(n int) WebhookOption {
	return func(s *WebhookSink) {
		if n > 0 {
			s.maxRetries = n
		}
	}
}

// WebhookSink implements AuditSink by POSTing batches of JSON-encoded audit
// events to a configured URL. Events are buffered internally and flushed by a
// background goroutine when the batch size or flush interval is reached.
// Delivery is retried with exponential backoff on 5xx and 429 responses. 4xx
// responses (except 429) cause the batch to be dropped. The in-memory buffer
// has a configurable cap; when full, the oldest events are evicted first.
// Safe for concurrent use.
type WebhookSink struct {
	url           string
	client        *http.Client
	batchSize     int
	flushInterval time.Duration
	maxBuffer     int
	maxRetries    int

	mu     sync.Mutex
	buf    []AuditEvent
	wake   chan struct{} // signals the flush loop that new events arrived
	stopCh chan struct{} // closed by Close to stop the flush loop
	done   chan struct{} // closed when the flush loop exits
	closed bool

	// sleepFn is used for backoff delays; overridable in tests.
	sleepFn func(time.Duration)
}

// NewWebhookSink creates a WebhookSink that POSTs audit event batches to the
// given URL. The returned sink starts a background goroutine for flushing.
// Call Close to stop it and flush remaining events.
func NewWebhookSink(url string, opts ...WebhookOption) *WebhookSink {
	s := &WebhookSink{
		url:           url,
		client:        http.DefaultClient,
		batchSize:     defaultWebhookBatchSize,
		flushInterval: defaultWebhookFlushInterval,
		maxBuffer:     defaultWebhookMaxBuffer,
		maxRetries:    defaultWebhookMaxRetries,
		wake:          make(chan struct{}, 1),
		stopCh:        make(chan struct{}),
		done:          make(chan struct{}),
		sleepFn:       time.Sleep,
	}
	for _, o := range opts {
		o(s)
	}
	go s.loop()
	return s
}

// Emit adds events to the internal buffer. It never blocks the caller. When the
// buffer is at capacity, the oldest events are evicted to make room.
func (s *WebhookSink) Emit(_ context.Context, events []AuditEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return fmt.Errorf("audit webhook sink: closed")
	}

	s.buf = append(s.buf, events...)

	// Enforce buffer cap - evict oldest events first.
	if len(s.buf) > s.maxBuffer {
		excess := len(s.buf) - s.maxBuffer
		s.buf = s.buf[excess:]
	}

	// Wake the flush loop.
	select {
	case s.wake <- struct{}{}:
	default:
	}

	return nil
}

// Close stops the background flush goroutine and flushes remaining buffered
// events with a deadline. Events that cannot be sent within the deadline are
// dropped.
func (s *WebhookSink) Close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	s.mu.Unlock()

	close(s.stopCh)
	// Wait for the loop to finish, with a deadline.
	select {
	case <-s.done:
	case <-time.After(defaultWebhookCloseTimeout):
	}
}

// loop is the background goroutine that periodically flushes buffered events.
func (s *WebhookSink) loop() {
	defer close(s.done)

	ticker := time.NewTicker(s.flushInterval)
	defer ticker.Stop()

	for {
		select {
		case <-s.stopCh:
			// Drain remaining events.
			s.flushAll()
			return
		case <-ticker.C:
			s.flushAll()
		case <-s.wake:
			// Check if we have a full batch.
			s.mu.Lock()
			n := len(s.buf)
			s.mu.Unlock()
			if n >= s.batchSize {
				s.flushAll()
			}
		}
	}
}

// flushAll sends all buffered events in batches.
func (s *WebhookSink) flushAll() {
	for {
		batch := s.takeBatch()
		if len(batch) == 0 {
			return
		}
		s.sendWithRetry(batch)
	}
}

// takeBatch removes up to batchSize events from the front of the buffer.
func (s *WebhookSink) takeBatch() []AuditEvent {
	s.mu.Lock()
	defer s.mu.Unlock()

	if len(s.buf) == 0 {
		return nil
	}

	n := min(s.batchSize, len(s.buf))

	batch := make([]AuditEvent, n)
	copy(batch, s.buf[:n])
	s.buf = s.buf[n:]
	return batch
}

// sendWithRetry attempts to POST the batch, retrying on 5xx and 429 responses.
func (s *WebhookSink) sendWithRetry(batch []AuditEvent) {
	for attempt := 0; attempt < s.maxRetries; attempt++ {
		statusCode, retryAfter, err := s.post(batch)
		if err == nil && statusCode >= 200 && statusCode < 300 {
			return // success
		}

		if statusCode == http.StatusTooManyRequests {
			delay := s.retryAfterDelay(retryAfter, attempt)
			s.sleepFn(delay)
			continue
		}

		// Drop on 4xx (except 429 handled above).
		if statusCode >= 400 && statusCode < 500 {
			return
		}

		// 5xx or network error - use exponential backoff.
		if attempt < s.maxRetries-1 {
			delay := s.backoffDelay(attempt)
			s.sleepFn(delay)
		}
	}
	// All retries exhausted - drop the batch.
}

// post sends a single HTTP POST with the JSON-encoded batch and returns the
// status code, the Retry-After header value (if any), and any error.
func (s *WebhookSink) post(batch []AuditEvent) (int, string, error) {
	data, err := json.Marshal(batch)
	if err != nil {
		return 0, "", fmt.Errorf("audit webhook sink: marshal: %w", err)
	}

	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, s.url, bytes.NewReader(data))
	if err != nil {
		return 0, "", fmt.Errorf("audit webhook sink: new request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.client.Do(req)
	if err != nil {
		return 0, "", fmt.Errorf("audit webhook sink: do: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	return resp.StatusCode, resp.Header.Get("Retry-After"), nil
}

// retryAfterDelay parses the Retry-After header value as seconds and returns
// the delay clamped to defaultWebhookMaxRetryAfter. If the header is missing
// or unparseable, it falls back to the standard backoff schedule.
func (s *WebhookSink) retryAfterDelay(headerVal string, attempt int) time.Duration {
	if headerVal != "" {
		if seconds, err := strconv.Atoi(headerVal); err == nil && seconds > 0 {
			d := min(time.Duration(seconds)*time.Second, defaultWebhookMaxRetryAfter)
			return d
		}
	}
	return s.backoffDelay(attempt)
}

// backoffDelay returns the backoff duration for the given attempt index.
func (s *WebhookSink) backoffDelay(attempt int) time.Duration {
	if attempt < len(webhookBackoffSchedule) {
		return webhookBackoffSchedule[attempt]
	}
	return webhookBackoffSchedule[len(webhookBackoffSchedule)-1]
}
