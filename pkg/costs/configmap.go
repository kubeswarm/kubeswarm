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

import (
	"fmt"
	"strconv"
	"strings"
	"sync"
)

// ConfigMapCostProvider reads pricing from a flat key→value map loaded from a
// Kubernetes ConfigMap. The operator loads this once at startup via
// NewConfigMapCostProvider and watches the ConfigMap for changes; call
// Reload(data) whenever the ConfigMap's Data field changes.
//
// ConfigMap format (each key is a model name prefix):
//
//	claude-opus-4:    "15.00/75.00"   # input_per_1m/output_per_1m USD
//	my-custom-model:  "0.50/1.00"
//	local-model:      "0.00/0.00"
type ConfigMapCostProvider struct {
	mu       sync.RWMutex
	table    []cmEntry
	fallback CostProvider // used for models not found in the ConfigMap
}

type cmEntry struct {
	prefix     string
	inputPerM  float64
	outputPerM float64
}

// NewConfigMapCostProvider creates a provider pre-loaded with the given ConfigMap
// data map. Pass the static provider as fallback so unknown models still get
// a reasonable cost rather than $0.
func NewConfigMapCostProvider(data map[string]string, fallback CostProvider) (*ConfigMapCostProvider, error) {
	p := &ConfigMapCostProvider{fallback: fallback}
	if err := p.Reload(data); err != nil {
		return nil, err
	}
	return p, nil
}

// Reload replaces the pricing table with fresh data from a ConfigMap.
// Call this whenever the ConfigMap's Data field is updated (via a Watch event).
// Thread-safe; the provider continues serving requests during the reload.
func (p *ConfigMapCostProvider) Reload(data map[string]string) error {
	entries := make([]cmEntry, 0, len(data))
	for key, val := range data {
		in, out, err := parsePricePair(strings.TrimSpace(val))
		if err != nil {
			return fmt.Errorf("configmap cost provider: key %q: %w", key, err)
		}
		entries = append(entries, cmEntry{
			prefix:     strings.ToLower(strings.TrimSpace(key)),
			inputPerM:  in,
			outputPerM: out,
		})
	}
	p.mu.Lock()
	p.table = entries
	p.mu.Unlock()
	return nil
}

// Cost returns the dollar cost for the given model and token counts.
// Uses longest-prefix match against the loaded ConfigMap entries.
// Falls back to the fallback provider if no prefix matches.
func (p *ConfigMapCostProvider) Cost(model string, inputTokens, outputTokens int64) float64 {
	lower := strings.ToLower(model)
	p.mu.RLock()
	best := cmEntry{}
	bestLen := -1
	for _, e := range p.table {
		if strings.HasPrefix(lower, e.prefix) && len(e.prefix) > bestLen {
			best = e
			bestLen = len(e.prefix)
		}
	}
	p.mu.RUnlock()

	if bestLen >= 0 {
		return float64(inputTokens)/1_000_000*best.inputPerM +
			float64(outputTokens)/1_000_000*best.outputPerM
	}
	if p.fallback != nil {
		return p.fallback.Cost(model, inputTokens, outputTokens)
	}
	return 0
}

func (p *ConfigMapCostProvider) Currency() string { return currencyUSD }

// parsePricePair parses "input/output" into two float64 values.
// Example: "15.00/75.00" → (15.0, 75.0)
func parsePricePair(s string) (float64, float64, error) {
	parts := strings.SplitN(s, "/", 2)
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("expected format \"input/output\" (e.g. \"3.00/15.00\"), got %q", s)
	}
	in, err := strconv.ParseFloat(strings.TrimSpace(parts[0]), 64)
	if err != nil {
		return 0, 0, fmt.Errorf("invalid input price %q: %w", parts[0], err)
	}
	out, err := strconv.ParseFloat(strings.TrimSpace(parts[1]), 64)
	if err != nil {
		return 0, 0, fmt.Errorf("invalid output price %q: %w", parts[1], err)
	}
	return in, out, nil
}
