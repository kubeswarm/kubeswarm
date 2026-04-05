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

package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// SwarmTeamPhase describes the overall state of an SwarmTeam.
// +kubebuilder:validation:Enum=Pending;Ready;Running;Succeeded;Failed
type SwarmTeamPhase string

const (
	SwarmTeamPhasePending   SwarmTeamPhase = "Pending"
	SwarmTeamPhaseReady     SwarmTeamPhase = "Ready"     // infra up, dynamic mode idle
	SwarmTeamPhaseRunning   SwarmTeamPhase = "Running"   // pipeline executing
	SwarmTeamPhaseSucceeded SwarmTeamPhase = "Succeeded" // pipeline completed
	SwarmTeamPhaseFailed    SwarmTeamPhase = "Failed"
)

// SwarmTeamRole defines one role in the team.
// +kubebuilder:validation:XValidation:rule="!(has(self.swarmAgent) && has(self.swarmTeam)) && !(has(self.swarmAgent) && has(self.model)) && !(has(self.swarmTeam) && has(self.model))",message="at most one of swarmAgent or swarmTeam or model (inline) may be set"
type SwarmTeamRole struct {
	// Name is the unique role identifier (e.g. "researcher", "coordinator").
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// SwarmAgent is the name of an existing SwarmAgent in the same namespace.
	// Mutually exclusive with Model+SystemPrompt (inline definition).
	// +optional
	SwarmAgent string `json:"swarmAgent,omitempty"`

	// SwarmTeam is the name of another SwarmTeam in the same namespace whose entry role
	// fulfils this role. Only valid in pipeline mode (spec.pipeline must be set).
	// +optional
	SwarmTeam string `json:"swarmTeam,omitempty"`

	// Model is the LLM model ID for an inline role definition.
	// If set, the operator auto-creates an SwarmAgent named {team}-{role}.
	// +optional
	Model string `json:"model,omitempty"`

	// Prompt configures the system prompt for an inline role's auto-created SwarmAgent.
	// Matches the SwarmAgent spec.prompt structure (inline or from ConfigMap/Secret).
	// +optional
	Prompt *AgentPrompt `json:"prompt,omitempty"`

	// Tools groups MCP server connections and inline webhook tools for an inline role.
	// Matches the SwarmAgent spec.tools structure.
	// +optional
	Tools *AgentTools `json:"tools,omitempty"`

	// Runtime groups replica count, autoscaling, and resources for an inline role.
	// +optional
	Runtime *AgentRuntime `json:"runtime,omitempty"`

	// Limits constrains per-agent resource usage for an inline role definition.
	// +optional
	Limits *GuardrailLimits `json:"limits,omitempty"`

	// CanDelegate lists role names this role is permitted to call via delegate().
	// Empty means this is a leaf role - it cannot delegate further.
	// +optional
	CanDelegate []string `json:"canDelegate,omitempty"`

	// Settings references SwarmSettings objects whose fragments are composed into this
	// role's system prompt, in list order. Only applies to inline roles.
	// For roles referencing an external SwarmAgent, set settings on the SwarmAgent CR directly.
	// +optional
	// +listType=map
	// +listMapKey=name
	Settings []LocalObjectReference `json:"settings,omitempty"`

	// EnvFrom injects environment variables from Secrets or ConfigMaps into the agent pods
	// created for this role. Use this to supply API keys on a per-role basis.
	// Only applies to inline roles.
	// +optional
	EnvFrom []corev1.EnvFromSource `json:"envFrom,omitempty"`

	// Plugins configures external gRPC provider or queue overrides for this role (RFC-0025).
	// Only applies to inline roles.
	// +optional
	Plugins *AgentPlugins `json:"plugins,omitempty"`
}

