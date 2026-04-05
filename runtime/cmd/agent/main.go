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

package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"time"

	"go.opentelemetry.io/otel/attribute"

	goredis "github.com/redis/go-redis/v9"

	"github.com/kubeswarm/kubeswarm/pkg/agent/budget"
	"github.com/kubeswarm/kubeswarm/pkg/agent/config"
	"github.com/kubeswarm/kubeswarm/pkg/agent/health"
	"github.com/kubeswarm/kubeswarm/pkg/agent/mcp"
	"github.com/kubeswarm/kubeswarm/pkg/agent/providers"
	"github.com/kubeswarm/kubeswarm/pkg/agent/queue"
	"github.com/kubeswarm/kubeswarm/pkg/agent/runner"
	"github.com/kubeswarm/kubeswarm/pkg/artifacts"
	"github.com/kubeswarm/kubeswarm/pkg/audit"
	"github.com/kubeswarm/kubeswarm/pkg/costs"
	"github.com/kubeswarm/kubeswarm/pkg/observability"

	// Register built-in LLM providers and queue backends. These keep SDK deps
	// out of the controller module. Activated by their init() functions.
	_ "github.com/kubeswarm/kubeswarm/runtime/costs/redisstore"
	_ "github.com/kubeswarm/kubeswarm/runtime/pkg/artifacts/gcs"
	_ "github.com/kubeswarm/kubeswarm/runtime/pkg/artifacts/s3"
	_ "github.com/kubeswarm/kubeswarm/runtime/providers/anthropic"
	_ "github.com/kubeswarm/kubeswarm/runtime/providers/gemini"
	_ "github.com/kubeswarm/kubeswarm/runtime/providers/openai"
	_ "github.com/kubeswarm/kubeswarm/runtime/queue/redis"

	// Register gRPC adapter providers/queues (RFC-0025). Always compiled in;
	// activate only when SWARM_PLUGIN_LLM_ADDR / SWARM_PLUGIN_QUEUE_ADDR are set.
	_ "github.com/kubeswarm/kubeswarm/pkg/agent/providers/grpcprovider"
	_ "github.com/kubeswarm/kubeswarm/pkg/agent/queue/grpcqueue"

	// Register vector store backends (RFC-0026).
	_ "github.com/kubeswarm/kubeswarm/runtime/pkg/vectors/qdrant"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	otelShutdown, err := observability.Init(context.Background(), "kubeswarm-runtime")
	if err != nil {
		logger.Warn("failed to initialise OpenTelemetry, continuing without observability", "error", err)
	} else {
		defer otelShutdown()
	}

	cfg, err := config.Load()
	if err != nil {
		logger.Error("failed to load config", "error", err)
		os.Exit(1)
	}

	// Connect to MCP servers and discover tools.
	// Failures are non-fatal: the agent runs with whatever tools were successfully discovered.
	mcpManager, err := mcp.NewManager(cfg.MCPServers)
	if err != nil {
		logger.Warn("one or more MCP servers unavailable, continuing with reduced toolset", "error", err)
		mcpManager, _ = mcp.NewManager(nil)
	}
	defer mcpManager.Close()

	// Select LLM provider: external gRPC plugin takes precedence over built-in (RFC-0025).
	// Auto-detect from model name when AGENT_PROVIDER is not explicitly set.
	providerName := cfg.Provider
	if providerName == "" {
		providerName = providers.Detect(cfg.Model)
		logger.Info("auto-detected provider from model", "model", cfg.Model, "provider", providerName)
	}
	if cfg.ExternalProviderAddr != "" {
		providerName = "grpc"
		logger.Info("using external gRPC LLM provider", "addr", cfg.ExternalProviderAddr)
	}
	provider, err := providers.New(providerName)
	if err != nil {
		logger.Error("unsupported LLM provider", "provider", providerName, "error", err)
		os.Exit(1)
	}

	// Select task queue: external gRPC plugin takes precedence over TASK_QUEUE_URL (RFC-0025).
	queueURL := cfg.TaskQueueURL
	if cfg.ExternalQueueAddr != "" {
		queueURL = "grpc://" + cfg.ExternalQueueAddr
		logger.Info("using external gRPC task queue", "addr", cfg.ExternalQueueAddr)
	}
	taskQueue, err := queue.NewQueue(queueURL, cfg.MaxRetries)
	if err != nil {
		logger.Error("failed to create task queue", "error", err)
		os.Exit(1)
	}

	stream, err := queue.NewStream(cfg.StreamChannelURL)
	if err != nil {
		logger.Error("failed to create stream channel", "error", err)
		os.Exit(1)
	}

	budgetStore, err := budget.NewStore(cfg.TaskQueueURL, cfg.DailyTokenLimit, cfg.Namespace, cfg.AgentName)
	if err != nil {
		logger.Error("failed to create budget store", "error", err)
		os.Exit(1)
	}
	defer func() {
		if err := budgetStore.Close(); err != nil {
			logger.Warn("failed to close budget store", "error", err)
		}
	}()

	spendStoreURL := os.Getenv("SPEND_STORE_URL")
	if spendStoreURL == "" {
		// Strip kubeswarm-specific ?stream= param - standard Redis URL parsers reject it.
		spendStoreURL, _, _ = strings.Cut(cfg.TaskQueueURL, "?")
	}
	spendStore, err := costs.NewSpendStore(spendStoreURL)
	if err != nil {
		logger.Warn("failed to create spend store, spend will not be recorded", "error", err)
		spendStore = costs.NoopSpendStore{}
	}

	// Build delegate queue connections for each team route.
	var delegateQueues map[string]queue.TaskQueue
	if len(cfg.TeamRoutes) > 0 {
		delegateQueues = make(map[string]queue.TaskQueue, len(cfg.TeamRoutes))
		for role, queueURL := range cfg.TeamRoutes {
			dq, err := queue.NewQueue(queueURL, cfg.MaxRetries)
			if err != nil {
				logger.Error("failed to create delegate queue", "role", role, "error", err)
				os.Exit(1)
			}
			delegateQueues[role] = dq
		}
	}

	defer func() {
		taskQueue.Close()
		for _, dq := range delegateQueues {
			dq.Close()
		}
	}()

	metrics, err := observability.NewAgentMetrics()
	if err != nil {
		logger.Warn("failed to create agent metrics, continuing without metrics", "error", err)
		metrics = nil
	}

	k8sEvents := observability.NewAgentEventRecorder(cfg.Namespace, cfg.AgentName)

	r := runner.New(cfg, mcpManager, provider, taskQueue, stream, delegateQueues)
	r.SetEventRecorder(k8sEvents)

	// Wire audit emitter (RFC-0030). Reads from cfg.AuditLog which is parsed
	// from the AGENT_AUDIT_LOG env var injected by the operator.
	if cfg.AuditLog != nil {
		var auditSink audit.AuditSink
		switch cfg.AuditLog.Sink {
		case "redis":
			if cfg.AuditLog.RedisURL != "" {
				opts, err := goredis.ParseURL(cfg.AuditLog.RedisURL)
				if err != nil {
					logger.Error("failed to parse audit redis URL", "error", err)
					os.Exit(1)
				}
				rdb := goredis.NewClient(opts)
				auditSink = audit.NewRedisSink(&goredisAdapter{rdb})
			} else {
				logger.Warn("audit sink=redis but no redisURL configured, falling back to stdout")
				auditSink = audit.NewStdoutSink(nil)
			}
		case "stdout", "":
			auditSink = audit.NewStdoutSink(nil)
		default:
			auditSink = audit.NewStdoutSink(nil)
		}
		emitter := audit.NewEmitter(auditSink, 0)
		emitter.SetMode(audit.AuditLogMode(cfg.AuditLog.Mode))
		redactor := audit.NewRedactor(cfg.AuditLog.Redact)
		truncator := audit.NewTruncator(cfg.AuditLog.MaxDetailBytes)
		r.SetAuditEmitter(emitter, redactor, truncator)
		logger.Info("audit emitter enabled for agent", "mode", cfg.AuditLog.Mode, "sink", cfg.AuditLog.Sink)
		defer func() {
			shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer shutdownCancel()
			emitter.Close(shutdownCtx)
		}()
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()

	logger.Info("agent runtime started",
		"model", cfg.Model,
		"provider", cfg.Provider,
		"mcp_servers", len(cfg.MCPServers),
	)

	go health.ServeProbe(":8080", r, cfg.ValidatorPrompt)

	sem := make(chan struct{}, cfg.MaxConcurrentTasks)

	for {
		select {
		case <-ctx.Done():
			logger.Info("shutting down")
			return
		default:
		}

		task, err := taskQueue.Poll(ctx)
		if err != nil {
			logger.Error("queue poll error", "error", err)
			continue
		}
		if task == nil {
			continue
		}

		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			logger.Info("shutting down")
			return
		}

		go func(t queue.Task) {
			defer func() { <-sem }()
			processTask(ctx, t, cfg, r, taskQueue, stream, budgetStore, spendStore, metrics, k8sEvents)
		}(*task)
	}
}

