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

// Package mcpgateway implements the swarm MCP gateway — an http.Handler that exposes
// SwarmAgent capabilities as callable MCP tools over the standard SSE transport.
//
// A single Gateway instance serves all namespaces. Agent-as-tool calls are translated
// into task queue submissions; results are returned as MCP tool responses. The agent
// runtime is unchanged — it never knows whether a task came from a pipeline step or
// a tool call.
package mcpgateway

import (
	"fmt"
	"net/http"
	"strings"
	"sync"

	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/kubeswarm/kubeswarm/pkg/agent/queue"
)

const (
	// maxSSESessions caps the number of concurrent MCP SSE sessions to bound
	// goroutine and memory usage. HTTP 503 is returned when the cap is reached.
	maxSSESessions = 50

	// annotationTeamQueueURL matches the constant in swarmagent_controller.go.
	// It carries the role-specific Redis stream URL for team-managed agents.
	annotationTeamQueueURL = "kubeswarm/team-queue-url"
)

// Gateway is the MCP SSE gateway. It implements http.Handler.
// A single instance is registered as a controller-runtime Runnable and serves
// all namespaces on a dedicated port.
type Gateway struct {
	client       client.Client
	taskQueueURL string // operator's base TASK_QUEUE_URL — used for standalone agents

	// sessions maps sessionID → response channel. handleSSE writes to the SSE stream
	// by reading from the channel; handleMessage sends dispatched responses to it.
	sessions sync.Map // map[string]chan []byte

	// queues caches open TaskQueue clients keyed by URL to avoid per-request
	// connection churn when agents share the same Redis instance with different streams.
	queues sync.Map // map[string]queue.TaskQueue

	// slotsC is a counting semaphore that caps concurrent SSE sessions.
	slotsC chan struct{}
}

// New creates a Gateway. tq is the operator's shared task queue (used as the fallback
// for standalone agents). taskQueueURL is the base connection URL used when opening
// agent-specific queue streams.
func New(c client.Client, tq queue.TaskQueue, taskQueueURL string) *Gateway {
	g := &Gateway{
		client:       c,
		taskQueueURL: taskQueueURL,
		slotsC:       make(chan struct{}, maxSSESessions),
	}
	// Pre-register the shared queue under its URL so we reuse it without a new connection.
	if tq != nil && taskQueueURL != "" {
		g.queues.Store(taskQueueURL, tq)
	}
	return g
}

// GatewayURL returns the base agent URL for the given agent.
// Used by the SwarmAgent reconciler to build the resolved MCPServerSpec.URL.
// The agent runtime MCP client appends /tools/list and /tools/call to this URL.
func GatewayURL(baseURL, namespace, agentName string) string {
	return fmt.Sprintf("%s/namespaces/%s/agents/%s", baseURL, namespace, agentName)
}

// ServeHTTP dispatches incoming requests to the appropriate handler.
//
//	GET  /healthz                                        → 200 OK
//	GET  /namespaces/:ns/agents/:name/sse                → MCP SSE session (external clients)
//	POST /namespaces/:ns/agents/:name/message            → MCP JSON-RPC message (SSE transport)
//	POST /namespaces/:ns/agents/:name/tools/list         → REST tools discovery (agent runtime)
//	POST /namespaces/:ns/agents/:name/tools/call         → REST tool call (agent runtime)
func (g *Gateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/healthz" {
		w.WriteHeader(http.StatusOK)
		return
	}

	ns, agentName, endpoint, ok := parsePath(r.URL.Path)
	if !ok {
		http.NotFound(w, r)
		return
	}

	switch {
	case r.Method == http.MethodGet && endpoint == "sse":
		g.handleSSE(w, r, ns, agentName)
	case r.Method == http.MethodPost && endpoint == "message":
		g.handleMessage(w, r, ns, agentName)
	case r.Method == http.MethodPost && endpoint == "tools/list":
		g.handleRESTToolsList(w, r, ns, agentName)
	case r.Method == http.MethodPost && endpoint == "tools/call":
		g.handleRESTToolsCall(w, r, ns, agentName)
	default:
		http.NotFound(w, r)
	}
}

// parsePath parses /namespaces/:ns/agents/:name/:endpoint.
// The endpoint may be a single segment (e.g. "sse", "message") or two segments
// joined with "/" (e.g. "tools/list", "tools/call").
// Returns ok=false for any path that doesn't match the expected structure.
func parsePath(path string) (ns, agentName, endpoint string, ok bool) {
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	// Minimum: namespaces/:ns/agents/:name/:ep  (5 parts)
	// Maximum: namespaces/:ns/agents/:name/:ep1/:ep2 (6 parts, e.g. tools/list)
	if len(parts) < 5 || len(parts) > 6 || parts[0] != "namespaces" || parts[2] != "agents" {
		return "", "", "", false
	}
	ns, name := parts[1], parts[3]
	if ns == "" || name == "" || parts[4] == "" {
		return "", "", "", false
	}
	ep := parts[4]
	if len(parts) == 6 {
		if parts[5] == "" {
			return "", "", "", false
		}
		ep = parts[4] + "/" + parts[5]
	}
	return ns, name, ep, true
}

// agentQueueURL returns the task queue URL for the target agent.
// Prefers the role-specific URL from the team-queue-url annotation (set on team-managed
// agents by SwarmTeamReconciler); falls back to the operator's default TASK_QUEUE_URL.
func (g *Gateway) agentQueueURL(annotations map[string]string) string {
	if url, ok := annotations[annotationTeamQueueURL]; ok && url != "" {
		return url
	}
	return g.taskQueueURL
}

// queueFor returns (or creates) a TaskQueue for the given URL.
// Results are cached in g.queues to avoid per-request connection overhead.
func (g *Gateway) queueFor(url string) (queue.TaskQueue, error) {
	if v, ok := g.queues.Load(url); ok {
		return v.(queue.TaskQueue), nil
	}
	tq, err := queue.NewQueue(url, 0)
	if err != nil {
		return nil, fmt.Errorf("opening queue for %q: %w", url, err)
	}
	// Store with LoadOrStore to avoid a race where two requests create the same queue.
	actual, _ := g.queues.LoadOrStore(url, tq)
	if actual != tq {
		tq.Close() // we lost the race — close the duplicate
	}
	return actual.(queue.TaskQueue), nil
}