// StepValidation configures output validation for a pipeline step.
// At least one of Contains, Schema, or Semantic must be set.
// When multiple modes are configured all must pass; evaluation order is
// Contains → Schema → Semantic (cheapest first).
type StepValidation struct {
	// Contains is a RE2 regular expression that must match somewhere in the step output.
	// Avoid anchoring on multi-byte characters (e.g. emoji) — use substring match instead.
	// +optional
	Contains string `json:"contains,omitempty"`

	// Schema is a JSON Schema string. The step output must be valid JSON that satisfies
	// the schema's required fields and top-level property type constraints.
	// +optional
	Schema string `json:"schema,omitempty"`

	// Semantic is a natural-language validator prompt sent to an LLM.
	// The LLM must respond with "PASS" (case-insensitive) for validation to pass.
	// Use {{ .output }} in the prompt to embed the step output.
	// +optional
	Semantic string `json:"semantic,omitempty"`

	// SemanticModel overrides the LLM model used for semantic validation.
	// Defaults to the step's SwarmAgent model when empty.
	// Recommended: use a stronger model than the step agent to avoid grading its own output.
	// +optional
	SemanticModel string `json:"semanticModel,omitempty"`

	// OnFailure controls what happens when validation fails.
	// "fail" (default) marks the step Failed immediately.
	// "retry" resets the step to Pending for re-execution.
	// +kubebuilder:default=fail
	// +kubebuilder:validation:Enum=fail;retry
	// +optional
	OnFailure string `json:"onFailure,omitempty"`

	// MaxRetries caps validation-level retries when OnFailure is "retry".
	// Independent of queue-level task retries.
	// +kubebuilder:default=2
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=10
	// +optional
	MaxRetries int `json:"maxRetries,omitempty"`

	// RejectPatterns is a list of RE2 regular expressions that act as a security gate
	// against prompt injection. A match against any pattern causes the step to fail
	// immediately with reason OutputRejected, regardless of other validation settings.
	// Evaluated before Contains, Schema, and Semantic checks.
	// Example: ["(?i)ignore.*previous.*instructions", "(?i)act as"]
	// +optional
	RejectPatterns []string `json:"rejectPatterns,omitempty"`
}

// ArtifactStoreType identifies the storage backend for file artifacts.
// +kubebuilder:validation:Enum=local;s3;gcs
type ArtifactStoreType string

const (
	// ArtifactStoreLocal stores artifacts on the local filesystem (swarm run only).
	ArtifactStoreLocal ArtifactStoreType = "local"
	// ArtifactStoreS3 stores artifacts in an Amazon S3 bucket.
	ArtifactStoreS3 ArtifactStoreType = "s3"
	// ArtifactStoreGCS stores artifacts in a Google Cloud Storage bucket.
	ArtifactStoreGCS ArtifactStoreType = "gcs"
)

// ArtifactStoreLocalSpec configures a volume-backed local artifact store.
// In Kubernetes, set ClaimName to mount a PersistentVolumeClaim into every agent pod
// in the team. For single-process local runs (swarm run CLI), ClaimName is not needed
// and Path is used as a plain directory on the local filesystem.
type ArtifactStoreLocalSpec struct {
	// Path is the mount path inside agent pods (and the directory path for swarm run).
	// Defaults to /artifacts in Kubernetes (when ClaimName is set) or
	// /tmp/swarm-artifacts for CLI runs.
	// +optional
	Path string `json:"path,omitempty"`

	// ClaimName is the name of a PersistentVolumeClaim to mount at Path inside agent pods.
	// Required for Kubernetes deployments; omit for swarm run (CLI) local testing.
	// The PVC must support ReadWriteMany if multiple agent replicas run concurrently;
	// ReadWriteOnce is sufficient for single-replica agents.
	// +optional
	ClaimName string `json:"claimName,omitempty"`
}

// ArtifactStoreS3 configures Amazon S3 artifact storage.
type ArtifactStoreS3Spec struct {
	// Bucket is the S3 bucket name.
	// +kubebuilder:validation:Required
	Bucket string `json:"bucket"`
	// Region is the AWS region (e.g. us-east-1).
	// +optional
	Region string `json:"region,omitempty"`
	// Prefix is an optional key prefix applied to all stored artifacts.
	// +optional
	Prefix string `json:"prefix,omitempty"`
	// CredentialsSecret references a k8s Secret containing AWS_ACCESS_KEY_ID
	// and AWS_SECRET_ACCESS_KEY keys. When empty, the default credential chain is used.
	// +optional
	CredentialsSecret *LocalObjectReference `json:"credentialsSecret,omitempty"`
}

