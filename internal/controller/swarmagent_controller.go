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
	"slices"
	"strconv"
	"strings"
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
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kubeswarmv1alpha1 "github.com/kubeswarm/kubeswarm/api/v1alpha1"
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

	// Annotations set by the SwarmTeam controller on SwarmAgent resources.
	// Explicit env vars injected from these take precedence over EnvFrom values.
	annotationTeamQueueURL      = "kubeswarm/team-queue-url"
	annotationTeamRoutes        = "kubeswarm/team-routes"
	annotationTeamRole          = "kubeswarm/team-role"
	annotationTeamArtifactStore = "kubeswarm/team-artifact-store-url"
	annotationTeamArtifactClaim = "kubeswarm/team-artifact-claim"

	// MCP server auth types used when building runtime config and volumes.
	mcpAuthBearer = "bearer"
	mcpAuthMTLS   = "mtls"

	// Default resource constraints injected into agent pods when spec.resources is not set.
	// These ensure every agent pod has explicit limits, preventing a runaway agent from
	// consuming unbounded node resources (RFC-0016 Phase 1).
	defaultCPURequest            = "100m"
	defaultCPULimit              = "500m"
	defaultMemoryRequest         = "128Mi"
	defaultMemoryLimit           = "512Mi"
	defaultEphemeralStorageLimit = "256Mi"
)

// defaultAgentResources returns the safe default resource requirements injected into
// agent pods when spec.resources is not explicitly set.
func defaultAgentResources() corev1.ResourceRequirements {
	return corev1.ResourceRequirements{
		Requests: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse(defaultCPURequest),
			corev1.ResourceMemory: resource.MustParse(defaultMemoryRequest),
		},
		Limits: corev1.ResourceList{
			corev1.ResourceCPU:              resource.MustParse(defaultCPULimit),
			corev1.ResourceMemory:           resource.MustParse(defaultMemoryLimit),
			corev1.ResourceEphemeralStorage: resource.MustParse(defaultEphemeralStorageLimit),
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
		r.Limits[corev1.ResourceEphemeralStorage] = resource.MustParse(defaultEphemeralStorageLimit)
	}
	return r
}

