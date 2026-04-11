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
				apimeta.SetStatusCondition(&notify.Status.Conditions, metav1.Condition{
					Type:               kubeswarmv1alpha1.ConditionReady,
					Status:             metav1.ConditionFalse,
					ObservedGeneration: notify.Generation,
					Reason:             "InvalidChannelConfig",
					Message:            "channel[" + itoa(i) + "] type=webhook but webhook config is missing",
				})
				return ctrl.Result{}, r.Status().Update(ctx, notify)
			}
			if ch.Webhook.URL == "" && ch.Webhook.URLFrom == nil {
				apimeta.SetStatusCondition(&notify.Status.Conditions, metav1.Condition{
					Type:               kubeswarmv1alpha1.ConditionReady,
					Status:             metav1.ConditionFalse,
					ObservedGeneration: notify.Generation,
					Reason:             "InvalidChannelConfig",
					Message:            "channel[" + itoa(i) + "] webhook requires url or urlFrom",
				})
				return ctrl.Result{}, r.Status().Update(ctx, notify)
			}
		case kubeswarmv1alpha1.NotifyChannelSlack:
			if ch.Slack == nil {
				apimeta.SetStatusCondition(&notify.Status.Conditions, metav1.Condition{
					Type:               kubeswarmv1alpha1.ConditionReady,
					Status:             metav1.ConditionFalse,
					ObservedGeneration: notify.Generation,
					Reason:             "InvalidChannelConfig",
					Message:            "channel[" + itoa(i) + "] type=slack but slack config is missing",
				})
				return ctrl.Result{}, r.Status().Update(ctx, notify)
			}
		}
	}

	apimeta.SetStatusCondition(&notify.Status.Conditions, metav1.Condition{
		Type:               kubeswarmv1alpha1.ConditionReady,
		Status:             metav1.ConditionTrue,
		ObservedGeneration: notify.Generation,
		Reason:             "Accepted",
		Message:            "SwarmNotify policy is valid",
	})
	return ctrl.Result{}, r.Status().Update(ctx, notify)
}

// itoa converts an int to its string representation (avoids strconv import).
func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	buf := make([]byte, 0, 4)
	for i > 0 {
		buf = append([]byte{byte('0' + i%10)}, buf...)
		i /= 10
	}
	if neg {
		buf = append([]byte{'-'}, buf...)
	}
	return string(buf)
}

// SetupWithManager sets up the controller with the Manager.
func (r *SwarmNotifyReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&kubeswarmv1alpha1.SwarmNotify{}).
		Named("swarmnotify").
		Complete(WithMetrics(r, "swarmnotify"))
}
