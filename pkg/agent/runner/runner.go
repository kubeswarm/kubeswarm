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
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"maps"
	"net/http"
	"strings"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	oteltrace "go.opentelemetry.io/otel/trace"

	"github.com/kubeswarm/kubeswarm/pkg/agent/config"
	"github.com/kubeswarm/kubeswarm/pkg/agent/mcp"
	"github.com/kubeswarm/kubeswarm/pkg/agent/providers"
	"github.com/kubeswarm/kubeswarm/pkg/agent/queue"
	"github.com/kubeswarm/kubeswarm/pkg/observability"
)

// webhookClient is used for all inline webhook tool calls.
// The 30s timeout is a transport-level safety net; the task context provides the primary bound.
var webhookClient = &http.Client{Timeout: 30 * time.Second}

const (
	submitSubtaskTool   = "submit_subtask"
	delegateTool        = "delegate"
	collectResultsTool  = "collect_results"
	spawnAndCollectTool = "spawn_and_collect"
)

type contextKey int

const taskIDKey contextKey = iota

// Runner executes tasks by delegating to a configured LLMProvider.
// It merges tools from four sources:
//   - MCP servers (discovered at startup)
//   - Inline webhook tools (injected via AGENT_WEBHOOK_TOOLS env var)
//   - Built-in tools (submit_subtask when a task queue is available)
//   - Built-in tools (delegate when the agent is part of an SwarmTeam)
type Runner struct {
	cfg            *config.Config
	mcpManager     *mcp.Manager
	provider       providers.LLMProvider
	taskQueue      queue.TaskQueue            // nil = submit_subtask tool unavailable
	stream         queue.StreamChannel        // nil = token streaming unavailable
	delegateQueues map[string]queue.TaskQueue // role → queue; nil = not in a team
	allTools       []mcp.Tool
	metrics        *observability.AgentMetrics
	metricAttrs    []attribute.KeyValue // namespace/agent/role label set
	k8sEvents      *observability.AgentEventRecorder

	// RFC-0026 deep-research hooks; all nil when loopPolicy is unset.
	loopDedup bool // semantic dedup enabled for this runner
}

// New creates a Runner, builds the merged tool list, and wires up the task queue
// for the supervisor/worker submit_subtask built-in.
// delegateQueues is non-nil only when the agent is part of an SwarmTeam; it maps
// role names to pre-connected TaskQueue instances for the delegate() built-in tool.
func New(cfg *config.Config, mcpManager *mcp.Manager, provider providers.LLMProvider, tq queue.TaskQueue, sc queue.StreamChannel, delegateQueues map[string]queue.TaskQueue) *Runner {
	r := &Runner{cfg: cfg, mcpManager: mcpManager, provider: provider, taskQueue: tq, stream: sc, delegateQueues: delegateQueues}
	r.buildTools()
	r.metrics, _ = observability.NewAgentMetrics()
	r.metricAttrs = []attribute.KeyValue{
		attribute.String("namespace", cfg.Namespace),
		attribute.String("agent", cfg.AgentName),
		attribute.String("role", cfg.TeamRole),
	}
	if cfg.LoopPolicy != nil {
		r.loopDedup = cfg.LoopPolicy.Dedup
	}
	return r
}

