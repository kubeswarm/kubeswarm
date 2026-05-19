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

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"

	kubeswarmv1alpha1 "github.com/kubeswarm/kubeswarm/api/v1alpha1"
	"github.com/kubeswarm/kubeswarm/pkg/audit"
	"github.com/kubeswarm/kubeswarm/pkg/flow"
)

// maxNodesCap is the absolute upper bound for tree nodes regardless of user config.
// The CRD CEL rule enforces maxNodes * maxOutputBytes <= 1MB to stay within etcd
// value size limits, so the effective cap depends on the configured maxOutputBytes.
const maxNodesCap int32 = 200

// defaultBeamWidth is used when strategy is BeamSearch and beamWidth is unset.
const defaultBeamWidth int32 = 3

// Planner action types.
const (
	actionExpand   = "expand"
	actionPrune    = "prune"
	actionConverge = "converge"
)

// initSearchTree creates the initial tree with a root node.
// The rootPrompt should be the resolved initial prompt (template already applied).
func initSearchTree(rootPrompt string) *kubeswarmv1alpha1.SearchTreeStatus {
	return &kubeswarmv1alpha1.SearchTreeStatus{
		Nodes: []kubeswarmv1alpha1.SearchNodeStatus{
			{
				ID:    0,
				Depth: 0,
				Task:  rootPrompt,
				Phase: kubeswarmv1alpha1.SearchNodePhasePending,
			},
		},
	}
}

// frontierContextNode is a compact node representation for the planner.
type frontierContextNode struct {
	ID          int32  `json:"id"`
	ParentID    *int32 `json:"parentID,omitempty"`
	Depth       int32  `json:"depth,omitempty"`
	Task        string `json:"task"`
	ScoreMillis *int32 `json:"scoreMillis,omitempty"`
	Phase       string `json:"phase,omitempty"`
}

// frontierEntry pairs a frontier node with the path from root.
type frontierEntry struct {
	Node         frontierContextNode   `json:"node"`
	PathFromRoot []frontierContextNode `json:"pathFromRoot"`
}

// frontierContext is the JSON structure sent to the planner.
type frontierContext struct {
	Iteration       int32                 `json:"iteration"`
	Strategy        string                `json:"strategy"`
	BestScoreMillis *int32                `json:"bestScoreMillis,omitempty"`
	Scored          []frontierContextNode `json:"scored,omitempty"`
	Frontier        []frontierEntry       `json:"frontier,omitempty"`
	EvalFailed      []frontierContextNode `json:"evalFailed,omitempty"`
}

// buildFrontierContext serializes the tree state for the planner.
// Frontier policy: unscored leaf nodes + root-to-leaf paths. Scored branches summarized.
func buildFrontierContext(tree *kubeswarmv1alpha1.SearchTreeStatus, search *kubeswarmv1alpha1.SwarmTeamSearchSpec) ([]byte, error) {
	nodeByID := make(map[int32]*kubeswarmv1alpha1.SearchNodeStatus, len(tree.Nodes))
	for i := range tree.Nodes {
		nodeByID[tree.Nodes[i].ID] = &tree.Nodes[i]
	}

	fc := frontierContext{
		Iteration: tree.Iterations,
		Strategy:  string(search.Strategy),
	}

	// Find best score across all scored nodes.
	for i := range tree.Nodes {
		n := &tree.Nodes[i]
		if n.ScoreMillis != nil {
			if fc.BestScoreMillis == nil || *n.ScoreMillis > *fc.BestScoreMillis {
				score := *n.ScoreMillis
				fc.BestScoreMillis = &score
			}
		}
	}

	// Classify nodes.
	for i := range tree.Nodes {
		n := &tree.Nodes[i]
		switch n.Phase {
		case kubeswarmv1alpha1.SearchNodePhaseScored:
			fc.Scored = append(fc.Scored, toContextNode(n))
		case kubeswarmv1alpha1.SearchNodePhaseEvalFailed:
			fc.EvalFailed = append(fc.EvalFailed, toContextNode(n))
		case kubeswarmv1alpha1.SearchNodePhasePending:
			path := buildPathFromRoot(n, nodeByID)
			fc.Frontier = append(fc.Frontier, frontierEntry{
				Node:         toContextNode(n),
				PathFromRoot: path,
			})
		}
	}

	return json.Marshal(fc)
}

// toContextNode converts a SearchNodeStatus to a compact planner representation.
func toContextNode(n *kubeswarmv1alpha1.SearchNodeStatus) frontierContextNode {
	return frontierContextNode{
		ID:          n.ID,
		ParentID:    n.ParentID,
		Depth:       n.Depth,
		Task:        n.Task,
		ScoreMillis: n.ScoreMillis,
		Phase:       string(n.Phase),
	}
}

// buildPathFromRoot returns the path from the root node to the given node's parent.
func buildPathFromRoot(n *kubeswarmv1alpha1.SearchNodeStatus, nodeByID map[int32]*kubeswarmv1alpha1.SearchNodeStatus) []frontierContextNode {
	var path []frontierContextNode
	current := n.ParentID
	for current != nil {
		parent, ok := nodeByID[*current]
		if !ok {
			break
		}
		path = append(path, toContextNode(parent))
		current = parent.ParentID
	}
	// Reverse so root is first.
	for i, j := 0, len(path)-1; i < j; i, j = i+1, j-1 {
		path[i], path[j] = path[j], path[i]
	}
	return path
}

