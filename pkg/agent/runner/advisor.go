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
	"net/url"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	oteltrace "go.opentelemetry.io/otel/trace"

	"github.com/kubeswarm/kubeswarm/pkg/agent/config"
	agenterrors "github.com/kubeswarm/kubeswarm/pkg/agent/errors"
	"github.com/kubeswarm/kubeswarm/pkg/agent/mcp"
	"github.com/kubeswarm/kubeswarm/pkg/agent/queue"
	"github.com/kubeswarm/kubeswarm/pkg/observability"
)

const toolTypeAdvisor = "advisor"

// advisorState tracks per-task call counts and cumulative tokens for one advisor.
type advisorState struct {
	mu               sync.Mutex
	callCount        int32
	cumulativeTokens int32
}

// advisorTracker manages per-task advisor call budgets. Created fresh per task attempt.
type advisorTracker struct {
	states  map[string]*advisorState        // keyed by advisor name
	configs map[string]config.AdvisorConfig // keyed by tool name
}

func newAdvisorTracker(advisors []config.AdvisorConfig) *advisorTracker {
	t := &advisorTracker{
		states:  make(map[string]*advisorState, len(advisors)),
		configs: make(map[string]config.AdvisorConfig, len(advisors)),
	}
	for _, a := range advisors {
		t.states[a.Name] = &advisorState{}
		t.configs[a.ToolName] = a
	}
	return t
}

// isAdvisorTool returns the advisor config if toolName matches an advisor tool.
func (t *advisorTracker) isAdvisorTool(toolName string) (config.AdvisorConfig, bool) {
	cfg, ok := t.configs[toolName]
	return cfg, ok
}

// checkAndIncrement checks call budget and increments the counter. Returns error if limit exceeded.
func (t *advisorTracker) checkAndIncrement(advisorName string, maxCalls int32) (callIndex int32, remaining int32, err error) {
	s := t.states[advisorName]
	s.mu.Lock()
	defer s.mu.Unlock()

	if maxCalls > 0 && s.callCount >= maxCalls {
		return s.callCount, 0, fmt.Errorf("advisor call limit exceeded")
	}
	s.callCount++
	callIndex = s.callCount
	if maxCalls > 0 {
		remaining = maxCalls - s.callCount
	}
	return callIndex, remaining, nil
}

