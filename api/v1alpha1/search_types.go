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

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// SearchAction is the JSON envelope the planner agent returns.
// Not a CRD type - used for planner output parsing only.
type SearchAction struct {
	// Action is the planner directive: "expand", "prune", or "converge".
	Action string `json:"action"`
	// ParentNode is the ID of the parent node for expand actions.
	ParentNode *int32 `json:"parentNode,omitempty"`
	// Node is the target node ID for prune or score actions.
	Node *int32 `json:"node,omitempty"`
	// Task is the task description assigned to a new child node.
	Task *string `json:"task,omitempty"`
	// ScoreMillis is the planner-assigned score (0-1000) when no evaluator is configured.
	ScoreMillis *int32 `json:"scoreMillis,omitempty"`
	// Reason is an optional explanation for prune or converge decisions.
	Reason *string `json:"reason,omitempty"`
}

// SearchNodeScore is the JSON envelope the evaluator agent returns.
// Not a CRD type - used for evaluator output parsing only.
type SearchNodeScore struct {
	// ScoreMillis is the quality score on a 0-1000 scale.
	ScoreMillis int32 `json:"scoreMillis"`
	// Reasoning explains the score assignment.
	Reasoning string `json:"reasoning"`
	// ShouldPrune hints the planner to prune this node on the next iteration.
	ShouldPrune bool `json:"shouldPrune"`
	// Metadata carries arbitrary key-value pairs for downstream consumers.
	Metadata map[string]string `json:"metadata,omitempty"`
}

// SearchNodeStatus records the state of a single search tree node.
// Stored in SwarmRun status (Phase 2).
type SearchNodeStatus struct {
	// ID is the zero-based index of this node in the tree.
	ID int32 `json:"id"`
	// ParentID is the ID of the parent node. Nil for the root node.
	ParentID *int32 `json:"parentID,omitempty"`
	// Depth is the tree depth of this node (root = 0).
	Depth int32 `json:"depth"`
	// Task is the task description assigned to this node.
	Task string `json:"task"`
	// Output is the executor's response for this node.
	Output string `json:"output,omitempty"`
	// ScoreMillis is the quality score (0-1000) assigned by the evaluator or planner.
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=1000
	ScoreMillis *int32 `json:"scoreMillis,omitempty"`
	// ScoreReasoning is the evaluator's explanation for the score.
	ScoreReasoning string `json:"scoreReasoning,omitempty"`
	// Phase tracks this node's lifecycle.
	Phase SearchNodePhase `json:"phase"`
	// TaskID is the queue task ID for the in-flight executor or evaluator call.
	// Empty when the node is not waiting for a result.
	// +optional
	TaskID string `json:"taskID,omitempty"`
	// TokenUsage records tokens consumed by the executor for this node.
	TokenUsage *TokenUsage `json:"tokenUsage,omitempty"`
}

// SearchTreeStatus captures the full state of a search tree.
// Stored in SwarmRun status (Phase 2).
type SearchTreeStatus struct {
	// Nodes is the ordered list of all tree nodes.
	// +listType=map
	// +listMapKey=id
	Nodes []SearchNodeStatus `json:"nodes,omitempty"`
	// Iterations is the number of planner invocations completed so far.
	Iterations int32 `json:"iterations"`
	// SolutionNodeID is the ID of the node selected as the final answer.
	SolutionNodeID *int32 `json:"solutionNodeID,omitempty"`
	// TerminationReason explains why the search stopped.
	TerminationReason *SearchTerminationReason `json:"terminationReason,omitempty"`
	// LastPlannerIteration is the timestamp of the most recent planner invocation.
	LastPlannerIteration *metav1.Time `json:"lastPlannerIteration,omitempty"`
}