// validateSearchActions validates planner output against the schema.
// hasEvaluator=false means expand/converge MUST include scoreMillis.
func validateSearchActions(actions []kubeswarmv1alpha1.SearchAction, tree *kubeswarmv1alpha1.SearchTreeStatus, hasEvaluator bool) []error {
	nodeByID := make(map[int32]bool, len(tree.Nodes))
	for _, n := range tree.Nodes {
		nodeByID[n.ID] = true
	}

	var errs []error
	for i, a := range actions {
		switch a.Action {
		case actionExpand:
			if a.ParentNode == nil {
				errs = append(errs, fmt.Errorf("action[%d]: expand requires parentNode", i))
			} else if !nodeByID[*a.ParentNode] {
				errs = append(errs, fmt.Errorf("action[%d]: expand parentNode %d not found", i, *a.ParentNode))
			}
			if a.Task == nil || *a.Task == "" {
				errs = append(errs, fmt.Errorf("action[%d]: expand requires non-empty task", i))
			}
			if !hasEvaluator && a.ScoreMillis == nil {
				errs = append(errs, fmt.Errorf("action[%d]: expand requires scoreMillis when no evaluator is configured", i))
			}
		case actionPrune:
			if a.Node == nil {
				errs = append(errs, fmt.Errorf("action[%d]: prune requires node", i))
			} else if !nodeByID[*a.Node] {
				errs = append(errs, fmt.Errorf("action[%d]: prune node %d not found", i, *a.Node))
			}
		case actionConverge:
			if a.Node == nil {
				errs = append(errs, fmt.Errorf("action[%d]: converge requires node", i))
			} else if !nodeByID[*a.Node] {
				errs = append(errs, fmt.Errorf("action[%d]: converge node %d not found", i, *a.Node))
			}
			if !hasEvaluator && a.ScoreMillis == nil {
				errs = append(errs, fmt.Errorf("action[%d]: converge requires scoreMillis when no evaluator is configured", i))
			}
		default:
			errs = append(errs, fmt.Errorf("action[%d]: unknown action %q", i, a.Action))
		}
	}
	return errs
}

// applySearchActions applies expand/prune/converge actions to the tree atomically.
// Returns error if any action is invalid (e.g. exceeds maxNodes).
func applySearchActions(tree *kubeswarmv1alpha1.SearchTreeStatus, actions []kubeswarmv1alpha1.SearchAction, maxNodes int32) error {
	if maxNodes <= 0 || maxNodes > maxNodesCap {
		maxNodes = maxNodesCap
	}

	nodeByID := make(map[int32]*kubeswarmv1alpha1.SearchNodeStatus, len(tree.Nodes))
	for i := range tree.Nodes {
		nodeByID[tree.Nodes[i].ID] = &tree.Nodes[i]
	}

	nextID := int32(len(tree.Nodes))

	for _, a := range actions {
		switch a.Action {
		case actionExpand:
			if int32(len(tree.Nodes)) >= maxNodes {
				return fmt.Errorf("cannot expand: tree has %d nodes (max %d)", len(tree.Nodes), maxNodes)
			}
			parent, ok := nodeByID[*a.ParentNode]
			if !ok {
				return fmt.Errorf("expand: parent node %d not found", *a.ParentNode)
			}
			newNode := kubeswarmv1alpha1.SearchNodeStatus{
				ID:          nextID,
				ParentID:    a.ParentNode,
				Depth:       parent.Depth + 1,
				Task:        *a.Task,
				ScoreMillis: a.ScoreMillis,
				Phase:       kubeswarmv1alpha1.SearchNodePhasePending,
			}
			tree.Nodes = append(tree.Nodes, newNode)
			nodeByID[nextID] = &tree.Nodes[len(tree.Nodes)-1]
			nextID++

		case actionPrune:
			node, ok := nodeByID[*a.Node]
			if !ok {
				return fmt.Errorf("prune: node %d not found", *a.Node)
			}
			node.Phase = kubeswarmv1alpha1.SearchNodePhasePruned

		case actionConverge:
			node, ok := nodeByID[*a.Node]
			if !ok {
				return fmt.Errorf("converge: node %d not found", *a.Node)
			}
			node.Phase = kubeswarmv1alpha1.SearchNodePhaseSolution
			if a.ScoreMillis != nil {
				node.ScoreMillis = a.ScoreMillis
			}
			tree.SolutionNodeID = a.Node
		}
	}
	return nil
}

