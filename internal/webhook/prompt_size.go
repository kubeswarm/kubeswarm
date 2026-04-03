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

// Package webhook provides admission webhooks for kubeswarm CRDs.
package webhook

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	authv1 "k8s.io/api/authorization/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	kubeswarmv1alpha1 "github.com/kubeswarm/kubeswarm/api/v1alpha1"
)

const (
	// promptWarnBytes is the per-role inline prompt size at which a warning is issued.
	// Prompts this size can still be applied but the operator will surface a Warning
	// condition. Users should migrate to systemPromptRef.
	promptWarnBytes = 50 * 1024 // 50 KB

	// promptDenyBytes is the per-role inline prompt size at which the request is denied.
	// A single prompt this large consumes > 1/3 of etcd's default 1.5 MB object budget.
	promptDenyBytes = 512 * 1024 // 512 KB

	// teamTotalDenyBytes is the combined inline prompt limit across all roles in one
	// SwarmTeam. Exceeding this approaches the etcd object size limit even before
	// status, managed fields, and other spec fields are counted.
	teamTotalDenyBytes = 800 * 1024 // 800 KB
)

// SwarmTeamPromptValidator validates inline systemPrompt sizes on SwarmTeam resources.
// It warns when a single role prompt exceeds 50 KB and denies when it exceeds 512 KB
// or when the team total exceeds 800 KB.
//
// +kubebuilder:webhook:path=/validate-kubeswarm-v1alpha1-swarmteam,mutating=false,failurePolicy=ignore,sideEffects=None,groups=kubeswarm,resources=swarmteams,verbs=create;update,versions=v1alpha1,name=vswarmteam.kb.io,admissionReviewVersions=v1
type SwarmTeamPromptValidator struct {
	decoder admission.Decoder
}

// NewSwarmTeamPromptValidator creates a new SwarmTeamPromptValidator.
func NewSwarmTeamPromptValidator(decoder admission.Decoder) *SwarmTeamPromptValidator {
	return &SwarmTeamPromptValidator{decoder: decoder}
}

// Handle implements admission.Handler.
func (v *SwarmTeamPromptValidator) Handle(_ context.Context, req admission.Request) admission.Response {
	team := &kubeswarmv1alpha1.SwarmTeam{}
	if err := v.decoder.DecodeRaw(req.Object, team); err != nil {
		return admission.Errored(http.StatusBadRequest, err)
	}

	var warnings []string
	var totalBytes int

	for _, role := range team.Spec.Roles {
		size := len(role.SystemPrompt)
		if size == 0 {
			continue
		}
		totalBytes += size

		if size > promptDenyBytes {
			return admission.Denied(fmt.Sprintf(
				"role %q has an inline systemPrompt of %s, which exceeds the %s per-role limit. "+
					"Move the prompt to a ConfigMap and reference it via spec.roles[%s].systemPromptRef.configMapKeyRef.",
				role.Name, humanBytes(size), humanBytes(promptDenyBytes), role.Name,
			))
		}
		if size > promptWarnBytes {
			warnings = append(warnings, fmt.Sprintf(
				"role %q has an inline systemPrompt of %s (> %s). "+
					"Consider using systemPromptRef to avoid etcd object size limits.",
				role.Name, humanBytes(size), humanBytes(promptWarnBytes),
			))
		}
	}

	if totalBytes > teamTotalDenyBytes {
		return admission.Denied(fmt.Sprintf(
			"total inline systemPrompt size across all roles is %s, which exceeds the %s team limit. "+
				"Move prompts to ConfigMaps and reference them via spec.roles[*].systemPromptRef.configMapKeyRef.",
			humanBytes(totalBytes), humanBytes(teamTotalDenyBytes),
		))
	}

	if len(warnings) > 0 {
		return admission.Allowed("").WithWarnings(warnings...)
	}
	return admission.Allowed("")
}

// SwarmAgentPromptValidator validates inline systemPrompt sizes on SwarmAgent resources
// and enforces system prompt immutability (RFC-0016 Phase 4).
//
// +kubebuilder:webhook:path=/validate-kubeswarm-v1alpha1-swarmagent,mutating=false,failurePolicy=ignore,sideEffects=None,groups=kubeswarm,resources=swarmagents,verbs=create;update,versions=v1alpha1,name=vswarmagent.kb.io,admissionReviewVersions=v1
type SwarmAgentPromptValidator struct {
	decoder admission.Decoder
	client  client.Client
}

