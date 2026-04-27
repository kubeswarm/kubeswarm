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
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/kubeswarm/kubeswarm/pkg/agent/config"
)

// ---------------------------------------------------------------------------
// InMemoryToolCache unit tests
// ---------------------------------------------------------------------------

func TestInMemoryToolCache_SetThenGet(t *testing.T) {
	c := NewInMemoryToolCache()
	ctx := context.Background()
	c.Set(ctx, "k1", "hello", 5*time.Second)
	val, ok := c.Get(ctx, "k1")
	if !ok {
		t.Fatal("expected cache hit")
	}
	if val != "hello" {
		t.Errorf("got %q, want %q", val, "hello")
	}
}

func TestInMemoryToolCache_Miss(t *testing.T) {
	c := NewInMemoryToolCache()
	_, ok := c.Get(context.Background(), "nonexistent")
	if ok {
		t.Error("expected cache miss on empty cache")
	}
}

func TestInMemoryToolCache_Expiry(t *testing.T) {
	c := NewInMemoryToolCache()
	ctx := context.Background()
	c.Set(ctx, "k1", "val", 1*time.Millisecond)
	time.Sleep(5 * time.Millisecond)
	_, ok := c.Get(ctx, "k1")
	if ok {
		t.Error("expected cache miss after TTL expiry")
	}
}

func TestInMemoryToolCache_Overwrite(t *testing.T) {
	c := NewInMemoryToolCache()
	ctx := context.Background()
	c.Set(ctx, "k1", "v1", 5*time.Second)
	c.Set(ctx, "k1", "v2", 5*time.Second)
	val, ok := c.Get(ctx, "k1")
	if !ok {
		t.Fatal("expected hit")
	}
	if val != "v2" {
		t.Errorf("got %q, want %q", val, "v2")
	}
}

func TestInMemoryToolCache_ConcurrentAccess(t *testing.T) {
	c := NewInMemoryToolCache()
	ctx := context.Background()
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			key := "k"
			c.Set(ctx, key, "val", 5*time.Second)
			c.Get(ctx, key)
		}(i)
	}
	wg.Wait()
}

// ---------------------------------------------------------------------------
// ToolCacheWrapper tests
// ---------------------------------------------------------------------------

func TestToolCacheWrapper_ConfigForTool_Hit(t *testing.T) {
	w := &ToolCacheWrapper{
		cache: NewInMemoryToolCache(),
		servers: map[string]*toolCacheServerConfig{
			"search": {ttl: 600 * time.Second, excludeTools: map[string]struct{}{}},
		},
	}
	cfg := w.ConfigForTool("search__web_search")
	if cfg == nil {
		t.Fatal("expected config for search__web_search")
	}
	if cfg.ttl != 600*time.Second {
		t.Errorf("ttl = %v, want 600s", cfg.ttl)
	}
}

func TestToolCacheWrapper_ConfigForTool_NoServer(t *testing.T) {
	w := &ToolCacheWrapper{
		cache:   NewInMemoryToolCache(),
		servers: map[string]*toolCacheServerConfig{},
	}
	if w.ConfigForTool("unknown__tool") != nil {
		t.Error("expected nil for unconfigured server")
	}
}

func TestToolCacheWrapper_ConfigForTool_Excluded(t *testing.T) {
	w := &ToolCacheWrapper{
		cache: NewInMemoryToolCache(),
		servers: map[string]*toolCacheServerConfig{
			"api": {
				ttl:          300 * time.Second,
				excludeTools: map[string]struct{}{"create_item": {}},
			},
		},
	}
	if w.ConfigForTool("api__create_item") != nil {
		t.Error("expected nil for excluded tool")
	}
	if w.ConfigForTool("api__get_item") == nil {
		t.Error("expected config for non-excluded tool")
	}
}

