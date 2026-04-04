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
	"fmt"
	"net/url"
	"sort"

	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kubeswarmv1alpha1 "github.com/kubeswarm/kubeswarm/api/v1alpha1"
	"github.com/kubeswarm/kubeswarm/internal/registry"
)

// SwarmRegistryReconciler reconciles SwarmRegistry objects.
//
// +kubebuilder:rbac:groups=kubeswarm.io,resources=swarmregistries,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=kubeswarm.io,resources=swarmregistries/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=kubeswarm.io,resources=swarmregistries/finalizers,verbs=update
type SwarmRegistryReconciler struct {
	client.Client
	Scheme            *runtime.Scheme
	Registry          *registry.Registry
	TaskQueueURL      string // operator-side connection URL
	AgentTaskQueueURL string // in-cluster URL injected into pods (may differ from above)
}

func (r *SwarmRegistryReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	logger.Info("reconciling SwarmRegistry", "name", req.Name, "namespace", req.Namespace)

	reg := &kubeswarmv1alpha1.SwarmRegistry{}
	if err := r.Get(ctx, req.NamespacedName, reg); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	key := req.Namespace + "/" + req.Name

	// Handle deletion: remove the bucket from the in-process index.
	if !reg.DeletionTimestamp.IsZero() {
		r.Registry.Remove(key)
		return ctrl.Result{}, nil
	}

	// Collect SwarmAgents by scope.
	var agentList kubeswarmv1alpha1.SwarmAgentList
	listOpts := []client.ListOption{}
	if reg.Spec.Scope != kubeswarmv1alpha1.RegistryScopeCluster {
		listOpts = append(listOpts, client.InNamespace(req.Namespace))
	}
	if err := r.List(ctx, &agentList, listOpts...); err != nil {
		return ctrl.Result{}, err
	}

	// Filter to agents that explicitly opted into this registry via spec.registryRef.
	// Agents with registryRef == "" have opted out of all indexing and are excluded.
	// Agents with registryRef == registry.Name (or defaulted to "default") are included.
	registered := make([]kubeswarmv1alpha1.SwarmAgent, 0, len(agentList.Items))
	for _, a := range agentList.Items {
		if a.Spec.Infrastructure != nil && a.Spec.Infrastructure.RegistryRef != nil && a.Spec.Infrastructure.RegistryRef.Name == reg.Name {
			registered = append(registered, a)
		}
	}

	// Capable agents are the subset that advertise at least one capability.
	// These are used for the capability index. All registered agents appear in the fleet.
	capable := make([]kubeswarmv1alpha1.SwarmAgent, 0, len(registered))
	for _, a := range registered {
		if len(a.Spec.Capabilities) > 0 {
			capable = append(capable, a)
		}
	}

	// Rebuild the in-process index for this registry bucket.
	r.Registry.Rebuild(key, capable)

	// Annotate registered standalone agents with a per-agent stream URL so their
	// pods get the right TASK_QUEUE_URL injected. Skip agents already assigned to
	// a team queue (annotationTeamQueueURL set by the SwarmTeam controller).
	if streamBase := r.agentStreamBase(); streamBase != "" {
		for i := range registered {
			a := &registered[i]
			if a.Annotations[annotationTeamQueueURL] != "" {
				continue // static team member — leave untouched
			}
			streamURL := r.buildAgentStreamURL(streamBase, a.Namespace, a.Name)
			if a.Annotations[annotationTeamQueueURL] == streamURL {
				continue // already up-to-date, avoid spurious patch
			}
			patch := client.MergeFrom(a.DeepCopy())
			if a.Annotations == nil {
				a.Annotations = make(map[string]string)
			}
			a.Annotations[annotationTeamQueueURL] = streamURL
			if err := r.Patch(ctx, a, patch); err != nil {
				logger.Error(err, "patching agent stream URL annotation", "agent", a.Name)
			}
		}
	}

	// Build per-registry capability snapshot (capable agents only).
	capSnap := buildSnapshot(capable)
	// Build fleet view (all registered agents, including capability-less ones).
	fleetSnap := buildFleet(registered)

	// Only touch LastRebuild when actual content changes so the status update is a
	// no-op when nothing changed, breaking the reconcile loop that arises because
	// r.Status().Update() always bumps the resource version and retriggers the watch.
	readyCondition := apimeta.FindStatusCondition(reg.Status.Conditions, kubeswarmv1alpha1.ConditionReady)
	contentChanged := reg.Status.IndexedAgents != len(capable) ||
		reg.Status.ObservedGeneration != reg.Generation ||
		!capabilitiesEqual(reg.Status.Capabilities, capSnap) ||
		!fleetEqual(reg.Status.Fleet, fleetSnap) ||
		readyCondition == nil

	if !contentChanged {
		return ctrl.Result{}, nil
	}

	now := metav1.Now()
	reg.Status.IndexedAgents = len(capable)
	reg.Status.LastRebuild = &now
	reg.Status.Capabilities = capSnap
	reg.Status.Fleet = fleetSnap
	reg.Status.ObservedGeneration = reg.Generation

	apimeta.SetStatusCondition(&reg.Status.Conditions, metav1.Condition{
		Type:               kubeswarmv1alpha1.ConditionReady,
		Status:             metav1.ConditionTrue,
		ObservedGeneration: reg.Generation,
		Reason:             "IndexReady",
		Message:            "capability index rebuilt successfully",
	})

	logger.Info("updating SwarmRegistry status",
		"indexedAgents", reg.Status.IndexedAgents,
		"capabilities", len(reg.Status.Capabilities),
	)

	return ctrl.Result{}, r.Status().Update(ctx, reg)
}