// checkConvergence checks all termination criteria.
// Returns the termination reason, or nil if search should continue.
func checkConvergence(tree *kubeswarmv1alpha1.SearchTreeStatus, search *kubeswarmv1alpha1.SwarmTeamSearchSpec) *kubeswarmv1alpha1.SearchTerminationReason {
	// Planner-directed convergence: a solution node was selected.
	if tree.SolutionNodeID != nil {
		reason := kubeswarmv1alpha1.SearchTerminationPlannerConverged
		return &reason
	}

	// MinScorePercent threshold.
	if search.MinScorePercent != nil {
		threshold := *search.MinScorePercent * 10 // percent to millis
		for _, n := range tree.Nodes {
			if n.ScoreMillis != nil && *n.ScoreMillis >= threshold {
				reason := kubeswarmv1alpha1.SearchTerminationMinScoreReached
				return &reason
			}
		}
	}

	// MaxDepth.
	if search.MaxDepth > 0 {
		for _, n := range tree.Nodes {
			if n.Depth >= search.MaxDepth && n.Phase != kubeswarmv1alpha1.SearchNodePhasePruned {
				reason := kubeswarmv1alpha1.SearchTerminationMaxDepthReached
				return &reason
			}
		}
	}

	// MaxNodes.
	maxNodes := search.MaxNodes
	if maxNodes <= 0 {
		maxNodes = 50
	}
	if int32(len(tree.Nodes)) >= maxNodes {
		reason := kubeswarmv1alpha1.SearchTerminationMaxNodesReached
		return &reason
	}

	// MaxIterations.
	if search.MaxIterations > 0 && tree.Iterations >= search.MaxIterations {
		reason := kubeswarmv1alpha1.SearchTerminationMaxIterationsReached
		return &reason
	}

	return nil
}

// truncateNodeOutput truncates output and task fields exceeding maxBytes.
func truncateNodeOutput(nodes []kubeswarmv1alpha1.SearchNodeStatus, maxBytes int32) {
	if maxBytes <= 0 {
		return
	}
	limit := int(maxBytes)
	for i := range nodes {
		if len(nodes[i].Output) > limit {
			nodes[i].Output = nodes[i].Output[:limit] + " [truncated]"
		}
		if len(nodes[i].Task) > limit {
			nodes[i].Task = nodes[i].Task[:limit] + " [truncated]"
		}
	}
}

// beamPrune keeps only top beamWidth scored nodes per depth, prunes rest.
func beamPrune(tree *kubeswarmv1alpha1.SearchTreeStatus, beamWidth int32) {
	if beamWidth <= 0 {
		return
	}

	// Group scored nodes by depth.
	byDepth := make(map[int32][]*kubeswarmv1alpha1.SearchNodeStatus)
	for i := range tree.Nodes {
		n := &tree.Nodes[i]
		if n.Phase == kubeswarmv1alpha1.SearchNodePhaseScored && n.ScoreMillis != nil {
			byDepth[n.Depth] = append(byDepth[n.Depth], n)
		}
	}

	// For each depth, sort by score descending and prune excess.
	for _, nodes := range byDepth {
		if int32(len(nodes)) <= beamWidth {
			continue
		}
		sort.Slice(nodes, func(i, j int) bool {
			return *nodes[i].ScoreMillis > *nodes[j].ScoreMillis
		})
		for _, n := range nodes[beamWidth:] {
			n.Phase = kubeswarmv1alpha1.SearchNodePhasePruned
		}
	}
}

// hasRunningNodes returns true if any node is in Running phase.
func hasRunningNodes(tree *kubeswarmv1alpha1.SearchTreeStatus) bool {
	for _, n := range tree.Nodes {
		if n.Phase == kubeswarmv1alpha1.SearchNodePhaseRunning {
			return true
		}
	}
	return false
}

