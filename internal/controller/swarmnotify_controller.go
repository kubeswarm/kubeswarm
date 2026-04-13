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
	"strconv"

	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kubeswarmv1alpha1 "github.com/kubeswarm/kubeswarm/api/v1alpha1"
)

// SwarmNotifyReconciler reconciles SwarmNotify objects.
type SwarmNotifyReconciler struct {
	client.Client
	Scheme     *runtime.Scheme
	Dispatcher *NotifyDispatcher
}

// +kubebuilder:rbac:groups=kubeswarm.io,resources=swarmnotifies,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=kubeswarm.io,resources=swarmnotifies/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=kubeswarm.io,resources=swarmnotifies/finalizers,verbs=update

func (r *SwarmNotifyReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	notify := &kubeswarmv1alpha1.SwarmNotify{}
	if err := r.Get(ctx, req.NamespacedName, notify); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if !notify.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, nil
	}

	// Handle dashboard Test button: fire a synthetic notification and clear the annotation.
	if _, ok := notify.Annotations["kubeswarm/test-trigger"]; ok {
		if r.Dispatcher != nil {
			r.Dispatcher.DispatchTest(ctx, notify)
		}
		patch := client.MergeFrom(notify.DeepCopy())
		delete(notify.Annotations, "kubeswarm/test-trigger")
		if err := r.Patch(ctx, notify, patch); err != nil {
			return ctrl.Result{}, err
		}
	}

	// Validate that each channel has the required sub-spec.
	for i, ch := range notify.Spec.Channels {
		switch ch.Type {
		case kubeswarmv1alpha1.NotifyChannelWebhook:
			if ch.Webhook == nil {
				setCondition(&notify.Status.Conditions, notify.Generation, "", metav1.ConditionFalse, "InvalidChannelConfig",
					"channel["+strconv.Itoa(i)+"] type=webhook but webhook config is missing")
				return ctrl.Result{}, r.Status().Update(ctx, notify)
			}
			if ch.Webhook.URL == "" && ch.Webhook.URLFrom == nil {
				setCondition(&notify.Status.Conditions, notify.Generation, "", metav1.ConditionFalse, "InvalidChannelConfig",
					"channel["+strconv.Itoa(i)+"] webhook requires url or urlFrom")
				return ctrl.Result{}, r.Status().Update(ctx, notify)
			}
		case kubeswarmv1alpha1.NotifyChannelSlack:
			if ch.Slack == nil {
				setCondition(&notify.Status.Conditions, notify.Generation, "", metav1.ConditionFalse, "InvalidChannelConfig",
					"channel["+strconv.Itoa(i)+"] type=slack but slack config is missing")
				return ctrl.Result{}, r.Status().Update(ctx, notify)
			}
		}
	}

	// Guard: skip status write when already up-to-date.
	existingCond := apimeta.FindStatusCondition(notify.Status.Conditions, kubeswarmv1alpha1.ConditionReady)
	if notify.Status.ObservedGeneration == notify.Generation &&
		existingCond != nil && existingCond.Status == metav1.ConditionTrue && existingCond.Reason == "Accepted" {
		return ctrl.Result{}, nil
	}

	notify.Status.ObservedGeneration = notify.Generation
	setCondition(&notify.Status.Conditions, notify.Generation, "", metav1.ConditionTrue, "Accepted", "SwarmNotify policy is valid")
	return ctrl.Result{}, r.Status().Update(ctx, notify)
}

// SetupWithManager sets up the controller with the Manager.
func (r *SwarmNotifyReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&kubeswarmv1alpha1.SwarmNotify{}).
		Named("swarmnotify").
		Complete(WithMetrics(r, "swarmnotify"))
}
