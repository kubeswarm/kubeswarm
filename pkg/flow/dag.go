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

// Package flow contains pure business logic for SwarmTeam DAG execution.
// It has no dependency on Kubernetes controller-runtime or Redis —
// the operator and the swarm CLI both import it.
package flow

import (
	"fmt"

	kubeswarmv1alpha1 "github.com/kubeswarm/kubeswarm/api/v1alpha1"
)

// IsTerminalTeamPhase reports whether an SwarmTeam pipeline has reached a final state.
func IsTerminalTeamPhase(phase kubeswarmv1alpha1.SwarmTeamPhase) bool {
	return phase == kubeswarmv1alpha1.SwarmTeamPhaseSucceeded || phase == kubeswarmv1alpha1.SwarmTeamPhaseFailed
}

// IsTerminalRunPhase reports whether an SwarmRun has reached a final state.
func IsTerminalRunPhase(phase kubeswarmv1alpha1.SwarmRunPhase) bool {
	return phase == kubeswarmv1alpha1.SwarmRunPhaseSucceeded || phase == kubeswarmv1alpha1.SwarmRunPhaseFailed
}

// BuildRunStatusByName returns a name→status pointer map for SwarmRun pipeline step lookups.
func BuildRunStatusByName(f *kubeswarmv1alpha1.SwarmRun) map[string]*kubeswarmv1alpha1.SwarmFlowStepStatus {
	m := make(map[string]*kubeswarmv1alpha1.SwarmFlowStepStatus, len(f.Status.Steps))
	for i := range f.Status.Steps {
		m[f.Status.Steps[i].Name] = &f.Status.Steps[i]
	}
	return m
}

// DepsSucceeded returns true when every name in deps has completed (Succeeded or Skipped).
func DepsSucceeded(deps []string, statusByName map[string]*kubeswarmv1alpha1.SwarmFlowStepStatus) bool {
	for _, dep := range deps {
		st, ok := statusByName[dep]
		if !ok {
			return false
		}
		if st.Phase != kubeswarmv1alpha1.SwarmFlowStepPhaseSucceeded &&
			st.Phase != kubeswarmv1alpha1.SwarmFlowStepPhaseSkipped {
			return false
		}
	}
	return true
}

// BuildAdjacencySet returns the set of step names that are direct predecessors of
// consumerRole in the pipeline. A step is a direct predecessor when:
//   - It is listed in consumerRole's dependsOn, OR
//   - dependsOn is empty and it is the immediately preceding step by position.
func BuildAdjacencySet(consumerRole string, pipeline []kubeswarmv1alpha1.SwarmTeamPipelineStep) map[string]struct{} {
	adjacent := make(map[string]struct{})
	for i, step := range pipeline {
		if step.Role != consumerRole {
			continue
		}
		if len(step.DependsOn) > 0 {
			for _, dep := range step.DependsOn {
				adjacent[dep] = struct{}{}
			}
		} else if i > 0 {
			adjacent[pipeline[i-1].Role] = struct{}{}
		}
		break
	}
	return adjacent
}

// ResolveContextPolicy returns the effective StepContextPolicy for a producing step
// (producerRole) as seen by a consuming step (consumerRole).
//
// Resolution order:
//  1. The producer step's own contextPolicy — always wins.
//  2. If the producer is a direct predecessor of the consumer (adjacent), strategy=full.
//  3. Otherwise, defaultContextPolicy from the pipeline level.
//  4. Fallback: nil (caller treats as strategy=full).
func ResolveContextPolicy(
	producerRole string,
	consumerRole string,
	pipeline []kubeswarmv1alpha1.SwarmTeamPipelineStep,
	defaultPolicy *kubeswarmv1alpha1.StepContextPolicy,
) *kubeswarmv1alpha1.StepContextPolicy {
	// Find the producer step's explicit per-step policy.
	for _, step := range pipeline {
		if step.Role == producerRole {
			if step.ContextPolicy != nil {
				return step.ContextPolicy
			}
			break
		}
	}

	// No per-step policy — check adjacency.
	adjacent := BuildAdjacencySet(consumerRole, pipeline)
	if _, isAdjacent := adjacent[producerRole]; isAdjacent {
		return nil // adjacent → full output (nil = strategy full)
	}

	// Non-adjacent → apply pipeline default.
	return defaultPolicy
}

// ValidateRunDAG checks an SwarmRun pipeline spec for unknown dependencies and cycles.
func ValidateRunDAG(f *kubeswarmv1alpha1.SwarmRun) error {
	return validatePipelineDAG(f.Spec.Pipeline)
}

// ValidateTeamDAG checks the SwarmTeam pipeline steps for unknown dependencies and cycles.
func ValidateTeamDAG(f *kubeswarmv1alpha1.SwarmTeam) error {
	return validatePipelineDAG(f.Spec.Pipeline)
}

// validatePipelineDAG checks a pipeline step slice for unknown dependencies and cycles.
func validatePipelineDAG(pipeline []kubeswarmv1alpha1.SwarmTeamPipelineStep) error {
	stepNames := make(map[string]struct{}, len(pipeline))
	for _, step := range pipeline {
		stepNames[step.Role] = struct{}{}
	}
	for _, step := range pipeline {
		for _, dep := range step.DependsOn {
			if _, ok := stepNames[dep]; !ok {
				return fmt.Errorf("step %q depends on unknown step %q", step.Role, dep)
			}
		}
	}
	adj := make(map[string][]string, len(pipeline))
	for _, step := range pipeline {
		adj[step.Role] = step.DependsOn
	}
	const white, gray, black = 0, 1, 2
	color := make(map[string]int, len(pipeline))
	var dfs func(name string) error
	dfs = func(name string) error {
		color[name] = gray
		for _, dep := range adj[name] {
			switch color[dep] {
			case gray:
				return fmt.Errorf("cycle detected: step %q → %q forms a cycle", name, dep)
			case white:
				if err := dfs(dep); err != nil {
					return err
				}
			}
		}
		color[name] = black
		return nil
	}
	for _, step := range pipeline {
		if color[step.Role] == white {
			if err := dfs(step.Role); err != nil {
				return err
			}
		}
	}
	return nil
}
