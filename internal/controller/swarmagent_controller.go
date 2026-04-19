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
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	neturl "net/url"
	"os"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kubeswarmv1alpha1 "github.com/kubeswarm/kubeswarm/api/v1alpha1"
	"github.com/kubeswarm/kubeswarm/pkg/agent/providers"
	pkgflow "github.com/kubeswarm/kubeswarm/pkg/flow"
)

const (
	// agentAPIKeysSecret is the k8s Secret expected to contain ANTHROPIC_API_KEY
	// and TASK_QUEUE_URL, injected via EnvFrom into every agent pod.
	agentAPIKeysSecret = "kubeswarm-api-keys" //nolint:gosec // this is a Secret name, not a credential value

	// agentServiceAccount is the ServiceAccount assigned to every agent pod.
	// The SwarmAgentReconciler creates it (+ Role + RoleBinding) when first reconciling
	// an SwarmAgent in a namespace so agent pods can emit K8s Events for audit logging.
	agentServiceAccount = "swarm-agent"

	// MCP server auth types used when building runtime config and volumes.
	mcpAuthBearer = "bearer"
	mcpAuthMTLS   = "mtls"

	// Default resource constraints injected into agent pods when spec.resources is not set.
	// These ensure every agent pod has explicit limits, preventing a runaway agent from
	// consuming unbounded node resources (RFC-0016 Phase 1).
)

// Default resource quantities parsed once at startup.
var (
	defaultCPURequestQty            = resource.MustParse("100m")
	defaultCPULimitQty              = resource.MustParse("500m")
	defaultMemoryRequestQty         = resource.MustParse("128Mi")
	defaultMemoryLimitQty           = resource.MustParse("512Mi")
	defaultEphemeralStorageLimitQty = resource.MustParse("256Mi")
)

// defaultAgentResources returns the safe default resource requirements injected into
// agent pods when spec.resources is not explicitly set.
func defaultAgentResources() corev1.ResourceRequirements {
	return corev1.ResourceRequirements{
		Requests: corev1.ResourceList{
			corev1.ResourceCPU:    defaultCPURequestQty.DeepCopy(),
			corev1.ResourceMemory: defaultMemoryRequestQty.DeepCopy(),
		},
		Limits: corev1.ResourceList{
			corev1.ResourceCPU:              defaultCPULimitQty.DeepCopy(),
			corev1.ResourceMemory:           defaultMemoryLimitQty.DeepCopy(),
			corev1.ResourceEphemeralStorage: defaultEphemeralStorageLimitQty.DeepCopy(),
		},
	}
}

// agentResources returns the resource requirements for an agent pod.
// Uses spec.runtime.resources when set, falling back to safe defaults. The ephemeral-storage
// limit is always injected - even into custom resource specs - to prevent /tmp exhaustion
// on a readOnlyRootFilesystem pod where /tmp is the only writable path.
func agentResources(swarmAgent *kubeswarmv1alpha1.SwarmAgent) corev1.ResourceRequirements {
	if swarmAgent.Spec.Runtime.Resources == nil {
		return defaultAgentResources()
	}
	r := *swarmAgent.Spec.Runtime.Resources
	if r.Limits == nil {
		r.Limits = corev1.ResourceList{}
	}
	if _, ok := r.Limits[corev1.ResourceEphemeralStorage]; !ok {
		r.Limits[corev1.ResourceEphemeralStorage] = defaultEphemeralStorageLimitQty.DeepCopy()
	}
	return r
}

// SwarmAgentReconciler reconciles a SwarmAgent object
type SwarmAgentReconciler struct {
	client.Client
	Scheme                *runtime.Scheme
	AgentImage            string
	AgentImagePullPolicy  corev1.PullPolicy
	AgentImagePullSecrets []corev1.LocalObjectReference
	// MCPGatewayURL is the base URL of the MCP gateway, e.g.
	// "http://kubeswarm-mcp-gateway.kubeswarm-system.svc:8082". When set, swarmAgentRef entries
	// in spec.mcpServers are resolved to gateway URLs at reconcile time.
	// When empty, agents with swarmAgentRef entries will fail reconciliation with
	// MCPResolutionError until the gateway is configured.
	MCPGatewayURL string
	// OperatorNamespace is the namespace the operator pod runs in (POD_NAMESPACE).
	// Used to scope the Redis egress rule in generated NetworkPolicies to the correct
	// namespace regardless of how the operator is deployed.
	OperatorNamespace string

	// registryEnsured tracks namespaces where ensureDefaultRegistry has already succeeded,
	// avoiding a Get on every reconcile for the common steady-state path.
	registryEnsured sync.Map // map[string]struct{}
	// saEnsured tracks namespaces where SA/Role/RoleBinding already exist,
	// avoiding 3 Gets every reconcile once they're confirmed present.
	saEnsured sync.Map // map[string]struct{}
	// NotifyDispatcher dispatches notifications for agent-level events (e.g. AgentDegraded).
	// When nil, agent notifications are disabled.
	NotifyDispatcher *NotifyDispatcher
}

// +kubebuilder:rbac:groups=kubeswarm.io,resources=swarmagents,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=kubeswarm.io,resources=swarmagents/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=kubeswarm.io,resources=swarmagents/finalizers,verbs=update
// +kubebuilder:rbac:groups=networking.k8s.io,resources=networkpolicies,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=kubeswarm.io,resources=swarmevents,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=kubeswarm.io,resources=swarmevents/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=kubeswarm.io,resources=swarmevents/finalizers,verbs=update
// +kubebuilder:rbac:groups=kubeswarm.io,resources=swarmmemories,verbs=get;list;watch
// +kubebuilder:rbac:groups=kubeswarm.io,resources=swarmsettings,verbs=get;list;watch
// +kubebuilder:rbac:groups=kubeswarm.io,resources=swarmregistries,verbs=get;list;watch;create
// +kubebuilder:rbac:groups=kubeswarm.io,resources=swarmruns,verbs=get;list;watch
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=pods,verbs=get;list;watch
// +kubebuilder:rbac:groups=core,resources=configmaps,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups=core,resources=secrets,verbs=get;list;watch
// +kubebuilder:rbac:groups=core,resources=events,verbs=create;patch
// +kubebuilder:rbac:groups=core,resources=serviceaccounts,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=roles;rolebindings,verbs=get;list;watch;create;update;patch

func (r *SwarmAgentReconciler) Reconcile(ctx context.Context, req ctrl.Request) (result ctrl.Result, retErr error) {
	logger := log.FromContext(ctx)

	// 1. Fetch the SwarmAgent CR.
	swarmAgent := &kubeswarmv1alpha1.SwarmAgent{}
	if err := r.Get(ctx, req.NamespacedName, swarmAgent); err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// 2. OwnerRef handles child cleanup on deletion - nothing extra needed.
	if !swarmAgent.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, nil
	}

	// Single deferred status write: persists all in-memory status mutations
	// (conditions, toolConnections, advisorConnections, etc.) on every exit
	// path. This replaces scattered Status().Update calls that were prone to
	// missed writes when new steps were added.
	defer func() {
		if err := r.Status().Update(ctx, swarmAgent); err != nil {
			if retErr == nil {
				retErr = fmt.Errorf("status update: %w", err)
			} else {
				logger.Error(err, "failed to persist status on error path")
			}
		}
	}()

	// 2a. Ensure a "default" SwarmRegistry exists in this namespace.
	// Non-blocking: a creation error is logged but does not prevent agent reconciliation.
	if err := r.ensureDefaultRegistry(ctx, req.Namespace); err != nil {
		logger.Error(err, "failed to ensure default SwarmRegistry")
	}

	// 3. Load all referenced SwarmSettings (settingsRefs takes precedence over deprecated configRef).
	allSettings, err := r.loadSettingsRefs(ctx, swarmAgent)
	if err != nil {
		return ctrl.Result{}, err
	}

	// 3a. Record which settings were applied and how many fragments were composed.
	settingsNames := make([]string, len(allSettings))
	totalFragments := 0
	for i, s := range allSettings {
		settingsNames[i] = s.Name
		totalFragments += len(s.Spec.Fragments)
	}
	swarmAgent.Status.AppliedSettings = settingsNames
	swarmAgent.Status.AppliedFragmentCount = totalFragments

	// 3a-bis. Compute effective reasoning config from the SwarmSettings cascade
	// and set the ReasoningActive status condition per RFC-0033 DD7. This runs
	// on every reconcile and before deployment logic so the condition is always
	// populated even when later steps short-circuit.
	r.applyReasoningCondition(swarmAgent, allSettings)

	// 3b. Optionally load the referenced SwarmMemory.
	swarmMemory, err := r.loadSwarmMemory(ctx, swarmAgent)
	if err != nil {
		return ctrl.Result{}, err
	}

	// 3c. Resolve the effective system prompt (inline or from ConfigMap/Secret).
	resolvedPrompt, err := r.resolveSystemPrompt(ctx, swarmAgent)
	if err != nil {
		logger.Error(err, "failed to resolve systemPrompt")
		setCondition(&swarmAgent.Status.Conditions, swarmAgent.Generation, kubeswarmv1alpha1.ConditionReady, metav1.ConditionFalse, "PromptResolutionError", err.Error())
		return ctrl.Result{}, err
	}

	// Record the hash of the resolved prompt in status so the admission webhook can
	// detect and block unauthorised system prompt changes (RFC-0016 Phase 4).
	swarmAgent.Status.SystemPromptHash = hashPrompt(resolvedPrompt)

	// 3d. Resolve the API key from SwarmSecret (if configured).
	apiKeyEnvVar, apiKeyVersion, err := r.resolveAPIKeyEnvVar(ctx, swarmAgent)
	if err != nil {
		logger.Error(err, "failed to resolve apiKeyRef")
		setCondition(&swarmAgent.Status.Conditions, swarmAgent.Generation, kubeswarmv1alpha1.ConditionReady, metav1.ConditionFalse, "APIKeyResolutionError", err.Error())
		return ctrl.Result{}, err
	}

	// 3e. Resolve capabilityRef entries in MCPServers via the namespace's SwarmRegistry.
	resolvedMCPServers, err := r.resolveMCPServers(ctx, swarmAgent)
	if err != nil {
		logger.Error(err, "failed to resolve MCP capabilityRefs")
		setCondition(&swarmAgent.Status.Conditions, swarmAgent.Generation, kubeswarmv1alpha1.ConditionReady, metav1.ConditionFalse, "MCPResolutionError", err.Error())
		return ctrl.Result{}, err
	}

	// 3f. Validate registryRef resolves; emit RegistryNotFound condition when absent.
	// Non-blocking - the agent starts regardless of registry state.
	r.reconcileRegistryRef(ctx, swarmAgent)

	// 4. Calculate rolling 24h token usage and enforce daily budget.
	requeueAfter, err := r.reconcileDailyBudget(ctx, swarmAgent)
	if err != nil {
		return ctrl.Result{}, err
	}

	// 4b. Enforce SwarmBudget referenced by spec.budgetRef (hard stop when exceeded).
	if err := r.reconcileBudgetRef(ctx, swarmAgent); err != nil {
		logger.Error(err, "failed to reconcile budgetRef")
	}

	// 5. Ensure the agent ServiceAccount (+ Role + RoleBinding) exists in this namespace
	// so agent pods can emit K8s Events for audit logging.
	if err := r.reconcileAgentServiceAccount(ctx, swarmAgent); err != nil {
		logger.Error(err, "failed to reconcile agent ServiceAccount")
		r.setCondition(swarmAgent, kubeswarmv1alpha1.ConditionReady, metav1.ConditionFalse, "ReconcileError", err.Error())
		return ctrl.Result{}, err
	}

	// 5b. Reconcile the prompt ConfigMap so the system prompt is mounted as a file
	// instead of injected as an env var (avoids size limits and plaintext exposure).
	assembledPrompt := assembleSystemPrompt(resolvedPrompt, allSettings, resolvedMCPServers)
	if err := r.reconcilePromptConfigMap(ctx, swarmAgent, assembledPrompt); err != nil {
		logger.Error(err, "failed to reconcile prompt ConfigMap")
		r.setCondition(swarmAgent, kubeswarmv1alpha1.ConditionReady, metav1.ConditionFalse, "ReconcileError", err.Error())
		return ctrl.Result{}, err
	}

	// 5c. Fetch effective SwarmPolicy for this namespace (RFC-0049 Phase 5).
	// Non-blocking: if policies can't be listed, the agent runs with its own limits.
	effectivePolicy := r.fetchEffectivePolicy(ctx, req.Namespace)

	// 6. Reconcile the owned k8s Deployment (budget check may override replicas to 0).
	// Gateway capabilities are resolved inside reconcileDeployment so transient
	// registry-lookup errors flow through the existing deployment-error branch
	// rather than adding a separate one.
	if err := r.reconcileDeployment(ctx, deploymentInput{
		swarmAgent:         swarmAgent,
		allSettings:        allSettings,
		swarmMemory:        swarmMemory,
		assembledPrompt:    assembledPrompt,
		apiKeyEnvVar:       apiKeyEnvVar,
		apiKeyVersion:      apiKeyVersion,
		resolvedMCPServers: resolvedMCPServers,
		effectivePolicy:    effectivePolicy,
	}); err != nil {
		logger.Error(err, "failed to reconcile Deployment")
		r.setCondition(swarmAgent, kubeswarmv1alpha1.ConditionReady, metav1.ConditionFalse, "ReconcileError", err.Error())
		return ctrl.Result{}, err
	}

	// 7. Reconcile the NetworkPolicy for this agent (RFC-0016 Phase 2).
	if err := r.reconcileNetworkPolicy(ctx, swarmAgent, resolvedMCPServers); err != nil {
		logger.Error(err, "failed to reconcile NetworkPolicy")
		r.setCondition(swarmAgent, kubeswarmv1alpha1.ConditionReady, metav1.ConditionFalse, "NetworkPolicyError", err.Error())
		return ctrl.Result{}, err
	}

	// 7b. Strict NetworkPolicy bakes DNS-resolved IPs at reconcile time. Force periodic
	// re-resolution so the policy stays current when MCP server IPs rotate.
	if swarmAgent.Spec.Infrastructure != nil &&
		swarmAgent.Spec.Infrastructure.NetworkPolicy == kubeswarmv1alpha1.NetworkPolicyModeStrict {
		const strictRequeue = 5 * time.Minute
		if requeueAfter == 0 || strictRequeue < requeueAfter {
			requeueAfter = strictRequeue
		}
	}

	// 8. Sync status.readyReplicas from the owned Deployment.
	if err := r.syncStatus(ctx, swarmAgent); err != nil {
		return ctrl.Result{}, err
	}

	// 9. Probe MCP server health and surface results in status.toolConnections[].
	mcpRequeue := r.reconcileMCPHealth(swarmAgent, resolvedMCPServers)
	if mcpRequeue > 0 && (requeueAfter == 0 || mcpRequeue < requeueAfter) {
		requeueAfter = mcpRequeue
	}

	// 9b. Reconcile advisor connections: check targets, set status and condition (RFC-0048).
	advisorStatuses, advisorCondition := reconcileAdvisorConnections(ctx, r.Client, swarmAgent)
	swarmAgent.Status.AdvisorConnections = advisorStatuses
	setCondition(&swarmAgent.Status.Conditions, swarmAgent.Generation, kubeswarmv1alpha1.ConditionAdvisorsReady, advisorCondition.Status, advisorCondition.Reason, advisorCondition.Message)

	// 9c. Reconcile tool-role agent connections: check targets, set status.
	swarmAgent.Status.ToolAgentConnections = reconcileToolAgentConnections(ctx, r.Client, swarmAgent)

	// 9d. Surface dedup config in status.
	swarmAgent.Status.DedupEnabled = swarmAgent.Spec.Runtime.Loop != nil && swarmAgent.Spec.Runtime.Loop.Dedup

	// 10. Reconcile KEDA ScaledObject when autoscaling is configured.
	if err := r.reconcileKEDA(ctx, swarmAgent); err != nil {
		logger.Error(err, "failed to reconcile KEDA ScaledObject")
	}

	return ctrl.Result{RequeueAfter: requeueAfter}, nil
}

