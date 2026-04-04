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
	"net/url"
	"sort"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kubeswarmv1alpha1 "github.com/kubeswarm/kubeswarm/api/v1alpha1"
	"github.com/kubeswarm/kubeswarm/pkg/agent/queue"
	"github.com/kubeswarm/kubeswarm/pkg/flow"
)

const (
	defaultSuccessfulRunsLimit = int32(10)
	defaultFailedRunsLimit     = int32(3)
)

// SwarmTeamReconciler reconciles SwarmTeam objects.
type SwarmTeamReconciler struct {
	client.Client
	Scheme            *runtime.Scheme
	TaskQueueURL      string // operator's own Redis connection URL
	AgentTaskQueueURL string // Redis URL injected into agent pods (may differ when operator runs outside cluster)
	TaskQueue         queue.TaskQueue
	Recorder          record.EventRecorder
}

// +kubebuilder:rbac:groups=kubeswarm.io,resources=swarmteams,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=kubeswarm.io,resources=swarmteams/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=kubeswarm.io,resources=swarmteams/finalizers,verbs=update
// +kubebuilder:rbac:groups=kubeswarm.io,resources=swarmagents,verbs=get;list;watch;create;update;patch;delete

// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch
// +kubebuilder:rbac:groups="",resources=namespaces,verbs=get;patch

func (r *SwarmTeamReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	team := &kubeswarmv1alpha1.SwarmTeam{}
	if err := r.Get(ctx, req.NamespacedName, team); err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	if !team.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, nil
	}

	// Apply Pod Security Standards labels to the namespace — non-fatal, log and continue.
	if err := r.ensureNamespacePSS(ctx, team.Namespace); err != nil {
		log.FromContext(ctx).Error(err, "failed to apply PSS labels to namespace", "namespace", team.Namespace)
	}

	// Routed mode: no managed roles — standalone agents are resolved from SwarmRegistry.
	if team.Spec.Routing != nil {
		return r.reconcileRoutedInfra(ctx, team)
	}

	// Pipeline mode: infrastructure reconcile only (execution handled by SwarmRun controller).
	if len(team.Spec.Pipeline) > 0 {
		return r.reconcilePipelineInfra(ctx, team)
	}

	// Dynamic mode: service semantics, delegation routing.
	return r.reconcileDynamicMode(ctx, team)
}

// ensureNamespacePSS applies Pod Security Standard labels to a namespace managed by
// kubeswarm (RFC-0016 Phase 1). Uses the "restricted" profile — the most secure
// standard — which all swarm-managed pods already comply with.
// This is a best-effort patch; failure does not block team reconciliation.
func (r *SwarmTeamReconciler) ensureNamespacePSS(ctx context.Context, namespace string) error {
	// Server-side apply: no Get needed, no cache dependency.
	// Only requires patch permission on namespaces.
	ns := &corev1.Namespace{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "v1",
			Kind:       "Namespace",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name: namespace,
			Labels: map[string]string{
				"pod-security.kubernetes.io/enforce":         "restricted",
				"pod-security.kubernetes.io/enforce-version": "latest",
				"pod-security.kubernetes.io/warn":            "restricted",
				"pod-security.kubernetes.io/warn-version":    "latest",
			},
		},
	}
	//nolint:staticcheck // client.Apply is the correct SSA patch type for controller-runtime v0.23
	return r.Patch(ctx, ns, client.Apply, client.FieldOwner("kubeswarm"), client.ForceOwnership)
}

// reconcileDynamicMode handles SwarmTeam in dynamic mode (no pipeline).
func (r *SwarmTeamReconciler) reconcileDynamicMode(ctx context.Context, team *kubeswarmv1alpha1.SwarmTeam) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// Only treat as a template when the team hasn't been touched yet.
	if team.Status.Phase == "" {
		if isTemplate, err := r.isTriggerTemplate(ctx, team); err != nil {
			return ctrl.Result{}, err
		} else if isTemplate {
			return ctrl.Result{}, nil
		}
	}

	// 1. Validate topology.
	if err := r.validateTopology(team); err != nil {
		logger.Error(err, "invalid team topology")
		r.setCondition(team, metav1.ConditionFalse, "InvalidTopology", err.Error())
		_ = r.Status().Update(ctx, team)
		if r.Recorder != nil {
			r.Recorder.Event(team, corev1.EventTypeWarning, "TopologyInvalid", err.Error())
		}
		return ctrl.Result{}, nil
	}

	// 2. Reconcile inline roles (auto-create SwarmAgent CRs) and collect role statuses.
	roleStatuses, entryRoleName, err := r.reconcileRoles(ctx, team)
	if err != nil {
		logger.Error(err, "failed to reconcile team roles")
		r.setCondition(team, metav1.ConditionFalse, "ReconcileError", err.Error())
		_ = r.Status().Update(ctx, team)
		return ctrl.Result{}, err
	}

	// 3. Update status.
	team.Status.Phase = kubeswarmv1alpha1.SwarmTeamPhaseReady
	team.Status.Roles = roleStatuses
	team.Status.EntryRole = entryRoleName
	team.Status.ObservedGeneration = team.Generation
	r.setPromptSizeWarning(team)
	msg := fmt.Sprintf("team %q ready with %d roles", team.Name, len(team.Spec.Roles))
	r.setCondition(team, metav1.ConditionTrue, "Reconciled", msg)
	if r.Recorder != nil {
		r.Recorder.Event(team, corev1.EventTypeNormal, "TeamReconciled", msg)
	}

	return ctrl.Result{}, r.Status().Update(ctx, team)
}

