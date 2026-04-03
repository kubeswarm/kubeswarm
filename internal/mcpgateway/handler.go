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

package mcpgateway

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	kubeswarmv1alpha1 "github.com/kubeswarm/kubeswarm/api/v1alpha1"
)

// MCP JSON-RPC error codes (subset of the MCP specification).
const (
	errCodeTimeout    = -32001 // swarm extension: agent tool call timed out
	errCodeNoReplicas = -32002 // swarm extension: agent has no ready replicas
	errCodeNotFound   = -32601 // JSON-RPC: method / tool not found
	errCodeInternal   = -32603 // JSON-RPC: internal error
)

// defaultToolTimeout is used when the target agent has no explicit timeout configured.
const defaultToolTimeout = 120 * time.Second

// pollInterval is how often Results() is polled while waiting for a tool call to complete.
const pollInterval = 500 * time.Millisecond

// — JSON-RPC types ————————————————————————————————————————————————————————————

type jsonRPCRequest struct {
	JSONRPC string           `json:"jsonrpc"`
	ID      *json.RawMessage `json:"id,omitempty"` // nil for notifications
	Method  string           `json:"method"`
	Params  json.RawMessage  `json:"params,omitempty"`
}

type jsonRPCResponse struct {
	JSONRPC string           `json:"jsonrpc"`
	ID      *json.RawMessage `json:"id"`
	Result  any              `json:"result,omitempty"`
	Error   *jsonRPCError    `json:"error,omitempty"`
}

type jsonRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// — MCP protocol types ————————————————————————————————————————————————————————

type mcpServerInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type mcpCapabilities struct {
	Tools map[string]any `json:"tools"`
}

type mcpInitializeResult struct {
	ProtocolVersion string          `json:"protocolVersion"`
	Capabilities    mcpCapabilities `json:"capabilities"`
	ServerInfo      mcpServerInfo   `json:"serverInfo"`
}

type mcpTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"inputSchema,omitempty"`
}

type mcpToolsListResult struct {
	Tools []mcpTool `json:"tools"`
}

type mcpToolsCallParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

type mcpContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type mcpToolsCallResult struct {
	Content []mcpContent `json:"content"`
	IsError bool         `json:"isError,omitempty"`
}

// — SSE session handling ——————————————————————————————————————————————————————

// handleSSE establishes an MCP SSE session for the named agent.
//
// Protocol flow:
//  1. Acquire a session slot (cap: maxSSESessions).
//  2. Generate a session ID and register a response channel.
//  3. Send the MCP `endpoint` event so the client knows where to POST messages.
//  4. Loop forwarding responses from the channel to the SSE stream until the client
//     disconnects or the context is cancelled.
func (g *Gateway) handleSSE(w http.ResponseWriter, r *http.Request, ns, agentName string) {
	logger := log.FromContext(r.Context()).WithValues("ns", ns, "agent", agentName)

	// Enforce session cap.
	select {
	case g.slotsC <- struct{}{}:
		defer func() { <-g.slotsC }()
	default:
		http.Error(w, "too many concurrent MCP sessions", http.StatusServiceUnavailable)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	// Generate a unique session ID.
	sessionID := newSessionID()

	// Register the response channel before sending the endpoint event, so
	// a fast client that immediately POSTs a message won't find a missing channel.
	respCh := make(chan []byte, 8)
	g.sessions.Store(sessionID, respCh)
	defer func() {
		g.sessions.Delete(sessionID)
		close(respCh)
	}()

	// SSE headers.
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	// Send the endpoint event. The client will POST JSON-RPC messages to this URL.
	endpointPath := fmt.Sprintf("/namespaces/%s/agents/%s/message?sessionId=%s", ns, agentName, sessionID)
	if _, err := fmt.Fprintf(w, "event: endpoint\ndata: %s\n\n", endpointPath); err != nil {
		return
	}
	flusher.Flush()

	logger.V(1).Info("MCP SSE session started", "sessionId", sessionID)

	// Forward responses to the SSE stream until the client disconnects.
	for {
		select {
		case <-r.Context().Done():
			logger.V(1).Info("MCP SSE session closed", "sessionId", sessionID)
			return
		case msg, open := <-respCh:
			if !open {
				return
			}
			if _, err := fmt.Fprintf(w, "event: message\ndata: %s\n\n", msg); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

// — JSON-RPC message dispatch ————————————————————————————————————————————————

// handleMessage processes one JSON-RPC request POSTed to the message endpoint.
// It looks up the session channel and sends the response back over the SSE stream.
func (g *Gateway) handleMessage(w http.ResponseWriter, r *http.Request, ns, agentName string) {
	sessionID := r.URL.Query().Get("sessionId")
	if sessionID == "" {
		http.Error(w, "missing sessionId", http.StatusBadRequest)
		return
	}

	v, ok := g.sessions.Load(sessionID)
	if !ok {
		http.Error(w, "session not found", http.StatusNotFound)
		return
	}
	respCh := v.(chan []byte)

	var req jsonRPCRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON-RPC request", http.StatusBadRequest)
		return
	}

	// Notifications have no id and require no response.
	if req.ID == nil {
		w.WriteHeader(http.StatusAccepted)
		return
	}

	resp := g.dispatch(r.Context(), &req, ns, agentName)
	resp.JSONRPC = "2.0"
	resp.ID = req.ID

	data, err := json.Marshal(resp)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	// Non-blocking send: if the SSE goroutine is gone, drop the response.
	select {
	case respCh <- data:
	default:
	}

	w.WriteHeader(http.StatusAccepted)
}

// dispatch routes a JSON-RPC method to its handler and returns a response.
func (g *Gateway) dispatch(ctx context.Context, req *jsonRPCRequest, ns, agentName string) jsonRPCResponse {
	switch req.Method {
	case "initialize":
		return jsonRPCResponse{Result: mcpInitializeResult{
			ProtocolVersion: "2024-11-05",
			Capabilities:    mcpCapabilities{Tools: map[string]any{}},
			ServerInfo:      mcpServerInfo{Name: "kubeswarm-mcp-gateway", Version: "0.1.0"},
		}}

	case "ping":
		return jsonRPCResponse{Result: map[string]any{}}

	case "tools/list":
		return g.handleToolsList(ctx, ns, agentName)

	case "tools/call":
		return g.handleToolsCall(ctx, req.Params, ns, agentName)

	default:
		return jsonRPCResponse{Error: &jsonRPCError{
			Code:    errCodeNotFound,
			Message: fmt.Sprintf("unknown method %q", req.Method),
		}}
	}
}

// — tools/list ———————————————————————————————————————————————————————————————

func (g *Gateway) handleToolsList(ctx context.Context, ns, agentName string) jsonRPCResponse {
	agent := &kubeswarmv1alpha1.SwarmAgent{}
	if err := g.client.Get(ctx, client.ObjectKey{Namespace: ns, Name: agentName}, agent); err != nil {
		return errResponse(errCodeInternal, fmt.Sprintf("agent %q not found: %v", agentName, err))
	}

	tools := buildToolList(agent)
	return jsonRPCResponse{Result: mcpToolsListResult{Tools: tools}}
}

// buildToolList converts SwarmAgent capabilities with exposeMCP:true into MCP tool descriptors.
func buildToolList(agent *kubeswarmv1alpha1.SwarmAgent) []mcpTool {
	var tools []mcpTool
	for _, cap := range agent.Spec.Capabilities {
		if !cap.ExposeMCP {
			continue
		}
		t := mcpTool{
			Name:        cap.Name,
			Description: cap.Description,
		}
		if cap.InputSchema != nil && len(cap.InputSchema.Raw) > 0 {
			t.InputSchema = json.RawMessage(cap.InputSchema.Raw)
		} else {
			// Default to an open object schema when none is declared.
			t.InputSchema = json.RawMessage(`{"type":"object"}`)
		}
		tools = append(tools, t)
	}
	return tools
}

// — tools/call ———————————————————————————————————————————————————————————————

func (g *Gateway) handleToolsCall(ctx context.Context, params json.RawMessage, ns, agentName string) jsonRPCResponse {
	var p mcpToolsCallParams
	if err := json.Unmarshal(params, &p); err != nil {
		return errResponse(errCodeInternal, "invalid tools/call params")
	}

	// Fetch the target agent.
	agent := &kubeswarmv1alpha1.SwarmAgent{}
	if err := g.client.Get(ctx, client.ObjectKey{Namespace: ns, Name: agentName}, agent); err != nil {
		return errResponse(errCodeInternal, fmt.Sprintf("agent %q not found: %v", agentName, err))
	}

	// Circuit breaker: refuse if no pods are ready.
	if agent.Status.ReadyReplicas == 0 {
		return errResponse(errCodeNoReplicas,
			fmt.Sprintf("agent %q has no ready replicas", agentName))
	}

	// Verify the requested tool is exposed.
	cap, found := findCapability(agent, p.Name)
	if !found {
		return errResponse(errCodeNotFound,
			fmt.Sprintf("agent %q does not expose capability %q", agentName, p.Name))
	}

	// Build the prompt from the capability description + caller arguments.
	prompt := buildPrompt(cap, p.Arguments)

	// Resolve queue URL: prefer team-specific stream, fall back to default.
	queueURL := g.agentQueueURL(agent.Annotations)
	if queueURL == "" {
		return errResponse(errCodeInternal, "no task queue URL configured")
	}

	tq, err := g.queueFor(queueURL)
	if err != nil {
		return errResponse(errCodeInternal, fmt.Sprintf("queue unavailable: %v", err))
	}

	// Submit the task.
	taskID, err := tq.Submit(ctx, prompt, map[string]string{
		"source":    "mcp-gateway",
		"agent":     agentName,
		"namespace": ns,
		"tool":      p.Name,
	})
	if err != nil {
		return errResponse(errCodeInternal, fmt.Sprintf("task submission failed: %v", err))
	}

	// Poll for result with a timeout derived from the agent's configured limit.
	timeout := agentTimeout(agent)
	deadline := time.Now().Add(timeout)

	for {
		if time.Now().After(deadline) {
			return errResponse(errCodeTimeout, fmt.Sprintf("agent tool call timed out after %s", timeout))
		}

		results, err := tq.Results(ctx, []string{taskID})
		if err == nil && len(results) > 0 {
			r := results[0]
			if r.Error != "" {
				return jsonRPCResponse{Result: mcpToolsCallResult{
					Content: []mcpContent{{Type: "text", Text: r.Error}},
					IsError: true,
				}}
			}
			return jsonRPCResponse{Result: mcpToolsCallResult{
				Content: []mcpContent{{Type: "text", Text: r.Output}},
			}}
		}

		select {
		case <-ctx.Done():
			return errResponse(errCodeInternal, "request cancelled")
		case <-time.After(pollInterval):
		}
	}
}

// — helpers ——————————————————————————————————————————————————————————————————

func findCapability(agent *kubeswarmv1alpha1.SwarmAgent, toolName string) (kubeswarmv1alpha1.AgentCapability, bool) {
	for _, cap := range agent.Spec.Capabilities {
		if cap.ExposeMCP && cap.Name == toolName {
			return cap, true
		}
	}
	return kubeswarmv1alpha1.AgentCapability{}, false
}

// buildPrompt formats a tool call as a plain-text prompt for the target agent.
// The agent's system prompt already describes its capabilities; we just need to
// pass the tool name and arguments clearly.
func buildPrompt(cap kubeswarmv1alpha1.AgentCapability, args json.RawMessage) string {
	argsStr := string(args)
	if argsStr == "" || argsStr == "null" {
		argsStr = "{}"
	}
	if cap.Description != "" {
		return fmt.Sprintf("Tool: %s\nDescription: %s\nArguments: %s", cap.Name, cap.Description, argsStr)
	}
	return fmt.Sprintf("Tool: %s\nArguments: %s", cap.Name, argsStr)
}

// agentTimeout returns the per-task timeout for an agent.
func agentTimeout(agent *kubeswarmv1alpha1.SwarmAgent) time.Duration {
	if agent.Spec.Guardrails != nil &&
		agent.Spec.Guardrails.Limits != nil &&
		agent.Spec.Guardrails.Limits.TimeoutSeconds > 0 {
		return time.Duration(agent.Spec.Guardrails.Limits.TimeoutSeconds) * time.Second
	}
	return defaultToolTimeout
}

func errResponse(code int, msg string) jsonRPCResponse {
	return jsonRPCResponse{Error: &jsonRPCError{Code: code, Message: msg}}
}

// — REST handlers (used by the agent runtime MCP client) ———————————————————————
//
// The agent runtime calls POST .../tools/list and POST .../tools/call directly
// over plain HTTP — no SSE session required.

// handleRESTToolsList returns the agent's exposed MCP tools as a JSON object.
// Response: {"tools": [...]}
func (g *Gateway) handleRESTToolsList(w http.ResponseWriter, r *http.Request, ns, agentName string) {
	agent := &kubeswarmv1alpha1.SwarmAgent{}
	if err := g.client.Get(r.Context(), client.ObjectKey{Namespace: ns, Name: agentName}, agent); err != nil {
		http.Error(w, fmt.Sprintf("agent %q not found", agentName), http.StatusNotFound)
		return
	}
	tools := buildToolList(agent)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(struct {
		Tools []mcpTool `json:"tools"`
	}{Tools: tools})
}

// handleRESTToolsCall executes a tool call synchronously over plain HTTP.
// Request:  {"name": "tool_name", "arguments": {...}}
// Response: {"content": [{"type": "text", "text": "..."}]}
func (g *Gateway) handleRESTToolsCall(w http.ResponseWriter, r *http.Request, ns, agentName string) {
	var p mcpToolsCallParams
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	params, _ := json.Marshal(p)
	result := g.handleToolsCall(r.Context(), params, ns, agentName)

	w.Header().Set("Content-Type", "application/json")
	if result.Error != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(result.Error)
		return
	}
	_ = json.NewEncoder(w).Encode(result.Result)
}

func newSessionID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
