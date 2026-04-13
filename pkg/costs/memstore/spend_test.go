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

package memstore

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/kubeswarm/kubeswarm/pkg/costs"
)

func entry(t time.Time, ns, team, model string, cost float64) costs.SpendEntry {
	return costs.SpendEntry{
		Timestamp: t,
		Namespace: ns,
		Team:      team,
		Model:     model,
		CostUSD:   cost,
	}
}

func TestRecord_And_Total(t *testing.T) {
	ctx := context.Background()
	s := New()
	now := time.Now()

	if err := s.Record(ctx, entry(now, "ns1", "team1", "claude-sonnet-4-6", 1.50)); err != nil {
		t.Fatalf("Record: %v", err)
	}
	if err := s.Record(ctx, entry(now.Add(time.Second), "ns1", "team1", "claude-sonnet-4-6", 2.50)); err != nil {
		t.Fatalf("Record: %v", err)
	}

	total, err := s.Total(ctx, costs.SpendScope{Namespace: "ns1"}, now.Add(-time.Minute))
	if err != nil {
		t.Fatalf("Total: %v", err)
	}
	if total != 4.0 {
		t.Errorf("Total = %v, want 4.0", total)
	}
}

func TestTotal_SinceFiltering(t *testing.T) {
	ctx := context.Background()
	s := New()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	// Record entries at hour 0, 1, 2, 3
	for i := range 4 {
		if err := s.Record(ctx, entry(base.Add(time.Duration(i)*time.Hour), "ns", "team", "model", 1.0)); err != nil {
			t.Fatalf("Record: %v", err)
		}
	}

	// Query since hour 2 - should get entries at hour 2 and 3.
	total, err := s.Total(ctx, costs.SpendScope{}, base.Add(2*time.Hour))
	if err != nil {
		t.Fatalf("Total: %v", err)
	}
	if total != 2.0 {
		t.Errorf("Total since hour 2 = %v, want 2.0", total)
	}

	// Query since hour 0 - should get all 4.
	total, err = s.Total(ctx, costs.SpendScope{}, base)
	if err != nil {
		t.Fatalf("Total: %v", err)
	}
	if total != 4.0 {
		t.Errorf("Total since hour 0 = %v, want 4.0", total)
	}

	// Query since hour 5 - should get none.
	total, err = s.Total(ctx, costs.SpendScope{}, base.Add(5*time.Hour))
	if err != nil {
		t.Fatalf("Total: %v", err)
	}
	if total != 0.0 {
		t.Errorf("Total since hour 5 = %v, want 0.0", total)
	}
}

func TestTotal_ScopeFiltering(t *testing.T) {
	ctx := context.Background()
	s := New()
	now := time.Now()
	since := now.Add(-time.Minute)

	_ = s.Record(ctx, entry(now, "ns1", "teamA", "claude-sonnet-4-6", 1.0))
	_ = s.Record(ctx, entry(now, "ns1", "teamB", "gpt-4o", 2.0))
	_ = s.Record(ctx, entry(now, "ns2", "teamA", "claude-sonnet-4-6", 3.0))

	tests := []struct {
		name  string
		scope costs.SpendScope
		want  float64
	}{
		{"all", costs.SpendScope{}, 6.0},
		{"by namespace", costs.SpendScope{Namespace: "ns1"}, 3.0},
		{"by team", costs.SpendScope{Team: "teamA"}, 4.0},
		{"by model case-insensitive", costs.SpendScope{Model: "GPT-4O"}, 2.0},
		{"by namespace+team", costs.SpendScope{Namespace: "ns1", Team: "teamA"}, 1.0},
		{"no match", costs.SpendScope{Namespace: "ns3"}, 0.0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			total, err := s.Total(ctx, tt.scope, since)
			if err != nil {
				t.Fatalf("Total: %v", err)
			}
			if total != tt.want {
				t.Errorf("Total = %v, want %v", total, tt.want)
			}
		})
	}
}

func TestTotal_EmptyStore(t *testing.T) {
	ctx := context.Background()
	s := New()
	total, err := s.Total(ctx, costs.SpendScope{}, time.Time{})
	if err != nil {
		t.Fatalf("Total: %v", err)
	}
	if total != 0.0 {
		t.Errorf("Total = %v, want 0.0", total)
	}
}