// reconcileSearchRun handles search-mode team runs.
func (r *SwarmRunReconciler) reconcileSearchRun(
	ctx context.Context,
	run *kubeswarmv1alpha1.SwarmRun,
	logger logr.Logger,
) (ctrl.Result, error) {
	search := run.Spec.Search

	// 1. Initialize tree if empty. Resolve the initial prompt template against run input.
	if run.Status.SearchTree == nil {
		rootPrompt := search.InitialPrompt
		if len(run.Spec.Input) > 0 {
			data := map[string]any{"input": run.Spec.Input}
			if resolved, err := flow.ResolveTemplate(rootPrompt, data); err == nil {
				rootPrompt = resolved
			}
		}
		run.Status.SearchTree = initSearchTree(rootPrompt)
	}
	tree := run.Status.SearchTree

	// 2. Set phase to Running if Pending.
	if run.Status.Phase == "" || run.Status.Phase == kubeswarmv1alpha1.SwarmRunPhasePending {
		now := metav1.Now()
		run.Status.Phase = kubeswarmv1alpha1.SwarmRunPhaseRunning
		run.Status.StartTime = &now
		if r.Recorder != nil {
			r.Recorder.Eventf(run, nil, corev1.EventTypeNormal, "RunStarted", "Reconcile",
				"SwarmRun %q started in search mode", run.Name)
		}
	}

	// Enforce run-level timeout.
	if flow.EnforceRunTimeout(run, metav1.Now()) {
		logger.Info("search run timed out", "run", run.Name)
		flow.SetRunCondition(run, metav1.ConditionFalse, "Timeout",
			fmt.Sprintf("run exceeded timeout of %ds", run.Spec.TimeoutSeconds))
		if r.Recorder != nil {
			r.Recorder.Eventf(run, nil, corev1.EventTypeWarning, "RunTimedOut", "Reconcile",
				"SwarmRun %q timed out after %ds", run.Name, run.Spec.TimeoutSeconds)
		}
		r.cancelSearchTasks(ctx, run, tree, logger)
		if r.NotifyDispatcher != nil {
			r.NotifyDispatcher.DispatchRun(ctx, run)
		}
		run.Status.ObservedGeneration = run.Generation
		return ctrl.Result{}, r.Status().Update(ctx, run)
	}

	roleAgentMap, _ := buildRoleMaps(run)

	// 3. Collect executor results (poll queue for Running nodes).
	if err := r.collectSearchExecutorResults(ctx, run, tree, search.ExecutorRole, roleAgentMap, logger); err != nil {
		logger.Error(err, "collecting search executor results")
	}

	// 4. Collect evaluator results (poll queue for nodes awaiting scoring).
	if search.EvaluatorRole != nil {
		if err := r.collectSearchEvaluatorResults(ctx, run, tree, *search.EvaluatorRole, roleAgentMap, logger); err != nil {
			logger.Error(err, "collecting search evaluator results")
		}
	}

	// 5. Apply beam pruning if strategy=BeamSearch.
	if search.Strategy == kubeswarmv1alpha1.SearchStrategyBeamSearch {
		bw := search.BeamWidth
		if bw <= 0 {
			bw = defaultBeamWidth
		}
		beamPrune(tree, bw)
	}

	// 6. Check convergence.
	if reason := checkConvergence(tree, search); reason != nil {
		tree.TerminationReason = reason
		r.finalizeSearchRun(run, tree)
		if r.NotifyDispatcher != nil {
			r.NotifyDispatcher.DispatchRun(ctx, run)
		}
		run.Status.ObservedGeneration = run.Generation
		return ctrl.Result{}, r.Status().Update(ctx, run)
	}

	// 7. Submit any Pending nodes to executor queue before invoking the planner.
	// On the first iteration this dispatches the root node to the executor.
	if err := r.submitSearchNodes(ctx, run, tree, search, roleAgentMap, logger); err != nil {
		logger.Error(err, "submitting search nodes")
		return ctrl.Result{}, fmt.Errorf("submitting search nodes: %w", err)
	}

	// 8. If executors are running, wait (requeue). Don't invoke the planner
	// until all executors have completed so the planner sees full results.
	if hasRunningNodes(tree) {
		run.Status.ObservedGeneration = run.Generation
		if err := r.Status().Update(ctx, run); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: runRequeueAfter}, nil
	}

	// 9. All executors done. Dispatch planner to get next actions.
	actions, err := r.invokePlanner(ctx, run, tree, search, roleAgentMap, logger)
	if err != nil {
		logger.Error(err, "planner invocation failed")
		reason := kubeswarmv1alpha1.SearchTerminationPlannerFailure
		tree.TerminationReason = &reason
		r.finalizeSearchRun(run, tree)
		run.Status.ObservedGeneration = run.Generation
		return ctrl.Result{}, r.Status().Update(ctx, run)
	}

	// 10. Validate and apply actions.
	hasEvaluator := search.EvaluatorRole != nil
	if validationErrs := validateSearchActions(actions, tree, hasEvaluator); len(validationErrs) > 0 {
		logger.Error(validationErrs[0], "planner returned invalid actions", "count", len(validationErrs))
		if r.Recorder != nil {
			r.Recorder.Eventf(run, nil, corev1.EventTypeWarning, "PlannerValidationFailed", "Reconcile",
				"SwarmRun %q planner output invalid: %s", run.Name, validationErrs[0])
		}
		reason := kubeswarmv1alpha1.SearchTerminationPlannerFailure
		tree.TerminationReason = &reason
		r.finalizeSearchRun(run, tree)
		run.Status.ObservedGeneration = run.Generation
		return ctrl.Result{}, r.Status().Update(ctx, run)
	}

	// 11. Apply actions to tree.
	nodesBefore := int32(len(tree.Nodes))
	maxNodes := search.MaxNodes
	if maxNodes <= 0 {
		maxNodes = 50
	}
	if err := applySearchActions(tree, actions, maxNodes); err != nil {
		logger.Error(err, "applying search actions")
	}

	// Emit audit and K8s events for applied actions.
	r.emitSearchActionEvents(run, actions, tree, nodesBefore)

	// Check convergence again after converge actions.
	if reason := checkConvergence(tree, search); reason != nil {
		tree.TerminationReason = reason
		r.finalizeSearchRun(run, tree)
		if r.NotifyDispatcher != nil {
			r.NotifyDispatcher.DispatchRun(ctx, run)
		}
		run.Status.ObservedGeneration = run.Generation
		return ctrl.Result{}, r.Status().Update(ctx, run)
	}

	// 12. Truncate outputs to maxOutputBytes.
	maxOutputBytes := int32(4096)
	if search.MaxOutputBytes != nil {
		maxOutputBytes = *search.MaxOutputBytes
	}
	truncateNodeOutput(tree.Nodes, maxOutputBytes)

	// 13. Submit newly created Pending nodes from planner expand actions.
	if err := r.submitSearchNodes(ctx, run, tree, search, roleAgentMap, logger); err != nil {
		logger.Error(err, "submitting new search nodes")
		return ctrl.Result{}, fmt.Errorf("submitting search nodes: %w", err)
	}

	// Increment iteration counter.
	tree.Iterations++
	now := metav1.Now()
	tree.LastPlannerIteration = &now

	// 13. Update status and requeue.
	run.Status.ObservedGeneration = run.Generation
	if err := r.Status().Update(ctx, run); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: runRequeueAfter}, nil
}