// NewSwarmAgentPromptValidator creates a new SwarmAgentPromptValidator.
// c is used to perform SubjectAccessReview checks for system prompt update guard.
func NewSwarmAgentPromptValidator(decoder admission.Decoder, c client.Client) *SwarmAgentPromptValidator {
	return &SwarmAgentPromptValidator{decoder: decoder, client: c}
}

// Handle implements admission.Handler.
func (v *SwarmAgentPromptValidator) Handle(ctx context.Context, req admission.Request) admission.Response {
	agent := &kubeswarmv1alpha1.SwarmAgent{}
	if err := v.decoder.DecodeRaw(req.Object, agent); err != nil {
		return admission.Errored(http.StatusBadRequest, err)
	}

	var warnings []string

	var inlinePrompt string
	if agent.Spec.Prompt != nil {
		inlinePrompt = agent.Spec.Prompt.Inline
	}
	size := len(inlinePrompt)
	if size > promptDenyBytes {
		return admission.Denied(fmt.Sprintf(
			"inline prompt is %s, which exceeds the %s limit. "+
				"Move the prompt to a ConfigMap and reference it via spec.prompt.from.configMapKeyRef.",
			humanBytes(size), humanBytes(promptDenyBytes),
		))
	}
	if size > promptWarnBytes {
		warnings = append(warnings, fmt.Sprintf(
			"inline prompt is %s (> %s). "+
				"Consider using spec.prompt.from to avoid etcd object size limits.",
			humanBytes(size), humanBytes(promptWarnBytes),
		))
	}

	// Warn when spec.runtime.resources is not set. The operator injects safe defaults
	// (cpu: 100m/500m, memory: 128Mi/512Mi, ephemeral-storage: 256Mi) but
	// explicit limits are recommended to match your workload's actual footprint.
	if agent.Spec.Runtime == nil || agent.Spec.Runtime.Resources == nil {
		warnings = append(warnings,
			"spec.runtime.resources is not set; the operator will inject default limits "+
				"(requests: cpu=100m mem=128Mi; limits: cpu=500m mem=512Mi ephemeral-storage=256Mi). "+
				"Set spec.runtime.resources explicitly to tune for your workload.")
	}

	// System prompt immutability guard (RFC-0016 Phase 4 / Area 4c).
	// On updates, reject any change to spec.systemPrompt unless the caller holds
	// the swarm-prompt-admin ClusterRole (kubeswarm/swarmagents/systemprompt:update).
	if req.OldObject.Raw != nil {
		old := &kubeswarmv1alpha1.SwarmAgent{}
		if err := v.decoder.DecodeRaw(req.OldObject, old); err != nil {
			return admission.Errored(http.StatusBadRequest, err)
		}
		var newInline, oldInline string
		if agent.Spec.Prompt != nil {
			newInline = agent.Spec.Prompt.Inline
		}
		if old.Spec.Prompt != nil {
			oldInline = old.Spec.Prompt.Inline
		}
		if newInline != oldInline {
			allowed, err := v.callerCanUpdateSystemPrompt(ctx, req)
			if err != nil {
				// SAR failure is non-fatal — warn rather than block (failurePolicy=ignore
				// on the webhook ensures availability; this is defence-in-depth).
				warnings = append(warnings, fmt.Sprintf(
					"could not verify swarm-prompt-admin permission: %v — "+
						"set spec.systemPrompt carefully; unauthorised changes will be blocked "+
						"once the swarm-prompt-admin ClusterRole is deployed.", err))
			} else if !allowed {
				return admission.Denied(
					"changing spec.systemPrompt requires the swarm-prompt-admin ClusterRole " +
						"(kubeswarm/swarmagents/systemprompt update permission). " +
						"Contact your cluster administrator to obtain elevated access.")
			}
		}
	}

	// MCP security policy enforcement (RFC-0016 Phase 5).
	// Load all SwarmSettings in the namespace and apply the strictest policy found.
	if denied, reason := v.checkMCPPolicy(ctx, req.Namespace, agent); denied {
		return admission.Denied(reason)
	}

	if len(warnings) > 0 {
		return admission.Allowed("").WithWarnings(warnings...)
	}
	return admission.Allowed("")
}

