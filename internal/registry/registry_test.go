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

package registry

import (
	"testing"

	kubeswarmv1alpha1 "github.com/kubeswarm/kubeswarm/api/v1alpha1"
)

func namedAgent(name string, caps ...kubeswarmv1alpha1.AgentCapability) kubeswarmv1alpha1.SwarmAgent {
	a := kubeswarmv1alpha1.SwarmAgent{}
	a.Name = name
	a.Spec.Capabilities = caps
	return a
}

func cap(name string, tags ...string) kubeswarmv1alpha1.AgentCapability {
	return kubeswarmv1alpha1.AgentCapability{Name: name, Tags: tags}
}

func TestRebuild_IndexesAgents(t *testing.T) {
	r := &Registry{}
	r.Rebuild("ns/reg", []kubeswarmv1alpha1.SwarmAgent{
		namedAgent("a1", cap("code-review")),
		namedAgent("a2", cap("search")),
	})
	if r.AgentCount() != 2 {
		t.Errorf("AgentCount = %d, want 2", r.AgentCount())
	}
}

func TestRebuild_SkipsAgentsWithoutCapabilities(t *testing.T) {
	r := &Registry{}
	r.Rebuild("ns/reg", []kubeswarmv1alpha1.SwarmAgent{
		namedAgent("a1", cap("code-review")),
		namedAgent("a2"), // no capabilities
	})
	if r.AgentCount() != 1 {
		t.Errorf("AgentCount = %d, want 1", r.AgentCount())
	}
}

func TestRebuild_ReplacesExistingBucket(t *testing.T) {
	r := &Registry{}
	r.Rebuild("ns/reg", []kubeswarmv1alpha1.SwarmAgent{
		namedAgent("a1", cap("old")),
	})
	r.Rebuild("ns/reg", []kubeswarmv1alpha1.SwarmAgent{
		namedAgent("a2", cap("new")),
	})
	if r.AgentCount() != 1 {
		t.Errorf("AgentCount = %d, want 1", r.AgentCount())
	}
	if got := r.Resolve(ResolveRequest{Capability: "new"}); got != "a2" {
		t.Errorf("Resolve(new) = %q, want a2", got)
	}
}

func TestRebuild_MultipleBucketsCoexist(t *testing.T) {
	r := &Registry{}
	r.Rebuild("ns/reg1", []kubeswarmv1alpha1.SwarmAgent{
		namedAgent("a1", cap("search")),
	})
	r.Rebuild("ns/reg2", []kubeswarmv1alpha1.SwarmAgent{
		namedAgent("a2", cap("search")),
	})
	if r.AgentCount() != 2 {
		t.Errorf("AgentCount = %d, want 2", r.AgentCount())
	}
}

func TestRemove_DeletesBucket(t *testing.T) {
	r := &Registry{}
	r.Rebuild("ns/reg", []kubeswarmv1alpha1.SwarmAgent{
		namedAgent("a1", cap("search")),
	})
	r.Remove("ns/reg")
	if r.AgentCount() != 0 {
		t.Errorf("AgentCount = %d after Remove, want 0", r.AgentCount())
	}
}

func TestRemove_NonexistentKey(t *testing.T) {
	r := &Registry{}
	r.Remove("does/not/exist") // should not panic
}

func TestResolve_NoMatch_ReturnsEmpty(t *testing.T) {
	r := &Registry{}
	r.Rebuild("ns/reg", []kubeswarmv1alpha1.SwarmAgent{
		namedAgent("a1", cap("search")),
	})
	if got := r.Resolve(ResolveRequest{Capability: "nonexistent"}); got != "" {
		t.Errorf("Resolve(nonexistent) = %q, want empty", got)
	}
}

func TestResolve_Random_ReturnsCandidate(t *testing.T) {
	r := &Registry{}
	r.Rebuild("ns/reg", []kubeswarmv1alpha1.SwarmAgent{
		namedAgent("a1", cap("search")),
		namedAgent("a2", cap("search")),
	})
	got := r.Resolve(ResolveRequest{
		Capability: "search",
		Strategy:   kubeswarmv1alpha1.RegistryLookupStrategyRandom,
	})
	if got != "a1" && got != "a2" {
		t.Errorf("Resolve(random) = %q, want a1 or a2", got)
	}
}

func TestResolve_RoundRobin_ReturnsDeterministic(t *testing.T) {
	r := &Registry{}
	r.Rebuild("ns/reg", []kubeswarmv1alpha1.SwarmAgent{
		namedAgent("a1", cap("search")),
		namedAgent("a2", cap("search")),
	})
	got1 := r.Resolve(ResolveRequest{
		Capability: "search",
		Strategy:   kubeswarmv1alpha1.RegistryLookupStrategyRoundRobin,
	})
	got2 := r.Resolve(ResolveRequest{
		Capability: "search",
		Strategy:   kubeswarmv1alpha1.RegistryLookupStrategyRoundRobin,
	})
	// Same input = same output (deterministic).
	if got1 != got2 {
		t.Errorf("RoundRobin not deterministic: %q != %q", got1, got2)
	}
}

