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
	"slices"
	"strings"
	"testing"

	"github.com/kubeswarm/kubeswarm/pkg/agent/config"
)

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func testGatewayHandler(caps []config.GatewayCapabilityConfig, cfg *config.GatewayRuntimeConfig) *gatewayHandler {
	return &gatewayHandler{
		capabilities: caps,
		config:       cfg,
		agentName:    "gateway-agent",
		namespace:    "default",
		queueURL:     "", // empty - dispatch validation should fail before queue submission
	}
}

func mustUnmarshalSearchResult(t *testing.T, raw string) registrySearchResult {
	t.Helper()
	var r registrySearchResult
	if err := json.Unmarshal([]byte(raw), &r); err != nil {
		t.Fatalf("unmarshal search result: %v", err)
	}
	return r
}

// registrySearchResult is the expected JSON shape returned by registrySearch.
type registrySearchResult struct {
	Matches      []registrySearchMatch `json:"capabilities"`
	TotalMatches int                   `json:"totalMatches"`
	RateLimited  bool                  `json:"rateLimited,omitempty"`
	Note         string                `json:"note,omitempty"`
}

type registrySearchMatch struct {
	Name          string   `json:"name"`
	Description   string   `json:"description"`
	Agent         string   `json:"agent"`
	Namespace     string   `json:"namespace"`
	Tags          []string `json:"tags"`
	ReadyReplicas int32    `json:"readyReplicas"`
}

// ---------------------------------------------------------------------------
// registry_search tests
// ---------------------------------------------------------------------------

func TestRegistrySearch_ReturnsMatchingCapabilities(t *testing.T) {
	h := testGatewayHandler(
		[]config.GatewayCapabilityConfig{
			{Name: "code-review", Description: "Reviews code for quality", Agent: "reviewer", Namespace: "default", Tags: []string{"code"}, ReadyReplicas: 2},
			{Name: "research", Description: "Performs research tasks", Agent: "researcher", Namespace: "default", Tags: []string{"research"}, ReadyReplicas: 1},
			{Name: "code-gen", Description: "Generates code from specs", Agent: "coder", Namespace: "default", Tags: []string{"code", "generate"}, ReadyReplicas: 3},
		},
		&config.GatewayRuntimeConfig{MaxResultsPerSearch: 10},
	)

	input := json.RawMessage(`{"query":"code"}`)
	result, err := h.registrySearch(input)
	if err != nil {
		t.Fatalf("registrySearch error: %v", err)
	}

	r := mustUnmarshalSearchResult(t, result)
	if r.TotalMatches != 2 {
		t.Errorf("totalMatches = %d, want 2", r.TotalMatches)
	}
	if len(r.Matches) != 2 {
		t.Fatalf("matches count = %d, want 2", len(r.Matches))
	}

	names := map[string]bool{}
	for _, m := range r.Matches {
		names[m.Name] = true
	}
	if !names["code-review"] {
		t.Error("expected code-review in matches")
	}
	if !names["code-gen"] {
		t.Error("expected code-gen in matches")
	}
	if names["research"] {
		t.Error("research should not match query 'code'")
	}
}

func TestRegistrySearch_CapsAtMaxResultsPerSearch(t *testing.T) {
	caps := make([]config.GatewayCapabilityConfig, 5)
	for i := range caps {
		caps[i] = config.GatewayCapabilityConfig{
			Name:          "agent-" + string(rune('a'+i)),
			Description:   "Does stuff",
			Agent:         "agent-" + string(rune('a'+i)),
			Namespace:     "default",
			Tags:          []string{"general"},
			ReadyReplicas: 1,
		}
	}

	h := testGatewayHandler(caps, &config.GatewayRuntimeConfig{MaxResultsPerSearch: 2})

	input := json.RawMessage(`{"query":"stuff"}`)
	result, err := h.registrySearch(input)
	if err != nil {
		t.Fatalf("registrySearch error: %v", err)
	}

	r := mustUnmarshalSearchResult(t, result)
	if len(r.Matches) != 2 {
		t.Errorf("matches count = %d, want 2 (capped by MaxResultsPerSearch)", len(r.Matches))
	}
	if r.TotalMatches != 5 {
		t.Errorf("totalMatches = %d, want 5 (all matching before cap)", r.TotalMatches)
	}
}

