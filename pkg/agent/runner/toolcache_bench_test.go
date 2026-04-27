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
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kubeswarm/kubeswarm/pkg/agent/config"
)

// TestToolCache_LatencyAndHitRate measures real latency savings and MCP call reduction.
// This uses an httptest MCP server with configurable latency to simulate real conditions.
func TestToolCache_LatencyAndHitRate(t *testing.T) {
	const (
		simulatedLatency = 50 * time.Millisecond // realistic MCP server latency
		totalCalls       = 20                     // total tool calls
		uniqueCalls      = 4                      // unique arg combinations
		cacheTTL         = 60                     // seconds
	)

	// Track how many times the MCP server is actually called.
	var serverHits atomic.Int64

	// MCP mock server with simulated latency.
	mcp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		serverHits.Add(1)
		time.Sleep(simulatedLatency)
		var req struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		resp := fmt.Sprintf(`{"content":[{"type":"text","text":"result for %s"}]}`, string(req.Arguments))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(resp))
	}))
	defer mcp.Close()

	// Build call args: 20 calls cycling through 4 unique inputs.
	type call struct {
		name  string
		input json.RawMessage
	}
	calls := make([]call, totalCalls)
	for i := range calls {
		key := fmt.Sprintf(`{"key":"project-%d"}`, i%uniqueCalls)
		calls[i] = call{name: "demo__get_result", input: json.RawMessage(key)}
	}

	// callMCP simulates what the agent runner does: POST to MCP server.
	callMCP := func(ctx context.Context, toolName string, input json.RawMessage) (string, error) {
		body, _ := json.Marshal(map[string]any{"name": "get_result", "arguments": input})
		req, _ := http.NewRequestWithContext(ctx, http.MethodPost, mcp.URL+"/tools/call", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return "", err
		}
		defer resp.Body.Close()
		var result struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&result)
		if len(result.Content) > 0 {
			return result.Content[0].Text, nil
		}
		return "", nil
	}

	ctx := context.Background()

	// --- Run WITHOUT cache ---
	serverHits.Store(0)
	startNo := time.Now()
	for _, c := range calls {
		_, err := callMCP(ctx, c.name, c.input)
		if err != nil {
			t.Fatalf("uncached call failed: %v", err)
		}
	}
	durationNo := time.Since(startNo)
	hitsNo := serverHits.Load()

	// --- Run WITH cache ---
	serverHits.Store(0)
	wrapper := newToolCacheWrapper([]config.MCPServerConfig{
		{Name: "demo", URL: mcp.URL, Cache: &config.ToolCacheConfigRuntime{
			Enabled: true, TTLSeconds: cacheTTL,
		}},
	})

	startWith := time.Now()
	for _, c := range calls {
		cacheKey := fingerprintCall(c.name, c.input)
		if cached, ok := wrapper.cache.Get(ctx, cacheKey); ok {
			_ = cached // cache hit
			continue
		}
		result, err := callMCP(ctx, c.name, c.input)
		if err != nil {
			t.Fatalf("cached call failed: %v", err)
		}
		wrapper.cache.Set(ctx, cacheKey, result, time.Duration(cacheTTL)*time.Second)
	}
	durationWith := time.Since(startWith)
	hitsWith := serverHits.Load()

	// --- Report ---
	cacheHits := int64(totalCalls) - hitsWith
	savingPct := float64(durationNo-durationWith) / float64(durationNo) * 100

	t.Logf("")
	t.Logf("=== Tool Result Cache Benchmark ===")
	t.Logf("Total calls:        %d (%d unique args)", totalCalls, uniqueCalls)
	t.Logf("Simulated latency:  %v per MCP call", simulatedLatency)
	t.Logf("")
	t.Logf("WITHOUT cache:")
	t.Logf("  MCP server hits:  %d", hitsNo)
	t.Logf("  Total latency:    %v", durationNo)
	t.Logf("  Avg per call:     %v", durationNo/time.Duration(totalCalls))
	t.Logf("")
	t.Logf("WITH cache:")
	t.Logf("  MCP server hits:  %d (saved %d calls)", hitsWith, hitsNo-hitsWith)
	t.Logf("  Cache hits:       %d", cacheHits)
	t.Logf("  Total latency:    %v", durationWith)
	t.Logf("  Avg per call:     %v", durationWith/time.Duration(totalCalls))
	t.Logf("")
	t.Logf("SAVINGS:")
	t.Logf("  Latency reduction: %.1f%%", savingPct)
	t.Logf("  API calls saved:   %.1f%% (%d/%d)", float64(hitsNo-hitsWith)/float64(hitsNo)*100, hitsNo-hitsWith, hitsNo)

	// Assert meaningful savings.
	if hitsWith >= hitsNo {
		t.Errorf("cache should reduce MCP hits: got %d with cache vs %d without", hitsWith, hitsNo)
	}
	if durationWith >= durationNo {
		t.Errorf("cache should reduce latency: got %v with cache vs %v without", durationWith, durationNo)
	}
}