// ArtifactStoreGCS configures Google Cloud Storage artifact storage.
type ArtifactStoreGCSSpec struct {
	// Bucket is the GCS bucket name.
	// +kubebuilder:validation:Required
	Bucket string `json:"bucket"`
	// Prefix is an optional object prefix applied to all stored artifacts.
	// +optional
	Prefix string `json:"prefix,omitempty"`
	// CredentialsSecret references a k8s Secret with a service account JSON key
	// under the "credentials.json" key.
	// +optional
	CredentialsSecret *LocalObjectReference `json:"credentialsSecret,omitempty"`
}

// ArtifactStoreSpec configures where pipeline file artifacts are stored.
// +kubebuilder:validation:XValidation:rule="self.type == 'local' || !has(self.local)",message="local config can only be set when type is local"
// +kubebuilder:validation:XValidation:rule="self.type == 's3' || !has(self.s3)",message="s3 config can only be set when type is s3"
// +kubebuilder:validation:XValidation:rule="self.type == 'gcs' || !has(self.gcs)",message="gcs config can only be set when type is gcs"
type ArtifactStoreSpec struct {
	// Type selects the storage backend.
	// +kubebuilder:validation:Required
	Type ArtifactStoreType `json:"type"`
	// Local configures local-disk storage. Only used when type=local.
	// +optional
	Local *ArtifactStoreLocalSpec `json:"local,omitempty"`
	// S3 configures Amazon S3 storage. Only used when type=s3.
	// +optional
	S3 *ArtifactStoreS3Spec `json:"s3,omitempty"`
	// GCS configures Google Cloud Storage. Only used when type=gcs.
	// +optional
	GCS *ArtifactStoreGCSSpec `json:"gcs,omitempty"`
}

// ArtifactSpec declares a named file artifact produced by a pipeline step.
// The agent is expected to write the artifact file under $AGENT_ARTIFACT_DIR/<name>.
type ArtifactSpec struct {
	// Name is the artifact identifier, used in template references:
	// {{ .steps.<stepName>.artifacts.<name> }}
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
	// Description documents the artifact for operators and tooling.
	// +optional
	Description string `json:"description,omitempty"`
	// ContentType is the MIME type hint for the artifact (e.g. application/pdf).
	// +optional
	ContentType string `json:"contentType,omitempty"`
}

// SwarmTeamRoutingSpec configures routed mode execution for an SwarmTeam.
// When set on an SwarmTeam, incoming tasks are dispatched automatically to the
// best-matching agent by an LLM router call against SwarmRegistry — no pipeline
// DAG or hardcoded roles required.
type SwarmTeamRoutingSpec struct {
	// RegistryRef names the SwarmRegistry to query for capability resolution.
	// Defaults to the first SwarmRegistry found in the namespace when omitted.
	// +optional
	RegistryRef *LocalObjectReference `json:"registryRef,omitempty"`

	// Model is the LLM model used for the router call.
	// A lightweight model (e.g. haiku) is sufficient and recommended.
	// Defaults to the operator-wide default model when omitted.
	// +kubebuilder:validation:MinLength=1
	// +optional
	Model string `json:"model,omitempty"`

	// SystemPrompt overrides the default router system prompt.
	// Use {{ .Capabilities }} to embed the capability list and
	// {{ .Input }} to embed the task input in a custom prompt.
	// +optional
	SystemPrompt string `json:"systemPrompt,omitempty"`

	// Fallback is the name of a standalone SwarmAgent to use when no capability
	// matches or the router LLM fails to select one.
	// When absent and no match is found, the run fails with RoutingFailed.
	// +optional
	Fallback string `json:"fallback,omitempty"`

	// MaxHops is the maximum number of sequential routing decisions per run.
	// Reserved for future multi-hop support. Must be 1 in this version.
	// +kubebuilder:default=1
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=1
	// +optional
	MaxHops int `json:"maxHops,omitempty"`
}

