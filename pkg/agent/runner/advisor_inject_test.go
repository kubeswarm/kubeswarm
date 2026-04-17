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

package runner_test

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/kubeswarm/kubeswarm/pkg/agent/config"
	"github.com/kubeswarm/kubeswarm/pkg/agent/runner"
)

// TestRunner_AdvisorToolAutoInjection covers TEST-TRACKER.md item:
//
//	[x] A2A: consult tool auto-injection
//
// Behaviour under test: when the agent config includes advisors, the Runner
// automatically injects a consult tool for each advisor into AllTools() so the
// LLM provider can discover and invoke them without manual wiring.
func TestRunner_AdvisorToolAutoInjection(t *testing.T) {
	cfg := &config.Config{
		Model:        "mock",
		SystemPrompt: "test",
		Advisors: []config.AdvisorConfig{
			{Name: "reviewer", ToolName: "consult_reviewer", AgentRef: "reviewer-agent"},
			{Name: "security", ToolName: "consult_security", AgentRef: "security-agent"},
		},
	}

	r := runner.New(cfg, newMCPManager(t), &mockProvider{result: "ok"}, nil, nil, nil)
	tools := r.AllTools()

	// Build a lookup of tool names.
	found := make(map[string]bool, len(tools))
	for _, tool := range tools {
		found[tool.Name] = true
	}

	for _, want := range []string{"consult_reviewer", "consult_security"} {
		if !found[want] {
			t.Errorf("AllTools() missing advisor tool %q", want)
		}
	}
}

// TestRunner_NoAdvisors_NoConsultTools verifies that when no advisors are
// configured, no consult tools appear in AllTools().
func TestRunner_NoAdvisors_NoConsultTools(t *testing.T) {
	cfg := &config.Config{
		Model:        "mock",
		SystemPrompt: "test",
	}

	r := runner.New(cfg, newMCPManager(t), &mockProvider{result: "ok"}, nil, nil, nil)

	for _, tool := range r.AllTools() {
		if len(tool.Name) > 8 && tool.Name[:8] == "consult_" {
			t.Errorf("unexpected consult tool %q when no advisors configured", tool.Name)
		}
	}
}

// TestRunner_AdvisorToolHasValidSchema verifies that injected advisor tools have
// a well-formed JSON schema with a required "question" string property.
func TestRunner_AdvisorToolHasValidSchema(t *testing.T) {
	cfg := &config.Config{
		Model:        "mock",
		SystemPrompt: "test",
		Advisors: []config.AdvisorConfig{
			{Name: "arch", ToolName: "consult_arch", AgentRef: "arch-agent"},
		},
	}

	r := runner.New(cfg, newMCPManager(t), &mockProvider{result: "ok"}, nil, nil, nil)

	var schemaBytes []byte
	for _, tool := range r.AllTools() {
		if tool.Name == "consult_arch" {
			schemaBytes = tool.InputSchema
			break
		}
	}
	if schemaBytes == nil {
		t.Fatal("consult_arch tool not found in AllTools()")
	}

	// Parse the schema as JSON and validate structure.
	var schema struct {
		Type       string `json:"type"`
		Properties map[string]struct {
			Type string `json:"type"`
		} `json:"properties"`
		Required []string `json:"required"`
	}
	if err := json.Unmarshal(schemaBytes, &schema); err != nil {
		t.Fatalf("InputSchema is not valid JSON: %v", err)
	}

	if schema.Type != "object" {
		t.Errorf("schema type = %q, want %q", schema.Type, "object")
	}

	qProp, ok := schema.Properties["question"]
	if !ok {
		t.Fatal("schema missing 'question' property")
	}
	if qProp.Type != "string" {
		t.Errorf("question type = %q, want %q", qProp.Type, "string")
	}

	hasRequired := slices.Contains(schema.Required, "question")
	if !hasRequired {
		t.Errorf("'question' not in required list: %v", schema.Required)
	}
}

// TestRunner_AdvisorToolCustomInstructions verifies that a custom Instructions
// field on AdvisorConfig becomes the tool's Description.
func TestRunner_AdvisorToolCustomInstructions(t *testing.T) {
	cfg := &config.Config{
		Model:        "mock",
		SystemPrompt: "test",
		Advisors: []config.AdvisorConfig{
			{
				Name:         "expert",
				ToolName:     "consult_expert",
				AgentRef:     "expert-agent",
				Instructions: "Ask this expert about database schema design.",
			},
		},
	}

	r := runner.New(cfg, newMCPManager(t), &mockProvider{result: "ok"}, nil, nil, nil)

	for _, tool := range r.AllTools() {
		if tool.Name == "consult_expert" {
			if tool.Description != "Ask this expert about database schema design." {
				t.Errorf("Description = %q, want custom instructions", tool.Description)
			}
			return
		}
	}
	t.Fatal("consult_expert tool not found")
}