// reconcilePipelineInfra handles SwarmTeam in pipeline mode: ensures SwarmAgent CRs exist
// for each role and mirrors the latest SwarmRun phase to the team status. Actual pipeline
// execution is handled by the SwarmRun controller.
func (r *SwarmTeamReconciler) reconcilePipelineInfra(ctx context.Context, team *kubeswarmv1alpha1.SwarmTeam) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	if err := flow.ValidateTeamDAG(team); err != nil {
		logger.Error(err, "invalid pipeline DAG")
		flow.SetTeamCondition(team, metav1.ConditionFalse, "InvalidDAG", err.Error())
		_ = r.Status().Update(ctx, team)
		return ctrl.Result{}, nil
	}

	roleStatuses, _, err := r.reconcileRoles(ctx, team)
	if err != nil {
		logger.Error(err, "failed to reconcile pipeline roles")
		flow.SetTeamCondition(team, metav1.ConditionFalse, "RolesNotReady", err.Error())
		_ = r.Status().Update(ctx, team)
		return ctrl.Result{}, err
	}
	r.setPromptSizeWarning(team)

	// Mirror latest SwarmRun phase and name so kubectl get swarmteam shows live status.
	var runs kubeswarmv1alpha1.SwarmRunList
	if err := r.List(ctx, &runs,
		client.InNamespace(team.Namespace),
		client.MatchingLabels{"kubeswarm/team": team.Name},
	); err != nil {
		return ctrl.Result{}, err
	}
	// Enforce retention policy — delete old completed runs beyond the configured limits.
	if err := r.pruneRuns(ctx, team, runs.Items); err != nil {
		logger.Error(err, "pruning old SwarmRuns")
		// Non-fatal: log and continue reconciling.
	}

	// Scale inline-role agents up/down based on active steps and idle timeout.
	autoscaleResult, asErr := r.reconcileAutoscaling(ctx, team, runs.Items)
	if asErr != nil {
		logger.Error(asErr, "autoscaling reconcile failed — continuing")
	}

	var latestRun *kubeswarmv1alpha1.SwarmRun
	for i := range runs.Items {
		run := &runs.Items[i]
		if latestRun == nil || run.CreationTimestamp.After(latestRun.CreationTimestamp.Time) {
			latestRun = run
		}
	}

	team.Status.Roles = roleStatuses
	team.Status.ObservedGeneration = team.Generation
	if latestRun != nil {
		team.Status.LastRunName = latestRun.Name
		team.Status.LastRunPhase = latestRun.Status.Phase
		switch latestRun.Status.Phase {
		case kubeswarmv1alpha1.SwarmRunPhaseRunning:
			team.Status.Phase = kubeswarmv1alpha1.SwarmTeamPhaseRunning
		case kubeswarmv1alpha1.SwarmRunPhaseSucceeded:
			team.Status.Phase = kubeswarmv1alpha1.SwarmTeamPhaseSucceeded
		case kubeswarmv1alpha1.SwarmRunPhaseFailed:
			team.Status.Phase = kubeswarmv1alpha1.SwarmTeamPhaseFailed
		default:
			team.Status.Phase = kubeswarmv1alpha1.SwarmTeamPhaseReady
		}
	} else {
		team.Status.Phase = kubeswarmv1alpha1.SwarmTeamPhaseReady
	}

	flow.SetTeamCondition(team, metav1.ConditionTrue, "Reconciled",
		fmt.Sprintf("pipeline team %q ready with %d roles", team.Name, len(team.Spec.Roles)))
	if err := r.Status().Update(ctx, team); err != nil {
		return ctrl.Result{}, err
	}
	// Honour requeue requested by autoscaler (idle scale-to-zero countdown).
	return autoscaleResult, nil
}

