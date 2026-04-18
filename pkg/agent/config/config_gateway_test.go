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

package config_test

import (
	"testing"

	"github.com/kubeswarm/kubeswarm/pkg/agent/config"
)

// ---------------------------------------------------------------------------
// AGENT_GATEWAY_CAPABILITIES
// ---------------------------------------------------------------------------

func TestLoadGatewayCapabilities_NotSet(t *testing.T) {
	requiredEnvs(t)

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.GatewayCapabilities != nil {
		t.Errorf("GatewayCapabilities = %v, want nil when env not set", cfg.GatewayCapabilities)
	}
}

func TestLoadGatewayCapabilities_ValidJSON(t *testing.T) {
	requiredEnvs(t)
	setEnv(t, "AGENT_GATEWAY_CAPABILITIES", `[
		{
			"name":"code-review",
			"description":"Reviews pull requests for style and correctness",
			"agent":"reviewer",
			"namespace":"default",
			"tags":["code","review"],
			"readyReplicas":2
		},
		{
			"name":"test-gen",
			"description":"Generates unit tests",
			"agent":"tester",
			"namespace":"ci",
			"tags":["testing"],
			"readyReplicas":1
		}
	]`)

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if len(cfg.GatewayCapabilities) != 2 {
		t.Fatalf("GatewayCapabilities len = %d, want 2", len(cfg.GatewayCapabilities))
	}

	cap0 := cfg.GatewayCapabilities[0]
	if cap0.Name != "code-review" {
		t.Errorf("cap[0].Name = %q, want code-review", cap0.Name)
	}
	if cap0.Description != "Reviews pull requests for style and correctness" {
		t.Errorf("cap[0].Description = %q", cap0.Description)
	}
	if cap0.Agent != "reviewer" {
		t.Errorf("cap[0].Agent = %q, want reviewer", cap0.Agent)
	}
	if cap0.Namespace != "default" {
		t.Errorf("cap[0].Namespace = %q, want default", cap0.Namespace)
	}
	if len(cap0.Tags) != 2 || cap0.Tags[0] != "code" || cap0.Tags[1] != "review" {
		t.Errorf("cap[0].Tags = %v, want [code review]", cap0.Tags)
	}
	if cap0.ReadyReplicas != 2 {
		t.Errorf("cap[0].ReadyReplicas = %d, want 2", cap0.ReadyReplicas)
	}

	cap1 := cfg.GatewayCapabilities[1]
	if cap1.Name != "test-gen" {
		t.Errorf("cap[1].Name = %q, want test-gen", cap1.Name)
	}
	if cap1.Agent != "tester" {
		t.Errorf("cap[1].Agent = %q, want tester", cap1.Agent)
	}
	if cap1.Namespace != "ci" {
		t.Errorf("cap[1].Namespace = %q, want ci", cap1.Namespace)
	}
	if cap1.ReadyReplicas != 1 {
		t.Errorf("cap[1].ReadyReplicas = %d, want 1", cap1.ReadyReplicas)
	}
}

func TestLoadGatewayCapabilities_InvalidJSON(t *testing.T) {
	requiredEnvs(t)
	setEnv(t, "AGENT_GATEWAY_CAPABILITIES", `not-valid-json`)

	_, err := config.Load()
	if err == nil {
		t.Fatal("expected error for invalid AGENT_GATEWAY_CAPABILITIES JSON, got nil")
	}
}

// ---------------------------------------------------------------------------
// AGENT_GATEWAY_CONFIG
// ---------------------------------------------------------------------------

func TestLoadGatewayConfig_NotSet(t *testing.T) {
	requiredEnvs(t)

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.GatewayConfig != nil {
		t.Errorf("GatewayConfig = %v, want nil when env not set", cfg.GatewayConfig)
	}
}

func TestLoadGatewayConfig_ValidJSON(t *testing.T) {
	requiredEnvs(t)
	// Use the actual API enum values so a spec change (new/renamed enum)
	// surfaces as a test failure, not a silent drift.
	setEnv(t, "AGENT_GATEWAY_CONFIG", `{
		"dispatchMode":"enabled",
		"dispatchTimeoutSeconds":30,
		"maxDispatchDepth":3,
		"maxResultsPerSearch":10,
		"maxDispatchCalls":25,
		"fallbackMode":"fail",
		"fallbackAgent":"fallback-agent",
		"allowedTargets":["reviewer","tester"]
	}`)

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.GatewayConfig == nil {
		t.Fatal("GatewayConfig is nil")
	}

	gc := cfg.GatewayConfig
	if gc.DispatchMode != "enabled" {
		t.Errorf("DispatchMode = %q, want enabled", gc.DispatchMode)
	}
	if gc.DispatchTimeoutSeconds != 30 {
		t.Errorf("DispatchTimeoutSeconds = %d, want 30", gc.DispatchTimeoutSeconds)
	}
	if gc.MaxDispatchDepth != 3 {
		t.Errorf("MaxDispatchDepth = %d, want 3", gc.MaxDispatchDepth)
	}
	if gc.MaxResultsPerSearch != 10 {
		t.Errorf("MaxResultsPerSearch = %d, want 10", gc.MaxResultsPerSearch)
	}
	if gc.MaxDispatchCalls != 25 {
		t.Errorf("MaxDispatchCalls = %d, want 25", gc.MaxDispatchCalls)
	}
	if gc.FallbackMode != "fail" {
		t.Errorf("FallbackMode = %q, want fail", gc.FallbackMode)
	}
	if gc.FallbackAgent != "fallback-agent" {
		t.Errorf("FallbackAgent = %q, want fallback-agent", gc.FallbackAgent)
	}
	if len(gc.AllowedTargets) != 2 || gc.AllowedTargets[0] != "reviewer" || gc.AllowedTargets[1] != "tester" {
		t.Errorf("AllowedTargets = %v, want [reviewer tester]", gc.AllowedTargets)
	}
}