// buildTools assembles allTools from MCP + webhook + built-in sources.
func (r *Runner) buildTools() {
	r.allTools = append(r.allTools, r.mcpManager.Tools()...)

	// Inline webhook tools defined in spec.tools on the SwarmAgent.
	for _, wt := range r.cfg.WebhookTools {
		schema := json.RawMessage(`{}`)
		if wt.InputSchema != "" {
			schema = json.RawMessage(wt.InputSchema)
		}
		r.allTools = append(r.allTools, mcp.Tool{
			Name:        wt.Name,
			Description: wt.Description,
			InputSchema: schema,
			// ServerURL left blank — CallTool handles these by name via cfg.WebhookTools.
		})
	}

	// Built-in: submit_subtask — only available when the task queue is wired in.
	if r.taskQueue != nil {
		const submitSchema = `{"type":"object",` +
			`"properties":{"prompt":{"type":"string","description":"The task prompt to execute"}},` +
			`"required":["prompt"]}`
		r.allTools = append(r.allTools, mcp.Tool{
			Name:        submitSubtaskTool,
			Description: "Enqueue a new agent task for asynchronous processing. Returns the assigned task ID.",
			InputSchema: json.RawMessage(submitSchema),
		})

		// Built-in: collect_results (RFC-0029) — poll for completed results by task ID.
		const collectSchema = `{"type":"object",` +
			`"properties":{` +
			`"task_ids":{"type":"array","items":{"type":"string"},"description":"Task IDs returned by submit_subtask or delegate"},` +
			`"timeout_seconds":{"type":"integer","description":"Max seconds to wait for all results. Defaults to 120.","default":120}},` +
			`"required":["task_ids"]}`
		r.allTools = append(r.allTools, mcp.Tool{
			Name:        collectResultsTool,
			Description: "Wait for and collect results from previously submitted subtasks. Returns completed results and any still-pending task IDs.",
			InputSchema: json.RawMessage(collectSchema),
		})

		// Built-in: spawn_and_collect (RFC-0029) — submit N tasks and collect all results.
		const spawnSchema = `{"type":"object",` +
			`"properties":{` +
			`"tasks":{"type":"array","items":{"type":"object","properties":{` +
			`"prompt":{"type":"string","description":"Task prompt to execute"},` +
			`"role":{"type":"string","description":"Target role for delegation. Omit for self-queue."}},"required":["prompt"]},` +
			`"description":"List of tasks to execute in parallel"},` +
			`"timeout_seconds":{"type":"integer","description":"Max seconds to wait for all results. Defaults to 120.","default":120}},` +
			`"required":["tasks"]}`
		r.allTools = append(r.allTools, mcp.Tool{
			Name:        spawnAndCollectTool,
			Description: "Submit multiple tasks in parallel and wait for all results. Combines submit_subtask + collect_results in a single call.",
			InputSchema: json.RawMessage(spawnSchema),
		})
	}

	// Built-in: delegate — only available when the agent is part of an SwarmTeam.
	if len(r.delegateQueues) > 0 {
		roles := make([]string, 0, len(r.delegateQueues))
		for role := range r.delegateQueues {
			roles = append(roles, `"`+role+`"`)
		}
		delegateSchema := `{"type":"object",` +
			`"properties":{` +
			`"role":{"type":"string","description":"Target role name. Available roles: ` + strings.Join(roles, ", ") + `"},` +
			`"prompt":{"type":"string","description":"Task prompt to deliver to the target role"}},` +
			`"required":["role","prompt"]}`
		r.allTools = append(r.allTools, mcp.Tool{
			Name:        delegateTool,
			Description: "Delegate a task to another role in the team. Returns the assigned task ID.",
			InputSchema: json.RawMessage(delegateSchema),
		})
	}
}

// SetEventRecorder wires in a Kubernetes event recorder for audit events.
// Called after New by the agent runtime; not used in tests or the CLI.
func (r *Runner) SetEventRecorder(rec *observability.AgentEventRecorder) {
	r.k8sEvents = rec
}

// AllTools returns the merged tool list for use by the health probe and tests.
func (r *Runner) AllTools() []mcp.Tool {
	return r.allTools
}

// CallTool dispatches a tool invocation to the correct handler.
// Priority: built-in → webhook → MCP.
func (r *Runner) CallTool(ctx context.Context, toolName string, input json.RawMessage) (string, error) {
	toolType := "mcp"
	if toolName == submitSubtaskTool || toolName == delegateTool || toolName == collectResultsTool || toolName == spawnAndCollectTool {
		toolType = "builtin"
	} else if r.isWebhookTool(toolName) {
		toolType = "webhook"
	}

	ctx, span := observability.Tracer("swarm-runner").Start(ctx, "kubeswarm.tool.call",
		oteltrace.WithAttributes(
			attribute.String("tool.name", toolName),
			attribute.String("tool.type", toolType),
		),
	)
	defer span.End()

	start := time.Now()
	result, err := r.callToolInner(ctx, toolName, input)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	}
	if r.metrics != nil {
		r.metrics.RecordToolCall(ctx, start, err != nil,
			append(r.metricAttrs,
				attribute.String("tool_name", toolName),
				attribute.String("tool_type", toolType),
			)...,
		)
		if toolName == delegateTool && err == nil {
			r.metrics.RecordDelegate(ctx, r.metricAttrs...)
		}
	}
	return result, err
}

