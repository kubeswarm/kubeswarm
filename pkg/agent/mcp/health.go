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

package mcp

import (
	"sync"
	"time"
)

// callOutcome records one tool call result.
type callOutcome struct {
	success bool
	latency time.Duration
	err     error
}

// ServerHealth is a point-in-time view of a single MCP server's health.
type ServerHealth struct {
	ServerURL    string
	ServerName   string
	Successes    int
	Failures     int
	AvgLatencyMs int64
	LastError    error
	Up           bool // false when failure rate > 80% in the window
}

// HealthTracker tracks per-server call outcomes in a rolling window.
type HealthTracker struct {
	mu         sync.Mutex
	windowSize int
	outcomes   map[string][]callOutcome // keyed by server URL
	names      map[string]string        // server URL -> server name
}

// NewHealthTracker creates a tracker with the given rolling window size.
func NewHealthTracker(windowSize int) *HealthTracker {
	return &HealthTracker{
		windowSize: windowSize,
		outcomes:   make(map[string][]callOutcome),
		names:      make(map[string]string),
	}
}

// SetServerName associates a human-readable name with a server URL.
func (ht *HealthTracker) SetServerName(serverURL, name string) {
	ht.mu.Lock()
	defer ht.mu.Unlock()
	ht.names[serverURL] = name
}

// RecordSuccess records a successful tool call.
func (ht *HealthTracker) RecordSuccess(serverURL string, latency time.Duration) {
	ht.record(serverURL, callOutcome{success: true, latency: latency})
}

// RecordFailure records a failed tool call.
func (ht *HealthTracker) RecordFailure(serverURL string, latency time.Duration, err error) {
	ht.record(serverURL, callOutcome{success: false, latency: latency, err: err})
}

func (ht *HealthTracker) record(serverURL string, o callOutcome) {
	ht.mu.Lock()
	defer ht.mu.Unlock()
	outcomes := ht.outcomes[serverURL]
	outcomes = append(outcomes, o)
	if len(outcomes) > ht.windowSize {
		outcomes = outcomes[len(outcomes)-ht.windowSize:]
	}
	ht.outcomes[serverURL] = outcomes
}

// Health returns the current health for a single server.
func (ht *HealthTracker) Health(serverURL string) ServerHealth {
	ht.mu.Lock()
	defer ht.mu.Unlock()
	return ht.healthLocked(serverURL)
}

// AllHealth returns health for all tracked servers.
func (ht *HealthTracker) AllHealth() []ServerHealth {
	ht.mu.Lock()
	defer ht.mu.Unlock()
	all := make([]ServerHealth, 0, len(ht.outcomes))
	for url := range ht.outcomes {
		all = append(all, ht.healthLocked(url))
	}
	return all
}

// OverallStatus returns the aggregate health status across all servers.
// Returns "AllHealthy" when all servers are Up and have no failures,
// "Degraded" when some have failures but are still Up,
// "Unreachable" when at least one server has Up=false.
func (ht *HealthTracker) OverallStatus() string {
	ht.mu.Lock()
	defer ht.mu.Unlock()
	if len(ht.outcomes) == 0 {
		return "AllHealthy"
	}
	anyUnreachable := false
	anyErrors := false
	for url := range ht.outcomes {
		h := ht.healthLocked(url)
		if !h.Up {
			anyUnreachable = true
		} else if h.Failures > 0 {
			anyErrors = true
		}
	}
	if anyUnreachable {
		return "Unreachable"
	}
	if anyErrors {
		return "Degraded"
	}
	return "AllHealthy"
}

// healthLocked computes health for a server. Caller must hold ht.mu.
func (ht *HealthTracker) healthLocked(serverURL string) ServerHealth {
	outcomes := ht.outcomes[serverURL]
	h := ServerHealth{
		ServerURL:  serverURL,
		ServerName: ht.names[serverURL],
	}
	if len(outcomes) == 0 {
		h.Up = true // no data = assume healthy
		return h
	}
	var totalLatency time.Duration
	for _, o := range outcomes {
		if o.success {
			h.Successes++
		} else {
			h.Failures++
			h.LastError = o.err
		}
		totalLatency += o.latency
	}
	h.AvgLatencyMs = totalLatency.Milliseconds() / int64(len(outcomes))
	total := h.Successes + h.Failures
	if total > 0 {
		failureRate := float64(h.Failures) / float64(total)
		h.Up = failureRate < 0.8
	}
	return h
}
