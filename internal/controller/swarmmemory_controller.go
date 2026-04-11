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

	"k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kubeswarmv1alpha1 "github.com/kubeswarm/kubeswarm/api/v1alpha1"
)

// SwarmMemoryReconciler reconciles a SwarmMemory object.
type SwarmMemoryReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=kubeswarm.io,resources=swarmmemories,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=kubeswarm.io,resources=swarmmemories/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=kubeswarm.io,resources=swarmmemories/finalizers,verbs=update

// SwarmMemory is a configuration resource (analogous to PersistentVolumeClaim).
// The reconciler validates the spec and sets a Ready condition.
// SwarmAgents reference it by name; the operator reads it during pod construction.
func (r *SwarmMemoryReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	swarmMemory := &kubeswarmv1alpha1.SwarmMemory{}
	if err := r.Get(ctx, req.NamespacedName, swarmMemory); err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	if !swarmMemory.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, nil
	}

	if err := r.validate(swarmMemory); err != nil {
		swarmMemory.Status.ObservedGeneration = swarmMemory.Generation
		apimeta.SetStatusCondition(&swarmMemory.Status.Conditions, metav1.Condition{
			Type:               kubeswarmv1alpha1.ConditionReady,
			Status:             metav1.ConditionFalse,
			ObservedGeneration: swarmMemory.Generation,
			Reason:             "InvalidSpec",
			Message:            err.Error(),
		})
		return ctrl.Result{}, r.Status().Update(ctx, swarmMemory)
	}

	swarmMemory.Status.ObservedGeneration = swarmMemory.Generation
	apimeta.SetStatusCondition(&swarmMemory.Status.Conditions, metav1.Condition{
		Type:               kubeswarmv1alpha1.ConditionReady,
		Status:             metav1.ConditionTrue,
		ObservedGeneration: swarmMemory.Generation,
		Reason:             "Accepted",
		Message:            "SwarmMemory is valid and available",
	})

	return ctrl.Result{}, r.Status().Update(ctx, swarmMemory)
}

// validate checks that the spec is consistent (backend-specific config is present).
func (r *SwarmMemoryReconciler) validate(swarmMemory *kubeswarmv1alpha1.SwarmMemory) error {
	switch swarmMemory.Spec.Backend {
	case kubeswarmv1alpha1.MemoryBackendRedis:
		if swarmMemory.Spec.Redis == nil {
			return fmt.Errorf("spec.redis is required when backend is %q", kubeswarmv1alpha1.MemoryBackendRedis)
		}
		if swarmMemory.Spec.Redis.SecretRef.Name == "" {
			return fmt.Errorf("spec.redis.secretRef.name is required")
		}
	case kubeswarmv1alpha1.MemoryBackendVectorStore:
		if swarmMemory.Spec.VectorStore == nil {
			return fmt.Errorf("spec.vectorStore is required when backend is %q", kubeswarmv1alpha1.MemoryBackendVectorStore)
		}
		if swarmMemory.Spec.VectorStore.Endpoint == "" {
			return fmt.Errorf("spec.vectorStore.endpoint is required")
		}
	case kubeswarmv1alpha1.MemoryBackendInContext:
		// No additional config required.
	}
	return nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *SwarmMemoryReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&kubeswarmv1alpha1.SwarmMemory{}).
		Named("swarmmemory").
		Complete(WithMetrics(r, "swarmmemory"))
}
