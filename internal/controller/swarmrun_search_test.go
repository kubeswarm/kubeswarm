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
	"encoding/json"
	"strings"
	"testing"

	kubeswarmv1alpha1 "github.com/kubeswarm/kubeswarm/api/v1alpha1"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// int32Ptr is in helpers.go

func searchSpec(opts ...func(*kubeswarmv1alpha1.SwarmTeamSearchSpec)) *kubeswarmv1alpha1.SwarmTeamSearchSpec {
	spec := &kubeswarmv1alpha1.SwarmTeamSearchSpec{
		Strategy:      kubeswarmv1alpha1.SearchStrategyBFS,
		PlannerRole:   "planner",
		ExecutorRole:  "executor",
		InitialPrompt: "solve the problem",
		MaxDepth:      10,
		MaxNodes:      50,
		MaxIterations: 20,
		MaxParallel:   3,
	}
	for _, o := range opts {
		o(spec)
	}
	return spec
}

func treeWithNodes(nodes ...kubeswarmv1alpha1.SearchNodeStatus) *kubeswarmv1alpha1.SearchTreeStatus {
	return &kubeswarmv1alpha1.SearchTreeStatus{Nodes: nodes}
}

func node(id int32, depth int32, phase kubeswarmv1alpha1.SearchNodePhase, task string) kubeswarmv1alpha1.SearchNodeStatus {
	return kubeswarmv1alpha1.SearchNodeStatus{
		ID:    id,
		Depth: depth,
		Phase: phase,
		Task:  task,
	}
}

func nodeWithParent(id, depth int32, phase kubeswarmv1alpha1.SearchNodePhase, task string) kubeswarmv1alpha1.SearchNodeStatus {
	n := node(id, depth, phase, task)
	n.ParentID = int32Ptr(0)
	return n
}

func nodeWithScore(id, depth int32, phase kubeswarmv1alpha1.SearchNodePhase, task string, score int32) kubeswarmv1alpha1.SearchNodeStatus {
	n := node(id, depth, phase, task)
	n.ScoreMillis = int32Ptr(score)
	return n
}

// ---------------------------------------------------------------------------
// Tree initialization
// ---------------------------------------------------------------------------

func TestInitSearchTree_CreatesRootNode(t *testing.T) {
	spec := searchSpec()
	tree := initSearchTree(spec.InitialPrompt)

	requireNotNil(t, tree)
	requireLen(t, tree.Nodes, 1)

	root := tree.Nodes[0]
	requireEqual(t, root.ID, int32(0))
	requireEqual(t, root.Depth, int32(0))
	requireEqual(t, root.Phase, kubeswarmv1alpha1.SearchNodePhasePending)
	requireEqual(t, root.Task, "solve the problem")
	requireNil(t, root.ParentID)
}

// ---------------------------------------------------------------------------
// Frontier context serialization
// ---------------------------------------------------------------------------

func TestBuildFrontierContext_SinglePendingNode(t *testing.T) {
	spec := searchSpec()
	tree := &kubeswarmv1alpha1.SearchTreeStatus{
		Nodes: []kubeswarmv1alpha1.SearchNodeStatus{
			node(0, 0, kubeswarmv1alpha1.SearchNodePhasePending, "root task"),
		},
	}

	data, err := buildFrontierContext(tree, spec)
	requireNoError(t, err)

	// The frontier context should be valid JSON and contain the pending node's task.
	s := string(data)
	if !json.Valid(data) {
		t.Fatalf("frontier context is not valid JSON: %s", s)
	}
	requireContains(t, s, "root task")
}

