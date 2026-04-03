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

package controller

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"golang.org/x/time/rate"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	kubeswarmv1alpha1 "github.com/kubeswarm/kubeswarm/api/v1alpha1"
	"github.com/kubeswarm/kubeswarm/pkg/agent/queue"
)

// TriggerWebhookServer handles inbound HTTP requests that fire webhook-type SwarmEvents.
//
// Async (default):
//
//	POST /triggers/{namespace}/{name}/fire
//	→ 202 Accepted  { "fired": true, "firedAt": "...", "trigger": "...", "targets": N }
//
// Sync mode — holds the connection open until the flow completes:
//
//	POST /triggers/{namespace}/{name}/fire?mode=sync
//	POST /triggers/{namespace}/{name}/fire?mode=sync&timeout=30s
//	→ 200 OK        { "status": "succeeded", "output": "...", "durationMs": N, "tokenUsage": { "inputTokens": N, "outputTokens": N, "totalTokens": N } }
//	→ 500           { "status": "failed",    "error":  "..." }
//	→ 504           { "error": "timed out" }
//
// SSE mode — streams progress events and individual tokens as the flow runs:
//
//	POST /triggers/{namespace}/{name}/fire?mode=sse
//	POST /triggers/{namespace}/{name}/fire?mode=sse&timeout=5m
//	→ text/event-stream
//	  event: step.started
//	  data: {"step":"<name>","phase":"Running"}
//	  event: token
//	  data: {"token":"<text chunk>"}     (one event per generated token)
//	  event: step.completed
//	  data: {"step":"<name>","output":"...","tokenUsage":{...},"durationMs":N}
//	  event: flow.completed
//	  data: {"status":"succeeded","output":"...","tokenUsage":{...},"durationMs":N}
//	  event: flow.failed
//	  data: {"status":"failed","error":"...","durationMs":N}
//	  event: error
//	  data: {"error":"timed out"}
//
// Sync and SSE modes require exactly one target flow. The default timeout is 60s; maximum is 5m.
//
// Authentication: pass the trigger's token as a Bearer token in the Authorization header.
// The token is stored in a Secret named <trigger-name>-webhook-token in the same namespace.
// Token comparison is constant-time to prevent timing attacks.
//
// The request body is optional JSON. Fields are available in target input templates
// as {{ .trigger.body.<field> }}.
type TriggerWebhookServer struct {
	reconciler *SwarmEventReconciler
	watcher    client.WithWatch    // H5: used for Watch-based flow status updates instead of polling
	stream     queue.StreamChannel // nil when no stream channel is configured; disables SSE streaming
	sseSlots   chan struct{}       // H6: bounded semaphore capping concurrent SSE connections
	ipLimiters ipLimiterMap        // per-IP rate limiter (token bucket)
}

// maxSSEConnections is the upper bound on concurrent SSE connections. Each SSE
// connection holds a goroutine and a Watch; this cap prevents resource exhaustion.
const maxSSEConnections = 500

// webhookRateLimit is the sustained per-IP request rate: 10 requests/second with
// a burst of 20. This is generous for legitimate callers and blocks floods.
const (
	webhookRateLimit rate.Limit = 10
	webhookRateBurst int        = 20
)

// ipLimiterMap holds a per-IP token-bucket rate limiter.
// Limiters are never evicted — this is appropriate because the set of caller
// IPs is bounded by cluster network policy in normal deployments.
type ipLimiterMap struct {
	m map[string]*rate.Limiter
}

func (m *ipLimiterMap) allow(ip string) bool {
	if m.m == nil {
		m.m = make(map[string]*rate.Limiter)
	}
	lim, ok := m.m[ip]
	if !ok {
		lim = rate.NewLimiter(webhookRateLimit, webhookRateBurst)
		m.m[ip] = lim
	}
	return lim.Allow()
}

// NewTriggerWebhookServer returns a new TriggerWebhookServer.
// watcher must be a client.WithWatch (the manager's cache-backed client satisfies this).
// stream may be nil to disable token streaming in SSE mode.
func NewTriggerWebhookServer(r *SwarmEventReconciler, watcher client.WithWatch, stream queue.StreamChannel) *TriggerWebhookServer {
	slots := make(chan struct{}, maxSSEConnections)
	for range maxSSEConnections {
		slots <- struct{}{}
	}
	return &TriggerWebhookServer{reconciler: r, watcher: watcher, stream: stream, sseSlots: slots}
}

