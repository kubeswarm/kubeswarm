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

package flow

import (
	"fmt"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kubeswarmv1alpha1 "github.com/kubeswarm/kubeswarm/api/v1alpha1"
)

// SumRunStepTokens accumulates token counts and cost for SwarmRun pipeline steps.
func SumRunStepTokens(f *kubeswarmv1alpha1.SwarmRun) int64 {
	var totalIn, totalOut int64
	var totalCost float64
	for _, st := range f.Status.Steps {
		if st.TokenUsage != nil {
			totalIn += st.TokenUsage.InputTokens
			totalOut += st.TokenUsage.OutputTokens
		}
		totalCost += st.CostUSD
	}
	if totalIn > 0 || totalOut > 0 {
		f.Status.TotalTokenUsage = &kubeswarmv1alpha1.TokenUsage{
			InputTokens:  totalIn,
			OutputTokens: totalOut,
			TotalTokens:  totalIn + totalOut,
		}
	}
	f.Status.TotalCostUSD = totalCost
	return totalIn + totalOut
}

// EnforceRunTimeout sets the SwarmRun to Failed if the pipeline timeout has elapsed.
// Any steps still in Running or Pending are marked Failed so retry can reset them.
func EnforceRunTimeout(f *kubeswarmv1alpha1.SwarmRun, now metav1.Time) bool {
	if f.Spec.TimeoutSeconds <= 0 || f.Status.StartTime == nil {
		return false
	}
	deadline := f.Status.StartTime.Add(time.Duration(f.Spec.TimeoutSeconds) * time.Second)
	if !now.After(deadline) {
		return false
	}
	msg := fmt.Sprintf("pipeline exceeded timeout of %ds", f.Spec.TimeoutSeconds)
	for i, st := range f.Status.Steps {
		if st.Phase == kubeswarmv1alpha1.SwarmFlowStepPhaseRunning ||
			st.Phase == kubeswarmv1alpha1.SwarmFlowStepPhasePending {
			f.Status.Steps[i].Phase = kubeswarmv1alpha1.SwarmFlowStepPhaseFailed
			f.Status.Steps[i].Message = msg
		}
	}
	f.Status.Phase = kubeswarmv1alpha1.SwarmRunPhaseFailed
	f.Status.CompletionTime = &now
	SetRunCondition(f, metav1.ConditionFalse, "TimedOut", msg)
	return true
}

// UpdateRunPipelinePhase inspects SwarmRun pipeline step statuses and transitions to Succeeded or Failed.
func UpdateRunPipelinePhase(f *kubeswarmv1alpha1.SwarmRun, templateData map[string]any) {
	now := metav1.Now()

	if EnforceRunTimeout(f, now) {
		return
	}

	totalTokens := SumRunStepTokens(f)

	if f.Spec.MaxTokens > 0 && totalTokens > f.Spec.MaxTokens {
		f.Status.Phase = kubeswarmv1alpha1.SwarmRunPhaseFailed
		f.Status.CompletionTime = &now
		SetRunCondition(f, metav1.ConditionFalse, "BudgetExceeded",
			fmt.Sprintf("token budget of %d exceeded: used %d", f.Spec.MaxTokens, totalTokens))
		return
	}

	// Guard: if there are no steps, the run is not ready to complete.
	// This prevents marking an empty pipeline as Succeeded.
	if len(f.Status.Steps) == 0 {
		return
	}

	failed, allDone := false, true
	for _, st := range f.Status.Steps {
		switch st.Phase {
		case kubeswarmv1alpha1.SwarmFlowStepPhaseFailed:
			failed = true
		case kubeswarmv1alpha1.SwarmFlowStepPhaseSucceeded, kubeswarmv1alpha1.SwarmFlowStepPhaseSkipped:
			// ok — both count as done
		default:
			allDone = false
		}
	}

	switch {
	case failed:
		f.Status.Phase = kubeswarmv1alpha1.SwarmRunPhaseFailed
		f.Status.CompletionTime = &now
		SetRunCondition(f, metav1.ConditionFalse, "StepFailed", "one or more steps failed")
	case allDone:
		f.Status.Phase = kubeswarmv1alpha1.SwarmRunPhaseSucceeded
		f.Status.CompletionTime = &now
		if f.Spec.Output != "" {
			out, _ := ResolveTemplate(f.Spec.Output, templateData)
			f.Status.Output = out
		}
		SetRunCondition(f, metav1.ConditionTrue, "Succeeded", "all steps completed successfully")
	}
}