// checkMCPPolicy loads SwarmSettings in the namespace and enforces mcpAllowlist and
// requireMCPAuth against the agent's MCP server list. Returns (true, reason) when denied.
func (v *SwarmAgentPromptValidator) checkMCPPolicy(
	ctx context.Context,
	namespace string,
	agent *kubeswarmv1alpha1.SwarmAgent,
) (bool, string) {
	if agent.Spec.Tools == nil || len(agent.Spec.Tools.MCP) == 0 {
		return false, ""
	}

	settingsList := &kubeswarmv1alpha1.SwarmSettingsList{}
	if err := v.client.List(ctx, settingsList, client.InNamespace(namespace)); err != nil {
		// Non-fatal — failurePolicy=ignore means the webhook never blocks on client errors.
		return false, ""
	}

	// Aggregate the strictest policy across all settings in the namespace.
	var allowlist []string
	requireAuth := false
	for i := range settingsList.Items {
		sec := settingsList.Items[i].Spec.Security
		if sec == nil {
			continue
		}
		allowlist = append(allowlist, sec.MCPAllowlist...)
		if sec.RequireMCPAuth {
			requireAuth = true
		}
	}

	for _, srv := range agent.Spec.Tools.MCP {
		// Allowlist check: if any allowlist entries exist the URL must match one.
		if len(allowlist) > 0 && srv.URL != "" {
			matched := false
			for _, prefix := range allowlist {
				if strings.HasPrefix(srv.URL, prefix) {
					matched = true
					break
				}
			}
			if !matched {
				return true, fmt.Sprintf(
					"MCP server %q URL %q is not in the namespace mcpAllowlist. "+
						"Add the URL prefix to an SwarmSettings.spec.security.mcpAllowlist in namespace %q.",
					srv.Name, srv.URL, namespace)
			}
		}

		// requireMCPAuth check: every MCP server must have bearer or mtls auth configured.
		if requireAuth {
			if srv.Auth == nil || (srv.Auth.Bearer == nil && srv.Auth.MTLS == nil) {
				return true, fmt.Sprintf(
					"MCP server %q has no auth configured but namespace policy requires authentication "+
						"(SwarmSettings.spec.security.requireMCPAuth=true). "+
						"Set spec.tools.mcp[%s].auth.bearer or auth.mtls.",
					srv.Name, srv.Name)
			}
		}
	}

	return false, ""
}

// callerCanUpdateSystemPrompt performs a SubjectAccessReview to check whether the
// requesting user may update the systemprompt subresource on SwarmAgents.
// This maps to the swarm-prompt-admin ClusterRole defined in RFC-0016 Phase 7.
func (v *SwarmAgentPromptValidator) callerCanUpdateSystemPrompt(ctx context.Context, req admission.Request) (bool, error) {
	extra := make(map[string]authv1.ExtraValue, len(req.UserInfo.Extra))
	for k, vals := range req.UserInfo.Extra {
		extra[k] = authv1.ExtraValue(vals)
	}
	sar := &authv1.SubjectAccessReview{
		Spec: authv1.SubjectAccessReviewSpec{
			User:   req.UserInfo.Username,
			Groups: req.UserInfo.Groups,
			Extra:  extra,
			ResourceAttributes: &authv1.ResourceAttributes{
				Group:       "kubeswarm",
				Resource:    "swarmagents",
				Subresource: "systemprompt",
				Verb:        "update",
				Namespace:   req.Namespace,
			},
		},
	}
	if err := v.client.Create(ctx, sar); err != nil {
		return false, err
	}
	return sar.Status.Allowed, nil
}

// humanBytes formats a byte count as a human-readable string (KB or MB).
func humanBytes(n int) string {
	if n >= 1024*1024 {
		return fmt.Sprintf("%.1f MB", float64(n)/(1024*1024))
	}
	return fmt.Sprintf("%d KB", n/1024)
}