const (
	defaultSyncTimeout = 60 * time.Second
	maxSyncTimeout     = 5 * time.Minute
)

// ServeHTTP implements http.Handler.
func (s *TriggerWebhookServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	logger := log.FromContext(ctx)

	// Per-IP rate limiting: 10 req/s sustained, burst of 20.
	clientIP, _, _ := net.SplitHostPort(r.RemoteAddr)
	if !s.ipLimiters.allow(clientIP) {
		http.Error(w, "too many requests", http.StatusTooManyRequests)
		return
	}

	// Expect: /triggers/{namespace}/{name}/fire
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/"), "/")
	if len(parts) != 4 || parts[0] != "triggers" || parts[3] != "fire" {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	namespace, name := parts[1], parts[2]

	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Load the trigger.
	trigger := &kubeswarmv1alpha1.SwarmEvent{}
	if err := s.reconciler.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, trigger); err != nil {
		http.Error(w, "trigger not found", http.StatusNotFound)
		return
	}

	if trigger.Spec.Source.Type != kubeswarmv1alpha1.TriggerSourceWebhook {
		http.Error(w, "trigger is not of type webhook", http.StatusBadRequest)
		return
	}

	if trigger.Spec.Suspended {
		http.Error(w, "trigger is suspended", http.StatusServiceUnavailable)
		return
	}

	// Validate token — constant-time comparison to prevent timing attacks.
	// Authorization header is the only supported method. The ?token query
	// parameter is not accepted (it leaks to access logs on every proxy).
	token := bearerToken(r)
	if !s.validToken(ctx, trigger, token) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	// Parse optional JSON body.
	var body map[string]any
	if r.ContentLength != 0 {
		raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err == nil && len(raw) > 0 {
			_ = json.Unmarshal(raw, &body)
		}
	}

	now := time.Now().UTC()

	// Determine mode: async (default), sync, or sse.
	mode := r.URL.Query().Get("mode")

	// In SSE mode, generate a unique streaming key so the agent can publish token chunks.
	var streamKey string
	if mode == "sse" && s.stream != nil {
		streamKey = fmt.Sprintf("chunks:%s:%s:%d", namespace, name, now.UnixNano())
	}

	fireCtx := FireContext{
		Name:      trigger.Name,
		FiredAt:   now.Format(time.RFC3339),
		Body:      body,
		StreamKey: streamKey,
	}

	runs, err := s.reconciler.fire(ctx, trigger, fireCtx)
	if err != nil {
		logger.Error(err, "firing webhook trigger", "trigger", name, "namespace", namespace)
		http.Error(w, "webhook trigger failed", http.StatusInternalServerError)
		return
	}

	nowMeta := metav1.NewTime(now)
	trigger.Status.LastFiredAt = &nowMeta
	trigger.Status.FiredCount++
	trigger.Status.ObservedGeneration = trigger.Generation
	if err := s.reconciler.Status().Update(ctx, trigger); err != nil {
		logger.Error(err, "updating trigger status after webhook fire")
	}

	// Emit a PipelineFired audit event on the SwarmEvent object so that kubectl get events
	// provides an audit trail: who triggered it, which token (by hash), and which runs were created.
	if s.reconciler.Recorder != nil {
		runNames := make([]string, len(runs))
		for i, r := range runs {
			runNames[i] = r.Name
		}
		tokenHash := fmt.Sprintf("%x", sha256.Sum256([]byte(token)))
		s.reconciler.Recorder.Eventf(trigger, corev1.EventTypeNormal, "PipelineFired",
			"fired by %s (token: %s...) → runs: %s",
			clientIP, tokenHash[:12], strings.Join(runNames, ", "))
	}

	switch mode {
	case "sync":
		s.handleSync(w, r, runs)
		return
	case "sse":
		s.handleSSE(w, r, runs, streamKey)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"fired":   true,
		"firedAt": now.Format(time.RFC3339),
		"trigger": name,
		"targets": len(runs),
	})
}

