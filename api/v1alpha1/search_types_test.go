package v1alpha1

import (
	"encoding/json"
	"testing"
)

const (
	testPlanner   = "planner"
	testExecutor  = "executor"
	testEvaluator = "evaluator"
)

func TestSearchStrategy_EnumValues(t *testing.T) {
	tests := []struct {
		name string
		val  SearchStrategy
		want string
	}{
		{"BFS", SearchStrategyBFS, "BFS"},
		{"BeamSearch", SearchStrategyBeamSearch, "BeamSearch"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if string(tt.val) != tt.want {
				t.Errorf("SearchStrategy = %q, want %q", tt.val, tt.want)
			}
		})
	}
}

func TestSearchNodePhase_EnumValues(t *testing.T) {
	tests := []struct {
		name string
		val  SearchNodePhase
		want string
	}{
		{"Pending", SearchNodePhasePending, "Pending"},
		{"Running", SearchNodePhaseRunning, "Running"},
		{"Scored", SearchNodePhaseScored, "Scored"},
		{"Pruned", SearchNodePhasePruned, "Pruned"},
		{"EvalFailed", SearchNodePhaseEvalFailed, "EvalFailed"},
		{"Solution", SearchNodePhaseSolution, "Solution"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if string(tt.val) != tt.want {
				t.Errorf("SearchNodePhase = %q, want %q", tt.val, tt.want)
			}
		})
	}
}

func TestSearchTerminationReason_EnumValues(t *testing.T) {
	tests := []struct {
		name string
		val  SearchTerminationReason
		want string
	}{
		{"MinScoreReached", SearchTerminationMinScoreReached, "MinScoreReached"},
		{"MaxDepthReached", SearchTerminationMaxDepthReached, "MaxDepthReached"},
		{"MaxNodesReached", SearchTerminationMaxNodesReached, "MaxNodesReached"},
		{"MaxIterationsReached", SearchTerminationMaxIterationsReached, "MaxIterationsReached"},
		{"BudgetExhausted", SearchTerminationBudgetExhausted, "BudgetExhausted"},
		{"PlannerConverged", SearchTerminationPlannerConverged, "PlannerConverged"},
		{"PlannerFailure", SearchTerminationPlannerFailure, "PlannerFailure"},
		{"SearchCancelled", SearchTerminationSearchCancelled, "SearchCancelled"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if string(tt.val) != tt.want {
				t.Errorf("SearchTerminationReason = %q, want %q", tt.val, tt.want)
			}
		})
	}
}

func TestSearchAction_JSONRoundTrip(t *testing.T) {
	parentNode := int32(0)
	node := int32(1)
	task := "research topic"
	scoreMillis := int32(850)

	action := SearchAction{
		Action:      "expand",
		ParentNode:  &parentNode,
		Node:        &node,
		Task:        &task,
		ScoreMillis: &scoreMillis,
	}

	data, err := json.Marshal(action)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var got SearchAction
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if got.Action != "expand" {
		t.Errorf("Action = %q, want %q", got.Action, "expand")
	}
	if got.ParentNode == nil || *got.ParentNode != 0 {
		t.Errorf("ParentNode = %v, want 0", got.ParentNode)
	}
	if got.Node == nil || *got.Node != 1 {
		t.Errorf("Node = %v, want 1", got.Node)
	}
	if got.Task == nil || *got.Task != "research topic" {
		t.Errorf("Task = %v, want %q", got.Task, "research topic")
	}
	if got.ScoreMillis == nil || *got.ScoreMillis != 850 {
		t.Errorf("ScoreMillis = %v, want 850", got.ScoreMillis)
	}
}

func TestSearchAction_JSONRoundTrip_MinimalFields(t *testing.T) {
	action := SearchAction{
		Action: "converge",
	}

	data, err := json.Marshal(action)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var got SearchAction
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if got.Action != "converge" {
		t.Errorf("Action = %q, want %q", got.Action, "converge")
	}
	if got.ParentNode != nil {
		t.Errorf("ParentNode = %v, want nil", got.ParentNode)
	}
	if got.Node != nil {
		t.Errorf("Node = %v, want nil", got.Node)
	}
	if got.Reason != nil {
		t.Errorf("Reason = %v, want nil", got.Reason)
	}
}