// SwarmAgentReconciler reconciles a SwarmAgent object
type SwarmAgentReconciler struct {
	client.Client
	Scheme               *runtime.Scheme
	AgentImage           string
	AgentImagePullPolicy corev1.PullPolicy
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
// +kubebuilder:rbac:groups=kubeswarm.io,resources=swarmregistries,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=kubeswarm.io,resources=swarmruns,verbs=get;list;watch
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=pods,verbs=get;list;watch
// +kubebuilder:rbac:groups=core,resources=configmaps,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups=core,resources=secrets,verbs=get;list;watch
// +kubebuilder:rbac:groups=core,resources=events,verbs=create;patch
// +kubebuilder:rbac:groups=core,resources=serviceaccounts,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=roles;rolebindings,verbs=get;list;watch;create;update;patch

func (r *SwarmAgentReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// 1. Fetch the SwarmAgent CR.
	swarmAgent := &kubeswarmv1alpha1.SwarmAgent{}
	if err := r.Get(ctx, req.NamespacedName, swarmAgent); err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// 2. OwnerRef handles child cleanup on deletion — nothing extra needed.
	if !swarmAgent.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, nil
	}

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

	// 3b. Optionally load the referenced SwarmMemory.
	var swarmMemory *kubeswarmv1alpha1.SwarmMemory
	if swarmAgent.Spec.Runtime.Loop != nil &&
		swarmAgent.Spec.Runtime.Loop.Memory != nil && swarmAgent.Spec.Runtime.Loop.Memory.Ref != nil {
		mem := &kubeswarmv1alpha1.SwarmMemory{}
		if err := r.Get(ctx, client.ObjectKey{
			Name:      swarmAgent.Spec.Runtime.Loop.Memory.Ref.Name,
			Namespace: swarmAgent.Namespace,
		}, mem); err != nil {
			if !errors.IsNotFound(err) {
				return ctrl.Result{}, fmt.Errorf("fetching SwarmMemory %q: %w", swarmAgent.Spec.Runtime.Loop.Memory.Ref.Name, err)
			}
			logger.Info("SwarmMemory not found, proceeding without it", "memoryRef", swarmAgent.Spec.Runtime.Loop.Memory.Ref.Name)
		} else {
			swarmMemory = mem
		}
	}

	// 3c. Resolve the effective system prompt (inline or from ConfigMap/Secret).
	resolvedPrompt, err := r.resolveSystemPrompt(ctx, swarmAgent)
	if err != nil {
		logger.Error(err, "failed to resolve systemPrompt")
		r.setCondition(swarmAgent, kubeswarmv1alpha1.ConditionReady, metav1.ConditionFalse, "PromptResolutionError", err.Error())
		_ = r.Status().Update(ctx, swarmAgent)
		return ctrl.Result{}, err
	}

	// Record the hash of the resolved prompt in status so the admission webhook can
	// detect and block unauthorised system prompt changes (RFC-0016 Phase 4).
	swarmAgent.Status.SystemPromptHash = hashPrompt(resolvedPrompt)

	// 3d. Resolve the API key from SwarmSecret (if configured).
	apiKeyEnvVar, apiKeyVersion, err := r.resolveAPIKeyEnvVar(ctx, swarmAgent)
	if err != nil {
		logger.Error(err, "failed to resolve apiKeyRef")
		r.setCondition(swarmAgent, kubeswarmv1alpha1.ConditionReady, metav1.ConditionFalse, "APIKeyResolutionError", err.Error())
		_ = r.Status().Update(ctx, swarmAgent)
		return ctrl.Result{}, err
	}

	// 3e. Resolve capabilityRef entries in MCPServers via the namespace's SwarmRegistry.
	resolvedMCPServers, err := r.resolveMCPServers(ctx, swarmAgent)
	if err != nil {
		logger.Error(err, "failed to resolve MCP capabilityRefs")
		r.setCondition(swarmAgent, kubeswarmv1alpha1.ConditionReady, metav1.ConditionFalse, "MCPResolutionError", err.Error())
		_ = r.Status().Update(ctx, swarmAgent)
		return ctrl.Result{}, err
	}

	// 3f. Validate registryRef resolves; emit RegistryNotFound condition when absent.
	// Non-blocking — the agent starts regardless of registry state.
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
		_ = r.Status().Update(ctx, swarmAgent)
		return ctrl.Result{}, err
	}

	// 5b. Reconcile the prompt ConfigMap so the system prompt is mounted as a file
	// instead of injected as an env var (avoids size limits and plaintext exposure).
	assembledPrompt := assembleSystemPrompt(resolvedPrompt, allSettings, resolvedMCPServers)
	if err := r.reconcilePromptConfigMap(ctx, swarmAgent, assembledPrompt); err != nil {
		logger.Error(err, "failed to reconcile prompt ConfigMap")
		r.setCondition(swarmAgent, kubeswarmv1alpha1.ConditionReady, metav1.ConditionFalse, "ReconcileError", err.Error())
		_ = r.Status().Update(ctx, swarmAgent)
		return ctrl.Result{}, err
	}

	// 6. Reconcile the owned k8s Deployment (budget check may override replicas to 0).
	if err := r.reconcileDeployment(ctx, swarmAgent, allSettings, swarmMemory, resolvedPrompt, apiKeyEnvVar, apiKeyVersion, resolvedMCPServers); err != nil {
		logger.Error(err, "failed to reconcile Deployment")
		r.setCondition(swarmAgent, kubeswarmv1alpha1.ConditionReady, metav1.ConditionFalse, "ReconcileError", err.Error())
		_ = r.Status().Update(ctx, swarmAgent)
		return ctrl.Result{}, err
	}

	// 7. Reconcile the NetworkPolicy for this agent (RFC-0016 Phase 2).
	if err := r.reconcileNetworkPolicy(ctx, swarmAgent, resolvedMCPServers); err != nil {
		logger.Error(err, "failed to reconcile NetworkPolicy")
		r.setCondition(swarmAgent, kubeswarmv1alpha1.ConditionReady, metav1.ConditionFalse, "NetworkPolicyError", err.Error())
		_ = r.Status().Update(ctx, swarmAgent)
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

	// 8. Probe MCP server health and surface results in status.toolConnections[].
	mcpRequeue, err := r.reconcileMCPHealth(ctx, swarmAgent, resolvedMCPServers)
	if err != nil {
		logger.Error(err, "failed to reconcile MCP health")
	}
	if mcpRequeue > 0 && (requeueAfter == 0 || mcpRequeue < requeueAfter) {
		requeueAfter = mcpRequeue
	}

	// 9. Reconcile KEDA ScaledObject when autoscaling is configured.
	if err := r.reconcileKEDA(ctx, swarmAgent); err != nil {
		logger.Error(err, "failed to reconcile KEDA ScaledObject")
	}

	return ctrl.Result{RequeueAfter: requeueAfter}, nil
}