// handleSync waits for the single dispatched run to finish and writes the result.
func (s *TriggerWebhookServer) handleSync(w http.ResponseWriter, r *http.Request, runs []*kubeswarmv1alpha1.SwarmRun) {
	if len(runs) != 1 {
		http.Error(w, "sync mode requires exactly one target team", http.StatusBadRequest)
		return
	}

	timeout := parseSyncTimeout(r)

	syncCtx, cancel := context.WithTimeout(r.Context(), timeout)
	defer cancel()

	start := time.Now()
	result, err := s.waitForRun(syncCtx, runs[0])
	if err != nil {
		if syncCtx.Err() != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusGatewayTimeout)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"error":      fmt.Sprintf("timed out after %s waiting for run to complete", timeout),
				"durationMs": time.Since(start).Milliseconds(),
			})
			return
		}
		log.FromContext(syncCtx).Error(err, "waiting for run in sync mode", "run", runs[0].Name)
		http.Error(w, "error waiting for run to complete", http.StatusInternalServerError)
		return
	}

	result.DurationMs = time.Since(start).Milliseconds()

	status := http.StatusOK
	if result.Status == "failed" {
		status = http.StatusInternalServerError
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(result)
}

// sseMsg is a single SSE event payload used internally.
type sseMsg struct {
	event string
	data  any
}

