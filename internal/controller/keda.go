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

// Package controller contains helpers for creating and managing KEDA ScaledObjects.
// The operator uses the unstructured client so that it has no hard compile-time dependency
// on the KEDA CRDs — the cluster just needs KEDA v2 installed at runtime.
package controller

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strconv"

	"k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	kubeswarmv1alpha1 "github.com/kubeswarm/kubeswarm/api/v1alpha1"
)

var scaledObjectGVK = schema.GroupVersionKind{
	Group:   "keda.sh",
	Version: "v1alpha1",
	Kind:    "ScaledObject",
}

const defaultPendingTasks = int32(5)

const defaultTaskStream = "agent-tasks"

// reconcileKEDA creates, updates, or deletes the KEDA ScaledObject for agent.
// It is a no-op (with a log warning) when KEDA is not installed in the cluster.
func (r *SwarmAgentReconciler) reconcileKEDA(ctx context.Context, agent *kubeswarmv1alpha1.SwarmAgent) error {
	logger := log.FromContext(ctx)

	// When budget is exceeded, buildDeployment sets replicas=0. Delete the
	// ScaledObject so KEDA's minReplicas does not override the scale-to-zero.
	// The next reconcile after BudgetExceeded clears will recreate it.
	if apimeta.IsStatusConditionTrue(agent.Status.Conditions, "BudgetExceeded") {
		return r.deleteScaledObjectIfExists(ctx, agent)
	}

	if agent.Spec.Runtime.Autoscaling == nil {
		return r.deleteScaledObjectIfExists(ctx, agent)
	}

	redisAddr := resolveRedisAddress(agent)
	if redisAddr == "" {
		logger.Info("KEDA autoscaling enabled but no Redis address found; " +
			"set SWARM_AGENT_INJECT_TASK_QUEUE_URL on the operator pod or annotate the agent with the queue URL")
		return nil
	}

	streamName := resolveStreamName(agent)

	desired := buildScaledObject(agent, redisAddr, streamName)
	if err := ctrl.SetControllerReference(agent, desired, r.Scheme); err != nil {
		return fmt.Errorf("setting owner reference on ScaledObject: %w", err)
	}

	existing := &unstructured.Unstructured{}
	existing.SetGroupVersionKind(scaledObjectGVK)
	err := r.Get(ctx, client.ObjectKeyFromObject(desired), existing)
	if errors.IsNotFound(err) {
		if createErr := r.Create(ctx, desired); createErr != nil {
			if isNoMatchError(createErr) {
				logger.Info("KEDA not installed in this cluster; skipping ScaledObject creation")
				return nil
			}
			return fmt.Errorf("creating ScaledObject: %w", createErr)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("getting ScaledObject: %w", err)
	}

	// Update triggers and replica counts in-place.
	if err := unstructured.SetNestedField(existing.Object, int64(minReplicas(agent)), "spec", "minReplicaCount"); err != nil {
		return err
	}
	if err := unstructured.SetNestedField(existing.Object, int64(maxReplicas(agent)), "spec", "maxReplicaCount"); err != nil {
		return err
	}
	triggers, _, _ := unstructured.NestedSlice(desired.Object, "spec", "triggers")
	if err := unstructured.SetNestedSlice(existing.Object, triggers, "spec", "triggers"); err != nil {
		return err
	}
	return r.Update(ctx, existing)
}

func (r *SwarmAgentReconciler) deleteScaledObjectIfExists(ctx context.Context, agent *kubeswarmv1alpha1.SwarmAgent) error {
	existing := &unstructured.Unstructured{}
	existing.SetGroupVersionKind(scaledObjectGVK)
	existing.SetName(agent.Name)
	existing.SetNamespace(agent.Namespace)
	err := r.Delete(ctx, existing)
	if err == nil || errors.IsNotFound(err) {
		return nil
	}
	if isNoMatchError(err) {
		return nil // KEDA not installed — nothing to delete
	}
	return fmt.Errorf("deleting ScaledObject: %w", err)
}

func buildScaledObject(agent *kubeswarmv1alpha1.SwarmAgent, redisAddr, streamName string) *unstructured.Unstructured {
	pending := strconv.Itoa(int(targetPendingTasks(agent)))
	obj := &unstructured.Unstructured{
		Object: map[string]any{
			"apiVersion": "keda.sh/v1alpha1",
			"kind":       "ScaledObject",
			"metadata": map[string]any{
				"name":      agent.Name,
				"namespace": agent.Namespace,
			},
			"spec": map[string]any{
				"scaleTargetRef": map[string]any{
					"name": agent.Name + "-agent",
				},
				"minReplicaCount": int64(minReplicas(agent)),
				"maxReplicaCount": int64(maxReplicas(agent)),
				"triggers": []any{
					map[string]any{
						"type": "redis-streams",
						"metadata": map[string]any{
							"address":             redisAddr,
							"stream":              streamName,
							"consumerGroup":       "swarm-agents",
							"pendingEntriesCount": pending,
						},
					},
				},
			},
		},
	}
	obj.SetGroupVersionKind(scaledObjectGVK)
	return obj
}

// resolveRedisAddress returns the Redis connection string for the KEDA trigger.
// Priority: team queue URL annotation → operator inject env var.
func resolveRedisAddress(agent *kubeswarmv1alpha1.SwarmAgent) string {
	// Team-member agents have a per-role queue URL injected as an annotation.
	if queueURL, ok := agent.Annotations[annotationTeamQueueURL]; ok && queueURL != "" {
		return stripStreamParam(queueURL)
	}
	// Operator-wide injection via SWARM_AGENT_INJECT_TASK_QUEUE_URL.
	if injected := os.Getenv("SWARM_AGENT_INJECT_TASK_QUEUE_URL"); injected != "" {
		return stripStreamParam(injected)
	}
	return ""
}

// resolveStreamName derives the Redis stream name from the queue URL.
// Falls back to the default defaultTaskStream stream.
func resolveStreamName(agent *kubeswarmv1alpha1.SwarmAgent) string {
	queueURL := agent.Annotations[annotationTeamQueueURL]
	if queueURL == "" {
		queueURL = os.Getenv("SWARM_AGENT_INJECT_TASK_QUEUE_URL")
	}
	if queueURL == "" {
		return defaultTaskStream
	}
	u, err := url.Parse(queueURL)
	if err != nil {
		return defaultTaskStream
	}
	if s := u.Query().Get("stream"); s != "" {
		return s
	}
	return defaultTaskStream
}

// stripStreamParam removes the ?stream= query parameter from a Redis URL,
// returning the clean connection string suitable for KEDA.
func stripStreamParam(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	q := u.Query()
	q.Del("stream")
	u.RawQuery = q.Encode()
	return u.String()
}

func minReplicas(agent *kubeswarmv1alpha1.SwarmAgent) int32 {
	if agent.Spec.Runtime.Autoscaling != nil && agent.Spec.Runtime.Autoscaling.MinReplicas != nil {
		return *agent.Spec.Runtime.Autoscaling.MinReplicas
	}
	if agent.Spec.Runtime.Replicas != nil {
		return *agent.Spec.Runtime.Replicas
	}
	return 1
}

func maxReplicas(agent *kubeswarmv1alpha1.SwarmAgent) int32 {
	if agent.Spec.Runtime.Autoscaling != nil && agent.Spec.Runtime.Autoscaling.MaxReplicas != nil {
		return *agent.Spec.Runtime.Autoscaling.MaxReplicas
	}
	return 10
}

func targetPendingTasks(agent *kubeswarmv1alpha1.SwarmAgent) int32 {
	if agent.Spec.Runtime.Autoscaling != nil && agent.Spec.Runtime.Autoscaling.TargetPendingTasks != nil {
		return *agent.Spec.Runtime.Autoscaling.TargetPendingTasks
	}
	return defaultPendingTasks
}

// isNoMatchError returns true when the API server reports no kind match for the
// requested resource — the reliable signal that KEDA CRDs are not installed.
func isNoMatchError(err error) bool {
	if err == nil {
		return false
	}
	return isNoMatchErrorString(err.Error())
}

func isNoMatchErrorString(msg string) bool {
	return contains(msg, "no kind is registered") ||
		contains(msg, "no matches for kind") ||
		contains(msg, "no resources found") ||
		contains(msg, "the server could not find the requested resource")
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(s) > 0 && containsHelper(s, sub))
}

func containsHelper(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
