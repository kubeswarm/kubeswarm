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

package providers_test

import (
	"testing"

	"github.com/kubeswarm/kubeswarm/pkg/agent/providers"
	// Register the mock provider so New("mock") and New("") resolve.
	_ "github.com/kubeswarm/kubeswarm/pkg/agent/providers/mock"
)

func TestNew_Mock(t *testing.T) {
	for _, name := range []string{"mock"} {
		p, err := providers.New(name)
		if err != nil {
			t.Errorf("New(%q) error: %v", name, err)
		}
		if p == nil {
			t.Errorf("New(%q) returned nil", name)
		}
	}
}

func TestNew_Unknown(t *testing.T) {
	_, err := providers.New("no-such-provider")
	if err == nil {
		t.Fatal("expected error for unknown provider, got nil")
	}
}

func TestDetect(t *testing.T) {
	cases := []struct {
		model string
		want  string
	}{
		{"claude-sonnet-4-20250514", "anthropic"},
		{"claude-haiku-4-5-20251001", "anthropic"},
		{"gpt-4o", "openai"},
		{"gpt-4o-mini", "openai"},
		{"o1-preview", "openai"},
		{"o3-mini", "openai"},
		{"o4-mini", "openai"},
		{"unknown-model-xyz", "openai"}, // default: OpenAI-compatible (e.g. Ollama)
		{"qwen2.5:7b", "openai"},
		{"llama3.2", "openai"},
	}
	for _, tc := range cases {
		if got := providers.Detect(tc.model); got != tc.want {
			t.Errorf("Detect(%q) = %q, want %q", tc.model, got, tc.want)
		}
	}
}