func processTask(
	ctx context.Context,
	t queue.Task,
	cfg *config.Config,
	r *runner.Runner,
	taskQueue queue.TaskQueue,
	stream queue.StreamChannel,
	budgetStore budget.Store,
	spendStore costs.SpendStore,
	metrics *observability.AgentMetrics,
	k8sEvents *observability.AgentEventRecorder,
) {
	taskStart := time.Now()
	taskAttrs := []attribute.KeyValue{
		attribute.String("namespace", cfg.Namespace),
		attribute.String("agent", cfg.AgentName),
		attribute.String("role", cfg.TeamRole),
	}

	taskCtx, taskCancel := context.WithTimeout(ctx, config.TaskTimeout(cfg))
	defer taskCancel()

	if timeout := config.TaskTimeout(cfg); timeout > 0 {
		ageID := t.ID
		if origID := t.Meta["original_task_id"]; origID != "" {
			ageID = origID
		}
		if parts := strings.SplitN(ageID, "-", 2); len(parts) == 2 {
			if ms, parseErr := strconv.ParseInt(parts[0], 10, 64); parseErr == nil {
				if age := time.Since(time.UnixMilli(ms)); age > timeout {
					slog.Warn("skipping stale task",
						"task_id", t.ID,
						"original_task_id", ageID,
						"age", age.Round(time.Second),
						"timeout", timeout,
					)
					if nackErr := taskQueue.Nack(t, "stale task: age exceeds timeout"); nackErr != nil {
						slog.Error("nack failed for stale task", "task_id", t.ID, "error", nackErr)
					}
					return
				}
			}
		}
	}

	if err := budgetStore.Check(taskCtx); err != nil {
		slog.Warn("task rejected: daily token budget exceeded",
			"task_id", t.ID,
			"agent", cfg.AgentName,
		)
		if nackErr := taskQueue.Nack(t, "daily token budget exceeded"); nackErr != nil {
			slog.Error("nack failed", "task_id", t.ID, "error", nackErr)
		}
		return
	}

	if metrics != nil {
		metrics.RecordTaskStarted(ctx, taskAttrs...)
		metrics.RecordQueueWait(ctx, t.Meta["enqueued_at"], taskAttrs...)
	}
	k8sEvents.TaskStarted(t.ID, cfg.TeamRole)

	result, usage, err := r.RunTask(taskCtx, t)

	if key := t.Meta["stream_key"]; key != "" {
		if doneErr := stream.Done(key); doneErr != nil {
			slog.Error("stream Done failed", "task_id", t.ID, "stream_key", key, "error", doneErr)
		}
	}

	if err != nil {
		slog.Error("task failed", "task_id", t.ID, "error", err)
		if metrics != nil {
			metrics.RecordTaskFailed(ctx, taskAttrs...)
		}
		k8sEvents.TaskFailed(t.ID, err.Error())
		if nackErr := taskQueue.Nack(t, err.Error()); nackErr != nil {
			slog.Error("nack failed", "task_id", t.ID, "error", nackErr)
		}
		return
	}
	slog.Info("task completed", "task_id", t.ID,
		"input_tokens", usage.InputTokens,
		"output_tokens", usage.OutputTokens,
	)
	if metrics != nil {
		metrics.RecordTaskCompleted(ctx, taskStart, taskAttrs...)
	}
	k8sEvents.TaskCompleted(t.ID, usage.InputTokens, usage.OutputTokens)
	collected := collectArtifacts(ctx, cfg, t)
	if ackErr := taskQueue.Ack(t, result, usage, collected); ackErr != nil {
		slog.Error("ack failed", "task_id", t.ID, "error", ackErr)
	}
	if recordErr := budgetStore.Record(taskCtx, t.ID, usage.InputTokens+usage.OutputTokens); recordErr != nil {
		slog.Warn("failed to record token usage in budget store", "task_id", t.ID, "error", recordErr)
	}
	slog.Info("spend check", "team_name", cfg.TeamName, "input", usage.InputTokens, "output", usage.OutputTokens)
	if cfg.TeamName != "" && (usage.InputTokens > 0 || usage.OutputTokens > 0) {
		costUSD := costs.Default().Cost(cfg.Model, usage.InputTokens, usage.OutputTokens)
		spendEntry := costs.SpendEntry{
			Timestamp:    time.Now(),
			Namespace:    cfg.Namespace,
			Team:         cfg.TeamName,
			RunName:      t.ID,
			StepName:     cfg.TeamRole,
			Model:        cfg.Model,
			InputTokens:  usage.InputTokens,
			OutputTokens: usage.OutputTokens,
			CostUSD:      costUSD,
		}
		if spendErr := spendStore.Record(taskCtx, spendEntry); spendErr != nil {
			slog.Warn("failed to record spend", "task_id", t.ID, "error", spendErr)
		}
	}
}

