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

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/kubeswarm/kubeswarm/pkg/costs"
)

// CostConfigMapWatcher is a controller-runtime Runnable that polls a named
// ConfigMap and reloads the ConfigMapCostProvider whenever its data changes.
// Polling interval is 60 seconds — pricing changes are not latency-sensitive.
type CostConfigMapWatcher struct {
	client    client.Client
	namespace string
	name      string
	provider  *costs.ConfigMapCostProvider
}

// NewCostConfigMapWatcher creates a watcher that keeps provider in sync with
// the ConfigMap identified by namespace/name.
func NewCostConfigMapWatcher(
	c client.Client, namespace, name string, provider *costs.ConfigMapCostProvider,
) *CostConfigMapWatcher {
	return &CostConfigMapWatcher{
		client:    c,
		namespace: namespace,
		name:      name,
		provider:  provider,
	}
}

// Start polls the ConfigMap every 60 seconds until ctx is cancelled.
func (w *CostConfigMapWatcher) Start(ctx context.Context) error {
	logger := log.FromContext(ctx).WithName("cost-configmap-watcher").WithValues(
		"namespace", w.namespace, "configmap", w.name)

	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			cm := &corev1.ConfigMap{}
			if err := w.client.Get(ctx, types.NamespacedName{
				Namespace: w.namespace,
				Name:      w.name,
			}, cm); err != nil {
				logger.Error(err, "failed to read cost ConfigMap — using previous pricing")
				continue
			}
			if err := w.provider.Reload(cm.Data); err != nil {
				logger.Error(err, "failed to reload cost ConfigMap — using previous pricing")
				continue
			}
			logger.V(1).Info("cost ConfigMap reloaded", "models", len(cm.Data))
		}
	}
}
