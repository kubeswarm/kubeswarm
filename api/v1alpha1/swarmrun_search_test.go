package v1alpha1

import (
	"encoding/json"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestSwarmRunSpec_SearchField_NilByDefault(t *testing.T) {
	spec := SwarmRunSpec{}
	if spec.Search != nil {
		t.Errorf("SwarmRunSpec{}.Search = %v, want nil", spec.Search)
	}
}

func TestSwarmRunSpec_SearchField_WithSearchConfig(t *testing.T) {
	evaluator := testEvaluator
	minScore := int32(80)
	spec := SwarmRunSpec{
		TeamRef: "my-team",
		Search: &SwarmTeamSearchSpec{
			Strategy:        SearchStrategyBeamSearch,
			PlannerRole:     testPlanner,
			ExecutorRole:    testExecutor,
			EvaluatorRole:   &evaluator,
			InitialPrompt:   "Solve the problem",
			MinScorePercent: &minScore,
			MaxDepth:        10,
			MaxNodes:        50,
			MaxIterations:   20,
			MaxParallel:     3,
			BeamWidth:       5,
		},
	}

	if spec.Search == nil {
		t.Fatal("Search = nil, want non-nil")
	}
	if spec.Search.Strategy != SearchStrategyBeamSearch {
		t.Errorf("Strategy = %q, want %q", spec.Search.Strategy, SearchStrategyBeamSearch)
	}
	if spec.Search.PlannerRole != testPlanner {
		t.Errorf("PlannerRole = %q, want %q", spec.Search.PlannerRole, testPlanner)
	}
	if spec.Search.ExecutorRole != testExecutor {
		t.Errorf("ExecutorRole = %q, want %q", spec.Search.ExecutorRole, testExecutor)
	}
	if spec.Search.EvaluatorRole == nil || *spec.Search.EvaluatorRole != testEvaluator {
		t.Errorf("EvaluatorRole = %v, want %q", spec.Search.EvaluatorRole, testEvaluator)
	}
	if spec.Search.InitialPrompt != "Solve the problem" {
		t.Errorf("InitialPrompt = %q, want expected string", spec.Search.InitialPrompt)
	}
	if spec.Search.MinScorePercent == nil || *spec.Search.MinScorePercent != 80 {
		t.Errorf("MinScorePercent = %v, want 80", spec.Search.MinScorePercent)
	}
	if spec.Search.MaxDepth != 10 {
		t.Errorf("MaxDepth = %d, want 10", spec.Search.MaxDepth)
	}
	if spec.Search.MaxNodes != 50 {
		t.Errorf("MaxNodes = %d, want 50", spec.Search.MaxNodes)
	}
	if spec.Search.MaxIterations != 20 {
		t.Errorf("MaxIterations = %d, want 20", spec.Search.MaxIterations)
	}
	if spec.Search.MaxParallel != 3 {
		t.Errorf("MaxParallel = %d, want 3", spec.Search.MaxParallel)
	}
	if spec.Search.BeamWidth != 5 {
		t.Errorf("BeamWidth = %d, want 5", spec.Search.BeamWidth)
	}
}

func TestSwarmRunStatus_SearchTree_NilByDefault(t *testing.T) {
	status := SwarmRunStatus{}
	if status.SearchTree != nil {
		t.Errorf("SwarmRunStatus{}.SearchTree = %v, want nil", status.SearchTree)
	}
}

func TestSwarmRunStatus_SearchTree_WithNodes(t *testing.T) {
	parentID := int32(0)
	score0 := int32(600)
	score1 := int32(850)

	tree := &SearchTreeStatus{
		Nodes: []SearchNodeStatus{
			{
				ID:          0,
				Depth:       0,
				Task:        "root task",
				Output:      "root output",
				ScoreMillis: &score0,
				Phase:       SearchNodePhaseScored,
			},
			{
				ID:             1,
				ParentID:       &parentID,
				Depth:          1,
				Task:           "child task",
				Output:         "child output",
				ScoreMillis:    &score1,
				ScoreReasoning: "excellent coverage",
				Phase:          SearchNodePhaseScored,
			},
		},
		Iterations: 3,
	}

	status := SwarmRunStatus{
		Phase:      SwarmRunPhaseRunning,
		SearchTree: tree,
	}

	data, err := json.Marshal(status)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var got SwarmRunStatus
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if got.SearchTree == nil {
		t.Fatal("SearchTree = nil, want non-nil")
	}
	if len(got.SearchTree.Nodes) != 2 {
		t.Fatalf("Nodes length = %d, want 2", len(got.SearchTree.Nodes))
	}
	if got.SearchTree.Nodes[0].ID != 0 {
		t.Errorf("Nodes[0].ID = %d, want 0", got.SearchTree.Nodes[0].ID)
	}
	if got.SearchTree.Nodes[1].ID != 1 {
		t.Errorf("Nodes[1].ID = %d, want 1", got.SearchTree.Nodes[1].ID)
	}
	if got.SearchTree.Nodes[1].ParentID == nil || *got.SearchTree.Nodes[1].ParentID != 0 {
		t.Errorf("Nodes[1].ParentID = %v, want 0", got.SearchTree.Nodes[1].ParentID)
	}
	if got.SearchTree.Iterations != 3 {
		t.Errorf("Iterations = %d, want 3", got.SearchTree.Iterations)
	}
}

func TestSwarmRunStatus_SearchTree_EmptyNodes(t *testing.T) {
	tree := &SearchTreeStatus{
		Nodes:      []SearchNodeStatus{},
		Iterations: 0,
	}

	status := SwarmRunStatus{
		Phase:      SwarmRunPhasePending,
		SearchTree: tree,
	}

	data, err := json.Marshal(status)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var got SwarmRunStatus
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if got.SearchTree == nil {
		t.Fatal("SearchTree = nil, want non-nil")
	}
	if got.SearchTree.Nodes == nil {
		// Empty slice may unmarshal to nil depending on JSON; both are acceptable.
		// The key invariant is that length is 0.
		t.Log("Nodes unmarshalled to nil (acceptable for empty JSON array)")
	}
	if len(got.SearchTree.Nodes) != 0 {
		t.Errorf("Nodes length = %d, want 0", len(got.SearchTree.Nodes))
	}
	if got.SearchTree.Iterations != 0 {
		t.Errorf("Iterations = %d, want 0", got.SearchTree.Iterations)
	}
}

func TestSwarmRunStatus_SearchTree_WithSolution(t *testing.T) {
	solutionID := int32(4)
	reason := SearchTerminationMinScoreReached

	tree := &SearchTreeStatus{
		Nodes: []SearchNodeStatus{
			{ID: 0, Depth: 0, Task: "root", Phase: SearchNodePhaseScored},
			{ID: 4, Depth: 2, Task: "winning node", Phase: SearchNodePhaseSolution},
		},
		Iterations:        8,
		SolutionNodeID:    &solutionID,
		TerminationReason: &reason,
	}

	status := SwarmRunStatus{
		Phase:      SwarmRunPhaseSucceeded,
		SearchTree: tree,
	}

	if status.SearchTree.SolutionNodeID == nil || *status.SearchTree.SolutionNodeID != 4 {
		t.Errorf("SolutionNodeID = %v, want 4", status.SearchTree.SolutionNodeID)
	}
	if status.SearchTree.TerminationReason == nil || *status.SearchTree.TerminationReason != SearchTerminationMinScoreReached {
		t.Errorf("TerminationReason = %v, want MinScoreReached", status.SearchTree.TerminationReason)
	}
}

func TestSwarmRunSpec_SearchSnapshot_ImmutableCopy(t *testing.T) {
	evaluator := testEvaluator
	minScore := int32(70)
	original := SwarmTeamSearchSpec{
		Strategy:        SearchStrategyBFS,
		PlannerRole:     testPlanner,
		ExecutorRole:    testExecutor,
		EvaluatorRole:   &evaluator,
		InitialPrompt:   "original prompt",
		MinScorePercent: &minScore,
		MaxDepth:        10,
		MaxNodes:        50,
		MaxIterations:   20,
		MaxParallel:     3,
	}

	// Snapshot via DeepCopy into SwarmRunSpec.
	snapshot := original.DeepCopy()
	spec := SwarmRunSpec{
		TeamRef: "my-team",
		Search:  snapshot,
	}

	// Mutate the original after snapshotting.
	original.PlannerRole = "mutated-planner"
	original.InitialPrompt = "mutated prompt"
	original.MaxDepth = 99
	newEval := "mutated-evaluator"
	original.EvaluatorRole = &newEval
	newMinScore := int32(99)
	original.MinScorePercent = &newMinScore

	// Verify snapshot is unchanged.
	if spec.Search.PlannerRole != testPlanner {
		t.Errorf("snapshot PlannerRole = %q, want %q", spec.Search.PlannerRole, testPlanner)
	}
	if spec.Search.InitialPrompt != "original prompt" {
		t.Errorf("snapshot InitialPrompt = %q, want %q", spec.Search.InitialPrompt, "original prompt")
	}
	if spec.Search.MaxDepth != 10 {
		t.Errorf("snapshot MaxDepth = %d, want 10", spec.Search.MaxDepth)
	}
	if spec.Search.EvaluatorRole == nil || *spec.Search.EvaluatorRole != testEvaluator {
		t.Errorf("snapshot EvaluatorRole = %v, want %q", spec.Search.EvaluatorRole, testEvaluator)
	}
	if spec.Search.MinScorePercent == nil || *spec.Search.MinScorePercent != 70 {
		t.Errorf("snapshot MinScorePercent = %v, want 70", spec.Search.MinScorePercent)
	}
}

func TestSearchNodeStatus_ScoreMillis_Range(t *testing.T) {
	tests := []struct {
		name  string
		score int32
	}{
		{"minimum boundary", 0},
		{"midpoint", 500},
		{"maximum boundary", 1000},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			score := tt.score
			node := SearchNodeStatus{
				ID:          0,
				Depth:       0,
				Task:        "test",
				ScoreMillis: &score,
				Phase:       SearchNodePhaseScored,
			}

			data, err := json.Marshal(node)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}

			var got SearchNodeStatus
			if err := json.Unmarshal(data, &got); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}

			if got.ScoreMillis == nil {
				t.Fatal("ScoreMillis = nil, want non-nil")
			}
			if *got.ScoreMillis != tt.score {
				t.Errorf("ScoreMillis = %d, want %d", *got.ScoreMillis, tt.score)
			}
		})
	}
}