func TestRollup_DayBuckets(t *testing.T) {
	ctx := context.Background()
	s := New()
	day1 := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)
	day2 := time.Date(2026, 3, 11, 14, 0, 0, 0, time.UTC)

	_ = s.Record(ctx, entry(day1, "ns", "team", "model", 1.0))
	_ = s.Record(ctx, entry(day1.Add(time.Hour), "ns", "team", "model", 2.0))
	_ = s.Record(ctx, entry(day2, "ns", "team", "model", 3.0))

	rollups, err := s.Rollup(ctx, costs.SpendScope{}, costs.PeriodDay, day1.Add(-time.Hour))
	if err != nil {
		t.Fatalf("Rollup: %v", err)
	}
	if len(rollups) != 2 {
		t.Fatalf("got %d rollup buckets, want 2", len(rollups))
	}

	// Find day1 bucket.
	var d1Total, d2Total float64
	for _, r := range rollups {
		if r.Date.Day() == 10 {
			d1Total = r.TotalCostUSD
		} else if r.Date.Day() == 11 {
			d2Total = r.TotalCostUSD
		}
	}
	if d1Total != 3.0 {
		t.Errorf("day1 total = %v, want 3.0", d1Total)
	}
	if d2Total != 3.0 {
		t.Errorf("day2 total = %v, want 3.0", d2Total)
	}
}

func TestEviction_OldEntriesRemoved(t *testing.T) {
	ctx := context.Background()
	s := New()
	now := time.Now()

	// Record an old entry (> 31 days ago) with a distinctive cost.
	old := now.Add(-32 * 24 * time.Hour)
	_ = s.Record(ctx, entry(old, "ns", "team", "model", 100.0))

	// Fill enough entries to trigger eviction.
	for i := range evictEveryN {
		_ = s.Record(ctx, entry(now.Add(time.Duration(i)*time.Millisecond), "ns", "team", "model", 1.0))
	}

	// Query from epoch - if old entry survived, total would be 100 + evictEveryN.
	// After eviction, it should be exactly evictEveryN (only the recent entries).
	total, err := s.Total(ctx, costs.SpendScope{}, time.Time{})
	if err != nil {
		t.Fatalf("Total: %v", err)
	}
	expected := float64(evictEveryN)
	if total != expected {
		t.Errorf("Total = %v, want %v (old entry should be evicted)", total, expected)
	}
}

func TestTotal_BinarySearchCorrectness(t *testing.T) {
	// Verify that the since cutoff correctly includes the boundary entry.
	ctx := context.Background()
	s := New()
	base := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)

	for i := range 100 {
		_ = s.Record(ctx, entry(base.Add(time.Duration(i)*time.Second), "ns", "team", "model", 1.0))
	}

	// since = exactly entry 50's timestamp - should include entry 50.
	since := base.Add(50 * time.Second)
	total, err := s.Total(ctx, costs.SpendScope{}, since)
	if err != nil {
		t.Fatalf("Total: %v", err)
	}
	if total != 50.0 {
		t.Errorf("Total = %v, want 50.0 (entries 50-99)", total)
	}
}

func TestTotal_ConcurrentAccess(t *testing.T) {
	ctx := context.Background()
	s := New()
	now := time.Now()

	// Pre-fill some entries.
	for i := range 100 {
		_ = s.Record(ctx, entry(now.Add(time.Duration(i)*time.Millisecond), "ns", "team", "model", 1.0))
	}

	// Concurrent reads and writes should not race.
	done := make(chan struct{})
	for i := range 10 {
		go func(n int) {
			defer func() { done <- struct{}{} }()
			for j := range 50 {
				_ = s.Record(ctx, entry(now.Add(time.Duration(n*50+j)*time.Millisecond), "ns", "team", "model", 0.01))
				_, _ = s.Total(ctx, costs.SpendScope{}, now.Add(-time.Minute))
			}
		}(i)
	}
	for range 10 {
		<-done
	}
}

func BenchmarkTotal_LinearVsBinarySearch(b *testing.B) {
	ctx := context.Background()
	now := time.Now()

	for _, size := range []int{100, 1000, 10000} {
		s := New()
		for i := range size {
			_ = s.Record(ctx, entry(now.Add(time.Duration(i)*time.Millisecond), "ns", "team", "model", 1.0))
		}
		// Query the last 10% of entries.
		since := now.Add(time.Duration(size*9/10) * time.Millisecond)
		b.Run(fmt.Sprintf("size=%d", size), func(b *testing.B) {
			for b.Loop() {
				_, _ = s.Total(ctx, costs.SpendScope{Namespace: "ns"}, since)
			}
		})
	}
}
