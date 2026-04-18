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
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kubeswarmv1alpha1 "github.com/kubeswarm/kubeswarm/api/v1alpha1"
)

func TestSwarmMemoryController(t *testing.T) {
	const namespace = "default"
	ctx := context.Background()

	newReconciler := func() *SwarmMemoryReconciler {
		return &SwarmMemoryReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
	}

	reconcileAndFetch := func(t *testing.T, name string) *kubeswarmv1alpha1.SwarmMemory {
		t.Helper()
		nn := types.NamespacedName{Name: name, Namespace: namespace}
		_, err := newReconciler().Reconcile(ctx, reconcile.Request{NamespacedName: nn})
		requireNoError(t, err)
		mem := &kubeswarmv1alpha1.SwarmMemory{}
		requireNoError(t, k8sClient.Get(ctx, nn, mem))
		return mem
	}

	cleanup := func(t *testing.T, name string) {
		t.Helper()
		mem := &kubeswarmv1alpha1.SwarmMemory{}
		nn := types.NamespacedName{Name: name, Namespace: namespace}
		if err := k8sClient.Get(ctx, nn, mem); err == nil {
			requireNoError(t, k8sClient.Delete(ctx, mem))
		}
	}

	t.Run("in-context backend", func(t *testing.T) {
		const name = "mem-incontext"
		t.Cleanup(func() { cleanup(t, name) })

		t.Run("should set Ready=True with no extra config required", func(t *testing.T) {
			requireNoError(t, k8sClient.Create(ctx, &kubeswarmv1alpha1.SwarmMemory{
				ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
				Spec: kubeswarmv1alpha1.SwarmMemorySpec{
					Backend: kubeswarmv1alpha1.MemoryBackendInContext,
				},
			}))

			mem := reconcileAndFetch(t, name)
			cond := apimeta.FindStatusCondition(mem.Status.Conditions, "Ready")
			if cond == nil {
				t.Fatal("expected Ready condition, got nil")
			}
			requireEqual(t, cond.Status, metav1.ConditionTrue)
			requireEqual(t, cond.Reason, "Accepted")
		})
	})

	t.Run("redis backend", func(t *testing.T) {
		t.Run("should set Ready=True when secretRef is provided", func(t *testing.T) {
			const name = "mem-redis-ok"
			t.Cleanup(func() { cleanup(t, name) })

			requireNoError(t, k8sClient.Create(ctx, &kubeswarmv1alpha1.SwarmMemory{
				ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
				Spec: kubeswarmv1alpha1.SwarmMemorySpec{
					Backend: kubeswarmv1alpha1.MemoryBackendRedis,
					Redis: &kubeswarmv1alpha1.RedisMemoryConfig{
						SecretRef:  corev1.LocalObjectReference{Name: "redis-secret"},
						TTLSeconds: 3600,
					},
				},
			}))

			mem := reconcileAndFetch(t, name)
			cond := apimeta.FindStatusCondition(mem.Status.Conditions, "Ready")
			if cond == nil {
				t.Fatal("expected Ready condition, got nil")
			}
			requireEqual(t, cond.Status, metav1.ConditionTrue)
		})

		t.Run("should set Ready=False when spec.redis is missing", func(t *testing.T) {
			const name = "mem-redis-nospec"
			t.Cleanup(func() { cleanup(t, name) })

			requireNoError(t, k8sClient.Create(ctx, &kubeswarmv1alpha1.SwarmMemory{
				ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
				Spec: kubeswarmv1alpha1.SwarmMemorySpec{
					Backend: kubeswarmv1alpha1.MemoryBackendRedis,
					// Redis field intentionally omitted
				},
			}))

			mem := reconcileAndFetch(t, name)
			cond := apimeta.FindStatusCondition(mem.Status.Conditions, "Ready")
			if cond == nil {
				t.Fatal("expected Ready condition, got nil")
			}
			requireEqual(t, cond.Status, metav1.ConditionFalse)
			requireEqual(t, cond.Reason, "InvalidSpec")
			requireContains(t, cond.Message, "spec.redis is required")
		})

		t.Run("should set Ready=False when secretRef.name is empty", func(t *testing.T) {
			const name = "mem-redis-nosecret"
			t.Cleanup(func() { cleanup(t, name) })

			requireNoError(t, k8sClient.Create(ctx, &kubeswarmv1alpha1.SwarmMemory{
				ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
				Spec: kubeswarmv1alpha1.SwarmMemorySpec{
					Backend: kubeswarmv1alpha1.MemoryBackendRedis,
					Redis: &kubeswarmv1alpha1.RedisMemoryConfig{
						SecretRef: corev1.LocalObjectReference{Name: ""},
					},
				},
			}))

			mem := reconcileAndFetch(t, name)
			cond := apimeta.FindStatusCondition(mem.Status.Conditions, "Ready")
			if cond == nil {
				t.Fatal("expected Ready condition, got nil")
			}
			requireEqual(t, cond.Status, metav1.ConditionFalse)
			requireContains(t, cond.Message, "secretRef.name is required")
		})
	})

	t.Run("vector-store backend", func(t *testing.T) {
		t.Run("should set Ready=True when endpoint is provided", func(t *testing.T) {
			const name = "mem-vectorstore-ok"
			t.Cleanup(func() { cleanup(t, name) })

			requireNoError(t, k8sClient.Create(ctx, &kubeswarmv1alpha1.SwarmMemory{
				ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
				Spec: kubeswarmv1alpha1.SwarmMemorySpec{
					Backend: kubeswarmv1alpha1.MemoryBackendVectorStore,
					VectorStore: &kubeswarmv1alpha1.VectorStoreMemoryConfig{
						Provider:   kubeswarmv1alpha1.VectorStoreProviderQdrant,
						Endpoint:   "http://qdrant.qdrant.svc.cluster.local:6333",
						Collection: "agent-memories",
					},
					Embedding: &kubeswarmv1alpha1.EmbeddingConfig{
						Model: "text-embedding-3-small",
					},
				},
			}))

			mem := reconcileAndFetch(t, name)
			cond := apimeta.FindStatusCondition(mem.Status.Conditions, "Ready")
			if cond == nil {
				t.Fatal("expected Ready condition, got nil")
			}
			requireEqual(t, cond.Status, metav1.ConditionTrue)
		})

		t.Run("should set Ready=False when spec.vectorStore is missing", func(t *testing.T) {
			const name = "mem-vectorstore-nospec"
			t.Cleanup(func() { cleanup(t, name) })

			requireNoError(t, k8sClient.Create(ctx, &kubeswarmv1alpha1.SwarmMemory{
				ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
				Spec: kubeswarmv1alpha1.SwarmMemorySpec{
					Backend: kubeswarmv1alpha1.MemoryBackendVectorStore,
					// VectorStore field intentionally omitted
					Embedding: &kubeswarmv1alpha1.EmbeddingConfig{
						Model: "text-embedding-3-small",
					},
				},
			}))

			mem := reconcileAndFetch(t, name)
			cond := apimeta.FindStatusCondition(mem.Status.Conditions, "Ready")
			if cond == nil {
				t.Fatal("expected Ready condition, got nil")
			}
			requireEqual(t, cond.Status, metav1.ConditionFalse)
			requireContains(t, cond.Message, "spec.vectorStore is required")
		})

		t.Run("should set Ready=False when endpoint is empty", func(t *testing.T) {
			const name = "mem-vectorstore-noendpoint"
			t.Cleanup(func() { cleanup(t, name) })

			requireNoError(t, k8sClient.Create(ctx, &kubeswarmv1alpha1.SwarmMemory{
				ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
				Spec: kubeswarmv1alpha1.SwarmMemorySpec{
					Backend: kubeswarmv1alpha1.MemoryBackendVectorStore,
					VectorStore: &kubeswarmv1alpha1.VectorStoreMemoryConfig{
						Provider: kubeswarmv1alpha1.VectorStoreProviderQdrant,
						Endpoint: "", // missing
					},
					Embedding: &kubeswarmv1alpha1.EmbeddingConfig{
						Model: "text-embedding-3-small",
					},
				},
			}))

			mem := reconcileAndFetch(t, name)
			cond := apimeta.FindStatusCondition(mem.Status.Conditions, "Ready")
			if cond == nil {
				t.Fatal("expected Ready condition, got nil")
			}
			requireEqual(t, cond.Status, metav1.ConditionFalse)
			requireContains(t, cond.Message, "endpoint is required")
		})
	})

	t.Run("when the SwarmMemory does not exist", func(t *testing.T) {
		t.Run("should return without error", func(t *testing.T) {
			_, err := newReconciler().Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: "does-not-exist", Namespace: namespace},
			})
			requireNoError(t, err)
		})
	})
}
