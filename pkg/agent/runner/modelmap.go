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

package runner

import (
	"context"

	"github.com/kubeswarm/kubeswarm/pkg/agent/providers"
)

// builtinContextWindows maps known model IDs to their context window sizes in tokens.
// Consulted when LoopCompressionConfig.ContextWindow is 0 and the provider
// does not implement ModelInfoProvider. Add entries here as new models ship.
var builtinContextWindows = map[string]int{
	// Anthropic Claude
	"claude-3-haiku-20240307":    200_000,
	"claude-3-5-haiku-20241022":  200_000,
	"claude-haiku-4-5-20251001":  200_000,
	"claude-3-5-sonnet-20241022": 200_000,
	"claude-3-7-sonnet-20250219": 200_000,
	"claude-sonnet-4-6":          200_000,
	"claude-opus-4-6":            200_000,
	// OpenAI GPT-4 series
	"gpt-4o":              128_000,
	"gpt-4o-mini":         128_000,
	"gpt-4-turbo":         128_000,
	"gpt-4-turbo-preview": 128_000,
	// OpenAI o-series (reasoning) - RFC-0022 coupling point
	"o1":      200_000,
	"o1-mini": 128_000,
	"o3":      200_000,
	"o3-mini": 200_000,
	"o4-mini": 200_000,
	// Google Gemini
	"gemini-1.5-pro":   2_000_000,
	"gemini-1.5-flash": 1_000_000,
	"gemini-2.0-flash": 1_000_000,
	"gemini-2.5-pro":   1_000_000,
}

// resolveContextWindow returns the context window for model in tokens.
// Priority: explicit config override > provider ModelInfoProvider > built-in map.
// Returns 0 if unknown (caller should treat as "cannot compress").
func resolveContextWindow(ctx context.Context, model string, explicit int, p providers.LLMProvider) int {
	if explicit > 0 {
		return explicit
	}
	if mip, ok := p.(providers.ModelInfoProvider); ok {
		if info, err := mip.ModelInfo(ctx, model); err == nil && info != nil && info.ContextWindow > 0 {
			return info.ContextWindow
		}
	}
	return builtinContextWindows[model]
}

// estimateTokens is a cheap character-based token estimate used when a
// provider does not implement TokenCounter. Accuracy is sufficient for
// triggering compression (over-estimation is safe; under-estimation is not).
func estimateTokens(text string) int {
	// ~4 chars per token is a well-known heuristic for English + code.
	return (len(text) + 3) / 4
}