// RegistryLookupStrategy controls which agent wins when multiple match.
// +kubebuilder:validation:Enum=least-busy;round-robin;random
type RegistryLookupStrategy string

const (
	RegistryLookupStrategyLeastBusy  RegistryLookupStrategy = "least-busy"
	RegistryLookupStrategyRoundRobin RegistryLookupStrategy = "round-robin"
	RegistryLookupStrategyRandom     RegistryLookupStrategy = "random"
)

// RegistryLookupSpec configures runtime agent resolution via SwarmRegistry.
type RegistryLookupSpec struct {
	// Capability is the exact capability ID to match.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Capability string `json:"capability"`
	// Tags narrows candidates to agents that declare ALL listed tags.
	// +optional
	Tags []string `json:"tags,omitempty"`
	// Strategy controls which agent is selected when multiple match.
	// +kubebuilder:default=least-busy
	Strategy RegistryLookupStrategy `json:"strategy,omitempty"`
	// RegistryRef names the SwarmRegistry to query. Defaults to first registry in namespace.
	// +optional
	RegistryRef *LocalObjectReference `json:"registryRef,omitempty"`
	// Fallback is the role/agent name to use when no agent matches.
	// If unset and no match, the step fails with RegistryLookupFailed.
	// +optional
	Fallback string `json:"fallback,omitempty"`
}

// SwarmTeamPipelineStep is one node in the SwarmTeam DAG pipeline.
type SwarmTeamPipelineStep struct {
	// Role references a role name in spec.roles. The step name equals the role name.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Role string `json:"role"`

	// Inputs is a map of input key → Go template expression referencing pipeline
	// inputs or earlier step outputs. Example: "{{ .steps.research.output }}"
	Inputs map[string]string `json:"inputs,omitempty"`

	// DependsOn lists role names (step names) that must complete before this step runs.
	DependsOn []string `json:"dependsOn,omitempty"`

	// If is an optional Go template expression. When set, the step only executes if the
	// expression evaluates to a truthy value. A falsy result marks the step Skipped.
	If string `json:"if,omitempty"`

	// Loop makes this step repeat until Condition evaluates to false or MaxIterations is reached.
	Loop *LoopSpec `json:"loop,omitempty"`

	// OutputSchema is an optional JSON Schema string that constrains this step's output.
	OutputSchema string `json:"outputSchema,omitempty"`

	// Validate configures optional output validation for this step.
	// When set, the step enters Validating phase after the agent completes and only
	// transitions to Succeeded once all configured checks pass.
	// +optional
	Validate *StepValidation `json:"validate,omitempty"`

	// OutputArtifacts declares file artifacts this step produces.
	// The agent writes each artifact to $AGENT_ARTIFACT_DIR/<name> after its task.
	// Artifact URLs are stored in SwarmFlowStepStatus.Artifacts and available to
	// downstream steps via "{{ .steps.<stepName>.artifacts.<name> }}".
	// +optional
	OutputArtifacts []ArtifactSpec `json:"outputArtifacts,omitempty"`

	// InputArtifacts maps a local artifact name to an upstream step's artifact.
	// The value format is "<stepName>.<artifactName>".
	// The resolved URL is injected via AGENT_INPUT_ARTIFACTS env var as a JSON map.
	// +optional
	InputArtifacts map[string]string `json:"inputArtifacts,omitempty"`

	// RegistryLookup resolves the executing agent by capability at runtime.
	// The SwarmRun controller resolves this before the step starts and records
	// the resolved agent in status.resolvedAgent.
	// +optional
	RegistryLookup *RegistryLookupSpec `json:"registryLookup,omitempty"`

	// ContextPolicy controls how this step's output is prepared before injection
	// into downstream step prompts. Defaults to strategy=full (verbatim, current behaviour).
	// +optional
	ContextPolicy *StepContextPolicy `json:"contextPolicy,omitempty"`

	// MaxOutputBytes limits the size of stored step output. Default: 65536 (64KB).
	// Outputs exceeding this limit are truncated with a "[truncated]" marker.
	// Set to 0 for unlimited (not recommended - risks exceeding etcd object size limits).
	// +kubebuilder:default=65536
	// +kubebuilder:validation:Minimum=0
	// +optional
	MaxOutputBytes int `json:"maxOutputBytes,omitempty"`
}