func TestSwarmRunStatus_SearchTree_JSONRoundTrip(t *testing.T) {
	parentID := int32(0)
	score := int32(920)
	solutionID := int32(1)
	reason := SearchTerminationPlannerConverged
	now := metav1.NewTime(time.Date(2026, 5, 19, 12, 0, 0, 0, time.UTC))

	tree := &SearchTreeStatus{
		Nodes: []SearchNodeStatus{
			{
				ID:    0,
				Depth: 0,
				Task:  "root task",
				Phase: SearchNodePhaseScored,
				TokenUsage: &TokenUsage{
					InputTokens:  200,
					OutputTokens: 100,
					TotalTokens:  300,
				},
			},
			{
				ID:             1,
				ParentID:       &parentID,
				Depth:          1,
				Task:           "solution task",
				Output:         "final answer",
				ScoreMillis:    &score,
				ScoreReasoning: "comprehensive and accurate",
				Phase:          SearchNodePhaseSolution,
				TokenUsage: &TokenUsage{
					InputTokens:  500,
					OutputTokens: 250,
					TotalTokens:  750,
				},
			},
		},
		Iterations:           5,
		SolutionNodeID:       &solutionID,
		TerminationReason:    &reason,
		LastPlannerIteration: &now,
	}

	status := SwarmRunStatus{
		Phase:      SwarmRunPhaseSucceeded,
		SearchTree: tree,
	}

	data, err := json.Marshal(status)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var got SwarmRunStatus
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if got.SearchTree == nil {
		t.Fatal("SearchTree = nil after round-trip")
	}
	if len(got.SearchTree.Nodes) != 2 {
		t.Fatalf("Nodes length = %d, want 2", len(got.SearchTree.Nodes))
	}
	if got.SearchTree.Iterations != 5 {
		t.Errorf("Iterations = %d, want 5", got.SearchTree.Iterations)
	}
	if got.SearchTree.SolutionNodeID == nil || *got.SearchTree.SolutionNodeID != 1 {
		t.Errorf("SolutionNodeID = %v, want 1", got.SearchTree.SolutionNodeID)
	}
	if got.SearchTree.TerminationReason == nil || *got.SearchTree.TerminationReason != SearchTerminationPlannerConverged {
		t.Errorf("TerminationReason = %v, want PlannerConverged", got.SearchTree.TerminationReason)
	}
	if got.SearchTree.LastPlannerIteration == nil {
		t.Fatal("LastPlannerIteration = nil, want non-nil")
	}
	if !got.SearchTree.LastPlannerIteration.Time.Equal(now.Time) {
		t.Errorf("LastPlannerIteration = %v, want %v", got.SearchTree.LastPlannerIteration.Time, now.Time)
	}

	// Verify node details survived round-trip.
	node1 := got.SearchTree.Nodes[1]
	if node1.Output != "final answer" {
		t.Errorf("Nodes[1].Output = %q, want %q", node1.Output, "final answer")
	}
	if node1.ScoreMillis == nil || *node1.ScoreMillis != 920 {
		t.Errorf("Nodes[1].ScoreMillis = %v, want 920", node1.ScoreMillis)
	}
	if node1.TokenUsage == nil || node1.TokenUsage.TotalTokens != 750 {
		t.Errorf("Nodes[1].TokenUsage.TotalTokens = %v, want 750", node1.TokenUsage)
	}
}

