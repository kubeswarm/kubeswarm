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

package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	kubeswarmv1alpha1 "github.com/kubeswarm/kubeswarm/api/v1alpha1"
)

// GatewayCapabilityEntry is one capability entry injected into the gateway pod.
type GatewayCapabilityEntry struct {
	Name          string   `json:"name"`
	Description   string   `json:"description"`
	Agent         string   `json:"agent"`
	Namespace     string   `json:"namespace"`
	Tags          []string `json:"tags"`
	ReadyReplicas int32    `json:"readyReplicas"`
	IsGateway     bool     `json:"-"` // internal, not serialized to JSON
}

// GatewayRuntimeConfig is the JSON shape of AGENT_GATEWAY_CONFIG.
type GatewayRuntimeConfig struct {
	DispatchMode           string   `json:"dispatchMode"`
	DispatchTimeoutSeconds int32    `json:"dispatchTimeoutSeconds"`
	MaxDispatchDepth       int32    `json:"maxDispatchDepth"`
	MaxResultsPerSearch    int32    `json:"maxResultsPerSearch"`
	MaxDispatchCalls       int32    `json:"maxDispatchCalls"`
	MaxSearchCalls         int32    `json:"maxSearchCalls"`
	FallbackMode           string   `json:"fallbackMode"`
	FallbackAgent          string   `json:"fallbackAgent,omitempty"`
	AllowedTargets         []string `json:"allowedTargets,omitempty"`
}

// gatewayToolDef is a minimal tool definition for the AGENT_GATEWAY_TOOLS env var.
type gatewayToolDef struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// maxGatewayCapabilities is the hard cap on capability entries injected into the pod.
const maxGatewayCapabilities = 50

