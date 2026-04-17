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
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kubeswarmv1alpha1 "github.com/kubeswarm/kubeswarm/api/v1alpha1"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// gwEnvVal finds an env var by name in a slice. Returns ("", false) if not found.
func gwEnvVal(envs []corev1.EnvVar, name string) (string, bool) {
	for _, e := range envs {
		if e.Name == name {
			return e.Value, true
		}
	}
	return "", false
}

func baseGatewayAgent() *kubeswarmv1alpha1.SwarmAgent {
	return &kubeswarmv1alpha1.SwarmAgent{
		ObjectMeta: metav1.ObjectMeta{Name: "gw-agent", Namespace: "default"},
		Spec: kubeswarmv1alpha1.SwarmAgentSpec{
			Model:  "claude-sonnet-4-6",
			Prompt: &kubeswarmv1alpha1.AgentPrompt{Inline: "You are a gateway agent."},
			Gateway: &kubeswarmv1alpha1.GatewayConfig{
				RegistryRef:  corev1.LocalObjectReference{Name: "my-registry"},
				DispatchMode: kubeswarmv1alpha1.GatewayDispatchEnabled,
			},
		},
	}
}

func sampleCapabilities() []GatewayCapabilityEntry {
	return []GatewayCapabilityEntry{
		{Name: "code-review", Description: "Reviews code", Agent: "reviewer", Namespace: "default", Tags: []string{"code"}, ReadyReplicas: 3},
		{Name: "summarize", Description: "Summarizes text", Agent: "summarizer", Namespace: "default", Tags: []string{"text"}, ReadyReplicas: 1},
	}
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestBuildGatewayEnvVars_NilGateway_ReturnsNil(t *testing.T) {
	agent := &kubeswarmv1alpha1.SwarmAgent{
		ObjectMeta: metav1.ObjectMeta{Name: "plain-agent", Namespace: "default"},
		Spec: kubeswarmv1alpha1.SwarmAgentSpec{
			Model:  "claude-sonnet-4-6",
			Prompt: &kubeswarmv1alpha1.AgentPrompt{Inline: "test"},
		},
	}
	envs := buildGatewayEnvVars(agent, nil)
	if envs != nil {
		t.Errorf("expected nil when spec.gateway is nil, got %v", envs)
	}
}

func TestBuildGatewayEnvVars_Capabilities_JSON(t *testing.T) {
	agent := baseGatewayAgent()
	caps := sampleCapabilities()
	envs := buildGatewayEnvVars(agent, caps)

	val, ok := gwEnvVal(envs, "AGENT_GATEWAY_CAPABILITIES")
	if !ok {
		t.Fatal("expected AGENT_GATEWAY_CAPABILITIES env var, not found")
	}

	var decoded []map[string]any
	if err := json.Unmarshal([]byte(val), &decoded); err != nil {
		t.Fatalf("failed to unmarshal AGENT_GATEWAY_CAPABILITIES: %v", err)
	}
	if len(decoded) != 2 {
		t.Errorf("expected 2 capabilities, got %d", len(decoded))
	}
	// First entry should be code-review (highest readyReplicas).
	if decoded[0]["name"] != "code-review" {
		t.Errorf("expected first capability name = code-review, got %v", decoded[0]["name"])
	}
	if decoded[0]["agent"] != "reviewer" {
		t.Errorf("expected first capability agent = reviewer, got %v", decoded[0]["agent"])
	}
}

func TestBuildGatewayEnvVars_Config_JSON(t *testing.T) {
	agent := baseGatewayAgent()
	agent.Spec.Gateway.DispatchMode = kubeswarmv1alpha1.GatewayDispatchEnabled
	envs := buildGatewayEnvVars(agent, sampleCapabilities())

	val, ok := gwEnvVal(envs, "AGENT_GATEWAY_CONFIG")
	if !ok {
		t.Fatal("expected AGENT_GATEWAY_CONFIG env var, not found")
	}

	var cfg map[string]any
	if err := json.Unmarshal([]byte(val), &cfg); err != nil {
		t.Fatalf("failed to unmarshal AGENT_GATEWAY_CONFIG: %v", err)
	}
	if cfg["dispatchMode"] != "enabled" {
		t.Errorf("expected dispatchMode = enabled, got %v", cfg["dispatchMode"])
	}
}

func TestBuildGatewayEnvVars_Tools_Injected(t *testing.T) {
	agent := baseGatewayAgent()
	envs := buildGatewayEnvVars(agent, sampleCapabilities())

	_, ok := gwEnvVal(envs, "AGENT_GATEWAY_TOOLS")
	if !ok {
		t.Fatal("expected AGENT_GATEWAY_TOOLS env var, not found")
	}
}

func TestBuildGatewayEnvVars_FilterByTags_AND_Semantics(t *testing.T) {
	agent := baseGatewayAgent()
	agent.Spec.Gateway.FilterByTags = []string{"code", "review"}

	caps := []GatewayCapabilityEntry{
		{Name: "cap-both", Description: "Has both", Agent: "a", Namespace: "default", Tags: []string{"code", "review"}, ReadyReplicas: 2},
		{Name: "cap-one", Description: "Has one", Agent: "b", Namespace: "default", Tags: []string{"code"}, ReadyReplicas: 1},
		{Name: "cap-none", Description: "Has none", Agent: "c", Namespace: "default", Tags: []string{"text"}, ReadyReplicas: 1},
	}
	envs := buildGatewayEnvVars(agent, caps)

	val, ok := gwEnvVal(envs, "AGENT_GATEWAY_CAPABILITIES")
	if !ok {
		t.Fatal("expected AGENT_GATEWAY_CAPABILITIES env var, not found")
	}

	var decoded []map[string]any
	if err := json.Unmarshal([]byte(val), &decoded); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}
	if len(decoded) != 1 {
		t.Fatalf("expected 1 capability after tag filter (AND semantics), got %d", len(decoded))
	}
	if decoded[0]["name"] != "cap-both" {
		t.Errorf("expected filtered capability = cap-both, got %v", decoded[0]["name"])
	}
}

