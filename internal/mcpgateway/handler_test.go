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
	"encoding/json"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/runtime"

	kubeswarmv1alpha1 "github.com/kubeswarm/kubeswarm/api/v1alpha1"
)

// ---------------------------------------------------------------------------
// buildToolList
// ---------------------------------------------------------------------------

func TestBuildToolList_ExposedMCP(t *testing.T) {
	agent := &kubeswarmv1alpha1.SwarmAgent{}
	agent.Spec.Capabilities = []kubeswarmv1alpha1.AgentCapability{
		{Name: "search", Description: "Search code", ExposeMCP: true},
		{Name: "internal-only", Description: "Not exposed", ExposeMCP: false},
	}
	tools := buildToolList(agent)
	if len(tools) != 1 {
		t.Fatalf("got %d tools, want 1", len(tools))
	}
	if tools[0].Name != "search" {
		t.Errorf("tool name = %q, want search", tools[0].Name)
	}
	if tools[0].Description != "Search code" {
		t.Errorf("tool description = %q, want 'Search code'", tools[0].Description)
	}
}

func TestBuildToolList_WithInputSchema(t *testing.T) {
	schema := `{"type":"object","properties":{"query":{"type":"string"}}}`
	agent := &kubeswarmv1alpha1.SwarmAgent{}
	agent.Spec.Capabilities = []kubeswarmv1alpha1.AgentCapability{
		{
			Name:        "search",
			ExposeMCP:   true,
			InputSchema: &runtime.RawExtension{Raw: []byte(schema)},
		},
	}
	tools := buildToolList(agent)
	if len(tools) != 1 {
		t.Fatalf("got %d tools, want 1", len(tools))
	}
	if string(tools[0].InputSchema) != schema {
		t.Errorf("InputSchema = %s, want %s", tools[0].InputSchema, schema)
	}
}

func TestBuildToolList_WithoutInputSchema_DefaultsToObject(t *testing.T) {
	agent := &kubeswarmv1alpha1.SwarmAgent{}
	agent.Spec.Capabilities = []kubeswarmv1alpha1.AgentCapability{
		{Name: "search", ExposeMCP: true},
	}
	tools := buildToolList(agent)
	if len(tools) != 1 {
		t.Fatalf("got %d tools, want 1", len(tools))
	}
	if string(tools[0].InputSchema) != `{"type":"object"}` {
		t.Errorf("InputSchema = %s, want default object schema", tools[0].InputSchema)
	}
}

func TestBuildToolList_EmptyCapabilities(t *testing.T) {
	agent := &kubeswarmv1alpha1.SwarmAgent{}
	tools := buildToolList(agent)
	if len(tools) != 0 {
		t.Errorf("got %d tools for empty capabilities, want 0", len(tools))
	}
}

func TestBuildToolList_NilRawExtension(t *testing.T) {
	agent := &kubeswarmv1alpha1.SwarmAgent{}
	agent.Spec.Capabilities = []kubeswarmv1alpha1.AgentCapability{
		{
			Name:        "search",
			ExposeMCP:   true,
			InputSchema: &runtime.RawExtension{}, // non-nil but empty Raw
		},
	}
	tools := buildToolList(agent)
	if string(tools[0].InputSchema) != `{"type":"object"}` {
		t.Errorf("InputSchema = %s, want default for empty RawExtension", tools[0].InputSchema)
	}
}

// ---------------------------------------------------------------------------
// findCapability
// ---------------------------------------------------------------------------

func TestFindCapability_Found(t *testing.T) {
	agent := &kubeswarmv1alpha1.SwarmAgent{}
	agent.Spec.Capabilities = []kubeswarmv1alpha1.AgentCapability{
		{Name: "search", ExposeMCP: true, Description: "Search"},
		{Name: "review", ExposeMCP: true, Description: "Review"},
	}
	cap, found := findCapability(agent, "review")
	if !found {
		t.Fatal("expected to find capability 'review'")
	}
	if cap.Description != "Review" {
		t.Errorf("cap.Description = %q, want Review", cap.Description)
	}
}

func TestFindCapability_NotFound(t *testing.T) {
	agent := &kubeswarmv1alpha1.SwarmAgent{}
	agent.Spec.Capabilities = []kubeswarmv1alpha1.AgentCapability{
		{Name: "search", ExposeMCP: true},
	}
	_, found := findCapability(agent, "nonexistent")
	if found {
		t.Error("expected not found for nonexistent capability")
	}
}

func TestFindCapability_NotExposedMCP(t *testing.T) {
	agent := &kubeswarmv1alpha1.SwarmAgent{}
	agent.Spec.Capabilities = []kubeswarmv1alpha1.AgentCapability{
		{Name: "search", ExposeMCP: false},
	}
	_, found := findCapability(agent, "search")
	if found {
		t.Error("expected not found for capability with ExposeMCP=false")
	}
}

// ---------------------------------------------------------------------------
// buildPrompt
// ---------------------------------------------------------------------------