// reconcileRoutedInfra handles SwarmTeam in routed mode: no managed roles exist (agents are
// standalone and registered in SwarmRegistry). The team controller only mirrors the latest
// SwarmRun phase and prunes old runs — actual dispatch is handled by SwarmRunReconciler.
func (r *SwarmTeamReconciler) reconcileRoutedInfra(ctx context.Context, team *kubeswarmv1alpha1.SwarmTeam) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	var runs kubeswarmv1alpha1.SwarmRunList
	if err := r.List(ctx, &runs,
		client.InNamespace(team.Namespace),
		client.MatchingLabels{"kubeswarm/team": team.Name},
	); err != nil {
		return ctrl.Result{}, err
	}

	if err := r.pruneRuns(ctx, team, runs.Items); err != nil {
		logger.Error(err, "pruning old SwarmRuns for routed team")
	}

	var latestRun *kubeswarmv1alpha1.SwarmRun
	for i := range runs.Items {
		run := &runs.Items[i]
		if latestRun == nil || run.CreationTimestamp.After(latestRun.CreationTimestamp.Time) {
			latestRun = run
		}
	}

	team.Status.Roles = nil // no managed roles in routed mode
	team.Status.ObservedGeneration = team.Generation
	if latestRun != nil {
		team.Status.LastRunName = latestRun.Name
		team.Status.LastRunPhase = latestRun.Status.Phase
		switch latestRun.Status.Phase {
		case kubeswarmv1alpha1.SwarmRunPhaseRunning:
			team.Status.Phase = kubeswarmv1alpha1.SwarmTeamPhaseRunning
		case kubeswarmv1alpha1.SwarmRunPhaseSucceeded:
			team.Status.Phase = kubeswarmv1alpha1.SwarmTeamPhaseSucceeded
		case kubeswarmv1alpha1.SwarmRunPhaseFailed:
			team.Status.Phase = kubeswarmv1alpha1.SwarmTeamPhaseFailed
		default:
			team.Status.Phase = kubeswarmv1alpha1.SwarmTeamPhaseReady
		}
	} else {
		team.Status.Phase = kubeswarmv1alpha1.SwarmTeamPhaseReady
	}

	flow.SetTeamCondition(team, metav1.ConditionTrue, "Reconciled",
		fmt.Sprintf("routed team %q ready", team.Name))
	return ctrl.Result{}, r.Status().Update(ctx, team)
}

// validateTopology checks dynamic mode team spec for consistency:
// - unique role names, entry role present (spec.entry matches a role), delegate targets are declared,
// - no cycles in the delegation graph.
func (r *SwarmTeamReconciler) validateTopology(team *kubeswarmv1alpha1.SwarmTeam) error {
	roleSet := make(map[string]struct{}, len(team.Spec.Roles))
	for _, role := range team.Spec.Roles {
		if _, dup := roleSet[role.Name]; dup {
			return fmt.Errorf("duplicate role name %q", role.Name)
		}
		roleSet[role.Name] = struct{}{}
	}

	// Entry is optional in pipeline mode (handled separately), but required for dynamic mode.
	if team.Spec.Entry != "" {
		if _, ok := roleSet[team.Spec.Entry]; !ok {
			return fmt.Errorf("spec.entry %q is not a declared role", team.Spec.Entry)
		}
	} else if len(team.Spec.Roles) > 0 {
		return fmt.Errorf("spec.entry is required in dynamic mode")
	}

	// Validate delegate targets are declared roles.
	for _, role := range team.Spec.Roles {
		for _, target := range role.CanDelegate {
			if _, ok := roleSet[target]; !ok {
				return fmt.Errorf("role %q canDelegate to %q which is not a declared role", role.Name, target)
			}
		}
	}

	// Cycle detection via DFS on the delegation graph.
	adjacency := make(map[string][]string, len(team.Spec.Roles))
	for _, role := range team.Spec.Roles {
		adjacency[role.Name] = role.CanDelegate
	}
	visited := make(map[string]bool)
	inStack := make(map[string]bool)
	var dfs func(role string) bool
	dfs = func(role string) bool {
		visited[role] = true
		inStack[role] = true
		for _, next := range adjacency[role] {
			if !visited[next] {
				if dfs(next) {
					return true
				}
			} else if inStack[next] {
				return true
			}
		}
		inStack[role] = false
		return false
	}
	for _, role := range team.Spec.Roles {
		if !visited[role.Name] {
			if dfs(role.Name) {
				return fmt.Errorf("delegation graph contains a cycle")
			}
		}
	}

	return nil
}

