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

package healthz

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// --- Mock probe ---

type mockProbe struct {
	err error
	mu  sync.Mutex
	ctx context.Context // captures the context passed to Check
}

func (m *mockProbe) Check(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ctx = ctx
	return m.err
}

func (m *mockProbe) lastCtx() context.Context {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.ctx
}

// --- Tests ---

func TestChecker_HealthyProbe_ReturnsNil(t *testing.T) {
	t.Parallel()

	probe := &mockProbe{err: nil}
	c := NewChecker(RoleQueue, probe)

	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	if err := c.Check(req); err != nil {
		t.Errorf("Check() = %v, want nil", err)
	}
}

func TestChecker_UnhealthyProbe_ReturnsError(t *testing.T) {
	t.Parallel()

	wantErr := errors.New("connection refused")
	probe := &mockProbe{err: wantErr}
	c := NewChecker(RoleStream, probe)

	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	err := c.Check(req)
	if err == nil {
		t.Fatal("Check() = nil, want error")
	}
	if !errors.Is(err, wantErr) {
		t.Errorf("Check() = %v, want %v", err, wantErr)
	}
}

func TestChecker_Name_ReturnsRole(t *testing.T) {
	t.Parallel()

	tests := []struct {
		role Role
		want string
	}{
		{RoleQueue, "queue"},
		{RoleStream, "stream"},
		{RoleSpend, "spend"},
		{RoleAudit, "audit"},
	}

	for _, tt := range tests {
		t.Run(string(tt.role), func(t *testing.T) {
			t.Parallel()
			probe := &mockProbe{}
			c := NewChecker(tt.role, probe)
			if got := c.Name(); got != tt.want {
				t.Errorf("Name() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestChecker_PassesRequestContextToProbe(t *testing.T) {
	t.Parallel()

	type ctxKey string
	key := ctxKey("test-key")

	probe := &mockProbe{}
	c := NewChecker(RoleQueue, probe)

	ctx := context.WithValue(context.Background(), key, "test-value")
	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	req = req.WithContext(ctx)

	_ = c.Check(req)

	got := probe.lastCtx()
	if got == nil {
		t.Fatal("probe did not receive a context")
	}
	if got.Value(key) != "test-value" {
		t.Error("probe context does not contain the request's context value")
	}
}

func TestChecker_NilProbe_ReturnsError(t *testing.T) {
	t.Parallel()

	defer func() {
		if r := recover(); r != nil {
			// Panic is an acceptable defensive behavior for nil probe
			return
		}
	}()

	c := NewChecker(RoleQueue, nil)
	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	err := c.Check(req)

	// If it didn't panic, it should return an error
	if err == nil {
		t.Error("Check() with nil probe should return error or panic")
	}
}

func TestChecker_ConcurrentCheckCalls(t *testing.T) {
	t.Parallel()

	probe := &mockProbe{}
	c := NewChecker(RoleQueue, probe)

	const goroutines = 50
	var wg sync.WaitGroup
	wg.Add(goroutines)

	errs := make(chan error, goroutines)
	for range goroutines {
		go func() {
			defer wg.Done()
			req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
			errs <- c.Check(req)
		}()
	}
	wg.Wait()
	close(errs)

	for err := range errs {
		if err != nil {
			t.Errorf("concurrent Check() returned unexpected error: %v", err)
		}
	}
}
