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

// Package routing provides LLM-driven capability dispatch for SwarmTeam routed mode.
// The router makes a single, lightweight LLM call that treats indexed agent capabilities
// as a "tool list" and selects the best match for the incoming task — analogous to how
// an LLM picks a function when function-calling is enabled.
package routing

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"text/template"

	kubeswarmv1alpha1 "github.com/kubeswarm/kubeswarm/api/v1alpha1"
	"github.com/kubeswarm/kubeswarm/internal/registry"
)

// defaultRouterSystemPrompt is used when spec.routing.systemPrompt is not set.
// Template variables: .Capabilities ([]IndexedCapability), .Input (JSON string).
const defaultRouterSystemPrompt = `You are a task router. Select the most appropriate agent for the given task.

Available capabilities:
{{ range .Capabilities }}- id: {{ .ID }}
  description: {{ .Description }}
  tags: [{{ join .Tags ", " }}]
{{ end }}
Task input:
{{ .Input }}

Respond with a JSON object only — no markdown, no explanation outside the JSON:
{"capability": "<selected capability ID>", "reason": "<one sentence explanation>"}

If no capability is a good fit, respond with:
{"capability": "", "reason": "<why no match>"}`

// Result holds the outcome of a single routing decision.
type Result struct {
	// AgentName is the resolved SwarmAgent name. Empty when no capability matched and no fallback.
	AgentName string
	// Capability is the selected capability ID. May be empty when fallback was used.
	Capability string
	// Reason is the router LLM's one-sentence explanation.
	Reason string
}

// LLMFn is the function signature for a single-turn LLM call.
// It matches the SemanticValidateFn / RouterFn signature in SwarmRunReconciler
// so the same builder (pkg/validation.BuildSemanticValidateFn) can be reused.
type LLMFn func(ctx context.Context, model, prompt string) (string, error)

// Route selects the best agent for the given task input by making a single LLM call
// using the capability index from the provided registry.
//
// Error conditions:
//   - routingCfg is nil or Model is empty → immediate error
//   - registry has no indexed capabilities and no fallback is configured → error
//   - LLM call fails → error
//   - LLM response cannot be parsed → error
//   - Capability matched but no agent found and no fallback → error
//
// When a fallback agent is configured (routingCfg.Fallback), it is used instead of
// returning an error whenever the router cannot resolve a concrete agent.
func Route(
	ctx context.Context,
	reg *registry.Registry,
	routingCfg *kubeswarmv1alpha1.SwarmTeamRoutingSpec,
	input map[string]string,
	llmFn LLMFn,
) (Result, error) {
	if routingCfg == nil {
		return Result{}, fmt.Errorf("routing config is nil")
	}
	model := routingCfg.Model
	if model == "" {
		return Result{}, fmt.Errorf("spec.routing.model is required")
	}

	caps := reg.Snapshot()
	if len(caps) == 0 {
		if routingCfg.Fallback != "" {
			return Result{AgentName: routingCfg.Fallback, Reason: "no capabilities indexed in registry"}, nil
		}
		return Result{}, fmt.Errorf("registry has no indexed capabilities and no fallback is configured")
	}

	prompt, err := buildRouterPrompt(routingCfg.SystemPrompt, caps, input)
	if err != nil {
		return Result{}, fmt.Errorf("building router prompt: %w", err)
	}

	response, err := llmFn(ctx, model, prompt)
	if err != nil {
		return Result{}, fmt.Errorf("router LLM call failed: %w", err)
	}

	capID, reason, err := parseRouterResponse(response)
	if err != nil {
		return Result{}, fmt.Errorf("parsing router response: %w", err)
	}

	if capID == "" {
		if routingCfg.Fallback != "" {
			return Result{AgentName: routingCfg.Fallback, Reason: reason}, nil
		}
		return Result{}, fmt.Errorf("router selected no capability and no fallback is configured: %s", reason)
	}

	agentName := reg.Resolve(registry.ResolveRequest{
		Capability: capID,
		Strategy:   kubeswarmv1alpha1.RegistryLookupStrategyLeastBusy,
	})
	if agentName == "" {
		if routingCfg.Fallback != "" {
			return Result{AgentName: routingCfg.Fallback, Capability: capID, Reason: reason}, nil
		}
		return Result{}, fmt.Errorf("capability %q matched but no agent is indexed in the registry", capID)
	}

	return Result{AgentName: agentName, Capability: capID, Reason: reason}, nil
}

type promptData struct {
	Capabilities []kubeswarmv1alpha1.IndexedCapability
	Input        string
}

func buildRouterPrompt(
	customPrompt string,
	caps []kubeswarmv1alpha1.IndexedCapability,
	input map[string]string,
) (string, error) {
	tmplStr := defaultRouterSystemPrompt
	if customPrompt != "" {
		tmplStr = customPrompt
	}

	inputBytes, _ := json.Marshal(input)

	funcMap := template.FuncMap{
		"join": strings.Join,
	}
	tmpl, err := template.New("router").Funcs(funcMap).Parse(tmplStr)
	if err != nil {
		return "", fmt.Errorf("parsing router prompt template: %w", err)
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, promptData{
		Capabilities: caps,
		Input:        string(inputBytes),
	}); err != nil {
		return "", fmt.Errorf("executing router prompt template: %w", err)
	}
	return buf.String(), nil
}

// parseRouterResponse extracts the capability ID and reason from the LLM response.
// The LLM is asked to return {"capability": "...", "reason": "..."}.
// We locate the first JSON object in the response to tolerate minor formatting drift.
func parseRouterResponse(response string) (capID, reason string, err error) {
	start := strings.Index(response, "{")
	end := strings.LastIndex(response, "}")
	if start == -1 || end == -1 || end <= start {
		return "", "", fmt.Errorf("no JSON object found in router response: %q", response)
	}
	raw := response[start : end+1]

	var parsed struct {
		Capability string `json:"capability"`
		Reason     string `json:"reason"`
	}
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return "", "", fmt.Errorf("parsing router JSON %q: %w", raw, err)
	}
	return strings.TrimSpace(parsed.Capability), parsed.Reason, nil
}
