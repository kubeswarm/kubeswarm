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

// Package validation provides helpers for step output validation, including
// semantic (LLM-based) validation for the SwarmRun controller and swarm run CLI.
package validation

import (
	"context"

	"github.com/kubeswarm/kubeswarm/pkg/agent/config"
	"github.com/kubeswarm/kubeswarm/pkg/agent/providers"
	"github.com/kubeswarm/kubeswarm/pkg/agent/queue"
)

// BuildSemanticValidateFn returns a SemanticValidateFn that makes a single-turn
// LLM call for step output validation. The returned function is safe to call
// concurrently. Provider credentials are read from the environment at call time
// (ANTHROPIC_API_KEY, OPENAI_API_KEY, OPENAI_BASE_URL, etc.).
func BuildSemanticValidateFn() func(ctx context.Context, model, prompt string) (string, error) {
	return func(ctx context.Context, model, prompt string) (string, error) {
		providerName := providers.Detect(model)
		p, err := providers.New(providerName)
		if err != nil {
			return "", err
		}
		cfg := &config.Config{
			Model:            model,
			MaxTokensPerCall: 500,
			TimeoutSeconds:   30,
		}
		task := queue.Task{
			ID:     "semantic-validation",
			Prompt: prompt,
		}
		result, _, err := p.RunTask(ctx, cfg, task, nil, nil, nil)
		return result, err
	}
}