func TestSearchAction_JSONRoundTrip_PruneAction(t *testing.T) {
	node := int32(3)
	reason := "low quality output"

	action := SearchAction{
		Action: "prune",
		Node:   &node,
		Reason: &reason,
	}

	data, err := json.Marshal(action)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var got SearchAction
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if got.Action != "prune" {
		t.Errorf("Action = %q, want %q", got.Action, "prune")
	}
	if got.Reason == nil || *got.Reason != "low quality output" {
		t.Errorf("Reason = %v, want %q", got.Reason, "low quality output")
	}
}

func TestSearchNodeScore_JSONRoundTrip(t *testing.T) {
	score := SearchNodeScore{
		ScoreMillis: 920,
		Reasoning:   "High quality output with good coverage",
		ShouldPrune: false,
		Metadata: map[string]string{
			"confidence": "high",
			"category":   "research",
		},
	}

	data, err := json.Marshal(score)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var got SearchNodeScore
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if got.ScoreMillis != 920 {
		t.Errorf("ScoreMillis = %d, want 920", got.ScoreMillis)
	}
	if got.Reasoning != "High quality output with good coverage" {
		t.Errorf("Reasoning = %q, want expected string", got.Reasoning)
	}
	if got.ShouldPrune {
		t.Error("ShouldPrune = true, want false")
	}
	if len(got.Metadata) != 2 {
		t.Fatalf("Metadata length = %d, want 2", len(got.Metadata))
	}
	if got.Metadata["confidence"] != "high" {
		t.Errorf("Metadata[confidence] = %q, want %q", got.Metadata["confidence"], "high")
	}
	if got.Metadata["category"] != "research" {
		t.Errorf("Metadata[category] = %q, want %q", got.Metadata["category"], "research")
	}
}

func TestSearchNodeScore_JSONRoundTrip_NilMetadata(t *testing.T) {
	score := SearchNodeScore{
		ScoreMillis: 500,
		Reasoning:   "average",
		ShouldPrune: true,
	}

	data, err := json.Marshal(score)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var got SearchNodeScore
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if got.ShouldPrune != true {
		t.Error("ShouldPrune = false, want true")
	}
	if got.Metadata != nil {
		t.Errorf("Metadata = %v, want nil", got.Metadata)
	}
}

func TestSearchNodeStatus_NilVsNonNilScoreMillis(t *testing.T) {
	t.Run("nil ScoreMillis", func(t *testing.T) {
		node := SearchNodeStatus{
			ID:    0,
			Depth: 0,
			Task:  "initial task",
			Phase: SearchNodePhasePending,
		}

		data, err := json.Marshal(node)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}

		var got SearchNodeStatus
		if err := json.Unmarshal(data, &got); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}

		if got.ScoreMillis != nil {
			t.Errorf("ScoreMillis = %v, want nil", got.ScoreMillis)
		}
		if got.Phase != SearchNodePhasePending {
			t.Errorf("Phase = %q, want %q", got.Phase, SearchNodePhasePending)
		}
	})

	t.Run("non-nil ScoreMillis", func(t *testing.T) {
		score := int32(750)
		parentID := int32(0)
		node := SearchNodeStatus{
			ID:             1,
			ParentID:       &parentID,
			Depth:          1,
			Task:           "subtask",
			Output:         "result text",
			ScoreMillis:    &score,
			ScoreReasoning: "good output",
			Phase:          SearchNodePhaseScored,
			TokenUsage: &TokenUsage{
				InputTokens:  100,
				OutputTokens: 50,
				TotalTokens:  150,
			},
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
		if *got.ScoreMillis != 750 {
			t.Errorf("ScoreMillis = %d, want 750", *got.ScoreMillis)
		}
		if got.ParentID == nil || *got.ParentID != 0 {
			t.Errorf("ParentID = %v, want 0", got.ParentID)
		}
		if got.TokenUsage == nil {
			t.Fatal("TokenUsage = nil, want non-nil")
		}
		if got.TokenUsage.TotalTokens != 150 {
			t.Errorf("TokenUsage.TotalTokens = %d, want 150", got.TokenUsage.TotalTokens)
		}
	})
}

