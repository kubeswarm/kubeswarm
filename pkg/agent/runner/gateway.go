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
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kubeswarm/kubeswarm/pkg/agent/config"
	agenterrors "github.com/kubeswarm/kubeswarm/pkg/agent/errors"
	"github.com/kubeswarm/kubeswarm/pkg/agent/mcp"
	"github.com/kubeswarm/kubeswarm/pkg/agent/queue"
)

const (
	toolTypeGateway     = "gateway"
	gatewayToolSearch   = "registry_search"
	gatewayToolDispatch = "dispatch"
)

// gatewayDepthMetaKey is the task metadata key used to propagate dispatch chain depth.
const gatewayDepthMetaKey = "kubeswarm.io/dispatch-depth"

// gatewayHandler manages gateway tool state for one Runner instance (RFC-0052).
type gatewayHandler struct {
	capabilities  []config.GatewayCapabilityConfig
	config        *config.GatewayRuntimeConfig
	tools         []config.GatewayToolConfig
	agentName     string // self-dispatch guard
	namespace     string
	queueURL      string // base Redis queue URL for dispatch
	dispatchCount int    // per-task call counter (reset per task)
	searchCount   int    // per-task search call counter
	dispatchDepth int    // chain depth from incoming task metadata
	mu            sync.Mutex
}

// resetPerTask resets per-task counters and reads the incoming dispatch depth
// from task metadata. Called at the start of each task execution.
func (h *gatewayHandler) resetPerTask(meta map[string]string) {
	h.mu.Lock()
	h.dispatchCount = 0
	h.searchCount = 0
	h.dispatchDepth = 0
	if v, ok := meta[gatewayDepthMetaKey]; ok {
		if d, err := strconv.Atoi(v); err == nil {
			h.dispatchDepth = d
		}
	}
	h.mu.Unlock()
}

// maxSearchCalls returns the configured cap or a default of 3.
func (h *gatewayHandler) maxSearchCalls() int {
	if h.config != nil && h.config.MaxSearchCalls > 0 {
		return int(h.config.MaxSearchCalls)
	}
	return 3
}

// maxResultsPerSearch returns the configured cap or the default of 10.
func (h *gatewayHandler) maxResultsPerSearch() int {
	if h.config != nil && h.config.MaxResultsPerSearch > 0 {
		return int(h.config.MaxResultsPerSearch)
	}
	return 10
}

// maxDispatchCalls returns the configured cap or a default of 5.
func (h *gatewayHandler) maxDispatchCalls() int {
	if h.config != nil && h.config.MaxDispatchCalls > 0 {
		return int(h.config.MaxDispatchCalls)
	}
	return 5
}

// maxDispatchDepth returns the configured cap or a default of 3.
func (h *gatewayHandler) maxDispatchDepth() int {
	if h.config != nil && h.config.MaxDispatchDepth > 0 {
		return int(h.config.MaxDispatchDepth)
	}
	return 3
}

// dispatchTimeoutSeconds returns the configured timeout or a default of 120.
func (h *gatewayHandler) dispatchTimeoutSeconds() int {
	if h.config != nil && h.config.DispatchTimeoutSeconds > 0 {
		return int(h.config.DispatchTimeoutSeconds)
	}
	return 120
}

// registrySearch filters capabilities by keyword and/or tags.
// Accepts query alone, tags alone, or both; rejects only when both are empty.
func (h *gatewayHandler) registrySearch(input json.RawMessage) (string, error) {
	var args struct {
		Query string   `json:"query"`
		Tags  []string `json:"tags"`
	}
	// Validate input before burning a search-quota slot so malformed LLM output
	// does not rate-limit the agent.
	if err := json.Unmarshal(input, &args); err != nil {
		return "", agenterrors.NewToolError(agenterrors.ErrToolInvalidArgs, "registry_search: invalid input", err)
	}
	queryTrimmed := strings.TrimSpace(args.Query)
	if queryTrimmed == "" && len(args.Tags) == 0 {
		return "", agenterrors.NewToolError(agenterrors.ErrToolInvalidArgs, "registry_search: query or tags must be provided", nil)
	}

	// Enforce search call limit after validation so only real attempts count.
	h.mu.Lock()
	h.searchCount++
	count := h.searchCount
	h.mu.Unlock()
	if count > h.maxSearchCalls() {
		return `{"capabilities":[],"returned":0,"totalMatches":0,"rateLimited":true,"note":"Search call limit reached. No more searches allowed for this task. Answer the user directly with your own knowledge."}`, nil
	}

	queryWords := tokenize(queryTrimmed)

	var matches []config.GatewayCapabilityConfig
	for _, entry := range h.capabilities {
		queryMatch := len(queryWords) > 0 && matchesQuery(entry, queryWords)
		tagMatch := len(args.Tags) > 0 && hasAllTags(entry, args.Tags)
		// Match if either condition holds. Empty query with tags → tags alone;
		// empty tags with query → query alone; both present → OR between them.
		if !queryMatch && !tagMatch {
			continue
		}
		matches = append(matches, entry)
	}

	totalMatches := len(matches)
	maxResults := h.maxResultsPerSearch()
	if len(matches) > maxResults {
		matches = matches[:maxResults]
	}

	type searchResult struct {
		Capabilities []config.GatewayCapabilityConfig `json:"capabilities"`
		Returned     int                              `json:"returned"`
		TotalMatches int                              `json:"totalMatches"`
		RateLimited  bool                             `json:"rateLimited,omitempty"`
		Note         string                           `json:"note,omitempty"`
	}
	result := searchResult{
		Capabilities: matches,
		Returned:     len(matches),
		TotalMatches: totalMatches,
	}
	if result.Capabilities == nil {
		result.Capabilities = []config.GatewayCapabilityConfig{}
	}
	if totalMatches == 0 {
		result.Note = "No matching capabilities found. Answer the user directly with your own knowledge. Do not search again."
	} else if totalMatches > len(matches) {
		result.Note = fmt.Sprintf("Showing %d of %d matching capabilities. Refine your query or tags to narrow results.", len(matches), totalMatches)
	}

	out, err := json.Marshal(result)
	if err != nil {
		return "", agenterrors.NewToolError(agenterrors.ErrToolExecFailed, "registry_search: marshalling result", err)
	}
	return string(out), nil
}