// loadSwarmMemory loads the SwarmMemory referenced by spec.runtime.loop.memory.ref.
// Returns nil, nil when no memory is configured or the referenced object is not found.
func (r *SwarmAgentReconciler) loadSwarmMemory(
	ctx context.Context,
	agent *kubeswarmv1alpha1.SwarmAgent,
) (*kubeswarmv1alpha1.SwarmMemory, error) {
	if agent.Spec.Runtime.Loop == nil ||
		agent.Spec.Runtime.Loop.Memory == nil || agent.Spec.Runtime.Loop.Memory.Ref == nil {
		return nil, nil
	}
	mem := &kubeswarmv1alpha1.SwarmMemory{}
	if err := r.Get(ctx, client.ObjectKey{
		Name:      agent.Spec.Runtime.Loop.Memory.Ref.Name,
		Namespace: agent.Namespace,
	}, mem); err != nil {
		if errors.IsNotFound(err) {
			log.FromContext(ctx).Info("SwarmMemory not found, proceeding without it",
				"memoryRef", agent.Spec.Runtime.Loop.Memory.Ref.Name)
			return nil, nil
		}
		return nil, fmt.Errorf("fetching SwarmMemory %q: %w", agent.Spec.Runtime.Loop.Memory.Ref.Name, err)
	}
	return mem, nil
}

// fetchEffectivePolicy reads the pre-merged effective policy from the first
// SwarmPolicy's status (computed by SwarmPolicyReconciler). Falls back to
// merging on the fly if no status is populated yet. Returns nil if no
// policies exist or if listing fails (non-blocking).
func (r *SwarmAgentReconciler) fetchEffectivePolicy(ctx context.Context, namespace string) *kubeswarmv1alpha1.EffectivePolicySpec {
	var policyList kubeswarmv1alpha1.SwarmPolicyList
	if err := r.List(ctx, &policyList, client.InNamespace(namespace)); err != nil {
		log.FromContext(ctx).Error(err, "failed to list SwarmPolicies for effective guardrails")
		return nil
	}
	if len(policyList.Items) == 0 {
		return nil
	}
	// Prefer the pre-computed effective policy from status (avoids re-merging).
	for i := range policyList.Items {
		if ep := policyList.Items[i].Status.EffectivePolicy; ep != nil {
			return ep
		}
	}
	// Fallback: policy reconciler hasn't run yet - merge on the fly.
	ep, _ := MergePolicies(policyList.Items)
	return ep
}

// deploymentInput groups all inputs needed to build or reconcile the agent Deployment.
type deploymentInput struct {
	swarmAgent          *kubeswarmv1alpha1.SwarmAgent
	allSettings         []kubeswarmv1alpha1.SwarmSettings
	swarmMemory         *kubeswarmv1alpha1.SwarmMemory
	assembledPrompt     string
	apiKeyEnvVar        *corev1.EnvVar
	apiKeyVersion       string
	resolvedMCPServers  []kubeswarmv1alpha1.MCPToolSpec
	effectivePolicy     *kubeswarmv1alpha1.EffectivePolicySpec
	gatewayCapabilities []GatewayCapabilityEntry // RFC-0052, populated inside reconcileDeployment
}

func (r *SwarmAgentReconciler) reconcileDeployment(ctx context.Context, in deploymentInput) error {
	// Resolve gateway capabilities before building the Deployment so transient
	// registry-lookup errors requeue instead of leaving the pod with a stale or
	// misleading AGENT_GATEWAY_CAPABILITIES env var.
	caps, err := r.resolveGatewayCapsForReconcile(ctx, in.swarmAgent)
	if err != nil {
		return err
	}
	in.gatewayCapabilities = caps

	// Update gateway status fields so operators can observe capability counts.
	if in.swarmAgent.Spec.Gateway != nil {
		filtered := filterCapabilities(in.swarmAgent.Spec.Gateway, caps)
		now := metav1.Now()
		in.swarmAgent.Status.Gateway = &kubeswarmv1alpha1.GatewayStatus{
			RoutableCapabilities:      int32(len(filtered)),
			TotalMatchingCapabilities: int32(len(caps)),
			LastCapabilitySync:        &now,
		}
	}

	desired := r.buildDeployment(in)

	if err := ctrl.SetControllerReference(in.swarmAgent, desired, r.Scheme); err != nil {
		return err
	}

	existing := &appsv1.Deployment{}
	err = r.Get(ctx, client.ObjectKeyFromObject(desired), existing)
	if errors.IsNotFound(err) {
		return r.Create(ctx, desired)
	}
	if err != nil {
		return err
	}

	original := existing.DeepCopy()
	patch := client.MergeFrom(original)
	existing.Spec.Replicas = desired.Spec.Replicas
	existing.Spec.Template.Annotations = desired.Spec.Template.Annotations
	// Only update the fields that the operator controls - env vars, image, and pull policy.
	// Replacing the entire Containers slice strips k8s-defaulted fields (terminationMessagePath,
	// terminationMessagePolicy, etc.) which k8s re-adds on every GET, causing an infinite
	// generation-bump loop that triggers continuous rolling updates.
	if len(existing.Spec.Template.Spec.Containers) > 0 && len(desired.Spec.Template.Spec.Containers) > 0 {
		existing.Spec.Template.Spec.Containers[0].Image = desired.Spec.Template.Spec.Containers[0].Image
		existing.Spec.Template.Spec.Containers[0].ImagePullPolicy = desired.Spec.Template.Spec.Containers[0].ImagePullPolicy
		existing.Spec.Template.Spec.Containers[0].Env = desired.Spec.Template.Spec.Containers[0].Env
		existing.Spec.Template.Spec.Containers[0].EnvFrom = desired.Spec.Template.Spec.Containers[0].EnvFrom
		existing.Spec.Template.Spec.Containers[0].Resources = desired.Spec.Template.Spec.Containers[0].Resources
		existing.Spec.Template.Spec.Containers[0].LivenessProbe = desired.Spec.Template.Spec.Containers[0].LivenessProbe
		existing.Spec.Template.Spec.Containers[0].ReadinessProbe = desired.Spec.Template.Spec.Containers[0].ReadinessProbe
		// mTLS volume mounts change when MCP servers are added/removed/reconfigured.
		existing.Spec.Template.Spec.Containers[0].VolumeMounts = desired.Spec.Template.Spec.Containers[0].VolumeMounts
	} else {
		existing.Spec.Template.Spec.Containers = desired.Spec.Template.Spec.Containers
	}
	// mTLS volumes change when MCP servers are added/removed/reconfigured.
	existing.Spec.Template.Spec.Volumes = desired.Spec.Template.Spec.Volumes
	existing.Spec.Template.Spec.ImagePullSecrets = desired.Spec.Template.Spec.ImagePullSecrets

	// Guard: skip the patch if nothing changed. Compare against the original
	// snapshot (before mutation) so that env-var-only changes are detected.
	if reflect.DeepEqual(original.Spec, existing.Spec) {
		return nil
	}

	return r.Patch(ctx, existing, patch)
}

const (
	promptConfigMapKey = "system-prompt.txt"
	promptVolumeName   = "system-prompt"
	promptMountPath    = "/etc/swarm/prompt"
	promptFilePath     = promptMountPath + "/" + promptConfigMapKey
)

// reconcilePromptConfigMap creates or updates a ConfigMap containing the assembled system prompt.
// The prompt is mounted as a file into agent pods instead of being injected as an env var,
// avoiding env var size limits and plaintext exposure in kubectl describe.
func (r *SwarmAgentReconciler) reconcilePromptConfigMap(
	ctx context.Context,
	swarmAgent *kubeswarmv1alpha1.SwarmAgent,
	assembledPrompt string,
) error {
	cmName := swarmAgent.Name + "-prompt"
	desired := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      cmName,
			Namespace: swarmAgent.Namespace,
			Labels: map[string]string{
				"app.kubernetes.io/name":       "agent",
				"app.kubernetes.io/instance":   swarmAgent.Name,
				"app.kubernetes.io/managed-by": "kubeswarm",
			},
		},
		Data: map[string]string{
			promptConfigMapKey: assembledPrompt,
		},
	}
	if err := ctrl.SetControllerReference(swarmAgent, desired, r.Scheme); err != nil {
		return err
	}

	existing := &corev1.ConfigMap{}
	err := r.Get(ctx, client.ObjectKey{Name: cmName, Namespace: swarmAgent.Namespace}, existing)
	if errors.IsNotFound(err) {
		return r.Create(ctx, desired)
	}
	if err != nil {
		return err
	}

	// Guard: skip the patch if the prompt hasn't changed.
	if existing.Data[promptConfigMapKey] == assembledPrompt {
		return nil
	}

	patch := client.MergeFrom(existing.DeepCopy())
	existing.Data = desired.Data
	return r.Patch(ctx, existing, patch)
}

// reconcileNetworkPolicy creates, updates, or deletes the NetworkPolicy for an agent
// Deployment depending on spec.networkPolicy (RFC-0016 Phase 2).
func (r *SwarmAgentReconciler) reconcileNetworkPolicy(
	ctx context.Context,
	swarmAgent *kubeswarmv1alpha1.SwarmAgent,
	resolvedMCPServers []kubeswarmv1alpha1.MCPToolSpec,
) error {
	mode := kubeswarmv1alpha1.NetworkPolicyModeDefault
	if swarmAgent.Spec.Infrastructure != nil && swarmAgent.Spec.Infrastructure.NetworkPolicy != "" {
		mode = swarmAgent.Spec.Infrastructure.NetworkPolicy
	}

	npName := swarmAgent.Name + "-netpol"
	existing := &networkingv1.NetworkPolicy{}
	getErr := r.Get(ctx, client.ObjectKey{Name: npName, Namespace: swarmAgent.Namespace}, existing)

	if mode == kubeswarmv1alpha1.NetworkPolicyModeDisabled {
		// Delete existing policy if present - user manages network policy externally.
		if getErr == nil {
			return r.Delete(ctx, existing)
		}
		return client.IgnoreNotFound(getErr)
	}

	desired, err := r.buildNetworkPolicy(swarmAgent, resolvedMCPServers, mode)
	if err != nil {
		return err
	}
	if err := ctrl.SetControllerReference(swarmAgent, desired, r.Scheme); err != nil {
		return err
	}

	if errors.IsNotFound(getErr) {
		return r.Create(ctx, desired)
	}
	if getErr != nil {
		return getErr
	}

	patch := client.MergeFrom(existing.DeepCopy())
	existing.Spec = desired.Spec
	return r.Patch(ctx, existing, patch)
}