// collectSearchExecutorResults polls the executor queue for completed nodes.
func (r *SwarmRunReconciler) collectSearchExecutorResults(
	ctx context.Context,
	run *kubeswarmv1alpha1.SwarmRun,
	tree *kubeswarmv1alpha1.SearchTreeStatus,
	executorRole string,
	roleAgentMap map[string]string,
	logger logr.Logger,
) error {
	var taskIDs []string
	taskToNode := make(map[string]*kubeswarmv1alpha1.SearchNodeStatus)
	for i := range tree.Nodes {
		n := &tree.Nodes[i]
		if n.Phase == kubeswarmv1alpha1.SearchNodePhaseRunning && n.TaskID != "" {
			taskIDs = append(taskIDs, n.TaskID)
			taskToNode[n.TaskID] = n
		}
	}
	if len(taskIDs) == 0 {
		return nil
	}

	agentName := r.resolveRoleAgent(run, executorRole, roleAgentMap)
	queueURL, err := r.agentQueueURL(ctx, run.Namespace, agentName)
	if err != nil {
		return fmt.Errorf("reading executor queue URL: %w", err)
	}
	if queueURL == "" {
		queueURL = r.computeRoleQueueURL(run.Namespace, run.Spec.TeamRef, executorRole)
	}

	q, closeQ, err := r.openQueueURL(queueURL)
	if err != nil {
		return fmt.Errorf("opening executor queue: %w", err)
	}
	results, err := q.Results(ctx, taskIDs)
	if closeQ != nil {
		closeQ()
	}
	if err != nil {
		logger.Error(err, "polling executor results")
		return nil
	}

	for _, res := range results {
		n, ok := taskToNode[res.TaskID]
		if !ok {
			continue
		}
		if res.Error != "" {
			n.Phase = kubeswarmv1alpha1.SearchNodePhaseEvalFailed
			n.Output = res.Error
			n.TaskID = ""
			r.emitSearchNodeEvent(run, audit.ActionSearchNodeEvalFailed, n)
			continue
		}
		n.Output = res.Output
		n.TaskID = ""
		if res.Usage.InputTokens > 0 || res.Usage.OutputTokens > 0 {
			n.TokenUsage = &kubeswarmv1alpha1.TokenUsage{
				InputTokens:    res.Usage.InputTokens,
				OutputTokens:   res.Usage.OutputTokens,
				ThinkingTokens: res.Usage.ThinkingTokens,
				TotalTokens:    res.Usage.InputTokens + res.Usage.OutputTokens + res.Usage.ThinkingTokens,
			}
		}
		// If no evaluator, the node stays in whatever phase the planner set (Scored if scoreMillis was provided).
		// If evaluator exists, mark as Scored pending evaluation submission.
		n.Phase = kubeswarmv1alpha1.SearchNodePhaseScored
	}
	return nil
}

// collectSearchEvaluatorResults polls the evaluator queue for scored nodes.
func (r *SwarmRunReconciler) collectSearchEvaluatorResults(
	ctx context.Context,
	run *kubeswarmv1alpha1.SwarmRun,
	tree *kubeswarmv1alpha1.SearchTreeStatus,
	evaluatorRole string,
	roleAgentMap map[string]string,
	logger logr.Logger,
) error {
	// Evaluator tasks use a separate TaskID tracking convention:
	// nodes that have output but no score and have a taskID are awaiting evaluator results.
	var taskIDs []string
	taskToNode := make(map[string]*kubeswarmv1alpha1.SearchNodeStatus)
	for i := range tree.Nodes {
		n := &tree.Nodes[i]
		// Nodes waiting for evaluator: they have output, no score, and a taskID set.
		if n.Output != "" && n.ScoreMillis == nil && n.TaskID != "" && n.Phase == kubeswarmv1alpha1.SearchNodePhaseScored {
			taskIDs = append(taskIDs, n.TaskID)
			taskToNode[n.TaskID] = n
		}
	}
	if len(taskIDs) == 0 {
		return nil
	}

	agentName := r.resolveRoleAgent(run, evaluatorRole, roleAgentMap)
	queueURL, err := r.agentQueueURL(ctx, run.Namespace, agentName)
	if err != nil {
		return fmt.Errorf("reading evaluator queue URL: %w", err)
	}
	if queueURL == "" {
		queueURL = r.computeRoleQueueURL(run.Namespace, run.Spec.TeamRef, evaluatorRole)
	}

	q, closeQ, err := r.openQueueURL(queueURL)
	if err != nil {
		return fmt.Errorf("opening evaluator queue: %w", err)
	}
	results, err := q.Results(ctx, taskIDs)
	if closeQ != nil {
		closeQ()
	}
	if err != nil {
		logger.Error(err, "polling evaluator results")
		return nil
	}

	for _, res := range results {
		n, ok := taskToNode[res.TaskID]
		if !ok {
			continue
		}
		n.TaskID = ""
		if res.Error != "" {
			n.Phase = kubeswarmv1alpha1.SearchNodePhaseEvalFailed
			n.ScoreReasoning = res.Error
			r.emitSearchNodeEvent(run, audit.ActionSearchNodeEvalFailed, n)
			if r.Recorder != nil {
				r.Recorder.Eventf(run, nil, corev1.EventTypeWarning, "EvaluatorParseFailed", "Reconcile",
					"SwarmRun %q evaluator failed for node %d", run.Name, n.ID)
			}
			continue
		}
		var score kubeswarmv1alpha1.SearchNodeScore
		if err := json.Unmarshal([]byte(res.Output), &score); err != nil {
			n.Phase = kubeswarmv1alpha1.SearchNodePhaseEvalFailed
			n.ScoreReasoning = fmt.Sprintf("evaluator output parse error: %v", err)
			r.emitSearchNodeEvent(run, audit.ActionSearchNodeEvalFailed, n)
			if r.Recorder != nil {
				r.Recorder.Eventf(run, nil, corev1.EventTypeWarning, "EvaluatorParseFailed", "Reconcile",
					"SwarmRun %q evaluator parse error for node %d: %v", run.Name, n.ID, err)
			}
			continue
		}
		n.ScoreMillis = &score.ScoreMillis
		n.ScoreReasoning = score.Reasoning
		n.Phase = kubeswarmv1alpha1.SearchNodePhaseScored
		r.emitSearchNodeEvent(run, audit.ActionSearchNodeScored, n)
	}
	return nil
}