func (r *SwarmAgentReconciler) reconcileDeployment(
	ctx context.Context,
	swarmAgent *kubeswarmv1alpha1.SwarmAgent,
	allSettings []kubeswarmv1alpha1.SwarmSettings,
	swarmMemory *kubeswarmv1alpha1.SwarmMemory,
	resolvedPrompt string,
	apiKeyEnvVar *corev1.EnvVar,
	apiKeyVersion string,
	resolvedMCPServers []kubeswarmv1alpha1.MCPToolSpec,
) error {
	desired := r.buildDeployment(swarmAgent, allSettings, swarmMemory, resolvedPrompt, apiKeyEnvVar, apiKeyVersion, resolvedMCPServers)

	if err := ctrl.SetControllerReference(swarmAgent, desired, r.Scheme); err != nil {
		return err
	}

	existing := &appsv1.Deployment{}
	err := r.Get(ctx, client.ObjectKeyFromObject(desired), existing)
	if errors.IsNotFound(err) {
		return r.Create(ctx, desired)
	}
	if err != nil {
		return err
	}

	patch := client.MergeFrom(existing.DeepCopy())
	existing.Spec.Replicas = desired.Spec.Replicas
	existing.Spec.Template.Annotations = desired.Spec.Template.Annotations
	// Only update the fields that the operator controls — env vars, image, and pull policy.
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
		// Delete existing policy if present — user manages network policy externally.
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

	dnsPort53UDP := intstrFromInt32(53)
	dnsPort53TCP := intstrFromInt32(53)
	redisPort := intstrFromInt32(6379)
	httpsPort := intstrFromInt32(443)

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
		// and no To selector allows all egress — DNS and Redis rules above are subsets.
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
func resolveMCPIPs(servers []kubeswarmv1alpha1.MCPToolSpec) ([]networkingv1.NetworkPolicyPeer, error) {
	seen := make(map[string]struct{})
	var peers []networkingv1.NetworkPolicyPeer
	for _, s := range servers {
		u, err := neturl.Parse(s.URL)
		if err != nil || u.Hostname() == "" {
			continue
		}
		addrs, err := net.LookupHost(u.Hostname())
		if err != nil {
			return nil, fmt.Errorf("DNS lookup for MCP server %q (%s): %w", s.Name, u.Hostname(), err)
		}
		for _, addr := range addrs {
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
	r.setCondition(swarmAgent, kubeswarmv1alpha1.ConditionReady, condStatus, condReason, condMsg)

	return r.Status().Update(ctx, swarmAgent)
}

func (r *SwarmAgentReconciler) buildDeployment(swarmAgent *kubeswarmv1alpha1.SwarmAgent, allSettings []kubeswarmv1alpha1.SwarmSettings, swarmMemory *kubeswarmv1alpha1.SwarmMemory, resolvedPrompt string, apiKeyEnvVar *corev1.EnvVar, apiKeyVersion string, resolvedMCPServers []kubeswarmv1alpha1.MCPToolSpec) *appsv1.Deployment {
	// Assemble the full system prompt (fragments + MCP guidance) before hashing so that
	// changes to referenced SwarmSettings or MCPToolSpec.instructions trigger rolling restarts.
	assembledPrompt := assembleSystemPrompt(resolvedPrompt, allSettings, resolvedMCPServers)
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
	if apimeta.IsStatusConditionTrue(swarmAgent.Status.Conditions, "BudgetExceeded") {
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
						"kubeswarm/system-prompt-hash": promptHash,
						// ResourceVersion of the referenced k8s Secret — triggers rolling restart on key rotation.
						"kubeswarm/api-key-version": apiKeyVersion,
					},
				},
				Spec: corev1.PodSpec{
					ServiceAccountName: agentServiceAccount,
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
						// Resource limits — use spec.resources if set, otherwise inject safe defaults.
						// Ephemeral storage limit is always added to prevent /tmp exhaustion.
						Resources: agentResources(swarmAgent),
						Env:       r.buildEnvVars(swarmAgent, allSettings, swarmMemory, apiKeyEnvVar, resolvedMCPServers),
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
							return base
						}(),
						// mTLS Secret volumes for MCP servers (RFC-0016 Phase 5).
						// Artifact PVC mount (RFC-0013) appended when configured.
						VolumeMounts: buildVolumeMounts(swarmAgent, resolvedMCPServers),
						LivenessProbe: &corev1.Probe{
							ProbeHandler: corev1.ProbeHandler{
								HTTPGet: &corev1.HTTPGetAction{
									Path: "/healthz",
									Port: intstrFromInt32(8080),
								},
							},
							InitialDelaySeconds: 10,
							PeriodSeconds:       30,
						},
						ReadinessProbe: &corev1.Probe{
							ProbeHandler: corev1.ProbeHandler{
								HTTPGet: &corev1.HTTPGetAction{
									Path: "/readyz",
									Port: intstrFromInt32(8080),
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
			// Deprecated fallback — synthesise named fragments so override semantics still apply.
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

	// MCP guidance section — one sub-heading per server that has instructions set.
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
// Format: {provider}://{host:port}/{collection}
// The Endpoint field may be "http://host:port" or just "host:port"; both are handled.
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
	return fmt.Sprintf("%s://%s/%s", vs.Provider, host, collection)
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
		provider = inferEmbeddingProvider(emb.Model)
	}
	if provider != "" {
		envs = append(envs, corev1.EnvVar{Name: "AGENT_EMBEDDING_PROVIDER", Value: provider})
	}
	return envs
}

// inferEmbeddingProvider returns the provider name inferred from the embedding model ID.
func inferEmbeddingProvider(model string) string {
	switch {
	case strings.HasPrefix(model, "text-embedding-"):
		return "openai"
	case strings.HasPrefix(model, "text-multilingual-embedding-") ||
		strings.HasPrefix(model, "text-embedding-004"):
		return "google"
	case strings.HasPrefix(model, "voyage-"):
		return "voyageai"
	default:
		return "openai" // OpenAI-compatible fallback (covers Ollama embedding models)
	}
}

// mcpServerRuntime is the runtime representation of one MCP server injected into
// AGENT_MCP_SERVERS. Auth secret values are never included — only env var names and
// pod-local file paths are carried here, matching config.MCPServerConfig fields.
type mcpServerRuntime struct {
	Name        string `json:"name"`
	URL         string `json:"url"`
	AuthType    string `json:"authType,omitempty"`
	TokenEnvVar string `json:"tokenEnvVar,omitempty"`
	CertFile    string `json:"certFile,omitempty"`
	KeyFile     string `json:"keyFile,omitempty"`
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

// mcpVolumeName returns the k8s volume name for an mTLS Secret mount for the given server.
func mcpVolumeName(serverName string) string {
	safe := strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			return r
		}
		if r >= 'A' && r <= 'Z' {
			return r + 32 // to lower
		}
		return '-'
	}, serverName)
	return "mcp-mtls-" + safe
}

// mcpMountPath returns the pod-local directory for an mTLS Secret mount.
func mcpMountPath(serverName string) string {
	safe := strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			return r
		}
		return '-'
	}, serverName)
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
	if claim, ok := swarmAgent.Annotations[annotationTeamArtifactClaim]; ok && claim != "" {
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
	if claim, ok := swarmAgent.Annotations[annotationTeamArtifactClaim]; ok && claim != "" {
		mountPath := swarmAgent.Annotations[annotationTeamArtifactStore]
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

	// Propagate the custom validator prompt when a semantic liveness probe is configured.
	if swarmAgent.Spec.Observability != nil &&
		swarmAgent.Spec.Observability.HealthCheck != nil &&
		swarmAgent.Spec.Observability.HealthCheck.Type == "semantic" &&
		swarmAgent.Spec.Observability.HealthCheck.Prompt != "" {
		envVars = append(envVars, corev1.EnvVar{
			Name:  "AGENT_VALIDATOR_PROMPT",
			Value: swarmAgent.Spec.Observability.HealthCheck.Prompt,
		})
	}

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

// buildTeamEnvVars returns env vars derived from SwarmTeam annotations/labels.
func buildTeamEnvVars(swarmAgent *kubeswarmv1alpha1.SwarmAgent) []corev1.EnvVar {
	var out []corev1.EnvVar
	if queueURL, ok := swarmAgent.Annotations[annotationTeamQueueURL]; ok && queueURL != "" {
		out = append(out, corev1.EnvVar{Name: "TASK_QUEUE_URL", Value: queueURL})
	}
	if routes, ok := swarmAgent.Annotations[annotationTeamRoutes]; ok && routes != "" {
		out = append(out, corev1.EnvVar{Name: "AGENT_TEAM_ROUTES", Value: routes})
	}
	if role, ok := swarmAgent.Annotations[annotationTeamRole]; ok && role != "" {
		out = append(out, corev1.EnvVar{Name: "AGENT_TEAM_ROLE", Value: role})
	}
	if teamName, ok := swarmAgent.Labels["kubeswarm/team"]; ok && teamName != "" {
		out = append(out, corev1.EnvVar{Name: "AGENT_TEAM_NAME", Value: teamName})
	}
	return out
}

// buildArtifactEnvVars returns env vars for the artifact store (RFC-0013).
func buildArtifactEnvVars(swarmAgent *kubeswarmv1alpha1.SwarmAgent) []corev1.EnvVar {
	storeURL, ok := swarmAgent.Annotations[annotationTeamArtifactStore]
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
	reg := &kubeswarmv1alpha1.SwarmRegistry{}
	err := r.Get(ctx, client.ObjectKey{Name: "default", Namespace: namespace}, reg)
	if err == nil {
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
				"kubeswarm/managed": "true",
			},
		},
		Spec: kubeswarmv1alpha1.SwarmRegistrySpec{
			Scope: kubeswarmv1alpha1.RegistryScopeNamespace,
		},
	}
	if err := r.Create(ctx, defaultReg); err != nil && !errors.IsAlreadyExists(err) {
		return fmt.Errorf("creating default SwarmRegistry: %w", err)
	}
	return nil
}