func TestBuildPrompt_WithDescription(t *testing.T) {
	cap := kubeswarmv1alpha1.AgentCapability{
		Name:        "search",
		Description: "Search the codebase",
	}
	args := json.RawMessage(`{"query":"hello"}`)
	got := buildPrompt(cap, args)
	want := "Tool: search\nDescription: Search the codebase\nArguments: {\"query\":\"hello\"}"
	if got != want {
		t.Errorf("buildPrompt = %q, want %q", got, want)
	}
}

func TestBuildPrompt_WithoutDescription(t *testing.T) {
	cap := kubeswarmv1alpha1.AgentCapability{Name: "search"}
	args := json.RawMessage(`{"query":"hello"}`)
	got := buildPrompt(cap, args)
	want := "Tool: search\nArguments: {\"query\":\"hello\"}"
	if got != want {
		t.Errorf("buildPrompt = %q, want %q", got, want)
	}
}

func TestBuildPrompt_NullArgs(t *testing.T) {
	cap := kubeswarmv1alpha1.AgentCapability{Name: "ping"}
	got := buildPrompt(cap, json.RawMessage("null"))
	want := "Tool: ping\nArguments: {}"
	if got != want {
		t.Errorf("buildPrompt(null) = %q, want %q", got, want)
	}
}

func TestBuildPrompt_EmptyArgs(t *testing.T) {
	cap := kubeswarmv1alpha1.AgentCapability{Name: "ping"}
	got := buildPrompt(cap, nil)
	want := "Tool: ping\nArguments: {}"
	if got != want {
		t.Errorf("buildPrompt(nil) = %q, want %q", got, want)
	}
}

// ---------------------------------------------------------------------------
// agentTimeout
// ---------------------------------------------------------------------------

func TestAgentTimeout_Default(t *testing.T) {
	agent := &kubeswarmv1alpha1.SwarmAgent{}
	got := agentTimeout(agent)
	if got != defaultToolTimeout {
		t.Errorf("agentTimeout(no guardrails) = %v, want %v", got, defaultToolTimeout)
	}
}

func TestAgentTimeout_NilGuardrails(t *testing.T) {
	agent := &kubeswarmv1alpha1.SwarmAgent{}
	agent.Spec.Guardrails = nil
	got := agentTimeout(agent)
	if got != defaultToolTimeout {
		t.Errorf("agentTimeout(nil guardrails) = %v, want %v", got, defaultToolTimeout)
	}
}

func TestAgentTimeout_NilLimits(t *testing.T) {
	agent := &kubeswarmv1alpha1.SwarmAgent{}
	agent.Spec.Guardrails = &kubeswarmv1alpha1.AgentGuardrails{}
	got := agentTimeout(agent)
	if got != defaultToolTimeout {
		t.Errorf("agentTimeout(nil limits) = %v, want %v", got, defaultToolTimeout)
	}
}

func TestAgentTimeout_ZeroTimeout(t *testing.T) {
	agent := &kubeswarmv1alpha1.SwarmAgent{}
	agent.Spec.Guardrails = &kubeswarmv1alpha1.AgentGuardrails{
		Limits: &kubeswarmv1alpha1.GuardrailLimits{TimeoutSeconds: 0},
	}
	got := agentTimeout(agent)
	if got != defaultToolTimeout {
		t.Errorf("agentTimeout(0) = %v, want %v", got, defaultToolTimeout)
	}
}

func TestAgentTimeout_CustomTimeout(t *testing.T) {
	agent := &kubeswarmv1alpha1.SwarmAgent{}
	agent.Spec.Guardrails = &kubeswarmv1alpha1.AgentGuardrails{
		Limits: &kubeswarmv1alpha1.GuardrailLimits{TimeoutSeconds: 60},
	}
	got := agentTimeout(agent)
	want := 60 * time.Second
	if got != want {
		t.Errorf("agentTimeout(60) = %v, want %v", got, want)
	}
}

// ---------------------------------------------------------------------------
// errResponse
// ---------------------------------------------------------------------------

func TestErrResponse(t *testing.T) {
	resp := errResponse(errCodeNotFound, "tool not found")
	if resp.Error == nil {
		t.Fatal("expected error in response")
	}
	if resp.Error.Code != errCodeNotFound {
		t.Errorf("error code = %d, want %d", resp.Error.Code, errCodeNotFound)
	}
	if resp.Error.Message != "tool not found" {
		t.Errorf("error message = %q, want 'tool not found'", resp.Error.Message)
	}
	if resp.Result != nil {
		t.Error("expected nil result on error response")
	}
}

// ---------------------------------------------------------------------------
// newSessionID
// ---------------------------------------------------------------------------

func TestNewSessionID_Length(t *testing.T) {
	id := newSessionID()
	// 8 random bytes = 16 hex chars
	if len(id) != 16 {
		t.Errorf("sessionID length = %d, want 16", len(id))
	}
}

func TestNewSessionID_Unique(t *testing.T) {
	ids := make(map[string]struct{}, 100)
	for range 100 {
		id := newSessionID()
		if _, dup := ids[id]; dup {
			t.Fatalf("duplicate sessionID: %s", id)
		}
		ids[id] = struct{}{}
	}
}