// SwarmTeamScaleToZero configures scale-to-zero behaviour for a team's inline agents.
type SwarmTeamScaleToZero struct {
	// Enabled activates scale-to-zero. When true, idle roles are scaled to 0 replicas after
	// AfterSeconds of inactivity and are warmed back up automatically when a new run triggers.
	// +optional
	Enabled bool `json:"enabled,omitempty"`

	// AfterSeconds is how long a role must be idle (no active steps) before it is scaled to zero.
	// Minimum 30. Defaults to 300 (5 minutes).
	// +kubebuilder:default=300
	// +kubebuilder:validation:Minimum=30
	// +optional
	AfterSeconds *int32 `json:"afterSeconds,omitempty"`
}

// SwarmTeamAutoscaling configures demand-driven replica scaling for a team's inline agents.
// The operator adjusts replicas between 0 and spec.roles[].replicas based on active pipeline steps.
// This is distinct from KEDA-based autoscaling on individual SwarmAgents.
type SwarmTeamAutoscaling struct {
	// Enabled turns on team-owned autoscaling. When false the operator does not touch replicas.
	// +optional
	Enabled bool `json:"enabled,omitempty"`

	// ScaleToZero configures idle scale-to-zero.
	// When unset, roles are always kept at their configured replica count.
	// +optional
	ScaleToZero *SwarmTeamScaleToZero `json:"scaleToZero,omitempty"`
}

// SwarmTeamLimits constrains team-level resource usage.
type SwarmTeamLimits struct {
	// MaxDailyTokens is the rolling 24-hour token budget across the whole team pipeline.
	// Zero means no daily limit.
	// +kubebuilder:validation:Minimum=1
	MaxDailyTokens int64 `json:"maxDailyTokens,omitempty"`
}

// SwarmTeamInputSpec defines one formal input parameter for a pipeline.
// Parameters declared here are validated and defaulted when an SwarmRun is created.
type SwarmTeamInputSpec struct {
	// Name is the parameter key, referenced in step prompts via "{{ .input.<name> }}".
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// Type is the expected type of the value, used for documentation and tooling.
	// The operator enforces presence/default but does not coerce string values.
	// +kubebuilder:validation:Enum=string;number;boolean;object;array
	// +kubebuilder:default=string
	// +optional
	Type string `json:"type,omitempty"`

	// Description documents the parameter for operators and tooling.
	// +optional
	Description string `json:"description,omitempty"`

	// Required marks this parameter as mandatory. When true and the parameter is
	// absent from spec.input at run creation, the SwarmRun is immediately failed.
	// +optional
	Required bool `json:"required,omitempty"`

	// Default is the value applied when Required is false and the parameter
	// is not provided in spec.input.
	// +optional
	Default string `json:"default,omitempty"`
}

