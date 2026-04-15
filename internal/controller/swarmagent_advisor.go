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
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kubeswarmv1alpha1 "github.com/kubeswarm/kubeswarm/api/v1alpha1"
)

// reconcileAdvisorConnections checks each advisor connection's target agent
// and returns status entries and a condition reflecting overall health.
func reconcileAdvisorConnections(
	ctx context.Context,
	c client.Reader,
	agent *kubeswarmv1alpha1.SwarmAgent,
) ([]kubeswarmv1alpha1.AdvisorConnectionStatus, metav1.Condition) {
	now := metav1.Now()

	// Filter to advisor-role connections only.
	var advisors []kubeswarmv1alpha1.AgentConnection
	for _, conn := range agent.Spec.Agents {
		if conn.Role == kubeswarmv1alpha1.AgentConnectionRoleAdvisor {
			advisors = append(advisors, conn)
		}
	}

	// No advisors - vacuously true.
	if len(advisors) == 0 {
		return nil, metav1.Condition{
			Type:               kubeswarmv1alpha1.ConditionAdvisorsReady,
			Status:             metav1.ConditionTrue,
			Reason:             "NoAdvisors",
			Message:            "No advisor connections configured",
			LastTransitionTime: now,
		}
	}

	statuses := make([]kubeswarmv1alpha1.AdvisorConnectionStatus, 0, len(advisors))
	var unhealthy []string
	var firstReason string

	for _, conn := range advisors {
		// Resolve tool name.
		toolName, err := kubeswarmv1alpha1.ResolveAdvisorToolName(conn)
		if err != nil {
			toolName = "consult_" + conn.Name // best effort fallback
		}

		s := kubeswarmv1alpha1.AdvisorConnectionStatus{
			Name:               conn.Name,
			ToolName:           toolName,
			LastTransitionTime: now,
		}

		if conn.AgentRef == nil {
			s.Ready = false
			unhealthy = append(unhealthy, conn.Name)
			if firstReason == "" {
				firstReason = "AdvisorNotFound"
			}
			statuses = append(statuses, s)
			continue
		}

		// Look up the target agent.
		target := &kubeswarmv1alpha1.SwarmAgent{}
		key := types.NamespacedName{Name: conn.AgentRef.Name, Namespace: agent.Namespace}
		if err := c.Get(ctx, key, target); err != nil {
			s.Ready = false
			unhealthy = append(unhealthy, conn.Name)
			if firstReason == "" {
				firstReason = "AdvisorNotFound"
			}
			statuses = append(statuses, s)
			continue
		}

		// Check ready replicas.
		if target.Status.ReadyReplicas == 0 {
			s.Ready = false
			unhealthy = append(unhealthy, conn.Name)
			if firstReason == "" {
				firstReason = "AdvisorNoReplicas"
			}
			statuses = append(statuses, s)
			continue
		}

		s.Ready = true
		s.ToolInjected = true
		statuses = append(statuses, s)
	}

	if len(unhealthy) > 0 {
		return statuses, metav1.Condition{
			Type:               kubeswarmv1alpha1.ConditionAdvisorsReady,
			Status:             metav1.ConditionFalse,
			Reason:             firstReason,
			Message:            fmt.Sprintf("advisor(s) not ready: %s", strings.Join(unhealthy, ", ")),
			LastTransitionTime: now,
		}
	}

	return statuses, metav1.Condition{
		Type:               kubeswarmv1alpha1.ConditionAdvisorsReady,
		Status:             metav1.ConditionTrue,
		Reason:             "AllAdvisorsReady",
		Message:            fmt.Sprintf("All %d advisor connections ready", len(advisors)),
		LastTransitionTime: now,
	}
}

// advisorRuntimeConfig is the JSON shape injected into the agent pod so the
// runtime knows about advisor connections (tool name, limits, target agent).
type advisorRuntimeConfig struct {
	Name                    string `json:"name"`
	ToolName                string `json:"toolName"`
	AgentRef                string `json:"agentRef"`
	RecentMessages          int32  `json:"recentMessages"`
	MaxCallsPerTask         int32  `json:"maxCallsPerTask"`
	TimeoutSeconds          int32  `json:"timeoutSeconds"`
	MaxAdvisorTokensPerTask int32  `json:"maxAdvisorTokensPerTask"`
	MaxContextBytes         int32  `json:"maxContextBytes"`
	ExcludeSystemPrompt     bool   `json:"excludeSystemPrompt"`
	Instructions            string `json:"instructions,omitempty"`
}

// buildAdvisorEnvVars returns AGENT_ADVISORS as JSON when the agent has advisor connections.
func buildAdvisorEnvVars(swarmAgent *kubeswarmv1alpha1.SwarmAgent) []corev1.EnvVar {
	var configs []advisorRuntimeConfig
	for _, conn := range swarmAgent.Spec.Agents {
		if conn.Role != kubeswarmv1alpha1.AgentConnectionRoleAdvisor || conn.AgentRef == nil {
			continue
		}
		toolName, _ := kubeswarmv1alpha1.ResolveAdvisorToolName(conn)

		cfg := advisorRuntimeConfig{
			Name:         conn.Name,
			ToolName:     toolName,
			AgentRef:     conn.AgentRef.Name,
			Instructions: conn.Instructions,
		}
		if conn.ContextPropagation != nil {
			cfg.RecentMessages = conn.ContextPropagation.RecentMessages
			cfg.MaxCallsPerTask = conn.ContextPropagation.MaxCallsPerTask
			cfg.TimeoutSeconds = conn.ContextPropagation.TimeoutSeconds
			cfg.MaxAdvisorTokensPerTask = conn.ContextPropagation.MaxAdvisorTokensPerTask
			cfg.MaxContextBytes = conn.ContextPropagation.MaxContextBytes
			cfg.ExcludeSystemPrompt = conn.ContextPropagation.ExcludeSystemPrompt
		}
		configs = append(configs, cfg)
	}
	if len(configs) == 0 {
		return nil
	}
	raw, err := json.Marshal(configs)
	if err != nil {
		return nil
	}
	return []corev1.EnvVar{
		{Name: "AGENT_ADVISORS", Value: string(raw)},
	}
}
