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

package webhook

import (
	"context"
	"fmt"

	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kubeswarmv1alpha1 "github.com/kubeswarm/kubeswarm/api/v1alpha1"
)

// ValidateAdvisorConnections validates all advisor-role agent connections on a SwarmAgent.
// It checks: advisor requires agentRef, no self-reference, target exists, depth 1, no tool name collisions.
func ValidateAdvisorConnections(ctx context.Context, c client.Client, agent *kubeswarmv1alpha1.SwarmAgent) []error {
	var errs []error

	// Collect existing tool names from MCP servers and webhooks.
	existingTools := map[string]string{} // tool name -> source description
	if agent.Spec.Tools != nil {
		for _, mcp := range agent.Spec.Tools.MCP {
			existingTools[mcp.Name] = fmt.Sprintf("spec.tools.mcp[%s]", mcp.Name)
		}
		for _, wh := range agent.Spec.Tools.Webhooks {
			existingTools[wh.Name] = fmt.Sprintf("spec.tools.webhooks[%s]", wh.Name)
		}
	}

	// Track advisor tool names for inter-advisor collision detection.
	advisorTools := map[string]string{} // tool name -> advisor connection name

	for _, conn := range agent.Spec.Agents {
		if conn.Role != kubeswarmv1alpha1.AgentConnectionRoleAdvisor {
			// Non-advisor with contextPropagation is caught by CEL rule C3,
			// but validate server-side too.
			if conn.ContextPropagation != nil {
				errs = append(errs, fmt.Errorf(
					"agent connection %q: contextPropagation is only valid when role is advisor",
					conn.Name))
			}
			continue
		}

		// C2: advisor requires agentRef.
		if conn.AgentRef == nil {
			errs = append(errs, fmt.Errorf(
				"agent connection %q: advisor role requires agentRef",
				conn.Name))
			continue
		}
		if conn.CapabilityRef != nil {
			errs = append(errs, fmt.Errorf(
				"agent connection %q: advisor role requires agentRef and forbids capabilityRef",
				conn.Name))
			continue
		}

		// No self-reference.
		if conn.AgentRef.Name == agent.Name {
			errs = append(errs, fmt.Errorf(
				"agent connection %q: self-reference is not allowed for advisors",
				conn.Name))
			continue
		}

		// Target must exist in same namespace.
		target := &kubeswarmv1alpha1.SwarmAgent{}
		key := types.NamespacedName{Name: conn.AgentRef.Name, Namespace: agent.Namespace}
		if err := c.Get(ctx, key, target); err != nil {
			errs = append(errs, fmt.Errorf(
				"agent connection %q: advisor agent %q not found in namespace %q",
				conn.Name, conn.AgentRef.Name, agent.Namespace))
			continue
		}

		// Depth 1: target must not itself have advisor connections.
		for _, targetConn := range target.Spec.Agents {
			if targetConn.Role == kubeswarmv1alpha1.AgentConnectionRoleAdvisor {
				errs = append(errs, fmt.Errorf(
					"agent connection %q: advisor agent %q has advisor connections itself (depth > 1 not allowed)",
					conn.Name, conn.AgentRef.Name))
				break
			}
		}

		// Resolve tool name.
		toolName, err := kubeswarmv1alpha1.ResolveAdvisorToolName(conn)
		if err != nil {
			errs = append(errs, fmt.Errorf(
				"agent connection %q: %w", conn.Name, err))
			continue
		}

		// Check collision with existing MCP/webhook tools.
		if source, ok := existingTools[toolName]; ok {
			errs = append(errs, fmt.Errorf(
				"advisor %q would register tool %q which conflicts with tool %q in %s",
				conn.Name, toolName, toolName, source))
		}

		// Check collision with other advisor tools.
		if otherAdvisor, ok := advisorTools[toolName]; ok {
			errs = append(errs, fmt.Errorf(
				"advisor %q would register tool %q which conflicts with advisor %q",
				conn.Name, toolName, otherAdvisor))
		}
		advisorTools[toolName] = conn.Name
	}

	return errs
}
