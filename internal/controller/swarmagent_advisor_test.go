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
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kubeswarmv1alpha1 "github.com/kubeswarm/kubeswarm/api/v1alpha1"
)

const reasonAdvisorNotFound = "AdvisorNotFound"

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func advisorTestClient(objs ...client.Object) client.Client {
	s := runtime.NewScheme()
	_ = kubeswarmv1alpha1.AddToScheme(s)
	return fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(objs...).
		Build()
}

func makeAdvisorTargetAgent(name string, readyReplicas int32) *kubeswarmv1alpha1.SwarmAgent {
	return &kubeswarmv1alpha1.SwarmAgent{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Spec: kubeswarmv1alpha1.SwarmAgentSpec{
			Model:  "claude-opus-4-6",
			Prompt: &kubeswarmv1alpha1.AgentPrompt{Inline: "You are an advisor."},
		},
		Status: kubeswarmv1alpha1.SwarmAgentStatus{
			ReadyReplicas: readyReplicas,
			Replicas:      readyReplicas,
		},
	}
}

func makeExecutorWithAdvisors(advisors []kubeswarmv1alpha1.AgentConnection) *kubeswarmv1alpha1.SwarmAgent {
	return &kubeswarmv1alpha1.SwarmAgent{
		ObjectMeta: metav1.ObjectMeta{Name: "executor", Namespace: "default"},
		Spec: kubeswarmv1alpha1.SwarmAgentSpec{
			Model:  "claude-sonnet-4-6",
			Prompt: &kubeswarmv1alpha1.AgentPrompt{Inline: "You are a coder."},
			Agents: advisors,
		},
	}
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestReconcileAdvisorConnections_TargetReady(t *testing.T) {
	advisor := makeAdvisorTargetAgent("senior-arch", 2)
	executor := makeExecutorWithAdvisors([]kubeswarmv1alpha1.AgentConnection{
		{
			Name:     "architect",
			AgentRef: &corev1.LocalObjectReference{Name: "senior-arch"},
			Role:     kubeswarmv1alpha1.AgentConnectionRoleAdvisor,
			ContextPropagation: &kubeswarmv1alpha1.ContextPropagationConfig{
				RecentMessages: 20,
			},
		},
	})

	c := advisorTestClient(advisor)
	statuses, condition := reconcileAdvisorConnections(context.Background(), c, executor)

	if len(statuses) != 1 {
		t.Fatalf("expected 1 advisor status, got %d", len(statuses))
	}
	s := statuses[0]
	if s.Name != "architect" {
		t.Errorf("Name = %q, want %q", s.Name, "architect")
	}
	if !s.Ready {
		t.Error("Ready should be true")
	}
	if !s.ToolInjected {
		t.Error("ToolInjected should be true")
	}
	if s.ToolName != "consult_architect" {
		t.Errorf("ToolName = %q, want %q", s.ToolName, "consult_architect")
	}
	if condition.Type != "AdvisorsReady" {
		t.Errorf("condition Type = %q, want %q", condition.Type, "AdvisorsReady")
	}
	if condition.Status != metav1.ConditionTrue {
		t.Errorf("condition Status = %q, want True", condition.Status)
	}
}

func TestReconcileAdvisorConnections_TargetNotFound(t *testing.T) {
	executor := makeExecutorWithAdvisors([]kubeswarmv1alpha1.AgentConnection{
		{
			Name:     "ghost",
			AgentRef: &corev1.LocalObjectReference{Name: "does-not-exist"},
			Role:     kubeswarmv1alpha1.AgentConnectionRoleAdvisor,
			ContextPropagation: &kubeswarmv1alpha1.ContextPropagationConfig{
				RecentMessages: 10,
			},
		},
	})

	c := advisorTestClient()
	statuses, condition := reconcileAdvisorConnections(context.Background(), c, executor)

	if len(statuses) != 1 {
		t.Fatalf("expected 1 advisor status, got %d", len(statuses))
	}
	if statuses[0].Ready {
		t.Error("Ready should be false when target not found")
	}
	if condition.Status != metav1.ConditionFalse {
		t.Errorf("condition Status = %q, want False", condition.Status)
	}
	if condition.Reason != reasonAdvisorNotFound {
		t.Errorf("condition Reason = %q, want %q", condition.Reason, "AdvisorNotFound")
	}
}

func TestReconcileAdvisorConnections_TargetNoReplicas(t *testing.T) {
	advisor := makeAdvisorTargetAgent("senior-arch", 0)
	executor := makeExecutorWithAdvisors([]kubeswarmv1alpha1.AgentConnection{
		{
			Name:     "architect",
			AgentRef: &corev1.LocalObjectReference{Name: "senior-arch"},
			Role:     kubeswarmv1alpha1.AgentConnectionRoleAdvisor,
			ContextPropagation: &kubeswarmv1alpha1.ContextPropagationConfig{
				RecentMessages: 20,
			},
		},
	})

	c := advisorTestClient(advisor)
	statuses, condition := reconcileAdvisorConnections(context.Background(), c, executor)

	if len(statuses) != 1 {
		t.Fatalf("expected 1 advisor status, got %d", len(statuses))
	}
	if statuses[0].Ready {
		t.Error("Ready should be false when target has 0 replicas")
	}
	if condition.Status != metav1.ConditionFalse {
		t.Errorf("condition Status = %q, want False", condition.Status)
	}
	if condition.Reason != "AdvisorNoReplicas" {
		t.Errorf("condition Reason = %q, want %q", condition.Reason, "AdvisorNoReplicas")
	}
}

func TestReconcileAdvisorConnections_MultipleAllHealthy(t *testing.T) {
	arch := makeAdvisorTargetAgent("senior-arch", 1)
	sec := makeAdvisorTargetAgent("sec-reviewer", 1)
	executor := makeExecutorWithAdvisors([]kubeswarmv1alpha1.AgentConnection{
		{
			Name:     "architect",
			AgentRef: &corev1.LocalObjectReference{Name: "senior-arch"},
			Role:     kubeswarmv1alpha1.AgentConnectionRoleAdvisor,
			ContextPropagation: &kubeswarmv1alpha1.ContextPropagationConfig{
				RecentMessages: 30,
			},
		},
		{
			Name:     "security",
			AgentRef: &corev1.LocalObjectReference{Name: "sec-reviewer"},
			Role:     kubeswarmv1alpha1.AgentConnectionRoleAdvisor,
			ContextPropagation: &kubeswarmv1alpha1.ContextPropagationConfig{
				RecentMessages: 5,
				ToolName:       "review_security",
			},
		},
	})

	c := advisorTestClient(arch, sec)
	statuses, condition := reconcileAdvisorConnections(context.Background(), c, executor)

	if len(statuses) != 2 {
		t.Fatalf("expected 2 advisor statuses, got %d", len(statuses))
	}
	for _, s := range statuses {
		if !s.Ready {
			t.Errorf("advisor %q should be Ready", s.Name)
		}
		if !s.ToolInjected {
			t.Errorf("advisor %q should have ToolInjected", s.Name)
		}
	}
	// Check custom tool name override.
	if statuses[1].ToolName != "review_security" {
		t.Errorf("second advisor ToolName = %q, want %q", statuses[1].ToolName, "review_security")
	}
	if condition.Status != metav1.ConditionTrue {
		t.Errorf("condition Status = %q, want True", condition.Status)
	}
}

func TestReconcileAdvisorConnections_MixedHealth(t *testing.T) {
	arch := makeAdvisorTargetAgent("senior-arch", 1)
	// sec-reviewer not created - will be not found.
	executor := makeExecutorWithAdvisors([]kubeswarmv1alpha1.AgentConnection{
		{
			Name:     "architect",
			AgentRef: &corev1.LocalObjectReference{Name: "senior-arch"},
			Role:     kubeswarmv1alpha1.AgentConnectionRoleAdvisor,
			ContextPropagation: &kubeswarmv1alpha1.ContextPropagationConfig{
				RecentMessages: 30,
			},
		},
		{
			Name:     "security",
			AgentRef: &corev1.LocalObjectReference{Name: "sec-reviewer"},
			Role:     kubeswarmv1alpha1.AgentConnectionRoleAdvisor,
			ContextPropagation: &kubeswarmv1alpha1.ContextPropagationConfig{
				RecentMessages: 5,
			},
		},
	})

	c := advisorTestClient(arch)
	statuses, condition := reconcileAdvisorConnections(context.Background(), c, executor)

	if len(statuses) != 2 {
		t.Fatalf("expected 2 advisor statuses, got %d", len(statuses))
	}
	if !statuses[0].Ready {
		t.Error("architect should be Ready")
	}
	if statuses[1].Ready {
		t.Error("security should not be Ready (target missing)")
	}
	if condition.Status != metav1.ConditionFalse {
		t.Errorf("condition Status = %q, want False", condition.Status)
	}
	// Message should name the unhealthy advisor.
	if !containsString(condition.Message, "security") {
		t.Errorf("condition Message should mention unhealthy advisor 'security', got: %q", condition.Message)
	}
}

func TestReconcileAdvisorConnections_NoAdvisors(t *testing.T) {
	executor := makeExecutorWithAdvisors(nil)

	c := advisorTestClient()
	statuses, condition := reconcileAdvisorConnections(context.Background(), c, executor)

	if len(statuses) != 0 {
		t.Fatalf("expected 0 advisor statuses, got %d", len(statuses))
	}
	// With no advisors, condition should be True (vacuously).
	if condition.Status != metav1.ConditionTrue {
		t.Errorf("condition Status = %q, want True (no advisors)", condition.Status)
	}
}

func TestReconcileAdvisorConnections_SkipsToolRoleConnections(t *testing.T) {
	executor := makeExecutorWithAdvisors([]kubeswarmv1alpha1.AgentConnection{
		{
			Name:     "formatter",
			AgentRef: &corev1.LocalObjectReference{Name: "fmt"},
			Role:     kubeswarmv1alpha1.AgentConnectionRoleTool,
		},
	})

	c := advisorTestClient()
	statuses, _ := reconcileAdvisorConnections(context.Background(), c, executor)

	if len(statuses) != 0 {
		t.Fatalf("expected 0 advisor statuses for tool-role connections, got %d", len(statuses))
	}
}

func containsString(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
