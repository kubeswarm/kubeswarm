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
	"errors"
	"testing"
	"time"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// fakeReconciler is a minimal reconcile.Reconciler used to test WithMetrics wrapping.
type fakeReconciler struct {
	result ctrl.Result
	err    error
	called bool
}

func (f *fakeReconciler) Reconcile(_ context.Context, _ reconcile.Request) (ctrl.Result, error) {
	f.called = true
	return f.result, f.err
}

func TestReconcileResult(t *testing.T) {
	t.Run("returns 'error' when err is set", func(t *testing.T) {
		requireEqual(t, reconcileResult(ctrl.Result{}, errors.New("boom")), "error")
	})

	t.Run("returns 'requeue' when RequeueAfter > 0 and no error", func(t *testing.T) {
		requireEqual(t, reconcileResult(ctrl.Result{RequeueAfter: time.Second}, nil), "requeue")
	})

	t.Run("returns 'ok' for an empty result with no error", func(t *testing.T) {
		requireEqual(t, reconcileResult(ctrl.Result{}, nil), "ok")
	})

	t.Run("returns 'error' even when RequeueAfter is set alongside an error", func(t *testing.T) {
		requireEqual(t, reconcileResult(ctrl.Result{RequeueAfter: time.Second}, errors.New("x")), "error")
	})
}

func TestWithMetrics(t *testing.T) {
	t.Run("wraps the reconciler and forwards successful calls", func(t *testing.T) {
		inner := &fakeReconciler{result: ctrl.Result{}}
		wrapped := WithMetrics(inner, "test-controller")
		result, err := wrapped.Reconcile(context.Background(), reconcile.Request{})
		requireNoError(t, err)
		requireEqual(t, result, ctrl.Result{})
		requireTrue(t, inner.called)
	})

	t.Run("propagates errors from the inner reconciler", func(t *testing.T) {
		inner := &fakeReconciler{err: errors.New("inner error")}
		wrapped := WithMetrics(inner, "test-controller")
		_, err := wrapped.Reconcile(context.Background(), reconcile.Request{})
		requireContains(t, err.Error(), "inner error")
	})

	t.Run("forwards a requeue result from the inner reconciler", func(t *testing.T) {
		inner := &fakeReconciler{result: ctrl.Result{RequeueAfter: 5 * time.Second}}
		wrapped := WithMetrics(inner, "test-controller")
		result, err := wrapped.Reconcile(context.Background(), reconcile.Request{})
		requireNoError(t, err)
		requireEqual(t, result.RequeueAfter, 5*time.Second)
	})
}