func TestResolve_LeastBusy_ReturnsFirstSorted(t *testing.T) {
	r := &Registry{}
	r.Rebuild("ns/reg", []kubeswarmv1alpha1.SwarmAgent{
		namedAgent("b-agent", cap("search")),
		namedAgent("a-agent", cap("search")),
	})
	got := r.Resolve(ResolveRequest{
		Capability: "search",
		// Default strategy = least-busy = first sorted
	})
	if got != "a-agent" {
		t.Errorf("Resolve(least-busy) = %q, want a-agent", got)
	}
}

func TestResolve_WithTags_FiltersCorrectly(t *testing.T) {
	r := &Registry{}
	r.Rebuild("ns/reg", []kubeswarmv1alpha1.SwarmAgent{
		namedAgent("a1", cap("search", "fast", "code")),
		namedAgent("a2", cap("search", "slow")),
	})

	// Require "fast" tag - only a1 matches.
	got := r.Resolve(ResolveRequest{Capability: "search", Tags: []string{"fast"}})
	if got != "a1" {
		t.Errorf("Resolve with tag 'fast' = %q, want a1", got)
	}

	// Require "slow" tag - only a2 matches.
	got = r.Resolve(ResolveRequest{Capability: "search", Tags: []string{"slow"}})
	if got != "a2" {
		t.Errorf("Resolve with tag 'slow' = %q, want a2", got)
	}

	// Require both "fast" and "code" - only a1 matches.
	got = r.Resolve(ResolveRequest{Capability: "search", Tags: []string{"fast", "code"}})
	if got != "a1" {
		t.Errorf("Resolve with tags 'fast,code' = %q, want a1", got)
	}

	// Require "nonexistent" tag - no match.
	got = r.Resolve(ResolveRequest{Capability: "search", Tags: []string{"nonexistent"}})
	if got != "" {
		t.Errorf("Resolve with bad tag = %q, want empty", got)
	}
}

func TestSnapshot_ReturnsAllCapabilities(t *testing.T) {
	r := &Registry{}
	r.Rebuild("ns/reg", []kubeswarmv1alpha1.SwarmAgent{
		namedAgent("a1", cap("search", "fast"), cap("review")),
		namedAgent("a2", cap("search", "slow")),
	})
	snap := r.Snapshot()
	if len(snap) != 2 {
		t.Fatalf("Snapshot len = %d, want 2", len(snap))
	}

	// Snapshot is sorted by ID.
	if snap[0].ID != "review" {
		t.Errorf("snap[0].ID = %q, want review", snap[0].ID)
	}
	if snap[1].ID != "search" {
		t.Errorf("snap[1].ID = %q, want search", snap[1].ID)
	}

	// "search" should have 2 agents and 2 tags.
	searchCap := snap[1]
	if len(searchCap.Agents) != 2 {
		t.Errorf("search agents = %d, want 2", len(searchCap.Agents))
	}
	if len(searchCap.Tags) != 2 {
		t.Errorf("search tags = %d, want 2", len(searchCap.Tags))
	}
}

func TestAgentCount_EmptyRegistry(t *testing.T) {
	r := &Registry{}
	if r.AgentCount() != 0 {
		t.Errorf("AgentCount on empty = %d, want 0", r.AgentCount())
	}
}

func TestHasAllTags(t *testing.T) {
	tests := []struct {
		name     string
		agent    []string
		required []string
		want     bool
	}{
		{"no required tags", []string{"a", "b"}, nil, true},
		{"empty required", []string{"a"}, []string{}, true},
		{"all present", []string{"a", "b", "c"}, []string{"a", "c"}, true},
		{"missing one", []string{"a", "b"}, []string{"a", "c"}, false},
		{"empty agent tags", nil, []string{"a"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := hasAllTags(tt.agent, tt.required); got != tt.want {
				t.Errorf("hasAllTags(%v, %v) = %v, want %v", tt.agent, tt.required, got, tt.want)
			}
		})
	}
}

func TestStableHash_Deterministic(t *testing.T) {
	h1 := stableHash("search")
	h2 := stableHash("search")
	if h1 != h2 {
		t.Errorf("stableHash not deterministic: %d != %d", h1, h2)
	}
	// Different inputs should (very likely) produce different hashes.
	h3 := stableHash("review")
	if h1 == h3 {
		t.Error("stableHash collision for 'search' and 'review'")
	}
}

func TestStableHash_NonNegative(t *testing.T) {
	// Test a variety of inputs to ensure non-negative.
	for _, s := range []string{"", "a", "test", "very-long-capability-name-that-might-overflow"} {
		if h := stableHash(s); h < 0 {
			t.Errorf("stableHash(%q) = %d, want non-negative", s, h)
		}
	}
}