// buildGatewayEnvVars builds the AGENT_GATEWAY_CAPABILITIES, AGENT_GATEWAY_CONFIG,
// and AGENT_GATEWAY_TOOLS environment variables from the agent's gateway spec and
// the resolved registry capabilities.
func buildGatewayEnvVars(agent *kubeswarmv1alpha1.SwarmAgent, registryCapabilities []GatewayCapabilityEntry) []corev1.EnvVar {
	if agent.Spec.Gateway == nil {
		return nil
	}
	gw := agent.Spec.Gateway

	// --- filter, sort, and cap capabilities ---
	filtered := filterCapabilities(gw, registryCapabilities)
	sort.Slice(filtered, func(i, j int) bool {
		if filtered[i].ReadyReplicas != filtered[j].ReadyReplicas {
			return filtered[i].ReadyReplicas > filtered[j].ReadyReplicas
		}
		return filtered[i].Name < filtered[j].Name
	})
	totalMatching := len(filtered)
	if len(filtered) > maxGatewayCapabilities {
		filtered = filtered[:maxGatewayCapabilities]
	}

	// --- capabilities env var ---
	// Marshal cannot fail for this concrete slice type; any error is a programmer bug.
	capJSON, _ := json.Marshal(filtered)

	// --- config env var ---
	fallbackMode := string(kubeswarmv1alpha1.GatewayFallbackAnswerDirectly)
	var fallbackAgent string
	if gw.Fallback != nil {
		if gw.Fallback.Mode != "" {
			fallbackMode = string(gw.Fallback.Mode)
		}
		if gw.Fallback.AgentRef != nil {
			fallbackAgent = gw.Fallback.AgentRef.Name
		}
	}

	dispatchMode := string(kubeswarmv1alpha1.GatewayDispatchEnabled)
	if gw.DispatchMode != "" {
		dispatchMode = string(gw.DispatchMode)
	}

	dispatchTimeout := gw.DispatchTimeoutSeconds
	if dispatchTimeout == 0 {
		dispatchTimeout = 120
	}
	maxDepth := gw.MaxDispatchDepth
	if maxDepth == 0 {
		maxDepth = 3
	}
	maxResults := gw.MaxResultsPerSearch
	if maxResults == 0 {
		maxResults = 10
	}
	maxCalls := gw.MaxDispatchCalls
	if maxCalls == 0 {
		maxCalls = 5
	}
	maxSearchCalls := gw.MaxSearchCalls
	if maxSearchCalls == 0 {
		maxSearchCalls = 3
	}

	cfg := GatewayRuntimeConfig{
		DispatchMode:           dispatchMode,
		DispatchTimeoutSeconds: dispatchTimeout,
		MaxDispatchDepth:       maxDepth,
		MaxResultsPerSearch:    maxResults,
		MaxDispatchCalls:       maxCalls,
		MaxSearchCalls:         maxSearchCalls,
		FallbackMode:           fallbackMode,
		FallbackAgent:          fallbackAgent,
		AllowedTargets:         gw.AllowedTargets,
	}
	cfgJSON, _ := json.Marshal(cfg)

	// --- tools env var ---
	// Build a description that includes available capability names and tags
	// so the LLM knows what search terms will match.
	// Cap total description length to avoid bloating LLM context.
	const maxSearchDescLen = 2000
	searchDesc := "Search for agent capabilities. "
	if totalMatching > len(filtered) {
		searchDesc += fmt.Sprintf("Showing %d of %d capabilities (sorted by readiness); refine your query to reach the long tail. ",
			len(filtered), totalMatching)
	}
	if len(filtered) > 0 {
		var names, tags []string
		tagSet := make(map[string]bool)
		for _, entry := range filtered {
			names = append(names, entry.Name)
			for _, t := range entry.Tags {
				if !tagSet[t] {
					tagSet[t] = true
					tags = append(tags, t)
				}
			}
		}
		detail := fmt.Sprintf("Available capabilities: %s. Available tags: %s. Search by these names or tags, not by the user's literal words.",
			strings.Join(names, ", "), strings.Join(tags, ", "))
		searchDesc += detail
		if len(searchDesc) > maxSearchDescLen {
			// Truncate at last comma before the limit to avoid cutting mid-name.
			cutoff := maxSearchDescLen - 3
			if idx := strings.LastIndex(searchDesc[:cutoff], ","); idx > 0 {
				cutoff = idx
			}
			searchDesc = searchDesc[:cutoff] + "..."
		}
	}
	tools := []gatewayToolDef{
		{
			Name:        "registry_search",
			Description: searchDesc,
		},
	}
	if dispatchMode == string(kubeswarmv1alpha1.GatewayDispatchEnabled) {
		tools = append(tools, gatewayToolDef{
			Name:        "dispatch",
			Description: fmt.Sprintf("Dispatch a task to a target agent. Timeout: %ds. Max depth: %d.", dispatchTimeout, maxDepth),
		})
	}
	toolsJSON, _ := json.Marshal(tools)

	return []corev1.EnvVar{
		{Name: "AGENT_GATEWAY_CAPABILITIES", Value: string(capJSON)},
		{Name: "AGENT_GATEWAY_CONFIG", Value: string(cfgJSON)},
		{Name: "AGENT_GATEWAY_TOOLS", Value: string(toolsJSON)},
	}
}

// filterCapabilities applies the gateway's filter rules to the raw capability list.
func filterCapabilities(gw *kubeswarmv1alpha1.GatewayConfig, caps []GatewayCapabilityEntry) []GatewayCapabilityEntry {
	var result []GatewayCapabilityEntry
	for _, entry := range caps {
		// Skip gateway agents unless explicitly allowed.
		if entry.IsGateway && !gw.AllowGatewayTargets {
			continue
		}
		// Skip capabilities with no ready replicas.
		if entry.ReadyReplicas == 0 {
			continue
		}
		// Filter by tags (AND semantics).
		if len(gw.FilterByTags) > 0 && !hasAllTags(entry.Tags, gw.FilterByTags) {
			continue
		}
		result = append(result, entry)
	}
	return result
}

// resolveGatewayCapsForReconcile wraps resolveGatewayCapabilities with the
// error-bookkeeping that the Reconcile loop needs (condition update + status
// flush) so Reconcile itself stays a flat success path. Returns a nil slice
// and nil error when the referenced registry is missing - that is a non-fatal
// state surfaced by reconcileRegistryRef, not a reconcile failure.
func (r *SwarmAgentReconciler) resolveGatewayCapsForReconcile(ctx context.Context, agent *kubeswarmv1alpha1.SwarmAgent) ([]GatewayCapabilityEntry, error) {
	entries, err := resolveGatewayCapabilities(ctx, r.Client, agent)
	if err != nil {
		log.FromContext(ctx).Error(err, "failed to resolve gateway capabilities")
		r.setCondition(agent, kubeswarmv1alpha1.ConditionReady, metav1.ConditionFalse, "ReconcileError", err.Error())
		_ = r.Status().Update(ctx, agent)
		return nil, err
	}
	return entries, nil
}

