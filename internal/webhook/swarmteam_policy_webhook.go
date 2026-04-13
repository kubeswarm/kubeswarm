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
	"net/http"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	kubeswarmv1alpha1 "github.com/kubeswarm/kubeswarm/api/v1alpha1"
)

// checkStepValidationLevel returns true if the step has the required output
// validation level configured. Each level is independent - schema does not
// require pattern.
func checkStepValidationLevel(step kubeswarmv1alpha1.SwarmTeamPipelineStep, minLevel kubeswarmv1alpha1.PolicyOutputLevel) bool {
	switch minLevel {
	case kubeswarmv1alpha1.PolicyOutputLevelNone, "":
		return true
	case kubeswarmv1alpha1.PolicyOutputLevelPattern:
		return step.Validate != nil && len(step.Validate.RejectPatterns) > 0
	case kubeswarmv1alpha1.PolicyOutputLevelSchema:
		return step.Validate != nil && step.Validate.Schema != ""
	case kubeswarmv1alpha1.PolicyOutputLevelSemantic:
		return step.Validate != nil && step.Validate.Semantic != ""
	default:
		return true
	}
}

// SwarmTeamPolicyValidator validates SwarmTeam create/update requests against
// the merged effective SwarmPolicy in the namespace. It supports three enforcement
// modes: Audit (log only), Warn (admission warnings), and Enforce (reject).
//
// The webhook is namespace-scoped via a namespaceSelector targeting
// kubeswarm.io/policy-governed=true, so ungoverned namespaces have zero latency
// overhead.
//
// +kubebuilder:webhook:path=/validate-kubeswarm-v1alpha1-swarmteam-policy,mutating=false,failurePolicy=fail,sideEffects=None,groups=kubeswarm.io,resources=swarmteams,verbs=create;update,versions=v1alpha1,name=vpolicy-swarmteam.kb.io,admissionReviewVersions=v1
type SwarmTeamPolicyValidator struct {
	decoder admission.Decoder
	client  client.Client
}

// NewSwarmTeamPolicyValidator creates a new SwarmTeamPolicyValidator.
func NewSwarmTeamPolicyValidator(decoder admission.Decoder, c client.Client) *SwarmTeamPolicyValidator {
	return &SwarmTeamPolicyValidator{decoder: decoder, client: c}
}

// Handle implements admission.Handler.
func (v *SwarmTeamPolicyValidator) Handle(ctx context.Context, req admission.Request) admission.Response {
	logger := log.FromContext(ctx).WithValues("webhook", "swarmteam-policy", "team", req.Name, "namespace", req.Namespace)

	team := &kubeswarmv1alpha1.SwarmTeam{}
	if err := v.decoder.DecodeRaw(req.Object, team); err != nil {
		return admission.Errored(http.StatusBadRequest, err)
	}

	effective, resp := fetchEffectivePolicyForWebhook(ctx, v.client, req.Namespace, logger)
	if effective == nil {
		return resp
	}

	// If no minimum validation required, allow.
	if effective.MinValidation == kubeswarmv1alpha1.PolicyOutputLevelNone || effective.MinValidation == "" {
		return admission.Allowed("no minimum validation required")
	}

	// If no pipeline steps, allow.
	if len(team.Spec.Pipeline) == 0 {
		return admission.Allowed("no pipeline steps to validate")
	}

	// Check each pipeline step against the minimum validation level.
	var msgs []string
	for _, step := range team.Spec.Pipeline {
		if !checkStepValidationLevel(step, effective.MinValidation) {
			msgs = append(msgs, fmt.Sprintf("SwarmPolicy violation: step %q requires %s validation", step.Role, effective.MinValidation))
		}
	}

	if len(msgs) == 0 {
		return admission.Allowed("team compliant with all policies")
	}

	return applyEnforcementMode(ctx, effective.EnforcementMode, msgs, logger, "team")
}