// buildNetworkPolicy constructs the desired NetworkPolicy for an agent Deployment.
//
// Default mode: DNS (53) + Redis (6379, derived ns) + open egress.
// Strict mode:  DNS + Redis egress only; HTTPS restricted to resolved MCP server IPs.
func (r *SwarmAgentReconciler) buildNetworkPolicy(
	swarmAgent *kubeswarmv1alpha1.SwarmAgent,
	resolvedMCPServers []kubeswarmv1alpha1.MCPToolSpec,
	mode kubeswarmv1alpha1.NetworkPolicyMode,
) (*networkingv1.NetworkPolicy, error) {
	podSelector := metav1.LabelSelector{
		MatchLabels: map[string]string{"kubeswarm/deployment": swarmAgent.Name},
	}

	dnsPort53UDP := intstr.FromInt32(53)
	dnsPort53TCP := intstr.FromInt32(53)
	redisPort := intstr.FromInt32(6379)
	httpsPort := intstr.FromInt32(443)

	dnsEgress := networkingv1.NetworkPolicyEgressRule{
		Ports: []networkingv1.NetworkPolicyPort{
			{Protocol: protocolPtr(corev1.ProtocolUDP), Port: &dnsPort53UDP},
			{Protocol: protocolPtr(corev1.ProtocolTCP), Port: &dnsPort53TCP},
		},
	}

	redisNS := r.OperatorNamespace
	if redisNS == "" {
		redisNS = "kubeswarm-system"
	}
	redisEgress := networkingv1.NetworkPolicyEgressRule{
		Ports: []networkingv1.NetworkPolicyPort{
			{Protocol: protocolPtr(corev1.ProtocolTCP), Port: &redisPort},
		},
		To: []networkingv1.NetworkPolicyPeer{{
			NamespaceSelector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"kubernetes.io/metadata.name": redisNS},
			},
		}},
	}

	egressRules := []networkingv1.NetworkPolicyEgressRule{dnsEgress, redisEgress}

	switch mode {
	case kubeswarmv1alpha1.NetworkPolicyModeStrict:
		// Resolve MCP server hostnames to IPs and allowlist only those.
		peers, err := resolveMCPIPs(resolvedMCPServers)
		if err != nil {
			return nil, fmt.Errorf("resolving MCP IPs for strict NetworkPolicy: %w", err)
		}
		if len(peers) > 0 {
			egressRules = append(egressRules, networkingv1.NetworkPolicyEgressRule{
				Ports: []networkingv1.NetworkPolicyPort{
					{Protocol: protocolPtr(corev1.ProtocolTCP), Port: &httpsPort},
				},
				To: peers,
			})
		}
	default:
		// Default mode: allow all TCP egress to any destination and port.
		// Agents must reach LLM APIs on arbitrary ports (e.g. Anthropic/OpenAI on 443,
		// Ollama on 11434, custom endpoints, etc.). An empty EgressRule with no Ports
		// and no To selector allows all egress - DNS and Redis rules above are subsets.
		egressRules = append(egressRules, networkingv1.NetworkPolicyEgressRule{})
	}

	return &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      swarmAgent.Name + "-netpol",
			Namespace: swarmAgent.Namespace,
		},
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: podSelector,
			PolicyTypes: []networkingv1.PolicyType{
				networkingv1.PolicyTypeIngress,
				networkingv1.PolicyTypeEgress,
			},
			Ingress: []networkingv1.NetworkPolicyIngressRule{}, // no inbound to agent pods
			Egress:  egressRules,
		},
	}, nil
}

// resolveMCPIPs resolves the hostnames of MCP server URLs to /32 ipBlock peers for
// strict-mode NetworkPolicy generation. Returns an error if any hostname fails DNS lookup.
// DNS lookups are fanned out concurrently.
func resolveMCPIPs(servers []kubeswarmv1alpha1.MCPToolSpec) ([]networkingv1.NetworkPolicyPeer, error) {
	type result struct {
		name     string
		hostname string
		addrs    []string
		err      error
	}
	results := make([]result, len(servers))
	var wg sync.WaitGroup
	for i, s := range servers {
		u, err := neturl.Parse(s.URL)
		if err != nil || u.Hostname() == "" {
			continue
		}
		wg.Add(1)
		go func(idx int, name, hostname string) {
			defer wg.Done()
			addrs, err := net.LookupHost(hostname)
			results[idx] = result{name: name, hostname: hostname, addrs: addrs, err: err}
		}(i, s.Name, u.Hostname())
	}
	wg.Wait()

	seen := make(map[string]struct{})
	var peers []networkingv1.NetworkPolicyPeer
	for _, r := range results {
		if r.hostname == "" {
			continue
		}
		if r.err != nil {
			return nil, fmt.Errorf("DNS lookup for MCP server %q (%s): %w", r.name, r.hostname, r.err)
		}
		for _, addr := range r.addrs {
			cidr := addr + "/32"
			if _, ok := seen[cidr]; ok {
				continue
			}
			seen[cidr] = struct{}{}
			peers = append(peers, networkingv1.NetworkPolicyPeer{
				IPBlock: &networkingv1.IPBlock{CIDR: cidr},
			})
		}
	}
	return peers, nil
}

func (r *SwarmAgentReconciler) syncStatus(
	ctx context.Context,
	swarmAgent *kubeswarmv1alpha1.SwarmAgent,
) error {
	dep := &appsv1.Deployment{}
	if err := r.Get(ctx, client.ObjectKey{
		Name:      swarmAgent.Name + "-agent",
		Namespace: swarmAgent.Namespace,
	}, dep); err != nil {
		if errors.IsNotFound(err) {
			return nil
		}
		return err
	}

	swarmAgent.Status.Replicas = dep.Status.Replicas
	swarmAgent.Status.ReadyReplicas = dep.Status.ReadyReplicas
	swarmAgent.Status.ObservedGeneration = swarmAgent.Generation

	// Compute the set of MCP-exposed capability IDs and write to status.
	// Only update the slice when it has changed to avoid spurious status patches.
	var exposedIDs []string
	for _, cap := range swarmAgent.Spec.Capabilities {
		if cap.ExposeMCP {
			exposedIDs = append(exposedIDs, cap.Name)
		}
	}
	if !slices.Equal(swarmAgent.Status.ExposedMCPCapabilities, exposedIDs) {
		swarmAgent.Status.ExposedMCPCapabilities = exposedIDs
	}

	condStatus := metav1.ConditionFalse
	condReason := "Progressing"
	condMsg := fmt.Sprintf("%d/%d replicas ready", dep.Status.ReadyReplicas, dep.Status.Replicas)
	if dep.Status.ReadyReplicas == dep.Status.Replicas && dep.Status.Replicas > 0 {
		condStatus = metav1.ConditionTrue
		condReason = "AllReplicasReady"
	}
	// Guard: skip the status write if nothing changed.
	existingCond := apimeta.FindStatusCondition(swarmAgent.Status.Conditions, kubeswarmv1alpha1.ConditionReady)
	if swarmAgent.Status.Replicas == dep.Status.Replicas &&
		swarmAgent.Status.ReadyReplicas == dep.Status.ReadyReplicas &&
		swarmAgent.Status.ObservedGeneration == swarmAgent.Generation &&
		slices.Equal(swarmAgent.Status.ExposedMCPCapabilities, exposedIDs) &&
		existingCond != nil &&
		existingCond.Status == condStatus &&
		existingCond.Reason == condReason {
		return nil
	}

	r.setCondition(swarmAgent, kubeswarmv1alpha1.ConditionReady, condStatus, condReason, condMsg)

	return r.Status().Update(ctx, swarmAgent)
}

func (r *SwarmAgentReconciler) buildDeployment(in deploymentInput) *appsv1.Deployment {
	swarmAgent := in.swarmAgent
	allSettings := in.allSettings
	swarmMemory := in.swarmMemory
	assembledPrompt := in.assembledPrompt
	apiKeyEnvVar := in.apiKeyEnvVar
	apiKeyVersion := in.apiKeyVersion
	resolvedMCPServers := in.resolvedMCPServers
	effectivePolicy := in.effectivePolicy
	promptHashBytes := sha256.Sum256([]byte(assembledPrompt))
	promptHash := fmt.Sprintf("%x", promptHashBytes)
	labels := map[string]string{
		"app.kubernetes.io/name":       "agent",
		"app.kubernetes.io/instance":   swarmAgent.Name,
		"app.kubernetes.io/managed-by": "kubeswarm",
		"kubeswarm/deployment":         swarmAgent.Name,
	}

	replicas := int32(1)
	if swarmAgent.Spec.Runtime.Replicas != nil {
		replicas = *swarmAgent.Spec.Runtime.Replicas
	}
	// Budget enforcement: scale to 0 while the daily token limit is exceeded.
	if apimeta.IsStatusConditionTrue(swarmAgent.Status.Conditions, kubeswarmv1alpha1.ConditionBudgetExceeded) {
		replicas = 0
	}

	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      swarmAgent.Name + "-agent",
			Namespace: swarmAgent.Namespace,
			Labels:    labels,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{MatchLabels: labels},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: labels,
					Annotations: map[string]string{
						// Hash changes trigger automatic rolling restart when the prompt is updated.
						kubeswarmv1alpha1.AnnotationSystemPromptHash: promptHash,
						// ResourceVersion of the referenced k8s Secret - triggers rolling restart on key rotation.
						kubeswarmv1alpha1.AnnotationAPIKeyVersion: apiKeyVersion,
					},
				},
				Spec: corev1.PodSpec{
					TerminationGracePeriodSeconds: in.swarmAgent.Spec.Runtime.DrainTimeoutSeconds,
					ServiceAccountName:            agentServiceAccount,
					ImagePullSecrets:              r.agentImagePullSecrets(swarmAgent),
					// Pod-level security: enforce non-root user and RuntimeDefault seccomp
					// profile. Matches the operator pod's own PodSecurityContext.
					SecurityContext: &corev1.PodSecurityContext{
						RunAsNonRoot: new(true),
						RunAsUser:    int64Ptr(65532),
						SeccompProfile: &corev1.SeccompProfile{
							Type: corev1.SeccompProfileTypeRuntimeDefault,
						},
					},
					Containers: []corev1.Container{{
						Name:            "agent",
						Image:           r.AgentImage,
						ImagePullPolicy: r.agentImagePullPolicy(),
						Ports: []corev1.ContainerPort{
							{Name: "health", ContainerPort: 8080, Protocol: corev1.ProtocolTCP},
						},
						// Container-level security: drop all Linux capabilities, read-only
						// root filesystem, no privilege escalation. Matches the operator pod.
						SecurityContext: &corev1.SecurityContext{
							AllowPrivilegeEscalation: new(false),
							ReadOnlyRootFilesystem:   new(true),
							Capabilities: &corev1.Capabilities{
								Drop: []corev1.Capability{"ALL"},
							},
						},
						// Resource limits - use spec.resources if set, otherwise inject safe defaults.
						// Ephemeral storage limit is always added to prevent /tmp exhaustion.
						Resources: agentResources(swarmAgent),
						Env:       r.buildEnvVars(swarmAgent, allSettings, swarmMemory, apiKeyEnvVar, resolvedMCPServers, effectivePolicy, in.gatewayCapabilities),
						// Global fallback secret (set via Helm apiKeys.existingSecret).
						// Per-agent spec.envFrom entries are appended after and take precedence.
						EnvFrom: func() []corev1.EnvFromSource {
							// User-provided envFrom entries come first so they
							// take precedence over the global secret (in K8s,
							// the first EnvFrom source wins for duplicate keys).
							var base []corev1.EnvFromSource
							if swarmAgent.Spec.Infrastructure != nil {
								base = append(base, swarmAgent.Spec.Infrastructure.EnvFrom...)
							}
							base = append(base, corev1.EnvFromSource{
								SecretRef: &corev1.SecretEnvSource{
									LocalObjectReference: corev1.LocalObjectReference{
										Name: agentAPIKeysSecret,
									},
									Optional: new(true),
								},
							})
							// Inject S3 credentials secret (AWS_ACCESS_KEY_ID, AWS_SECRET_ACCESS_KEY)
							// when the team artifact store references one.
							if credSecret := swarmAgent.Annotations[kubeswarmv1alpha1.AnnotationTeamArtifactCredentials]; credSecret != "" {
								optional := true
								base = append(base, corev1.EnvFromSource{
									SecretRef: &corev1.SecretEnvSource{
										LocalObjectReference: corev1.LocalObjectReference{Name: credSecret},
										Optional:             &optional,
									},
								})
							}
							return base
						}(),
						// mTLS Secret volumes for MCP servers (RFC-0016 Phase 5).
						// Artifact PVC mount (RFC-0013) appended when configured.
						VolumeMounts: buildVolumeMounts(swarmAgent, resolvedMCPServers),
						LivenessProbe: &corev1.Probe{
							ProbeHandler: corev1.ProbeHandler{
								HTTPGet: &corev1.HTTPGetAction{
									Path: "/healthz",
									Port: intstr.FromInt32(8080),
								},
							},
							InitialDelaySeconds: 10,
							PeriodSeconds:       30,
						},
						ReadinessProbe: &corev1.Probe{
							ProbeHandler: corev1.ProbeHandler{
								HTTPGet: &corev1.HTTPGetAction{
									Path: "/readyz",
									Port: intstr.FromInt32(8080),
								},
							},
							InitialDelaySeconds: 10,
							PeriodSeconds:       30,
							TimeoutSeconds:      20,
						},
					}},
					// mTLS Secret volumes for MCP servers (RFC-0016 Phase 5).
					// Artifact PVC volume (RFC-0013) appended when configured.
					Volumes: buildVolumes(swarmAgent, resolvedMCPServers),
				},
			},
		},
	}
}

// assembleSystemPrompt composes the final system prompt from the base text, ordered
// SwarmSettings fragments, and per-MCP-server guidance.
//
// Fragment composition rules (RFC-0012):
//   - Fragments are applied in settingsRefs list order, then within each SwarmSettings in slice order.
//   - When the same fragment name appears in multiple settings, the last occurrence wins
//     (both its text and its position).
//   - prepend fragments precede the base prompt; append fragments follow it.
//   - MCP guidance is appended last as a structured "## MCP Tool Guidance" section.
//
// Backward compat: when a settings object has no Fragments but has the deprecated
// PromptFragments pointer set, Persona maps to a prepend fragment and OutputRules
// maps to an append fragment. Fragments takes precedence when both are set.
func assembleSystemPrompt(base string, allSettings []kubeswarmv1alpha1.SwarmSettings, mcpServers []kubeswarmv1alpha1.MCPToolSpec) string {
	type frag struct {
		text     string
		position string // "prepend" or "append"
	}

	fragByName := make(map[string]frag)
	var fragOrder []string // insertion-order for deduplicated names

	addFrag := func(name, text, position string) {
		if _, exists := fragByName[name]; !exists {
			fragOrder = append(fragOrder, name)
		}
		if position == "" {
			position = "append"
		}
		fragByName[name] = frag{text: text, position: position}
	}

	for _, s := range allSettings {
		if len(s.Spec.Fragments) > 0 {
			for _, f := range s.Spec.Fragments {
				addFrag(f.Name, f.Text, f.Position)
			}
		} else if s.Spec.PromptFragments != nil {
			// Deprecated fallback - synthesise named fragments so override semantics still apply.
			if s.Spec.PromptFragments.Persona != "" {
				addFrag("__persona__", s.Spec.PromptFragments.Persona, "prepend")
			}
			if s.Spec.PromptFragments.OutputRules != "" {
				addFrag("__output_rules__", s.Spec.PromptFragments.OutputRules, "append")
			}
		}
	}

	var prepends, appends []string
	for _, name := range fragOrder {
		f := fragByName[name]
		if strings.TrimSpace(f.text) == "" {
			continue
		}
		if f.position == "prepend" {
			prepends = append(prepends, f.text)
		} else {
			appends = append(appends, f.text)
		}
	}

	// MCP guidance section - one sub-heading per server that has instructions set.
	var mcpGuidances []string
	for _, s := range mcpServers {
		if s.Instructions != "" {
			mcpGuidances = append(mcpGuidances, fmt.Sprintf("### %s\n%s", s.Name, s.Instructions))
		}
	}
	if len(mcpGuidances) > 0 {
		appends = append(appends, "## MCP Tool Guidance\n\n"+strings.Join(mcpGuidances, "\n\n"))
	}

	parts := make([]string, 0, len(prepends)+1+len(appends)+1)
	parts = append(parts, prepends...)
	if strings.TrimSpace(base) != "" {
		parts = append(parts, base)
	}
	parts = append(parts, appends...)
	// Always append the injection-defence fragment so agents treat <swarm:step-output>
	// content as untrusted data, not instructions (RFC-0016 Phase 4).
	parts = append(parts, strings.TrimSpace(pkgflow.InjectionDefenceFragment))
	return strings.Join(parts, "\n\n")
}

