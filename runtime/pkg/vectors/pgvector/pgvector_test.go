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

package pgvector

import (
	"context"
	"fmt"
	"math"
	"os"
	"testing"

	"github.com/kubeswarm/kubeswarm/pkg/agent/memory"
)

// ---------------------------------------------------------------------------
// Compile-time interface compliance
// ---------------------------------------------------------------------------

var _ memory.VectorStore = (*store)(nil)

// ---------------------------------------------------------------------------
// Unit tests - pure functions, no database required
// ---------------------------------------------------------------------------

func TestVectorLiteral(t *testing.T) {
	tests := []struct {
		name string
		in   []float32
		want string
	}{
		{
			name: "empty slice",
			in:   []float32{},
			want: "[]",
		},
		{
			name: "single element",
			in:   []float32{0.5},
			want: "[0.5]",
		},
		{
			name: "multiple elements",
			in:   []float32{0.1, 0.2, 0.3},
			want: "[0.1,0.2,0.3]",
		},
		{
			name: "integers rendered without decimals",
			in:   []float32{1, 2, 3},
			want: "[1,2,3]",
		},
		{
			name: "negative values",
			in:   []float32{-0.5, 0, 0.5},
			want: "[-0.5,0,0.5]",
		},
		{
			name: "very small values",
			in:   []float32{1e-7, 1e-10},
			want: "[0.0000001,0.0000000001]",
		},
		{
			name: "large values",
			in:   []float32{1e+20, -3.14e+10},
			want: "[100000000000000000000,-31400000000]",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := vectorLiteral(tt.in)
			if got != tt.want {
				t.Errorf("vectorLiteral(%v) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestExtractQueryParam(t *testing.T) {
	tests := []struct {
		name   string
		rawURL string
		key    string
		want   string
	}{
		{
			name:   "param present",
			rawURL: "postgres://host:5432/db?sslmode=disable&table=custom",
			key:    "table",
			want:   "custom",
		},
		{
			name:   "param not present",
			rawURL: "postgres://host:5432/db?sslmode=disable",
			key:    "table",
			want:   "",
		},
		{
			name:   "no query string at all",
			rawURL: "postgres://host:5432/db",
			key:    "table",
			want:   "",
		},
		{
			name:   "first param",
			rawURL: "postgres://host/db?table=my_vectors&sslmode=disable",
			key:    "table",
			want:   "my_vectors",
		},
		{
			name:   "empty value",
			rawURL: "postgres://host/db?table=&sslmode=disable",
			key:    "table",
			want:   "",
		},
		{
			name:   "key without equals",
			rawURL: "postgres://host/db?table",
			key:    "table",
			want:   "",
		},
		{
			name:   "multiple question marks in value",
			rawURL: "postgres://host/db?key=val?ue",
			key:    "key",
			want:   "val?ue",
		},
		{
			name:   "extracts sslmode not table",
			rawURL: "postgres://host/db?sslmode=require",
			key:    "sslmode",
			want:   "require",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractQueryParam(tt.rawURL, tt.key)
			if got != tt.want {
				t.Errorf("extractQueryParam(%q, %q) = %q, want %q", tt.rawURL, tt.key, got, tt.want)
			}
		})
	}
}

func TestInitRegistration(t *testing.T) {
	// init() should have registered both "postgres" and "postgresql" schemes.
	// We verify by calling memory.NewVectorStore with a URL that will fail
	// at the connection level but NOT with an "unknown scheme" error.
	// If the scheme were unregistered, NewVectorStore returns an error
	// containing "no VectorStore registered".

	for _, scheme := range []string{"postgres", "postgresql"} {
		t.Run(scheme, func(t *testing.T) {
			// Use an unreachable host so the factory is invoked but connection fails.
			url := fmt.Sprintf("%s://invalid:5432/testdb?connect_timeout=1", scheme)
			_, err := memory.NewVectorStore(url)
			if err == nil {
				t.Fatal("expected connection error, got nil")
			}
			// The error must NOT be "no VectorStore registered" - that would
			// mean the init() registration did not happen.
			errMsg := err.Error()
			if contains(errMsg, "no VectorStore registered") {
				t.Errorf("scheme %q was not registered: %s", scheme, errMsg)
			}
		})
	}
}

func TestValidateVector(t *testing.T) {
	tests := []struct {
		name    string
		vec     []float32
		wantErr bool
	}{
		{name: "valid", vec: []float32{0.1, 0.2, 0.3}, wantErr: false},
		{name: "empty", vec: []float32{}, wantErr: true},
		{name: "contains NaN", vec: []float32{1, float32(math.NaN()), 3}, wantErr: true},
		{name: "contains +Inf", vec: []float32{1, float32(math.Inf(1))}, wantErr: true},
		{name: "contains -Inf", vec: []float32{float32(math.Inf(-1)), 2}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateVector(tt.vec)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateVector(%v) error = %v, wantErr %v", tt.vec, err, tt.wantErr)
			}
		})
	}
}

func TestInvalidTableName(t *testing.T) {
	// Table names with SQL injection characters should be rejected at construction.
	badNames := []string{
		"'; DROP TABLE users; --",
		"table name with spaces",
		"123startswithnumber",
		"",
	}
	for _, name := range badNames {
		t.Run(name, func(t *testing.T) {
			url := "postgres://invalid:5432/db?connect_timeout=1&table=" + name
			_, err := newStore(url)
			if err == nil {
				t.Errorf("expected error for table name %q, got nil", name)
			}
		})
	}
}

func TestQuotedTable(t *testing.T) {
	s := &store{table: "my_table"}
	got := s.quotedTable()
	if got != `"my_table"` {
		t.Errorf("quotedTable() = %q, want %q", got, `"my_table"`)
	}
}

// ---------------------------------------------------------------------------
// Integration tests - require a real Postgres with pgvector
// ---------------------------------------------------------------------------
// Set PGVECTOR_TEST_URL to run, e.g.:
//   PGVECTOR_TEST_URL="postgres://localhost:5432/testdb?sslmode=disable"
//
// Skip with -short flag or by leaving the env var unset.

func integrationStore(t *testing.T, queryParams ...string) *store {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	rawURL := os.Getenv("PGVECTOR_TEST_URL")
	if rawURL == "" {
		t.Skip("PGVECTOR_TEST_URL not set, skipping integration test")
	}
	// Append extra query params if provided (e.g. table=custom).
	for _, qp := range queryParams {
		if containsByte(rawURL, '?') {
			rawURL += "&" + qp
		} else {
			rawURL += "?" + qp
		}
	}
	s, err := newStore(rawURL)
	if err != nil {
		t.Fatalf("newStore: %v", err)
	}
	t.Cleanup(func() {
		// Drop the test table to keep the database clean.
		_, _ = s.pool.Exec(context.Background(), fmt.Sprintf("DROP TABLE IF EXISTS %s", s.table))
		_ = s.Close()
	})
	return s
}

func TestIntegrationUpsertAndQuery(t *testing.T) {
	s := integrationStore(t, "table=test_upsert_query")
	ctx := context.Background()

	vec := []float32{1, 0, 0}
	payload := map[string]any{"key": "value"}

	if err := s.Upsert(ctx, "doc-1", vec, payload); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	results, err := s.Query(ctx, vec, 5)
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}

	r := results[0]
	if r.ID != "doc-1" {
		t.Errorf("ID = %q, want %q", r.ID, "doc-1")
	}
	// Querying with the exact same vector should yield a score very close to 1.0.
	if r.Score < 0.99 {
		t.Errorf("Score = %f, want >= 0.99", r.Score)
	}
	if r.Payload["key"] != "value" {
		t.Errorf("Payload[key] = %v, want %q", r.Payload["key"], "value")
	}
}