// reconcileRoles ensures SwarmAgent CRs exist for inline roles, annotates all agents with
// their role-specific queue URL and delegation routing table, and returns role statuses.
func (r *SwarmTeamReconciler) reconcileRoles(
	ctx context.Context,
	team *kubeswarmv1alpha1.SwarmTeam,
) ([]kubeswarmv1alpha1.SwarmTeamRoleStatus, string, error) {
	// Compute all role → queueURL mappings up front.
	roleQueueURL := make(map[string]string, len(team.Spec.Roles))
	for _, role := range team.Spec.Roles {
		roleQueueURL[role.Name] = r.roleQueueURL(team, role.Name)
	}

	var statuses []kubeswarmv1alpha1.SwarmTeamRoleStatus

	for _, role := range team.Spec.Roles {
		agentName, err := r.reconcileRoleAgent(ctx, team, role)
		if err != nil {
			return nil, "", err
		}

		agent := &kubeswarmv1alpha1.SwarmAgent{}
		if err := r.Get(ctx, client.ObjectKey{Name: agentName, Namespace: team.Namespace}, agent); err != nil {
			if errors.IsNotFound(err) {
				// Inline agent may still be creating.
				statuses = append(statuses, kubeswarmv1alpha1.SwarmTeamRoleStatus{
					Name:              role.Name,
					ManagedSwarmAgent: agentName,
				})
				continue
			}
			return nil, "", fmt.Errorf("fetching SwarmAgent %q for role %q: %w", agentName, role.Name, err)
		}

		// Build the routes table: only roles this role can delegate to.
		routes := make(map[string]string, len(role.CanDelegate))
		for _, target := range role.CanDelegate {
			routes[target] = roleQueueURL[target]
		}
		routesJSON, err := json.Marshal(routes)
		if err != nil {
			return nil, "", fmt.Errorf("marshalling routes for role %q: %w", role.Name, err)
		}

		patch := client.MergeFrom(agent.DeepCopy())
		if agent.Annotations == nil {
			agent.Annotations = make(map[string]string)
		}
		agent.Annotations[annotationTeamQueueURL] = roleQueueURL[role.Name]
		agent.Annotations[annotationTeamRoutes] = string(routesJSON)
		agent.Annotations[annotationTeamRole] = role.Name
		// Propagate artifact store info (RFC-0013) so the agent controller can inject
		// AGENT_ARTIFACT_STORE_URL and mount the PVC without re-reading the team spec.
		if storeURL := artifactStoreURL(team.Spec.ArtifactStore); storeURL != "" {
			agent.Annotations[annotationTeamArtifactStore] = storeURL
		}
		if claimName := artifactStoreClaim(team.Spec.ArtifactStore); claimName != "" {
			agent.Annotations[annotationTeamArtifactClaim] = claimName
		}
		if err := r.Patch(ctx, agent, patch); err != nil {
			return nil, "", fmt.Errorf("patching SwarmAgent %q: %w", agentName, err)
		}

		managedName := ""
		if role.SwarmAgent == "" {
			managedName = agentName // inline role
		}
		statuses = append(statuses, kubeswarmv1alpha1.SwarmTeamRoleStatus{
			Name:          role.Name,
			ReadyReplicas: agent.Status.ReadyReplicas,
			DesiredReplicas: func() int32 {
				if agent.Spec.Runtime.Replicas != nil {
					return *agent.Spec.Runtime.Replicas
				}
				return 1
			}(),
			ManagedSwarmAgent: managedName,
		})
	}

	// Determine entry role name.
	entryRoleName := team.Spec.Entry

	return statuses, entryRoleName, nil
}

// reconcileRoleAgent ensures an SwarmAgent CR exists for a role. For external SwarmAgent references
// it just returns the referenced name. For inline roles (Model+SystemPrompt set) it creates/updates
// an SwarmAgent named "{team}-{role}".
func (r *SwarmTeamReconciler) reconcileRoleAgent(
	ctx context.Context,
	team *kubeswarmv1alpha1.SwarmTeam,
	role kubeswarmv1alpha1.SwarmTeamRole,
) (string, error) {
	if role.SwarmAgent != "" {
		return role.SwarmAgent, nil
	}
	if role.SwarmTeam != "" {
		// Cross-team: handled separately; return placeholder.
		return role.SwarmTeam + "-entry", nil
	}

	// Inline role: auto-create SwarmAgent named "{team}-{role}".
	agentName := team.Name + "-" + role.Name
	var replicas *int32
	if role.Runtime != nil {
		replicas = role.Runtime.Replicas
	}

	desired := &kubeswarmv1alpha1.SwarmAgent{
		ObjectMeta: metav1.ObjectMeta{
			Name:      agentName,
			Namespace: team.Namespace,
			Labels: map[string]string{
				"kubeswarm/team": team.Name,
				"kubeswarm/role": role.Name,
			},
		},
		Spec: kubeswarmv1alpha1.SwarmAgentSpec{
			Model:  role.Model,
			Prompt: role.Prompt,
			Tools:  role.Tools,
			Guardrails: func() *kubeswarmv1alpha1.AgentGuardrails {
				g := &kubeswarmv1alpha1.AgentGuardrails{
					Limits:    role.Limits,
					BudgetRef: team.Spec.BudgetRef,
				}
				if g.Limits == nil && g.BudgetRef == nil {
					return nil
				}
				return g
			}(),
			Runtime: func() kubeswarmv1alpha1.AgentRuntime {
				if role.Runtime != nil {
					rt := *role.Runtime
					if rt.Replicas == nil {
						rt.Replicas = replicas
					}
					return rt
				}
				return kubeswarmv1alpha1.AgentRuntime{Replicas: replicas}
			}(),
			Settings: role.Settings,
			Infrastructure: func() *kubeswarmv1alpha1.AgentInfrastructure {
				infra := &kubeswarmv1alpha1.AgentInfrastructure{
					EnvFrom: role.EnvFrom,
					Plugins: role.Plugins,
					RegistryRef: func() *kubeswarmv1alpha1.LocalObjectReference {
						if team.Spec.RegistryRef == "" {
							return nil
						}
						return &kubeswarmv1alpha1.LocalObjectReference{Name: team.Spec.RegistryRef}
					}(),
				}
				if infra.EnvFrom == nil && infra.Plugins == nil && infra.RegistryRef == nil {
					return nil
				}
				return infra
			}(),
			Observability: func() *kubeswarmv1alpha1.AgentObservability {
				if team.Spec.NotifyRef == nil {
					return nil
				}
				return &kubeswarmv1alpha1.AgentObservability{
					HealthCheck: &kubeswarmv1alpha1.AgentHealthCheck{
						Type:      kubeswarmv1alpha1.HealthCheckSemantic,
						NotifyRef: team.Spec.NotifyRef,
					},
				}
			}(),
		},
	}
	if err := ctrl.SetControllerReference(team, desired, r.Scheme); err != nil {
		return "", err
	}

	existing := &kubeswarmv1alpha1.SwarmAgent{}
	err := r.Get(ctx, client.ObjectKeyFromObject(desired), existing)
	if errors.IsNotFound(err) {
		return agentName, r.Create(ctx, desired)
	}
	if err != nil {
		return "", err
	}
	// Update spec in case role definition changed.
	// Preserve spec.runtime.replicas when team autoscaling is enabled - the autoscaler owns replica count.
	patch := client.MergeFrom(existing.DeepCopy())
	preservedReplicas := existing.Spec.Runtime.Replicas
	existing.Spec = desired.Spec
	if team.Spec.Autoscaling != nil && team.Spec.Autoscaling.Enabled {
		existing.Spec.Runtime.Replicas = preservedReplicas
	}
	return agentName, r.Patch(ctx, existing, patch)
}

