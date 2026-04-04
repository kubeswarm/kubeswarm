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

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	kubeswarmv1alpha1 "github.com/kubeswarm/kubeswarm/api/v1alpha1"
)

var _ = Describe("SwarmAgent Controller - MCP health probes", func() {
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

	Context("reconcileMCPHealth", func() {
		It("should mark healthy when MCP server returns 200", func() {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusOK)
			}))
			defer srv.Close()

			agentName := uniqueName()
			agent := &kubeswarmv1alpha1.SwarmAgent{
				ObjectMeta: metav1.ObjectMeta{Name: agentName, Namespace: namespace},
				Spec: kubeswarmv1alpha1.SwarmAgentSpec{
					Model:  "claude-sonnet-4-6",
					Prompt: &kubeswarmv1alpha1.AgentPrompt{Inline: "test"},
				},
			}
			Expect(k8sClient.Create(ctx, agent)).To(Succeed())
			defer func() { _ = k8sClient.Delete(ctx, agent) }()

			// Re-fetch to get server-set fields.
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: agentName, Namespace: namespace}, agent)).To(Succeed())

			r := newReconciler()
			servers := []kubeswarmv1alpha1.MCPToolSpec{
				{Name: "healthy-server", URL: srv.URL},
			}
			_, err := r.reconcileMCPHealth(ctx, agent, servers)
			Expect(err).NotTo(HaveOccurred())

			Expect(agent.Status.ToolConnections).To(HaveLen(1))
			Expect(agent.Status.ToolConnections[0].Healthy).ToNot(BeNil())
			Expect(*agent.Status.ToolConnections[0].Healthy).To(BeTrue())
			Expect(apimeta.FindStatusCondition(agent.Status.Conditions, "MCPDegraded")).To(BeNil())
		})

		It("should mark reachable server as healthy even if it returns errors", func() {
			// TCP dial probes reachability, not HTTP status. A server returning 500
			// is still reachable and therefore healthy from a connectivity standpoint.
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusInternalServerError)
			}))
			defer srv.Close()

			agentName := uniqueName()
			agent := &kubeswarmv1alpha1.SwarmAgent{
				ObjectMeta: metav1.ObjectMeta{Name: agentName, Namespace: namespace},
				Spec: kubeswarmv1alpha1.SwarmAgentSpec{
					Model:  "claude-sonnet-4-6",
					Prompt: &kubeswarmv1alpha1.AgentPrompt{Inline: "test"},
				},
			}
			Expect(k8sClient.Create(ctx, agent)).To(Succeed())
			defer func() { _ = k8sClient.Delete(ctx, agent) }()

			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: agentName, Namespace: namespace}, agent)).To(Succeed())

			r := newReconciler()
			servers := []kubeswarmv1alpha1.MCPToolSpec{
				{Name: "error-server", URL: srv.URL},
			}
			_, err := r.reconcileMCPHealth(ctx, agent, servers)
			Expect(err).NotTo(HaveOccurred())

			Expect(agent.Status.ToolConnections).To(HaveLen(1))
			Expect(agent.Status.ToolConnections[0].Healthy).ToNot(BeNil())
			Expect(*agent.Status.ToolConnections[0].Healthy).To(BeTrue())
			// TCP dial succeeds since server is listening - no MCPDegraded condition
			Expect(apimeta.FindStatusCondition(agent.Status.Conditions, "MCPDegraded")).To(BeNil())
		})

		It("should treat 401 as healthy (auth required but reachable)", func() {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusUnauthorized)
			}))
			defer srv.Close()

			agentName := uniqueName()
			agent := &kubeswarmv1alpha1.SwarmAgent{
				ObjectMeta: metav1.ObjectMeta{Name: agentName, Namespace: namespace},
				Spec: kubeswarmv1alpha1.SwarmAgentSpec{
					Model:  "claude-sonnet-4-6",
					Prompt: &kubeswarmv1alpha1.AgentPrompt{Inline: "test"},
				},
			}
			Expect(k8sClient.Create(ctx, agent)).To(Succeed())
			defer func() { _ = k8sClient.Delete(ctx, agent) }()

			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: agentName, Namespace: namespace}, agent)).To(Succeed())

			r := newReconciler()
			servers := []kubeswarmv1alpha1.MCPToolSpec{
				{Name: "auth-server", URL: srv.URL},
			}
			_, err := r.reconcileMCPHealth(ctx, agent, servers)
			Expect(err).NotTo(HaveOccurred())

			Expect(agent.Status.ToolConnections[0].Healthy).ToNot(BeNil())
			Expect(*agent.Status.ToolConnections[0].Healthy).To(BeTrue())
		})

		It("should set MCPDegraded for unreachable server", func() {
			agentName := uniqueName()
			agent := &kubeswarmv1alpha1.SwarmAgent{
				ObjectMeta: metav1.ObjectMeta{Name: agentName, Namespace: namespace},
				Spec: kubeswarmv1alpha1.SwarmAgentSpec{
					Model:  "claude-sonnet-4-6",
					Prompt: &kubeswarmv1alpha1.AgentPrompt{Inline: "test"},
				},
			}
			Expect(k8sClient.Create(ctx, agent)).To(Succeed())
			defer func() { _ = k8sClient.Delete(ctx, agent) }()

			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: agentName, Namespace: namespace}, agent)).To(Succeed())

			r := newReconciler()
			servers := []kubeswarmv1alpha1.MCPToolSpec{
				{Name: "gone-server", URL: "http://127.0.0.1:1"},
			}
			_, err := r.reconcileMCPHealth(ctx, agent, servers)
			Expect(err).NotTo(HaveOccurred())

			Expect(agent.Status.ToolConnections[0].Healthy).ToNot(BeNil())
			Expect(*agent.Status.ToolConnections[0].Healthy).To(BeFalse())
			cond := apimeta.FindStatusCondition(agent.Status.Conditions, "MCPDegraded")
			Expect(cond).NotTo(BeNil())
			Expect(cond.Status).To(Equal(metav1.ConditionTrue))
		})

		It("should clear MCPDegraded when no MCP servers configured", func() {
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
				Type:   "MCPDegraded",
				Status: metav1.ConditionTrue,
				Reason: "MCPUnreachable",
			})
			Expect(k8sClient.Create(ctx, agent)).To(Succeed())
			defer func() { _ = k8sClient.Delete(ctx, agent) }()

			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: agentName, Namespace: namespace}, agent)).To(Succeed())

			r := newReconciler()
			_, err := r.reconcileMCPHealth(ctx, agent, nil)
			Expect(err).NotTo(HaveOccurred())

			Expect(agent.Status.ToolConnections).To(BeNil())
			Expect(apimeta.FindStatusCondition(agent.Status.Conditions, "MCPDegraded")).To(BeNil())
		})

		It("should recover: MCPDegraded cleared when server recovers", func() {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusOK)
			}))
			defer srv.Close()

			agentName := uniqueName()
			agent := &kubeswarmv1alpha1.SwarmAgent{
				ObjectMeta: metav1.ObjectMeta{Name: agentName, Namespace: namespace},
				Spec: kubeswarmv1alpha1.SwarmAgentSpec{
					Model:  "claude-sonnet-4-6",
					Prompt: &kubeswarmv1alpha1.AgentPrompt{Inline: "test"},
				},
			}
			Expect(k8sClient.Create(ctx, agent)).To(Succeed())
			defer func() { _ = k8sClient.Delete(ctx, agent) }()

			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: agentName, Namespace: namespace}, agent)).To(Succeed())

			r := newReconciler()
			servers := []kubeswarmv1alpha1.MCPToolSpec{
				{Name: "recovered", URL: srv.URL},
			}

			// First: simulate degraded state.
			apimeta.SetStatusCondition(&agent.Status.Conditions, metav1.Condition{
				Type:   "MCPDegraded",
				Status: metav1.ConditionTrue,
				Reason: "MCPUnreachable",
			})

			// Now reconcile with healthy server - should clear condition.
			_, err := r.reconcileMCPHealth(ctx, agent, servers)
			Expect(err).NotTo(HaveOccurred())

			Expect(agent.Status.ToolConnections[0].Healthy).ToNot(BeNil())
			Expect(*agent.Status.ToolConnections[0].Healthy).To(BeTrue())
			Expect(apimeta.FindStatusCondition(agent.Status.Conditions, "MCPDegraded")).To(BeNil())
		})
	})
})