// invokePlanner submits the frontier context to the planner and parses its response.
func (r *SwarmRunReconciler) invokePlanner(
	ctx context.Context,
	run *kubeswarmv1alpha1.SwarmRun,
	tree *kubeswarmv1alpha1.SearchTreeStatus,
	search *kubeswarmv1alpha1.SwarmTeamSearchSpec,
	roleAgentMap map[string]string,
	logger logr.Logger,
) ([]kubeswarmv1alpha1.SearchAction, error) {
	frontierJSON, err := buildFrontierContext(tree, search)
	if err != nil {
		return nil, fmt.Errorf("building frontier context: %w", err)
	}

	agentName := r.resolveRoleAgent(run, search.PlannerRole, roleAgentMap)
	queueURL, err := r.agentQueueURL(ctx, run.Namespace, agentName)
	if err != nil {
		return nil, fmt.Errorf("reading planner queue URL: %w", err)
	}
	if queueURL == "" {
		queueURL = r.computeRoleQueueURL(run.Namespace, run.Spec.TeamRef, search.PlannerRole)
	}

	q, closeQ, err := r.openQueueURL(queueURL)
	if err != nil {
		return nil, fmt.Errorf("opening planner queue: %w", err)
	}

	taskID, err := q.Submit(ctx, string(frontierJSON), map[string]string{
		"run_name": run.Name,
		"role":     search.PlannerRole,
		"mode":     "search_planner",
	})
	if closeQ != nil {
		closeQ()
	}
	if err != nil {
		return nil, fmt.Errorf("submitting planner task: %w", err)
	}

	logger.Info("submitted planner task", "run", run.Name, "taskID", taskID)

	// Poll for planner result. The planner should respond within a single reconcile cycle
	// for the synchronous case. For async, we would need to store the taskID and requeue.
	// For Phase 3, use synchronous polling with requeue.
	q2, closeQ2, err := r.openQueueURL(queueURL)
	if err != nil {
		return nil, fmt.Errorf("opening planner queue for result: %w", err)
	}
	results, err := q2.Results(ctx, []string{taskID})
	if closeQ2 != nil {
		closeQ2()
	}
	if err != nil {
		return nil, fmt.Errorf("polling planner result: %w", err)
	}

	if len(results) == 0 {
		// Planner hasn't responded yet. This is expected on first invocation.
		// We return empty actions, and the reconciler will requeue.
		return nil, nil
	}

	res := results[0]
	if res.Error != "" {
		return nil, fmt.Errorf("planner error: %s", res.Error)
	}

	var actions []kubeswarmv1alpha1.SearchAction
	if err := json.Unmarshal([]byte(res.Output), &actions); err != nil {
		return nil, fmt.Errorf("parsing planner output: %w", err)
	}

	return actions, nil
}

