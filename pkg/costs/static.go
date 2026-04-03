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

// modelPricing holds per-million-token prices for a model.
type modelPricing struct {
	inputPerM  float64 // USD per 1M input tokens
	outputPerM float64 // USD per 1M output tokens
}

// staticPricingTable maps model name prefixes to pricing.
// Longest prefix wins. Prices are in USD per 1M tokens (as of 2026-03-21).
// Update as model pricing changes — a future ConfigMapCostProvider will
// allow overrides without code changes.
var staticPricingTable = []struct {
	prefix  string
	pricing modelPricing
}{
	// Anthropic Claude 4.x
	{"claude-opus-4", modelPricing{inputPerM: 15.00, outputPerM: 75.00}},
	{"claude-sonnet-4", modelPricing{inputPerM: 3.00, outputPerM: 15.00}},
	{"claude-haiku-4", modelPricing{inputPerM: 0.80, outputPerM: 4.00}},
	// Anthropic Claude 3.x (legacy)
	{"claude-3-opus", modelPricing{inputPerM: 15.00, outputPerM: 75.00}},
	{"claude-3-5-sonnet", modelPricing{inputPerM: 3.00, outputPerM: 15.00}},
	{"claude-3-sonnet", modelPricing{inputPerM: 3.00, outputPerM: 15.00}},
	{"claude-3-haiku", modelPricing{inputPerM: 0.25, outputPerM: 1.25}},
	// OpenAI GPT-4o family
	{"gpt-4o-mini", modelPricing{inputPerM: 0.15, outputPerM: 0.60}},
	{"gpt-4o", modelPricing{inputPerM: 2.50, outputPerM: 10.00}},
	// OpenAI o-series
	{"o3-mini", modelPricing{inputPerM: 1.10, outputPerM: 4.40}},
	{"o3", modelPricing{inputPerM: 10.00, outputPerM: 40.00}},
	{"o1-mini", modelPricing{inputPerM: 1.10, outputPerM: 4.40}},
	{"o1", modelPricing{inputPerM: 15.00, outputPerM: 60.00}},
	// OpenAI GPT-4 legacy
	{"gpt-4-turbo", modelPricing{inputPerM: 10.00, outputPerM: 30.00}},
	{"gpt-4", modelPricing{inputPerM: 30.00, outputPerM: 60.00}},
	{"gpt-3.5-turbo", modelPricing{inputPerM: 0.50, outputPerM: 1.50}},
	// Google Gemini
	{"gemini-2.5-pro", modelPricing{inputPerM: 1.25, outputPerM: 10.00}},
	{"gemini-2.5-flash", modelPricing{inputPerM: 0.15, outputPerM: 0.60}},
	{"gemini-2.0-flash", modelPricing{inputPerM: 0.10, outputPerM: 0.40}},
	{"gemini-1.5-pro", modelPricing{inputPerM: 3.50, outputPerM: 10.50}},
	{"gemini-1.5-flash", modelPricing{inputPerM: 0.075, outputPerM: 0.30}},
	// Local / open-source models — always free
	{"qwen", modelPricing{}},
	{"llama", modelPricing{}},
	{"mistral", modelPricing{}},
	{"phi", modelPricing{}},
	{"deepseek", modelPricing{}},
}

// StaticCostProvider uses the hardcoded pricing table above.
// Unknown models return 0.0 (treated as free).
type StaticCostProvider struct{}

func (p *StaticCostProvider) Cost(model string, inputTokens, outputTokens int64) float64 {
	pricing := lookupPricing(model)
	inputCost := float64(inputTokens) / 1_000_000 * pricing.inputPerM
	outputCost := float64(outputTokens) / 1_000_000 * pricing.outputPerM
	return inputCost + outputCost
}

func (p *StaticCostProvider) Currency() string { return currencyUSD }

// lookupPricing finds the longest matching prefix in staticPricingTable.
// Returns zero pricing for unknown models.
func lookupPricing(model string) modelPricing {
	lower := strings.ToLower(model)
	best := modelPricing{}
	bestLen := -1
	for _, entry := range staticPricingTable {
		if strings.HasPrefix(lower, entry.prefix) && len(entry.prefix) > bestLen {
			best = entry.pricing
			bestLen = len(entry.prefix)
		}
	}
	return best
}

// zeroCostProvider always returns 0. For air-gapped or Ollama-only deployments.
type zeroCostProvider struct{}

func (p *zeroCostProvider) Cost(_ string, _, _ int64) float64 { return 0 }
func (p *zeroCostProvider) Currency() string                  { return currencyUSD }