func TestLoadGatewayConfig_InvalidJSON(t *testing.T) {
	requiredEnvs(t)
	setEnv(t, "AGENT_GATEWAY_CONFIG", `{bad json}`)

	_, err := config.Load()
	if err == nil {
		t.Fatal("expected error for invalid AGENT_GATEWAY_CONFIG JSON, got nil")
	}
}

// ---------------------------------------------------------------------------
// AGENT_GATEWAY_TOOLS
// ---------------------------------------------------------------------------

func TestLoadGatewayTools_NotSet(t *testing.T) {
	requiredEnvs(t)

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.GatewayTools != nil {
		t.Errorf("GatewayTools = %v, want nil when env not set", cfg.GatewayTools)
	}
}

func TestLoadGatewayTools_ValidJSON(t *testing.T) {
	requiredEnvs(t)
	setEnv(t, "AGENT_GATEWAY_TOOLS", `[
		{"name":"dispatch","description":"Dispatch a task to a specific agent"},
		{"name":"search_capabilities","description":"Search available agent capabilities by tag or description"}
	]`)

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if len(cfg.GatewayTools) != 2 {
		t.Fatalf("GatewayTools len = %d, want 2", len(cfg.GatewayTools))
	}
	if cfg.GatewayTools[0].Name != "dispatch" {
		t.Errorf("GatewayTools[0].Name = %q, want dispatch", cfg.GatewayTools[0].Name)
	}
	if cfg.GatewayTools[0].Description != "Dispatch a task to a specific agent" {
		t.Errorf("GatewayTools[0].Description = %q", cfg.GatewayTools[0].Description)
	}
	if cfg.GatewayTools[1].Name != "search_capabilities" {
		t.Errorf("GatewayTools[1].Name = %q, want search_capabilities", cfg.GatewayTools[1].Name)
	}
	if cfg.GatewayTools[1].Description != "Search available agent capabilities by tag or description" {
		t.Errorf("GatewayTools[1].Description = %q", cfg.GatewayTools[1].Description)
	}
}

func TestLoadGatewayTools_InvalidJSON(t *testing.T) {
	requiredEnvs(t)
	setEnv(t, "AGENT_GATEWAY_TOOLS", `[{"name":broken}]`)

	_, err := config.Load()
	if err == nil {
		t.Fatal("expected error for invalid AGENT_GATEWAY_TOOLS JSON, got nil")
	}
}

// ---------------------------------------------------------------------------
// All three gateway env vars set together
// ---------------------------------------------------------------------------

func TestLoadGateway_AllThreeEnvVarsSet(t *testing.T) {
	requiredEnvs(t)

	setEnv(t, "AGENT_GATEWAY_CAPABILITIES", `[{"name":"summarize","description":"Summarizes documents","agent":"summarizer","namespace":"default","tags":["nlp"],"readyReplicas":3}]`)
	setEnv(t, "AGENT_GATEWAY_CONFIG", `{"dispatchMode":"disabled","dispatchTimeoutSeconds":60,"maxDispatchDepth":5,"maxResultsPerSearch":20,"maxDispatchCalls":50,"fallbackMode":"answer-directly"}`)
	setEnv(t, "AGENT_GATEWAY_TOOLS", `[{"name":"dispatch","description":"Dispatch task"}]`)

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	// Capabilities
	if len(cfg.GatewayCapabilities) != 1 {
		t.Fatalf("GatewayCapabilities len = %d, want 1", len(cfg.GatewayCapabilities))
	}
	if cfg.GatewayCapabilities[0].Name != "summarize" {
		t.Errorf("GatewayCapabilities[0].Name = %q, want summarize", cfg.GatewayCapabilities[0].Name)
	}
	if cfg.GatewayCapabilities[0].ReadyReplicas != 3 {
		t.Errorf("GatewayCapabilities[0].ReadyReplicas = %d, want 3", cfg.GatewayCapabilities[0].ReadyReplicas)
	}

	// Config
	if cfg.GatewayConfig == nil {
		t.Fatal("GatewayConfig is nil")
	}
	if cfg.GatewayConfig.DispatchMode != "disabled" {
		t.Errorf("DispatchMode = %q, want disabled", cfg.GatewayConfig.DispatchMode)
	}
	if cfg.GatewayConfig.DispatchTimeoutSeconds != 60 {
		t.Errorf("DispatchTimeoutSeconds = %d, want 60", cfg.GatewayConfig.DispatchTimeoutSeconds)
	}
	if cfg.GatewayConfig.MaxDispatchDepth != 5 {
		t.Errorf("MaxDispatchDepth = %d, want 5", cfg.GatewayConfig.MaxDispatchDepth)
	}
	if cfg.GatewayConfig.FallbackMode != "answer-directly" {
		t.Errorf("FallbackMode = %q, want answer-directly", cfg.GatewayConfig.FallbackMode)
	}

	// Tools
	if len(cfg.GatewayTools) != 1 {
		t.Fatalf("GatewayTools len = %d, want 1", len(cfg.GatewayTools))
	}
	if cfg.GatewayTools[0].Name != "dispatch" {
		t.Errorf("GatewayTools[0].Name = %q, want dispatch", cfg.GatewayTools[0].Name)
	}
}
