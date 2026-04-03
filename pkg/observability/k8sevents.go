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

// Package observability — k8sevents.go provides a best-effort Kubernetes Event
// emitter for agent pods. When the pod is not running inside a Kubernetes
// cluster, all calls are no-ops so local development is unaffected.
package observability

import (
	"fmt"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	typedcorev1 "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/tools/reference"

	kubeswarmv1alpha1 "github.com/kubeswarm/kubeswarm/api/v1alpha1"
)

// AgentEventRecorder emits Kubernetes Events on the SwarmAgent object that owns
// this pod. All methods are safe to call when not running inside a cluster —
// they become no-ops.
type AgentEventRecorder struct {
	recorder record.EventRecorder
	agentRef *corev1.ObjectReference
}

// NewAgentEventRecorder attempts to create an in-cluster EventRecorder for the
// given SwarmAgent. Returns a no-op recorder (no error) when not in a cluster.
func NewAgentEventRecorder(namespace, agentName string) *AgentEventRecorder {
	cfg, err := rest.InClusterConfig()
	if err != nil {
		// Not in a cluster (local dev, swarm run). Return no-op recorder.
		return &AgentEventRecorder{}
	}
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return &AgentEventRecorder{}
	}

	scheme := runtime.NewScheme()
	_ = kubeswarmv1alpha1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)

	broadcaster := record.NewBroadcaster()
	broadcaster.StartRecordingToSink(&typedcorev1.EventSinkImpl{
		Interface: cs.CoreV1().Events(namespace),
	})

	rec := broadcaster.NewRecorder(scheme, corev1.EventSource{Component: "swarm-agent"})

	agent := &kubeswarmv1alpha1.SwarmAgent{
		ObjectMeta: metav1.ObjectMeta{
			Name:      agentName,
			Namespace: namespace,
		},
	}
	ref, err := reference.GetReference(scheme, agent)
	if err != nil {
		return &AgentEventRecorder{}
	}

	return &AgentEventRecorder{recorder: rec, agentRef: ref}
}

// TaskStarted emits a Normal event when a task begins processing.
func (r *AgentEventRecorder) TaskStarted(taskID, role string) {
	if r.recorder == nil {
		return
	}
	msg := fmt.Sprintf("task %s started", taskID)
	if role != "" {
		msg = fmt.Sprintf("task %s started (role: %s)", taskID, role)
	}
	r.recorder.Event(r.agentRef, corev1.EventTypeNormal, "TaskStarted", msg)
}

// TaskCompleted emits a Normal event when a task finishes successfully.
func (r *AgentEventRecorder) TaskCompleted(taskID string, inputTokens, outputTokens int64) {
	if r.recorder == nil {
		return
	}
	r.recorder.Event(r.agentRef, corev1.EventTypeNormal, "TaskCompleted",
		fmt.Sprintf("task %s completed — input: %d, output: %d tokens", taskID, inputTokens, outputTokens))
}

// TaskFailed emits a Warning event when a task errors out.
func (r *AgentEventRecorder) TaskFailed(taskID, reason string) {
	if r.recorder == nil {
		return
	}
	r.recorder.Event(r.agentRef, corev1.EventTypeWarning, "TaskFailed",
		fmt.Sprintf("task %s failed: %s", taskID, reason))
}

// TaskDelegated emits a Normal event when a task is delegated to another role.
func (r *AgentEventRecorder) TaskDelegated(taskID, toRole, childTaskID string) {
	if r.recorder == nil {
		return
	}
	r.recorder.Event(r.agentRef, corev1.EventTypeNormal, "TaskDelegated",
		fmt.Sprintf("task %s delegated to role %s as task %s", taskID, toRole, childTaskID))
}