func TestBuildGatewayEnvVars_FiltersOutZeroReadyReplicas(t *testing.T) {
	agent := baseGatewayAgent()
	caps := []GatewayCapabilityEntry{
		{Name: "healthy", Description: "Healthy", Agent: "a", Namespace: "default", ReadyReplicas: 2},
		{Name: "down", Description: "Down", Agent: "b", Namespace: "default", ReadyReplicas: 0},
	}
	envs := buildGatewayEnvVars(agent, caps)

	val, ok := gwEnvVal(envs, "AGENT_GATEWAY_CAPABILITIES")
	if !ok {
		t.Fatal("expected AGENT_GATEWAY_CAPABILITIES env var, not found")
	}

	var decoded []map[string]any
	if err := json.Unmarshal([]byte(val), &decoded); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}
	if len(decoded) != 1 {
		t.Fatalf("expected 1 capability after filtering zero replicas, got %d", len(decoded))
	}
	if decoded[0]["name"] != "healthy" {
		t.Errorf("expected remaining capability = healthy, got %v", decoded[0]["name"])
	}
}

func TestBuildGatewayEnvVars_FiltersOutGatewayAgents(t *testing.T) {
	agent := baseGatewayAgent()
	agent.Spec.Gateway.AllowGatewayTargets = false

	caps := []GatewayCapabilityEntry{
		{Name: "regular", Description: "Regular", Agent: "a", Namespace: "default", ReadyReplicas: 2, IsGateway: false},
		{Name: "another-gw", Description: "Gateway", Agent: "b", Namespace: "default", ReadyReplicas: 3, IsGateway: true},
	}
	envs := buildGatewayEnvVars(agent, caps)

	val, ok := gwEnvVal(envs, "AGENT_GATEWAY_CAPABILITIES")
	if !ok {
		t.Fatal("expected AGENT_GATEWAY_CAPABILITIES env var, not found")
	}

	var decoded []map[string]any
	if err := json.Unmarshal([]byte(val), &decoded); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}
	if len(decoded) != 1 {
		t.Fatalf("expected 1 capability after filtering gateway agents, got %d", len(decoded))
	}
	if decoded[0]["name"] != "regular" {
		t.Errorf("expected remaining capability = regular, got %v", decoded[0]["name"])
	}
}