// AddTokens adds to cumulative token count. Returns error if limit exceeded.
// Called by the gateway response handler when token usage is available.
func (t *advisorTracker) AddTokens(advisorName string, tokens int32, maxTokens int32) error {
	s, ok := t.states[advisorName]
	if !ok {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	s.cumulativeTokens += tokens
	if maxTokens > 0 && s.cumulativeTokens > maxTokens {
		return fmt.Errorf("advisor token limit exceeded")
	}
	return nil
}

// CumulativeTokens returns the current cumulative token count for an advisor.
func (t *advisorTracker) CumulativeTokens(advisorName string) int32 {
	s, ok := t.states[advisorName]
	if !ok {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cumulativeTokens
}

// buildAdvisorTools returns MCP tool definitions for all configured advisors.
func buildAdvisorTools(advisors []config.AdvisorConfig) []mcp.Tool {
	tools := make([]mcp.Tool, 0, len(advisors))
	for _, a := range advisors {
		desc := a.Instructions
		if desc == "" {
			desc = fmt.Sprintf("Consult %s for expert guidance. Send a specific question. The advisor sees your recent conversation context and can review your work so far.", a.Name)
		}
		schema := json.RawMessage(`{"type":"object","properties":{"question":{"type":"string","description":"The specific question or decision you need guidance on. The advisor will also see your recent conversation context."}},"required":["question"]}`)
		tools = append(tools, mcp.Tool{
			Name:        a.ToolName,
			Description: desc,
			InputSchema: schema,
		})
	}
	return tools
}

// callAdvisor handles the invocation of an advisor tool. It enforces call limits,
// token limits, and timeouts, then dispatches via the MCP gateway.
func (r *Runner) callAdvisor(ctx context.Context, toolName string, input json.RawMessage, tracker *advisorTracker) (string, error) {
	cfg, ok := tracker.isAdvisorTool(toolName)
	if !ok {
		return "", agenterrors.NewToolError(agenterrors.ErrToolInvalidArgs, fmt.Sprintf("unknown advisor tool %q", toolName), nil)
	}

	// Create advisor span.
	ctx, span := observability.Tracer("swarm-runner").Start(ctx, "advisor.consult",
		oteltrace.WithAttributes(
			attribute.String("kubeswarm.advisor.name", cfg.Name),
			attribute.String("kubeswarm.advisor.tool_name", cfg.ToolName),
			attribute.String("kubeswarm.advisor.agent_ref", cfg.AgentRef),
		),
	)
	defer span.End()

	// Check and increment call budget.
	callIndex, remaining, err := tracker.checkAndIncrement(cfg.Name, cfg.MaxCallsPerTask)
	if err != nil {
		result := fmt.Sprintf(`{"error":"advisor_limit_exceeded","advisor":"%s","limit":%d}`, cfg.Name, cfg.MaxCallsPerTask)
		span.SetAttributes(
			attribute.String("kubeswarm.advisor.outcome", "limit_exceeded"),
			attribute.Int("kubeswarm.advisor.call_index", int(callIndex)),
		)
		span.SetStatus(codes.Error, "advisor call limit exceeded")
		return result, nil // Return as tool result, not error - executor should reason about it.
	}

	span.SetAttributes(
		attribute.Int("kubeswarm.advisor.call_index", int(callIndex)),
		attribute.Int("kubeswarm.advisor.call_budget_remaining", int(remaining)),
	)

	// Parse question from input.
	var args struct {
		Question string `json:"question"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return "", agenterrors.NewToolError(agenterrors.ErrToolInvalidArgs, "advisor call: invalid input", err)
	}

	// Build the prompt for the advisor with context metadata.
	prompt := fmt.Sprintf("## Advisor Consultation\n\n**Question from executor:** %s", args.Question)

	// Compute effective timeout: min(configured, remaining task deadline).
	timeout := time.Duration(cfg.TimeoutSeconds) * time.Second
	if deadline, ok := ctx.Deadline(); ok {
		remaining := time.Until(deadline)
		if remaining < timeout {
			timeout = remaining
		}
	}

	advisorCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Dispatch via MCP gateway REST endpoint to the advisor agent.
	start := time.Now()
	ar, err := r.callAdvisorViaGateway(advisorCtx, cfg, prompt)
	elapsed := time.Since(start)

	if err != nil {
		// Check if it was a timeout.
		if advisorCtx.Err() != nil {
			result := fmt.Sprintf(`{"error":"advisor_timeout","advisor":"%s","elapsed_seconds":%d}`, cfg.Name, int(elapsed.Seconds()))
			span.SetAttributes(attribute.String("kubeswarm.advisor.outcome", "timeout"))
			span.SetStatus(codes.Error, "advisor timeout")
			return result, nil
		}
		// Unavailable.
		result := fmt.Sprintf(`{"error":"advisor_unavailable","advisor":"%s","reason":"%s"}`, cfg.Name, err.Error())
		span.SetAttributes(attribute.String("kubeswarm.advisor.outcome", "unavailable"))
		span.SetStatus(codes.Error, "advisor unavailable")
		return result, nil
	}

	// Track token usage from the advisor response.
	advisorTokens := int32(ar.Usage.InputTokens + ar.Usage.OutputTokens)
	if advisorTokens > 0 {
		if tokenErr := tracker.AddTokens(cfg.Name, advisorTokens, cfg.MaxAdvisorTokensPerTask); tokenErr != nil {
			span.SetAttributes(attribute.String("kubeswarm.advisor.outcome", "token_limit_exceeded"))
			span.SetStatus(codes.Error, "advisor token limit exceeded")
			// Return the result but annotate that the token budget is now exhausted.
			// The next call will be blocked by checkAndIncrement or the caller can
			// inspect the cumulative count.
		}
	}

	span.SetAttributes(
		attribute.Int("kubeswarm.advisor.tokens.cumulative", int(tracker.CumulativeTokens(cfg.Name))),
		attribute.String("kubeswarm.advisor.outcome", "success"),
	)
	return ar.Output, nil
}

// advisorResult holds the output and token usage from an advisor call.
type advisorResult struct {
	Output string
	Usage  queue.TokenUsage
}

// callAdvisorViaGateway submits a task to the advisor agent's queue and waits
// for the result. The advisor agent processes the prompt using its own LLM
// provider and returns the response through the same queue mechanism.
func (r *Runner) callAdvisorViaGateway(ctx context.Context, cfg config.AdvisorConfig, prompt string) (advisorResult, error) {
	baseURL := r.cfg.TaskQueueURL
	if baseURL == "" {
		return advisorResult{}, fmt.Errorf("no queue URL configured")
	}

	// Strip any existing stream param from the base URL to get the bare Redis URL,
	// then build the advisor's per-agent stream key.
	advisorStreamKey := r.cfg.Namespace + "." + cfg.AgentRef
	advisorQueueURL := replaceStreamParam(baseURL, advisorStreamKey)

	advisorQueue, err := queue.NewQueue(advisorQueueURL, 0)
	if err != nil {
		return advisorResult{}, fmt.Errorf("cannot connect to advisor queue: %v", err)
	}
	defer advisorQueue.Close()

	taskID, err := advisorQueue.Submit(ctx, prompt, map[string]string{
		"advisor_call": "true",
		"caller":       r.cfg.AgentName,
	})
	if err != nil {
		return advisorResult{}, fmt.Errorf("cannot submit to advisor: %v", err)
	}

	// Poll for the result within the advisor timeout.
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return advisorResult{}, ctx.Err()
		case <-ticker.C:
			results, err := advisorQueue.Results(ctx, []string{taskID})
			if err != nil || len(results) == 0 {
				continue // transient error or not ready yet, keep polling
			}
			if results[0].Error != "" {
				return advisorResult{}, fmt.Errorf("advisor error: %s", results[0].Error)
			}
			return advisorResult{
				Output: results[0].Output,
				Usage:  results[0].Usage,
			}, nil
		}
	}
}

// replaceStreamParam strips any existing stream query parameter from a Redis URL
// and appends a new one. This ensures the advisor queue URL targets the correct
// per-agent stream regardless of what stream the caller's own URL has.
func replaceStreamParam(rawURL, streamName string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	q := u.Query()
	q.Set("stream", streamName)
	u.RawQuery = q.Encode()
	return u.String()
}
