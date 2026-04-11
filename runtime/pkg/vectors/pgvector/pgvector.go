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

// Package pgvector implements the memory.VectorStore interface backed by
// PostgreSQL with the pgvector extension.
//
// URL format: postgres://host:port/dbname?sslmode=require
//
// Example:
//
//	postgres://postgres.example:5432/mydb?sslmode=disable
//
// The package self-registers with memory.RegisterVectorStore("postgres", ...) via init().
// Blank-import it from the agent binary:
//
//	import _ "github.com/kubeswarm/kubeswarm/runtime/pkg/vectors/pgvector"
//
// On first Upsert the table and pgvector extension are created automatically.
// The vector dimension is inferred from the first embedding.
package pgvector

import (
	"context"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kubeswarm/kubeswarm/pkg/agent/memory"
)

const (
	defaultTable   = "kubeswarm_vectors"
	connectTimeout = 10 * time.Second
)

// validTableName allows only lowercase alphanumeric and underscores, starting
// with a letter or underscore. This prevents SQL injection through the table
// name query parameter.
var validTableName = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]{0,62}$`)

func init() {
	memory.RegisterVectorStore("postgres", func(rawURL string) (memory.VectorStore, error) {
		return newStore(rawURL)
	})
	// Also register postgresql:// scheme for compatibility.
	memory.RegisterVectorStore("postgresql", func(rawURL string) (memory.VectorStore, error) {
		return newStore(rawURL)
	})
}

// store implements memory.VectorStore against PostgreSQL with pgvector.
type store struct {
	pool  *pgxpool.Pool
	table string

	mu     sync.Mutex
	inited bool
	closed bool
}

func newStore(rawURL string) (*store, error) {
	// Use a bounded context for the initial connection rather than
	// context.Background() so a misconfigured DSN fails fast.
	ctx, cancel := context.WithTimeout(context.Background(), connectTimeout)
	defer cancel()

	pool, err := pgxpool.New(ctx, rawURL)
	if err != nil {
		return nil, fmt.Errorf("pgvector: connect: %w", err)
	}

	// Verify connectivity eagerly so callers get a clear error at creation
	// time rather than on the first query.
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("pgvector: ping: %w", err)
	}

	// Extract and validate table name.
	table := defaultTable
	if t := extractQueryParam(rawURL, "table"); t != "" {
		if !validTableName.MatchString(t) {
			pool.Close()
			return nil, fmt.Errorf("pgvector: invalid table name %q: must match %s", t, validTableName.String())
		}
		table = t
	}

	return &store{
		pool:  pool,
		table: table,
	}, nil
}

// quotedTable returns the table name safe for SQL interpolation.
// We use pgx.Identifier quoting (double-quote escaping) as defense-in-depth
// even though the name is already validated by the regex on construction.
func (s *store) quotedTable() string {
	// Standard SQL identifier quoting: replace any embedded " with "" and wrap.
	escaped := strings.ReplaceAll(s.table, `"`, `""`)
	return `"` + escaped + `"`
}

// ensureInit runs ensureTable on the first call. Unlike sync.Once, this
// retries on failure so a transient error during init does not permanently
// brick the store.
func (s *store) ensureInit(ctx context.Context, dim int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.inited {
		return nil
	}
	if err := s.ensureTable(ctx, dim); err != nil {
		return fmt.Errorf("pgvector: init table: %w", err)
	}
	s.inited = true
	return nil
}

// Upsert stores a document with its embedding vector.
// On the first call the pgvector extension and table are created lazily
// using the vector dimension inferred from the embedding.
func (s *store) Upsert(ctx context.Context, id string, vector []float32, payload map[string]any) error {
	if err := validateVector(vector); err != nil {
		return err
	}
	if err := s.ensureInit(ctx, len(vector)); err != nil {
		return err
	}

	query := fmt.Sprintf(`INSERT INTO %s (id, embedding, payload)
		VALUES ($1, $2, $3)
		ON CONFLICT (id) DO UPDATE SET embedding = $2, payload = $3`, s.quotedTable())

	_, err := s.pool.Exec(ctx, query, id, vectorLiteral(vector), payload)
	if err != nil {
		return fmt.Errorf("pgvector: upsert: %w", err)
	}
	return nil
}