// roleQueueURL returns the task queue URL for a given role.
// It uses AgentTaskQueueURL when set (the in-cluster URL injected into pods),
// falling back to TaskQueueURL (the operator's own connection URL).
func (r *SwarmTeamReconciler) roleQueueURL(team *kubeswarmv1alpha1.SwarmTeam, role string) string {
	base := r.AgentTaskQueueURL
	if base == "" {
		base = r.TaskQueueURL
	}
	if base == "" {
		return ""
	}
	streamName := fmt.Sprintf("%s.%s.%s", team.Namespace, team.Name, role)
	u, err := url.Parse(base)
	if err != nil {
		return base
	}
	q := u.Query()
	q.Set("stream", streamName)
	u.RawQuery = q.Encode()
	return u.String()
}

// artifactStoreURL converts an ArtifactStoreSpec to the URL string injected as
// AGENT_ARTIFACT_STORE_URL (RFC-0013). Returns an empty string when spec is nil
// or the backend is not yet implemented.
func artifactStoreURL(spec *kubeswarmv1alpha1.ArtifactStoreSpec) string {
	if spec == nil {
		return ""
	}
	switch spec.Type {
	case kubeswarmv1alpha1.ArtifactStoreLocal:
		path := "/artifacts"
		if spec.Local != nil && spec.Local.Path != "" {
			path = spec.Local.Path
		}
		return "file://" + path
	case kubeswarmv1alpha1.ArtifactStoreS3:
		if spec.S3 == nil || spec.S3.Bucket == "" {
			return ""
		}
		u := url.URL{
			Scheme: "s3",
			Host:   spec.S3.Bucket,
			Path:   "/" + spec.S3.Prefix,
		}
		if spec.S3.Region != "" {
			q := u.Query()
			q.Set("region", spec.S3.Region)
			u.RawQuery = q.Encode()
		}
		return u.String()
	case kubeswarmv1alpha1.ArtifactStoreGCS:
		if spec.GCS == nil || spec.GCS.Bucket == "" {
			return ""
		}
		return (&url.URL{
			Scheme: "gcs",
			Host:   spec.GCS.Bucket,
			Path:   "/" + spec.GCS.Prefix,
		}).String()
	}
	return ""
}

// artifactStoreClaim returns the PVC claim name for local artifact stores, or empty string.
func artifactStoreClaim(spec *kubeswarmv1alpha1.ArtifactStoreSpec) string {
	if spec == nil || spec.Type != kubeswarmv1alpha1.ArtifactStoreLocal {
		return ""
	}
	if spec.Local == nil {
		return ""
	}
	return spec.Local.ClaimName
}

const swarmEventTargetTeamIndex = ".spec.targets.team"

// isTriggerTemplate returns true when this team is referenced as a target by any SwarmEvent.
func (r *SwarmTeamReconciler) isTriggerTemplate(ctx context.Context, team *kubeswarmv1alpha1.SwarmTeam) (bool, error) {
	var events kubeswarmv1alpha1.SwarmEventList
	if err := r.List(ctx, &events,
		client.InNamespace(team.Namespace),
		client.MatchingFields{swarmEventTargetTeamIndex: team.Name},
	); err == nil {
		return len(events.Items) > 0, nil
	}
	// Index not available — full list scan fallback.
	var all kubeswarmv1alpha1.SwarmEventList
	if err := r.List(ctx, &all, client.InNamespace(team.Namespace)); err != nil {
		return false, err
	}
	for _, ev := range all.Items {
		for _, target := range ev.Spec.Targets {
			if target.Team == team.Name {
				return true, nil
			}
		}
	}
	return false, nil
}

