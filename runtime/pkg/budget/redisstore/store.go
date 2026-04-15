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

// Package redisstore registers a Redis-backed budget Store.
// Import with a blank import to activate it:
//
//	import _ "github.com/kubeswarm/kubeswarm/runtime/pkg/budget/redisstore"
package redisstore

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"time"

	redisclient "github.com/redis/go-redis/v9"

	"github.com/kubeswarm/kubeswarm/pkg/agent/budget"
)

func init() {
	budget.RegisterStore("redis", factory)
	budget.RegisterStore("rediss", factory)
}

func factory(rawURL string, limit int64, namespace, agentName string) (budget.Store, error) {
	// Strip the custom "stream" query parameter (used by the task queue) before
	// passing to the Redis client which only understands standard Redis URL params.
	cleanURL := stripStreamParam(rawURL)
	opts, err := redisclient.ParseURL(cleanURL)
	if err != nil {
		return nil, fmt.Errorf("budget: parse redis URL: %w", err)
	}
	c := redisclient.NewClient(opts)
	return &redisStore{
		client: c,
		key:    fmt.Sprintf("swarm:budget:%s:%s:usage", namespace, agentName),
		limit:  limit,
	}, nil
}

// redisStore implements budget.Store using a Redis sorted set.
//
// Redis key layout:
//
//	swarm:budget:{namespace}:{agentName}:usage
//	  score  = Unix milliseconds of task completion time
//	  member = "{taskID}:{totalTokens}"
//
// Entries older than 24 hours are pruned on every Check call.
type redisStore struct {
	client *redisclient.Client
	key    string
	limit  int64
}

// Check prunes stale entries then returns ErrBudgetExceeded if the 24h total
// meets or exceeds the limit.
//
// If Redis is unavailable, Check returns nil - a monitoring failure should not
// block work. The operator-side reconcileDailyBudget scales replicas to 0 as a
// backstop in that case.
func (s *redisStore) Check(ctx context.Context) error {
	now := time.Now().UTC()
	windowStart := now.Add(-24 * time.Hour)

	// Prune entries outside the rolling 24h window.
	_ = s.client.ZRemRangeByScore(ctx, s.key,
		"-inf",
		strconv.FormatInt(windowStart.UnixMilli()-1, 10),
	).Err()

	members, err := s.client.ZRange(ctx, s.key, 0, -1).Result()
	if err != nil {
		return nil // Redis unavailable - allow task, operator is backstop
	}

	var total int64
	for _, m := range members {
		total += budget.ParseTokens(m)
	}

	if total >= s.limit {
		return fmt.Errorf("%w: used %d of %d tokens in the last 24h", budget.ErrBudgetExceeded, total, s.limit)
	}
	return nil
}

// Record adds the token usage for a completed task to the rolling window.
func (s *redisStore) Record(ctx context.Context, taskID string, totalTokens int64) error {
	if totalTokens <= 0 {
		return nil
	}
	member := fmt.Sprintf("%s:%d", taskID, totalTokens)
	if err := s.client.ZAdd(ctx, s.key, redisclient.Z{
		Score:  float64(time.Now().UTC().UnixMilli()),
		Member: member,
	}).Err(); err != nil {
		return fmt.Errorf("budget record: %w", err)
	}
	// Keep the key alive for 25h - slightly longer than the window so entries
	// are never evicted while they're still inside the rolling window.
	_ = s.client.Expire(ctx, s.key, 25*time.Hour).Err()
	return nil
}

// Close releases the Redis connection.
func (s *redisStore) Close() error {
	return s.client.Close()
}

// stripStreamParam removes the custom "stream" query parameter from a Redis URL.
// The task queue appends ?stream=<key> to route to per-agent streams, but the
// Redis client library does not recognise it and returns a parse error.
func stripStreamParam(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	q := u.Query()
	if !q.Has("stream") {
		return rawURL
	}
	q.Del("stream")
	u.RawQuery = q.Encode()
	return u.String()
}