// dispatch sends a task to a target agent via queue and waits for the result.
func (h *gatewayHandler) dispatch(ctx context.Context, input json.RawMessage) (string, error) {
	var args struct {
		Target    string `json:"target"`
		Namespace string `json:"namespace"`
		Prompt    string `json:"prompt"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return "", agenterrors.NewToolError(agenterrors.ErrToolInvalidArgs, "dispatch: invalid input", err)
	}
	if strings.TrimSpace(args.Target) == "" {
		return "", agenterrors.NewToolError(agenterrors.ErrToolInvalidArgs, "dispatch: target must not be empty", nil)
	}
	if strings.TrimSpace(args.Prompt) == "" {
		return "", agenterrors.NewToolError(agenterrors.ErrToolInvalidArgs, "dispatch: prompt must not be empty", nil)
	}
	if args.Namespace == "" {
		args.Namespace = h.namespace
	}

	// Validation 1: self-dispatch guard.
	if args.Target == h.agentName && args.Namespace == h.namespace {
		return "", agenterrors.NewToolError(agenterrors.ErrToolInvalidArgs, "dispatch: cannot dispatch to self", nil)
	}

	// Validation 2: target must exist in capabilities.
	if !h.hasCapability(args.Target, args.Namespace) {
		return "", agenterrors.NewToolError(agenterrors.ErrToolNotFound, fmt.Sprintf("dispatch: target %q not found in gateway capabilities; call registry_search to get valid targets", args.Target), nil)
	}

	// Validation 3: allowed targets check.
	if h.config != nil && len(h.config.AllowedTargets) > 0 {
		allowed := slices.Contains(h.config.AllowedTargets, args.Target)
		if !allowed {
			return "", agenterrors.NewToolError(agenterrors.ErrToolInvalidArgs, fmt.Sprintf("dispatch: target %q is not in the allowed targets list", args.Target), nil)
		}
	}

	// Validation 4: dispatch call limit.
	h.mu.Lock()
	h.dispatchCount++
	count := h.dispatchCount
	depth := h.dispatchDepth
	h.mu.Unlock()
	if count > h.maxDispatchCalls() {
		return "", agenterrors.NewToolError(agenterrors.ErrToolExecFailed, "dispatch call limit exceeded", nil)
	}
	// Validation 5: dispatch depth limit (depth is inherited from incoming task metadata).
	if depth >= h.maxDispatchDepth() {
		return "", agenterrors.NewToolError(agenterrors.ErrToolExecFailed, "dispatch depth exceeded", nil)
	}

	// Compute target queue URL.
	targetStream := args.Namespace + "." + args.Target
	targetURL := replaceStreamParam(h.queueURL, targetStream)

	// Create queue, submit task, poll for result.
	tq, err := queue.NewQueue(targetURL, 0)
	if err != nil {
		out, _ := json.Marshal(map[string]string{
			"error":  "dispatch_failed",
			"target": args.Target,
			"reason": fmt.Sprintf("creating queue: %s", err),
		})
		return string(out), nil
	}
	defer tq.Close()

	// Propagate dispatch depth to the child task so downstream gateways
	// can enforce the chain depth limit.
	dispatchMeta := map[string]string{
		gatewayDepthMetaKey: strconv.Itoa(depth + 1),
	}

	start := time.Now()
	taskID, err := tq.Submit(ctx, args.Prompt, dispatchMeta)
	if err != nil {
		out, _ := json.Marshal(map[string]string{
			"error":  "dispatch_failed",
			"target": args.Target,
			"reason": fmt.Sprintf("submitting task: %s", err),
		})
		return string(out), nil
	}

	// Poll for result with a hard deadline context so that slow Results() calls
	// cannot extend beyond the configured dispatch timeout.
	timeout := time.Duration(h.dispatchTimeoutSeconds()) * time.Second
	pollCtx, pollCancel := context.WithDeadline(ctx, time.Now().Add(timeout))
	defer pollCancel()

	backoff := time.Second
	// Treat Results() errors as transient up to this many consecutive failures
	// before surfacing dispatch_failed. A single Redis hiccup should not kill
	// a dispatch that is otherwise within its timeout budget.
	const maxConsecutiveResultErrors = 3
	consecutiveErrs := 0
	var lastErr error

	for {
		if pollCtx.Err() != nil {
			// Distinguish parent cancellation from dispatch timeout.
			if ctx.Err() != nil {
				return "", ctx.Err()
			}
			elapsed := int(time.Since(start).Seconds())
			out, _ := json.Marshal(map[string]any{
				"error":           "dispatch_timeout",
				"target":          args.Target,
				"elapsed_seconds": elapsed,
			})
			return string(out), nil
		}

		results, err := tq.Results(pollCtx, []string{taskID})
		if err != nil {
			if ctx.Err() != nil {
				return "", ctx.Err()
			}
			if pollCtx.Err() != nil {
				continue // will be caught as timeout at top of loop
			}
			consecutiveErrs++
			lastErr = err
			if consecutiveErrs >= maxConsecutiveResultErrors {
				out, _ := json.Marshal(map[string]string{
					"error":  "dispatch_failed",
					"target": args.Target,
					"reason": fmt.Sprintf("polling results after %d retries: %s", consecutiveErrs, lastErr),
				})
				return string(out), nil
			}
			// Fall through to the backoff sleep below and retry.
		} else {
			consecutiveErrs = 0
			for _, r := range results {
				if r.Error != "" {
					out, _ := json.Marshal(map[string]string{
						"error":  "dispatch_failed",
						"target": args.Target,
						"reason": r.Error,
					})
					return string(out), nil
				}
				return r.Output, nil
			}
		}

		timer := time.NewTimer(backoff)
		select {
		case <-timer.C:
		case <-pollCtx.Done():
		}
		timer.Stop()
		if backoff < 10*time.Second {
			backoff *= 2
			if backoff > 10*time.Second {
				backoff = 10 * time.Second
			}
		}
	}
}

// hasCapability checks whether the target agent exists in the capabilities list.
// Target is matched against the Agent field (agent name), not the capability Name.
func (h *gatewayHandler) hasCapability(agentName, namespace string) bool {
	for _, entry := range h.capabilities {
		if entry.Agent == agentName {
			if entry.Namespace != "" && namespace != "" && entry.Namespace != namespace {
				continue
			}
			return true
		}
	}
	return false
}

// tokenize splits a string into lowercase words.
func tokenize(s string) []string {
	words := strings.Fields(strings.ToLower(s))
	return words
}

// matchesQuery checks if any query word appears as a substring in the capability's
// name, description, or tags (OR semantics across words). This differs from tag
// filtering in registrySearch which uses AND semantics - query words are intentionally
// OR because users describe intent loosely, while tags are precise categorical filters.
// Substring matching handles inflected forms (e.g. "reviews" matches "review").
func matchesQuery(entry config.GatewayCapabilityConfig, queryWords []string) bool {
	// Build a single searchable string from name + description.
	searchText := strings.ToLower(strings.ReplaceAll(entry.Name, "-", " ") + " " + entry.Description)
	for _, qw := range queryWords {
		if strings.Contains(searchText, qw) {
			return true
		}
		for _, tag := range entry.Tags {
			if strings.EqualFold(tag, qw) {
				return true
			}
		}
	}
	return false
}

// hasAllTags checks that a capability has ALL of the listed tags (AND semantics, case-insensitive).
// Uses linear search - tag lists are typically 2-5 items, not worth map allocation.
func hasAllTags(entry config.GatewayCapabilityConfig, requiredTags []string) bool {
	for _, rt := range requiredTags {
		found := false
		for _, t := range entry.Tags {
			if strings.EqualFold(t, rt) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// buildGatewayTools constructs mcp.Tool definitions for gateway tools (RFC-0052).
// replaceStreamParam is defined in advisor.go and reused here.
func buildGatewayTools(cfg *config.Config) []mcp.Tool {
	if len(cfg.GatewayTools) == 0 {
		return nil
	}

	var tools []mcp.Tool
	for _, gt := range cfg.GatewayTools {
		switch gt.Name {
		case gatewayToolSearch:
			tools = append(tools, mcp.Tool{
				Name:        gatewayToolSearch,
				Description: gt.Description,
				InputSchema: json.RawMessage(`{"type":"object","properties":{"query":{"type":"string","description":"Keywords describing the type of work you need done"},"tags":{"type":"array","items":{"type":"string"},"description":"Optional tags to filter capabilities"}},"required":["query"]}`),
			})
		case gatewayToolDispatch:
			tools = append(tools, mcp.Tool{
				Name:        gatewayToolDispatch,
				Description: gt.Description,
				InputSchema: json.RawMessage(`{"type":"object","properties":{"target":{"type":"string","description":"Name of the agent (must be from registry_search results)"},"namespace":{"type":"string","description":"Namespace of the target (defaults to gateway namespace)"},"prompt":{"type":"string","description":"The task prompt to send"}},"required":["target","prompt"]}`),
			})
		}
	}
	return tools
}