func TestBuildGatewayEnvVars_AllowGatewayTargets_IncludesGateways(t *testing.T) {
	agent := baseGatewayAgent()
	agent.Spec.Gateway.AllowGatewayTargets = true

	caps := []GatewayCapabilityEntry{
		{Name: "regular", Description: "Regular", Agent: "a", Namespace: "default", ReadyReplicas: 2, IsGateway: false},
		{Name: "another-gw", Description: "Gateway", Agent: "b", Namespace: "default", ReadyReplicas: 3, IsGateway: true},
	}
	envs := buildGatewayEnvVars(agent, caps)

	val, ok := gwEnvVal(envs, "AGENT_GATEWAY_CAPABILITIES")
	if !ok {
		t.Fatal("expected AGENT_GATEWAY_CAPABILITIES env var, not found")
	}

	var decoded []map[string]any
	if err := json.Unmarshal([]byte(val), &decoded); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}
	if len(decoded) != 2 {
		t.Errorf("expected 2 capabilities when allowGatewayTargets is true, got %d", len(decoded))
	}
}

func TestBuildGatewayEnvVars_CapsAt50_SortedByReplicasDescThenNameAsc(t *testing.T) {
	agent := baseGatewayAgent()

	// Create 60 capabilities with varying replicas.
	caps := make([]GatewayCapabilityEntry, 60)
	for i := range 60 {
		caps[i] = GatewayCapabilityEntry{
			Name:          capName(i),
			Description:   "test",
			Agent:         capName(i),
			Namespace:     "default",
			ReadyReplicas: int32(60 - i), // descending replicas
		}
	}
	envs := buildGatewayEnvVars(agent, caps)

	val, ok := gwEnvVal(envs, "AGENT_GATEWAY_CAPABILITIES")
	if !ok {
		t.Fatal("expected AGENT_GATEWAY_CAPABILITIES env var, not found")
	}

	var decoded []map[string]any
	if err := json.Unmarshal([]byte(val), &decoded); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}
	if len(decoded) != 50 {
		t.Errorf("expected capability list capped at 50, got %d", len(decoded))
	}

	// Verify sorting: first entry should have highest readyReplicas.
	firstReplicas := decoded[0]["readyReplicas"].(float64)
	lastReplicas := decoded[49]["readyReplicas"].(float64)
	if firstReplicas < lastReplicas {
		t.Errorf("expected descending order by readyReplicas, first=%v last=%v", firstReplicas, lastReplicas)
	}
}

func TestBuildGatewayEnvVars_SortTieBreaker_NameAsc(t *testing.T) {
	agent := baseGatewayAgent()
	caps := []GatewayCapabilityEntry{
		{Name: "zebra", Description: "z", Agent: "z", Namespace: "default", ReadyReplicas: 2},
		{Name: "alpha", Description: "a", Agent: "a", Namespace: "default", ReadyReplicas: 2},
		{Name: "beta", Description: "b", Agent: "b", Namespace: "default", ReadyReplicas: 2},
	}
	envs := buildGatewayEnvVars(agent, caps)

	val, ok := gwEnvVal(envs, "AGENT_GATEWAY_CAPABILITIES")
	if !ok {
		t.Fatal("expected AGENT_GATEWAY_CAPABILITIES env var, not found")
	}

	var decoded []map[string]any
	if err := json.Unmarshal([]byte(val), &decoded); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}
	if len(decoded) != 3 {
		t.Fatalf("expected 3 capabilities, got %d", len(decoded))
	}
	// Same readyReplicas - should be sorted alphabetically by name.
	if decoded[0]["name"] != "alpha" {
		t.Errorf("expected first = alpha, got %v", decoded[0]["name"])
	}
	if decoded[1]["name"] != "beta" {
		t.Errorf("expected second = beta, got %v", decoded[1]["name"])
	}
	if decoded[2]["name"] != "zebra" {
		t.Errorf("expected third = zebra, got %v", decoded[2]["name"])
	}
}