func TestRegistrySearch_TagFilter(t *testing.T) {
	h := testGatewayHandler(
		[]config.GatewayCapabilityConfig{
			{Name: "scanner", Description: "Scans for vulnerabilities", Agent: "sec-agent", Namespace: "default", Tags: []string{"security", "scan"}, ReadyReplicas: 1},
			{Name: "formatter", Description: "Formats code", Agent: "fmt-agent", Namespace: "default", Tags: []string{"code", "format"}, ReadyReplicas: 1},
			{Name: "auditor", Description: "Audits security posture", Agent: "audit-agent", Namespace: "default", Tags: []string{"security", "audit"}, ReadyReplicas: 1},
		},
		&config.GatewayRuntimeConfig{MaxResultsPerSearch: 10},
	)

	input := json.RawMessage(`{"query":"scan","tags":["security"]}`)
	result, err := h.registrySearch(input)
	if err != nil {
		t.Fatalf("registrySearch error: %v", err)
	}

	r := mustUnmarshalSearchResult(t, result)
	// Only capabilities with the "security" tag should be returned.
	for _, m := range r.Matches {
		hasSecurity := slices.Contains(m.Tags, "security")
		if !hasSecurity {
			t.Errorf("match %q does not have 'security' tag", m.Name)
		}
	}
	if r.TotalMatches < 1 {
		t.Errorf("expected at least 1 match with security tag, got %d", r.TotalMatches)
	}
}

func TestRegistrySearch_EmptyQueryAndTags_RejectsWithError(t *testing.T) {
	caps := []config.GatewayCapabilityConfig{
		{Name: "alpha", Description: "Alpha agent", Agent: "a", Namespace: "default", Tags: []string{"a"}, ReadyReplicas: 1},
	}
	h := testGatewayHandler(caps, &config.GatewayRuntimeConfig{MaxResultsPerSearch: 10, MaxSearchCalls: 3})

	input := json.RawMessage(`{"query":""}`)
	_, err := h.registrySearch(input)
	if err == nil {
		t.Fatal("expected error for empty query and tags, got nil")
	}
	if !strings.Contains(err.Error(), "query or tags must be provided") {
		t.Errorf("error should mention missing query/tags, got: %s", err.Error())
	}
	// Empty-input rejection must not burn a search-quota slot.
	if h.searchCount != 0 {
		t.Errorf("searchCount = %d after validation failure, want 0", h.searchCount)
	}
}

func TestRegistrySearch_TagsOnly_ReturnsMatches(t *testing.T) {
	caps := []config.GatewayCapabilityConfig{
		{Name: "alpha", Description: "Alpha agent", Agent: "a", Namespace: "default", Tags: []string{"security"}, ReadyReplicas: 1},
		{Name: "beta", Description: "Beta agent", Agent: "b", Namespace: "default", Tags: []string{"code"}, ReadyReplicas: 1},
	}
	h := testGatewayHandler(caps, &config.GatewayRuntimeConfig{MaxResultsPerSearch: 10, MaxSearchCalls: 3})

	input := json.RawMessage(`{"tags":["security"]}`)
	result, err := h.registrySearch(input)
	if err != nil {
		t.Fatalf("registrySearch error: %v", err)
	}
	r := mustUnmarshalSearchResult(t, result)
	if r.TotalMatches != 1 {
		t.Errorf("totalMatches = %d, want 1 (tag-only match)", r.TotalMatches)
	}
	if len(r.Matches) != 1 || r.Matches[0].Name != "alpha" {
		t.Errorf("matches = %v, want [alpha]", r.Matches)
	}
}

