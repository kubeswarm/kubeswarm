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

// Package memory defines the VectorStore interface and a URL-scheme-based
// registry for vector memory backends.
//
// Backends register themselves via init():
//
//	func init() {
//	    memory.RegisterVectorStore("qdrant", func(url string) (memory.VectorStore, error) {
//	        return newQdrantStore(url)
//	    })
//	}
//
// The runtime selects a backend by calling NewVectorStore with the URL from
// AGENT_VECTOR_STORE_URL (e.g. "qdrant://qdrant.svc:6334/agent-memories").
package memory

import (
	"context"
	"fmt"
	"strings"
	"sync"
)

// VectorStore is the interface that vector memory backends must satisfy.
// Implementations are registered by URL scheme via RegisterVectorStore.
type VectorStore interface {
	// Upsert stores a document with its embedding vector.
	// id is a stable key (e.g. task ID + step name). vector is the embedding.
	Upsert(ctx context.Context, id string, vector []float32, payload map[string]any) error

	// Query returns the top-k most similar documents to the given vector.
	Query(ctx context.Context, vector []float32, topK int) ([]QueryResult, error)

	// Delete removes a document by ID. No-op if not found.
	Delete(ctx context.Context, id string) error

	// Close releases resources held by the backend.
	Close() error
}

// QueryResult is a single result returned from VectorStore.Query.
type QueryResult struct {
	ID      string
	Score   float32
	Payload map[string]any
}

// Factory creates a VectorStore from a connection URL.
type Factory func(url string) (VectorStore, error)

var (
	mu       sync.RWMutex
	registry = map[string]Factory{}
)

// RegisterVectorStore registers a factory for the given URL scheme.
// Call from an init() function in each backend package.
func RegisterVectorStore(scheme string, factory Factory) {
	mu.Lock()
	defer mu.Unlock()
	registry[scheme] = factory
}

// NewVectorStore creates a VectorStore from a connection URL.
// Returns an error if the URL scheme has no registered factory.
//
// Example URLs:
//
//	qdrant://qdrant.svc:6334/agent-memories
//	pinecone://index.svc.pinecone.io?apiKey=...
//	weaviate://weaviate.svc:8080/AgentMemory
func NewVectorStore(url string) (VectorStore, error) {
	scheme := schemeOf(url)
	mu.RLock()
	factory, ok := registry[scheme]
	mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("no VectorStore registered for scheme %q (url: %s); import the backend package to register it", scheme, url)
	}
	return factory(url)
}

// schemeOf returns the URL scheme (the part before "://").
// Returns an empty string for URLs without a scheme.
func schemeOf(url string) string {
	if before, _, ok := strings.Cut(url, "://"); ok {
		return before
	}
	return ""
}