func TestBuildGatewayEnvVars_DispatchDisabled_NoDispatchTool(t *testing.T) {
	agent := baseGatewayAgent()
	agent.Spec.Gateway.DispatchMode = kubeswarmv1alpha1.GatewayDispatchDisabled

	envs := buildGatewayEnvVars(agent, sampleCapabilities())

	val, ok := gwEnvVal(envs, "AGENT_GATEWAY_TOOLS")
	if !ok {
		// When dispatch is disabled, tools env var might be absent entirely or
		// present without the dispatch tool. Both are acceptable.
		return
	}

	var tools []map[string]any
	if err := json.Unmarshal([]byte(val), &tools); err != nil {
		t.Fatalf("failed to unmarshal AGENT_GATEWAY_TOOLS: %v", err)
	}
	for _, tool := range tools {
		if tool["name"] == "dispatch" {
			t.Error("expected no dispatch tool when dispatchMode is disabled")
		}
	}
}

func TestBuildGatewayEnvVars_IsGatewayNotSerialized(t *testing.T) {
	agent := baseGatewayAgent()
	agent.Spec.Gateway.AllowGatewayTargets = true

	caps := []GatewayCapabilityEntry{
		{Name: "cap", Description: "desc", Agent: "a", Namespace: "default", ReadyReplicas: 1, IsGateway: true},
	}
	envs := buildGatewayEnvVars(agent, caps)

	val, ok := gwEnvVal(envs, "AGENT_GATEWAY_CAPABILITIES")
	if !ok {
		t.Fatal("expected AGENT_GATEWAY_CAPABILITIES env var, not found")
	}

	// isGateway has json:"-" so it should not appear in the output.
	var decoded []map[string]any
	if err := json.Unmarshal([]byte(val), &decoded); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}
	if _, exists := decoded[0]["isGateway"]; exists {
		t.Error("IsGateway should not be serialized (json:\"-\")")
	}
}

func capName(i int) string {
	return fmt.Sprintf("cap-%02d", i)
}

// ---------------------------------------------------------------------------
// resolveGatewayCapabilities tests
// ---------------------------------------------------------------------------

func gatewayTestClient(objs ...client.Object) client.Client {
	s := runtime.NewScheme()
	_ = kubeswarmv1alpha1.AddToScheme(s)
	return fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(objs...).
		Build()
}

func makeRegistry(name, namespace string, scope kubeswarmv1alpha1.RegistryScope) *kubeswarmv1alpha1.SwarmRegistry {
	return &kubeswarmv1alpha1.SwarmRegistry{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec:       kubeswarmv1alpha1.SwarmRegistrySpec{Scope: scope},
	}
}

// memberAgent returns a SwarmAgent whose infrastructure.registryRef is set to registryName,
// with a single capability and one ready replica.
func memberAgent(name, namespace, registryName, capabilityName string) *kubeswarmv1alpha1.SwarmAgent {
	return &kubeswarmv1alpha1.SwarmAgent{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: kubeswarmv1alpha1.SwarmAgentSpec{
			Model: "claude-sonnet-4-6",
			Infrastructure: &kubeswarmv1alpha1.AgentInfrastructure{
				RegistryRef: &corev1.LocalObjectReference{Name: registryName},
			},
			Capabilities: []kubeswarmv1alpha1.AgentCapability{
				{Name: capabilityName, Description: "desc", Tags: []string{"t"}},
			},
		},
		Status: kubeswarmv1alpha1.SwarmAgentStatus{ReadyReplicas: 1},
	}
}

// gatewayAgent builds a SwarmAgent with a gateway block pointing at registryName
// in the default namespace. All callers use "default" - if a cross-namespace
// gateway is needed, set .Namespace on the returned object.
func gatewayAgent(registryName string) *kubeswarmv1alpha1.SwarmAgent {
	return &kubeswarmv1alpha1.SwarmAgent{
		ObjectMeta: metav1.ObjectMeta{Name: "gw", Namespace: "default"},
		Spec: kubeswarmv1alpha1.SwarmAgentSpec{
			Model: "claude-sonnet-4-6",
			Gateway: &kubeswarmv1alpha1.GatewayConfig{
				RegistryRef: corev1.LocalObjectReference{Name: registryName},
			},
		},
	}
}

func entryNames(entries []GatewayCapabilityEntry) []string {
	names := make([]string, len(entries))
	for i, e := range entries {
		names[i] = e.Agent + "/" + e.Name
	}
	return names
}