// capabilitiesEqual returns true when two sorted capability snapshots are identical.
func capabilitiesEqual(a, b []kubeswarmv1alpha1.IndexedCapability) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].ID != b[i].ID || a[i].Description != b[i].Description {
			return false
		}
		if len(a[i].Agents) != len(b[i].Agents) || len(a[i].Tags) != len(b[i].Tags) {
			return false
		}
		for j := range a[i].Agents {
			if a[i].Agents[j] != b[i].Agents[j] {
				return false
			}
		}
		for j := range a[i].Tags {
			if a[i].Tags[j] != b[i].Tags[j] {
				return false
			}
		}
	}
	return true
}

// buildSnapshot computes the capability snapshot from a slice of SwarmAgents directly.
// Used for per-registry status reporting.
func buildSnapshot(agents []kubeswarmv1alpha1.SwarmAgent) []kubeswarmv1alpha1.IndexedCapability {
	type agg struct {
		description string
		agents      map[string]struct{}
		tags        map[string]struct{}
	}
	m := make(map[string]*agg)
	for _, a := range agents {
		for _, c := range a.Spec.Capabilities {
			ag, ok := m[c.Name]
			if !ok {
				ag = &agg{agents: make(map[string]struct{}), tags: make(map[string]struct{})}
				m[c.Name] = ag
			}
			// Take the first non-empty description seen for this capability ID.
			if ag.description == "" && c.Description != "" {
				ag.description = c.Description
			}
			ag.agents[a.Name] = struct{}{}
			for _, t := range c.Tags {
				ag.tags[t] = struct{}{}
			}
		}
	}
	out := make([]kubeswarmv1alpha1.IndexedCapability, 0, len(m))
	for id, ag := range m {
		ic := kubeswarmv1alpha1.IndexedCapability{ID: id, Description: ag.description}
		for name := range ag.agents {
			ic.Agents = append(ic.Agents, name)
		}
		sort.Strings(ic.Agents)
		for t := range ag.tags {
			ic.Tags = append(ic.Tags, t)
		}
		sort.Strings(ic.Tags)
		out = append(out, ic)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// buildFleet produces the fleet summary for all registered agents, sorted by name.
func buildFleet(agents []kubeswarmv1alpha1.SwarmAgent) []kubeswarmv1alpha1.AgentFleetEntry {
	out := make([]kubeswarmv1alpha1.AgentFleetEntry, 0, len(agents))
	for _, a := range agents {
		entry := kubeswarmv1alpha1.AgentFleetEntry{
			Name:          a.Name,
			Model:         a.Spec.Model,
			ReadyReplicas: a.Status.ReadyReplicas,
		}
		if a.Status.DailyTokenUsage != nil {
			entry.DailyTokens = a.Status.DailyTokenUsage.TotalTokens
		}
		for _, c := range a.Spec.Capabilities {
			entry.Capabilities = append(entry.Capabilities, c.Name)
		}
		sort.Strings(entry.Capabilities)
		out = append(out, entry)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// fleetEqual returns true when two fleet snapshots are identical.
func fleetEqual(a, b []kubeswarmv1alpha1.AgentFleetEntry) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Name != b[i].Name ||
			a[i].Model != b[i].Model ||
			a[i].ReadyReplicas != b[i].ReadyReplicas ||
			a[i].DailyTokens != b[i].DailyTokens {
			return false
		}
		if len(a[i].Capabilities) != len(b[i].Capabilities) {
			return false
		}
		for j := range a[i].Capabilities {
			if a[i].Capabilities[j] != b[i].Capabilities[j] {
				return false
			}
		}
	}
	return true
}

// agentStreamBase returns the base Redis URL to use when building per-agent stream URLs.
// Prefers AgentTaskQueueURL (the in-cluster URL) so pods get the right connection string.
func (r *SwarmRegistryReconciler) agentStreamBase() string {
	if r.AgentTaskQueueURL != "" {
		return r.AgentTaskQueueURL
	}
	return r.TaskQueueURL
}

// buildAgentStreamURL constructs a Redis URL with ?stream={namespace}.{agentName}
// so a standalone capability agent has a dedicated, predictable queue stream.
func (r *SwarmRegistryReconciler) buildAgentStreamURL(base, namespace, agentName string) string {
	streamName := fmt.Sprintf("%s.%s", namespace, agentName)
	u, err := url.Parse(base)
	if err != nil {
		return base
	}
	q := u.Query()
	q.Set("stream", streamName)
	u.RawQuery = q.Encode()
	return u.String()
}

// findRegistriesForAgent returns the SwarmRegistry CRs in the same namespace as the
// changed SwarmAgent so the registry rebuilds whenever an agent's capabilities change.
func (r *SwarmRegistryReconciler) findRegistriesForAgent(
	ctx context.Context,
	obj client.Object,
) []reconcile.Request {
	agent, ok := obj.(*kubeswarmv1alpha1.SwarmAgent)
	if !ok {
		return nil
	}

	var regList kubeswarmv1alpha1.SwarmRegistryList
	// List namespace-scoped registries in the agent's namespace.
	if err := r.List(ctx, &regList, client.InNamespace(agent.Namespace)); err != nil {
		return nil
	}

	reqs := make([]reconcile.Request, 0, len(regList.Items))
	for _, reg := range regList.Items {
		reqs = append(reqs, reconcile.Request{
			NamespacedName: client.ObjectKey{Name: reg.Name, Namespace: reg.Namespace},
		})
	}
	return reqs
}

// SetupWithManager sets up the controller with the Manager.
func (r *SwarmRegistryReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&kubeswarmv1alpha1.SwarmRegistry{}).
		// Re-index whenever an SwarmAgent's capabilities change.
		Watches(
			&kubeswarmv1alpha1.SwarmAgent{},
			handler.EnqueueRequestsFromMapFunc(r.findRegistriesForAgent),
		).
		Named("swarmregistry").
		Complete(WithMetrics(r, "swarmregistry"))
}