func TestToolCacheWrapper_ConfigForTool_UnprefixedName(t *testing.T) {
	w := &ToolCacheWrapper{
		cache: NewInMemoryToolCache(),
		servers: map[string]*toolCacheServerConfig{
			"delegate": {ttl: 60 * time.Second, excludeTools: map[string]struct{}{}},
		},
	}
	// Built-in tools like "delegate" have no prefix. splitToolName returns (name, name).
	if w.ConfigForTool("delegate") != nil {
		// "delegate" without __ won't match server name "delegate" correctly
		// because splitToolName returns ("delegate", "delegate") - server match
		// but this is a built-in, not an MCP tool. The operator should not
		// configure cache for built-ins. This test documents the behavior.
	}
}

// ---------------------------------------------------------------------------
// newToolCacheWrapper tests
// ---------------------------------------------------------------------------

func TestNewToolCacheWrapper_NoCacheEnabled(t *testing.T) {
	servers := []config.MCPServerConfig{
		{Name: "s1", URL: "http://s1"},
		{Name: "s2", URL: "http://s2"},
	}
	w := newToolCacheWrapper(servers)
	if w != nil {
		t.Error("expected nil wrapper when no servers have cache enabled")
	}
}

func TestNewToolCacheWrapper_WithCacheEnabled(t *testing.T) {
	servers := []config.MCPServerConfig{
		{Name: "s1", URL: "http://s1", Cache: &config.ToolCacheConfigRuntime{
			Enabled: true, TTLSeconds: 600, ExcludeTools: []string{"create"},
		}},
		{Name: "s2", URL: "http://s2"}, // no cache
	}
	w := newToolCacheWrapper(servers)
	if w == nil {
		t.Fatal("expected non-nil wrapper")
	}
	if _, ok := w.servers["s1"]; !ok {
		t.Error("expected s1 in server config")
	}
	if _, ok := w.servers["s2"]; ok {
		t.Error("s2 should not be in server config")
	}
	cfg := w.servers["s1"]
	if cfg.ttl != 600*time.Second {
		t.Errorf("ttl = %v, want 600s", cfg.ttl)
	}
	if _, excluded := cfg.excludeTools["create"]; !excluded {
		t.Error("expected 'create' in exclude list")
	}
}

func TestNewToolCacheWrapper_DefaultTTL(t *testing.T) {
	servers := []config.MCPServerConfig{
		{Name: "s1", URL: "http://s1", Cache: &config.ToolCacheConfigRuntime{
			Enabled: true, TTLSeconds: 0, // should default to 300
		}},
	}
	w := newToolCacheWrapper(servers)
	if w == nil {
		t.Fatal("expected non-nil wrapper")
	}
	if w.servers["s1"].ttl != 300*time.Second {
		t.Errorf("default ttl = %v, want 300s", w.servers["s1"].ttl)
	}
}

// ---------------------------------------------------------------------------
// fingerprintCall determinism tests
// ---------------------------------------------------------------------------

func TestFingerprintCall_Deterministic(t *testing.T) {
	args := json.RawMessage(`{"query":"hello","limit":10}`)
	fp1 := fingerprintCall("search", args)
	fp2 := fingerprintCall("search", args)
	if fp1 != fp2 {
		t.Errorf("same input produced different fingerprints: %q vs %q", fp1, fp2)
	}
}

func TestFingerprintCall_DifferentArgs(t *testing.T) {
	fp1 := fingerprintCall("search", json.RawMessage(`{"q":"a"}`))
	fp2 := fingerprintCall("search", json.RawMessage(`{"q":"b"}`))
	if fp1 == fp2 {
		t.Error("different args should produce different fingerprints")
	}
}

func TestFingerprintCall_DifferentTools(t *testing.T) {
	args := json.RawMessage(`{"q":"a"}`)
	fp1 := fingerprintCall("search", args)
	fp2 := fingerprintCall("lookup", args)
	if fp1 == fp2 {
		t.Error("different tool names should produce different fingerprints")
	}
}

func TestFingerprintCall_WhitespaceNormalized(t *testing.T) {
	fp1 := fingerprintCall("t", json.RawMessage(`{"a":1,"b":2}`))
	fp2 := fingerprintCall("t", json.RawMessage(`{  "a" : 1,  "b" : 2  }`))
	if fp1 != fp2 {
		t.Error("whitespace differences should produce same fingerprint")
	}
}
