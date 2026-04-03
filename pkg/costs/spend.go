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
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// SpendEntry records the cost of a single pipeline step.
type SpendEntry struct {
	Timestamp    time.Time
	Namespace    string
	Team         string
	RunName      string
	StepName     string
	Model        string
	InputTokens  int64
	OutputTokens int64
	CostUSD      float64
}

// SpendScope filters queries to a subset of entries.
// Empty strings mean "all values".
type SpendScope struct {
	Namespace string // empty = all namespaces
	Team      string // empty = all teams
	Model     string // empty = all models
}

// Period controls the time bucket granularity for rollup queries.
type Period string

const (
	PeriodDay   Period = "day"
	PeriodWeek  Period = "week"
	PeriodMonth Period = "month"
)

// RollupEntry is one time-bucket of aggregated spend.
type RollupEntry struct {
	Date         time.Time // start of the bucket (truncated to Period)
	Namespace    string
	Team         string
	Model        string
	TotalCostUSD float64
	InputTokens  int64
	OutputTokens int64
	RunCount     int64
}

// SpendStore records and queries historical pipeline spend.
// All writes are best-effort; implementations should log errors but must not
// return errors that would fail the reconcile loop.
type SpendStore interface {
	// Record saves one step's spend. Called after each step succeeds.
	Record(ctx context.Context, entry SpendEntry) error

	// Rollup returns spend aggregated into period buckets for a scope.
	Rollup(ctx context.Context, scope SpendScope, period Period, since time.Time) ([]RollupEntry, error)

	// Total returns the sum of CostUSD for a scope since the given time.
	// Used by BudgetPolicy to compare against a spend limit.
	Total(ctx context.Context, scope SpendScope, since time.Time) (float64, error)
}

// SpendStoreFactory constructs a SpendStore from a connection URL.
// The URL scheme selects the backend (e.g. "redis://...").
type SpendStoreFactory func(url string) (SpendStore, error)

var (
	storeMu       sync.RWMutex
	storeBackends = map[string]SpendStoreFactory{}
)

// RegisterSpendStore registers a SpendStore factory under a scheme name.
// Call from an init() function so blank-importing the package activates it.
func RegisterSpendStore(scheme string, f SpendStoreFactory) {
	storeMu.Lock()
	defer storeMu.Unlock()
	storeBackends[scheme] = f
}

// NewSpendStore returns a SpendStore for the given URL.
// The URL scheme selects the backend (e.g. "redis://localhost:6379").
// Returns a NoopSpendStore when url is empty.
func NewSpendStore(url string) (SpendStore, error) {
	if url == "" {
		return NoopSpendStore{}, nil
	}
	scheme, _, _ := strings.Cut(url, "://")
	storeMu.RLock()
	f, ok := storeBackends[scheme]
	storeMu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("no SpendStore backend registered for scheme %q; available: %s",
			scheme, strings.Join(SpendStoreBackends(), ", "))
	}
	return f(url)
}

// SpendStoreBackends returns the sorted names of all registered schemes.
func SpendStoreBackends() []string {
	storeMu.RLock()
	defer storeMu.RUnlock()
	names := make([]string, 0, len(storeBackends))
	for k := range storeBackends {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
}

// NoopSpendStore silently discards all writes. Useful for deployments that
// only want cost-per-run tracking (Phase 1) without full spend history.
type NoopSpendStore struct{}

func (NoopSpendStore) Record(_ context.Context, _ SpendEntry) error { return nil }
func (NoopSpendStore) Rollup(_ context.Context, _ SpendScope, _ Period, _ time.Time) ([]RollupEntry, error) {
	return nil, nil
}
func (NoopSpendStore) Total(_ context.Context, _ SpendScope, _ time.Time) (float64, error) {
	return 0, nil
}

// TruncateToPeriod truncates t to the start of the given period bucket.
func TruncateToPeriod(t time.Time, p Period) time.Time {
	switch p {
	case PeriodWeek:
		// Truncate to Monday of the week.
		t = t.UTC().Truncate(24 * time.Hour)
		weekday := int(t.Weekday())
		if weekday == 0 {
			weekday = 7
		}
		return t.AddDate(0, 0, -(weekday - 1))
	case PeriodMonth:
		return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC)
	default: // PeriodDay
		return t.UTC().Truncate(24 * time.Hour)
	}
}
