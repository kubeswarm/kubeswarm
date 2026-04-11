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

// Package main is the operator binary entrypoint. It registers all built-in
// plugin implementations (Redis queue, Redis spend store, Anthropic/OpenAI LLM
// providers) and then delegates to the operator's Run function in the controller
// module. Keeping this file in runtime ensures the controller module has zero
// third-party SDK dependencies.
package main

import (
	// Register built-in queue backend and spend store (Redis).
	_ "github.com/kubeswarm/kubeswarm/runtime/costs/redisstore"
	_ "github.com/kubeswarm/kubeswarm/runtime/queue/redis"

	// Register LLM providers so the operator can perform semantic validation,
	// output routing, and step compression via the providers.New() registry.
	_ "github.com/kubeswarm/kubeswarm/runtime/providers/anthropic"
	_ "github.com/kubeswarm/kubeswarm/runtime/providers/gemini"
	_ "github.com/kubeswarm/kubeswarm/runtime/providers/openai"

	// Operator Run() lives in the controller module so controller/webhook/CRD
	// code stays in one place. This file only wires in the plugin implementations.
	"github.com/kubeswarm/kubeswarm/cmd"
)

func main() {
	cmd.Run()
}
