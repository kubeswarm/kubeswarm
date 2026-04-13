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

package costs

import "strings"

const currencyUSD = "USD"

func init() {
	RegisterCostProvider("static", func() CostProvider { return &StaticCostProvider{} })
	RegisterCostProvider("zero", func() CostProvider { return &zeroCostProvider{} })
}

// staticPricingTable maps model name prefixes to pricing.
// Longest prefix wins. Prices are in USD per 1M tokens (as of 2026-03-21).
// Update as model pricing changes - a future ConfigMapCostProvider will
// allow overrides without code changes.
type pricingEntry struct {
	prefix     string
	inputPerM  float64 // USD per 1M input tokens
	outputPerM float64 // USD per 1M output tokens
}

var staticPricingTable = []pricingEntry{
	// Anthropic Claude 4.x
	{"claude-opus-4", 15.00, 75.00},
	{"claude-sonnet-4", 3.00, 15.00},
	{"claude-haiku-4", 0.80, 4.00},
	// Anthropic Claude 3.x (legacy)
	{"claude-3-opus", 15.00, 75.00},
	{"claude-3-5-sonnet", 3.00, 15.00},
	{"claude-3-sonnet", 3.00, 15.00},
	{"claude-3-haiku", 0.25, 1.25},
	// OpenAI GPT-4o family
	{"gpt-4o-mini", 0.15, 0.60},
	{"gpt-4o", 2.50, 10.00},
	// OpenAI o-series
	{"o3-mini", 1.10, 4.40},
	{"o3", 10.00, 40.00},
	{"o1-mini", 1.10, 4.40},
	{"o1", 15.00, 60.00},
	// OpenAI GPT-4 legacy
	{"gpt-4-turbo", 10.00, 30.00},
	{"gpt-4", 30.00, 60.00},
	{"gpt-3.5-turbo", 0.50, 1.50},
	// Google Gemini
	{"gemini-2.5-pro", 1.25, 10.00},
	{"gemini-2.5-flash", 0.15, 0.60},
	{"gemini-2.0-flash", 0.10, 0.40},
	{"gemini-1.5-pro", 3.50, 10.50},
	{"gemini-1.5-flash", 0.075, 0.30},
	// Local / open-source models - always free
	{"qwen", 0, 0},
	{"llama", 0, 0},
	{"mistral", 0, 0},
	{"phi", 0, 0},
	{"deepseek", 0, 0},
}

// StaticCostProvider uses the hardcoded pricing table above.
// Unknown models return 0.0 (treated as free).
type StaticCostProvider struct{}

func (p *StaticCostProvider) Cost(model string, inputTokens, outputTokens, thinkingTokens int64) float64 {
	entry := lookupPricing(model)
	inputCost := float64(inputTokens) / 1_000_000 * entry.inputPerM
	outputCost := float64(outputTokens+thinkingTokens) / 1_000_000 * entry.outputPerM
	return inputCost + outputCost
}

func (p *StaticCostProvider) Currency() string { return currencyUSD }

// longestPrefixMatch returns the value from entries whose prefix key is the
// longest that matches the lowercased model string. ok is false if no entry matches.
func longestPrefixMatch[T any](model string, entries []T, key func(T) string) (T, bool) {
	lower := strings.ToLower(model)
	var best T
	bestLen := -1
	for _, e := range entries {
		p := key(e)
		if strings.HasPrefix(lower, p) && len(p) > bestLen {
			best = e
			bestLen = len(p)
		}
	}
	return best, bestLen >= 0
}

// lookupPricing finds the longest matching prefix in staticPricingTable.
// Returns zero pricing for unknown models.
func lookupPricing(model string) pricingEntry {
	e, ok := longestPrefixMatch(model, staticPricingTable, func(e pricingEntry) string { return e.prefix })
	if !ok {
		return pricingEntry{}
	}
	return e
}

// zeroCostProvider always returns 0. For air-gapped or Ollama-only deployments.
type zeroCostProvider struct{}

func (p *zeroCostProvider) Cost(_ string, _, _, _ int64) float64 { return 0 }
func (p *zeroCostProvider) Currency() string                     { return currencyUSD }