// collectArtifacts scans cfg.ArtifactDir after a task completes, uploads each file
// to the configured artifact store, and returns a name->URL map.
// Returns nil when no store is configured or no files are found.
func collectArtifacts(ctx context.Context, cfg *config.Config, task queue.Task) map[string]string {
	if cfg.ArtifactStoreURL == "" || cfg.ArtifactDir == "" {
		return nil
	}
	store, err := artifacts.NewFromURL(cfg.ArtifactStoreURL)
	if err != nil {
		slog.Warn("artifact store unavailable", "url", cfg.ArtifactStoreURL, "error", err)
		return nil
	}
	defer store.Close() //nolint:errcheck

	runName := task.Meta["run_name"]
	stepName := task.Meta["step_name"]
	if runName == "" {
		runName = task.ID
	}
	if stepName == "" {
		stepName = cfg.TeamRole
	}

	entries, err := os.ReadDir(cfg.ArtifactDir)
	if err != nil {
		if !os.IsNotExist(err) {
			slog.Warn("reading artifact dir", "dir", cfg.ArtifactDir, "error", err)
		}
		return nil
	}

	result := make(map[string]string, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		path := filepath.Join(cfg.ArtifactDir, entry.Name())
		data, err := os.ReadFile(path) //nolint:gosec
		if err != nil {
			slog.Warn("reading artifact", "file", path, "error", err)
			continue
		}
		url, err := store.Put(ctx, runName, stepName, entry.Name(), data)
		if err != nil {
			slog.Warn("uploading artifact", "file", path, "error", err)
			continue
		}
		result[entry.Name()] = url
		slog.Info("artifact uploaded", "name", entry.Name(), "url", url)
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

// goredisAdapter wraps a go-redis client to implement the audit.RedisClient interface.
type goredisAdapter struct {
	rdb *goredis.Client
}

func (a *goredisAdapter) XAdd(ctx context.Context, stream string, maxLen int64, values map[string]any) error {
	return a.rdb.XAdd(ctx, &goredis.XAddArgs{
		Stream: stream,
		MaxLen: maxLen,
		Approx: true,
		Values: values,
	}).Err()
}