// submitSearchNodes submits Pending nodes to the executor queue (up to maxParallel).
func (r *SwarmRunReconciler) submitSearchNodes(
	ctx context.Context,
	run *kubeswarmv1alpha1.SwarmRun,
	tree *kubeswarmv1alpha1.SearchTreeStatus,
	search *kubeswarmv1alpha1.SwarmTeamSearchSpec,
	roleAgentMap map[string]string,
	logger logr.Logger,
) error {
	maxParallel := search.MaxParallel
	if maxParallel <= 0 {
		maxParallel = 3
	}

	// Count currently running nodes.
	running := int32(0)
	for _, n := range tree.Nodes {
		if n.Phase == kubeswarmv1alpha1.SearchNodePhaseRunning {
			running++
		}
	}

	agentName := r.resolveRoleAgent(run, search.ExecutorRole, roleAgentMap)
	queueURL, err := r.agentQueueURL(ctx, run.Namespace, agentName)
	if err != nil {
		return fmt.Errorf("reading executor queue URL: %w", err)
	}
	if queueURL == "" {
		queueURL = r.computeRoleQueueURL(run.Namespace, run.Spec.TeamRef, search.ExecutorRole)
	}

	q, closeQ, err := r.openQueueURL(queueURL)
	if err != nil {
		return fmt.Errorf("opening executor queue: %w", err)
	}
	defer func() {
		if closeQ != nil {
			closeQ()
		}
	}()

	for i := range tree.Nodes {
		if running >= maxParallel {
			break
		}
		n := &tree.Nodes[i]
		if n.Phase != kubeswarmv1alpha1.SearchNodePhasePending {
			continue
		}

		taskID, err := q.Submit(ctx, n.Task, map[string]string{
			"run_name": run.Name,
			"role":     search.ExecutorRole,
			"mode":     "search_executor",
			"node_id":  fmt.Sprintf("%d", n.ID),
		})
		if err != nil {
			return fmt.Errorf("submitting executor task for node %d: %w", n.ID, err)
		}

		n.Phase = kubeswarmv1alpha1.SearchNodePhaseRunning
		n.TaskID = taskID
		running++
		logger.Info("submitted executor task", "run", run.Name, "nodeID", n.ID, "taskID", taskID)
	}

	return nil
}

// finalizeSearchRun sets the terminal phase and output for a completed search run.
func (r *SwarmRunReconciler) finalizeSearchRun(
	run *kubeswarmv1alpha1.SwarmRun,
	tree *kubeswarmv1alpha1.SearchTreeStatus,
) {
	now := metav1.Now()
	run.Status.CompletionTime = &now

	// Find the best node for the output.
	if tree.SolutionNodeID != nil {
		for _, n := range tree.Nodes {
			if n.ID == *tree.SolutionNodeID {
				run.Status.Output = n.Output
				break
			}
		}
	} else {
		// No explicit solution: pick highest-scored node.
		var best *kubeswarmv1alpha1.SearchNodeStatus
		for i := range tree.Nodes {
			n := &tree.Nodes[i]
			if n.ScoreMillis == nil {
				continue
			}
			if best == nil || *n.ScoreMillis > *best.ScoreMillis {
				best = n
			}
		}
		if best != nil {
			run.Status.Output = best.Output
		}
	}

	if tree.TerminationReason != nil && *tree.TerminationReason == kubeswarmv1alpha1.SearchTerminationPlannerFailure {
		run.Status.Phase = kubeswarmv1alpha1.SwarmRunPhaseFailed
		flow.SetRunCondition(run, metav1.ConditionFalse, "SearchFailed",
			fmt.Sprintf("search terminated: %s", *tree.TerminationReason))
		if r.Recorder != nil {
			r.Recorder.Eventf(run, nil, corev1.EventTypeWarning, "RunFailed", "Reconcile",
				"SwarmRun %q search failed: %s", run.Name, *tree.TerminationReason)
		}
	} else {
		run.Status.Phase = kubeswarmv1alpha1.SwarmRunPhaseSucceeded
		reason := "unknown"
		if tree.TerminationReason != nil {
			reason = string(*tree.TerminationReason)
		}
		flow.SetRunCondition(run, metav1.ConditionTrue, "Succeeded",
			fmt.Sprintf("search converged: %s", reason))
		if r.Recorder != nil {
			r.Recorder.Eventf(run, nil, corev1.EventTypeNormal, "RunSucceeded", "Reconcile",
				"SwarmRun %q search converged: %s", run.Name, reason)
		}
	}

	// Emit audit events for terminal search state.
	if r.AuditEmitter != nil {
		switch run.Status.Phase {
		case kubeswarmv1alpha1.SwarmRunPhaseSucceeded:
			r.AuditEmitter.Emit(newRunAuditEvent(audit.ActionRunSucceeded, audit.StatusSuccess, run, ""))
		case kubeswarmv1alpha1.SwarmRunPhaseFailed:
			r.AuditEmitter.Emit(newRunAuditEvent(audit.ActionRunFailed, audit.StatusError, run, ""))
		}
	}

	// Emit specific search K8s events.
	if r.Recorder != nil && tree.TerminationReason != nil {
		switch *tree.TerminationReason {
		case kubeswarmv1alpha1.SearchTerminationPlannerConverged,
			kubeswarmv1alpha1.SearchTerminationMinScoreReached:
			r.Recorder.Eventf(run, nil, corev1.EventTypeNormal, "SearchConverged", "Reconcile",
				"SwarmRun %q search converged: %s", run.Name, *tree.TerminationReason)
		case kubeswarmv1alpha1.SearchTerminationMaxNodesReached,
			kubeswarmv1alpha1.SearchTerminationMaxDepthReached,
			kubeswarmv1alpha1.SearchTerminationMaxIterationsReached,
			kubeswarmv1alpha1.SearchTerminationBudgetExhausted:
			r.Recorder.Eventf(run, nil, corev1.EventTypeWarning, "SearchExhausted", "Reconcile",
				"SwarmRun %q search exhausted: %s", run.Name, *tree.TerminationReason)
		}
	}

	// Sum token usage across all nodes.
	var totalUsage kubeswarmv1alpha1.TokenUsage
	for _, n := range tree.Nodes {
		if n.TokenUsage != nil {
			totalUsage.InputTokens += n.TokenUsage.InputTokens
			totalUsage.OutputTokens += n.TokenUsage.OutputTokens
			totalUsage.ThinkingTokens += n.TokenUsage.ThinkingTokens
			totalUsage.TotalTokens += n.TokenUsage.TotalTokens
		}
	}
	if totalUsage.TotalTokens > 0 {
		run.Status.TotalTokenUsage = &totalUsage
	}
}

