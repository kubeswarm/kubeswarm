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

// Package qdrant implements the memory.VectorStore interface backed by Qdrant
// using its REST API (port 6333). No Go SDK required.
//
// URL format: qdrant://host:port/collection-name
//
// Example:
//
//	qdrant://qdrant.qdrant.svc.cluster.local:6333/agent-memories
//
// The package self-registers with memory.RegisterVectorStore("qdrant", ...) via init().
// Blank-import it from the agent binary:
//
//	import _ "github.com/kubeswarm/kubeswarm/runtime/pkg/vectors/qdrant"
package qdrant

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/kubeswarm/kubeswarm/pkg/agent/memory"
)

func init() {
	memory.RegisterVectorStore("qdrant", func(rawURL string) (memory.VectorStore, error) {
		return newStore(rawURL)
	})
}

// store implements memory.VectorStore against Qdrant REST API.
type store struct {
	baseURL    string // e.g. http://qdrant:6333
	collection string
	client     *http.Client

	once sync.Once // lazy collection creation
}

func newStore(rawURL string) (*store, error) {
	// Parse qdrant://host:port/collection
	without := strings.TrimPrefix(rawURL, "qdrant://")
	idx := strings.IndexByte(without, '/')
	if idx < 0 {
		return nil, fmt.Errorf("qdrant: URL must contain a collection path: %s", rawURL)
	}
	hostPort := without[:idx]
	collection := strings.TrimPrefix(without[idx:], "/")
	if collection == "" {
		return nil, fmt.Errorf("qdrant: collection name is empty in URL: %s", rawURL)
	}
	return &store{
		baseURL:    "http://" + hostPort,
		collection: collection,
		client:     &http.Client{Timeout: 15 * time.Second},
	}, nil
}

// pointID converts an arbitrary string key to a uint64 suitable for Qdrant point IDs.
// Qdrant only accepts unsigned integers or UUIDs; FNV-64a gives a fast, deterministic mapping.
func pointID(s string) uint64 {
	h := fnv.New64a()
	h.Write([]byte(s))
	return h.Sum64()
}

// Upsert stores a document with its embedding vector.
// On the first call the collection is created lazily using the vector dimension.
// The string id is converted to a uint64 via FNV-64a to satisfy Qdrant's point ID format.
func (s *store) Upsert(ctx context.Context, id string, vector []float32, payload map[string]any) error {
	var initErr error
	s.once.Do(func() {
		initErr = s.ensureCollection(ctx, len(vector))
	})
	if initErr != nil {
		return fmt.Errorf("qdrant: create collection: %w", initErr)
	}

	body := map[string]any{
		"points": []map[string]any{
			{
				"id":      pointID(id),
				"vector":  vector,
				"payload": payload,
			},
		},
	}
	return s.do(ctx, http.MethodPut,
		fmt.Sprintf("/collections/%s/points", s.collection), body, nil)
}

// Query returns the top-k most similar documents.
func (s *store) Query(ctx context.Context, vector []float32, topK int) ([]memory.QueryResult, error) {
	body := map[string]any{
		"vector":       vector,
		"limit":        topK,
		"with_payload": true,
	}
	var resp struct {
		Result []struct {
			// Qdrant returns numeric IDs (uint64) when the point was stored with one.
			// Use json.Number to accept both numeric and string forms without type errors.
			ID      json.Number    `json:"id"`
			Score   float32        `json:"score"`
			Payload map[string]any `json:"payload"`
		} `json:"result"`
	}
	if err := s.do(ctx, http.MethodPost,
		fmt.Sprintf("/collections/%s/points/search", s.collection), body, &resp); err != nil {
		return nil, err
	}
	out := make([]memory.QueryResult, 0, len(resp.Result))
	for _, r := range resp.Result {
		out = append(out, memory.QueryResult{
			ID:      r.ID.String(),
			Score:   r.Score,
			Payload: r.Payload,
		})
	}
	return out, nil
}

// Delete removes a document by ID. No-op if not found.
func (s *store) Delete(ctx context.Context, id string) error {
	body := map[string]any{
		"points": []uint64{pointID(id)},
	}
	return s.do(ctx, http.MethodPost,
		fmt.Sprintf("/collections/%s/points/delete", s.collection), body, nil)
}

// Close is a no-op for the HTTP client.
func (s *store) Close() error { return nil }

// ensureCollection creates the Qdrant collection if it does not exist.
// dim is the vector dimension inferred from the first upsert.
func (s *store) ensureCollection(ctx context.Context, dim int) error {
	// Check if collection exists first.
	var info map[string]any
	err := s.do(ctx, http.MethodGet,
		fmt.Sprintf("/collections/%s", s.collection), nil, &info)
	if err == nil {
		return nil // already exists
	}

	// Create it.
	body := map[string]any{
		"vectors": map[string]any{
			"size":     dim,
			"distance": "Cosine",
		},
	}
	return s.do(ctx, http.MethodPut,
		fmt.Sprintf("/collections/%s", s.collection), body, nil)
}

// do performs an HTTP request against the Qdrant REST API.
func (s *store) do(ctx context.Context, method, path string, body, result any) error {
	var bodyReader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("qdrant: marshal request: %w", err)
		}
		bodyReader = bytes.NewReader(b)
	}

	req, err := http.NewRequestWithContext(ctx, method, s.baseURL+path, bodyReader)
	if err != nil {
		return fmt.Errorf("qdrant: build request: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("qdrant: request %s %s: %w", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("qdrant: read response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("qdrant: %s %s returned %d: %s", method, path, resp.StatusCode, raw)
	}
	if result != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, result); err != nil {
			return fmt.Errorf("qdrant: unmarshal response: %w", err)
		}
	}
	return nil
}