func (r *Runner) isWebhookTool(name string) bool {
	for _, wt := range r.cfg.WebhookTools {
		if wt.Name == name {
			return true
		}
	}
	return false
}

func (r *Runner) callToolInner(ctx context.Context, toolName string, input json.RawMessage) (string, error) {
	// Built-in: supervisor/worker sub-task submission.
	if toolName == submitSubtaskTool && r.taskQueue != nil {
		var args struct {
			Prompt string `json:"prompt"`
		}
		if err := json.Unmarshal(input, &args); err != nil {
			return "", fmt.Errorf("submit_subtask: invalid input: %w", err)
		}
		if strings.TrimSpace(args.Prompt) == "" {
			return "", fmt.Errorf("submit_subtask: prompt must not be empty")
		}
		taskID, err := r.taskQueue.Submit(ctx, args.Prompt, nil)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("subtask submitted with id: %s", taskID), nil
	}

	// Built-in: delegate — role-based task routing for SwarmTeam members.
	if toolName == delegateTool && len(r.delegateQueues) > 0 {
		var args struct {
			Role   string `json:"role"`
			Prompt string `json:"prompt"`
		}
		if err := json.Unmarshal(input, &args); err != nil {
			return "", fmt.Errorf("delegate: invalid input: %w", err)
		}
		if strings.TrimSpace(args.Role) == "" {
			return "", fmt.Errorf("delegate: role must not be empty")
		}
		if strings.TrimSpace(args.Prompt) == "" {
			return "", fmt.Errorf("delegate: prompt must not be empty")
		}
		dq, ok := r.delegateQueues[args.Role]
		if !ok {
			available := make([]string, 0, len(r.delegateQueues))
			for role := range r.delegateQueues {
				available = append(available, role)
			}
			return "", fmt.Errorf("delegate: unknown role %q; available: %s", args.Role, strings.Join(available, ", "))
		}
		ctx, delegateSpan := observability.Tracer("swarm-runner").Start(ctx, "kubeswarm.delegate",
			oteltrace.WithAttributes(
				attribute.String("delegate.from_role", r.cfg.TeamRole),
				attribute.String("delegate.to_role", args.Role),
			),
		)
		taskID, err := dq.Submit(ctx, args.Prompt, nil)
		delegateSpan.End()
		if err != nil {
			return "", fmt.Errorf("delegate: submitting to role %q: %w", args.Role, err)
		}
		if r.k8sEvents != nil {
			parentTaskID, _ := ctx.Value(taskIDKey).(string)
			r.k8sEvents.TaskDelegated(parentTaskID, args.Role, taskID)
		}
		return fmt.Sprintf("task delegated to role %q with id: %s", args.Role, taskID), nil
	}

	// Built-in: collect_results (RFC-0029) — poll for completed results by task ID.
	if toolName == collectResultsTool && r.taskQueue != nil {
		var args struct {
			TaskIDs        []string `json:"task_ids"`
			TimeoutSeconds int      `json:"timeout_seconds"`
		}
		if err := json.Unmarshal(input, &args); err != nil {
			return "", fmt.Errorf("collect_results: invalid input: %w", err)
		}
		if args.TaskIDs == nil {
			return "", fmt.Errorf("collect_results: task_ids is required")
		}
		if args.TimeoutSeconds <= 0 {
			args.TimeoutSeconds = 120
		}
		return collectResultsFromQueue(ctx, r.taskQueue, args.TaskIDs, time.Duration(args.TimeoutSeconds)*time.Second)
	}

	// Built-in: spawn_and_collect (RFC-0029) — submit N tasks, then collect all results.
	if toolName == spawnAndCollectTool && r.taskQueue != nil {
		return r.handleSpawnAndCollect(ctx, input)
	}

	// Inline webhook tools.
	for _, wt := range r.cfg.WebhookTools {
		if wt.Name == toolName {
			return callWebhook(ctx, wt, input)
		}
	}

	// Fall through to MCP tools.
	return r.mcpManager.CallTool(ctx, toolName, input)
}

