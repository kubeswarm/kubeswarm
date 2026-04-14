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

	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kubeswarmv1alpha1 "github.com/kubeswarm/kubeswarm/api/v1alpha1"
)

func TestSwarmSettingsController(t *testing.T) {
	const (
		resourceName = "test-swarmsettings"
		namespace    = "default"
	)

	ctx := context.Background()
	namespacedName := types.NamespacedName{Name: resourceName, Namespace: namespace}

	cleanupSettings := func(t *testing.T) {
		t.Helper()
		cfg := &kubeswarmv1alpha1.SwarmSettings{}
		if err := k8sClient.Get(ctx, namespacedName, cfg); err == nil {
			requireNoError(t, k8sClient.Delete(ctx, cfg))
		}
	}

	createMinimalSettings := func(t *testing.T) {
		t.Helper()
		resource := &kubeswarmv1alpha1.SwarmSettings{
			ObjectMeta: metav1.ObjectMeta{
				Name:      resourceName,
				Namespace: namespace,
			},
		}
		requireNoError(t, k8sClient.Create(ctx, resource))
	}

	createSettingsWithSpec := func(t *testing.T) {
		t.Helper()
		resource := &kubeswarmv1alpha1.SwarmSettings{
			ObjectMeta: metav1.ObjectMeta{
				Name:      resourceName,
				Namespace: namespace,
			},
			Spec: kubeswarmv1alpha1.SwarmSettingsSpec{
				Temperature:   "0.7",
				OutputFormat:  "structured-json",
				MemoryBackend: kubeswarmv1alpha1.MemoryBackendInContext,
				PromptFragments: &kubeswarmv1alpha1.PromptFragments{
					Persona:     "You are an expert analyst.",
					OutputRules: "Always cite your sources.",
				},
			},
		}
		requireNoError(t, k8sClient.Create(ctx, resource))
	}

	t.Run("When reconciling a minimal SwarmSettings", func(t *testing.T) {
		t.Run("should set Ready=True with reason Accepted", func(t *testing.T) {
			createMinimalSettings(t)
			t.Cleanup(func() { cleanupSettings(t) })

			r := &SwarmSettingsReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: namespacedName})
			requireNoError(t, err)

			cfg := &kubeswarmv1alpha1.SwarmSettings{}
			requireNoError(t, k8sClient.Get(ctx, namespacedName, cfg))

			cond := apimeta.FindStatusCondition(cfg.Status.Conditions, "Ready")
			if cond == nil {
				t.Fatal("expected Ready condition, got nil")
			}
			requireEqual(t, cond.Status, metav1.ConditionTrue)
			requireEqual(t, cond.Reason, "Accepted")
		})

		t.Run("should set ObservedGeneration to match the resource generation", func(t *testing.T) {
			createMinimalSettings(t)
			t.Cleanup(func() { cleanupSettings(t) })

			r := &SwarmSettingsReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: namespacedName})
			requireNoError(t, err)

			cfg := &kubeswarmv1alpha1.SwarmSettings{}
			requireNoError(t, k8sClient.Get(ctx, namespacedName, cfg))
			requireEqual(t, cfg.Status.ObservedGeneration, cfg.Generation)
		})
	})

	t.Run("When reconciling an SwarmSettings with spec values", func(t *testing.T) {
		t.Run("should set Ready=True regardless of which spec fields are set", func(t *testing.T) {
			createSettingsWithSpec(t)
			t.Cleanup(func() { cleanupSettings(t) })

			r := &SwarmSettingsReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: namespacedName})
			requireNoError(t, err)

			cfg := &kubeswarmv1alpha1.SwarmSettings{}
			requireNoError(t, k8sClient.Get(ctx, namespacedName, cfg))

			cond := apimeta.FindStatusCondition(cfg.Status.Conditions, "Ready")
			if cond == nil {
				t.Fatal("expected Ready condition, got nil")
			}
			requireEqual(t, cond.Status, metav1.ConditionTrue)

			// Verify spec values are preserved unchanged.
			requireEqual(t, cfg.Spec.Temperature, "0.7")
			requireEqual(t, cfg.Spec.OutputFormat, "structured-json")
			if cfg.Spec.PromptFragments == nil {
				t.Fatal("expected non-nil PromptFragments")
			}
			requireEqual(t, cfg.Spec.PromptFragments.Persona, "You are an expert analyst.")
		})
	})

	t.Run("When reconciling a nonexistent SwarmSettings", func(t *testing.T) {
		t.Run("should return without error", func(t *testing.T) {
			r := &SwarmSettingsReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
			_, err := r.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: "does-not-exist", Namespace: namespace},
			})
			requireNoError(t, err)
		})
	})
}