// hashPrompt returns the hex-encoded SHA-256 digest of the given prompt text.
// Stored in status.systemPromptHash for prompt-immutability enforcement (RFC-0016 Phase 4).
func hashPrompt(prompt string) string {
	sum := sha256.Sum256([]byte(prompt))
	return hex.EncodeToString(sum[:])
}

// mergeSettingsEnvVars returns the env vars derived from all referenced SwarmSettings.
// Last-wins semantics: if the same setting appears in multiple objects, the last value wins.
func mergeSettingsEnvVars(allSettings []kubeswarmv1alpha1.SwarmSettings) []corev1.EnvVar {
	var temp, format, backend string
	for _, s := range allSettings {
		if s.Spec.Temperature != "" {
			temp = s.Spec.Temperature
		}
		if s.Spec.OutputFormat != "" {
			format = s.Spec.OutputFormat
		}
		if s.Spec.MemoryBackend != "" {
			backend = string(s.Spec.MemoryBackend)
		}
	}
	var envs []corev1.EnvVar
	if temp != "" {
		envs = append(envs, corev1.EnvVar{Name: "AGENT_TEMPERATURE", Value: temp})
	}
	if format != "" {
		envs = append(envs, corev1.EnvVar{Name: "AGENT_OUTPUT_FORMAT", Value: format})
	}
	if backend != "" {
		envs = append(envs, corev1.EnvVar{Name: "AGENT_MEMORY_BACKEND", Value: backend})
	}
	return envs
}

// buildRedisMemoryEnvVars returns env vars for a Redis memory backend.
func buildRedisMemoryEnvVars(mem *kubeswarmv1alpha1.RedisMemoryConfig) []corev1.EnvVar {
	if mem == nil {
		return nil
	}
	envs := []corev1.EnvVar{{
		Name: "AGENT_MEMORY_REDIS_URL",
		ValueFrom: &corev1.EnvVarSource{
			SecretKeyRef: &corev1.SecretKeySelector{
				LocalObjectReference: corev1.LocalObjectReference{Name: mem.SecretRef.Name},
				Key:                  "REDIS_URL",
			},
		},
	}}
	if mem.TTLSeconds > 0 {
		envs = append(envs, corev1.EnvVar{Name: "AGENT_MEMORY_REDIS_TTL", Value: fmt.Sprintf("%d", mem.TTLSeconds)})
	}
	if mem.MaxEntries > 0 {
		envs = append(envs, corev1.EnvVar{Name: "AGENT_MEMORY_REDIS_MAX_ENTRIES", Value: fmt.Sprintf("%d", mem.MaxEntries)})
	}
	return envs
}

// buildVectorStoreMemoryEnvVars returns env vars for a vector-store memory backend.
// It injects both the legacy per-field vars and AGENT_VECTOR_STORE_URL (RFC-0013 Phase 2).
func buildVectorStoreMemoryEnvVars(vs *kubeswarmv1alpha1.VectorStoreMemoryConfig) []corev1.EnvVar {
	if vs == nil {
		return nil
	}
	envs := []corev1.EnvVar{
		{Name: "AGENT_MEMORY_VECTOR_STORE_PROVIDER", Value: string(vs.Provider)},
		{Name: "AGENT_MEMORY_VECTOR_STORE_ENDPOINT", Value: vs.Endpoint},
		{Name: "AGENT_MEMORY_VECTOR_STORE_COLLECTION", Value: vs.Collection},
	}
	if vs.SecretRef != nil {
		envs = append(envs, corev1.EnvVar{
			Name: "AGENT_MEMORY_VECTOR_STORE_API_KEY",
			ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{Name: vs.SecretRef.Name},
					Key:                  "VECTOR_STORE_API_KEY",
				},
			},
		})
	}
	if vs.TTLSeconds > 0 {
		envs = append(envs, corev1.EnvVar{Name: "AGENT_MEMORY_VECTOR_STORE_TTL", Value: fmt.Sprintf("%d", vs.TTLSeconds)})
	}
	// Inject the unified AGENT_VECTOR_STORE_URL (RFC-0013): scheme://host:port/collection.
	// The spec Endpoint may include an http:// scheme which we strip and replace with the
	// provider scheme so the runtime can resolve the backend by URL scheme prefix.
	if vectorStoreURL := vectorStoreURL(vs); vectorStoreURL != "" {
		envs = append(envs, corev1.EnvVar{Name: "AGENT_VECTOR_STORE_URL", Value: vectorStoreURL})
	}
	return envs
}

// vectorStoreURL builds AGENT_VECTOR_STORE_URL from a VectorStoreMemoryConfig.
// Format: {scheme}://{host:port}/{collection}
// The Endpoint field may be "http://host:port" or just "host:port"; both are handled.
// The pgvector provider maps to the standard "postgres" URI scheme so the runtime
// can resolve the backend via its registered "postgres" factory.
func vectorStoreURL(vs *kubeswarmv1alpha1.VectorStoreMemoryConfig) string {
	if vs.Provider == "" || vs.Endpoint == "" {
		return ""
	}
	host := vs.Endpoint
	// Strip any existing scheme so we can prepend the provider scheme.
	if i := strings.Index(host, "://"); i >= 0 {
		host = host[i+3:]
	}
	collection := vs.Collection
	if collection == "" {
		collection = "agent-memories"
	}
	scheme := string(vs.Provider)
	if vs.Provider == kubeswarmv1alpha1.VectorStoreProviderPgvector {
		scheme = "postgres"
	}
	return fmt.Sprintf("%s://%s/%s", scheme, host, collection)
}

// buildMemoryEnvVars returns env vars for the SwarmMemory backend (Redis or vector-store).
func buildMemoryEnvVars(swarmMemory *kubeswarmv1alpha1.SwarmMemory) []corev1.EnvVar {
	if swarmMemory == nil {
		return nil
	}
	envs := []corev1.EnvVar{{Name: "AGENT_MEMORY_BACKEND", Value: string(swarmMemory.Spec.Backend)}}
	switch swarmMemory.Spec.Backend {
	case kubeswarmv1alpha1.MemoryBackendRedis:
		envs = append(envs, buildRedisMemoryEnvVars(swarmMemory.Spec.Redis)...)
	case kubeswarmv1alpha1.MemoryBackendVectorStore:
		envs = append(envs, buildVectorStoreMemoryEnvVars(swarmMemory.Spec.VectorStore)...)
		envs = append(envs, buildEmbeddingEnvVars(swarmMemory.Spec.Embedding)...)
	}
	return envs
}

// buildEmbeddingEnvVars injects embedding model/provider config from SwarmMemory.spec.embedding
// into agent pods. These are merged into AGENT_LOOP_POLICY.memoryPolicy by the agent runtime.
func buildEmbeddingEnvVars(emb *kubeswarmv1alpha1.EmbeddingConfig) []corev1.EnvVar {
	if emb == nil {
		return nil
	}
	var envs []corev1.EnvVar
	if emb.Model != "" {
		envs = append(envs, corev1.EnvVar{Name: "AGENT_EMBEDDING_MODEL", Value: emb.Model})
	}
	provider := emb.Provider
	if provider == "" || provider == "auto" {
		provider = providers.DetectEmbedding(emb.Model)
	}
	if provider != "" {
		envs = append(envs, corev1.EnvVar{Name: "AGENT_EMBEDDING_PROVIDER", Value: provider})
	}
	return envs
}

// mcpServerRuntime is the runtime representation of one MCP server injected into
// AGENT_MCP_SERVERS. Auth secret values are never included - only env var names and
// pod-local file paths are carried here, matching config.MCPServerConfig fields.
type mcpServerRuntime struct {
	Name        string               `json:"name"`
	URL         string               `json:"url"`
	AuthType    string               `json:"authType,omitempty"`
	TokenEnvVar string               `json:"tokenEnvVar,omitempty"`
	CertFile    string               `json:"certFile,omitempty"`
	KeyFile     string               `json:"keyFile,omitempty"`
	Discovery   *mcpDiscoveryRuntime `json:"discovery,omitempty"`
}

// mcpDiscoveryRuntime is the runtime representation of MCPDiscoveryConfig,
// serialized into AGENT_MCP_SERVERS JSON and deserialized by config.MCPDiscoveryConfigRuntime.
type mcpDiscoveryRuntime struct {
	Dynamic             bool `json:"dynamic,omitempty"`
	PollIntervalSeconds int  `json:"pollIntervalSeconds,omitempty"`
}

// mcpTokenEnvVar returns the env var name used to carry a bearer token for the given
// MCP server name. Non-alphanumeric characters are replaced with underscores.
func mcpTokenEnvVar(serverName string) string {
	safe := strings.Map(func(r rune) rune {
		if (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			return r
		}
		return '_'
	}, serverName)
	return "AGENT_MCP_TOKEN_" + strings.ToUpper(safe)
}

// sanitizeMCPName normalizes a server name to a k8s-safe string.
// allowed controls which characters are kept; everything else becomes replacement.
func sanitizeMCPName(serverName string, allowed func(rune) bool, replacement rune) string {
	return strings.Map(func(r rune) rune {
		if allowed(r) {
			return r
		}
		return replacement
	}, serverName)
}

// mcpVolumeName returns the k8s volume name for an mTLS Secret mount for the given server.
func mcpVolumeName(serverName string) string {
	safe := sanitizeMCPName(serverName, func(r rune) bool {
		if r >= 'A' && r <= 'Z' {
			return false // lowered below
		}
		return (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-'
	}, '-')
	return "mcp-mtls-" + strings.ToLower(safe)
}

// mcpMountPath returns the pod-local directory for an mTLS Secret mount.
func mcpMountPath(serverName string) string {
	safe := sanitizeMCPName(serverName, func(r rune) bool {
		return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_'
	}, '-')
	return "/var/secrets/mcp/" + safe
}

// buildMCPRuntimeConfigs converts resolved MCPToolSpecs into their runtime representations
// for the AGENT_MCP_SERVERS JSON env var. Auth secret values are not included.
func buildMCPRuntimeConfigs(servers []kubeswarmv1alpha1.MCPToolSpec) []mcpServerRuntime {
	out := make([]mcpServerRuntime, 0, len(servers))
	for _, s := range servers {
		r := mcpServerRuntime{Name: s.Name, URL: s.URL}
		if s.Auth != nil {
			switch {
			case s.Auth.Bearer != nil:
				r.AuthType = mcpAuthBearer
				r.TokenEnvVar = mcpTokenEnvVar(s.Name)
			case s.Auth.MTLS != nil:
				r.AuthType = mcpAuthMTLS
				mount := mcpMountPath(s.Name)
				r.CertFile = mount + "/tls.crt"
				r.KeyFile = mount + "/tls.key"
			}
		}
		if s.Discovery != nil {
			r.Discovery = &mcpDiscoveryRuntime{
				Dynamic:             s.Discovery.Dynamic,
				PollIntervalSeconds: s.Discovery.PollIntervalSeconds,
			}
		}
		out = append(out, r)
	}
	return out
}

// buildMCPAuthEnvVars returns SecretKeyRef env vars for MCP servers using bearer auth.
func buildMCPAuthEnvVars(servers []kubeswarmv1alpha1.MCPToolSpec) []corev1.EnvVar {
	var envVars []corev1.EnvVar
	for _, s := range servers {
		if s.Auth == nil || s.Auth.Bearer == nil {
			continue
		}
		envVars = append(envVars, corev1.EnvVar{
			Name: mcpTokenEnvVar(s.Name),
			ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: &s.Auth.Bearer.SecretKeyRef,
			},
		})
	}
	return envVars
}

// buildMCPVolumes returns k8s Volumes for MCP servers using mTLS auth.
func buildMCPVolumes(servers []kubeswarmv1alpha1.MCPToolSpec) []corev1.Volume {
	var vols []corev1.Volume
	for _, s := range servers {
		if s.Auth == nil || s.Auth.MTLS == nil {
			continue
		}
		vols = append(vols, corev1.Volume{
			Name: mcpVolumeName(s.Name),
			VolumeSource: corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{
					SecretName:  s.Auth.MTLS.SecretRef.Name,
					DefaultMode: int32Ptr(0o444),
				},
			},
		})
	}
	return vols
}

// buildMCPVolumeMounts returns VolumeMounts for MCP mTLS secrets.
func buildMCPVolumeMounts(servers []kubeswarmv1alpha1.MCPToolSpec) []corev1.VolumeMount {
	var mounts []corev1.VolumeMount
	for _, s := range servers {
		if s.Auth == nil || s.Auth.MTLS == nil {
			continue
		}
		mounts = append(mounts, corev1.VolumeMount{
			Name:      mcpVolumeName(s.Name),
			MountPath: mcpMountPath(s.Name),
			ReadOnly:  true,
		})
	}
	return mounts
}

const artifactVolumeName = "swarm-artifacts"