// RunTask executes a single task through the provider's agentic loop.
// Returns the text result, accumulated token usage, and any error.
// If task.Meta["stream_key"] is set and a task queue is available, each generated
// token chunk is published to that Redis List in real time.
func (r *Runner) RunTask(ctx context.Context, task queue.Task) (string, queue.TokenUsage, error) {
	// Extract W3C trace context propagated through task.Meta so delegation chains
	// remain connected across Redis queue boundaries.
	ctx = otel.GetTextMapPropagator().Extract(ctx, propagation.MapCarrier(task.Meta))
	ctx = context.WithValue(ctx, taskIDKey, task.ID)

	ctx, span := observability.Tracer("swarm-runner").Start(ctx, "kubeswarm.task",
		oteltrace.WithAttributes(
			attribute.String("task.id", task.ID),
			attribute.Int("task.prompt_len", len(task.Prompt)),
		),
		oteltrace.WithAttributes(r.metricAttrs...),
	)
	defer span.End()

	// Initialise per-task RFC-0026 hooks.
	var dedup *ToolCallDeduplicator
	if r.loopDedup {
		dedup = newDeduplicator()
	}

	var compressor *LoopCompressor
	var memHook *LoopMemoryHook
	if lp := r.cfg.LoopPolicy; lp != nil {
		compressor = newCompressor(ctx, lp.Compression, r.cfg.Model, r.provider)
		memHook = newMemoryHook(lp.Memory, r.cfg.VectorStoreURL, task.ID, r.provider)
	}
	if memHook != nil {
		defer memHook.Close()
	}

	// Build a wrapped callTool that applies dedup, memory retrieve, and memory store.
	callTool := r.buildCallTool(ctx, dedup, compressor, memHook)

	var chunkFn func(string)
	if key := task.Meta["stream_key"]; key != "" && r.stream != nil {
		chunkFn = func(chunk string) {
			// Streaming is best-effort; a publish failure does not abort the task.
			_ = r.stream.Publish(key, chunk)
		}
	}
	llmStart := time.Now()
	result, usage, err := r.provider.RunTask(ctx, r.cfg, task, r.allTools, callTool, chunkFn)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	} else {
		span.SetAttributes(
			attribute.Int64("task.input_tokens", usage.InputTokens),
			attribute.Int64("task.output_tokens", usage.OutputTokens),
		)
	}
	if r.metrics != nil {
		r.metrics.RecordLLMCall(ctx, llmStart,
			usage.InputTokens, usage.OutputTokens,
			append(r.metricAttrs,
				attribute.String("provider", r.cfg.Provider),
				attribute.String("model", r.cfg.Model),
			)...,
		)
	}
	return result, usage, err
}

// buildCallTool returns a callTool function that wraps r.CallTool with RFC-0026 hooks.
// Hooks that are nil are no-ops.
func (r *Runner) buildCallTool(
	_ context.Context,
	dedup *ToolCallDeduplicator,
	compressor *LoopCompressor,
	memHook *LoopMemoryHook,
) func(context.Context, string, json.RawMessage) (string, error) {
	return func(callCtx context.Context, toolName string, input json.RawMessage) (string, error) {
		// 1. Semantic dedup: skip identical tool calls seen earlier in this task.
		if dedup != nil && dedup.IsDuplicate(toolName, input) {
			return "[skipped: duplicate tool call]", nil
		}

		// 2. Vector memory retrieve: fetch similar prior findings before dispatch.
		findings := memHook.BeforeCall(callCtx, toolName, input)

		// 3. Dispatch to the real tool.
		result, err := r.CallTool(callCtx, toolName, input)
		if err != nil {
			return result, err
		}

		// 4. In-loop compression: track result tokens; compress if threshold crossed.
		compressor.Track(result)
		if compressor.NeedsCompression() {
			if summary, ok := compressor.Compress(callCtx); ok {
				result = injectSummary(summary, result)
			}
		}

		// 5. Vector memory store: write result to vector store after execution.
		//    Skipped when dedup fired (no new result produced).
		memHook.AfterCall(callCtx, toolName, input, result)

		// 6. Inject prior findings into the result returned to the model.
		result = injectPriorFindings(findings, result)

		return result, nil
	}
}

