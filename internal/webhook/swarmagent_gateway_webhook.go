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

package webhook

import (
	"context"
	"fmt"
	"regexp"

	"sigs.k8s.io/controller-runtime/pkg/client"

	kubeswarmv1alpha1 "github.com/kubeswarm/kubeswarm/api/v1alpha1"
)

var (
	// tagPattern matches valid tag values: lowercase letter followed by
	// lowercase alphanumeric or hyphens, and must end with alphanumeric
	// (no trailing hyphen, matching k8s-style label conventions).
	tagPattern = regexp.MustCompile(`^[a-z]([a-z0-9-]*[a-z0-9])?$`)
	// targetPattern matches valid target agent names: lowercase alphanumeric, optional hyphens in the middle.
	targetPattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)

	// maxTargetNameLen is the DNS-1123 label length limit, which SwarmAgent
	// names must respect. The CRD MaxItems cap covers list length, not item
	// length, so enforce here in the validator.
	maxTargetNameLen = 63

	// reservedToolNames are tool names injected by the gateway and must not be user-defined.
	// Reserved globally (regardless of whether the agent is a gateway) so a non-gateway
	// agent shipping a tool named "dispatch" does not later break admission the moment
	// a gateway block is added.
	reservedToolNames = map[string]bool{
		"registry_search": true,
		"dispatch":        true,
	}
)

// ValidateGatewayConfig validates the gateway configuration on a SwarmAgent.
// Returns a slice of blocking errors and a slice of non-blocking warnings.
// Reserved tool names are enforced on every agent, not only gateways.
func ValidateGatewayConfig(_ context.Context, _ client.Client, agent *kubeswarmv1alpha1.SwarmAgent) ([]error, []string) {
	var errs []error
	var warnings []string

	// Reserved tool names are enforced globally so a tool named "dispatch" on a
	// non-gateway agent cannot silently slip through and break admission later
	// when a gateway block is added.
	if agent.Spec.Tools != nil {
		for _, mcp := range agent.Spec.Tools.MCP {
			if reservedToolNames[mcp.Name] {
				errs = append(errs, fmt.Errorf(
					"spec.tools.mcp tool name %q is reserved by the gateway and cannot be used", mcp.Name))
			}
		}
		for _, wh := range agent.Spec.Tools.Webhooks {
			if reservedToolNames[wh.Name] {
				errs = append(errs, fmt.Errorf(
					"spec.tools.webhooks tool name %q is reserved by the gateway and cannot be used", wh.Name))
			}
		}
	}

	gw := agent.Spec.Gateway
	if gw == nil {
		return errs, warnings
	}

	// Reject gateway on team-managed agents.
	// TODO: add a symmetric check in SwarmTeam admission to reject adding a gateway
	// agent as an inline role, covering the case where the annotation is set after creation.
	if _, hasTeamRole := agent.Annotations["kubeswarm.io/team-role"]; hasTeamRole {
		errs = append(errs, fmt.Errorf(
			"spec.gateway is not allowed on agents with the kubeswarm.io/team-role annotation; "+
				"gateway agents must be standalone"))
	}

	// Validate filterByTags entries.
	for _, tag := range gw.FilterByTags {
		if !tagPattern.MatchString(tag) {
			errs = append(errs, fmt.Errorf(
				"spec.gateway.filterByTags entry %q is invalid; must match %s", tag, tagPattern.String()))
		}
	}

	// Validate allowedTargets entries (pattern + DNS-1123 length cap).
	for _, target := range gw.AllowedTargets {
		if len(target) > maxTargetNameLen {
			errs = append(errs, fmt.Errorf(
				"spec.gateway.allowedTargets entry %q exceeds %d characters", target, maxTargetNameLen))
			continue
		}
		if !targetPattern.MatchString(target) {
			errs = append(errs, fmt.Errorf(
				"spec.gateway.allowedTargets entry %q is invalid; must match %s", target, targetPattern.String()))
		}
	}

	// Warn if allowGatewayTargets is true but allowedTargets is empty.
	if gw.AllowGatewayTargets && len(gw.AllowedTargets) == 0 {
		warnings = append(warnings,
			"spec.gateway.allowGatewayTargets is true but allowedTargets is empty; "+
				"any gateway agent in the registry may be dispatched to, which risks dispatch loops")
	}

	return errs, warnings
}