// buildVolumes returns all pod volumes: prompt ConfigMap + MCP mTLS secrets + optional artifact PVC (RFC-0013).
func buildVolumes(swarmAgent *kubeswarmv1alpha1.SwarmAgent, servers []kubeswarmv1alpha1.MCPToolSpec) []corev1.Volume {
	vols := []corev1.Volume{
		{
			Name: promptVolumeName,
			VolumeSource: corev1.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: swarmAgent.Name + "-prompt",
					},
				},
			},
		},
	}
	vols = append(vols, buildMCPVolumes(servers)...)
	if claim, ok := swarmAgent.Annotations[kubeswarmv1alpha1.AnnotationTeamArtifactClaim]; ok && claim != "" {
		vols = append(vols, corev1.Volume{
			Name: artifactVolumeName,
			VolumeSource: corev1.VolumeSource{
				PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
					ClaimName: claim,
				},
			},
		})
	}
	return vols
}

// buildVolumeMounts returns all container mounts: prompt ConfigMap + MCP mTLS secrets + optional artifact PVC.
func buildVolumeMounts(swarmAgent *kubeswarmv1alpha1.SwarmAgent, servers []kubeswarmv1alpha1.MCPToolSpec) []corev1.VolumeMount {
	mounts := []corev1.VolumeMount{
		{
			Name:      promptVolumeName,
			MountPath: promptMountPath,
			ReadOnly:  true,
		},
	}
	mounts = append(mounts, buildMCPVolumeMounts(servers)...)
	if claim, ok := swarmAgent.Annotations[kubeswarmv1alpha1.AnnotationTeamArtifactClaim]; ok && claim != "" {
		mountPath := swarmAgent.Annotations[kubeswarmv1alpha1.AnnotationTeamArtifactStore]
		// Extract the path from the file:// URL (strip scheme).
		if after, found := strings.CutPrefix(mountPath, "file://"); found {
			mountPath = after
		}
		if mountPath == "" {
			mountPath = "/artifacts"
		}
		mounts = append(mounts, corev1.VolumeMount{
			Name:      artifactVolumeName,
			MountPath: mountPath,
		})
	}
	return mounts
}

func (r *SwarmAgentReconciler) buildEnvVars(
	swarmAgent *kubeswarmv1alpha1.SwarmAgent,
	allSettings []kubeswarmv1alpha1.SwarmSettings,
	swarmMemory *kubeswarmv1alpha1.SwarmMemory,
	apiKeyEnvVar *corev1.EnvVar,
	resolvedMCPServers []kubeswarmv1alpha1.MCPToolSpec,
	effectivePolicy *kubeswarmv1alpha1.EffectivePolicySpec,
	gatewayCapabilities []GatewayCapabilityEntry,
) []corev1.EnvVar {
	mcpJSON, _ := json.Marshal(buildMCPRuntimeConfigs(resolvedMCPServers))

	maxTokens := 8000
	timeoutSecs := 120
	maxRetries := 3
	var dailyTokenLimit int64
	if swarmAgent.Spec.Guardrails != nil && swarmAgent.Spec.Guardrails.Limits != nil {
		limits := swarmAgent.Spec.Guardrails.Limits
		if limits.TokensPerCall > 0 {
			maxTokens = limits.TokensPerCall
		}
		if limits.TimeoutSeconds > 0 {
			timeoutSecs = limits.TimeoutSeconds
		}
		if limits.Retries > 0 {
			maxRetries = limits.Retries
		}
		dailyTokenLimit = limits.DailyTokens
	}

	// Clamp against effective policy (RFC-0049 Phase 5).
	maxTokens, timeoutSecs, maxRetries, dailyTokenLimit = applyPolicyLimits(
		maxTokens, timeoutSecs, maxRetries, dailyTokenLimit, effectivePolicy,
	)

	envVars := []corev1.EnvVar{
		{Name: "AGENT_MODEL", Value: swarmAgent.Spec.Model},
		{Name: "AGENT_SYSTEM_PROMPT_PATH", Value: promptFilePath},
		{Name: "AGENT_MCP_SERVERS", Value: string(mcpJSON)},
		{Name: "AGENT_MAX_TOKENS", Value: fmt.Sprintf("%d", maxTokens)},
		{Name: "AGENT_TIMEOUT_SECONDS", Value: fmt.Sprintf("%d", timeoutSecs)},
		{Name: "AGENT_MAX_RETRIES", Value: fmt.Sprintf("%d", maxRetries)},
		{Name: "AGENT_DAILY_TOKEN_LIMIT", Value: fmt.Sprintf("%d", dailyTokenLimit)},
		// POD_NAME is used as the Redis consumer group identity.
		{
			Name: "POD_NAME",
			ValueFrom: &corev1.EnvVarSource{
				FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.name"},
			},
		},
		// AGENT_NAMESPACE and AGENT_NAME are used for OTel metric labels.
		{
			Name: "AGENT_NAMESPACE",
			ValueFrom: &corev1.EnvVarSource{
				FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.namespace"},
			},
		},
		{Name: "AGENT_NAME", Value: swarmAgent.Name},
	}

	envVars = append(envVars, buildObservabilityEnvVars(swarmAgent)...)

	// Inject env vars forwarded from the operator pod via SWARM_AGENT_INJECT_* prefix.
	// These allow cluster-wide agent defaults (e.g. OPENAI_BASE_URL, AGENT_PROVIDER)
	// to be set once in the helm values.agentExtraEnv without touching every namespace.
	envVars = append(envVars, agentInjectEnvVars()...)

	envVars = append(envVars, mergeSettingsEnvVars(allSettings)...)
	// SwarmMemory takes precedence over any AGENT_MEMORY_BACKEND set via SwarmSettings.
	envVars = append(envVars, buildMemoryEnvVars(swarmMemory)...)

	// Inject inline webhook tool definitions so the agent runtime can call them directly.
	if swarmAgent.Spec.Tools != nil && len(swarmAgent.Spec.Tools.Webhooks) > 0 {
		toolsJSON, _ := json.Marshal(swarmAgent.Spec.Tools.Webhooks)
		envVars = append(envVars, corev1.EnvVar{
			Name:  "AGENT_WEBHOOK_TOOLS",
			Value: string(toolsJSON),
		})
	}

	envVars = append(envVars, buildTeamEnvVars(swarmAgent)...)

	// Inject API key from SwarmSecret if configured. Explicit env vars take precedence over EnvFrom.
	if apiKeyEnvVar != nil {
		envVars = append(envVars, *apiKeyEnvVar)
	}

	// Inject per-MCP-server bearer token env vars sourced from k8s Secrets (RFC-0016 Phase 5).
	envVars = append(envVars, buildMCPAuthEnvVars(resolvedMCPServers)...)

	envVars = append(envVars, buildArtifactEnvVars(swarmAgent)...)
	envVars = append(envVars, buildPluginEnvVars(swarmAgent)...)

	envVars = append(envVars, buildReasoningEnvVars(swarmAgent, allSettings, effectivePolicy)...)

	// Inject policy-specific env vars (tool deny, trust level, deny patterns).
	envVars = append(envVars, buildPolicyEnvVars(effectivePolicy)...)

	// Inject LoopPolicy as JSON (RFC-0026). Only set when the field is non-nil.
	if swarmAgent.Spec.Runtime.Loop != nil {
		if raw, err := json.Marshal(swarmAgent.Spec.Runtime.Loop); err == nil {
			envVars = append(envVars, corev1.EnvVar{
				Name:  "AGENT_LOOP_POLICY",
				Value: string(raw),
			})
		}
	}

	// Inject audit log config as JSON (RFC-0030).
	// Reads cluster-level defaults from operator env vars (set by Helm),
	// then overrides with agent-level spec.observability.auditLog if set.
	if auditJSON := buildAuditLogEnvVar(swarmAgent); auditJSON != "" {
		envVars = append(envVars, corev1.EnvVar{
			Name:  "AGENT_AUDIT_LOG",
			Value: auditJSON,
		})
	}

	envVars = append(envVars, buildCircuitBreakerEnvVars(swarmAgent)...)

	// Inject advisor connection configs as JSON (RFC-0048).
	envVars = append(envVars, buildAdvisorEnvVars(swarmAgent)...)

	// Inject gateway configuration (RFC-0052).
	envVars = append(envVars, buildGatewayEnvVars(swarmAgent, gatewayCapabilities)...)

	// Deduplicate: last occurrence wins. This prevents warnings when
	// SWARM_AGENT_INJECT_TASK_QUEUE_URL and the team annotation both set TASK_QUEUE_URL.
	seen := make(map[string]int, len(envVars))
	out := envVars[:0]
	for _, e := range envVars {
		if i, ok := seen[e.Name]; ok {
			out[i] = e
		} else {
			seen[e.Name] = len(out)
			out = append(out, e)
		}
	}
	return out
}

// buildObservabilityEnvVars returns env vars for health check and logging configuration.
func buildObservabilityEnvVars(swarmAgent *kubeswarmv1alpha1.SwarmAgent) []corev1.EnvVar {
	if swarmAgent.Spec.Observability == nil {
		return nil
	}
	var out []corev1.EnvVar

	if hc := swarmAgent.Spec.Observability.HealthCheck; hc != nil &&
		hc.Type == kubeswarmv1alpha1.HealthCheckSemantic && hc.Prompt != "" {
		out = append(out, corev1.EnvVar{Name: "AGENT_VALIDATOR_PROMPT", Value: hc.Prompt})
	}

	if logging := swarmAgent.Spec.Observability.Logging; logging != nil {
		if logging.Level != "" {
			out = append(out, corev1.EnvVar{Name: "AGENT_LOG_LEVEL", Value: string(logging.Level)})
		}
		if logging.ToolCalls {
			out = append(out, corev1.EnvVar{Name: "AGENT_LOG_TOOL_CALLS", Value: "true"})
		}
		if logging.LLMTurns {
			out = append(out, corev1.EnvVar{Name: "AGENT_LOG_LLM_TURNS", Value: "true"})
		}
		if logging.Redaction != nil {
			if logging.Redaction.Secrets {
				out = append(out, corev1.EnvVar{Name: "AGENT_LOG_REDACT_SECRETS", Value: "true"})
			}
			if logging.Redaction.PII {
				out = append(out, corev1.EnvVar{Name: "AGENT_LOG_REDACT_PII", Value: "true"})
			}
		}
	}
	return out
}

// buildReasoningEnvVars returns env vars for reasoning configuration (RFC-0033)
// and thinking/answer token guardrail limits.
func buildReasoningEnvVars(
	swarmAgent *kubeswarmv1alpha1.SwarmAgent,
	allSettings []kubeswarmv1alpha1.SwarmSettings,
	effectivePolicy *kubeswarmv1alpha1.EffectivePolicySpec,
) []corev1.EnvVar {
	var out []corev1.EnvVar

	if rc := mergeReasoningConfig(swarmAgent.Spec.Reasoning, allSettings); rc != nil {
		if rc.Mode != "" {
			out = append(out, corev1.EnvVar{Name: "AGENT_REASONING_MODE", Value: string(rc.Mode)})
		}
		if rc.Effort != "" {
			out = append(out, corev1.EnvVar{Name: "AGENT_REASONING_EFFORT", Value: string(rc.Effort)})
		}
		if rc.BudgetTokens != nil {
			out = append(out, corev1.EnvVar{
				Name:  "AGENT_REASONING_BUDGET_TOKENS",
				Value: strconv.FormatInt(int64(*rc.BudgetTokens), 10),
			})
		}
	}

	var thinkingTokens, answerTokens *int32
	if g := swarmAgent.Spec.Guardrails; g != nil && g.Limits != nil {
		thinkingTokens = g.Limits.MaxThinkingTokensPerCall
		answerTokens = g.Limits.MaxAnswerTokensPerCall
	}
	thinkingTokens, answerTokens = applyPolicyThinkingLimits(thinkingTokens, answerTokens, effectivePolicy)
	if thinkingTokens != nil {
		out = append(out, corev1.EnvVar{
			Name:  "AGENT_MAX_THINKING_TOKENS_PER_CALL",
			Value: strconv.FormatInt(int64(*thinkingTokens), 10),
		})
	}
	if answerTokens != nil {
		out = append(out, corev1.EnvVar{
			Name:  "AGENT_MAX_ANSWER_TOKENS_PER_CALL",
			Value: strconv.FormatInt(int64(*answerTokens), 10),
		})
	}
	return out
}

// buildCircuitBreakerEnvVars returns the AGENT_CIRCUIT_BREAKER env var when configured.
func buildCircuitBreakerEnvVars(swarmAgent *kubeswarmv1alpha1.SwarmAgent) []corev1.EnvVar {
	if swarmAgent.Spec.Guardrails == nil || swarmAgent.Spec.Guardrails.Limits == nil ||
		swarmAgent.Spec.Guardrails.Limits.CircuitBreaker == nil {
		return nil
	}
	cb := swarmAgent.Spec.Guardrails.Limits.CircuitBreaker
	cbJSON, _ := json.Marshal(map[string]int{
		"failureThreshold": cb.FailureThreshold,
		"cooldownSeconds":  cb.CooldownSeconds,
		"halfOpenMaxCalls": cb.HalfOpenMaxCalls,
	})
	return []corev1.EnvVar{{Name: "AGENT_CIRCUIT_BREAKER", Value: string(cbJSON)}}
}