// emitSearchNodeEvent emits a single audit event for a search node state change.
func (r *SwarmRunReconciler) emitSearchNodeEvent(run *kubeswarmv1alpha1.SwarmRun, action audit.Action, n *kubeswarmv1alpha1.SearchNodeStatus) {
	if r.AuditEmitter == nil {
		return
	}
	evt := newRunAuditEvent(action, audit.StatusSuccess, run, "")
	detail := map[string]any{
		"nodeID": n.ID,
		"depth":  n.Depth,
		"phase":  string(n.Phase),
		"task":   n.Task,
	}
	if n.ScoreMillis != nil {
		detail["scoreMillis"] = *n.ScoreMillis
	}
	evt.Detail, _ = json.Marshal(detail)
	r.AuditEmitter.Emit(evt)
}

// emitSearchActionEvents emits audit and K8s events for planner actions that were applied.
func (r *SwarmRunReconciler) emitSearchActionEvents(
	run *kubeswarmv1alpha1.SwarmRun,
	actions []kubeswarmv1alpha1.SearchAction,
	tree *kubeswarmv1alpha1.SearchTreeStatus,
	nodesBefore int32,
) {
	var expanded, pruned int
	for _, a := range actions {
		switch a.Action {
		case actionExpand:
			expanded++
		case actionPrune:
			pruned++
			if a.Node != nil {
				for i := range tree.Nodes {
					if tree.Nodes[i].ID == *a.Node {
						r.emitSearchNodeEvent(run, audit.ActionSearchNodePruned, &tree.Nodes[i])
						break
					}
				}
			}
		case actionConverge:
			if a.Node != nil {
				for i := range tree.Nodes {
					if tree.Nodes[i].ID == *a.Node {
						r.emitSearchNodeEvent(run, audit.ActionSearchNodeSolution, &tree.Nodes[i])
						break
					}
				}
			}
		}
	}

	// Emit audit events for newly created nodes.
	for i := nodesBefore; i < int32(len(tree.Nodes)); i++ {
		r.emitSearchNodeEvent(run, audit.ActionSearchNodeCreated, &tree.Nodes[i])
	}

	// K8s events for expand and prune.
	if r.Recorder != nil {
		if expanded > 0 {
			r.Recorder.Eventf(run, nil, corev1.EventTypeNormal, "SearchExpanded", "Reconcile",
				"SwarmRun %q expanded %d new nodes (total: %d)", run.Name, expanded, len(tree.Nodes))
		}
		if pruned > 0 {
			r.Recorder.Eventf(run, nil, corev1.EventTypeNormal, "SearchPruned", "Reconcile",
				"SwarmRun %q pruned %d nodes", run.Name, pruned)
		}
	}
}

// cancelSearchTasks cancels all in-flight search tasks.
func (r *SwarmRunReconciler) cancelSearchTasks(
	ctx context.Context,
	run *kubeswarmv1alpha1.SwarmRun,
	tree *kubeswarmv1alpha1.SearchTreeStatus,
	logger logr.Logger,
) {
	if tree == nil {
		return
	}
	var taskIDs []string
	for _, n := range tree.Nodes {
		if n.TaskID != "" && (n.Phase == kubeswarmv1alpha1.SearchNodePhaseRunning || n.Phase == kubeswarmv1alpha1.SearchNodePhasePending) {
			taskIDs = append(taskIDs, n.TaskID)
		}
	}
	if len(taskIDs) == 0 {
		return
	}

	roleAgentMap, _ := buildRoleMaps(run)
	search := run.Spec.Search

	// Cancel executor tasks.
	agentName := r.resolveRoleAgent(run, search.ExecutorRole, roleAgentMap)
	queueURL, err := r.agentQueueURL(ctx, run.Namespace, agentName)
	if err == nil {
		if queueURL == "" {
			queueURL = r.computeRoleQueueURL(run.Namespace, run.Spec.TeamRef, search.ExecutorRole)
		}
		if q, closeQ, qErr := r.openQueueURL(queueURL); qErr == nil {
			if cancelErr := q.Cancel(ctx, taskIDs); cancelErr != nil {
				logger.Error(cancelErr, "cancelling search tasks")
			}
			if closeQ != nil {
				closeQ()
			}
		}
	}
}
