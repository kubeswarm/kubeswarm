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

// Package memstore provides an in-memory SpendStore for local development,
// swarm run, and unit tests. Data is not persisted across restarts.
//
// Register it with a blank import:
//
//	import _ "github.com/kubeswarm/kubeswarm/pkg/costs/memstore"
package memstore

import (
	"cmp"
	"context"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/kubeswarm/kubeswarm/pkg/costs"
)

func init() {
	costs.RegisterSpendStore("memory", func(_ string) (costs.SpendStore, error) {
		return New(), nil
	})
}

// MemSpendStore is a non-persistent, thread-safe SpendStore backed by a
// timestamp-sorted slice. Entries are appended chronologically by Record and
// queries use binary search to skip entries older than the requested window.
type MemSpendStore struct {
	mu      sync.RWMutex
	entries []costs.SpendEntry
	writes  int // counts writes for eviction scheduling
}

// New returns an empty MemSpendStore.
func New() *MemSpendStore {
	return &MemSpendStore{}
}

// maxRetentionAge is the maximum age of entries before eviction.
// 31 days covers the largest query window (monthly budgets).
const maxRetentionAge = 31 * 24 * time.Hour

// evictEveryN controls how often eviction runs. Every Nth Record call
// triggers a sweep of entries older than maxRetentionAge.
const evictEveryN = 500

func (m *MemSpendStore) Record(_ context.Context, entry costs.SpendEntry) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.entries = append(m.entries, entry)
	m.writes++
	// Periodically evict old entries to bound memory growth.
	if m.writes%evictEveryN == 0 {
		m.evictLocked()
	}
	return nil
}

// evictLocked removes entries older than maxRetentionAge. Caller must hold mu.Lock.
func (m *MemSpendStore) evictLocked() {
	cutoff := time.Now().Add(-maxRetentionAge)
	// Since entries are sorted by timestamp, find the first entry >= cutoff
	// and discard everything before it.
	idx := sort.Search(len(m.entries), func(i int) bool {
		return !m.entries[i].Timestamp.Before(cutoff)
	})
	if idx > 0 {
		n := copy(m.entries, m.entries[idx:])
		// Clear references in the tail to allow GC.
		for i := n; i < len(m.entries); i++ {
			m.entries[i] = costs.SpendEntry{}
		}
		m.entries = m.entries[:n]
	}
}

func (m *MemSpendStore) Rollup(_ context.Context, scope costs.SpendScope, period costs.Period, since time.Time) ([]costs.RollupEntry, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	type key struct {
		date      time.Time
		namespace string
		team      string
		model     string
	}
	buckets := map[key]*costs.RollupEntry{}
	m.iterSince(since, func(e *costs.SpendEntry) {
		if !matchesScope(e, scope) {
			return
		}
		k := key{
			date:      costs.TruncateToPeriod(e.Timestamp, period),
			namespace: e.Namespace,
			team:      e.Team,
			model:     e.Model,
		}
		b, ok := buckets[k]
		if !ok {
			b = &costs.RollupEntry{Date: k.date, Namespace: k.namespace, Team: k.team, Model: k.model}
			buckets[k] = b
		}
		b.TotalCostUSD += e.CostUSD
		b.InputTokens += e.InputTokens
		b.OutputTokens += e.OutputTokens
		b.RunCount++
	})
	result := make([]costs.RollupEntry, 0, len(buckets))
	for _, b := range buckets {
		result = append(result, *b)
	}
	slices.SortFunc(result, func(a, b costs.RollupEntry) int {
		if c := a.Date.Compare(b.Date); c != 0 {
			return c
		}
		if c := cmp.Compare(a.Namespace, b.Namespace); c != 0 {
			return c
		}
		if c := cmp.Compare(a.Team, b.Team); c != 0 {
			return c
		}
		return cmp.Compare(a.Model, b.Model)
	})
	return result, nil
}

func (m *MemSpendStore) Total(_ context.Context, scope costs.SpendScope, since time.Time) (float64, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var total float64
	m.iterSince(since, func(e *costs.SpendEntry) {
		if matchesScope(e, scope) {
			total += e.CostUSD
		}
	})
	return total, nil
}

// iterSince calls fn for each entry with Timestamp >= since.
// Uses binary search to skip older entries. Caller must hold mu.RLock.
func (m *MemSpendStore) iterSince(since time.Time, fn func(e *costs.SpendEntry)) {
	start := sort.Search(len(m.entries), func(i int) bool {
		return !m.entries[i].Timestamp.Before(since)
	})
	for i := start; i < len(m.entries); i++ {
		fn(&m.entries[i])
	}
}

// matchesScope reports whether e matches the given scope filters.
func matchesScope(e *costs.SpendEntry, scope costs.SpendScope) bool {
	if scope.Namespace != "" && e.Namespace != scope.Namespace {
		return false
	}
	if scope.Team != "" && e.Team != scope.Team {
		return false
	}
	if scope.Model != "" && !strings.EqualFold(e.Model, scope.Model) {
		return false
	}
	return true
}
