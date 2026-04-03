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

// Package budget provides agent-side enforcement of rolling 24-hour token budgets.
//
// The operator injects AGENT_DAILY_TOKEN_LIMIT into each agent pod. The agent
// checks this limit before running every task using a Redis sorted set as the
// shared counter. This is proactive enforcement — the task is rejected before
// any LLM call is made, not after the operator notices the overage on its next
// reconcile cycle.
//
// Redis key layout:
//
//	swarm:budget:{namespace}:{agentName}:usage
//	  score  = Unix milliseconds of task completion time
//	  member = "{taskID}:{totalTokens}"
//
// Entries older than 24 hours are pruned on every Check call.
package budget

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	redisclient "github.com/redis/go-redis/v9"
)

// ErrBudgetExceeded is returned by Check when the rolling 24h token budget is full.
var ErrBudgetExceeded = errors.New("daily token budget exceeded")

// Store checks and records token usage against a rolling 24-hour budget.
type Store interface {
	// Check returns ErrBudgetExceeded if the daily token limit has been reached.
	// It also prunes entries older than 24 hours from the underlying store.
	Check(ctx context.Context) error

	// Record persists token usage for a completed task so it counts toward the budget.
	Record(ctx context.Context, taskID string, totalTokens int64) error

	// Close releases any resources held by the store.
	Close() error
}

// NewStore returns a Store that enforces the given daily token limit.
// Returns a no-op store when limit <= 0 or when namespace/agentName are empty
// (local development, swarm run, or no limit configured).
func NewStore(redisURL string, limit int64, namespace, agentName string) (Store, error) {
	if limit <= 0 || namespace == "" || agentName == "" {
		return noopStore{}, nil
	}
	opts, err := redisclient.ParseURL(redisURL)
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

// redisStore implements Store using a Redis sorted set.
type redisStore struct {
	client *redisclient.Client
	key    string
	limit  int64
}

// Check prunes stale entries then returns ErrBudgetExceeded if the 24h total
// meets or exceeds the limit.
//
// If Redis is unavailable, Check returns nil — a monitoring failure should not
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
		return nil // Redis unavailable — allow task, operator is backstop
	}

	var total int64
	for _, m := range members {
		total += parseTokens(m)
	}

	if total >= s.limit {
		return fmt.Errorf("%w: used %d of %d tokens in the last 24h", ErrBudgetExceeded, total, s.limit)
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
	// Keep the key alive for 25h — slightly longer than the window so entries
	// are never evicted while they're still inside the rolling window.
	_ = s.client.Expire(ctx, s.key, 25*time.Hour).Err()
	return nil
}

// Close releases the Redis connection.
func (s *redisStore) Close() error {
	return s.client.Close()
}

// noopStore is returned when no limit is configured or when running locally.
type noopStore struct{}

func (noopStore) Check(_ context.Context) error                     { return nil }
func (noopStore) Record(_ context.Context, _ string, _ int64) error { return nil }
func (noopStore) Close() error                                      { return nil }

// parseTokens extracts the token count from a member string "{taskID}:{totalTokens}".
// Uses LastIndex so taskIDs that contain colons are handled correctly.
func parseTokens(member string) int64 {
	idx := strings.LastIndex(member, ":")
	if idx < 0 {
		return 0
	}
	n, _ := strconv.ParseInt(member[idx+1:], 10, 64)
	return n
}