func TestSearchTreeStatus_NilSolutionAndTermination(t *testing.T) {
	tree := SearchTreeStatus{
		Nodes: []SearchNodeStatus{
			{
				ID:    0,
				Depth: 0,
				Task:  "root task",
				Phase: SearchNodePhaseRunning,
			},
		},
		Iterations: 1,
	}

	data, err := json.Marshal(tree)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var got SearchTreeStatus
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if got.SolutionNodeID != nil {
		t.Errorf("SolutionNodeID = %v, want nil", got.SolutionNodeID)
	}
	if got.TerminationReason != nil {
		t.Errorf("TerminationReason = %v, want nil", got.TerminationReason)
	}
	if got.Iterations != 1 {
		t.Errorf("Iterations = %d, want 1", got.Iterations)
	}
	if len(got.Nodes) != 1 {
		t.Fatalf("Nodes length = %d, want 1", len(got.Nodes))
	}
}

func TestSearchTreeStatus_WithSolutionAndTermination(t *testing.T) {
	solutionID := int32(5)
	reason := SearchTerminationMinScoreReached

	tree := SearchTreeStatus{
		Nodes: []SearchNodeStatus{
			{ID: 0, Depth: 0, Task: "root", Phase: SearchNodePhaseScored},
			{ID: 5, Depth: 2, Task: "solution", Phase: SearchNodePhaseSolution},
		},
		Iterations:        10,
		SolutionNodeID:    &solutionID,
		TerminationReason: &reason,
	}

	data, err := json.Marshal(tree)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var got SearchTreeStatus
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if got.SolutionNodeID == nil || *got.SolutionNodeID != 5 {
		t.Errorf("SolutionNodeID = %v, want 5", got.SolutionNodeID)
	}
	if got.TerminationReason == nil || *got.TerminationReason != SearchTerminationMinScoreReached {
		t.Errorf("TerminationReason = %v, want MinScoreReached", got.TerminationReason)
	}
}

func TestSwarmTeamSearchSpec_RequiredFields(t *testing.T) {
	evaluator := testEvaluator
	spec := SwarmTeamSearchSpec{
		Strategy:      SearchStrategyBeamSearch,
		PlannerRole:   testPlanner,
		ExecutorRole:  testExecutor,
		EvaluatorRole: &evaluator,
		InitialPrompt: "Solve this problem",
	}

	data, err := json.Marshal(spec)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var got SwarmTeamSearchSpec
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if got.Strategy != SearchStrategyBeamSearch {
		t.Errorf("Strategy = %q, want %q", got.Strategy, SearchStrategyBeamSearch)
	}
	if got.PlannerRole != testPlanner {
		t.Errorf("PlannerRole = %q, want %q", got.PlannerRole, testPlanner)
	}
	if got.ExecutorRole != testExecutor {
		t.Errorf("ExecutorRole = %q, want %q", got.ExecutorRole, testExecutor)
	}
	if got.EvaluatorRole == nil || *got.EvaluatorRole != testEvaluator {
		t.Errorf("EvaluatorRole = %v, want %q", got.EvaluatorRole, testEvaluator)
	}
	if got.InitialPrompt != "Solve this problem" {
		t.Errorf("InitialPrompt = %q, want expected string", got.InitialPrompt)
	}
}

func TestSwarmTeamSearchSpec_BFSWithoutEvaluator(t *testing.T) {
	spec := SwarmTeamSearchSpec{
		Strategy:      SearchStrategyBFS,
		PlannerRole:   testPlanner,
		ExecutorRole:  testExecutor,
		InitialPrompt: "Explore this space",
	}

	data, err := json.Marshal(spec)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var got SwarmTeamSearchSpec
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if got.Strategy != SearchStrategyBFS {
		t.Errorf("Strategy = %q, want %q", got.Strategy, SearchStrategyBFS)
	}
	if got.EvaluatorRole != nil {
		t.Errorf("EvaluatorRole = %v, want nil", got.EvaluatorRole)
	}
}