// reconcileRegistryRef validates that spec.registryRef names an existing SwarmRegistry.
// Emits a RegistryNotFound condition when the registry is absent. Non-blocking —
// the agent starts regardless. Agents with registryRef == nil have opted out; no check.
func (r *SwarmAgentReconciler) reconcileRegistryRef(ctx context.Context, agent *kubeswarmv1alpha1.SwarmAgent) {
	if agent.Spec.Infrastructure == nil || agent.Spec.Infrastructure.RegistryRef == nil {
		apimeta.RemoveStatusCondition(&agent.Status.Conditions, "RegistryNotFound")
		return
	}
	reg := &kubeswarmv1alpha1.SwarmRegistry{}
	err := r.Get(ctx, client.ObjectKey{Name: agent.Spec.Infrastructure.RegistryRef.Name, Namespace: agent.Namespace}, reg)
	if errors.IsNotFound(err) {
		r.setCondition(agent, "RegistryNotFound", metav1.ConditionTrue, "RegistryNotFound",
			fmt.Sprintf("SwarmRegistry %q not found in namespace %q; agent will not be indexed",
				agent.Spec.Infrastructure.RegistryRef.Name, agent.Namespace))
		return
	}
	apimeta.RemoveStatusCondition(&agent.Status.Conditions, "RegistryNotFound")
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
			// Budget not yet created — non-blocking, agent runs without enforcement.
			return nil
		}
		return fmt.Errorf("fetching SwarmBudget %q: %w", agent.Spec.Guardrails.BudgetRef.Name, err)
	}
	if budget.Spec.HardStop && budget.Status.Phase == kubeswarmv1alpha1.BudgetStatusExceeded {
		r.setCondition(agent, "BudgetExceeded", metav1.ConditionTrue, "BudgetRefExceeded",
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
	if err := r.List(ctx, runs, client.InNamespace(dep.Namespace)); err != nil {
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
			agentName := role.SwarmAgent
			if agentName == "" {
				agentName = run.Spec.TeamRef + "-" + role.Name
			}
			roleAgent[role.Name] = agentName
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
		apimeta.RemoveStatusCondition(&dep.Status.Conditions, "BudgetExceeded")
		return 0, nil
	}

	if usage.TotalTokens >= limit {
		r.setCondition(dep, "BudgetExceeded", metav1.ConditionTrue, "DailyLimitReached",
			fmt.Sprintf("daily token usage %d exceeds limit %d; replicas scaled to 0", usage.TotalTokens, limit))
		// Requeue when the oldest entry leaves the window so we can restore replicas.
		if earliestInWindow != nil {
			ttl := earliestInWindow.Add(24 * time.Hour).Sub(now)
			return ttl + time.Minute, nil // +1m buffer to avoid racing the boundary
		}
		return time.Hour, nil
	}

	// Under limit — clear condition.
	apimeta.RemoveStatusCondition(&dep.Status.Conditions, "BudgetExceeded")
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
	ctx context.Context,
	agent *kubeswarmv1alpha1.SwarmAgent,
	resolvedMCPServers []kubeswarmv1alpha1.MCPToolSpec,
) (time.Duration, error) {
	if len(resolvedMCPServers) == 0 {
		apimeta.RemoveStatusCondition(&agent.Status.Conditions, "MCPDegraded")
		agent.Status.ToolConnections = nil
		return 0, nil
	}

	boolPtr := func(v bool) *bool { return &v }
	now := metav1.Now()
	statuses := make([]kubeswarmv1alpha1.SwarmAgentMCPStatus, 0, len(resolvedMCPServers))
	allHealthy := true

	for _, server := range resolvedMCPServers {
		s := kubeswarmv1alpha1.SwarmAgentMCPStatus{
			Name:      server.Name,
			URL:       server.URL,
			LastCheck: &now,
		}

		u, err := neturl.Parse(server.URL)
		if err != nil {
			s.Healthy = boolPtr(false)
			s.Message = fmt.Sprintf("invalid URL: %v", err)
			allHealthy = false
		} else {
			host := u.Host
			if !strings.Contains(host, ":") {
				if u.Scheme == "https" {
					host += ":443"
				} else {
					host += ":80"
				}
			}
			conn, err := net.DialTimeout("tcp", host, mcpHealthDialTimeout)
			if err != nil {
				s.Healthy = boolPtr(false)
				s.Message = err.Error()
				allHealthy = false
			} else {
				_ = conn.Close()
				s.Healthy = boolPtr(true)
			}
		}
		statuses = append(statuses, s)
	}

	agent.Status.ToolConnections = statuses

	if allHealthy {
		apimeta.RemoveStatusCondition(&agent.Status.Conditions, "MCPDegraded")
	} else {
		r.setCondition(agent, "MCPDegraded", metav1.ConditionTrue, "MCPUnreachable",
			"one or more MCP servers are unreachable; see status.toolConnections for details")
	}

	// Status is updated by syncStatus which runs before this call; call Update here
	// to persist MCP health results independently.
	if err := r.Status().Update(ctx, agent); err != nil {
		return 0, err
	}

	return mcpHealthRequeueInterval, nil
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
	for _, role := range run.Spec.Roles {
		agentName := role.SwarmAgent
		if agentName == "" {
			agentName = run.Spec.TeamRef + "-" + role.Name
		}
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

	// Fast path: no entries requiring resolution — return spec as-is.
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
	if err := r.List(ctx, agents, client.InNamespace(cm.Namespace)); err != nil {
		return nil
	}
	var reqs []reconcile.Request
	for _, dep := range agents.Items {
		if dep.Spec.Prompt == nil || dep.Spec.Prompt.From == nil || dep.Spec.Prompt.From.ConfigMapKeyRef == nil {
			continue
		}
		if dep.Spec.Prompt.From.ConfigMapKeyRef.Name == cm.Name {
			reqs = append(reqs, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: dep.Name, Namespace: dep.Namespace},
			})
		}
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
	if err := r.List(ctx, agents, client.InNamespace(sec.Namespace)); err != nil {
		return nil
	}
	var reqs []reconcile.Request
	for _, dep := range agents.Items {
		if dep.Spec.Prompt == nil || dep.Spec.Prompt.From == nil || dep.Spec.Prompt.From.SecretKeyRef == nil {
			continue
		}
		if dep.Spec.Prompt.From.SecretKeyRef.Name == sec.Name {
			reqs = append(reqs, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: dep.Name, Namespace: dep.Namespace},
			})
		}
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

	// Role — events only, namespace-scoped.
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

	return nil
}

