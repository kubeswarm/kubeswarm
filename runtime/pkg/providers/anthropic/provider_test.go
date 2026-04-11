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
	"testing"

	anthropicsdk "github.com/anthropics/anthropic-sdk-go"

	"github.com/kubeswarm/kubeswarm/pkg/agent/config"
)

// thinkingBudget returns the effective thinking budget from built params, or -1
// if thinking is not enabled.
func thinkingBudget(p anthropicsdk.MessageNewParams) int64 {
	if bt := p.Thinking.GetBudgetTokens(); bt != nil {
		return *bt
	}
	return -1
}

// thinkingEnabled reports whether the params have an enabled thinking block.
// It checks OfEnabled directly because GetType() returns a pointer to the
// SDK's constant.Enabled (a string type whose Go zero value is "", not
// "enabled" - the string "enabled" only appears at JSON marshal time).
func thinkingEnabled(p anthropicsdk.MessageNewParams) bool {
	return p.Thinking.OfEnabled != nil
}

func TestBuildAnthropicParams_NoReasoning(t *testing.T) {
	cfg := &config.Config{
		Model:            "claude-sonnet-4-6",
		SystemPrompt:     "sys",
		MaxTokensPerCall: 8000,
	}
	p := buildAnthropicParams(cfg, nil, nil)
	if thinkingEnabled(p) {
		t.Errorf("expected thinking disabled/unset, got enabled with budget=%d", thinkingBudget(p))
	}
	if p.MaxTokens != 8000 {
		t.Errorf("MaxTokens = %d, want 8000", p.MaxTokens)
	}
}

func TestBuildAnthropicParams_AutoMode(t *testing.T) {
	cfg := &config.Config{
		Model:                 "claude-sonnet-4-6",
		MaxTokensPerCall:      16000,
		ReasoningMode:         "Auto",
		ReasoningBudgetTokens: 8192,
	}
	p := buildAnthropicParams(cfg, nil, nil)
	if !thinkingEnabled(p) {
		t.Fatal("expected thinking enabled for Auto mode")
	}
	if got := thinkingBudget(p); got != 8192 {
		t.Errorf("thinking budget = %d, want 8192", got)
	}
}

func TestBuildAnthropicParams_DisabledMode(t *testing.T) {
	cfg := &config.Config{
		Model:                 "claude-sonnet-4-6",
		MaxTokensPerCall:      8000,
		ReasoningMode:         "Disabled",
		ReasoningBudgetTokens: 8192,
	}
	p := buildAnthropicParams(cfg, nil, nil)
	if thinkingEnabled(p) {
		t.Errorf("expected thinking disabled regardless of budget, got budget=%d", thinkingBudget(p))
	}
}

func TestBuildAnthropicParams_ClampedByGuardrail(t *testing.T) {
	cfg := &config.Config{
		Model:                    "claude-sonnet-4-6",
		MaxTokensPerCall:         16000,
		ReasoningMode:            "Auto",
		ReasoningBudgetTokens:    8192,
		MaxThinkingTokensPerCall: 4096,
	}
	p := buildAnthropicParams(cfg, nil, nil)
	if !thinkingEnabled(p) {
		t.Fatal("expected thinking enabled")
	}
	if got := thinkingBudget(p); got != 4096 {
		t.Errorf("clamped thinking budget = %d, want 4096 (lesser of 8192 and 4096)", got)
	}
}

func TestBuildAnthropicParams_ExplicitWithOnlyBudget(t *testing.T) {
	cfg := &config.Config{
		Model:                 "claude-sonnet-4-6",
		MaxTokensPerCall:      16000,
		ReasoningMode:         "Explicit",
		ReasoningBudgetTokens: 8192,
		ReasoningEffort:       "",
	}
	p := buildAnthropicParams(cfg, nil, nil)
	if !thinkingEnabled(p) {
		t.Fatal("expected thinking enabled for Explicit mode with budget")
	}
	if got := thinkingBudget(p); got != 8192 {
		t.Errorf("thinking budget = %d, want 8192 (effort ignored for Anthropic)", got)
	}
}

func TestBuildAnthropicParams_BudgetAboveMaxTokens(t *testing.T) {
	cfg := &config.Config{
		Model:                  "claude-sonnet-4-6",
		MaxTokensPerCall:       8000,
		ReasoningMode:          "Auto",
		ReasoningBudgetTokens:  8192, // >= MaxTokensPerCall
		MaxAnswerTokensPerCall: 2000,
	}
	p := buildAnthropicParams(cfg, nil, nil)
	if !thinkingEnabled(p) {
		t.Fatal("expected thinking enabled")
	}
	budget := thinkingBudget(p)
	if p.MaxTokens <= budget {
		t.Errorf("MaxTokens (%d) must be strictly greater than thinking budget (%d)", p.MaxTokens, budget)
	}
}

func TestBuildAnthropicParams_AnswerTokenCapWins(t *testing.T) {
	cfg := &config.Config{
		Model:                  "claude-sonnet-4-6",
		MaxTokensPerCall:       8000,
		ReasoningMode:          "Disabled",
		MaxAnswerTokensPerCall: 2000,
	}
	p := buildAnthropicParams(cfg, nil, nil)
	if thinkingEnabled(p) {
		t.Error("expected thinking disabled")
	}
	// Lower of MaxTokensPerCall (8000) and MaxAnswerTokensPerCall (2000) should win.
	if p.MaxTokens != 2000 {
		t.Errorf("MaxTokens = %d, want 2000 (lower of 8000 and 2000)", p.MaxTokens)
	}
}
