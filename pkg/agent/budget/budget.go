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
// checks this limit before running every task using a pluggable Store backend.
// This is proactive enforcement - the task is rejected before any LLM call is
// made, not after the operator notices the overage on its next reconcile cycle.
package budget

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"

	agenterrors "github.com/kubeswarm/kubeswarm/pkg/agent/errors"
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

// StoreFactory constructs a Store from a connection URL and budget parameters.
type StoreFactory func(url string, limit int64, namespace, agentName string) (Store, error)

var (
	storeMu       sync.RWMutex
	storeBackends = map[string]StoreFactory{}
)

// RegisterStore registers a Store factory under a scheme name.
// Call from an init() function so blank-importing the package activates it.
func RegisterStore(scheme string, f StoreFactory) {
	storeMu.Lock()
	defer storeMu.Unlock()
	storeBackends[scheme] = f
}

// NewStore returns a Store for the given URL and budget parameters.
// Returns a no-op store when limit <= 0 or when namespace/agentName are empty
// (local development, swarm run, or no limit configured).
func NewStore(url string, limit int64, namespace, agentName string) (Store, error) {
	if limit <= 0 || namespace == "" || agentName == "" {
		return noopStore{}, nil
	}

	scheme, _, _ := strings.Cut(url, "://")
	storeMu.RLock()
	f, ok := storeBackends[scheme]
	storeMu.RUnlock()
	if !ok {
		return nil, agenterrors.NewConfigError(agenterrors.ErrConfigMissing, fmt.Sprintf("no budget Store backend registered for scheme %q", scheme), nil)
	}
	return f(url, limit, namespace, agentName)
}

// noopStore is returned when no limit is configured or when running locally.
type noopStore struct{}

func (noopStore) Check(_ context.Context) error                     { return nil }
func (noopStore) Record(_ context.Context, _ string, _ int64) error { return nil }
func (noopStore) Close() error                                      { return nil }

// ParseTokens extracts the token count from a member string "{taskID}:{totalTokens}".
// Uses LastIndex so taskIDs that contain colons are handled correctly.
// Exported for use by backend implementations.
func ParseTokens(member string) int64 {
	idx := strings.LastIndex(member, ":")
	if idx < 0 {
		return 0
	}
	n, _ := strconv.ParseInt(member[idx+1:], 10, 64)
	return n
}