func TestRegistrySearch_MalformedJSON_DoesNotBurnQuota(t *testing.T) {
	caps := []config.GatewayCapabilityConfig{
		{Name: "alpha", Description: "Alpha agent", Agent: "a", Namespace: "default", Tags: []string{"a"}, ReadyReplicas: 1},
	}
	h := testGatewayHandler(caps, &config.GatewayRuntimeConfig{MaxResultsPerSearch: 10, MaxSearchCalls: 3})

	input := json.RawMessage(`{not-valid-json`)
	_, err := h.registrySearch(input)
	if err == nil {
		t.Fatal("expected error for malformed JSON, got nil")
	}
	if h.searchCount != 0 {
		t.Errorf("malformed input must not consume search quota; searchCount = %d, want 0", h.searchCount)
	}
}

func TestRegistrySearch_NoMatches(t *testing.T) {
	h := testGatewayHandler(
		[]config.GatewayCapabilityConfig{
			{Name: "code-review", Description: "Reviews code", Agent: "reviewer", Namespace: "default", Tags: []string{"code"}, ReadyReplicas: 1},
		},
		&config.GatewayRuntimeConfig{MaxResultsPerSearch: 10},
	)

	input := json.RawMessage(`{"query":"nonexistent-capability-xyz"}`)
	result, err := h.registrySearch(input)
	if err != nil {
		t.Fatalf("registrySearch error: %v", err)
	}

	r := mustUnmarshalSearchResult(t, result)
	if r.TotalMatches != 0 {
		t.Errorf("totalMatches = %d, want 0", r.TotalMatches)
	}
	if len(r.Matches) != 0 {
		t.Errorf("matches count = %d, want 0", len(r.Matches))
	}
}

func TestRegistrySearch_ShowsTruncationNote(t *testing.T) {
	caps := make([]config.GatewayCapabilityConfig, 5)
	for i := range caps {
		caps[i] = config.GatewayCapabilityConfig{
			Name:          "task-agent",
			Description:   "Handles tasks",
			Agent:         "task-agent-" + string(rune('a'+i)),
			Namespace:     "default",
			Tags:          []string{"task"},
			ReadyReplicas: 1,
		}
	}

	h := testGatewayHandler(caps, &config.GatewayRuntimeConfig{MaxResultsPerSearch: 2})

	input := json.RawMessage(`{"query":"task"}`)
	result, err := h.registrySearch(input)
	if err != nil {
		t.Fatalf("registrySearch error: %v", err)
	}

	r := mustUnmarshalSearchResult(t, result)
	if r.Note == "" {
		t.Error("expected non-empty note when totalMatches > returned count")
	}
	if r.TotalMatches <= len(r.Matches) {
		t.Errorf("totalMatches (%d) should exceed returned matches (%d) for truncation note to apply", r.TotalMatches, len(r.Matches))
	}
}

// ---------------------------------------------------------------------------
// dispatch tests
// ---------------------------------------------------------------------------

func TestDispatch_SelfDispatchBlocked(t *testing.T) {
	h := &gatewayHandler{
		capabilities: []config.GatewayCapabilityConfig{
			{Name: "self-cap", Description: "Self capability", Agent: "gateway-agent", Namespace: "default", Tags: []string{}, ReadyReplicas: 1},
		},
		config:    &config.GatewayRuntimeConfig{MaxDispatchCalls: 10},
		agentName: "gateway-agent",
		namespace: "default",
		queueURL:  "redis://localhost:6379",
	}

	input := json.RawMessage(`{"target":"gateway-agent","namespace":"default","prompt":"do something"}`)
	_, err := h.dispatch(context.Background(), input)
	if err == nil {
		t.Fatal("expected error for self-dispatch")
	}
	if !strings.Contains(err.Error(), "self") && !strings.Contains(err.Error(), "own") {
		t.Errorf("error should mention self-dispatch, got: %v", err)
	}
}

