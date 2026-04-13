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

// Package costs provides a pluggable cost translation layer for controller.
// It follows the same registration pattern as internal/agent/providers and
// internal/agent/queue: implementations register via RegisterCostProvider and
// are selected by name via NewCostProvider.
//
// The default provider ("static") uses hardcoded pricing tables for Anthropic
// and OpenAI models. Unknown models (e.g. local Ollama) return zero cost rather
// than an error, so they are tracked as free rather than causing reconcile failures.
package costs

import (
	"fmt"
	"strings"

	"github.com/kubeswarm/kubeswarm/pkg/registry"
)

// CostProvider translates token usage into a dollar cost for a given model.
type CostProvider interface {
	// Cost returns the dollar cost for the given model and token counts.
	// thinkingTokens are billed at the output token rate by most providers.
	// Returns 0.0 for unknown models rather than an error.
	Cost(model string, inputTokens, outputTokens, thinkingTokens int64) float64

	// Currency returns the ISO 4217 currency code (e.g. "USD").
	Currency() string
}

// Factory constructs a CostProvider.
type Factory func() CostProvider

var reg registry.Registry[Factory]

// RegisterCostProvider makes a CostProvider available under the given name.
// Typically called from an init() function in the implementation package.
func RegisterCostProvider(name string, f Factory) { reg.Register(name, f) }

// NewCostProvider returns the named CostProvider, or the "static" default
// when name is empty. Returns an error only if the name is non-empty and unknown.
func NewCostProvider(name string) (CostProvider, error) {
	if name == "" {
		name = "static"
	}
	f, ok := reg.Lookup(name)
	if !ok {
		return nil, fmt.Errorf("unknown cost provider %q; available: %s", name, strings.Join(Backends(), ", "))
	}
	return f(), nil
}

// Default returns the static cost provider. Convenience wrapper for callers
// that don't need configurability - zero cost on unknown models, no error path.
func Default() CostProvider {
	p, _ := NewCostProvider("static")
	return p
}

// Backends returns the sorted names of all registered CostProvider implementations.
func Backends() []string { return reg.Keys() }