// handleSSE streams Server-Sent Events as the run progresses.
// streamKey, if non-empty, enables token-level streaming.
func (s *TriggerWebhookServer) handleSSE(w http.ResponseWriter, r *http.Request, runs []*kubeswarmv1alpha1.SwarmRun, streamKey string) {
	// H6: cap concurrent SSE connections to prevent goroutine + Watch explosion.
	select {
	case <-s.sseSlots:
		defer func() { s.sseSlots <- struct{}{} }()
	default:
		http.Error(w, "too many concurrent SSE connections", http.StatusServiceUnavailable)
		return
	}

	if len(runs) != 1 {
		http.Error(w, "sse mode requires exactly one target team", http.StatusBadRequest)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	timeout := parseSyncTimeout(r)

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	sseCtx, cancel := context.WithTimeout(r.Context(), timeout)
	defer cancel()

	// If streaming is enabled, drain token chunks from the Redis List in a goroutine.
	tokenCh := s.startTokenStream(sseCtx, streamKey)

	// H5: watch for run status changes instead of polling every 500ms.
	run := runs[0]
	watcher, err := s.watcher.Watch(sseCtx, &kubeswarmv1alpha1.SwarmRunList{},
		client.InNamespace(run.Namespace),
		client.MatchingFields{"metadata.name": run.Name},
	)
	if err != nil {
		sseEvent(w, flusher, "error", map[string]any{"error": fmt.Sprintf("watch error: %v", err)})
		return
	}
	defer watcher.Stop()

	start := time.Now()
	stepPhases := map[string]kubeswarmv1alpha1.SwarmFlowStepPhase{}

	// Emit any step/terminal events that may have already happened before the watch started.
	runKey := types.NamespacedName{Name: run.Name, Namespace: run.Namespace}
	if current := s.getRun(sseCtx, runKey); current != nil {
		s.emitStepEvents(w, flusher, current.Status.Steps, stepPhases)
		if s.emitRunTerminal(w, flusher, current, tokenCh, start) {
			return
		}
	}

	for {
		// Drain any pending token chunks before blocking.
		drainTokens(w, flusher, &tokenCh)

		select {
		case <-sseCtx.Done():
			sseEvent(w, flusher, "error", map[string]any{"error": fmt.Sprintf("timed out after %s", timeout)})
			return
		case msg, ok := <-tokenCh:
			if ok {
				sseEvent(w, flusher, msg.event, msg.data)
			}
		case event, ok := <-watcher.ResultChan():
			if !ok {
				sseEvent(w, flusher, "error", map[string]any{"error": "watch channel closed unexpectedly"})
				return
			}
			current, ok := event.Object.(*kubeswarmv1alpha1.SwarmRun)
			if !ok {
				continue
			}
			s.emitStepEvents(w, flusher, current.Status.Steps, stepPhases)
			if s.emitRunTerminal(w, flusher, current, tokenCh, start) {
				return
			}
		}
	}
}

// getRun fetches the current state of a run from the cache. Returns nil on error.
func (s *TriggerWebhookServer) getRun(ctx context.Context, key types.NamespacedName) *kubeswarmv1alpha1.SwarmRun {
	run := &kubeswarmv1alpha1.SwarmRun{}
	if err := s.reconciler.Get(ctx, key, run); err != nil {
		return nil
	}
	return run
}

// parseSyncTimeout reads the ?timeout query parameter and clamps it to maxSyncTimeout.
func parseSyncTimeout(r *http.Request) time.Duration {
	timeout := defaultSyncTimeout
	if raw := r.URL.Query().Get("timeout"); raw != "" {
		if d, err := time.ParseDuration(raw); err == nil {
			timeout = d
		}
	}
	if timeout > maxSyncTimeout {
		return maxSyncTimeout
	}
	return timeout
}

// startTokenStream starts a goroutine that reads token chunks from the streaming
// channel identified by streamKey and forwards them as sseMsg values. Returns a
// closed channel when streamKey is empty or no queue is configured (no-op).
func (s *TriggerWebhookServer) startTokenStream(ctx context.Context, streamKey string) chan sseMsg {
	ch := make(chan sseMsg, 256)
	if streamKey == "" || s.stream == nil {
		close(ch)
		return ch
	}
	go func() {
		defer close(ch)
		for {
			chunk, err := s.stream.Read(ctx, streamKey)
			if err != nil {
				return
			}
			if chunk == "" {
				continue
			}
			if chunk == queue.StreamDone {
				return
			}
			select {
			case ch <- sseMsg{"token", map[string]any{"token": chunk}}:
			case <-ctx.Done():
				return
			}
		}
	}()
	return ch
}

// drainTokens non-blockingly reads all pending messages from tokenCh and writes them as SSE events.
// Sets *ch to nil if the channel is closed.
func drainTokens(w http.ResponseWriter, flusher http.Flusher, ch *chan sseMsg) {
	if *ch == nil {
		return
	}
	for {
		select {
		case msg, ok := <-*ch:
			if !ok {
				*ch = nil
				return
			}
			sseEvent(w, flusher, msg.event, msg.data)
		default:
			return
		}
	}
}

// emitStepEvents emits SSE events for any step phase transitions detected since the last call.
func (s *TriggerWebhookServer) emitStepEvents(
	w http.ResponseWriter,
	flusher http.Flusher,
	steps []kubeswarmv1alpha1.SwarmFlowStepStatus,
	stepPhases map[string]kubeswarmv1alpha1.SwarmFlowStepPhase,
) {
	for _, step := range steps {
		if stepPhases[step.Name] == step.Phase {
			continue
		}
		stepPhases[step.Name] = step.Phase
		switch step.Phase {
		case kubeswarmv1alpha1.SwarmFlowStepPhaseRunning:
			sseEvent(w, flusher, "step.started", map[string]any{"step": step.Name, "phase": "Running"})
		case kubeswarmv1alpha1.SwarmFlowStepPhaseSucceeded:
			payload := map[string]any{
				"step":       step.Name,
				"output":     step.Output,
				"durationMs": step.CompletionTime.Sub(step.StartTime.Time).Milliseconds(),
			}
			if step.TokenUsage != nil {
				payload["tokenUsage"] = step.TokenUsage
			}
			sseEvent(w, flusher, "step.completed", payload)
		case kubeswarmv1alpha1.SwarmFlowStepPhaseFailed:
			sseEvent(w, flusher, "step.failed", map[string]any{"step": step.Name})
		}
	}
}

// emitRunTerminal emits the terminal run event if the run has reached a final phase.
// Returns true if a terminal event was emitted (caller should return).
func (s *TriggerWebhookServer) emitRunTerminal(
	w http.ResponseWriter,
	flusher http.Flusher,
	run *kubeswarmv1alpha1.SwarmRun,
	tokenCh chan sseMsg,
	start time.Time,
) bool {
	switch run.Status.Phase {
	case kubeswarmv1alpha1.SwarmRunPhaseSucceeded:
		// Drain remaining token chunks before the final event.
		if tokenCh != nil {
			for msg := range tokenCh {
				sseEvent(w, flusher, msg.event, msg.data)
			}
		}
		payload := map[string]any{
			"status":     "succeeded",
			"output":     run.Status.Output,
			"durationMs": time.Since(start).Milliseconds(),
		}
		if run.Status.TotalTokenUsage != nil {
			payload["tokenUsage"] = run.Status.TotalTokenUsage
		}
		sseEvent(w, flusher, "flow.completed", payload)
		return true
	case kubeswarmv1alpha1.SwarmRunPhaseFailed:
		msg := "run pipeline failed"
		for _, c := range run.Status.Conditions {
			if c.Type == kubeswarmv1alpha1.ConditionReady && c.Status == metav1.ConditionFalse {
				msg = c.Message
				break
			}
		}
		sseEvent(w, flusher, "flow.failed", map[string]any{
			"status":     "failed",
			"error":      msg,
			"durationMs": time.Since(start).Milliseconds(),
		})
		return true
	}
	return false
}

// sseEvent writes a single SSE event and flushes the connection.
func sseEvent(w http.ResponseWriter, flusher http.Flusher, event string, data any) {
	b, _ := json.Marshal(data)
	_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, b)
	flusher.Flush()
}

