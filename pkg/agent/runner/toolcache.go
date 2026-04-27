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

package runner

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/kubeswarm/kubeswarm/pkg/agent/config"
)

// ToolResultCache is the interface for caching tool call results (RFC-0038).
// Implementations must be safe for concurrent use.
type ToolResultCache interface {
	Get(ctx context.Context, key string) (string, bool)
	Set(ctx context.Context, key string, value string, ttl time.Duration)
}

// InMemoryToolCache is a simple in-memory cache with TTL expiration.
// Suitable for single-pod agents. For shared caching across replicas,
// plug in a Redis-backed implementation of ToolResultCache.
type InMemoryToolCache struct {
	mu      sync.RWMutex
	entries map[string]cacheEntry
}

type cacheEntry struct {
	value     string
	expiresAt time.Time
}

// NewInMemoryToolCache creates a new in-memory tool result cache.
func NewInMemoryToolCache() *InMemoryToolCache {
	return &InMemoryToolCache{entries: make(map[string]cacheEntry)}
}

// Get returns a cached result if present and not expired.
func (c *InMemoryToolCache) Get(_ context.Context, key string) (string, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	e, ok := c.entries[key]
	if !ok || time.Now().After(e.expiresAt) {
		return "", false
	}
	return e.value, true
}

// Set stores a result with the given TTL.
func (c *InMemoryToolCache) Set(_ context.Context, key string, value string, ttl time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[key] = cacheEntry{value: value, expiresAt: time.Now().Add(ttl)}
}

// ToolCacheWrapper holds a cache instance and per-server configuration (RFC-0038).
// It resolves which MCP server a tool belongs to by splitting the prefixed tool name.
type ToolCacheWrapper struct {
	cache   ToolResultCache
	servers map[string]*toolCacheServerConfig // keyed by MCP server name
}

type toolCacheServerConfig struct {
	ttl          time.Duration
	excludeTools map[string]struct{}
}

// ConfigForTool returns the cache config for the given prefixed tool name,
// or nil if caching is not enabled for that tool's server or the tool is excluded.
func (w *ToolCacheWrapper) ConfigForTool(toolName string) *toolCacheServerConfig {
	// Tool names are prefixed: "<server_name>__<original_tool_name>"
	serverName, originalName := splitToolName(toolName)
	cfg, ok := w.servers[serverName]
	if !ok {
		return nil
	}
	if _, excluded := cfg.excludeTools[originalName]; excluded {
		return nil
	}
	return cfg
}

// splitToolName splits a prefixed tool name into server name and original name.
func splitToolName(toolName string) (string, string) {
	parts := strings.SplitN(toolName, "__", 2)
	if len(parts) != 2 {
		return toolName, toolName
	}
	return parts[0], parts[1]
}

// newToolCacheWrapper builds a ToolCacheWrapper from MCP server configs.
// Returns nil if no servers have caching enabled.
func newToolCacheWrapper(servers []config.MCPServerConfig) *ToolCacheWrapper {
	cfgs := make(map[string]*toolCacheServerConfig)
	for _, s := range servers {
		if s.Cache == nil || !s.Cache.Enabled {
			continue
		}
		ttl := time.Duration(s.Cache.TTLSeconds) * time.Second
		if ttl <= 0 {
			ttl = 300 * time.Second // default 5 minutes
		}
		excludes := make(map[string]struct{}, len(s.Cache.ExcludeTools))
		for _, t := range s.Cache.ExcludeTools {
			excludes[t] = struct{}{}
		}
		cfgs[s.Name] = &toolCacheServerConfig{ttl: ttl, excludeTools: excludes}
	}
	if len(cfgs) == 0 {
		return nil
	}
	return &ToolCacheWrapper{
		cache:   NewInMemoryToolCache(),
		servers: cfgs,
	}
}
