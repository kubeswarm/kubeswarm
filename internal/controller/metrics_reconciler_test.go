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
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

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

var _ = Describe("reconcileResult", func() {
	It("returns 'error' when err is set", func() {
		Expect(reconcileResult(ctrl.Result{}, errors.New("boom"))).To(Equal("error"))
	})

	It("returns 'requeue' when RequeueAfter > 0 and no error", func() {
		Expect(reconcileResult(ctrl.Result{RequeueAfter: time.Second}, nil)).To(Equal("requeue"))
	})

	It("returns 'ok' for an empty result with no error", func() {
		Expect(reconcileResult(ctrl.Result{}, nil)).To(Equal("ok"))
	})

	It("returns 'error' even when RequeueAfter is set alongside an error", func() {
		Expect(reconcileResult(ctrl.Result{RequeueAfter: time.Second}, errors.New("x"))).To(Equal("error"))
	})
})

var _ = Describe("WithMetrics", func() {
	It("wraps the reconciler and forwards successful calls", func() {
		inner := &fakeReconciler{result: ctrl.Result{}}
		wrapped := WithMetrics(inner, "test-controller")
		result, err := wrapped.Reconcile(context.Background(), reconcile.Request{})
		Expect(err).NotTo(HaveOccurred())
		Expect(result).To(Equal(ctrl.Result{}))
		Expect(inner.called).To(BeTrue())
	})

	It("propagates errors from the inner reconciler", func() {
		inner := &fakeReconciler{err: errors.New("inner error")}
		wrapped := WithMetrics(inner, "test-controller")
		_, err := wrapped.Reconcile(context.Background(), reconcile.Request{})
		Expect(err).To(MatchError("inner error"))
	})

	It("forwards a requeue result from the inner reconciler", func() {
		inner := &fakeReconciler{result: ctrl.Result{RequeueAfter: 5 * time.Second}}
		wrapped := WithMetrics(inner, "test-controller")
		result, err := wrapped.Reconcile(context.Background(), reconcile.Request{})
		Expect(err).NotTo(HaveOccurred())
		Expect(result.RequeueAfter).To(Equal(5 * time.Second))
	})
})
