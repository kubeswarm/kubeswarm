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
	"strings"
	"sync"

	"github.com/go-logr/logr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	kubeswarmv1alpha1 "github.com/kubeswarm/kubeswarm/api/v1alpha1"
	"github.com/kubeswarm/kubeswarm/internal/controller"
	"github.com/kubeswarm/kubeswarm/pkg/observability"
)

// SwarmAgentPolicyValidator validates SwarmAgent create/update requests against
// the merged effective SwarmPolicy in the namespace. It supports three enforcement
// modes: Audit (log only), Warn (admission warnings), and Enforce (reject).
//
// The webhook is namespace-scoped via a namespaceSelector targeting
// kubeswarm.io/policy-governed=true, so ungoverned namespaces have zero latency
// overhead.
//
// +kubebuilder:webhook:path=/validate-kubeswarm-v1alpha1-swarmagent-policy,mutating=false,failurePolicy=fail,sideEffects=None,groups=kubeswarm.io,resources=swarmagents,verbs=create;update,versions=v1alpha1,name=vpolicy-swarmagent.kb.io,admissionReviewVersions=v1
type SwarmAgentPolicyValidator struct {
	decoder admission.Decoder
	client  client.Client
}

// NewSwarmAgentPolicyValidator creates a new SwarmAgentPolicyValidator.
func NewSwarmAgentPolicyValidator(decoder admission.Decoder, c client.Client) *SwarmAgentPolicyValidator {
	return &SwarmAgentPolicyValidator{decoder: decoder, client: c}
}

// Handle implements admission.Handler.
func (v *SwarmAgentPolicyValidator) Handle(ctx context.Context, req admission.Request) admission.Response {
	logger := log.FromContext(ctx).WithValues("webhook", "swarmagent-policy", "agent", req.Name, "namespace", req.Namespace)

	agent := &kubeswarmv1alpha1.SwarmAgent{}
	if err := v.decoder.DecodeRaw(req.Object, agent); err != nil {
		return admission.Errored(http.StatusBadRequest, err)
	}

	effective, resp := fetchEffectivePolicyForWebhook(ctx, v.client, req.Namespace, logger)
	if effective == nil {
		return resp
	}

	// Evaluate agent compliance.
	violations := controller.EvaluateAgentCompliance(agent, effective)
	if len(violations) == 0 {
		return admission.Allowed("agent compliant with all policies")
	}

	// Format violation messages.
	msgs := make([]string, 0, len(violations))
	for _, v := range violations {
		msgs = append(msgs, fmt.Sprintf("SwarmPolicy violation: %s", v.Message))
	}

	return applyEnforcementMode(ctx, effective.EnforcementMode, msgs, logger, "agent")
}

// fetchEffectivePolicyForWebhook lists and merges SwarmPolicies in the namespace.
// Returns the effective policy, or nil with an early admission.Response if no policy applies.
func fetchEffectivePolicyForWebhook(ctx context.Context, c client.Client, namespace string, logger logr.Logger) (*kubeswarmv1alpha1.EffectivePolicySpec, admission.Response) {
	var policyList kubeswarmv1alpha1.SwarmPolicyList
	if err := c.List(ctx, &policyList, client.InNamespace(namespace)); err != nil {
		logger.Error(err, "failed to list policies")
		return nil, admission.Allowed("unable to list policies, deferring to controller")
	}
	if len(policyList.Items) == 0 {
		return nil, admission.Allowed("no policies in namespace")
	}
	effective, _ := controller.MergePolicies(policyList.Items)
	if effective == nil {
		return nil, admission.Allowed("no effective policy")
	}
	return effective, admission.Response{}
}

// applyEnforcementMode maps a policy enforcement mode to an admission response.
// Shared between the SwarmAgent and SwarmTeam policy webhooks.
func applyEnforcementMode(ctx context.Context, mode kubeswarmv1alpha1.PolicyEnforcementMode, msgs []string, logger logr.Logger, subject string) admission.Response {
	om := policyWebhookMetrics()

	switch mode {
	case kubeswarmv1alpha1.PolicyEnforcementEnforce:
		logger.Info("rejecting non-compliant "+subject, "violations", len(msgs))
		if om != nil {
			om.RecordPolicyAdmissionRejected(ctx)
		}
		return admission.Denied(strings.Join(msgs, "; "))

	case kubeswarmv1alpha1.PolicyEnforcementWarn:
		logger.Info("warning for non-compliant "+subject, "violations", len(msgs))
		if om != nil {
			om.RecordPolicyAdmissionWarned(ctx)
		}
		return admission.Allowed("").WithWarnings(msgs...)

	default: // Audit
		logger.Info("audit: non-compliant "+subject+" allowed", "violations", len(msgs))
		if om != nil {
			om.RecordPolicyAdmissionWouldReject(ctx)
		}
		return admission.Allowed("audit mode: violations logged")
	}
}

// policyWebhookMetrics returns the shared operator metrics singleton.
func policyWebhookMetrics() *observability.OperatorMetrics {
	webhookMetricsOnce.Do(func() {
		webhookMetricsInstance, _ = observability.NewOperatorMetrics()
	})
	return webhookMetricsInstance
}

var (
	webhookMetricsOnce     sync.Once
	webhookMetricsInstance *observability.OperatorMetrics
)