// buildTeamEnvVars returns env vars derived from SwarmTeam annotations/labels.
// For standalone agents (no team annotation), it builds a per-agent stream key
// so that each agent polls its own Redis stream instead of the shared default.
func buildTeamEnvVars(swarmAgent *kubeswarmv1alpha1.SwarmAgent) []corev1.EnvVar {
	var out []corev1.EnvVar
	if queueURL, ok := swarmAgent.Annotations[kubeswarmv1alpha1.AnnotationTeamQueueURL]; ok && queueURL != "" {
		out = append(out, corev1.EnvVar{Name: "TASK_QUEUE_URL", Value: queueURL})
	} else {
		// Standalone agent: derive a per-agent stream key from the injected
		// base URL so agents don't share the default "agent-tasks" stream.
		// The actual base URL comes from SWARM_AGENT_INJECT_TASK_QUEUE_URL
		// which is appended earlier; here we set only the stream-qualified
		// override that will win during deduplication (last occurrence wins).
		baseURL := os.Getenv("SWARM_AGENT_INJECT_TASK_QUEUE_URL")
		if baseURL == "" {
			baseURL = os.Getenv("TASK_QUEUE_URL")
		}
		if baseURL != "" {
			streamKey := swarmAgent.Namespace + "." + swarmAgent.Name
			out = append(out, corev1.EnvVar{
				Name:  "TASK_QUEUE_URL",
				Value: appendStreamParam(baseURL, streamKey),
			})
		}
	}
	if routes, ok := swarmAgent.Annotations[kubeswarmv1alpha1.AnnotationTeamRoutes]; ok && routes != "" {
		out = append(out, corev1.EnvVar{Name: "AGENT_TEAM_ROUTES", Value: routes})
	}
	if role, ok := swarmAgent.Annotations[kubeswarmv1alpha1.AnnotationTeamRole]; ok && role != "" {
		out = append(out, corev1.EnvVar{Name: "AGENT_TEAM_ROLE", Value: role})
	}
	if teamName, ok := swarmAgent.Labels[kubeswarmv1alpha1.LabelTeam]; ok && teamName != "" {
		out = append(out, corev1.EnvVar{Name: "AGENT_TEAM_NAME", Value: teamName})
	}
	return out
}

// buildArtifactEnvVars returns env vars for the artifact store (RFC-0013).
func buildArtifactEnvVars(swarmAgent *kubeswarmv1alpha1.SwarmAgent) []corev1.EnvVar {
	storeURL, ok := swarmAgent.Annotations[kubeswarmv1alpha1.AnnotationTeamArtifactStore]
	if !ok || storeURL == "" {
		return nil
	}
	out := make([]corev1.EnvVar, 0, 2)
	out = append(out, corev1.EnvVar{Name: "AGENT_ARTIFACT_STORE_URL", Value: storeURL})
	artifactDir := "/tmp/swarm-artifacts"
	if after, found := strings.CutPrefix(storeURL, "file://"); found && after != "" {
		artifactDir = after
	}
	out = append(out, corev1.EnvVar{Name: "AGENT_ARTIFACT_DIR", Value: artifactDir})

	// Inject save-output config when spec.runtime.artifacts is set.
	if swarmAgent.Spec.Runtime.Artifacts != nil && swarmAgent.Spec.Runtime.Artifacts.SaveOutput {
		out = append(out, corev1.EnvVar{Name: "AGENT_ARTIFACT_SAVE_OUTPUT", Value: "true"})
		format := swarmAgent.Spec.Runtime.Artifacts.Format
		if format == "" {
			format = "text"
		}
		out = append(out, corev1.EnvVar{Name: "AGENT_ARTIFACT_SAVE_FORMAT", Value: string(format)})
	}

	return out
}

// buildPluginEnvVars returns env vars for external gRPC plugin addresses (RFC-0025).
func buildPluginEnvVars(swarmAgent *kubeswarmv1alpha1.SwarmAgent) []corev1.EnvVar {
	if swarmAgent.Spec.Infrastructure == nil || swarmAgent.Spec.Infrastructure.Plugins == nil {
		return nil
	}
	plugins := swarmAgent.Spec.Infrastructure.Plugins
	var out []corev1.EnvVar
	if plugins.LLM != nil {
		out = append(out, corev1.EnvVar{Name: "SWARM_PLUGIN_LLM_ADDR", Value: plugins.LLM.Address})
	}
	if plugins.Queue != nil {
		out = append(out, corev1.EnvVar{Name: "SWARM_PLUGIN_QUEUE_ADDR", Value: plugins.Queue.Address})
	}
	return out
}

// ensureDefaultRegistry creates an SwarmRegistry named "default" in the given namespace
// if one does not already exist. The created registry is annotated as operator-managed
// so users know it was auto-created. Users may edit its spec freely; the operator will
// not overwrite their changes.
func (r *SwarmAgentReconciler) ensureDefaultRegistry(ctx context.Context, namespace string) error {
	// Fast path: skip the Get when we've already confirmed the registry exists.
	if _, ok := r.registryEnsured.Load(namespace); ok {
		return nil
	}
	reg := &kubeswarmv1alpha1.SwarmRegistry{}
	err := r.Get(ctx, client.ObjectKey{Name: "default", Namespace: namespace}, reg)
	if err == nil {
		r.registryEnsured.Store(namespace, struct{}{})
		return nil // already exists
	}
	if !errors.IsNotFound(err) {
		return fmt.Errorf("checking default SwarmRegistry: %w", err)
	}
	defaultReg := &kubeswarmv1alpha1.SwarmRegistry{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "default",
			Namespace: namespace,
			Annotations: map[string]string{
				kubeswarmv1alpha1.AnnotationManaged: "true",
			},
		},
		Spec: kubeswarmv1alpha1.SwarmRegistrySpec{
			Scope: kubeswarmv1alpha1.RegistryScopeNamespace,
		},
	}
	if err := r.Create(ctx, defaultReg); err != nil && !errors.IsAlreadyExists(err) {
		return fmt.Errorf("creating default SwarmRegistry: %w", err)
	}
	r.registryEnsured.Store(namespace, struct{}{})
	return nil
}

// reconcileRegistryRef validates that spec.registryRef names an existing SwarmRegistry.
// Emits a RegistryNotFound condition when the registry is absent. Non-blocking -
// the agent starts regardless. Agents with registryRef == nil have opted out; no check.
func (r *SwarmAgentReconciler) reconcileRegistryRef(ctx context.Context, agent *kubeswarmv1alpha1.SwarmAgent) {
	if agent.Spec.Infrastructure == nil || agent.Spec.Infrastructure.RegistryRef == nil {
		apimeta.RemoveStatusCondition(&agent.Status.Conditions, kubeswarmv1alpha1.ConditionRegistryNotFound)
		return
	}
	reg := &kubeswarmv1alpha1.SwarmRegistry{}
	err := r.Get(ctx, client.ObjectKey{Name: agent.Spec.Infrastructure.RegistryRef.Name, Namespace: agent.Namespace}, reg)
	if errors.IsNotFound(err) {
		r.setCondition(agent, kubeswarmv1alpha1.ConditionRegistryNotFound, metav1.ConditionTrue, kubeswarmv1alpha1.ConditionRegistryNotFound,
			fmt.Sprintf("SwarmRegistry %q not found in namespace %q; agent will not be indexed",
				agent.Spec.Infrastructure.RegistryRef.Name, agent.Namespace))
		return
	}
	apimeta.RemoveStatusCondition(&agent.Status.Conditions, kubeswarmv1alpha1.ConditionRegistryNotFound)
}

// reconcileBudgetRef checks the SwarmBudget referenced by spec.guardrails.budgetRef.
// When the budget is exceeded and spec.hardStop is true, it sets the BudgetExceeded
// condition, which causes buildDeployment to scale replicas to 0.
func (r *SwarmAgentReconciler) reconcileBudgetRef(ctx context.Context, agent *kubeswarmv1alpha1.SwarmAgent) error {
	if agent.Spec.Guardrails == nil || agent.Spec.Guardrails.BudgetRef == nil {
		return nil
	}
	budget := &kubeswarmv1alpha1.SwarmBudget{}
	if err := r.Get(ctx, client.ObjectKey{
		Name:      agent.Spec.Guardrails.BudgetRef.Name,
		Namespace: agent.Namespace,
	}, budget); err != nil {
		if errors.IsNotFound(err) {
			// Budget not yet created - non-blocking, agent runs without enforcement.
			return nil
		}
		return fmt.Errorf("fetching SwarmBudget %q: %w", agent.Spec.Guardrails.BudgetRef.Name, err)
	}
	if budget.Spec.HardStop && budget.Status.Phase == kubeswarmv1alpha1.BudgetStatusExceeded {
		r.setCondition(agent, kubeswarmv1alpha1.ConditionBudgetExceeded, metav1.ConditionTrue, "BudgetRefExceeded",
			fmt.Sprintf("SwarmBudget %q is exceeded (%s/%s %s); replicas scaled to 0",
				budget.Name, budget.Status.SpentUSD, budget.Spec.Limit, budget.Spec.Currency))
	}
	return nil
}

// reconcileDailyBudget sums token usage from all pipeline steps that reference this
// agent and completed within the last 24 hours. If the sum exceeds spec.limits.maxDailyTokens
// it sets a BudgetExceeded condition (buildDeployment will scale replicas to 0).
// Returns a requeue duration so the controller wakes up when the oldest entry leaves the window.
func (r *SwarmAgentReconciler) reconcileDailyBudget(
	ctx context.Context,
	dep *kubeswarmv1alpha1.SwarmAgent,
) (time.Duration, error) {
	limit := int64(0)
	if dep.Spec.Guardrails != nil && dep.Spec.Guardrails.Limits != nil {
		limit = dep.Spec.Guardrails.Limits.DailyTokens
	}

	now := time.Now().UTC()
	windowStart := now.Add(-24 * time.Hour)

	runs := &kubeswarmv1alpha1.SwarmRunList{}
	listOpts := []client.ListOption{client.InNamespace(dep.Namespace)}
	if teamName, ok := dep.Labels[kubeswarmv1alpha1.LabelTeam]; ok && teamName != "" {
		listOpts = append(listOpts, client.MatchingLabels{kubeswarmv1alpha1.LabelTeam: teamName})
	}
	if err := r.List(ctx, runs, listOpts...); err != nil {
		return 0, fmt.Errorf("listing runs for budget: %w", err)
	}

	var usage kubeswarmv1alpha1.TokenUsage
	// earliestInWindow is the oldest CompletionTime still inside the window.
	// We use it to compute when the window shrinks enough to fall below the limit.
	var earliestInWindow *time.Time

	for i := range runs.Items {
		run := &runs.Items[i]
		// Build role → agentName map from the run's role snapshot (static team roles).
		roleAgent := make(map[string]string, len(run.Spec.Roles))
		for _, role := range run.Spec.Roles {
			roleAgent[role.Name] = resolveRoleAgentName(run.Spec.TeamRef, role)
		}
		for _, st := range run.Status.Steps {
			// Match via registry-resolved agent name first, then static role map.
			resolvedName := st.ResolvedAgent
			if resolvedName == "" {
				resolvedName = roleAgent[st.Name]
			}
			if resolvedName != dep.Name {
				continue
			}
			if st.TokenUsage == nil || st.CompletionTime == nil {
				continue
			}
			t := st.CompletionTime.Time
			if t.Before(windowStart) {
				continue
			}
			usage.InputTokens += st.TokenUsage.InputTokens
			usage.OutputTokens += st.TokenUsage.OutputTokens
			usage.TotalTokens += st.TokenUsage.TotalTokens
			if earliestInWindow == nil || t.Before(*earliestInWindow) {
				earliestInWindow = &t
			}
		}
	}

	// Always record usage so the dashboard shows token consumption even without a limit.
	if usage.TotalTokens > 0 {
		dep.Status.DailyTokenUsage = &usage
	} else {
		dep.Status.DailyTokenUsage = nil
	}

	// Budget enforcement only applies when a limit is configured.
	if limit <= 0 {
		apimeta.RemoveStatusCondition(&dep.Status.Conditions, kubeswarmv1alpha1.ConditionBudgetExceeded)
		return 0, nil
	}

	if usage.TotalTokens >= limit {
		r.setCondition(dep, kubeswarmv1alpha1.ConditionBudgetExceeded, metav1.ConditionTrue, "DailyLimitReached",
			fmt.Sprintf("daily token usage %d exceeds limit %d; replicas scaled to 0", usage.TotalTokens, limit))
		// Dispatch DailyLimitReached notification.
		if r.NotifyDispatcher != nil {
			r.NotifyDispatcher.DispatchDailyLimitReached(context.Background(), dep)
		}
		// Requeue when the oldest entry leaves the window so we can restore replicas.
		if earliestInWindow != nil {
			ttl := earliestInWindow.Add(24 * time.Hour).Sub(now)
			return ttl + time.Minute, nil // +1m buffer to avoid racing the boundary
		}
		return time.Hour, nil
	}

	// Under limit - clear condition.
	apimeta.RemoveStatusCondition(&dep.Status.Conditions, kubeswarmv1alpha1.ConditionBudgetExceeded)
	return 0, nil
}

// mcpHealthDialTimeout is used for TCP-based MCP server health probes.
// 5s is enough to distinguish "server is up" from "server is unreachable".
const mcpHealthDialTimeout = 5 * time.Second

const mcpHealthRequeueInterval = 60 * time.Second

