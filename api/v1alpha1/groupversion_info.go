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

// Package v1alpha1 contains API Schema definitions for the kubeswarm v1alpha1 API group.
// +kubebuilder:object:generate=true
// +groupName=kubeswarm.io
package v1alpha1

import (
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/scheme"
)

// Condition types used across Swarm resources.
const (
	ConditionReady            = "Ready"
	ConditionBudgetExceeded   = "BudgetExceeded"
	ConditionBudgetWarning    = "BudgetWarning"
	ConditionBudgetOK         = "OK"
	ConditionRegistryNotFound = "RegistryNotFound"
	ConditionMCPDegraded      = "MCPDegraded"
)

// Label and annotation keys used across controllers.
const (
	// AnnotationTeamQueueURL is the per-agent team queue URL.
	AnnotationTeamQueueURL = "kubeswarm/team-queue-url"

	// LabelTeam identifies the SwarmTeam that owns a resource.
	LabelTeam = "kubeswarm/team"

	// LabelTrigger identifies the SwarmEvent that created a run.
	LabelTrigger = "kubeswarm/trigger"

	// LabelTriggerTemplate identifies the template team used by a trigger.
	LabelTriggerTemplate = "kubeswarm/trigger-template"

	// AnnotationStreamKey is the queue stream key for a trigger-created run.
	AnnotationStreamKey = "kubeswarm/stream-key"

	// AnnotationTeamRoutes is the JSON delegate-route map set by the team controller.
	AnnotationTeamRoutes = "kubeswarm/team-routes"

	// AnnotationTeamRole is the role name assigned by the team controller.
	AnnotationTeamRole = "kubeswarm/team-role"

	// AnnotationTeamArtifactStore is the artifact store URL set by the team controller.
	AnnotationTeamArtifactStore = "kubeswarm/team-artifact-store-url"

	// AnnotationTeamArtifactClaim is the PVC claim name for team artifacts.
	AnnotationTeamArtifactClaim = "kubeswarm/team-artifact-claim"

	// AnnotationTeamArtifactCredentials is the Secret name containing cloud credentials
	// for the artifact store (e.g. AWS_ACCESS_KEY_ID / AWS_SECRET_ACCESS_KEY for S3).
	AnnotationTeamArtifactCredentials = "kubeswarm/team-artifact-credentials" //nolint:gosec // annotation key, not a credential

	// AnnotationSystemPromptHash triggers rolling restarts when the prompt changes.
	AnnotationSystemPromptHash = "kubeswarm/system-prompt-hash"

	// AnnotationAPIKeyVersion triggers rolling restarts on key rotation.
	AnnotationAPIKeyVersion = "kubeswarm/api-key-version" //nolint:gosec // annotation key, not a credential

	// AnnotationManaged marks auto-created resources as operator-managed.
	AnnotationManaged = "kubeswarm/managed"

	// LabelRole identifies the role within a SwarmTeam.
	LabelRole = "kubeswarm/role"
)

// PromptWarnBytes is the per-role inline prompt size (bytes) at which a warning is issued.
// Shared between the admission webhook and the team controller.
const PromptWarnBytes = 50 * 1024 // 50 KB

var (
	// GroupVersion is group version used to register these objects.
	GroupVersion = schema.GroupVersion{Group: "kubeswarm.io", Version: "v1alpha1"}

	// SchemeBuilder is used to add go types to the GroupVersionKind scheme.
	SchemeBuilder = &scheme.Builder{GroupVersion: GroupVersion}

	// AddToScheme adds the types in this group-version to the given scheme.
	AddToScheme = SchemeBuilder.AddToScheme
)