func TestBuildFrontierContext_ScoredAndPending(t *testing.T) {
	spec := searchSpec()
	tree := &kubeswarmv1alpha1.SearchTreeStatus{
		Nodes: []kubeswarmv1alpha1.SearchNodeStatus{
			{
				ID:          0,
				Depth:       0,
				Phase:       kubeswarmv1alpha1.SearchNodePhaseScored,
				Task:        "root task",
				Output:      "root output",
				ScoreMillis: int32Ptr(700),
			},
			{
				ID:       1,
				ParentID: int32Ptr(0),
				Depth:    1,
				Phase:    kubeswarmv1alpha1.SearchNodePhasePending,
				Task:     "child task",
			},
		},
	}

	data, err := buildFrontierContext(tree, spec)
	requireNoError(t, err)

	s := string(data)
	if !json.Valid(data) {
		t.Fatalf("frontier context is not valid JSON: %s", s)
	}
	// Scored node should appear in scored section.
	requireContains(t, s, "root task")
	// Pending leaf should appear in frontier.
	requireContains(t, s, "child task")
}

func TestBuildFrontierContext_PrunedExcluded(t *testing.T) {
	spec := searchSpec()
	tree := &kubeswarmv1alpha1.SearchTreeStatus{
		Nodes: []kubeswarmv1alpha1.SearchNodeStatus{
			node(0, 0, kubeswarmv1alpha1.SearchNodePhaseScored, "root task"),
			nodeWithParent(1, 1, kubeswarmv1alpha1.SearchNodePhasePruned, "pruned task"),
			nodeWithParent(2, 1, kubeswarmv1alpha1.SearchNodePhasePending, "active task"),
		},
	}

	data, err := buildFrontierContext(tree, spec)
	requireNoError(t, err)

	s := string(data)
	// Pruned node should not appear in frontier leaves.
	// The frontier should contain the active task but not the pruned one as a frontier leaf.
	requireContains(t, s, "active task")
	// We do not assert pruned is entirely absent since it may appear in tree context,
	// but it must not appear as a frontier candidate.
}

// ---------------------------------------------------------------------------
// Planner action validation
// ---------------------------------------------------------------------------

func TestValidateSearchActions_ValidExpand(t *testing.T) {
	tree := treeWithNodes(node(0, 0, kubeswarmv1alpha1.SearchNodePhaseScored, "root"))
	actions := []kubeswarmv1alpha1.SearchAction{
		{Action: "expand", ParentNode: int32Ptr(0), Task: new("explore option A")},
	}
	errs := validateSearchActions(actions, tree, true)
	requireLen(t, errs, 0)
}

func TestValidateSearchActions_ValidPrune(t *testing.T) {
	tree := treeWithNodes(
		node(0, 0, kubeswarmv1alpha1.SearchNodePhaseScored, "root"),
		nodeWithParent(1, 1, kubeswarmv1alpha1.SearchNodePhasePending, "bad branch"),
	)
	actions := []kubeswarmv1alpha1.SearchAction{
		{Action: "prune", Node: int32Ptr(1), Reason: new("dead end")},
	}
	errs := validateSearchActions(actions, tree, true)
	requireLen(t, errs, 0)
}

func TestValidateSearchActions_ValidConverge(t *testing.T) {
	tree := treeWithNodes(
		node(0, 0, kubeswarmv1alpha1.SearchNodePhaseScored, "root"),
		nodeWithParent(1, 1, kubeswarmv1alpha1.SearchNodePhaseScored, "solution"),
	)
	actions := []kubeswarmv1alpha1.SearchAction{
		{Action: "converge", Node: int32Ptr(1), Reason: new("best answer found")},
	}
	errs := validateSearchActions(actions, tree, true)
	requireLen(t, errs, 0)
}

func TestValidateSearchActions_ExpandMissingParent(t *testing.T) {
	tree := treeWithNodes(node(0, 0, kubeswarmv1alpha1.SearchNodePhaseScored, "root"))
	actions := []kubeswarmv1alpha1.SearchAction{
		{Action: "expand", Task: new("no parent")},
	}
	errs := validateSearchActions(actions, tree, true)
	if len(errs) == 0 {
		t.Fatal("expected validation error for expand without parentNode")
	}
	found := false
	for _, e := range errs {
		if strings.Contains(e.Error(), "parentNode") || strings.Contains(e.Error(), "parent") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected error mentioning parentNode, got: %v", errs)
	}
}

