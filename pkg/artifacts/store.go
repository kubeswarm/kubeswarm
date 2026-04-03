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

// Package artifacts provides a pluggable interface for storing and retrieving
// file artifacts produced by pipeline steps.
//
// Artifacts are named files that a pipeline step produces (e.g. a PDF report,
// a CSV dataset, an image). The agent writes them to $AGENT_ARTIFACT_DIR/<name>
// after its task completes. The store uploads them to the configured backend
// and returns a URL/path that downstream steps can reference via template variables:
//
//	{{ .steps.<stepName>.artifacts.<name> }}
package artifacts

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	kubeswarmv1alpha1 "github.com/kubeswarm/kubeswarm/api/v1alpha1"
)

// StoreFactory creates a Store from a connection URL.
type StoreFactory func(url string) (Store, error)

var (
	mu            sync.RWMutex
	storeRegistry = map[string]StoreFactory{}
)

func init() {
	// Register the built-in file:// backend.
	storeRegistry["file"] = func(url string) (Store, error) {
		// Strip scheme: "file:///path" → "/path", "file://path" → "path"
		path := strings.TrimPrefix(url, "file://")
		if path == "" {
			path = "/tmp/swarm-artifacts"
		}
		return NewLocalStore(path), nil
	}
}

// RegisterStore registers a factory for the given URL scheme.
// Call from an init() function in each backend package.
func RegisterStore(scheme string, factory StoreFactory) {
	mu.Lock()
	defer mu.Unlock()
	storeRegistry[scheme] = factory
}

// NewFromURL creates a Store from a connection URL.
// The built-in "file://" scheme is always available.
// Other schemes require importing the relevant backend package.
//
// Example URLs:
//
//	file:///data/artifacts
//	s3://bucket-name/prefix?region=us-east-1
//	gcs://bucket-name/prefix
func NewFromURL(url string) (Store, error) {
	scheme := schemeOf(url)
	mu.RLock()
	factory, ok := storeRegistry[scheme]
	mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("no artifact Store registered for scheme %q (url: %s); import the backend package to register it", scheme, url)
	}
	return factory(url)
}

// schemeOf returns the URL scheme (the part before "://").
func schemeOf(url string) string {
	if before, _, ok := strings.Cut(url, "://"); ok {
		return before
	}
	return ""
}

// Store is the interface for reading and writing file artifacts.
type Store interface {
	// Put uploads or copies data with the given artifact name under the step's namespace.
	// Returns the URL or path that can be used to retrieve the artifact.
	Put(ctx context.Context, runName, stepName, artifactName string, data []byte) (string, error)

	// Get retrieves artifact data by the URL/path returned from Put.
	Get(ctx context.Context, url string) ([]byte, error)

	// Close releases any resources held by the implementation.
	Close() error
}

// NoopStore is used when no artifact store is configured.
// All operations succeed silently.
type NoopStore struct{}

func (n *NoopStore) Put(_ context.Context, _, _, _ string, _ []byte) (string, error) {
	return "", nil
}
func (n *NoopStore) Get(_ context.Context, _ string) ([]byte, error) { return nil, nil }
func (n *NoopStore) Close() error                                    { return nil }

// LocalStore stores artifacts on the local filesystem.
type LocalStore struct {
	// BasePath is the root directory where artifacts are stored.
	BasePath string
}

// NewLocalStore creates a LocalStore. If basePath is empty, /tmp/swarm-artifacts is used.
func NewLocalStore(basePath string) *LocalStore {
	if basePath == "" {
		basePath = "/tmp/swarm-artifacts"
	}
	return &LocalStore{BasePath: basePath}
}

// Put writes data to <basePath>/<runName>/<stepName>/<artifactName> and returns the path.
func (s *LocalStore) Put(_ context.Context, runName, stepName, artifactName string, data []byte) (string, error) {
	dir := filepath.Join(s.BasePath, runName, stepName)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return "", fmt.Errorf("artifacts: create dir %q: %w", dir, err)
	}
	path := filepath.Join(dir, artifactName)
	if err := os.WriteFile(path, data, 0o644); err != nil { //nolint:gosec
		return "", fmt.Errorf("artifacts: write %q: %w", path, err)
	}
	return path, nil
}

// Get reads artifact data from the given local path.
func (s *LocalStore) Get(_ context.Context, path string) ([]byte, error) {
	data, err := os.ReadFile(path) //nolint:gosec
	if err != nil {
		return nil, fmt.Errorf("artifacts: read %q: %w", path, err)
	}
	return data, nil
}

func (s *LocalStore) Close() error { return nil }

// FromSpec builds a Store from an ArtifactStoreSpec. Returns NoopStore when spec is nil.
func FromSpec(spec *kubeswarmv1alpha1.ArtifactStoreSpec) (Store, error) {
	if spec == nil {
		return &NoopStore{}, nil
	}
	switch spec.Type {
	case kubeswarmv1alpha1.ArtifactStoreLocal:
		path := ""
		if spec.Local != nil {
			path = spec.Local.Path
		}
		return NewLocalStore(path), nil
	case kubeswarmv1alpha1.ArtifactStoreS3:
		return nil, fmt.Errorf("artifacts: S3 store is not yet implemented (planned v0.16.0)")
	case kubeswarmv1alpha1.ArtifactStoreGCS:
		return nil, fmt.Errorf("artifacts: GCS store is not yet implemented (planned v0.16.0)")
	default:
		return nil, fmt.Errorf("artifacts: unknown store type %q", spec.Type)
	}
}