func TestDispatch_TargetNotInCapabilityList(t *testing.T) {
	h := &gatewayHandler{
		capabilities: []config.GatewayCapabilityConfig{
			{Name: "known-cap", Description: "Known capability", Agent: "known-agent", Namespace: "default", Tags: []string{}, ReadyReplicas: 1},
		},
		config:    &config.GatewayRuntimeConfig{MaxDispatchCalls: 10},
		agentName: "gateway-agent",
		namespace: "default",
		queueURL:  "redis://localhost:6379",
	}

	input := json.RawMessage(`{"target":"unknown-agent","namespace":"default","prompt":"do something"}`)
	_, err := h.dispatch(context.Background(), input)
	if err == nil {
		t.Fatal("expected error for unknown target")
	}
	if !strings.Contains(err.Error(), "unknown-agent") && !strings.Contains(err.Error(), "not found") && !strings.Contains(err.Error(), "not in") {
		t.Errorf("error should mention the unknown target, got: %v", err)
	}
}

func TestDispatch_MaxDispatchCallsExceeded(t *testing.T) {
	h := &gatewayHandler{
		capabilities: []config.GatewayCapabilityConfig{
			{Name: "cap-a", Description: "Cap A", Agent: "agent-a", Namespace: "default", Tags: []string{}, ReadyReplicas: 1},
		},
		config:        &config.GatewayRuntimeConfig{MaxDispatchCalls: 2},
		agentName:     "gateway-agent",
		namespace:     "default",
		queueURL:      "redis://localhost:6379",
		dispatchCount: 2, // already at limit; next call increments to 3 > 2
	}

	input := json.RawMessage(`{"target":"agent-a","namespace":"default","prompt":"do something"}`)
	_, err := h.dispatch(context.Background(), input)
	if err == nil {
		t.Fatal("expected error when dispatch calls exceed limit")
	}
	if !strings.Contains(err.Error(), "limit") && !strings.Contains(err.Error(), "max") && !strings.Contains(err.Error(), "exceeded") {
		t.Errorf("error should mention dispatch limit, got: %v", err)
	}
}

func TestDispatch_AllowedTargetsEnforced(t *testing.T) {
	h := &gatewayHandler{
		capabilities: []config.GatewayCapabilityConfig{
			{Name: "cap-a", Description: "Cap A", Agent: "agent-a", Namespace: "default", Tags: []string{}, ReadyReplicas: 1},
			{Name: "cap-b", Description: "Cap B", Agent: "agent-b", Namespace: "default", Tags: []string{}, ReadyReplicas: 1},
		},
		config: &config.GatewayRuntimeConfig{
			MaxDispatchCalls: 10,
			AllowedTargets:   []string{"agent-a"}, // only agent-a is allowed
		},
		agentName: "gateway-agent",
		namespace: "default",
		queueURL:  "redis://localhost:6379",
	}

	input := json.RawMessage(`{"target":"agent-b","namespace":"default","prompt":"do something"}`)
	_, err := h.dispatch(context.Background(), input)
	if err == nil {
		t.Fatal("expected error for target not in AllowedTargets")
	}
	if !strings.Contains(err.Error(), "allowed") && !strings.Contains(err.Error(), "agent-b") {
		t.Errorf("error should mention target not allowed, got: %v", err)
	}
}

