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
	"math"
	"strings"
	"sync"
	"testing"

	"github.com/kubeswarm/kubeswarm/pkg/agent/config"
	"github.com/kubeswarm/kubeswarm/pkg/agent/mcp"
	"github.com/kubeswarm/kubeswarm/pkg/agent/memory"
	"github.com/kubeswarm/kubeswarm/pkg/agent/queue"
)

// inMemoryVectorStore is a simple vector store backed by a slice for testing.
type inMemoryVectorStore struct {
	mu    sync.Mutex
	items []storedItem
}

type storedItem struct {
	id      string
	vector  []float32
	payload map[string]any
}

func (s *inMemoryVectorStore) Upsert(_ context.Context, id string, vector []float32, payload map[string]any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Replace existing item with same ID.
	for i, item := range s.items {
		if item.id == id {
			s.items[i] = storedItem{id: id, vector: vector, payload: payload}
			return nil
		}
	}
	s.items = append(s.items, storedItem{id: id, vector: vector, payload: payload})
	return nil
}

func (s *inMemoryVectorStore) Query(_ context.Context, vector []float32, topK int) ([]memory.QueryResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var results []memory.QueryResult
	for _, item := range s.items {
		score := cosineSimilarity(vector, item.vector)
		results = append(results, memory.QueryResult{
			ID:      item.id,
			Score:   score,
			Payload: item.payload,
		})
	}

	// Sort by score descending (simple bubble sort for tests).
	for i := range results {
		for j := i + 1; j < len(results); j++ {
			if results[j].Score > results[i].Score {
				results[i], results[j] = results[j], results[i]
			}
		}
	}

	if topK < len(results) {
		results = results[:topK]
	}
	return results, nil
}

func (s *inMemoryVectorStore) Delete(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, item := range s.items {
		if item.id == id {
			s.items = append(s.items[:i], s.items[i+1:]...)
			return nil
		}
	}
	return nil
}

func (s *inMemoryVectorStore) Close() error { return nil }

func (s *inMemoryVectorStore) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.items)
}

func cosineSimilarity(a, b []float32) float32 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot, normA, normB float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		normA += float64(a[i]) * float64(a[i])
		normB += float64(b[i]) * float64(b[i])
	}
	denom := math.Sqrt(normA) * math.Sqrt(normB)
	if denom == 0 {
		return 0
	}
	return float32(dot / denom)
}

// embedProvider is a mock that returns deterministic embeddings based on the
// text content. This lets us test that similar queries retrieve similar results.
type embedProvider struct {
	embedCalls int
}

func (p *embedProvider) RunTask(
	_ context.Context,
	_ *config.Config,
	_ queue.Task,
	_ []mcp.Tool,
	_ func(context.Context, string, json.RawMessage) (string, error),
	_ func(string),
) (string, queue.TokenUsage, error) {
	return "", queue.TokenUsage{}, nil
}

func (p *embedProvider) Embed(_ context.Context, text string) ([]float32, error) {
	p.embedCalls++
	return deterministicEmbed(text), nil
}

// deterministicEmbed creates a simple 4-dimensional embedding from text content.
// Similar texts produce similar vectors, enabling cosine similarity to work.
func deterministicEmbed(text string) []float32 {
	// Simple hash-based embedding: count character classes.
	var vowels, consonants, digits, other float32
	for _, c := range strings.ToLower(text) {
		switch {
		case c == 'a' || c == 'e' || c == 'i' || c == 'o' || c == 'u':
			vowels++
		case c >= 'a' && c <= 'z':
			consonants++
		case c >= '0' && c <= '9':
			digits++
		default:
			other++
		}
	}
	total := vowels + consonants + digits + other
	if total == 0 {
		return []float32{0, 0, 0, 0}
	}
	// Normalize to unit vector.
	return []float32{vowels / total, consonants / total, digits / total, other / total}
}

// TestLoopMemoryHook_WriteAndRead covers TEST-TRACKER.md item:
//
//	[x] Memory: vector memory read / write within loop
//
// Behaviour under test: AfterCall stores a tool result embedding in the vector
// store, and a subsequent BeforeCall for a similar query retrieves it as a
// prior-finding block injected into the LLM context.
func TestLoopMemoryHook_WriteAndRead(t *testing.T) {
	vs := &inMemoryVectorStore{}
	prov := &embedProvider{}

	h := &LoopMemoryHook{
		cfg: &config.LoopMemoryConfig{
			Store:                true,
			Retrieve:             true,
			TopK:                 3,
			MinSimilarityPercent: 50, // low threshold for deterministic embeddings
		},
		store:    vs,
		provider: prov,
		embedder: prov,
		taskID:   "task-1",
	}
	defer h.Close()

	ctx := context.Background()

	// Step 1: Store a tool result via AfterCall.
	h.AfterCall(ctx, "search", json.RawMessage(`{"q":"kubernetes pods"}`), "Found 3 running pods in namespace default")

	// Verify the vector store received the upsert.
	if got := vs.count(); got != 1 {
		t.Fatalf("vector store items = %d, want 1", got)
	}

	// Verify the stored payload contains the text and metadata.
	vs.mu.Lock()
	item := vs.items[0]
	vs.mu.Unlock()

	if text, ok := item.payload["text"].(string); !ok || text == "" {
		t.Error("stored payload missing 'text' field")
	}
	if tool, ok := item.payload["tool"].(string); !ok || tool != "search" {
		t.Errorf("stored payload tool = %q, want %q", tool, "search")
	}
	if taskID, ok := item.payload["task_id"].(string); !ok || taskID != "task-1" {
		t.Errorf("stored payload task_id = %q, want %q", taskID, "task-1")
	}

	// Step 2: Retrieve prior findings via BeforeCall with a similar query.
	findings := h.BeforeCall(ctx, "search", json.RawMessage(`{"q":"kubernetes deployments"}`))

	if findings == "" {
		t.Fatal("BeforeCall returned empty findings, expected prior-findings block")
	}
	if !strings.Contains(findings, "<swarm:prior-findings>") {
		t.Errorf("findings missing prior-findings tag: %s", findings)
	}

	// Verify embedder was called: 1 for AfterCall store + 1 for BeforeCall query.
	if prov.embedCalls < 2 {
		t.Errorf("embedCalls = %d, want >= 2", prov.embedCalls)
	}
}

