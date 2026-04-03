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

// Package mock provides a deterministic LLMProvider for local testing.
// It returns canned responses without making any API calls, making it
// ideal for testing SwarmFlow logic (DAG order, conditionals, loops) without
// burning API credits or requiring an Anthropic API key.
//
// Usage — register and select:
//
//	import _ "github.com/kubeswarm/kubeswarm/pkg/agent/providers/mock"
//	provider, _ := providers.New("mock")
//
// Or construct directly for fine-grained control:
//
//	p := &mock.Provider{
//	    Responses: map[string]string{
//	        "research": "Here are the findings...",
//	        "summarize": "• Point one\n• Point two",
//	    },
//	    Default: "mock response",
//	    Delay:   200 * time.Millisecond,
//	}
package mock

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/kubeswarm/kubeswarm/pkg/agent/config"
	"github.com/kubeswarm/kubeswarm/pkg/agent/mcp"
	"github.com/kubeswarm/kubeswarm/pkg/agent/providers"
	"github.com/kubeswarm/kubeswarm/pkg/agent/queue"
)

func init() {
	providers.Register("mock", func() providers.LLMProvider {
		return &Provider{Default: "mock response"}
	})
}

// Provider is a configurable mock LLM backend.
//
// Response matching: for each entry in Responses, if the task prompt contains
// the key (case-insensitive substring match), that value is returned.
// Falls back to Default when nothing matches.
type Provider struct {
	// Responses maps prompt substrings to canned reply text.
	// Matched case-insensitively; first match wins.
	Responses map[string]string

	// Default is the response returned when no Responses entry matches.
	// Defaults to "mock response" when constructed via the registry.
	Default string

	// Delay simulates LLM latency. Zero means no delay.
	Delay time.Duration
}

// RunTask implements providers.LLMProvider.
// It matches the task prompt against Responses and returns the canned reply.
// No real LLM calls are made; tools are never invoked.
// If chunkFn is non-nil, the reply is emitted word-by-word to simulate streaming.
func (p *Provider) RunTask(
	ctx context.Context,
	_ *config.Config,
	task queue.Task,
	_ []mcp.Tool,
	_ func(context.Context, string, json.RawMessage) (string, error),
	chunkFn func(string),
) (string, queue.TokenUsage, error) {
	if p.Delay > 0 {
		select {
		case <-time.After(p.Delay):
		case <-ctx.Done():
			return "", queue.TokenUsage{}, ctx.Err()
		}
	}

	reply := p.match(task.Prompt)

	if chunkFn != nil {
		words := strings.Fields(reply)
		for i, w := range words {
			if i > 0 {
				chunkFn(" ")
			}
			chunkFn(w)
		}
	}

	// Simulate token counts proportional to prompt/reply length
	// so callers can exercise budget and cost-tracking logic.
	usage := queue.TokenUsage{
		InputTokens:  int64(len(task.Prompt) / 4),
		OutputTokens: int64(len(reply) / 4),
	}

	return reply, usage, nil
}

// Embed implements providers.LLMProvider.
// Returns a fixed-length zero vector suitable for testing embedding pipelines
// without a real embedding model.
func (p *Provider) Embed(_ context.Context, text string) ([]float32, error) {
	const dim = 1536 // matches OpenAI text-embedding-3-small output dimension
	vec := make([]float32, dim)
	// Give different texts slightly different vectors so similarity checks
	// can distinguish them: set vec[0] to a deterministic value from the text.
	if len(text) > 0 {
		vec[0] = float32(len(text)) / 10000.0
	}
	return vec, nil
}

// match returns the first Responses value whose key is a case-insensitive
// substring of prompt, or Default if nothing matches.
func (p *Provider) match(prompt string) string {
	lower := strings.ToLower(prompt)
	for key, reply := range p.Responses {
		if strings.Contains(lower, strings.ToLower(key)) {
			return reply
		}
	}
	if p.Default != "" {
		return p.Default
	}
	return "mock response"
}
