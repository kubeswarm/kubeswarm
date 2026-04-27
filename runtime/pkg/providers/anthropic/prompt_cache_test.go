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

package anthropic

import (
	"strings"
	"testing"

	anthropicsdk "github.com/anthropics/anthropic-sdk-go"

	"github.com/kubeswarm/kubeswarm/pkg/agent/config"
)

// hasCacheControl reports whether the TextBlockParam has a non-zero CacheControl.
func hasCacheControl(block anthropicsdk.TextBlockParam) bool {
	return block.CacheControl != (anthropicsdk.CacheControlEphemeralParam{})
}

func TestBuildAnthropicParams_CacheTable(t *testing.T) {
	longPrompt := strings.Repeat("x ", 3000) // well over 1024 tokens
	shortPrompt := "hi"

	tests := []struct {
		name            string
		prompt          string
		cache           *config.PromptCacheConfig
		wantSystemCache bool
	}{
		{
			name:            "nil config",
			prompt:          longPrompt,
			cache:           nil,
			wantSystemCache: false,
		},
		{
			name:            "disabled",
			prompt:          longPrompt,
			cache:           &config.PromptCacheConfig{Enabled: false, CacheableSystemPrompt: true},
			wantSystemCache: false,
		},
		{
			name:            "enabled and cacheable long prompt",
			prompt:          longPrompt,
			cache:           &config.PromptCacheConfig{Enabled: true, CacheableSystemPrompt: true, MinPrefixTokens: 1024},
			wantSystemCache: true,
		},
		{
			name:            "enabled but short prompt",
			prompt:          shortPrompt,
			cache:           &config.PromptCacheConfig{Enabled: true, CacheableSystemPrompt: true, MinPrefixTokens: 1024},
			wantSystemCache: false,
		},
		{
			name:            "enabled but system prompt not cacheable",
			prompt:          longPrompt,
			cache:           &config.PromptCacheConfig{Enabled: true, CacheableSystemPrompt: false, MinPrefixTokens: 1024},
			wantSystemCache: false,
		},
		{
			name:            "zero MinPrefixTokens defaults to 1024",
			prompt:          "Short.",
			cache:           &config.PromptCacheConfig{Enabled: true, CacheableSystemPrompt: true, MinPrefixTokens: 0},
			wantSystemCache: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.Config{
				Model:            "claude-sonnet-4-6",
				SystemPrompt:     tt.prompt,
				MaxTokensPerCall: 8000,
				PromptCache:      tt.cache,
			}
			p := buildAnthropicParams(cfg, nil, nil)
			got := hasCacheControl(p.System[0])
			if got != tt.wantSystemCache {
				t.Errorf("cache_control on system = %v, want %v", got, tt.wantSystemCache)
			}
		})
	}
}

func TestBuildAnthropicParams_CacheEnabled_ToolsCacheable(t *testing.T) {
	longPrompt := strings.Repeat("You are a helpful assistant. ", 200)
	tools := []anthropicsdk.ToolUnionParam{
		{OfTool: &anthropicsdk.ToolParam{Name: "alpha", Description: anthropicsdk.String("first tool")}},
		{OfTool: &anthropicsdk.ToolParam{Name: "beta", Description: anthropicsdk.String("second tool")}},
		{OfTool: &anthropicsdk.ToolParam{Name: "gamma", Description: anthropicsdk.String("third tool")}},
	}
	cfg := &config.Config{
		Model:            "claude-sonnet-4-6",
		SystemPrompt:     longPrompt,
		MaxTokensPerCall: 8000,
		PromptCache: &config.PromptCacheConfig{
			Enabled:               true,
			CacheableSystemPrompt: true,
			CacheableTools:        true,
			MinPrefixTokens:       1024,
		},
	}
	p := buildAnthropicParams(cfg, nil, tools)
	if len(p.Tools) != 3 {
		t.Fatalf("expected 3 tools, got %d", len(p.Tools))
	}
	// Only the last tool should have cache_control.
	last := p.Tools[len(p.Tools)-1]
	if last.OfTool == nil {
		t.Fatal("expected last tool to be a ToolParam")
	}
	if last.OfTool.CacheControl == (anthropicsdk.CacheControlEphemeralParam{}) {
		t.Error("expected cache_control on the last tool when CacheableTools is true")
	}
	// Earlier tools should NOT have cache_control.
	for i := 0; i < len(p.Tools)-1; i++ {
		if p.Tools[i].OfTool != nil && p.Tools[i].OfTool.CacheControl != (anthropicsdk.CacheControlEphemeralParam{}) {
			t.Errorf("tool[%d] should not have cache_control", i)
		}
	}
}

func TestBuildAnthropicParams_CacheEnabled_ToolsNotCacheable(t *testing.T) {
	tools := []anthropicsdk.ToolUnionParam{
		{OfTool: &anthropicsdk.ToolParam{Name: "alpha", Description: anthropicsdk.String("first tool")}},
	}
	cfg := &config.Config{
		Model:            "claude-sonnet-4-6",
		SystemPrompt:     strings.Repeat("test ", 1000),
		MaxTokensPerCall: 8000,
		PromptCache: &config.PromptCacheConfig{
			Enabled:               true,
			CacheableSystemPrompt: true,
			CacheableTools:        false,
			MinPrefixTokens:       1024,
		},
	}
	p := buildAnthropicParams(cfg, nil, tools)
	if p.Tools[0].OfTool != nil && p.Tools[0].OfTool.CacheControl != (anthropicsdk.CacheControlEphemeralParam{}) {
		t.Error("expected no cache_control on tools when CacheableTools is false")
	}
}

func TestBuildAnthropicParams_CacheEnabled_NoTools(t *testing.T) {
	longPrompt := strings.Repeat("You are a helpful assistant. ", 200)
	cfg := &config.Config{
		Model:            "claude-sonnet-4-6",
		SystemPrompt:     longPrompt,
		MaxTokensPerCall: 8000,
		PromptCache: &config.PromptCacheConfig{
			Enabled:               true,
			CacheableSystemPrompt: true,
			CacheableTools:        true,
			MinPrefixTokens:       1024,
		},
	}
	// Should not panic when tools is nil.
	p := buildAnthropicParams(cfg, nil, nil)
	if !hasCacheControl(p.System[0]) {
		t.Error("expected cache_control on system prompt even when no tools are present")
	}
}

func TestBuildAnthropicParams_CacheDoesNotAffectReasoning(t *testing.T) {
	longPrompt := strings.Repeat("You are a helpful assistant. ", 200)
	cfg := &config.Config{
		Model:                 "claude-sonnet-4-6",
		SystemPrompt:          longPrompt,
		MaxTokensPerCall:      16000,
		ReasoningMode:         "Auto",
		ReasoningBudgetTokens: 8192,
		PromptCache: &config.PromptCacheConfig{
			Enabled:               true,
			CacheableSystemPrompt: true,
			MinPrefixTokens:       1024,
		},
	}
	p := buildAnthropicParams(cfg, nil, nil)
	if !hasCacheControl(p.System[0]) {
		t.Error("expected cache_control on system prompt")
	}
	if !thinkingEnabled(p) {
		t.Error("expected thinking enabled alongside prompt caching")
	}
	if got := thinkingBudget(p); got != 8192 {
		t.Errorf("thinking budget = %d, want 8192", got)
	}
}
