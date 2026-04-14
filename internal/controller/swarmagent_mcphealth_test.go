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
	"net/http"
	"net/http/httptest"
	"testing"

	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	kubeswarmv1alpha1 "github.com/kubeswarm/kubeswarm/api/v1alpha1"
)

func TestSwarmAgentControllerMCPHealthProbes(t *testing.T) {
	const namespace = "default"
	ctx := context.Background()
	var nameIdx int

	uniqueName := func() string {
		nameIdx++
		return fmt.Sprintf("mcphealth-agent-%d", nameIdx)
	}

	newReconciler := func() *SwarmAgentReconciler {
		return &SwarmAgentReconciler{
			Client:     k8sClient,
			Scheme:     k8sClient.Scheme(),
			AgentImage: "test-image:latest",
		}
	}

	t.Run("reconcileMCPHealth", func(t *testing.T) {
		t.Run("should mark healthy when MCP server returns 200", func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusOK)
			}))
			t.Cleanup(func() { srv.Close() })

			agentName := uniqueName()
			agent := &kubeswarmv1alpha1.SwarmAgent{
				ObjectMeta: metav1.ObjectMeta{Name: agentName, Namespace: namespace},
				Spec: kubeswarmv1alpha1.SwarmAgentSpec{
					Model:  "claude-sonnet-4-6",
					Prompt: &kubeswarmv1alpha1.AgentPrompt{Inline: "test"},
				},
			}
			requireNoError(t, k8sClient.Create(ctx, agent))
			t.Cleanup(func() { _ = k8sClient.Delete(ctx, agent) })

			// Re-fetch to get server-set fields.
			requireNoError(t, k8sClient.Get(ctx, types.NamespacedName{Name: agentName, Namespace: namespace}, agent))

			r := newReconciler()
			servers := []kubeswarmv1alpha1.MCPToolSpec{
				{Name: "healthy-server", URL: srv.URL},
			}
			_, err := r.reconcileMCPHealth(ctx, agent, servers)
			requireNoError(t, err)

			requireLen(t, agent.Status.ToolConnections, 1)
			if agent.Status.ToolConnections[0].Healthy == nil {
				t.Fatal("expected non-nil Healthy")
			}
			requireTrue(t, *agent.Status.ToolConnections[0].Healthy)
			requireNil(t, apimeta.FindStatusCondition(agent.Status.Conditions, kubeswarmv1alpha1.ConditionMCPDegraded))
		})

		t.Run("should mark reachable server as healthy even if it returns errors", func(t *testing.T) {
			// TCP dial probes reachability, not HTTP status. A server returning 500
			// is still reachable and therefore healthy from a connectivity standpoint.
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusInternalServerError)
			}))
			t.Cleanup(func() { srv.Close() })

			agentName := uniqueName()
			agent := &kubeswarmv1alpha1.SwarmAgent{
				ObjectMeta: metav1.ObjectMeta{Name: agentName, Namespace: namespace},
				Spec: kubeswarmv1alpha1.SwarmAgentSpec{
					Model:  "claude-sonnet-4-6",
					Prompt: &kubeswarmv1alpha1.AgentPrompt{Inline: "test"},
				},
			}
			requireNoError(t, k8sClient.Create(ctx, agent))
			t.Cleanup(func() { _ = k8sClient.Delete(ctx, agent) })

			requireNoError(t, k8sClient.Get(ctx, types.NamespacedName{Name: agentName, Namespace: namespace}, agent))

			r := newReconciler()
			servers := []kubeswarmv1alpha1.MCPToolSpec{
				{Name: "error-server", URL: srv.URL},
			}
			_, err := r.reconcileMCPHealth(ctx, agent, servers)
			requireNoError(t, err)

			requireLen(t, agent.Status.ToolConnections, 1)
			if agent.Status.ToolConnections[0].Healthy == nil {
				t.Fatal("expected non-nil Healthy")
			}
			requireTrue(t, *agent.Status.ToolConnections[0].Healthy)
			// TCP dial succeeds since server is listening - no MCPDegraded condition
			requireNil(t, apimeta.FindStatusCondition(agent.Status.Conditions, kubeswarmv1alpha1.ConditionMCPDegraded))
		})

		t.Run("should treat 401 as healthy (auth required but reachable)", func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusUnauthorized)
			}))
			t.Cleanup(func() { srv.Close() })

			agentName := uniqueName()
			agent := &kubeswarmv1alpha1.SwarmAgent{
				ObjectMeta: metav1.ObjectMeta{Name: agentName, Namespace: namespace},
				Spec: kubeswarmv1alpha1.SwarmAgentSpec{
					Model:  "claude-sonnet-4-6",
					Prompt: &kubeswarmv1alpha1.AgentPrompt{Inline: "test"},
				},
			}
			requireNoError(t, k8sClient.Create(ctx, agent))
			t.Cleanup(func() { _ = k8sClient.Delete(ctx, agent) })

			requireNoError(t, k8sClient.Get(ctx, types.NamespacedName{Name: agentName, Namespace: namespace}, agent))

			r := newReconciler()
			servers := []kubeswarmv1alpha1.MCPToolSpec{
				{Name: "auth-server", URL: srv.URL},
			}
			_, err := r.reconcileMCPHealth(ctx, agent, servers)
			requireNoError(t, err)

			if agent.Status.ToolConnections[0].Healthy == nil {
				t.Fatal("expected non-nil Healthy")
			}
			requireTrue(t, *agent.Status.ToolConnections[0].Healthy)
		})

		t.Run("should set MCPDegraded for unreachable server", func(t *testing.T) {
			agentName := uniqueName()
			agent := &kubeswarmv1alpha1.SwarmAgent{
				ObjectMeta: metav1.ObjectMeta{Name: agentName, Namespace: namespace},
				Spec: kubeswarmv1alpha1.SwarmAgentSpec{
					Model:  "claude-sonnet-4-6",
					Prompt: &kubeswarmv1alpha1.AgentPrompt{Inline: "test"},
				},
			}
			requireNoError(t, k8sClient.Create(ctx, agent))
			t.Cleanup(func() { _ = k8sClient.Delete(ctx, agent) })

			requireNoError(t, k8sClient.Get(ctx, types.NamespacedName{Name: agentName, Namespace: namespace}, agent))

			r := newReconciler()
			servers := []kubeswarmv1alpha1.MCPToolSpec{
				{Name: "gone-server", URL: "http://127.0.0.1:1"},
			}
			_, err := r.reconcileMCPHealth(ctx, agent, servers)
			requireNoError(t, err)

			if agent.Status.ToolConnections[0].Healthy == nil {
				t.Fatal("expected non-nil Healthy")
			}
			requireFalse(t, *agent.Status.ToolConnections[0].Healthy)
			cond := apimeta.FindStatusCondition(agent.Status.Conditions, kubeswarmv1alpha1.ConditionMCPDegraded)
			requireNotNil(t, cond)
			requireEqual(t, cond.Status, metav1.ConditionTrue)
		})

		t.Run("should clear MCPDegraded when no MCP servers configured", func(t *testing.T) {
			agentName := uniqueName()
			agent := &kubeswarmv1alpha1.SwarmAgent{
				ObjectMeta: metav1.ObjectMeta{Name: agentName, Namespace: namespace},
				Spec: kubeswarmv1alpha1.SwarmAgentSpec{
					Model:  "claude-sonnet-4-6",
					Prompt: &kubeswarmv1alpha1.AgentPrompt{Inline: "test"},
				},
			}
			// Pre-set MCPDegraded condition.
			apimeta.SetStatusCondition(&agent.Status.Conditions, metav1.Condition{
				Type:   kubeswarmv1alpha1.ConditionMCPDegraded,
				Status: metav1.ConditionTrue,
				Reason: "MCPUnreachable",
			})
			requireNoError(t, k8sClient.Create(ctx, agent))
			t.Cleanup(func() { _ = k8sClient.Delete(ctx, agent) })

			requireNoError(t, k8sClient.Get(ctx, types.NamespacedName{Name: agentName, Namespace: namespace}, agent))

			r := newReconciler()
			_, err := r.reconcileMCPHealth(ctx, agent, nil)
			requireNoError(t, err)

			if agent.Status.ToolConnections != nil {
				t.Fatalf("expected nil ToolConnections, got %v", agent.Status.ToolConnections)
			}
			requireNil(t, apimeta.FindStatusCondition(agent.Status.Conditions, kubeswarmv1alpha1.ConditionMCPDegraded))
		})

		t.Run("should recover: MCPDegraded cleared when server recovers", func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusOK)
			}))
			t.Cleanup(func() { srv.Close() })

			agentName := uniqueName()
			agent := &kubeswarmv1alpha1.SwarmAgent{
				ObjectMeta: metav1.ObjectMeta{Name: agentName, Namespace: namespace},
				Spec: kubeswarmv1alpha1.SwarmAgentSpec{
					Model:  "claude-sonnet-4-6",
					Prompt: &kubeswarmv1alpha1.AgentPrompt{Inline: "test"},
				},
			}
			requireNoError(t, k8sClient.Create(ctx, agent))
			t.Cleanup(func() { _ = k8sClient.Delete(ctx, agent) })

			requireNoError(t, k8sClient.Get(ctx, types.NamespacedName{Name: agentName, Namespace: namespace}, agent))

			r := newReconciler()
			servers := []kubeswarmv1alpha1.MCPToolSpec{
				{Name: "recovered", URL: srv.URL},
			}

			// First: simulate degraded state.
			apimeta.SetStatusCondition(&agent.Status.Conditions, metav1.Condition{
				Type:   kubeswarmv1alpha1.ConditionMCPDegraded,
				Status: metav1.ConditionTrue,
				Reason: "MCPUnreachable",
			})

			// Now reconcile with healthy server - should clear condition.
			_, err := r.reconcileMCPHealth(ctx, agent, servers)
			requireNoError(t, err)

			if agent.Status.ToolConnections[0].Healthy == nil {
				t.Fatal("expected non-nil Healthy")
			}
			requireTrue(t, *agent.Status.ToolConnections[0].Healthy)
			requireNil(t, apimeta.FindStatusCondition(agent.Status.Conditions, kubeswarmv1alpha1.ConditionMCPDegraded))
		})
	})
}
