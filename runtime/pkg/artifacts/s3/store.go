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

// Package s3 implements the artifacts.Store interface backed by Amazon S3.
// Register by importing this package:
//
//	import _ "github.com/kubeswarm/kubeswarm/runtime/pkg/artifacts/s3"
//
// Connection URL format: s3://bucket-name/optional-prefix?region=us-east-1
// Credentials use the default AWS credential chain (instance role, env vars, ~/.aws).
package s3

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/url"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/kubeswarm/kubeswarm/pkg/artifacts"
)

func init() {
	artifacts.RegisterStore("s3", func(rawURL string) (artifacts.Store, error) {
		return newS3Store(rawURL)
	})
}

type s3Store struct {
	client *s3.Client
	bucket string
	prefix string
}

func newS3Store(rawURL string) (*s3Store, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("s3: invalid URL %q: %w", rawURL, err)
	}
	bucket := u.Host
	if bucket == "" {
		return nil, fmt.Errorf("s3: missing bucket in URL %q", rawURL)
	}
	prefix := strings.TrimPrefix(u.Path, "/")

	opts := []func(*awsconfig.LoadOptions) error{}
	if region := u.Query().Get("region"); region != "" {
		opts = append(opts, awsconfig.WithRegion(region))
	}
	cfg, err := awsconfig.LoadDefaultConfig(context.Background(), opts...)
	if err != nil {
		return nil, fmt.Errorf("s3: loading AWS config: %w", err)
	}

	// endpoint query param enables S3-compatible services (MinIO, Ceph, Wasabi, etc.).
	// Example: s3://my-bucket/prefix?region=us-east-1&endpoint=http://minio.swarm-system:9000
	endpoint := u.Query().Get("endpoint")
	clientOpts := []func(*s3.Options){}
	if endpoint != "" {
		clientOpts = append(clientOpts, func(o *s3.Options) {
			o.BaseEndpoint = aws.String(endpoint)
			o.UsePathStyle = true // required for MinIO and most S3-compatible services
		})
	}

	return &s3Store{
		client: s3.NewFromConfig(cfg, clientOpts...),
		bucket: bucket,
		prefix: prefix,
	}, nil
}

func (s *s3Store) Put(ctx context.Context, runName, stepName, artifactName string, data []byte) (string, error) {
	key := s.key(runName, stepName, artifactName)
	_, err := s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
		Body:   bytes.NewReader(data),
	})
	if err != nil {
		return "", fmt.Errorf("s3: put %q: %w", key, err)
	}
	return fmt.Sprintf("s3://%s/%s", s.bucket, key), nil
}

func (s *s3Store) Get(ctx context.Context, rawURL string) ([]byte, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("s3: invalid URL %q: %w", rawURL, err)
	}
	key := strings.TrimPrefix(u.Path, "/")
	out, err := s.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(u.Host),
		Key:    aws.String(key),
	})
	if err != nil {
		return nil, fmt.Errorf("s3: get %q: %w", key, err)
	}
	defer out.Body.Close() //nolint:errcheck
	return io.ReadAll(out.Body)
}

func (s *s3Store) Close() error { return nil }

func (s *s3Store) key(runName, stepName, artifactName string) string {
	parts := []string{}
	if s.prefix != "" {
		parts = append(parts, s.prefix)
	}
	parts = append(parts, runName, stepName, artifactName)
	return strings.Join(parts, "/")
}
