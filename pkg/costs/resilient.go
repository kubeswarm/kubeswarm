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

package costs

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// resilientConfig holds tuning knobs for the ResilientSpendStore.
type resilientConfig struct {
	retryInterval    time.Duration
	maxRetryInterval time.Duration
}

// ResilientOption configures ResilientSpendStore behaviour.
type ResilientOption func(*resilientConfig)

// WithRetryInterval sets the initial retry interval for background reconnection.
func WithRetryInterval(d time.Duration) ResilientOption {
	return func(c *resilientConfig) { c.retryInterval = d }
}

// WithMaxRetryInterval sets the maximum retry interval cap.
func WithMaxRetryInterval(d time.Duration) ResilientOption {
	return func(c *resilientConfig) { c.maxRetryInterval = d }
}

// ResilientSpendStore wraps a SpendStore with noop fallback and background
// reconnection. If the factory fails on first call, it starts with a
// NoopSpendStore and retries in the background with exponential backoff.
// All methods are safe for concurrent use.
type ResilientSpendStore struct {
	mu      sync.RWMutex
	store   SpendStore
	healthy bool

	factory func() (SpendStore, error)
	cfg     resilientConfig
	stopCh  chan struct{}
	stopped chan struct{}
}

// NewResilientSpendStore creates a ResilientSpendStore. It attempts to call
// factory immediately. If the factory succeeds, the real store is used from
// the start. If it fails, a NoopSpendStore is used and a background goroutine
// retries with exponential backoff until the factory succeeds.
func NewResilientSpendStore(factory func() (SpendStore, error), opts ...ResilientOption) *ResilientSpendStore {
	cfg := resilientConfig{
		retryInterval:    time.Second,
		maxRetryInterval: 30 * time.Second,
	}
	for _, o := range opts {
		o(&cfg)
	}

	r := &ResilientSpendStore{
		factory: factory,
		cfg:     cfg,
		stopCh:  make(chan struct{}),
		stopped: make(chan struct{}),
	}

	store, err := factory()
	if err != nil {
		slog.Warn("spend store factory failed, starting with noop fallback", "error", err)
		r.store = NoopSpendStore{}
		r.healthy = false
		go r.retryLoop()
	} else {
		r.store = store
		r.healthy = true
		close(r.stopped)
	}

	return r
}

// current returns the active store under a read lock.
func (r *ResilientSpendStore) current() SpendStore {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.store
}

// Record delegates to the current store.
func (r *ResilientSpendStore) Record(ctx context.Context, entry SpendEntry) error {
	return r.current().Record(ctx, entry)
}

// Rollup delegates to the current store.
func (r *ResilientSpendStore) Rollup(ctx context.Context, scope SpendScope, period Period, since time.Time) ([]RollupEntry, error) {
	return r.current().Rollup(ctx, scope, period, since)
}

// Total delegates to the current store.
func (r *ResilientSpendStore) Total(ctx context.Context, scope SpendScope, since time.Time) (float64, error) {
	return r.current().Total(ctx, scope, since)
}

// IsHealthy returns true when the real store is active, false when using the
// noop fallback.
func (r *ResilientSpendStore) IsHealthy() bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.healthy
}

// Close stops the background retry goroutine if running.
func (r *ResilientSpendStore) Close() {
	select {
	case <-r.stopCh:
		// Already closed.
	default:
		close(r.stopCh)
	}
	<-r.stopped
}

// retryLoop runs in the background, attempting to create a real store with
// exponential backoff until it succeeds or Close is called.
func (r *ResilientSpendStore) retryLoop() {
	defer close(r.stopped)

	interval := r.cfg.retryInterval
	for {
		select {
		case <-r.stopCh:
			return
		case <-time.After(interval):
		}

		store, err := r.factory()
		if err != nil {
			slog.Warn("spend store retry failed", "error", err, "next_retry", interval*2)
			interval *= 2
			if interval > r.cfg.maxRetryInterval {
				interval = r.cfg.maxRetryInterval
			}
			continue
		}

		r.mu.Lock()
		r.store = store
		r.healthy = true
		r.mu.Unlock()

		slog.Info("spend store connected, swapped from noop fallback")
		return
	}
}