func TestResolveGatewayCapabilities(t *testing.T) {
	ctx := context.Background()

	cases := []struct {
		name       string
		scope      kubeswarmv1alpha1.RegistryScope
		gateway    *kubeswarmv1alpha1.SwarmAgent
		extraObjs  []client.Object
		wantAgents []string // "agent/capability" strings, any order
		wantErr    bool
	}{
		{
			name:    "filters agents belonging to a different registry",
			scope:   kubeswarmv1alpha1.RegistryScopeNamespace,
			gateway: gatewayAgent("reg-a"),
			extraObjs: []client.Object{
				memberAgent("in-a", "default", "reg-a", "cap-a"),
				memberAgent("in-b", "default", "reg-b", "cap-b"),
			},
			wantAgents: []string{"in-a/cap-a"},
		},
		{
			name:    "cluster-scope discovery includes cross-namespace members",
			scope:   kubeswarmv1alpha1.RegistryScopeCluster,
			gateway: gatewayAgent("reg-a"),
			extraObjs: []client.Object{
				memberAgent("in-default", "default", "reg-a", "cap-1"),
				memberAgent("in-other", "other-ns", "reg-a", "cap-2"),
			},
			wantAgents: []string{"in-default/cap-1", "in-other/cap-2"},
		},
		{
			name:    "namespace-scope excludes other namespaces even when registryRef matches",
			scope:   kubeswarmv1alpha1.RegistryScopeNamespace,
			gateway: gatewayAgent("reg-a"),
			extraObjs: []client.Object{
				memberAgent("local", "default", "reg-a", "cap-local"),
				memberAgent("remote", "other-ns", "reg-a", "cap-remote"),
			},
			wantAgents: []string{"local/cap-local"},
		},
		{
			name:  "skips self even when self is a registry member",
			scope: kubeswarmv1alpha1.RegistryScopeNamespace,
			// The gateway itself is also registered into reg-a and advertises a
			// capability. It must be excluded to avoid self-dispatch loops.
			gateway: func() *kubeswarmv1alpha1.SwarmAgent {
				g := gatewayAgent("reg-a")
				g.Spec.Infrastructure = &kubeswarmv1alpha1.AgentInfrastructure{
					RegistryRef: &corev1.LocalObjectReference{Name: "reg-a"},
				}
				g.Spec.Capabilities = []kubeswarmv1alpha1.AgentCapability{
					{Name: "self-cap", Description: "self", Tags: []string{"t"}},
				}
				g.Status.ReadyReplicas = 1
				return g
			}(),
			extraObjs: []client.Object{
				memberAgent("other", "default", "reg-a", "cap-other"),
			},
			wantAgents: []string{"other/cap-other"},
		},
		{
			name:    "skips agents with no capabilities",
			scope:   kubeswarmv1alpha1.RegistryScopeNamespace,
			gateway: gatewayAgent("reg-a"),
			extraObjs: []client.Object{
				func() client.Object {
					a := memberAgent("no-cap", "default", "reg-a", "ignored")
					a.Spec.Capabilities = nil
					return a
				}(),
				memberAgent("with-cap", "default", "reg-a", "cap-yes"),
			},
			wantAgents: []string{"with-cap/cap-yes"},
		},
		{
			name:       "missing registry returns (nil, nil) - non-fatal",
			scope:      kubeswarmv1alpha1.RegistryScopeNamespace,
			gateway:    gatewayAgent("missing-reg"),
			extraObjs:  nil, // no registry created
			wantAgents: nil,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			objs := []client.Object{tc.gateway}
			// Add registry unless the test omits it.
			if tc.name != "missing registry returns (nil, nil) - non-fatal" {
				objs = append(objs, makeRegistry(tc.gateway.Spec.Gateway.RegistryRef.Name, tc.gateway.Namespace, tc.scope))
			}
			objs = append(objs, tc.extraObjs...)

			c := gatewayTestClient(objs...)
			entries, err := resolveGatewayCapabilities(ctx, c, tc.gateway)
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			got := entryNames(entries)
			if len(got) != len(tc.wantAgents) {
				t.Fatalf("got %d entries (%v), want %d (%v)", len(got), got, len(tc.wantAgents), tc.wantAgents)
			}
			gotSet := map[string]struct{}{}
			for _, g := range got {
				gotSet[g] = struct{}{}
			}
			for _, want := range tc.wantAgents {
				if _, ok := gotSet[want]; !ok {
					t.Errorf("missing expected entry %q; got %v", want, got)
				}
			}
		})
	}
}