// Query returns the top-k most similar documents using cosine distance (<=>).
func (s *store) Query(ctx context.Context, vector []float32, topK int) ([]memory.QueryResult, error) {
	if err := validateVector(vector); err != nil {
		return nil, err
	}

	tbl := s.quotedTable()
	query := fmt.Sprintf(`SELECT id, 1 - (embedding <=> $1) AS score, payload
		FROM %s
		ORDER BY embedding <=> $1
		LIMIT $2`, tbl)

	rows, err := s.pool.Query(ctx, query, vectorLiteral(vector), topK)
	if err != nil {
		return nil, fmt.Errorf("pgvector: query: %w", err)
	}
	defer rows.Close()

	results := make([]memory.QueryResult, 0, topK)
	for rows.Next() {
		var r memory.QueryResult
		if err := rows.Scan(&r.ID, &r.Score, &r.Payload); err != nil {
			return nil, fmt.Errorf("pgvector: scan row: %w", err)
		}
		results = append(results, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("pgvector: iterate rows: %w", err)
	}
	return results, nil
}

// Delete removes a document by ID. No-op if not found.
func (s *store) Delete(ctx context.Context, id string) error {
	query := fmt.Sprintf(`DELETE FROM %s WHERE id = $1`, s.quotedTable())
	_, err := s.pool.Exec(ctx, query, id)
	if err != nil {
		return fmt.Errorf("pgvector: delete: %w", err)
	}
	return nil
}

// Close releases the connection pool. Safe to call multiple times.
func (s *store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	s.pool.Close()
	return nil
}

// ensureTable creates the pgvector extension and table if they do not exist.
// dim is the vector dimension inferred from the first upsert.
func (s *store) ensureTable(ctx context.Context, dim int) error {
	tbl := s.quotedTable()

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `CREATE EXTENSION IF NOT EXISTS vector`); err != nil {
		return fmt.Errorf("create extension: %w", err)
	}

	createSQL := fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
		id TEXT PRIMARY KEY,
		embedding vector(%d) NOT NULL,
		payload JSONB DEFAULT '{}'
	)`, tbl, dim)
	if _, err := tx.Exec(ctx, createSQL); err != nil {
		return fmt.Errorf("create table: %w", err)
	}

	// Use HNSW instead of ivfflat. HNSW does not require training data so it
	// works on empty tables, and generally provides better recall at comparable
	// speed. This eliminates the convoluted fallback path that ivfflat needed.
	indexSQL := fmt.Sprintf(`CREATE INDEX IF NOT EXISTS %s_embedding_idx
		ON %s USING hnsw (embedding vector_cosine_ops)`, tbl+"_embedding_idx", tbl)
	if _, err := tx.Exec(ctx, indexSQL); err != nil {
		return fmt.Errorf("create index: %w", err)
	}

	return tx.Commit(ctx)
}

// validateVector rejects empty vectors and vectors containing NaN or Inf
// values, which pgvector cannot store.
func validateVector(v []float32) error {
	if len(v) == 0 {
		return fmt.Errorf("pgvector: empty vector")
	}
	for i, f := range v {
		if math.IsNaN(float64(f)) || math.IsInf(float64(f), 0) {
			return fmt.Errorf("pgvector: invalid float at index %d: %v", i, f)
		}
	}
	return nil
}

// vectorLiteral converts a float32 slice to pgvector's text format: [0.1,0.2,0.3]
func vectorLiteral(v []float32) string {
	var b strings.Builder
	// Pre-allocate: ~12 chars per float is a reasonable estimate.
	b.Grow(2 + len(v)*12)
	b.WriteByte('[')
	for i, f := range v {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(strconv.FormatFloat(float64(f), 'f', -1, 32))
	}
	b.WriteByte(']')
	return b.String()
}

// extractQueryParam extracts a query parameter from a raw URL string.
// Returns empty string if not found. Does not use net/url to avoid
// stripping the scheme that pgx needs.
func extractQueryParam(rawURL, key string) string {
	_, after, ok := strings.Cut(rawURL, "?")
	if !ok {
		return ""
	}
	query := after
	for pair := range strings.SplitSeq(query, "&") {
		k, v, _ := strings.Cut(pair, "=")
		if k == key {
			return v
		}
	}
	return ""
}

// Ensure store implements the interface at compile time.
var _ memory.VectorStore = (*store)(nil)