// reconcileMCPHealth probes each configured MCP server with a lightweight TCP dial
// and writes the results to status.toolConnections[]. Sets an MCPDegraded Warning condition
// when one or more servers are unreachable. Returns a requeue duration so the check
// repeats on a regular interval regardless of other reconcile triggers.
//
// A TCP dial is auth-neutral - it verifies that the server is reachable without
// needing credentials. HTTP-based checks gave misleading results because
// authenticated servers return 401/403 which was treated as healthy.
//
// Probe failures are non-blocking: they update status but do not prevent the rest of
// the reconcile loop from completing.
func (r *SwarmAgentReconciler) reconcileMCPHealth(
	agent *kubeswarmv1alpha1.SwarmAgent,
	resolvedMCPServers []kubeswarmv1alpha1.MCPToolSpec,
) time.Duration {
	if len(resolvedMCPServers) == 0 {
		apimeta.RemoveStatusCondition(&agent.Status.Conditions, kubeswarmv1alpha1.ConditionMCPDegraded)
		agent.Status.ToolConnections = nil
		return 0
	}

	now := metav1.Now()
	statuses := make([]kubeswarmv1alpha1.SwarmAgentMCPStatus, len(resolvedMCPServers))
	allHealthy := true

	// Fan out TCP dials concurrently - each dial blocks for up to mcpHealthDialTimeout.
	var wg sync.WaitGroup
	var mu sync.Mutex
	for i, server := range resolvedMCPServers {
		wg.Add(1)
		go func(idx int, srv kubeswarmv1alpha1.MCPToolSpec) {
			defer wg.Done()
			s := kubeswarmv1alpha1.SwarmAgentMCPStatus{
				Name:      srv.Name,
				URL:       srv.URL,
				LastCheck: &now,
				AuthType:  mcpAuthType(srv),
				Trust:     effectiveTrust(srv.Trust, agent),
			}
			u, err := neturl.Parse(srv.URL)
			if err != nil {
				s.Healthy = new(false)
				s.Message = fmt.Sprintf("invalid URL: %v", err)
				mu.Lock()
				allHealthy = false
				mu.Unlock()
			} else {
				host := u.Host
				if !strings.Contains(host, ":") {
					if u.Scheme == "https" {
						host += ":443"
					} else {
						host += ":80"
					}
				}
				conn, dialErr := net.DialTimeout("tcp", host, mcpHealthDialTimeout)
				if dialErr != nil {
					s.Healthy = new(false)
					s.Message = dialErr.Error()
					mu.Lock()
					allHealthy = false
					mu.Unlock()
				} else {
					_ = conn.Close()
					s.Healthy = new(true)
				}
			}
			statuses[idx] = s
		}(i, server)
	}
	wg.Wait()

	agent.Status.ToolConnections = statuses

	if allHealthy {
		apimeta.RemoveStatusCondition(&agent.Status.Conditions, kubeswarmv1alpha1.ConditionMCPDegraded)
	} else {
		r.setCondition(agent, kubeswarmv1alpha1.ConditionMCPDegraded, metav1.ConditionTrue, "MCPUnreachable",
			"one or more MCP servers are unreachable; see status.toolConnections for details")
		// Dispatch AgentDegraded notification when the agent has a notifyRef.
		if r.NotifyDispatcher != nil {
			r.NotifyDispatcher.DispatchAgentDegraded(context.Background(), agent)
		}
	}

	return mcpHealthRequeueInterval
}

// mcpAuthType returns the auth type string for a given MCP server spec.
func mcpAuthType(srv kubeswarmv1alpha1.MCPToolSpec) string {
	if srv.Auth == nil {
		return "none"
	}
	if srv.Auth.Bearer != nil {
		return mcpAuthBearer
	}
	if srv.Auth.MTLS != nil {
		return mcpAuthMTLS
	}
	return "none"
}

// reconcileToolAgentConnections checks all tool-role agent connections and
// returns their status. This mirrors reconcileAdvisorConnections but for
// role=tool entries in spec.agents.
func reconcileToolAgentConnections(
	ctx context.Context,
	c client.Client,
	agent *kubeswarmv1alpha1.SwarmAgent,
) []kubeswarmv1alpha1.ToolAgentConnectionStatus {
	var statuses []kubeswarmv1alpha1.ToolAgentConnectionStatus
	now := metav1.Now()
	for _, conn := range agent.Spec.Agents {
		if conn.Role != kubeswarmv1alpha1.AgentConnectionRoleTool {
			continue
		}
		s := kubeswarmv1alpha1.ToolAgentConnectionStatus{
			Name:               conn.Name,
			Trust:              effectiveTrust(conn.Trust, agent),
			LastTransitionTime: now,
		}
		if conn.AgentRef != nil {
			target := &kubeswarmv1alpha1.SwarmAgent{}
			if err := c.Get(ctx, client.ObjectKey{
				Name:      conn.AgentRef.Name,
				Namespace: agent.Namespace,
			}, target); err == nil {
				s.Ready = target.Status.ReadyReplicas > 0
			}
		}
		statuses = append(statuses, s)
	}
	return statuses
}

// runToAgents maps a changed SwarmRun to all SwarmAgents referenced by its roles,
// so budget recalculations fire automatically when run steps complete.
func (r *SwarmAgentReconciler) runToAgents(ctx context.Context, obj client.Object) []reconcile.Request {
	run, ok := obj.(*kubeswarmv1alpha1.SwarmRun)
	if !ok {
		return nil
	}
	seen := make(map[string]struct{})
	var reqs []reconcile.Request

	// Standalone agent runs: map directly to the named agent.
	if run.Spec.Agent != "" {
		seen[run.Spec.Agent] = struct{}{}
		reqs = append(reqs, reconcile.Request{
			NamespacedName: types.NamespacedName{
				Name:      run.Spec.Agent,
				Namespace: run.Namespace,
			},
		})
	}

	// Team runs: map via roles.
	for _, role := range run.Spec.Roles {
		agentName := resolveRoleAgentName(run.Spec.TeamRef, role)
		if _, dup := seen[agentName]; dup {
			continue
		}
		seen[agentName] = struct{}{}
		reqs = append(reqs, reconcile.Request{
			NamespacedName: types.NamespacedName{
				Name:      agentName,
				Namespace: run.Namespace,
			},
		})
	}
	return reqs
}

// resolveSystemPrompt returns the effective system prompt text for swarmAgent.
// If spec.systemPromptRef is set it reads from the referenced ConfigMap or Secret.
// Falls back to spec.systemPrompt when no ref is configured.
// resolveMCPServers returns a copy of the agent's MCPServers with any capabilityRef
// or swarmAgentRef entries resolved to concrete URLs.
//   - capabilityRef entries are resolved via SwarmRegistry mcpBindings in the namespace.
//   - swarmAgentRef entries are resolved to the MCP gateway URL for the target agent.
//
// All SwarmRegistries in the namespace are merged (last binding wins on conflict).
// Returns an error if any entry cannot be resolved.
func (r *SwarmAgentReconciler) resolveMCPServers(
	ctx context.Context,
	agent *kubeswarmv1alpha1.SwarmAgent,
) ([]kubeswarmv1alpha1.MCPToolSpec, error) {
	if agent.Spec.Tools == nil {
		return nil, nil
	}
	mcpServers := agent.Spec.Tools.MCP

	// Fast path: no entries requiring resolution - return spec as-is.
	needsResolution := false
	for _, s := range mcpServers {
		if s.CapabilityRef != "" {
			needsResolution = true
			break
		}
	}
	if !needsResolution {
		return mcpServers, nil
	}

	// Build a capabilityID → URL map from all SwarmRegistries in the namespace
	// (only needed when at least one capabilityRef is present).
	var bindings map[string]string
	for _, s := range mcpServers {
		if s.CapabilityRef != "" {
			bindings = make(map[string]string)
			registryList := &kubeswarmv1alpha1.SwarmRegistryList{}
			if err := r.List(ctx, registryList, client.InNamespace(agent.Namespace)); err != nil {
				return nil, fmt.Errorf("listing SwarmRegistries: %w", err)
			}
			for _, reg := range registryList.Items {
				for _, b := range reg.Spec.MCPBindings {
					bindings[b.CapabilityID] = b.URL
				}
			}
			break
		}
	}

	resolved := make([]kubeswarmv1alpha1.MCPToolSpec, len(mcpServers))
	for i, s := range mcpServers {
		switch {
		case s.CapabilityRef != "":
			url, ok := bindings[s.CapabilityRef]
			if !ok {
				return nil, fmt.Errorf("MCP server %q: capabilityRef %q not found in any SwarmRegistry in namespace %q",
					s.Name, s.CapabilityRef, agent.Namespace)
			}
			resolved[i] = s
			resolved[i].URL = url

		default:
			resolved[i] = s
		}
	}
	return resolved, nil
}

func (r *SwarmAgentReconciler) resolveSystemPrompt(
	ctx context.Context,
	dep *kubeswarmv1alpha1.SwarmAgent,
) (string, error) {
	if dep.Spec.Prompt == nil {
		return "", nil
	}
	if dep.Spec.Prompt.From == nil {
		return dep.Spec.Prompt.Inline, nil
	}
	ref := dep.Spec.Prompt.From
	switch {
	case ref.ConfigMapKeyRef != nil:
		cm := &corev1.ConfigMap{}
		if err := r.Get(ctx, types.NamespacedName{
			Name:      ref.ConfigMapKeyRef.Name,
			Namespace: dep.Namespace,
		}, cm); err != nil {
			return "", fmt.Errorf("reading ConfigMap %q for prompt.from: %w", ref.ConfigMapKeyRef.Name, err)
		}
		val, ok := cm.Data[ref.ConfigMapKeyRef.Key]
		if !ok {
			return "", fmt.Errorf("key %q not found in ConfigMap %q", ref.ConfigMapKeyRef.Key, ref.ConfigMapKeyRef.Name)
		}
		return val, nil
	case ref.SecretKeyRef != nil:
		sec := &corev1.Secret{}
		if err := r.Get(ctx, types.NamespacedName{
			Name:      ref.SecretKeyRef.Name,
			Namespace: dep.Namespace,
		}, sec); err != nil {
			return "", fmt.Errorf("reading Secret %q for prompt.from: %w", ref.SecretKeyRef.Name, err)
		}
		val, ok := sec.Data[ref.SecretKeyRef.Key]
		if !ok {
			return "", fmt.Errorf("key %q not found in Secret %q", ref.SecretKeyRef.Key, ref.SecretKeyRef.Name)
		}
		return string(val), nil
	default:
		return dep.Spec.Prompt.Inline, nil
	}
}

// configMapToAgents re-enqueues SwarmAgents that reference a changed ConfigMap
// via spec.prompt.from, so prompt content changes trigger automatic rolling restarts.
func (r *SwarmAgentReconciler) configMapToAgents(ctx context.Context, obj client.Object) []reconcile.Request {
	cm, ok := obj.(*corev1.ConfigMap)
	if !ok {
		return nil
	}
	agents := &kubeswarmv1alpha1.SwarmAgentList{}
	if err := r.List(ctx, agents,
		client.InNamespace(cm.Namespace),
		client.MatchingFields{agentPromptConfigMapIndex: cm.Name},
	); err != nil {
		return nil
	}
	reqs := make([]reconcile.Request, 0, len(agents.Items))
	for _, dep := range agents.Items {
		reqs = append(reqs, reconcile.Request{
			NamespacedName: types.NamespacedName{Name: dep.Name, Namespace: dep.Namespace},
		})
	}
	return reqs
}

// secretToAgents re-enqueues SwarmAgents that reference a changed Secret
// via spec.prompt.from.
func (r *SwarmAgentReconciler) secretToAgents(ctx context.Context, obj client.Object) []reconcile.Request {
	sec, ok := obj.(*corev1.Secret)
	if !ok {
		return nil
	}
	agents := &kubeswarmv1alpha1.SwarmAgentList{}
	if err := r.List(ctx, agents,
		client.InNamespace(sec.Namespace),
		client.MatchingFields{agentPromptSecretIndex: sec.Name},
	); err != nil {
		return nil
	}
	reqs := make([]reconcile.Request, 0, len(agents.Items))
	for _, dep := range agents.Items {
		reqs = append(reqs, reconcile.Request{
			NamespacedName: types.NamespacedName{Name: dep.Name, Namespace: dep.Namespace},
		})
	}
	return reqs
}

// reconcileAgentServiceAccount ensures the swarm-agent ServiceAccount, Role, and RoleBinding
// exist in the SwarmAgent's namespace. Agent pods use this SA to emit K8s Events for audit logging.
// The Role grants only create;patch on events in the namespace (principle of least privilege).
func (r *SwarmAgentReconciler) reconcileAgentServiceAccount(
	ctx context.Context,
	swarmAgent *kubeswarmv1alpha1.SwarmAgent,
) error {
	ns := swarmAgent.Namespace

	// Fast path: skip the 3 Gets when we've already confirmed all resources exist.
	if _, ok := r.saEnsured.Load(ns); ok {
		return nil
	}

	// ServiceAccount.
	sa := &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{Name: agentServiceAccount, Namespace: ns},
	}
	existingSA := &corev1.ServiceAccount{}
	if err := r.Get(ctx, client.ObjectKeyFromObject(sa), existingSA); errors.IsNotFound(err) {
		if err := r.Create(ctx, sa); err != nil {
			return fmt.Errorf("creating agent ServiceAccount: %w", err)
		}
	} else if err != nil {
		return fmt.Errorf("getting agent ServiceAccount: %w", err)
	}

	// Role - events only, namespace-scoped.
	role := &rbacv1.Role{
		ObjectMeta: metav1.ObjectMeta{Name: agentServiceAccount, Namespace: ns},
		Rules: []rbacv1.PolicyRule{{
			APIGroups: []string{""},
			Resources: []string{"events"},
			Verbs:     []string{"create", "patch"},
		}},
	}
	existingRole := &rbacv1.Role{}
	if err := r.Get(ctx, client.ObjectKeyFromObject(role), existingRole); errors.IsNotFound(err) {
		if err := r.Create(ctx, role); err != nil {
			return fmt.Errorf("creating agent Role: %w", err)
		}
	} else if err != nil {
		return fmt.Errorf("getting agent Role: %w", err)
	}

	// RoleBinding.
	rb := &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: agentServiceAccount, Namespace: ns},
		Subjects: []rbacv1.Subject{{
			Kind:      "ServiceAccount",
			Name:      agentServiceAccount,
			Namespace: ns,
		}},
		RoleRef: rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "Role",
			Name:     agentServiceAccount,
		},
	}
	existingRB := &rbacv1.RoleBinding{}
	if err := r.Get(ctx, client.ObjectKeyFromObject(rb), existingRB); errors.IsNotFound(err) {
		if err := r.Create(ctx, rb); err != nil {
			return fmt.Errorf("creating agent RoleBinding: %w", err)
		}
	} else if err != nil {
		return fmt.Errorf("getting agent RoleBinding: %w", err)
	}

	r.saEnsured.Store(ns, struct{}{})
	return nil
}

