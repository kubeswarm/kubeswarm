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
	"context"
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

// MemSpendStore is a non-persistent, thread-safe SpendStore backed by a slice.
type MemSpendStore struct {
	mu      sync.RWMutex
	entries []costs.SpendEntry
}

// New returns an empty MemSpendStore.
func New() *MemSpendStore {
	return &MemSpendStore{}
}

func (m *MemSpendStore) Record(_ context.Context, entry costs.SpendEntry) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.entries = append(m.entries, entry)
	return nil
}

func (m *MemSpendStore) Rollup(_ context.Context, scope costs.SpendScope, period costs.Period, since time.Time) ([]costs.RollupEntry, error) {
	m.mu.RLock()
	filtered := m.filter(scope, since)
	m.mu.RUnlock()

	type key struct {
		date      time.Time
		namespace string
		team      string
		model     string
	}
	buckets := map[key]*costs.RollupEntry{}
	for _, e := range filtered {
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
	}
	result := make([]costs.RollupEntry, 0, len(buckets))
	for _, b := range buckets {
		result = append(result, *b)
	}
	return result, nil
}

func (m *MemSpendStore) Total(_ context.Context, scope costs.SpendScope, since time.Time) (float64, error) {
	m.mu.RLock()
	filtered := m.filter(scope, since)
	m.mu.RUnlock()
	var total float64
	for _, e := range filtered {
		total += e.CostUSD
	}
	return total, nil
}

// filter returns entries matching the scope since the given time. Caller must hold mu.RLock.
func (m *MemSpendStore) filter(scope costs.SpendScope, since time.Time) []costs.SpendEntry {
	var out []costs.SpendEntry
	for _, e := range m.entries {
		if e.Timestamp.Before(since) {
			continue
		}
		if scope.Namespace != "" && e.Namespace != scope.Namespace {
			continue
		}
		if scope.Team != "" && e.Team != scope.Team {
			continue
		}
		if scope.Model != "" && !strings.EqualFold(e.Model, scope.Model) {
			continue
		}
		out = append(out, e)
	}
	return out
}
