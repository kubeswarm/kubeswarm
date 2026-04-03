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

// Package redisstore registers a Redis-backed SpendStore.
// Import with a blank import to activate it:
//
//	import _ "github.com/kubeswarm/kubeswarm/pkg/costs/redisstore"
package redisstore

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/kubeswarm/kubeswarm/pkg/costs"
)

func init() {
	costs.RegisterSpendStore("redis", func(url string) (costs.SpendStore, error) {
		return New(url)
	})
}

const (
	// keyPrefix is the Redis key namespace for spend data.
	keyPrefix = "swarm:spend:"
	// globalKey holds all entries for cross-namespace queries.
	globalKey = "swarm:spend:_all"
	// maxEntries limits how many entries we read per ZRANGEBYSCORE to avoid
	// memory spikes on very large datasets.
	maxEntries = 100_000
)

// RedisSpendStore stores SpendEntry values in Redis sorted sets.
//
// Storage layout:
//
//	swarm:spend:<namespace>.<team>  → sorted set, score = unix ms, value = JSON SpendEntry
//	swarm:spend:_all                → same entries, for cross-namespace queries
type RedisSpendStore struct {
	client *redis.Client
}

// New creates a RedisSpendStore from a Redis URL (redis://...).
func New(url string) (*RedisSpendStore, error) {
	opts, err := redis.ParseURL(url)
	if err != nil {
		return nil, fmt.Errorf("redisstore: parse URL: %w", err)
	}
	return &RedisSpendStore{client: redis.NewClient(opts)}, nil
}

// Record saves one SpendEntry into the namespace+team sorted set and the global set.
func (s *RedisSpendStore) Record(ctx context.Context, entry costs.SpendEntry) error {
	data, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("redisstore: marshal entry: %w", err)
	}
	score := float64(entry.Timestamp.UnixMilli())
	member := redis.Z{Score: score, Member: string(data)}

	teamKey := keyPrefix + entry.Namespace + "." + entry.Team

	pipe := s.client.Pipeline()
	pipe.ZAdd(ctx, teamKey, member)
	pipe.ZAdd(ctx, globalKey, member)
	_, err = pipe.Exec(ctx)
	if err != nil {
		return fmt.Errorf("redisstore: record: %w", err)
	}
	return nil
}

// Rollup returns spend grouped into period buckets for the given scope.
func (s *RedisSpendStore) Rollup(
	ctx context.Context, scope costs.SpendScope, period costs.Period, since time.Time,
) ([]costs.RollupEntry, error) {
	entries, err := s.fetchEntries(ctx, scope, since)
	if err != nil {
		return nil, err
	}
	return aggregate(entries, period), nil
}

// Total returns the sum of CostUSD for a scope since the given time.
func (s *RedisSpendStore) Total(ctx context.Context, scope costs.SpendScope, since time.Time) (float64, error) {
	entries, err := s.fetchEntries(ctx, scope, since)
	if err != nil {
		return 0, err
	}
	var total float64
	for _, e := range entries {
		total += e.CostUSD
	}
	return total, nil
}

// fetchEntries reads raw SpendEntry values from Redis matching the scope.
func (s *RedisSpendStore) fetchEntries(
	ctx context.Context, scope costs.SpendScope, since time.Time,
) ([]costs.SpendEntry, error) {
	minScore := fmt.Sprintf("%d", since.UnixMilli())

	var keys []string
	if scope.Namespace != "" && scope.Team != "" {
		keys = []string{keyPrefix + scope.Namespace + "." + scope.Team}
	} else if scope.Namespace != "" {
		// Need all teams in namespace — scan keys matching swarm:spend:<namespace>.*
		pattern := keyPrefix + scope.Namespace + ".*"
		var cursor uint64
		for {
			batch, next, err := s.client.Scan(ctx, cursor, pattern, 100).Result()
			if err != nil {
				return nil, fmt.Errorf("redisstore: scan keys: %w", err)
			}
			keys = append(keys, batch...)
			cursor = next
			if cursor == 0 {
				break
			}
		}
	} else {
		keys = []string{globalKey}
	}

	var all []costs.SpendEntry
	for _, key := range keys {
		vals, err := s.client.ZRangeArgs(ctx, redis.ZRangeArgs{
			Key:     key,
			Start:   minScore,
			Stop:    "+inf",
			ByScore: true,
			Count:   maxEntries,
		}).Result()
		if err != nil {
			return nil, fmt.Errorf("redisstore: zrangebyscore %q: %w", key, err)
		}
		for _, v := range vals {
			var e costs.SpendEntry
			if err := json.Unmarshal([]byte(v), &e); err != nil {
				continue // skip corrupt entries
			}
			// Apply model filter if set.
			if scope.Model != "" && !strings.EqualFold(e.Model, scope.Model) {
				continue
			}
			all = append(all, e)
		}
	}
	return all, nil
}

// aggregate groups entries into period buckets.
func aggregate(entries []costs.SpendEntry, period costs.Period) []costs.RollupEntry {
	type key struct {
		date      time.Time
		namespace string
		team      string
		model     string
	}
	buckets := map[key]*costs.RollupEntry{}

	for _, e := range entries {
		k := key{
			date:      costs.TruncateToPeriod(e.Timestamp, period),
			namespace: e.Namespace,
			team:      e.Team,
			model:     e.Model,
		}
		b, ok := buckets[k]
		if !ok {
			b = &costs.RollupEntry{
				Date:      k.date,
				Namespace: k.namespace,
				Team:      k.team,
				Model:     k.model,
			}
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
	return result
}