func TestDispatch_AllowedTargetsEmpty_AllowsAll(t *testing.T) {
	h := &gatewayHandler{
		capabilities: []config.GatewayCapabilityConfig{
			{Name: "cap-a", Description: "Cap A", Agent: "agent-a", Namespace: "default", Tags: []string{}, ReadyReplicas: 1},
		},
		config: &config.GatewayRuntimeConfig{
			MaxDispatchCalls: 10,
			AllowedTargets:   nil, // empty - allows all
		},
		agentName: "gateway-agent",
		namespace: "default",
		queueURL:  "", // empty URL - will fail at queue creation, not validation
	}

	// Dispatch should pass validation (no allowed-targets error) and fail at
	// queue creation instead. Assert the exact shape of the result: a JSON
	// dispatch_failed whose reason mentions queue creation, not allow-listing.
	input := json.RawMessage(`{"target":"agent-a","namespace":"default","prompt":"do something"}`)
	result, err := h.dispatch(context.Background(), input)
	if err != nil {
		t.Fatalf("expected JSON dispatch_failed result (err == nil), got err=%v", err)
	}
	var decoded map[string]string
	if jerr := json.Unmarshal([]byte(result), &decoded); jerr != nil {
		t.Fatalf("expected JSON result, got unparseable: %s (err=%v)", result, jerr)
	}
	if decoded["error"] != "dispatch_failed" {
		t.Errorf("error field = %q, want dispatch_failed (result=%s)", decoded["error"], result)
	}
	if decoded["target"] != "agent-a" {
		t.Errorf("target field = %q, want agent-a", decoded["target"])
	}
	if !strings.Contains(decoded["reason"], "creating queue") {
		t.Errorf("reason = %q, want prefix 'creating queue'", decoded["reason"])
	}
	if strings.Contains(decoded["reason"], "allowed") || strings.Contains(decoded["reason"], "not in") {
		t.Errorf("empty AllowedTargets should allow all; got allow-list reason: %s", decoded["reason"])
	}
}

// ---------------------------------------------------------------------------
// buildGatewayTools tests
// ---------------------------------------------------------------------------

func TestBuildGatewayTools_DispatchEnabled(t *testing.T) {
	cfg := &config.Config{
		GatewayTools: []config.GatewayToolConfig{
			{Name: "registry_search", Description: "Search capabilities"},
			{Name: "dispatch", Description: "Dispatch a task"},
		},
	}
	tools := buildGatewayTools(cfg)
	if tools == nil {
		t.Fatal("expected non-nil tools")
	}

	names := map[string]bool{}
	for _, tool := range tools {
		names[tool.Name] = true
	}
	if !names["registry_search"] {
		t.Error("expected registry_search tool")
	}
	if !names["dispatch"] {
		t.Error("expected dispatch tool")
	}
}

func TestBuildGatewayTools_DispatchDisabled(t *testing.T) {
	cfg := &config.Config{
		GatewayTools: []config.GatewayToolConfig{
			{Name: "registry_search", Description: "Search capabilities"},
		},
	}
	tools := buildGatewayTools(cfg)
	if tools == nil {
		t.Fatal("expected non-nil tools")
	}

	names := map[string]bool{}
	for _, tool := range tools {
		names[tool.Name] = true
	}
	if !names["registry_search"] {
		t.Error("expected registry_search tool")
	}
	if names["dispatch"] {
		t.Error("dispatch tool should not be present when not in GatewayTools")
	}
}

func TestBuildGatewayTools_NilConfig(t *testing.T) {
	cfg := &config.Config{} // no GatewayTools
	tools := buildGatewayTools(cfg)
	if tools != nil {
		t.Errorf("expected nil tools for empty GatewayTools, got %d tools", len(tools))
	}
}

// ---------------------------------------------------------------------------
// isGatewayTool tests
// ---------------------------------------------------------------------------

func TestIsGatewayTool_True(t *testing.T) {
	r := &Runner{gatewayHandler: &gatewayHandler{}}
	cases := []string{"registry_search", "dispatch"}
	for _, name := range cases {
		if !r.isGatewayTool(name) {
			t.Errorf("isGatewayTool(%q) = false, want true", name)
		}
	}
}

func TestIsGatewayTool_False(t *testing.T) {
	r := &Runner{gatewayHandler: &gatewayHandler{}}
	cases := []string{
		"submit_subtask",
		"delegate",
		"collect_results",
		"search",
		"",
		"registry_search_extended",
		"dispatch_task",
	}
	for _, name := range cases {
		if r.isGatewayTool(name) {
			t.Errorf("isGatewayTool(%q) = true, want false", name)
		}
	}
}

func TestIsGatewayTool_NilHandler(t *testing.T) {
	r := &Runner{} // no gatewayHandler
	if r.isGatewayTool("registry_search") {
		t.Error("isGatewayTool should return false when gatewayHandler is nil")
	}
}
