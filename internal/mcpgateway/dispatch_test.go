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
	"encoding/json"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kubeswarmv1alpha1 "github.com/kubeswarm/kubeswarm/api/v1alpha1"
)

func init() {
	_ = kubeswarmv1alpha1.AddToScheme(scheme.Scheme)
}

func testGateway(agents ...*kubeswarmv1alpha1.SwarmAgent) *Gateway {
	objs := make([]runtime.Object, 0, len(agents))
	for _, a := range agents {
		objs = append(objs, a)
	}
	fc := fake.NewClientBuilder().
		WithScheme(scheme.Scheme).
		WithRuntimeObjects(objs...).
		Build()
	return &Gateway{
		client: fc,
	}
}

func testAgent(name string) *kubeswarmv1alpha1.SwarmAgent {
	return &kubeswarmv1alpha1.SwarmAgent{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Spec: kubeswarmv1alpha1.SwarmAgentSpec{
			Model:  "claude-sonnet-4-6",
			Prompt: &kubeswarmv1alpha1.AgentPrompt{Inline: "test"},
			Capabilities: []kubeswarmv1alpha1.AgentCapability{
				{Name: "search", Description: "Search code", ExposeMCP: true},
			},
		},
		Status: kubeswarmv1alpha1.SwarmAgentStatus{
			ReadyReplicas: 1,
		},
	}
}

// ---------------------------------------------------------------------------
// dispatch - initialize
// ---------------------------------------------------------------------------

func TestDispatch_Initialize(t *testing.T) {
	g := testGateway()
	req := &jsonRPCRequest{Method: "initialize"}
	resp := g.dispatch(context.Background(), req, "default", "test")

	if resp.Error != nil {
		t.Fatalf("unexpected error: %v", resp.Error)
	}
	result, ok := resp.Result.(mcpInitializeResult)
	if !ok {
		t.Fatalf("result type = %T, want mcpInitializeResult", resp.Result)
	}
	if result.ProtocolVersion != "2024-11-05" {
		t.Errorf("protocolVersion = %q, want 2024-11-05", result.ProtocolVersion)
	}
	if result.ServerInfo.Name != "kubeswarm-mcp-gateway" {
		t.Errorf("serverInfo.name = %q, want kubeswarm-mcp-gateway", result.ServerInfo.Name)
	}
}

// ---------------------------------------------------------------------------
// dispatch - ping
// ---------------------------------------------------------------------------

func TestDispatch_Ping(t *testing.T) {
	g := testGateway()
	req := &jsonRPCRequest{Method: "ping"}
	resp := g.dispatch(context.Background(), req, "default", "test")

	if resp.Error != nil {
		t.Fatalf("unexpected error: %v", resp.Error)
	}
	// Ping returns an empty object.
	m, ok := resp.Result.(map[string]any)
	if !ok {
		t.Fatalf("result type = %T, want map[string]any", resp.Result)
	}
	if len(m) != 0 {
		t.Errorf("ping result should be empty, got %v", m)
	}
}

// ---------------------------------------------------------------------------
// dispatch - unknown method
// ---------------------------------------------------------------------------

func TestDispatch_UnknownMethod(t *testing.T) {
	g := testGateway()
	req := &jsonRPCRequest{Method: "resources/list"}
	resp := g.dispatch(context.Background(), req, "default", "test")

	if resp.Error == nil {
		t.Fatal("expected error for unknown method")
	}
	if resp.Error.Code != errCodeNotFound {
		t.Errorf("error code = %d, want %d", resp.Error.Code, errCodeNotFound)
	}
}

// ---------------------------------------------------------------------------
// dispatch - tools/list
// ---------------------------------------------------------------------------

func TestDispatch_ToolsList(t *testing.T) {
	agent := testAgent("my-agent")
	g := testGateway(agent)

	req := &jsonRPCRequest{Method: "tools/list"}
	resp := g.dispatch(context.Background(), req, "default", "my-agent")

	if resp.Error != nil {
		t.Fatalf("unexpected error: %v", resp.Error)
	}
	result, ok := resp.Result.(mcpToolsListResult)
	if !ok {
		t.Fatalf("result type = %T, want mcpToolsListResult", resp.Result)
	}
	if len(result.Tools) != 1 {
		t.Fatalf("tools count = %d, want 1", len(result.Tools))
	}
	if result.Tools[0].Name != "search" {
		t.Errorf("tool name = %q, want search", result.Tools[0].Name)
	}
}

func TestDispatch_ToolsList_AgentNotFound(t *testing.T) {
	g := testGateway() // no agents
	req := &jsonRPCRequest{Method: "tools/list"}
	resp := g.dispatch(context.Background(), req, "default", "nonexistent")

	if resp.Error == nil {
		t.Fatal("expected error for nonexistent agent")
	}
	if resp.Error.Code != errCodeInternal {
		t.Errorf("error code = %d, want %d", resp.Error.Code, errCodeInternal)
	}
}

// ---------------------------------------------------------------------------
// dispatch - tools/call
// ---------------------------------------------------------------------------

func TestDispatch_ToolsCall_AgentNotFound(t *testing.T) {
	g := testGateway()
	params, _ := json.Marshal(mcpToolsCallParams{Name: "search", Arguments: json.RawMessage(`{}`)})
	req := &jsonRPCRequest{Method: "tools/call", Params: params}
	resp := g.dispatch(context.Background(), req, "default", "nonexistent")

	if resp.Error == nil {
		t.Fatal("expected error for nonexistent agent")
	}
}

func TestDispatch_ToolsCall_NoReadyReplicas(t *testing.T) {
	agent := testAgent("down-agent")
	agent.Status.ReadyReplicas = 0
	g := testGateway(agent)

	params, _ := json.Marshal(mcpToolsCallParams{Name: "search", Arguments: json.RawMessage(`{}`)})
	req := &jsonRPCRequest{Method: "tools/call", Params: params}
	resp := g.dispatch(context.Background(), req, "default", "down-agent")

	if resp.Error == nil {
		t.Fatal("expected error for agent with no ready replicas")
	}
	if resp.Error.Code != errCodeNoReplicas {
		t.Errorf("error code = %d, want %d (errCodeNoReplicas)", resp.Error.Code, errCodeNoReplicas)
	}
}

func TestDispatch_ToolsCall_ToolNotExposed(t *testing.T) {
	agent := testAgent("my-agent")
	g := testGateway(agent)

	params, _ := json.Marshal(mcpToolsCallParams{Name: "nonexistent-tool", Arguments: json.RawMessage(`{}`)})
	req := &jsonRPCRequest{Method: "tools/call", Params: params}
	resp := g.dispatch(context.Background(), req, "default", "my-agent")

	if resp.Error == nil {
		t.Fatal("expected error for non-exposed tool")
	}
	if resp.Error.Code != errCodeNotFound {
		t.Errorf("error code = %d, want %d (errCodeNotFound)", resp.Error.Code, errCodeNotFound)
	}
}

func TestDispatch_ToolsCall_InvalidParams(t *testing.T) {
	agent := testAgent("my-agent")
	g := testGateway(agent)

	req := &jsonRPCRequest{Method: "tools/call", Params: json.RawMessage(`not-json`)}
	resp := g.dispatch(context.Background(), req, "default", "my-agent")

	if resp.Error == nil {
		t.Fatal("expected error for invalid params")
	}
	if resp.Error.Code != errCodeInternal {
		t.Errorf("error code = %d, want %d", resp.Error.Code, errCodeInternal)
	}
}