func (r *SwarmAgentReconciler) setCondition(
	swarmAgent *kubeswarmv1alpha1.SwarmAgent,
	condType string,
	status metav1.ConditionStatus,
	reason, message string,
) {
	apimeta.SetStatusCondition(&swarmAgent.Status.Conditions, metav1.Condition{
		Type:               condType,
		Status:             status,
		ObservedGeneration: swarmAgent.Generation,
		Reason:             reason,
		Message:            message,
	})
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

// SetupWithManager sets up the controller with the Manager.
func (r *SwarmAgentReconciler) SetupWithManager(mgr ctrl.Manager) error {
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
		Named("swarmagent").
		Complete(WithMetrics(r, "swarmagent"))
}

// agentInjectEnvVars reads env vars prefixed with SWARM_AGENT_INJECT_ from the
// operator pod's environment and returns them as agent pod env vars (prefix stripped).
// This lets helm values.agentExtraEnv propagate cluster-wide defaults to every agent
// pod without requiring a per-namespace secret (e.g. OPENAI_BASE_URL, AGENT_PROVIDER).
func agentInjectEnvVars() []corev1.EnvVar {
	const prefix = "SWARM_AGENT_INJECT_"
	var vars []corev1.EnvVar
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, prefix) {
			continue
		}
		idx := strings.IndexByte(kv, '=')
		if idx < 0 {
			continue
		}
		vars = append(vars, corev1.EnvVar{
			Name:  kv[len(prefix):idx],
			Value: kv[idx+1:],
		})
	}
	return vars
}

// buildAuditLogEnvVar builds the AGENT_AUDIT_LOG JSON value from the operator's
// own audit config (set by Helm via AUDIT_LOG_* env vars) merged with any
// agent-level spec.observability.auditLog override.
func buildAuditLogEnvVar(agent *kubeswarmv1alpha1.SwarmAgent) string {
	// Read cluster-level defaults from operator env vars (set by Helm).
	mode := os.Getenv("AUDIT_LOG_MODE")
	if mode == "" || mode == "off" {
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
		if mode == "off" {
			return ""
		}
	}

	cfg := map[string]any{
		"mode": mode,
		"sink": os.Getenv("AUDIT_LOG_SINK"),
	}
	if cfg["sink"] == "" {
		cfg["sink"] = "stdout"
	}
	if v := os.Getenv("AUDIT_LOG_MAX_DETAIL_BYTES"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg["maxDetailBytes"] = n
		}
	}
	if v := os.Getenv("AUDIT_LOG_REDIS_URL"); v != "" {
		cfg["redisURL"] = v
	}

	// Merge redaction patterns: cluster + agent level.
	var redact []string
	if v := os.Getenv("AUDIT_LOG_REDACT"); v != "" {
		_ = json.Unmarshal([]byte(v), &redact)
	}
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