// collectResults polls taskQueue.Results with exponential backoff until all task IDs
// have results or the timeout/context expires. Returns JSON with completed and pending fields.
// handleSpawnAndCollect implements the spawn_and_collect built-in tool (RFC-0029).
// It submits multiple tasks (to self-queue or delegate queues), then collects all results.
func (r *Runner) handleSpawnAndCollect(ctx context.Context, input json.RawMessage) (string, error) {
	var args struct {
		Tasks []struct {
			Prompt string `json:"prompt"`
			Role   string `json:"role"`
		} `json:"tasks"`
		TimeoutSeconds int `json:"timeout_seconds"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return "", fmt.Errorf("spawn_and_collect: invalid input: %w", err)
	}
	if args.TimeoutSeconds <= 0 {
		args.TimeoutSeconds = 120
	}

	type submission struct {
		ID     string
		Prompt string
		Queue  queue.TaskQueue
	}
	var submitted []submission
	for _, task := range args.Tasks {
		if strings.TrimSpace(task.Prompt) == "" {
			return "", fmt.Errorf("spawn_and_collect: prompt must not be empty")
		}
		var tq queue.TaskQueue
		if task.Role != "" {
			dq, ok := r.delegateQueues[task.Role]
			if !ok {
				available := make([]string, 0, len(r.delegateQueues))
				for role := range r.delegateQueues {
					available = append(available, role)
				}
				return "", fmt.Errorf("spawn_and_collect: unknown role %q; available: %s", task.Role, strings.Join(available, ", "))
			}
			tq = dq
		} else {
			tq = r.taskQueue
		}
		taskID, err := tq.Submit(ctx, task.Prompt, nil)
		if err != nil {
			return "", fmt.Errorf("spawn_and_collect: submitting task: %w", err)
		}
		submitted = append(submitted, submission{ID: taskID, Prompt: task.Prompt, Queue: tq})
	}

	// Group by queue for collection.
	type queueGroup struct {
		queue   queue.TaskQueue
		taskIDs []string
	}
	groupMap := map[queue.TaskQueue]*queueGroup{}
	promptByID := make(map[string]string, len(submitted))
	for _, s := range submitted {
		promptByID[s.ID] = s.Prompt
		g, ok := groupMap[s.Queue]
		if !ok {
			g = &queueGroup{queue: s.Queue}
			groupMap[s.Queue] = g
		}
		g.taskIDs = append(g.taskIDs, s.ID)
	}

	// Collect results from each queue.
	timeout := time.Duration(args.TimeoutSeconds) * time.Second
	allCompleted := make(map[string]string)
	var allPending []string
	for _, g := range groupMap {
		resultJSON, err := collectResultsFromQueue(ctx, g.queue, g.taskIDs, timeout)
		if err != nil {
			return "", err
		}
		var partial struct {
			Completed map[string]string `json:"completed"`
			Pending   []string          `json:"pending"`
		}
		if err := json.Unmarshal([]byte(resultJSON), &partial); err != nil {
			continue
		}
		maps.Copy(allCompleted, partial.Completed)
		allPending = append(allPending, partial.Pending...)
	}
	if allPending == nil {
		allPending = []string{}
	}

	// Enrich completed results with the original prompt.
	type enrichedResult struct {
		Prompt string `json:"prompt"`
		Output string `json:"output"`
	}
	enriched := make(map[string]enrichedResult, len(allCompleted))
	for id, output := range allCompleted {
		enriched[id] = enrichedResult{Prompt: promptByID[id], Output: output}
	}

	out, _ := json.Marshal(struct {
		Completed map[string]enrichedResult `json:"completed"`
		Pending   []string                  `json:"pending"`
	}{Completed: enriched, Pending: allPending})
	return string(out), nil
}

// collectResultsFromQueue polls a TaskQueue for completed results with exponential backoff
// until all task IDs have results or the timeout/context expires. Returns JSON with
// completed and pending fields.
func collectResultsFromQueue(ctx context.Context, tq queue.TaskQueue, taskIDs []string, timeout time.Duration) (string, error) {
	if len(taskIDs) == 0 {
		out, _ := json.Marshal(struct {
			Completed map[string]string `json:"completed"`
			Pending   []string          `json:"pending"`
		}{Completed: map[string]string{}, Pending: []string{}})
		return string(out), nil
	}

	deadline := time.Now().Add(timeout)
	completed := make(map[string]string, len(taskIDs))
	backoff := time.Second

	for {
		// Build list of IDs still pending.
		var pending []string
		for _, id := range taskIDs {
			if _, ok := completed[id]; !ok {
				pending = append(pending, id)
			}
		}
		if len(pending) == 0 {
			break
		}

		// Check deadline and context before polling.
		if time.Now().After(deadline) {
			break
		}
		if ctx.Err() != nil {
			break
		}

		results, err := tq.Results(ctx, pending)
		if err != nil {
			return "", fmt.Errorf("collect_results: polling queue: %w", err)
		}
		for _, tr := range results {
			if tr.Error != "" {
				completed[tr.TaskID] = fmt.Sprintf("[error] %s", tr.Error)
			} else {
				completed[tr.TaskID] = tr.Output
			}
		}

		// Check if all done after this poll.
		if len(completed) == len(taskIDs) {
			break
		}

		// Exponential backoff capped at 10s.
		select {
		case <-time.After(backoff):
		case <-ctx.Done():
		}
		if backoff < 10*time.Second {
			backoff *= 2
			if backoff > 10*time.Second {
				backoff = 10 * time.Second
			}
		}
	}

	// Build final pending list.
	var finalPending []string
	for _, id := range taskIDs {
		if _, ok := completed[id]; !ok {
			finalPending = append(finalPending, id)
		}
	}
	if finalPending == nil {
		finalPending = []string{}
	}

	out, _ := json.Marshal(struct {
		Completed map[string]string `json:"completed"`
		Pending   []string          `json:"pending"`
	}{Completed: completed, Pending: finalPending})
	return string(out), nil
}

// callWebhook invokes an inline HTTP webhook tool and returns the response body as text.
func callWebhook(ctx context.Context, wt config.WebhookToolConfig, input json.RawMessage) (string, error) {
	method := strings.ToUpper(wt.Method)
	if method == "" {
		method = http.MethodPost
	}

	var bodyReader io.Reader
	if method != http.MethodGet && len(input) > 0 {
		bodyReader = bytes.NewReader(input)
	}

	req, err := http.NewRequestWithContext(ctx, method, wt.URL, bodyReader)
	if err != nil {
		return "", fmt.Errorf("webhook tool %q: building request: %w", wt.Name, err)
	}
	if bodyReader != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := webhookClient.Do(req)
	if err != nil {
		log.Printf("webhook tool %q: request failed: %v", wt.Name, err)
		return "", fmt.Errorf("webhook tool %q: request failed: %w", wt.Name, err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("webhook tool %q: reading response: %w", wt.Name, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		log.Printf("webhook tool %q: server returned %d: %s", wt.Name, resp.StatusCode, string(body))
		return "", fmt.Errorf("webhook tool %q: server returned %d: %s", wt.Name, resp.StatusCode, string(body))
	}
	log.Printf("webhook tool %q: called successfully, status=%d", wt.Name, resp.StatusCode)
	return string(body), nil
}