// syncResult is the response body for sync mode.
type syncResult struct {
	Status     string                        `json:"status"`
	Output     string                        `json:"output,omitempty"`
	Error      string                        `json:"error,omitempty"`
	DurationMs int64                         `json:"durationMs"`
	TokenUsage *kubeswarmv1alpha1.TokenUsage `json:"tokenUsage,omitempty"`
}

// waitForRun blocks until the run reaches a terminal phase or ctx is cancelled.
// H5: uses Watch instead of polling to eliminate per-SSE-client ticker load.
func (s *TriggerWebhookServer) waitForRun(ctx context.Context, run *kubeswarmv1alpha1.SwarmRun) (*syncResult, error) {
	watcher, err := s.watcher.Watch(ctx, &kubeswarmv1alpha1.SwarmRunList{},
		client.InNamespace(run.Namespace),
		client.MatchingFields{"metadata.name": run.Name},
	)
	if err != nil {
		return nil, fmt.Errorf("watch: %w", err)
	}
	defer watcher.Stop()

	// Check current state before entering the watch loop to avoid missing a
	// terminal transition that happened before the watch started.
	key := types.NamespacedName{Name: run.Name, Namespace: run.Namespace}
	if current := s.getRun(ctx, key); current != nil {
		if result := runSyncResult(current); result != nil {
			return result, nil
		}
	}

	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case event, ok := <-watcher.ResultChan():
			if !ok {
				return nil, fmt.Errorf("watch channel closed")
			}
			current, ok := event.Object.(*kubeswarmv1alpha1.SwarmRun)
			if !ok {
				continue
			}
			if result := runSyncResult(current); result != nil {
				return result, nil
			}
		}
	}
}

// runSyncResult returns a syncResult if the run is in a terminal phase, nil otherwise.
func runSyncResult(run *kubeswarmv1alpha1.SwarmRun) *syncResult {
	switch run.Status.Phase {
	case kubeswarmv1alpha1.SwarmRunPhaseSucceeded:
		return &syncResult{Status: "succeeded", Output: run.Status.Output, TokenUsage: run.Status.TotalTokenUsage}
	case kubeswarmv1alpha1.SwarmRunPhaseFailed:
		msg := "run pipeline failed"
		for _, c := range run.Status.Conditions {
			if c.Type == kubeswarmv1alpha1.ConditionReady && c.Status == metav1.ConditionFalse {
				msg = c.Message
				break
			}
		}
		return &syncResult{Status: "failed", Error: msg}
	}
	return nil
}

// validToken checks the provided token against the one stored in the trigger's webhook token Secret.
// Comparison is constant-time to prevent timing-based token brute-forcing.
func (s *TriggerWebhookServer) validToken(ctx context.Context, trigger *kubeswarmv1alpha1.SwarmEvent, token string) bool {
	if token == "" {
		return false
	}
	stored, err := webhookTokenFromSecret(ctx, s.reconciler.Client, trigger)
	if err != nil {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(token), []byte(stored)) == 1
}

// webhookTokenFromSecret reads the token from the trigger's token Secret.
// Secret name: <trigger-name>-webhook-token, key: token.
func webhookTokenFromSecret(ctx context.Context, c client.Client, trigger *kubeswarmv1alpha1.SwarmEvent) (string, error) {
	secret := &corev1.Secret{}
	if err := c.Get(ctx, types.NamespacedName{
		Name:      trigger.Name + "-webhook-token",
		Namespace: trigger.Namespace,
	}, secret); err != nil {
		return "", err
	}
	return string(secret.Data["token"]), nil
}

// bearerToken extracts the Bearer token from the Authorization header.
func bearerToken(r *http.Request) string {
	if token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok {
		return token
	}
	return ""
}