// SwarmTeamSpec defines the desired state of SwarmTeam.
// +kubebuilder:validation:XValidation:rule="has(self.routing) ? (!has(self.roles) || self.roles.size() == 0) : (has(self.roles) && self.roles.size() > 0)",message="spec.routing is mutually exclusive with spec.roles; in routed mode omit spec.roles, in pipeline/dynamic mode spec.roles must have at least one role"
// +kubebuilder:validation:XValidation:rule="!(has(self.routing) && has(self.pipeline) && self.pipeline.size() > 0)",message="spec.routing is mutually exclusive with spec.pipeline"
type SwarmTeamSpec struct {
	// Entry is the role name that receives external tasks in dynamic mode.
	// Exactly one role should be the entry point for dynamic teams.
	// In pipeline mode (spec.pipeline set), entry is optional.
	// +optional
	Entry string `json:"entry,omitempty"`

	// Output is a Go template expression that selects the final pipeline result.
	// Example: "{{ .steps.summarize.output }}"
	// Only used in pipeline mode.
	// +optional
	Output string `json:"output,omitempty"`

	// Inputs defines the formal schema for pipeline input parameters.
	// When set, required parameters are enforced and defaults are applied before
	// an SwarmRun starts executing. Steps reference these values via "{{ .input.<name> }}".
	// +optional
	Inputs []SwarmTeamInputSpec `json:"inputs,omitempty"`

	// Input is the initial data passed into the pipeline.
	// Step inputs can reference these values via "{{ .input.<key> }}".
	// Only used in pipeline mode.
	// +optional
	Input map[string]string `json:"input,omitempty"`

	// TimeoutSeconds is the maximum wall-clock seconds the pipeline may run.
	// Zero means no timeout. Only used in pipeline mode.
	// +kubebuilder:validation:Minimum=1
	// +optional
	TimeoutSeconds int `json:"timeoutSeconds,omitempty"`

	// MaxTokens is the total token budget for the entire pipeline run.
	// Zero means no limit. Only used in pipeline mode.
	// +kubebuilder:validation:Minimum=1
	// +optional
	MaxTokens int64 `json:"maxTokens,omitempty"`

	// Limits constrains team-level resource usage.
	// +optional
	Limits *SwarmTeamLimits `json:"limits,omitempty"`

	// Roles defines the roles that make up this team.
	// At least one role is required unless spec.routing is set (routed mode).
	// +optional
	Roles []SwarmTeamRole `json:"roles,omitempty"`

	// Pipeline defines an optional DAG of steps that drive ordered execution.
	// When set, the team operates in pipeline mode (job semantics).
	// When unset, the team operates in dynamic mode (service semantics).
	// +optional
	Pipeline []SwarmTeamPipelineStep `json:"pipeline,omitempty"`

	// DefaultContextPolicy is applied to any step's output when it is referenced
	// by a non-adjacent downstream step. A step is considered adjacent when it
	// appears in the consuming step's dependsOn list, or is the immediately
	// preceding step when dependsOn is absent.
	// Per-step contextPolicy takes precedence over this default.
	// When unset, strategy=full is used for all steps (current behaviour).
	// +optional
	DefaultContextPolicy *StepContextPolicy `json:"defaultContextPolicy,omitempty"`

	// SuccessfulRunsHistoryLimit is the number of successful SwarmRun objects to
	// retain for this team. Oldest runs beyond this limit are deleted automatically.
	// Set to 0 to delete successful runs immediately after completion.
	// +kubebuilder:default=10
	// +kubebuilder:validation:Minimum=0
	// +optional
	SuccessfulRunsHistoryLimit *int32 `json:"successfulRunsHistoryLimit,omitempty"`

	// FailedRunsHistoryLimit is the number of failed SwarmRun objects to retain.
	// +kubebuilder:default=3
	// +kubebuilder:validation:Minimum=0
	// +optional
	FailedRunsHistoryLimit *int32 `json:"failedRunsHistoryLimit,omitempty"`

	// RunRetainFor is the maximum age of completed SwarmRun objects for this team.
	// Runs older than this duration are deleted regardless of the history limits.
	// Zero means no age-based cleanup (only count-based limits apply).
	// Example: "168h" (7 days), "720h" (30 days).
	// +optional
	RunRetainFor *metav1.Duration `json:"runRetainFor,omitempty"`

	// NotifyRef references an SwarmNotify policy in the same namespace.
	// When set, the operator dispatches notifications after terminal phase transitions.
	// +optional
	NotifyRef *LocalObjectReference `json:"notifyRef,omitempty"`

	// BudgetRef references an SwarmBudget in the same namespace that governs token
	// spend for this team. When the budget is exhausted, new runs are blocked.
	// +optional
	BudgetRef *LocalObjectReference `json:"budgetRef,omitempty"`

	// RegistryRef names the SwarmRegistry used for registryLookup steps and routed
	// mode agent resolution. Defaults to "default".
	// +kubebuilder:default=default
	// +optional
	RegistryRef string `json:"registryRef,omitempty"`

	// ArtifactStore configures where pipeline file artifacts are stored.
	// When unset, file artifact support is disabled and any OutputArtifacts
	// declarations on pipeline steps are ignored.
	// +optional
	ArtifactStore *ArtifactStoreSpec `json:"artifactStore,omitempty"`

	// Autoscaling configures demand-driven replica scaling for this team's inline agents.
	// When enabled, the operator scales each role's managed SwarmAgent between 0 and its
	// configured replica count based on the number of active pipeline steps for that role.
	// Only applies to inline roles (those with model+systemPrompt); external SwarmAgent references
	// are not scaled by the team controller.
	// +optional
	Autoscaling *SwarmTeamAutoscaling `json:"autoscaling,omitempty"`

	// Routing configures routed mode. When set, the team operates in routed mode:
	// tasks are dispatched automatically via an LLM router call against SwarmRegistry.
	// Mutually exclusive with spec.pipeline and spec.roles.
	// +optional
	Routing *SwarmTeamRoutingSpec `json:"routing,omitempty"`
}