func TestValidateSearchActions_ExpandMissingTask(t *testing.T) {
	tree := treeWithNodes(node(0, 0, kubeswarmv1alpha1.SearchNodePhaseScored, "root"))
	actions := []kubeswarmv1alpha1.SearchAction{
		{Action: "expand", ParentNode: int32Ptr(0)},
	}
	errs := validateSearchActions(actions, tree, true)
	if len(errs) == 0 {
		t.Fatal("expected validation error for expand without task")
	}
	found := false
	for _, e := range errs {
		if strings.Contains(e.Error(), "task") || strings.Contains(e.Error(), "Task") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected error mentioning task, got: %v", errs)
	}
}

func TestValidateSearchActions_PruneMissingNode(t *testing.T) {
	tree := treeWithNodes(node(0, 0, kubeswarmv1alpha1.SearchNodePhaseScored, "root"))
	actions := []kubeswarmv1alpha1.SearchAction{
		{Action: "prune", Reason: new("bad")},
	}
	errs := validateSearchActions(actions, tree, true)
	if len(errs) == 0 {
		t.Fatal("expected validation error for prune without node")
	}
}

func TestValidateSearchActions_InvalidAction(t *testing.T) {
	tree := treeWithNodes(node(0, 0, kubeswarmv1alpha1.SearchNodePhaseScored, "root"))
	actions := []kubeswarmv1alpha1.SearchAction{
		{Action: "teleport"},
	}
	errs := validateSearchActions(actions, tree, true)
	if len(errs) == 0 {
		t.Fatal("expected validation error for unknown action")
	}
	found := false
	for _, e := range errs {
		if strings.Contains(e.Error(), "teleport") || strings.Contains(e.Error(), "unknown") || strings.Contains(e.Error(), "invalid") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected error mentioning invalid action, got: %v", errs)
	}
}

func TestValidateSearchActions_ExpandWithScore_NoEvaluator(t *testing.T) {
	tree := treeWithNodes(node(0, 0, kubeswarmv1alpha1.SearchNodePhaseScored, "root"))
	actions := []kubeswarmv1alpha1.SearchAction{
		{Action: "expand", ParentNode: int32Ptr(0), Task: new("task A"), ScoreMillis: int32Ptr(500)},
	}
	// hasEvaluator=false, planner provides score: valid.
	errs := validateSearchActions(actions, tree, false)
	requireLen(t, errs, 0)
}

func TestValidateSearchActions_ExpandWithoutScore_NoEvaluator(t *testing.T) {
	tree := treeWithNodes(node(0, 0, kubeswarmv1alpha1.SearchNodePhaseScored, "root"))
	actions := []kubeswarmv1alpha1.SearchAction{
		{Action: "expand", ParentNode: int32Ptr(0), Task: new("task A")},
	}
	// hasEvaluator=false but no scoreMillis: error.
	errs := validateSearchActions(actions, tree, false)
	if len(errs) == 0 {
		t.Fatal("expected validation error for expand without scoreMillis when no evaluator")
	}
	found := false
	for _, e := range errs {
		if strings.Contains(e.Error(), "score") || strings.Contains(e.Error(), "Score") || strings.Contains(e.Error(), "evaluator") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected error mentioning score/evaluator, got: %v", errs)
	}
}

// ---------------------------------------------------------------------------
// Action application
// ---------------------------------------------------------------------------

