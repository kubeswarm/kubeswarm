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

// Package gcs implements the artifacts.Store interface backed by Google Cloud Storage.
// Register by importing this package:
//
//	import _ "github.com/kubeswarm/kubeswarm/runtime/pkg/artifacts/gcs"
//
// Connection URL format: gcs://bucket-name/optional-prefix
// Credentials use the default GCP credential chain (workload identity, GOOGLE_APPLICATION_CREDENTIALS, gcloud ADC).
package gcs

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"strings"

	gcstorage "cloud.google.com/go/storage"
	"google.golang.org/api/option"

	"github.com/kubeswarm/kubeswarm/pkg/artifacts"
)

func init() {
	artifacts.RegisterStore("gcs", func(rawURL string) (artifacts.Store, error) {
		return newGCSStore(context.Background(), rawURL)
	})
}

type gcsStore struct {
	client *gcstorage.Client
	bucket string
	prefix string
}

func newGCSStore(ctx context.Context, rawURL string) (*gcsStore, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("gcs: invalid URL %q: %w", rawURL, err)
	}
	bucket := u.Host
	if bucket == "" {
		return nil, fmt.Errorf("gcs: missing bucket in URL %q", rawURL)
	}
	prefix := strings.TrimPrefix(u.Path, "/")

	opts := []option.ClientOption{}
	client, err := gcstorage.NewClient(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("gcs: creating client: %w", err)
	}
	return &gcsStore{client: client, bucket: bucket, prefix: prefix}, nil
}

func (g *gcsStore) Put(ctx context.Context, runName, stepName, artifactName string, data []byte) (string, error) {
	key := g.key(runName, stepName, artifactName)
	w := g.client.Bucket(g.bucket).Object(key).NewWriter(ctx)
	if _, err := w.Write(data); err != nil {
		_ = w.Close()
		return "", fmt.Errorf("gcs: write %q: %w", key, err)
	}
	if err := w.Close(); err != nil {
		return "", fmt.Errorf("gcs: close writer for %q: %w", key, err)
	}
	return fmt.Sprintf("gcs://%s/%s", g.bucket, key), nil
}

func (g *gcsStore) Get(ctx context.Context, rawURL string) ([]byte, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("gcs: invalid URL %q: %w", rawURL, err)
	}
	key := strings.TrimPrefix(u.Path, "/")
	r, err := g.client.Bucket(u.Host).Object(key).NewReader(ctx)
	if err != nil {
		return nil, fmt.Errorf("gcs: get %q: %w", key, err)
	}
	defer r.Close() //nolint:errcheck
	return io.ReadAll(r)
}

func (g *gcsStore) Close() error {
	return g.client.Close()
}

func (g *gcsStore) key(runName, stepName, artifactName string) string {
	parts := []string{}
	if g.prefix != "" {
		parts = append(parts, g.prefix)
	}
	parts = append(parts, runName, stepName, artifactName)
	return strings.Join(parts, "/")
}