// SwarmTeamRoleStatus captures the observed state of one team role.
type SwarmTeamRoleStatus struct {
	// Name matches SwarmTeamRole.Name.
	Name string `json:"name"`
	// ReadyReplicas is the number of agent pods ready to accept tasks.
	ReadyReplicas int32 `json:"readyReplicas,omitempty"`
	// DesiredReplicas is the configured replica count for this role.
	DesiredReplicas int32 `json:"desiredReplicas,omitempty"`
	// ManagedSwarmAgent is the name of the auto-created SwarmAgent for inline roles.
	ManagedSwarmAgent string `json:"managedSwarmAgent,omitempty"`
}

// SwarmTeamStatus defines the observed state of SwarmTeam.
type SwarmTeamStatus struct {
	// Phase is the overall team state.
	// For pipeline teams this mirrors the most recent SwarmRun phase.
	// For dynamic teams this reflects infrastructure readiness.
	Phase SwarmTeamPhase `json:"phase,omitempty"`

	// Roles lists the observed state of each role.
	Roles []SwarmTeamRoleStatus `json:"roles,omitempty"`

	// EntryRole is the role name that is the external submission point.
	EntryRole string `json:"entryRole,omitempty"`

	// LastRunName is the name of the most recently created SwarmRun for this team.
	// Empty when no run has been triggered yet.
	LastRunName string `json:"lastRunName,omitempty"`

	// LastRunPhase is the phase of the most recently created SwarmRun.
	// Mirrors SwarmRun.Status.Phase for quick visibility in kubectl get swarmteam.
	LastRunPhase SwarmRunPhase `json:"lastRunPhase,omitempty"`

	// ScaledToZero is true when all inline-role agents have been scaled to 0 replicas
	// due to team autoscaling idle timeout. The team warms up automatically when triggered.
	// +optional
	ScaledToZero bool `json:"scaledToZero,omitempty"`

	// LastActiveTime is when the team last had an active (running) pipeline step.
	// Used by the autoscaler to decide when to scale idle roles to zero.
	// +optional
	LastActiveTime *metav1.Time `json:"lastActiveTime,omitempty"`

	// ObservedGeneration is the .metadata.generation this status reflects.
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Conditions reflect the current state of the SwarmTeam.
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Last Run",type=string,JSONPath=`.status.lastRunPhase`
// +kubebuilder:printcolumn:name="Entry",type=string,JSONPath=`.status.entryRole`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
// +kubebuilder:resource:shortName={swteam,swteams},scope=Namespaced,categories=kubeswarm

// SwarmTeam is the unified resource for agent teams. It supports three execution modes:
// dynamic mode (no spec.pipeline — service semantics, roles use delegate() for routing),
// pipeline mode (spec.pipeline set — job semantics, DAG execution like SwarmFlow), and
// routed mode (spec.routing set — LLM-driven capability dispatch via SwarmRegistry, RFC-0019).
type SwarmTeam struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// +required
	Spec SwarmTeamSpec `json:"spec"`

	// +optional
	Status SwarmTeamStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// SwarmTeamList contains a list of SwarmTeam.
type SwarmTeamList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []SwarmTeam `json:"items"`
}

func init() {
	SchemeBuilder.Register(&SwarmTeam{}, &SwarmTeamList{})
}