func (r *SwarmTeamReconciler) setCondition(
	team *kubeswarmv1alpha1.SwarmTeam,
	status metav1.ConditionStatus,
	reason, message string,
) {
	apimeta.SetStatusCondition(&team.Status.Conditions, metav1.Condition{
		Type:               kubeswarmv1alpha1.ConditionReady,
		Status:             status,
		ObservedGeneration: team.Generation,
		Reason:             reason,
		Message:            message,
	})
}

// setPromptSizeWarning sets or clears the SystemPromptSizeWarning condition based on the
// total size of inline prompts across all roles. A warning is surfaced when the
// combined inline prompt bytes exceed 50 KB. Users should migrate to prompt.from to
// avoid approaching etcd's per-object size limit.
func (r *SwarmTeamReconciler) setPromptSizeWarning(team *kubeswarmv1alpha1.SwarmTeam) {
	const warnBytes = 50 * 1024 // 50 KB - mirrors webhook.promptWarnBytes
	var totalBytes int
	for _, role := range team.Spec.Roles {
		if role.Prompt != nil {
			totalBytes += len(role.Prompt.Inline)
		}
	}
	if totalBytes > warnBytes {
		apimeta.SetStatusCondition(&team.Status.Conditions, metav1.Condition{
			Type:               "SystemPromptSizeWarning",
			Status:             metav1.ConditionTrue,
			ObservedGeneration: team.Generation,
			Reason:             "InlinePromptTooLarge",
			Message: fmt.Sprintf(
				"total inline prompt size is %d KB (> 50 KB). "+
					"Move prompts to ConfigMaps and reference them via spec.roles[*].prompt.from.configMapKeyRef "+
					"to avoid etcd object size limits.",
				totalBytes/1024,
			),
		})
	} else {
		apimeta.RemoveStatusCondition(&team.Status.Conditions, "SystemPromptSizeWarning")
	}
}

// pruneRuns enforces the SwarmTeam's run retention policy by deleting completed SwarmRuns
// that exceed the configured history limits or age threshold.
// Running and Pending runs are never deleted.
func (r *SwarmTeamReconciler) pruneRuns(ctx context.Context, team *kubeswarmv1alpha1.SwarmTeam, runs []kubeswarmv1alpha1.SwarmRun) error {
	successLimit := defaultSuccessfulRunsLimit
	if team.Spec.SuccessfulRunsHistoryLimit != nil {
		successLimit = *team.Spec.SuccessfulRunsHistoryLimit
	}
	failedLimit := defaultFailedRunsLimit
	if team.Spec.FailedRunsHistoryLimit != nil {
		failedLimit = *team.Spec.FailedRunsHistoryLimit
	}

	now := time.Now()
	var toDelete []kubeswarmv1alpha1.SwarmRun
	var succeeded []kubeswarmv1alpha1.SwarmRun
	var failed []kubeswarmv1alpha1.SwarmRun

	for _, run := range runs {
		phase := run.Status.Phase
		if phase != kubeswarmv1alpha1.SwarmRunPhaseSucceeded && phase != kubeswarmv1alpha1.SwarmRunPhaseFailed {
			continue // never delete Running or Pending runs
		}
		// Age-based pruning: use CompletionTime so we measure how long the run has been done.
		if team.Spec.RunRetainFor != nil && team.Spec.RunRetainFor.Duration > 0 {
			completedAt := run.CreationTimestamp.Time
			if run.Status.CompletionTime != nil {
				completedAt = run.Status.CompletionTime.Time
			}
			if now.Sub(completedAt) > team.Spec.RunRetainFor.Duration {
				runCopy := run
				toDelete = append(toDelete, runCopy)
				continue
			}
		}
		runCopy := run
		if phase == kubeswarmv1alpha1.SwarmRunPhaseSucceeded {
			succeeded = append(succeeded, runCopy)
		} else {
			failed = append(failed, runCopy)
		}
	}

	// Sort newest-first so we keep the most recent N runs.
	sortRunsNewestFirst(succeeded)
	sortRunsNewestFirst(failed)

	if int32(len(succeeded)) > successLimit {
		toDelete = append(toDelete, succeeded[successLimit:]...)
	}
	if int32(len(failed)) > failedLimit {
		toDelete = append(toDelete, failed[failedLimit:]...)
	}

	for i := range toDelete {
		if err := r.Delete(ctx, &toDelete[i]); client.IgnoreNotFound(err) != nil {
			return fmt.Errorf("deleting SwarmRun %q: %w", toDelete[i].Name, err)
		}
	}
	return nil
}

