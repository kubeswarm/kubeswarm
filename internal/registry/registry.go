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
	"math/rand"
	"sort"
	"sync"

	kubeswarmv1alpha1 "github.com/kubeswarm/kubeswarm/api/v1alpha1"
)

type entry struct {
	agentName string
	caps      []kubeswarmv1alpha1.AgentCapability
}

// Registry is a thread-safe in-process capability index.
// buckets is keyed by "namespace/name" of the owning SwarmRegistry CR so multiple
// SwarmRegistry instances coexist without overwriting each other.
type Registry struct {
	mu      sync.RWMutex
	buckets map[string][]entry
}

// Rebuild atomically replaces the entries for one SwarmRegistry CR (key = "namespace/name").
// Does not touch other registries' buckets.
func (r *Registry) Rebuild(key string, agents []kubeswarmv1alpha1.SwarmAgent) {
	newEntries := make([]entry, 0, len(agents))
	for _, a := range agents {
		if len(a.Spec.Capabilities) == 0 {
			continue
		}
		newEntries = append(newEntries, entry{
			agentName: a.Name,
			caps:      a.Spec.Capabilities,
		})
	}
	// Sort by agent name for deterministic round-robin.
	sort.Slice(newEntries, func(i, j int) bool {
		return newEntries[i].agentName < newEntries[j].agentName
	})
	r.mu.Lock()
	if r.buckets == nil {
		r.buckets = make(map[string][]entry)
	}
	r.buckets[key] = newEntries
	r.mu.Unlock()
}

// Remove deletes the bucket for a deleted SwarmRegistry CR.
func (r *Registry) Remove(key string) {
	r.mu.Lock()
	delete(r.buckets, key)
	r.mu.Unlock()
}

// ResolveRequest carries the parameters of a single registry lookup.
type ResolveRequest struct {
	Capability string
	Tags       []string
	Strategy   kubeswarmv1alpha1.RegistryLookupStrategy
}

// Resolve returns the agent name that best matches the request, or "" if no agent matches.
func (r *Registry) Resolve(req ResolveRequest) string {
	r.mu.RLock()
	candidates := r.candidates(req.Capability, req.Tags)
	r.mu.RUnlock()

	if len(candidates) == 0 {
		return ""
	}
	switch req.Strategy {
	case kubeswarmv1alpha1.RegistryLookupStrategyRandom:
		return candidates[rand.Intn(len(candidates))] //nolint:gosec
	case kubeswarmv1alpha1.RegistryLookupStrategyRoundRobin:
		idx := stableHash(req.Capability) % len(candidates)
		return candidates[idx]
	default: // least-busy — first in sorted list
		return candidates[0]
	}
}

// Snapshot returns a read-only capability snapshot across all buckets for status reporting.
func (r *Registry) Snapshot() []kubeswarmv1alpha1.IndexedCapability {
	r.mu.RLock()
	defer r.mu.RUnlock()

	type agg struct {
		agents map[string]struct{}
		tags   map[string]struct{}
	}
	m := make(map[string]*agg)
	for _, entries := range r.buckets {
		for _, e := range entries {
			for _, c := range e.caps {
				a, ok := m[c.Name]
				if !ok {
					a = &agg{agents: make(map[string]struct{}), tags: make(map[string]struct{})}
					m[c.Name] = a
				}
				a.agents[e.agentName] = struct{}{}
				for _, t := range c.Tags {
					a.tags[t] = struct{}{}
				}
			}
		}
	}
	out := make([]kubeswarmv1alpha1.IndexedCapability, 0, len(m))
	for id, a := range m {
		ic := kubeswarmv1alpha1.IndexedCapability{ID: id}
		for name := range a.agents {
			ic.Agents = append(ic.Agents, name)
		}
		sort.Strings(ic.Agents)
		for t := range a.tags {
			ic.Tags = append(ic.Tags, t)
		}
		sort.Strings(ic.Tags)
		out = append(out, ic)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// AgentCount returns the total number of indexed agents across all buckets.
func (r *Registry) AgentCount() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	n := 0
	for _, entries := range r.buckets {
		n += len(entries)
	}
	return n
}

func (r *Registry) candidates(capID string, tags []string) []string {
	var out []string
	seen := make(map[string]struct{})
	for _, entries := range r.buckets {
		for _, e := range entries {
			if _, already := seen[e.agentName]; already {
				continue
			}
			for _, c := range e.caps {
				if c.Name != capID {
					continue
				}
				if !hasAllTags(c.Tags, tags) {
					continue
				}
				out = append(out, e.agentName)
				seen[e.agentName] = struct{}{}
				break
			}
		}
	}
	sort.Strings(out) // deterministic ordering
	return out
}

func hasAllTags(agentTags, required []string) bool {
	if len(required) == 0 {
		return true
	}
	set := make(map[string]struct{}, len(agentTags))
	for _, t := range agentTags {
		set[t] = struct{}{}
	}
	for _, t := range required {
		if _, ok := set[t]; !ok {
			return false
		}
	}
	return true
}

func stableHash(s string) int {
	h := 0
	for i := range len(s) {
		h = h*31 + int(s[i])
	}
	if h < 0 {
		h = -h
	}
	return h
}
