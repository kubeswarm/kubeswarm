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

package controller

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	kubeswarmv1alpha1 "github.com/kubeswarm/kubeswarm/api/v1alpha1"
)

var (
	ctx       context.Context
	cancel    context.CancelFunc
	testEnv   *envtest.Environment
	cfg       *rest.Config
	k8sClient client.Client
)

func TestMain(m *testing.M) {
	logf.SetLogger(zap.New(zap.WriteTo(os.Stderr), zap.UseDevMode(true)))

	ctx, cancel = context.WithCancel(context.TODO())

	if err := kubeswarmv1alpha1.AddToScheme(scheme.Scheme); err != nil {
		fmt.Fprintf(os.Stderr, "failed to add scheme: %v\n", err)
		os.Exit(1)
	}

	testEnv = &envtest.Environment{
		CRDDirectoryPaths:     []string{filepath.Join("..", "..", "config", "crd", "bases")},
		ErrorIfCRDPathMissing: true,
	}

	// Retrieve the first found binary directory to allow running tests from IDEs.
	if dir := getFirstFoundEnvTestBinaryDir(); dir != "" {
		testEnv.BinaryAssetsDirectory = dir
	}

	var err error
	cfg, err = testEnv.Start()
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to start envtest: %v\n", err)
		os.Exit(1)
	}
	if cfg == nil {
		fmt.Fprintln(os.Stderr, "envtest config is nil")
		os.Exit(1)
	}

	k8sClient, err = client.New(cfg, client.Options{Scheme: scheme.Scheme})
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to create k8s client: %v\n", err)
		os.Exit(1)
	}

	code := m.Run()

	cancel()
	// Tear down the test environment with retries.
	deadline := time.Now().Add(time.Minute)
	for time.Now().Before(deadline) {
		if err := testEnv.Stop(); err == nil {
			break
		}
		time.Sleep(time.Second)
	}

	os.Exit(code)
}

// getFirstFoundEnvTestBinaryDir locates the first binary in the specified path.
func getFirstFoundEnvTestBinaryDir() string {
	basePath := filepath.Join("..", "..", "bin", "k8s")
	entries, err := os.ReadDir(basePath)
	if err != nil {
		logf.Log.Error(err, "Failed to read directory", "path", basePath)
		return ""
	}
	for _, entry := range entries {
		if entry.IsDir() {
			return filepath.Join(basePath, entry.Name())
		}
	}
	return ""
}

// ---------------------------------------------------------------------------
// Test assertion helpers
// ---------------------------------------------------------------------------

func requireNoError(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func requireError(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func requireEqual[T comparable](t *testing.T, got, want T, msgAndArgs ...any) {
	t.Helper()
	if got != want {
		suffix := ""
		if len(msgAndArgs) > 0 {
			suffix = ": " + fmt.Sprint(msgAndArgs...)
		}
		t.Fatalf("got %v, want %v%s", got, want, suffix)
	}
}

func requireTrue(t *testing.T, v bool, msgAndArgs ...any) {
	t.Helper()
	if !v {
		msg := "expected true"
		if len(msgAndArgs) > 0 {
			msg = fmt.Sprint(msgAndArgs...)
		}
		t.Fatal(msg)
	}
}

func requireFalse(t *testing.T, v bool, msgAndArgs ...any) {
	t.Helper()
	if v {
		msg := "expected false"
		if len(msgAndArgs) > 0 {
			msg = fmt.Sprint(msgAndArgs...)
		}
		t.Fatal(msg)
	}
}

func isNil(v any) bool {
	if v == nil {
		return true
	}
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Ptr, reflect.Interface, reflect.Slice, reflect.Map, reflect.Chan, reflect.Func:
		return rv.IsNil()
	}
	return false
}

func requireNil(t *testing.T, v any) {
	t.Helper()
	if !isNil(v) {
		t.Fatalf("expected nil, got %v", v)
	}
}

func requireNotNil(t *testing.T, v any) {
	t.Helper()
	if isNil(v) {
		t.Fatal("expected non-nil, got nil")
	}
}

func requireContains(t *testing.T, s, substr string) {
	t.Helper()
	if !strings.Contains(s, substr) {
		t.Fatalf("expected %q to contain %q", s, substr)
	}
}

func requireNotEmpty(t *testing.T, s string) {
	t.Helper()
	if s == "" {
		t.Fatal("expected non-empty string")
	}
}

func requireLen[T any](t *testing.T, s []T, n int) {
	t.Helper()
	if len(s) != n {
		t.Fatalf("expected len %d, got %d", n, len(s))
	}
}

func requireZero[T comparable](t *testing.T, v T) {
	t.Helper()
	var zero T
	if v != zero {
		t.Fatalf("expected zero value, got %v", v)
	}
}

func requireGreaterThan[T int | int32 | int64 | float64 | time.Duration](t *testing.T, got, threshold T) {
	t.Helper()
	if got <= threshold {
		t.Fatalf("expected %v > %v", got, threshold)
	}
}

func requireNoPanic(t *testing.T, fn func()) {
	t.Helper()
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("unexpected panic: %v", r)
		}
	}()
	fn()
}