// TestLoopMemoryHook_StoreDisabled verifies that AfterCall is a no-op when
// Store is false, and no data reaches the vector store.
func TestLoopMemoryHook_StoreDisabled(t *testing.T) {
	vs := &inMemoryVectorStore{}
	prov := &embedProvider{}

	h := &LoopMemoryHook{
		cfg: &config.LoopMemoryConfig{
			Store:    false,
			Retrieve: true,
		},
		store:    vs,
		provider: prov,
		embedder: prov,
		taskID:   "task-2",
	}

	h.AfterCall(context.Background(), "tool", json.RawMessage(`{}`), "result")

	if got := vs.count(); got != 0 {
		t.Errorf("vector store items = %d, want 0 (Store disabled)", got)
	}
}

// TestLoopMemoryHook_RetrieveDisabled verifies that BeforeCall returns empty
// when Retrieve is false, even if the store has data.
func TestLoopMemoryHook_RetrieveDisabled(t *testing.T) {
	vs := &inMemoryVectorStore{}
	prov := &embedProvider{}

	h := &LoopMemoryHook{
		cfg: &config.LoopMemoryConfig{
			Store:    true,
			Retrieve: false,
		},
		store:    vs,
		provider: prov,
		embedder: prov,
		taskID:   "task-3",
	}

	// Store something first.
	h.AfterCall(context.Background(), "tool", json.RawMessage(`{"q":"test"}`), "stored data")

	// BeforeCall should return empty since Retrieve is false.
	findings := h.BeforeCall(context.Background(), "tool", json.RawMessage(`{"q":"test"}`))
	if findings != "" {
		t.Errorf("BeforeCall returned %q with Retrieve disabled, want empty", findings)
	}
}

// TestLoopMemoryHook_NilSafe verifies that nil hook methods are no-ops.
func TestLoopMemoryHook_NilSafe(t *testing.T) {
	var h *LoopMemoryHook

	// None of these should panic.
	h.Close()
	h.AfterCall(context.Background(), "tool", json.RawMessage(`{}`), "result")
	findings := h.BeforeCall(context.Background(), "tool", json.RawMessage(`{}`))
	if findings != "" {
		t.Errorf("nil hook BeforeCall = %q, want empty", findings)
	}
}

// TestLoopMemoryHook_MinSimilarityFilters verifies that results below the
// MinSimilarityPercent threshold are filtered out from prior findings.
func TestLoopMemoryHook_MinSimilarityFilters(t *testing.T) {
	vs := &inMemoryVectorStore{}
	prov := &embedProvider{}

	h := &LoopMemoryHook{
		cfg: &config.LoopMemoryConfig{
			Store:                true,
			Retrieve:             true,
			TopK:                 10,
			MinSimilarityPercent: 99, // very high threshold - almost nothing should match
		},
		store:    vs,
		provider: prov,
		embedder: prov,
		taskID:   "task-4",
	}

	ctx := context.Background()

	// Store a result.
	h.AfterCall(ctx, "search_pods", json.RawMessage(`{"ns":"default"}`), "Found 5 pods")

	// Query with very different content - should be below 99% similarity.
	findings := h.BeforeCall(ctx, "deploy_chart", json.RawMessage(`{"chart":"nginx","version":"1.2.3"}`))
	if findings != "" {
		t.Errorf("expected no findings with 99%% similarity threshold, got: %s", findings)
	}
}

// TestInjectPriorFindings verifies the prior-findings injection format.
func TestInjectPriorFindings(t *testing.T) {
	result := injectPriorFindings("<swarm:prior-findings>\nold data\n</swarm:prior-findings>", "new result")
	if !strings.Contains(result, "old data") {
		t.Error("missing prior findings content")
	}
	if !strings.Contains(result, "new result") {
		t.Error("missing tool result")
	}

	// Empty findings should pass through.
	if got := injectPriorFindings("", "tool output"); got != "tool output" {
		t.Errorf("empty findings: got %q, want %q", got, "tool output")
	}
}
