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

// Package providers defines the LLMProvider interface and a self-registration
// registry. Each provider implementation registers itself via its init() function,
// so adding a new provider requires only:
//
//  1. Create internal/providers/<name>/provider.go implementing LLMProvider.
//  2. Call providers.Register("<name>", ...) in its init().
//  3. Add a blank import in main.go: _ "…/providers/<name>"
package providers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/kubeswarm/kubeswarm/pkg/agent/config"
	"github.com/kubeswarm/kubeswarm/pkg/agent/mcp"
	"github.com/kubeswarm/kubeswarm/pkg/agent/queue"
)

// ErrEmbeddingNotSupported is returned by LLMProvider.Embed when the provider
// does not expose an embeddings API (e.g. Anthropic).
var ErrEmbeddingNotSupported = errors.New("provider does not support embeddings")

// LLMProvider is the interface every LLM backend must implement.
type LLMProvider interface {
	// RunTask executes a task through the provider's agentic tool-use loop.
	// tools is the merged list of available tools (MCP + webhook + built-ins).
	// callTool dispatches a named tool invocation and returns the result.
	// chunkFn, if non-nil, is called with each text token as it is generated (streaming mode).
	// Returns the text result, token usage accumulated across all LLM calls, and any error.
	RunTask(
		ctx context.Context,
		cfg *config.Config,
		task queue.Task,
		tools []mcp.Tool,
		callTool func(context.Context, string, json.RawMessage) (string, error),
		chunkFn func(string),
	) (string, queue.TokenUsage, error)

	// Embed returns a vector embedding for the given text.
	// Returns ErrEmbeddingNotSupported if the provider does not offer embeddings.
	Embed(ctx context.Context, text string) ([]float32, error)
}

// ModelInfo holds provider-reported context window metadata.
type ModelInfo struct {
	// ContextWindow is the total context window in tokens. 0 means unknown.
	ContextWindow int
}

// TokenCounter is an optional interface for providers that can count tokens
// without performing inference. Used by the in-loop compressor to determine
// whether the context threshold has been reached.
type TokenCounter interface {
	CountTokens(ctx context.Context, model string, messages []map[string]any) (int, error)
}

// ModelInfoProvider is an optional interface for providers that expose context
// window metadata for their models. Used by the compressor to resolve the
// context window when not set explicitly in LoopCompressionConfig.ContextWindow.
type ModelInfoProvider interface {
	ModelInfo(ctx context.Context, model string) (*ModelInfo, error)
}

// ConversationCompressor is an optional interface providers implement to support
// in-loop context compression (RFC-0026). When the runner detects that context
// usage has crossed the configured threshold it calls Compress with the full
// conversation so far, and the returned summary replaces older turns.
// Providers that do not implement this interface cannot perform compression.
type ConversationCompressor interface {
	// RegisterCompressionHook supplies a callback that the provider invokes
	// before each LLM API call with the current estimated token count.
	// If the hook returns a non-empty summary the provider replaces older turns
	// (respecting preserveRecentTurns) with a single synthetic assistant turn
	// containing the summary. The hook must return ("", false) to skip.
	RegisterCompressionHook(fn func(ctx context.Context, tokenCount int) (summary string, compress bool))
}

// Completer is an optional interface for providers that support simple single-turn
// completions without the full agentic tool-use loop.
// Use Complete() (the package-level helper) rather than calling this directly.
type Completer interface {
	// Complete performs one non-streaming Messages call.
	// model is the model ID (e.g. "claude-sonnet-4-20250514").
	// Returns the assistant's text response.
	Complete(ctx context.Context, model, systemPrompt, userMsg string, maxTokens int) (string, error)
}

// Complete performs a simple single-turn text completion using the provider
// registered for the given model. It requires the provider to implement Completer.
// Typical use: optimizer meta-agent calls, semantic health checks.
func Complete(ctx context.Context, model, systemPrompt, userMsg string, maxTokens int) (string, error) {
	providerName := Detect(model)
	p, err := New(providerName)
	if err != nil {
		return "", err
	}
	c, ok := p.(Completer)
	if !ok {
		return "", fmt.Errorf("provider %q does not implement Completer", providerName)
	}
	return c.Complete(ctx, model, systemPrompt, userMsg, maxTokens)
}

var (
	mu       sync.RWMutex
	registry = map[string]func() LLMProvider{}
)

// Register adds a provider factory to the registry under the given name.
// Call this from an init() function in each provider package.
func Register(name string, factory func() LLMProvider) {
	mu.Lock()
	defer mu.Unlock()
	registry[name] = factory
}

// New returns the LLMProvider for the given name.
// Callers should use Detect(model) to resolve the provider name when AGENT_PROVIDER is unset.
func New(name string) (LLMProvider, error) {
	if name == "" {
		return nil, fmt.Errorf("provider name is empty; set AGENT_PROVIDER or use Detect(model)")
	}
	mu.RLock()
	factory, ok := registry[name]
	mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("unsupported provider %q; import its package to register it", name)
	}
	return factory(), nil
}

// Detect returns the provider name for a given model ID.
// It inspects the model string prefix to infer the backend:
//
//	claude-*             → anthropic
//	gpt-*, o1*, o3*, o4* → openai
//	gemini-*             → gemini
//
// Falls back to "openai" for unrecognised models so that any
// OpenAI-compatible endpoint (e.g. Ollama) works out of the box.
func Detect(model string) string {
	switch {
	case strings.HasPrefix(model, "claude-"):
		return "anthropic"
	case strings.HasPrefix(model, "gpt-"),
		strings.HasPrefix(model, "o1"),
		strings.HasPrefix(model, "o3"),
		strings.HasPrefix(model, "o4"):
		return "openai"
	case strings.HasPrefix(model, "gemini-"):
		return "gemini"
	default:
		return "openai"
	}
}
