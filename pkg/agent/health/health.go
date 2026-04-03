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

package health

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/kubeswarm/kubeswarm/pkg/agent/queue"
	"github.com/kubeswarm/kubeswarm/pkg/agent/runner"
)

// ServeProbe starts HTTP health endpoints on addr.
//
//   - GET /healthz — basic liveness: always 200 if the process is running.
//   - GET /readyz  — readiness check. Behaviour depends on whether a
//     validatorPrompt is set (controlled by SwarmAgent.spec.livenessProbe):
//   - No prompt (type: ping): returns 200 immediately — process-level readiness only.
//   - Prompt set (type: semantic): calls the LLM API and checks that the
//     response contains "HEALTHY" before returning 200.
func ServeProbe(addr string, r *runner.Runner, validatorPrompt string) {
	mux := http.NewServeMux()

	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	mux.HandleFunc("/readyz", func(w http.ResponseWriter, req *http.Request) {
		// When no validator prompt is configured (probe type: ping), skip the LLM
		// call and return ready immediately — the process is up and that's enough.
		if validatorPrompt == "" {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("ready"))
			return
		}

		ctx, cancel := context.WithTimeout(req.Context(), 15*time.Second)
		defer cancel()

		result, _, err := r.RunTask(ctx, queue.Task{
			ID:     "health-probe",
			Prompt: validatorPrompt,
		})
		if err != nil || !strings.Contains(result, "HEALTHY") {
			w.WriteHeader(http.StatusServiceUnavailable)
			if err != nil {
				_, _ = w.Write([]byte("semantic check failed: " + err.Error()))
			} else {
				_, _ = w.Write([]byte("unexpected response: " + result))
			}
			return
		}

		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ready"))
	})

	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       20 * time.Second,
	}
	_ = srv.ListenAndServe()
}