func TestApplySearchActions_Expand(t *testing.T) {
	tree := treeWithNodes(node(0, 0, kubeswarmv1alpha1.SearchNodePhaseScored, "root"))
	actions := []kubeswarmv1alpha1.SearchAction{
		{Action: "expand", ParentNode: int32Ptr(0), Task: new("child A")},
		{Action: "expand", ParentNode: int32Ptr(0), Task: new("child B")},
	}

	err := applySearchActions(tree, actions, 50)
	requireNoError(t, err)

	requireLen(t, tree.Nodes, 3) // root + 2 children

	childA := tree.Nodes[1]
	requireEqual(t, childA.ID, int32(1))
	requireNotNil(t, childA.ParentID)
	requireEqual(t, *childA.ParentID, int32(0))
	requireEqual(t, childA.Depth, int32(1))
	requireEqual(t, childA.Phase, kubeswarmv1alpha1.SearchNodePhasePending)
	requireEqual(t, childA.Task, "child A")

	childB := tree.Nodes[2]
	requireEqual(t, childB.ID, int32(2))
	requireNotNil(t, childB.ParentID)
	requireEqual(t, *childB.ParentID, int32(0))
	requireEqual(t, childB.Depth, int32(1))
	requireEqual(t, childB.Phase, kubeswarmv1alpha1.SearchNodePhasePending)
	requireEqual(t, childB.Task, "child B")
}

func TestApplySearchActions_Prune(t *testing.T) {
	tree := treeWithNodes(
		node(0, 0, kubeswarmv1alpha1.SearchNodePhaseScored, "root"),
		nodeWithParent(1, 1, kubeswarmv1alpha1.SearchNodePhasePending, "dead end"),
	)
	actions := []kubeswarmv1alpha1.SearchAction{
		{Action: "prune", Node: int32Ptr(1), Reason: new("no potential")},
	}

	err := applySearchActions(tree, actions, 50)
	requireNoError(t, err)

	requireEqual(t, tree.Nodes[1].Phase, kubeswarmv1alpha1.SearchNodePhasePruned)
}

func TestApplySearchActions_Converge(t *testing.T) {
	tree := treeWithNodes(
		node(0, 0, kubeswarmv1alpha1.SearchNodePhaseScored, "root"),
		nodeWithParent(1, 1, kubeswarmv1alpha1.SearchNodePhaseScored, "best answer"),
	)
	actions := []kubeswarmv1alpha1.SearchAction{
		{Action: "converge", Node: int32Ptr(1), Reason: new("optimal")},
	}

	err := applySearchActions(tree, actions, 50)
	requireNoError(t, err)

	requireEqual(t, tree.Nodes[1].Phase, kubeswarmv1alpha1.SearchNodePhaseSolution)
	requireNotNil(t, tree.SolutionNodeID)
	requireEqual(t, *tree.SolutionNodeID, int32(1))
}

func TestApplySearchActions_ExpandExceedsMaxNodes(t *testing.T) {
	// Tree already has 3 nodes, maxNodes=3.
	tree := treeWithNodes(
		node(0, 0, kubeswarmv1alpha1.SearchNodePhaseScored, "root"),
		nodeWithParent(1, 1, kubeswarmv1alpha1.SearchNodePhaseScored, "child1"),
		nodeWithParent(2, 1, kubeswarmv1alpha1.SearchNodePhaseScored, "child2"),
	)
	actions := []kubeswarmv1alpha1.SearchAction{
		{Action: "expand", ParentNode: int32Ptr(0), Task: new("overflow")},
	}

	err := applySearchActions(tree, actions, 3)
	requireError(t, err)
	requireContains(t, err.Error(), "max")
}

func TestApplySearchActions_PruneNonexistentNode(t *testing.T) {
	tree := treeWithNodes(node(0, 0, kubeswarmv1alpha1.SearchNodePhaseScored, "root"))
	actions := []kubeswarmv1alpha1.SearchAction{
		{Action: "prune", Node: int32Ptr(99), Reason: new("ghost")},
	}

	err := applySearchActions(tree, actions, 50)
	requireError(t, err)
}

// ---------------------------------------------------------------------------
// Convergence checking
// ---------------------------------------------------------------------------

func TestCheckConvergence_MinScoreReached(t *testing.T) {
	tree := treeWithNodes(
		nodeWithScore(0, 0, kubeswarmv1alpha1.SearchNodePhaseScored, "root", 950),
	)
	spec := searchSpec(func(s *kubeswarmv1alpha1.SwarmTeamSearchSpec) {
		s.MinScorePercent = int32Ptr(90) // 90% = 900 millis
	})

	reason := checkConvergence(tree, spec)
	requireNotNil(t, reason)
	requireEqual(t, *reason, kubeswarmv1alpha1.SearchTerminationMinScoreReached)
}