func sortRunsNewestFirst(runs []kubeswarmv1alpha1.SwarmRun) {
	sort.Slice(runs, func(i, j int) bool {
		ti := runs[i].CreationTimestamp.Time
		if runs[i].Status.CompletionTime != nil {
			ti = runs[i].Status.CompletionTime.Time
		}
		tj := runs[j].CreationTimestamp.Time
		if runs[j].Status.CompletionTime != nil {
			tj = runs[j].Status.CompletionTime.Time
		}
		return ti.After(tj)
	})
}

// reconcileAutoscaling computes desired replica counts for inline roles based on active
// pipeline steps and scale-to-zero idle timeout. It is a no-op when spec.autoscaling
// is not enabled. Returns a non-zero RequeueAfter when an idle countdown is in progress.
func (r *SwarmTeamReconciler) reconcileAutoscaling(
	ctx context.Context,
	team *kubeswarmv1alpha1.SwarmTeam,
	runs []kubeswarmv1alpha1.SwarmRun,
) (ctrl.Result, error) {
	if team.Spec.Autoscaling == nil || !team.Spec.Autoscaling.Enabled {
		return ctrl.Result{}, nil
	}

	now := metav1.Now()
	activeByRole := countActiveStepsByRole(runs)
	if len(activeByRole) > 0 {
		team.Status.LastActiveTime = &now
	}

	scaleToZero := team.Spec.Autoscaling.ScaleToZero != nil && team.Spec.Autoscaling.ScaleToZero.Enabled
	afterSeconds := int32(300)
	if scaleToZero && team.Spec.Autoscaling.ScaleToZero.AfterSeconds != nil {
		afterSeconds = *team.Spec.Autoscaling.ScaleToZero.AfterSeconds
	}

	idleFor := time.Duration(0)
	if team.Status.LastActiveTime != nil {
		idleFor = now.Sub(team.Status.LastActiveTime.Time)
	}

	allZero := true
	for _, role := range team.Spec.Roles {
		if role.SwarmAgent != "" || role.SwarmTeam != "" {
			allZero = false
			continue // only manage inline roles
		}
		desired, skip, err := r.computeRoleDesired(ctx, team, role, activeByRole, scaleToZero, afterSeconds, idleFor)
		if err != nil {
			return ctrl.Result{}, err
		}
		if skip {
			allZero = false
			continue
		}
		if desired > 0 {
			allZero = false
		}
		if err := r.applyRoleScale(ctx, team, role, desired, int32(activeByRole[role.Name])); err != nil {
			return ctrl.Result{}, err
		}
	}

	team.Status.ScaledToZero = scaleToZero && allZero

	// Schedule a requeue for when the idle countdown expires.
	if scaleToZero && len(activeByRole) == 0 && !team.Status.ScaledToZero {
		remaining := time.Duration(afterSeconds)*time.Second - idleFor
		if remaining > 0 {
			return ctrl.Result{RequeueAfter: remaining + time.Second}, nil
		}
	}

	return ctrl.Result{}, nil
}

// computeRoleDesired fetches the managed SwarmAgent for one inline role and returns the
// desired replica count. Returns skip=true when the role should not be touched
// (KEDA-managed or agent not found yet).
func (r *SwarmTeamReconciler) computeRoleDesired(
	ctx context.Context,
	team *kubeswarmv1alpha1.SwarmTeam,
	role kubeswarmv1alpha1.SwarmTeamRole,
	activeByRole map[string]int,
	scaleToZero bool,
	afterSeconds int32,
	idleFor time.Duration,
) (desired int32, skip bool, err error) {
	agentName := team.Name + "-" + role.Name
	agent := &kubeswarmv1alpha1.SwarmAgent{}
	if err = r.Get(ctx, client.ObjectKey{Name: agentName, Namespace: team.Namespace}, agent); err != nil {
		if errors.IsNotFound(err) {
			return 0, true, nil
		}
		return 0, false, fmt.Errorf("fetching SwarmAgent %q for autoscaling: %w", agentName, err)
	}
	if agent.Spec.Runtime.Autoscaling != nil {
		return 0, true, nil // KEDA owns this agent's replicas
	}

	configuredReplicas := int32(1)
	if role.Runtime != nil && role.Runtime.Replicas != nil {
		configuredReplicas = *role.Runtime.Replicas
	}
	maxConcurrent := int32(5)
	if role.Limits != nil && role.Limits.ConcurrentTasks > 0 {
		maxConcurrent = int32(role.Limits.ConcurrentTasks)
	}

	activeSteps := int32(activeByRole[role.Name])
	switch {
	case activeSteps > 0:
		// ceil(activeSteps / maxConcurrent), capped at configured max.
		desired = min((activeSteps+maxConcurrent-1)/maxConcurrent, configuredReplicas)
	case scaleToZero && idleFor >= time.Duration(afterSeconds)*time.Second:
		desired = 0
	default:
		desired = configuredReplicas
	}
	return desired, false, nil
}