func TestSwarmRunSpec_Search_JSONRoundTrip(t *testing.T) {
	evaluator := testEvaluator
	minScore := int32(85)
	maxOutput := int32(8192)
	plannerTimeout := int32(60)

	spec := SwarmRunSpec{
		TeamRef:        "search-team",
		TeamGeneration: 3,
		Search: &SwarmTeamSearchSpec{
			Strategy:              SearchStrategyBeamSearch,
			PlannerRole:           testPlanner,
			ExecutorRole:          testExecutor,
			EvaluatorRole:         &evaluator,
			InitialPrompt:         "Find the best solution for {{ .input.problem }}",
			MinScorePercent:       &minScore,
			MaxDepth:              8,
			MaxNodes:              100,
			MaxOutputBytes:        &maxOutput,
			MaxIterations:         15,
			MaxParallel:           4,
			BeamWidth:             3,
			MaxPlannerRetries:     2,
			MaxEvaluatorRetries:   2,
			StagnationThreshold:   5,
			PlannerTimeoutSeconds: &plannerTimeout,
		},
	}

	data, err := json.Marshal(spec)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var got SwarmRunSpec
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if got.TeamRef != "search-team" {
		t.Errorf("TeamRef = %q, want %q", got.TeamRef, "search-team")
	}
	if got.TeamGeneration != 3 {
		t.Errorf("TeamGeneration = %d, want 3", got.TeamGeneration)
	}
	if got.Search == nil {
		t.Fatal("Search = nil after round-trip")
	}
	if got.Search.Strategy != SearchStrategyBeamSearch {
		t.Errorf("Strategy = %q, want %q", got.Search.Strategy, SearchStrategyBeamSearch)
	}
	if got.Search.PlannerRole != testPlanner {
		t.Errorf("PlannerRole = %q, want %q", got.Search.PlannerRole, testPlanner)
	}
	if got.Search.ExecutorRole != testExecutor {
		t.Errorf("ExecutorRole = %q, want %q", got.Search.ExecutorRole, testExecutor)
	}
	if got.Search.EvaluatorRole == nil || *got.Search.EvaluatorRole != testEvaluator {
		t.Errorf("EvaluatorRole = %v, want %q", got.Search.EvaluatorRole, testEvaluator)
	}
	if got.Search.InitialPrompt != "Find the best solution for {{ .input.problem }}" {
		t.Errorf("InitialPrompt = %q, want expected template string", got.Search.InitialPrompt)
	}
	if got.Search.MinScorePercent == nil || *got.Search.MinScorePercent != 85 {
		t.Errorf("MinScorePercent = %v, want 85", got.Search.MinScorePercent)
	}
	if got.Search.MaxDepth != 8 {
		t.Errorf("MaxDepth = %d, want 8", got.Search.MaxDepth)
	}
	if got.Search.MaxNodes != 100 {
		t.Errorf("MaxNodes = %d, want 100", got.Search.MaxNodes)
	}
	if got.Search.MaxOutputBytes == nil || *got.Search.MaxOutputBytes != 8192 {
		t.Errorf("MaxOutputBytes = %v, want 8192", got.Search.MaxOutputBytes)
	}
	if got.Search.MaxIterations != 15 {
		t.Errorf("MaxIterations = %d, want 15", got.Search.MaxIterations)
	}
	if got.Search.MaxParallel != 4 {
		t.Errorf("MaxParallel = %d, want 4", got.Search.MaxParallel)
	}
	if got.Search.BeamWidth != 3 {
		t.Errorf("BeamWidth = %d, want 3", got.Search.BeamWidth)
	}
	if got.Search.MaxPlannerRetries != 2 {
		t.Errorf("MaxPlannerRetries = %d, want 2", got.Search.MaxPlannerRetries)
	}
	if got.Search.MaxEvaluatorRetries != 2 {
		t.Errorf("MaxEvaluatorRetries = %d, want 2", got.Search.MaxEvaluatorRetries)
	}
	if got.Search.StagnationThreshold != 5 {
		t.Errorf("StagnationThreshold = %d, want 5", got.Search.StagnationThreshold)
	}
	if got.Search.PlannerTimeoutSeconds == nil || *got.Search.PlannerTimeoutSeconds != 60 {
		t.Errorf("PlannerTimeoutSeconds = %v, want 60", got.Search.PlannerTimeoutSeconds)
	}
}
