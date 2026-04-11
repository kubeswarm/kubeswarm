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
	"time"

	"go.opentelemetry.io/otel/attribute"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/kubeswarm/kubeswarm/pkg/observability"
)

// metricsReconciler wraps any reconcile.Reconciler and records
// kubeswarm.reconcile.duration and kubeswarm.reconcile.errors metrics.
type metricsReconciler struct {
	inner      reconcile.Reconciler
	metrics    *observability.OperatorMetrics
	controller string
}

// WithMetrics wraps r so that every Reconcile call records operator metrics.
// controller is the label value for the "controller" metric attribute (e.g. "swarmagent").
// If NewOperatorMetrics fails (which only happens on SDK misconfiguration), the
// original reconciler is returned unwrapped.
func WithMetrics(r reconcile.Reconciler, controller string) reconcile.Reconciler {
	om, err := observability.NewOperatorMetrics()
	if err != nil {
		return r
	}
	return &metricsReconciler{inner: r, metrics: om, controller: controller}
}

func (r *metricsReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	start := time.Now()
	result, err := r.inner.Reconcile(ctx, req)
	r.metrics.RecordReconcile(ctx, start, err != nil,
		attribute.String("controller", r.controller),
		attribute.String("namespace", req.Namespace),
		attribute.String("result", reconcileResult(result, err)),
	)
	return result, err
}

func reconcileResult(result ctrl.Result, err error) string {
	if err != nil {
		return "error"
	}
	if result.RequeueAfter > 0 {
		return "requeue"
	}
	return "ok"
}