// applyRoleScale patches SwarmAgent spec.replicas and status.desiredReplicas when the
// computed desired count differs from the current value.
func (r *SwarmTeamReconciler) applyRoleScale(
	ctx context.Context,
	team *kubeswarmv1alpha1.SwarmTeam,
	role kubeswarmv1alpha1.SwarmTeamRole,
	desired, activeSteps int32,
) error {
	logger := log.FromContext(ctx)
	agentName := team.Name + "-" + role.Name

	agent := &kubeswarmv1alpha1.SwarmAgent{}
	if err := r.Get(ctx, client.ObjectKey{Name: agentName, Namespace: team.Namespace}, agent); err != nil {
		return client.IgnoreNotFound(err)
	}

	currentReplicas := int32(1)
	if agent.Spec.Runtime.Replicas != nil {
		currentReplicas = *agent.Spec.Runtime.Replicas
	}

	if currentReplicas != desired {
		patch := client.MergeFrom(agent.DeepCopy())
		agent.Spec.Runtime.Replicas = &desired
		if err := r.Patch(ctx, agent, patch); err != nil {
			return fmt.Errorf("autoscaling SwarmAgent %q to %d replicas: %w", agentName, desired, err)
		}
		logger.Info("autoscaled agent", "agent", agentName, "from", currentReplicas, "to", desired, "activeSteps", activeSteps)
		if r.Recorder != nil {
			r.Recorder.Eventf(team, corev1.EventTypeNormal, "AutoscaleAgent",
				"scaled %s from %d to %d replicas (active steps: %d)", agentName, currentReplicas, desired, activeSteps)
		}
	}

	// Refresh after potential spec patch so status patch uses the latest resourceVersion.
	if agent.Status.DesiredReplicas == nil || *agent.Status.DesiredReplicas != desired {
		fresh := &kubeswarmv1alpha1.SwarmAgent{}
		if err := r.Get(ctx, client.ObjectKey{Name: agentName, Namespace: team.Namespace}, fresh); err == nil {
			statusPatch := client.MergeFrom(fresh.DeepCopy())
			fresh.Status.DesiredReplicas = &desired
			if err := r.Status().Patch(ctx, fresh, statusPatch); err != nil {
				logger.Error(err, "updating SwarmAgent status.desiredReplicas", "agent", agentName)
			}
		}
	}
	return nil
}

// countActiveStepsByRole counts Running, WarmingUp, and Validating steps per role name
// across all provided SwarmRuns. Only these phases count as "active" for autoscaling.
func countActiveStepsByRole(runs []kubeswarmv1alpha1.SwarmRun) map[string]int {
	counts := make(map[string]int)
	for i := range runs {
		for _, step := range runs[i].Status.Steps {
			switch step.Phase {
			case kubeswarmv1alpha1.SwarmFlowStepPhaseRunning,
				kubeswarmv1alpha1.SwarmFlowStepPhaseWarmingUp,
				kubeswarmv1alpha1.SwarmFlowStepPhaseValidating:
				counts[step.Name]++
			}
		}
	}
	return counts
}

// SetupWithManager sets up the controller with the Manager.
func (r *SwarmTeamReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if err := mgr.GetFieldIndexer().IndexField(
		context.Background(),
		&kubeswarmv1alpha1.SwarmEvent{},
		swarmEventTargetTeamIndex,
		func(rawObj client.Object) []string {
			event := rawObj.(*kubeswarmv1alpha1.SwarmEvent)
			names := make([]string, 0, len(event.Spec.Targets))
			for _, t := range event.Spec.Targets {
				names = append(names, t.Team)
			}
			return names
		},
	); err != nil {
		return fmt.Errorf("registering SwarmEvent targets index: %w", err)
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&kubeswarmv1alpha1.SwarmTeam{}).
		Owns(&kubeswarmv1alpha1.SwarmAgent{}).
		// Requeue the team when one of its SwarmRuns changes phase (so LastRunName/LastRunPhase mirror updates quickly).
		Watches(&kubeswarmv1alpha1.SwarmRun{}, handler.EnqueueRequestsFromMapFunc(
			func(_ context.Context, obj client.Object) []reconcile.Request {
				teamName := obj.GetLabels()["kubeswarm/team"]
				if teamName == "" {
					return nil
				}
				return []reconcile.Request{{
					NamespacedName: types.NamespacedName{
						Namespace: obj.GetNamespace(),
						Name:      teamName,
					},
				}}
			},
		)).
		// Requeue referenced teams when an SwarmEvent is created/updated/deleted.
		Watches(&kubeswarmv1alpha1.SwarmEvent{}, handler.EnqueueRequestsFromMapFunc(
			func(ctx context.Context, obj client.Object) []reconcile.Request {
				event := obj.(*kubeswarmv1alpha1.SwarmEvent)
				reqs := make([]reconcile.Request, 0, len(event.Spec.Targets))
				for _, t := range event.Spec.Targets {
					reqs = append(reqs, reconcile.Request{
						NamespacedName: types.NamespacedName{
							Namespace: event.Namespace,
							Name:      t.Team,
						},
					})
				}
				return reqs
			},
		)).
		Named("swarmteam").
		Complete(WithMetrics(r, "swarmteam"))
}
