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

func TestLoad_ToolDenyPatterns_ValidJSON(t *testing.T) {
	requiredEnvs(t)
	setEnv(t, "AGENT_POLICY_TOOL_DENY", `["shell/*","filesystem/write_file"]`)

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cfg.ToolDenyPatterns) != 2 {
		t.Fatalf("ToolDenyPatterns len = %d, want 2", len(cfg.ToolDenyPatterns))
	}
	if cfg.ToolDenyPatterns[0] != "shell/*" {
		t.Errorf("ToolDenyPatterns[0] = %q, want %q", cfg.ToolDenyPatterns[0], "shell/*")
	}
	if cfg.ToolDenyPatterns[1] != "filesystem/write_file" {
		t.Errorf("ToolDenyPatterns[1] = %q, want %q", cfg.ToolDenyPatterns[1], "filesystem/write_file")
	}
}

func TestLoad_ToolDenyPatterns_NotSet(t *testing.T) {
	requiredEnvs(t)
	// AGENT_POLICY_TOOL_DENY not set.

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cfg.ToolDenyPatterns) != 0 {
		t.Errorf("ToolDenyPatterns = %v, want nil/empty when env not set", cfg.ToolDenyPatterns)
	}
}

func TestLoad_ToolDenyPatterns_InvalidJSON(t *testing.T) {
	requiredEnvs(t)
	setEnv(t, "AGENT_POLICY_TOOL_DENY", "not-valid-json")

	_, err := config.Load()
	if err == nil {
		t.Fatal("expected error for invalid AGENT_POLICY_TOOL_DENY JSON, got nil")
	}
}

func TestLoad_PolicyForceTrustLevel_Set(t *testing.T) {
	requiredEnvs(t)
	setEnv(t, "AGENT_POLICY_FORCE_TRUST_LEVEL", "sandbox")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.PolicyForceTrustLevel != "sandbox" {
		t.Errorf("PolicyForceTrustLevel = %q, want %q", cfg.PolicyForceTrustLevel, "sandbox")
	}
}

func TestLoad_PolicyForceTrustLevel_NotSet(t *testing.T) {
	requiredEnvs(t)
	// AGENT_POLICY_FORCE_TRUST_LEVEL not set.

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.PolicyForceTrustLevel != "" {
		t.Errorf("PolicyForceTrustLevel = %q, want empty string when env not set", cfg.PolicyForceTrustLevel)
	}
}
