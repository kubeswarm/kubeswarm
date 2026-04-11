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
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/kubeswarm/kubeswarm/pkg/agent/config"
)

// defaultPollInterval is the internal fallback when PollIntervalSeconds is not
// configured. In production the operator injects PollIntervalSeconds from the
// CRD (default 300s), so this only applies in tests or direct API usage.
const defaultPollInterval = 100 * time.Millisecond

// RefreshTools re-discovers tools from a single server and atomically swaps
// them into the manager's tool list. Returns the names of added and removed tools.
func (m *Manager) RefreshTools(server config.MCPServerConfig) (added []string, removed []string, err error) {
	conn, ok := m.serverConns[server.URL]
	if !ok {
		return nil, nil, fmt.Errorf("no connection for server %q", server.URL)
	}

	newTools, err := discoverTools(server, conn)
	if err != nil {
		return nil, nil, err
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	// Build sets of old and new tool names for this server.
	oldNames := map[string]bool{}
	newNames := map[string]bool{}

	for _, t := range m.tools {
		if t.ServerURL == server.URL {
			oldNames[t.Name] = true
		}
	}
	for _, t := range newTools {
		newNames[t.Name] = true
	}

	// Compute delta.
	for name := range newNames {
		if !oldNames[name] {
			added = append(added, name)
		}
	}
	for name := range oldNames {
		if !newNames[name] {
			removed = append(removed, name)
		}
	}
	sort.Strings(added)
	sort.Strings(removed)

	// If no changes, skip the swap.
	if len(added) == 0 && len(removed) == 0 {
		return nil, nil, nil
	}

	// Rebuild the full tools list: keep tools from other servers, replace this server's.
	var merged []Tool
	for _, t := range m.tools {
		if t.ServerURL != server.URL {
			merged = append(merged, t)
		}
	}
	merged = append(merged, newTools...)
	m.tools = merged

	return added, removed, nil
}

// StartPolling starts background goroutines that periodically refresh tools
// for the given servers. The caller is responsible for filtering which servers
// should be polled. The poll interval is derived from Discovery.PollIntervalSeconds
// when set, otherwise defaults to 5 minutes.
func (m *Manager) StartPolling(ctx context.Context, servers []config.MCPServerConfig) {
	ctx, m.cancel = context.WithCancel(ctx)
	for _, s := range servers {
		interval := defaultPollInterval
		if s.Discovery != nil && s.Discovery.PollIntervalSeconds > 0 {
			interval = time.Duration(s.Discovery.PollIntervalSeconds) * time.Second
		}
		go m.pollLoop(ctx, s, interval)
	}
}

func (m *Manager) pollLoop(ctx context.Context, server config.MCPServerConfig, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_, _, _ = m.RefreshTools(server)
		}
	}
}

// Stop cancels all background polling goroutines.
func (m *Manager) Stop() {
	if m.cancel != nil {
		m.cancel()
	}
}