func TestIntegrationUpsertOverwrites(t *testing.T) {
	s := integrationStore(t, "table=test_upsert_overwrite")
	ctx := context.Background()

	vec := []float32{1, 0, 0}

	// First upsert.
	if err := s.Upsert(ctx, "doc-1", vec, map[string]any{"version": float64(1)}); err != nil {
		t.Fatalf("Upsert (first): %v", err)
	}
	// Second upsert with same ID, different payload.
	if err := s.Upsert(ctx, "doc-1", vec, map[string]any{"version": float64(2)}); err != nil {
		t.Fatalf("Upsert (second): %v", err)
	}

	results, err := s.Query(ctx, vec, 5)
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result after overwrite, got %d", len(results))
	}
	// JSONB round-trips numbers as float64.
	version, ok := results[0].Payload["version"].(float64)
	if !ok {
		t.Fatalf("Payload[version] type = %T, want float64", results[0].Payload["version"])
	}
	if version != 2 {
		t.Errorf("Payload[version] = %v, want 2", version)
	}
}

func TestIntegrationDelete(t *testing.T) {
	s := integrationStore(t, "table=test_delete")
	ctx := context.Background()

	vec := []float32{0, 1, 0}

	if err := s.Upsert(ctx, "doc-del", vec, map[string]any{"x": "y"}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	if err := s.Delete(ctx, "doc-del"); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	results, err := s.Query(ctx, vec, 5)
	if err != nil {
		t.Fatalf("Query after delete: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("expected 0 results after delete, got %d", len(results))
	}
}

func TestIntegrationDeleteNonexistent(t *testing.T) {
	s := integrationStore(t, "table=test_delete_noexist")
	ctx := context.Background()

	// Ensure table exists by upserting then deleting a dummy doc.
	vec := []float32{0, 0, 1}
	if err := s.Upsert(ctx, "setup", vec, nil); err != nil {
		t.Fatalf("Upsert setup: %v", err)
	}

	// Delete a non-existent ID - should be a no-op, no error.
	if err := s.Delete(ctx, "does-not-exist"); err != nil {
		t.Errorf("Delete non-existent: %v", err)
	}
}

func TestIntegrationQueryTopK(t *testing.T) {
	s := integrationStore(t, "table=test_topk")
	ctx := context.Background()

	// Insert 5 vectors along different axes so they have distinct distances.
	vecs := [][]float32{
		{1, 0, 0, 0, 0},
		{0, 1, 0, 0, 0},
		{0, 0, 1, 0, 0},
		{0, 0, 0, 1, 0},
		{0, 0, 0, 0, 1},
	}
	for i, v := range vecs {
		id := fmt.Sprintf("vec-%d", i)
		if err := s.Upsert(ctx, id, v, map[string]any{"index": float64(i)}); err != nil {
			t.Fatalf("Upsert %s: %v", id, err)
		}
	}

	// Query for top 3 - should return exactly 3.
	results, err := s.Query(ctx, vecs[0], 3)
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(results) != 3 {
		t.Errorf("expected 3 results, got %d", len(results))
	}

	// The first result should be the exact match.
	if len(results) > 0 && results[0].ID != "vec-0" {
		t.Errorf("top result ID = %q, want %q", results[0].ID, "vec-0")
	}

	// Query for top 10 but only 5 exist - should return 5.
	all, err := s.Query(ctx, vecs[0], 10)
	if err != nil {
		t.Fatalf("Query all: %v", err)
	}
	if len(all) != 5 {
		t.Errorf("expected 5 results, got %d", len(all))
	}
}

func TestIntegrationCustomTableName(t *testing.T) {
	customTable := "test_custom_table_name"
	s := integrationStore(t, "table="+customTable)

	if s.table != customTable {
		t.Errorf("table = %q, want %q", s.table, customTable)
	}

	ctx := context.Background()
	vec := []float32{0.5, 0.5, 0.5}
	if err := s.Upsert(ctx, "custom-1", vec, map[string]any{"custom": true}); err != nil {
		t.Fatalf("Upsert into custom table: %v", err)
	}

	// Verify the table was actually created with the custom name.
	var tableName string
	err := s.pool.QueryRow(ctx,
		"SELECT tablename FROM pg_tables WHERE tablename = $1", customTable).Scan(&tableName)
	if err != nil {
		t.Fatalf("custom table not found in pg_tables: %v", err)
	}
	if tableName != customTable {
		t.Errorf("pg_tables tablename = %q, want %q", tableName, customTable)
	}
}

func TestIntegrationClose(t *testing.T) {
	s := integrationStore(t, "table=test_close")
	ctx := context.Background()

	// Upsert to force table creation and verify connectivity.
	vec := []float32{1, 0, 0}
	if err := s.Upsert(ctx, "close-test", vec, nil); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	// Close the store.
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// After close, operations on the pool should fail.
	err := s.Upsert(ctx, "after-close", vec, nil)
	if err == nil {
		t.Error("expected error after Close, got nil")
	}

	// Cleanup: reopen a connection to drop the table since the original pool is closed.
	rawURL := os.Getenv("PGVECTOR_TEST_URL")
	cleanup, err := newStore(rawURL + "&table=test_close")
	if err == nil {
		_, _ = cleanup.pool.Exec(ctx, "DROP TABLE IF EXISTS test_close")
		_ = cleanup.Close()
	}
}

func TestIntegrationQueryScoresOrdered(t *testing.T) {
	s := integrationStore(t, "table=test_score_order")
	ctx := context.Background()

	// Insert vectors with known similarity relationships.
	// "close" is very similar to the query vector; "far" is orthogonal.
	query := []float32{1, 0, 0}
	close := []float32{0.9, 0.1, 0}
	far := []float32{0, 0, 1}

	if err := s.Upsert(ctx, "close", close, nil); err != nil {
		t.Fatalf("Upsert close: %v", err)
	}
	if err := s.Upsert(ctx, "far", far, nil); err != nil {
		t.Fatalf("Upsert far: %v", err)
	}

	results, err := s.Query(ctx, query, 2)
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}

	// Results should be ordered by descending score (ascending distance).
	if results[0].ID != "close" {
		t.Errorf("first result ID = %q, want %q", results[0].ID, "close")
	}
	if results[1].ID != "far" {
		t.Errorf("second result ID = %q, want %q", results[1].ID, "far")
	}
	if results[0].Score <= results[1].Score {
		t.Errorf("expected scores descending: %f <= %f", results[0].Score, results[1].Score)
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func contains(s, substr string) bool {
	return len(s) >= len(substr) && searchString(s, substr)
}

func searchString(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func containsByte(s string, b byte) bool {
	for i := 0; i < len(s); i++ {
		if s[i] == b {
			return true
		}
	}
	return false
}

// approxEqual checks if two float32 values are approximately equal.
// Keeping this helper available for future test expansion.
var _ = approxEqual

func approxEqual(a, b, epsilon float32) bool {
	return float32(math.Abs(float64(a-b))) < epsilon
}