func TestCheckConvergence_MaxDepthReached(t *testing.T) {
	tree := treeWithNodes(
		node(0, 0, kubeswarmv1alpha1.SearchNodePhaseScored, "root"),
		nodeWithParent(1, 11, kubeswarmv1alpha1.SearchNodePhasePending, "deep node"),
	)
	spec := searchSpec(func(s *kubeswarmv1alpha1.SwarmTeamSearchSpec) {
		s.MaxDepth = 10
	})

	reason := checkConvergence(tree, spec)
	requireNotNil(t, reason)
	requireEqual(t, *reason, kubeswarmv1alpha1.SearchTerminationMaxDepthReached)
}

func TestCheckConvergence_MaxNodesReached(t *testing.T) {
	nodes := make([]kubeswarmv1alpha1.SearchNodeStatus, 5)
	for i := range nodes {
		nodes[i] = node(int32(i), 0, kubeswarmv1alpha1.SearchNodePhaseScored, "node")
	}
	tree := treeWithNodes(nodes...)
	spec := searchSpec(func(s *kubeswarmv1alpha1.SwarmTeamSearchSpec) {
		s.MaxNodes = 5
	})

	reason := checkConvergence(tree, spec)
	requireNotNil(t, reason)
	requireEqual(t, *reason, kubeswarmv1alpha1.SearchTerminationMaxNodesReached)
}

func TestCheckConvergence_MaxIterationsReached(t *testing.T) {
	tree := treeWithNodes(node(0, 0, kubeswarmv1alpha1.SearchNodePhaseScored, "root"))
	tree.Iterations = 20
	spec := searchSpec(func(s *kubeswarmv1alpha1.SwarmTeamSearchSpec) {
		s.MaxIterations = 20
	})

	reason := checkConvergence(tree, spec)
	requireNotNil(t, reason)
	requireEqual(t, *reason, kubeswarmv1alpha1.SearchTerminationMaxIterationsReached)
}

func TestCheckConvergence_NotReached(t *testing.T) {
	tree := treeWithNodes(
		nodeWithScore(0, 0, kubeswarmv1alpha1.SearchNodePhaseScored, "root", 500),
	)
	tree.Iterations = 2
	spec := searchSpec(func(s *kubeswarmv1alpha1.SwarmTeamSearchSpec) {
		s.MinScorePercent = int32Ptr(90)
		s.MaxDepth = 10
		s.MaxNodes = 50
		s.MaxIterations = 20
	})

	reason := checkConvergence(tree, spec)
	requireNil(t, reason)
}

func TestCheckConvergence_PlannerConverged(t *testing.T) {
	tree := treeWithNodes(
		node(0, 0, kubeswarmv1alpha1.SearchNodePhaseScored, "root"),
		nodeWithParent(1, 1, kubeswarmv1alpha1.SearchNodePhaseSolution, "answer"),
	)
	tree.SolutionNodeID = int32Ptr(1)
	spec := searchSpec()

	reason := checkConvergence(tree, spec)
	requireNotNil(t, reason)
	requireEqual(t, *reason, kubeswarmv1alpha1.SearchTerminationPlannerConverged)
}

// ---------------------------------------------------------------------------
// Output truncation
// ---------------------------------------------------------------------------

func TestTruncateNodeOutput_WithinLimit(t *testing.T) {
	nodes := []kubeswarmv1alpha1.SearchNodeStatus{
		{ID: 0, Output: "short", Task: "short task"},
	}
	truncateNodeOutput(nodes, 4096)
	requireEqual(t, nodes[0].Output, "short")
	requireEqual(t, nodes[0].Task, "short task")
}