// resolveAPIKeyEnvVar returns the corev1.EnvVar to inject for the API key and the
// ResourceVersion of the referenced k8s Secret (used as a rolling-restart trigger).
// Returns (nil, "", nil) when spec.apiKeyRef is not set.
//
// The env var name is the Secret key itself (e.g. key "ANTHROPIC_API_KEY" in Secret
// "my-keys" produces env var ANTHROPIC_API_KEY sourced from that Secret).
func (r *SwarmAgentReconciler) resolveAPIKeyEnvVar(
	ctx context.Context,
	swarmAgent *kubeswarmv1alpha1.SwarmAgent,
) (*corev1.EnvVar, string, error) {
	if swarmAgent.Spec.Infrastructure == nil || swarmAgent.Spec.Infrastructure.APIKeyRef == nil {
		return nil, "", nil
	}
	ref := swarmAgent.Spec.Infrastructure.APIKeyRef

	// Fetch the Secret to get its ResourceVersion for rolling-restart detection.
	secret := &corev1.Secret{}
	if err := r.Get(ctx, types.NamespacedName{
		Namespace: swarmAgent.Namespace,
		Name:      ref.Name,
	}, secret); err != nil {
		return nil, "", fmt.Errorf("fetching Secret %q for apiKeyRef: %w", ref.Name, err)
	}

	envVar := corev1.EnvVar{
		Name: ref.Key,
		ValueFrom: &corev1.EnvVarSource{
			SecretKeyRef: &corev1.SecretKeySelector{
				LocalObjectReference: corev1.LocalObjectReference{Name: ref.Name},
				Key:                  ref.Key,
			},
		},
	}
	return &envVar, secret.ResourceVersion, nil
}

// loadSettingsRefs fetches all SwarmSettings referenced by the agent in list order.
// If spec.settings is absent the returned slice is empty (no settings applied).
// A missing SwarmSettings object is treated as an error - the user must fix the reference.
func (r *SwarmAgentReconciler) loadSettingsRefs(
	ctx context.Context,
	swarmAgent *kubeswarmv1alpha1.SwarmAgent,
) ([]kubeswarmv1alpha1.SwarmSettings, error) {
	refs := swarmAgent.Spec.Settings
	if len(refs) == 0 {
		return nil, nil
	}
	result := make([]kubeswarmv1alpha1.SwarmSettings, 0, len(refs))
	for _, ref := range refs {
		s := kubeswarmv1alpha1.SwarmSettings{}
		if err := r.Get(ctx, client.ObjectKey{
			Name:      ref.Name,
			Namespace: swarmAgent.Namespace,
		}, &s); err != nil {
			return nil, fmt.Errorf("fetching SwarmSettings %q: %w", ref.Name, err)
		}
		result = append(result, s)
	}
	return result, nil
}

// settingsToAgents re-enqueues SwarmAgents that reference a changed SwarmSettings object
// via spec.settingsRefs or the deprecated spec.configRef, so that settings changes
// (new fragments, temperature, etc.) trigger an automatic rolling restart.
func (r *SwarmAgentReconciler) settingsToAgents(ctx context.Context, obj client.Object) []reconcile.Request {
	settings, ok := obj.(*kubeswarmv1alpha1.SwarmSettings)
	if !ok {
		return nil
	}
	agentList := &kubeswarmv1alpha1.SwarmAgentList{}
	if err := r.List(ctx, agentList, client.InNamespace(settings.Namespace)); err != nil {
		return nil
	}
	var reqs []reconcile.Request
	for _, agent := range agentList.Items {
		for _, ref := range agent.Spec.Settings {
			if ref.Name == settings.Name {
				reqs = append(reqs, reconcile.Request{
					NamespacedName: types.NamespacedName{Name: agent.Name, Namespace: agent.Namespace},
				})
				break
			}
		}
	}
	return reqs
}

const (
	agentPromptConfigMapIndex = ".spec.prompt.from.configMapKeyRef.name"
	agentPromptSecretIndex    = ".spec.prompt.from.secretKeyRef.name" //nolint:gosec // field index path, not a credential
)

// SetupWithManager sets up the controller with the Manager.
func (r *SwarmAgentReconciler) SetupWithManager(mgr ctrl.Manager) error {
	ctx := context.Background()
	indexer := mgr.GetFieldIndexer()

	if err := indexer.IndexField(ctx, &kubeswarmv1alpha1.SwarmAgent{}, agentPromptConfigMapIndex, func(obj client.Object) []string {
		agent := obj.(*kubeswarmv1alpha1.SwarmAgent)
		if agent.Spec.Prompt != nil && agent.Spec.Prompt.From != nil && agent.Spec.Prompt.From.ConfigMapKeyRef != nil {
			return []string{agent.Spec.Prompt.From.ConfigMapKeyRef.Name}
		}
		return nil
	}); err != nil {
		return err
	}
	if err := indexer.IndexField(ctx, &kubeswarmv1alpha1.SwarmAgent{}, agentPromptSecretIndex, func(obj client.Object) []string {
		agent := obj.(*kubeswarmv1alpha1.SwarmAgent)
		if agent.Spec.Prompt != nil && agent.Spec.Prompt.From != nil && agent.Spec.Prompt.From.SecretKeyRef != nil {
			return []string{agent.Spec.Prompt.From.SecretKeyRef.Name}
		}
		return nil
	}); err != nil {
		return err
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&kubeswarmv1alpha1.SwarmAgent{}).
		Owns(&appsv1.Deployment{}).
		Owns(&networkingv1.NetworkPolicy{}).
		// Re-evaluate daily budget whenever a run step completes.
		Watches(
			&kubeswarmv1alpha1.SwarmRun{},
			handler.EnqueueRequestsFromMapFunc(r.runToAgents),
		).
		// Trigger rolling restart when the referenced system prompt content changes.
		Watches(
			&corev1.ConfigMap{},
			handler.EnqueueRequestsFromMapFunc(r.configMapToAgents),
		).
		Watches(
			&corev1.Secret{},
			handler.EnqueueRequestsFromMapFunc(r.secretToAgents),
		).
		// Trigger rolling restart when referenced SwarmSettings change (new fragments, guidance, etc.).
		Watches(
			&kubeswarmv1alpha1.SwarmSettings{},
			handler.EnqueueRequestsFromMapFunc(r.settingsToAgents),
		).
		// Re-evaluate gateway capabilities when any agent changes readiness
		// (new capability agents becoming ready, agents being deleted, etc.).
		Watches(
			&kubeswarmv1alpha1.SwarmAgent{},
			handler.EnqueueRequestsFromMapFunc(r.agentToGateways),
		).
		Named("swarmagent").
		Complete(WithMetrics(r, "swarmagent"))
}

// agentToGateways maps a changed SwarmAgent to all gateway agents in the same
// namespace so they re-evaluate their capability list when agents become ready,
// gain capabilities, or are deleted.
func (r *SwarmAgentReconciler) agentToGateways(ctx context.Context, obj client.Object) []reconcile.Request {
	agent, ok := obj.(*kubeswarmv1alpha1.SwarmAgent)
	if !ok {
		return nil
	}
	// Don't re-enqueue gateway agents for their own changes (avoid infinite loop).
	if agent.Spec.Gateway != nil {
		return nil
	}
	// Only react when the agent has capabilities (potential gateway targets).
	if len(agent.Spec.Capabilities) == 0 {
		return nil
	}

	var agents kubeswarmv1alpha1.SwarmAgentList
	if err := r.List(ctx, &agents, client.InNamespace(agent.Namespace)); err != nil {
		return nil
	}
	var reqs []reconcile.Request
	for _, a := range agents.Items {
		if a.Spec.Gateway != nil {
			reqs = append(reqs, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      a.Name,
					Namespace: a.Namespace,
				},
			})
		}
	}
	return reqs
}

// agentInjectEnvVars reads env vars prefixed with SWARM_AGENT_INJECT_ from the
// operator pod's environment and returns them as agent pod env vars (prefix stripped).
// This lets helm values.agentExtraEnv propagate cluster-wide defaults to every agent
// pod without requiring a per-namespace secret (e.g. OPENAI_BASE_URL, AGENT_PROVIDER).
// Cached at first call since the operator's environment is static after startup.
func agentInjectEnvVars() []corev1.EnvVar {
	agentInjectOnce.Do(func() {
		const prefix = "SWARM_AGENT_INJECT_"
		for _, kv := range os.Environ() {
			if !strings.HasPrefix(kv, prefix) {
				continue
			}
			idx := strings.IndexByte(kv, '=')
			if idx < 0 {
				continue
			}
			agentInjectCache = append(agentInjectCache, corev1.EnvVar{
				Name:  kv[len(prefix):idx],
				Value: kv[idx+1:],
			})
		}
	})
	return agentInjectCache
}

var (
	agentInjectOnce  sync.Once
	agentInjectCache []corev1.EnvVar
)

// clusterAuditConfig holds the static cluster-level audit configuration read from
// operator env vars at startup. Cached with sync.Once since the operator's
// environment is static after startup.
type clusterAuditConfig struct {
	mode           string
	sink           string
	maxDetailBytes int
	redisURL       string
	maxStreamLen   int64
	webhookURL     string
	redact         []string
}

var (
	clusterAuditOnce   sync.Once
	clusterAuditCached clusterAuditConfig
)

func getClusterAuditConfig() clusterAuditConfig {
	clusterAuditOnce.Do(func() {
		clusterAuditCached.mode = os.Getenv("AUDIT_LOG_MODE")
		clusterAuditCached.sink = os.Getenv("AUDIT_LOG_SINK")
		if clusterAuditCached.sink == "" {
			clusterAuditCached.sink = "stdout"
		}
		if v := os.Getenv("AUDIT_LOG_MAX_DETAIL_BYTES"); v != "" {
			if n, err := strconv.Atoi(v); err == nil {
				clusterAuditCached.maxDetailBytes = n
			}
		}
		clusterAuditCached.redisURL = os.Getenv("AUDIT_LOG_REDIS_URL")
		clusterAuditCached.webhookURL = os.Getenv("AUDIT_LOG_WEBHOOK_URL")
		if v := os.Getenv("AUDIT_LOG_MAX_STREAM_LEN"); v != "" {
			if n, err := strconv.ParseInt(v, 10, 64); err == nil {
				clusterAuditCached.maxStreamLen = n
			}
		}
		if v := os.Getenv("AUDIT_LOG_REDACT"); v != "" {
			_ = json.Unmarshal([]byte(v), &clusterAuditCached.redact)
		}
	})
	return clusterAuditCached
}

// buildAuditLogEnvVar builds the AGENT_AUDIT_LOG JSON value from the operator's
// cached cluster config merged with any agent-level spec.observability.auditLog override.
func buildAuditLogEnvVar(agent *kubeswarmv1alpha1.SwarmAgent) string {
	cluster := getClusterAuditConfig()

	mode := cluster.mode
	if mode == "" || mode == string(kubeswarmv1alpha1.AuditLogModeOff) {
		// Check if agent-level override enables audit.
		if agent.Spec.Observability != nil &&
			agent.Spec.Observability.AuditLog != nil &&
			agent.Spec.Observability.AuditLog.Mode != "" &&
			agent.Spec.Observability.AuditLog.Mode != kubeswarmv1alpha1.AuditLogModeOff {
			mode = string(agent.Spec.Observability.AuditLog.Mode)
		} else {
			return ""
		}
	}

	// Agent-level mode overrides cluster-level.
	if agent.Spec.Observability != nil &&
		agent.Spec.Observability.AuditLog != nil &&
		agent.Spec.Observability.AuditLog.Mode != "" {
		mode = string(agent.Spec.Observability.AuditLog.Mode)
		if mode == string(kubeswarmv1alpha1.AuditLogModeOff) {
			return ""
		}
	}

	cfg := map[string]any{
		"mode": mode,
		"sink": cluster.sink,
	}
	if cluster.maxDetailBytes > 0 {
		cfg["maxDetailBytes"] = cluster.maxDetailBytes
	}
	if cluster.redisURL != "" {
		cfg["redisURL"] = cluster.redisURL
	}
	if cluster.maxStreamLen > 0 {
		cfg["maxStreamLen"] = cluster.maxStreamLen
	}
	if cluster.webhookURL != "" {
		cfg["webhookURL"] = cluster.webhookURL
	}

	// Merge redaction patterns: cluster + agent level.
	redact := append([]string{}, cluster.redact...)
	if agent.Spec.Observability != nil &&
		agent.Spec.Observability.AuditLog != nil {
		redact = append(redact, agent.Spec.Observability.AuditLog.Redact...)
	}
	if len(redact) > 0 {
		cfg["redact"] = redact
	}

	raw, err := json.Marshal(cfg)
	if err != nil {
		return ""
	}
	return string(raw)
}

// agentImagePullPolicy returns the configured pull policy, defaulting to Always.
func (r *SwarmAgentReconciler) agentImagePullPolicy() corev1.PullPolicy {
	if r.AgentImagePullPolicy != "" {
		return r.AgentImagePullPolicy
	}
	return corev1.PullAlways
}

// agentImagePullSecrets returns imagePullSecrets for an agent pod.
// Per-agent spec.runtime.imagePullSecrets takes precedence over the
// operator-level --agent-image-pull-secrets flag.
func (r *SwarmAgentReconciler) agentImagePullSecrets(agent *kubeswarmv1alpha1.SwarmAgent) []corev1.LocalObjectReference {
	if len(agent.Spec.Runtime.ImagePullSecrets) > 0 {
		return agent.Spec.Runtime.ImagePullSecrets
	}
	return r.AgentImagePullSecrets
}

func (r *SwarmAgentReconciler) setCondition(
	swarmAgent *kubeswarmv1alpha1.SwarmAgent,
	condType string,
	status metav1.ConditionStatus,
	reason, message string,
) {
	setCondition(&swarmAgent.Status.Conditions, swarmAgent.Generation, condType, status, reason, message)
}

// effectiveTrust returns the explicit trust level if set, otherwise falls back
// to the agent's guardrails.tools.trust.default. Returns "external" as the
// ultimate fallback (matching the CRD default for ToolTrustPolicy.Default).
func effectiveTrust(explicit kubeswarmv1alpha1.ToolTrustLevel, agent *kubeswarmv1alpha1.SwarmAgent) kubeswarmv1alpha1.ToolTrustLevel {
	if explicit != "" {
		return explicit
	}
	if agent.Spec.Guardrails != nil &&
		agent.Spec.Guardrails.Tools != nil &&
		agent.Spec.Guardrails.Tools.Trust != nil &&
		agent.Spec.Guardrails.Tools.Trust.Default != "" {
		return agent.Spec.Guardrails.Tools.Trust.Default
	}
	return kubeswarmv1alpha1.ToolTrustExternal
}