// resolveGatewayCapabilities looks up the SwarmRegistry named by the gateway's
// RegistryRef, honours its scope (namespace-scoped vs cluster-wide), and returns
// the capability entries for agents that are members of that registry.
//
// Returns (nil, nil) when the gateway is not a gateway or when the referenced
// registry does not exist (non-fatal: the gateway will reconcile with no caps
// and surface the condition elsewhere). Transient API errors propagate so the
// reconcile is requeued rather than injecting a misleading empty list.
//
// TODO: at scale, replace List with a field indexer keyed on
// spec.infrastructure.registryRef so this does not load every SwarmAgent in
// the list scope on every reconcile.
func resolveGatewayCapabilities(ctx context.Context, c client.Reader, gateway *kubeswarmv1alpha1.SwarmAgent) ([]GatewayCapabilityEntry, error) {
	if gateway.Spec.Gateway == nil {
		return nil, nil
	}
	gw := gateway.Spec.Gateway
	logger := log.FromContext(ctx).WithName("gateway")

	// Resolve the referenced registry. Registry is always looked up in the
	// gateway's own namespace - cluster-wide scope affects which agents the
	// registry indexes, not where the registry object lives.
	var reg kubeswarmv1alpha1.SwarmRegistry
	if err := c.Get(ctx, client.ObjectKey{Name: gw.RegistryRef.Name, Namespace: gateway.Namespace}, &reg); err != nil {
		if errors.IsNotFound(err) {
			logger.V(1).Info("referenced SwarmRegistry not found; gateway has no capabilities",
				"registry", gw.RegistryRef.Name, "namespace", gateway.Namespace)
			return nil, nil
		}
		return nil, fmt.Errorf("fetching SwarmRegistry %q: %w", gw.RegistryRef.Name, err)
	}

	// Collect candidate agents by registry scope.
	var agents kubeswarmv1alpha1.SwarmAgentList
	listOpts := []client.ListOption{}
	if reg.Spec.Scope != kubeswarmv1alpha1.RegistryScopeCluster {
		listOpts = append(listOpts, client.InNamespace(gateway.Namespace))
	}
	if err := c.List(ctx, &agents, listOpts...); err != nil {
		return nil, fmt.Errorf("listing SwarmAgents for registry %q: %w", reg.Name, err)
	}

	var entries []GatewayCapabilityEntry
	for i := range agents.Items {
		agent := &agents.Items[i]
		// Skip self (match on namespaced name, not name alone).
		if agent.Name == gateway.Name && agent.Namespace == gateway.Namespace {
			continue
		}
		// Skip agents that have not opted into this registry.
		if agent.Spec.Infrastructure == nil ||
			agent.Spec.Infrastructure.RegistryRef == nil ||
			agent.Spec.Infrastructure.RegistryRef.Name != reg.Name {
			continue
		}
		// Skip agents with no capabilities.
		if len(agent.Spec.Capabilities) == 0 {
			continue
		}
		isGateway := agent.Spec.Gateway != nil
		for _, ac := range agent.Spec.Capabilities {
			entries = append(entries, GatewayCapabilityEntry{
				Name:          ac.Name,
				Description:   ac.Description,
				Agent:         agent.Name,
				Namespace:     agent.Namespace,
				Tags:          ac.Tags,
				ReadyReplicas: agent.Status.ReadyReplicas,
				IsGateway:     isGateway,
			})
		}
	}
	return entries, nil
}

// hasAllTags returns true if capTags contains all required tags (case-insensitive).
func hasAllTags(capTags []string, required []string) bool {
	for _, r := range required {
		found := false
		for _, t := range capTags {
			if strings.EqualFold(t, r) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}