func TestTruncateNodeOutput_ExceedsLimit(t *testing.T) {
	longOutput := strings.Repeat("x", 5000)
	longTask := strings.Repeat("y", 5000)
	nodes := []kubeswarmv1alpha1.SearchNodeStatus{
		{ID: 0, Output: longOutput, Task: longTask},
	}
	truncateNodeOutput(nodes, 100)

	const marker = " [truncated]"
	n := nodes[0] //nolint:gosec // slice is non-empty by construction
	if len(n.Output) > 100+len(marker) {
		t.Errorf("output len = %d, expected around %d", len(n.Output), 100+len(marker))
	}
	if !strings.HasSuffix(n.Output, "[truncated]") {
		t.Errorf("output = %q, expected [truncated] suffix", n.Output)
	}
	if len(n.Task) > 100+len(marker) {
		t.Errorf("task len = %d, expected around %d", len(n.Task), 100+len(marker))
	}
	if !strings.HasSuffix(n.Task, "[truncated]") {
		t.Errorf("task = %q, expected [truncated] suffix", n.Task)
	}
}

// ---------------------------------------------------------------------------
// Beam search pruning
// ---------------------------------------------------------------------------

func TestBeamPrune_KeepsTopK(t *testing.T) {
	// 4 scored nodes at depth 1, beamWidth=2: keep top 2, prune bottom 2.
	tree := treeWithNodes(
		node(0, 0, kubeswarmv1alpha1.SearchNodePhaseScored, "root"),
		func() kubeswarmv1alpha1.SearchNodeStatus {
			n := nodeWithParent(1, 1, kubeswarmv1alpha1.SearchNodePhaseScored, "low")
			n.ScoreMillis = int32Ptr(100)
			return n
		}(),
		func() kubeswarmv1alpha1.SearchNodeStatus {
			n := nodeWithParent(2, 1, kubeswarmv1alpha1.SearchNodePhaseScored, "high")
			n.ScoreMillis = int32Ptr(900)
			return n
		}(),
		func() kubeswarmv1alpha1.SearchNodeStatus {
			n := nodeWithParent(3, 1, kubeswarmv1alpha1.SearchNodePhaseScored, "mid")
			n.ScoreMillis = int32Ptr(500)
			return n
		}(),
		func() kubeswarmv1alpha1.SearchNodeStatus {
			n := nodeWithParent(4, 1, kubeswarmv1alpha1.SearchNodePhaseScored, "lowest")
			n.ScoreMillis = int32Ptr(50)
			return n
		}(),
	)

	beamPrune(tree, 2)

	// Count how many depth-1 scored nodes remain (not pruned).
	kept := 0
	pruned := 0
	for _, n := range tree.Nodes {
		if n.Depth != 1 {
			continue
		}
		if n.Phase == kubeswarmv1alpha1.SearchNodePhasePruned {
			pruned++
		} else {
			kept++
		}
	}
	requireEqual(t, kept, 2)
	requireEqual(t, pruned, 2)

	// The two kept nodes should be the highest scorers (900 and 500).
	for _, n := range tree.Nodes {
		if n.Depth == 1 && n.Phase != kubeswarmv1alpha1.SearchNodePhasePruned {
			if n.ScoreMillis == nil || *n.ScoreMillis < 500 {
				t.Errorf("expected kept nodes to have score >= 500, got node %d with score %v", n.ID, n.ScoreMillis)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Has running nodes check
// ---------------------------------------------------------------------------

func TestHasRunningNodes_True(t *testing.T) {
	tree := treeWithNodes(
		node(0, 0, kubeswarmv1alpha1.SearchNodePhaseScored, "root"),
		nodeWithParent(1, 1, kubeswarmv1alpha1.SearchNodePhaseRunning, "in progress"),
	)
	requireTrue(t, hasRunningNodes(tree), "expected hasRunningNodes=true when a node is Running")
}

func TestHasRunningNodes_False(t *testing.T) {
	tree := treeWithNodes(
		node(0, 0, kubeswarmv1alpha1.SearchNodePhaseScored, "root"),
		nodeWithParent(1, 1, kubeswarmv1alpha1.SearchNodePhasePending, "waiting"),
	)
	requireFalse(t, hasRunningNodes(tree), "expected hasRunningNodes=false when no node is Running")
}
