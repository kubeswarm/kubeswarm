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

// Package registry provides a generic, concurrency-safe factory registry used
// by provider, queue, artifact store, cost, and memory backends.
package registry

import (
	"sort"
	"sync"
)

// Registry is a concurrency-safe map from string keys to factory values of type F.
// F is typically a function type (e.g. func() Provider or func(url string) (Store, error)).
type Registry[F any] struct {
	mu       sync.RWMutex
	backends map[string]F
}

// Register stores factory f under the given key, replacing any previous entry.
func (r *Registry[F]) Register(key string, f F) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.backends == nil {
		r.backends = make(map[string]F)
	}
	r.backends[key] = f
}

// Lookup returns the factory registered under key and whether it was found.
func (r *Registry[F]) Lookup(key string) (F, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	f, ok := r.backends[key]
	return f, ok
}

// Keys returns a sorted list of all registered keys.
func (r *Registry[F]) Keys() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	keys := make([]string, 0, len(r.backends))
	for k := range r.backends {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
