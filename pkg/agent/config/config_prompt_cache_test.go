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

package config

import (
	"os"
	"testing"
)

func TestLoad_PromptCacheUnset(t *testing.T) {
	setRequiredEnvs(t)
	os.Unsetenv("AGENT_PROMPT_CACHE")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if cfg.PromptCache != nil {
		t.Error("expected PromptCache to be nil when env is unset")
	}
}

func TestLoad_PromptCacheValid(t *testing.T) {
	setRequiredEnvs(t)
	t.Setenv("AGENT_PROMPT_CACHE", `{"enabled":true,"cacheableSystemPrompt":true,"cacheableTools":true,"minPrefixTokens":2048}`)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if cfg.PromptCache == nil {
		t.Fatal("expected PromptCache to be non-nil")
	}
	if !cfg.PromptCache.Enabled {
		t.Error("expected Enabled=true")
	}
	if !cfg.PromptCache.CacheableSystemPrompt {
		t.Error("expected CacheableSystemPrompt=true")
	}
	if !cfg.PromptCache.CacheableTools {
		t.Error("expected CacheableTools=true")
	}
	if cfg.PromptCache.MinPrefixTokens != 2048 {
		t.Errorf("MinPrefixTokens = %d, want 2048", cfg.PromptCache.MinPrefixTokens)
	}
}

func TestLoad_PromptCacheInvalidJSON(t *testing.T) {
	setRequiredEnvs(t)
	t.Setenv("AGENT_PROMPT_CACHE", `{broken`)

	_, err := Load()
	if err == nil {
		t.Fatal("expected error for invalid AGENT_PROMPT_CACHE JSON")
	}
}

// setRequiredEnvs sets the minimum env vars required for config.Load() to succeed.
func setRequiredEnvs(t *testing.T) {
	t.Helper()
	t.Setenv("AGENT_MODEL", "claude-sonnet-4-6")
	t.Setenv("AGENT_SYSTEM_PROMPT", "test prompt")
	t.Setenv("TASK_QUEUE_URL", "redis://localhost:6379")
}
