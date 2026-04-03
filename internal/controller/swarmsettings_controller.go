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

	"k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kubeswarmv1alpha1 "github.com/kubeswarm/kubeswarm/api/v1alpha1"
)

// SwarmSettingsReconciler reconciles a SwarmSettings object
type SwarmSettingsReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=kubeswarm.io,resources=swarmsettings,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=kubeswarm.io,resources=swarmsettings/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=kubeswarm.io,resources=swarmsettings/finalizers,verbs=update

// SwarmSettings is a storage-only resource (analogous to ConfigMap).
// The reconciler just acknowledges the resource and sets a Ready condition.
// SwarmAgents reference it by name; the operator reads it during pod construction.
func (r *SwarmSettingsReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	swarmSettings := &kubeswarmv1alpha1.SwarmSettings{}
	if err := r.Get(ctx, req.NamespacedName, swarmSettings); err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	if !swarmSettings.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, nil
	}

	swarmSettings.Status.ObservedGeneration = swarmSettings.Generation
	apimeta.SetStatusCondition(&swarmSettings.Status.Conditions, metav1.Condition{
		Type:               kubeswarmv1alpha1.ConditionReady,
		Status:             metav1.ConditionTrue,
		ObservedGeneration: swarmSettings.Generation,
		Reason:             "Accepted",
		Message:            "SwarmSettings is valid and available",
	})

	return ctrl.Result{}, r.Status().Update(ctx, swarmSettings)
}

// SetupWithManager sets up the controller with the Manager.
func (r *SwarmSettingsReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&kubeswarmv1alpha1.SwarmSettings{}).
		Named("swarmsettings").
		Complete(WithMetrics(r, "swarmsettings"))
}
