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

package controller

import (
	"net/url"

	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kubeswarmv1alpha1 "github.com/kubeswarm/kubeswarm/api/v1alpha1"
)

//go:fix inline
func int32Ptr(i int32) *int32 { return new(i) }

//go:fix inline
func int64Ptr(i int64) *int64 { return new(i) }

//go:fix inline
func protocolPtr(p corev1.Protocol) *corev1.Protocol { return new(p) }

// appendStreamParam appends or replaces the ?stream= query parameter on a Redis URL.
func appendStreamParam(baseURL, streamName string) string {
	u, err := url.Parse(baseURL)
	if err != nil {
		return baseURL
	}
	q := u.Query()
	q.Set("stream", streamName)
	u.RawQuery = q.Encode()
	return u.String()
}

// setCondition sets a status condition on any object whose status has a Conditions slice.
// condType defaults to kubeswarmv1alpha1.ConditionReady when empty.
func setCondition(conditions *[]metav1.Condition, generation int64, condType string, status metav1.ConditionStatus, reason, message string) {
	if condType == "" {
		condType = kubeswarmv1alpha1.ConditionReady
	}
	apimeta.SetStatusCondition(conditions, metav1.Condition{
		Type:               condType,
		Status:             status,
		ObservedGeneration: generation,
		Reason:             reason,
		Message:            message,
	})
}

// latestRunFromList returns the most recently created SwarmRun from the slice, or nil if empty.
func latestRunFromList(runs []kubeswarmv1alpha1.SwarmRun) *kubeswarmv1alpha1.SwarmRun {
	var latest *kubeswarmv1alpha1.SwarmRun
	for i := range runs {
		run := &runs[i]
		if latest == nil || run.CreationTimestamp.After(latest.CreationTimestamp.Time) {
			latest = run
		}
	}
	return latest
}

// mirrorRunPhaseToTeam maps the latest SwarmRun phase to the SwarmTeam status fields.
func mirrorRunPhaseToTeam(team *kubeswarmv1alpha1.SwarmTeam, latestRun *kubeswarmv1alpha1.SwarmRun) {
	if latestRun == nil {
		team.Status.Phase = kubeswarmv1alpha1.SwarmTeamPhaseReady
		return
	}
	team.Status.LastRunName = latestRun.Name
	team.Status.LastRunPhase = latestRun.Status.Phase
	switch latestRun.Status.Phase {
	case kubeswarmv1alpha1.SwarmRunPhaseRunning:
		team.Status.Phase = kubeswarmv1alpha1.SwarmTeamPhaseRunning
	case kubeswarmv1alpha1.SwarmRunPhaseSucceeded:
		team.Status.Phase = kubeswarmv1alpha1.SwarmTeamPhaseSucceeded
	case kubeswarmv1alpha1.SwarmRunPhaseFailed:
		team.Status.Phase = kubeswarmv1alpha1.SwarmTeamPhaseFailed
	default:
		team.Status.Phase = kubeswarmv1alpha1.SwarmTeamPhaseReady
	}
}

// resolveRoleAgentName returns the SwarmAgent name for a role.
// Explicit SwarmAgent refs use the ref name; inline roles use "{teamRef}-{roleName}".
func resolveRoleAgentName(teamRef string, role kubeswarmv1alpha1.SwarmTeamRole) string {
	if role.SwarmAgent != "" {
		return role.SwarmAgent
	}
	return teamRef + "-" + role.Name
}
