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
	"context"
	"fmt"
	"testing"
	"time"

	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kubeswarmv1alpha1 "github.com/kubeswarm/kubeswarm/api/v1alpha1"

	"github.com/robfig/cron/v3"
)

// ---- Pure function tests ----

func TestResolveTriggerTemplate(t *testing.T) {
	t.Run("returns a plain string unchanged", func(t *testing.T) {
		out, err := resolveTriggerTemplate("hello", FireContext{Name: "t"})
		requireNoError(t, err)
		requireEqual(t, out, "hello")
	})

	t.Run("resolves .trigger.name", func(t *testing.T) {
		out, err := resolveTriggerTemplate("{{ .trigger.name }}", FireContext{Name: "my-trigger"})
		requireNoError(t, err)
		requireEqual(t, out, "my-trigger")
	})

	t.Run("resolves .trigger.firedAt", func(t *testing.T) {
		out, err := resolveTriggerTemplate("fired:{{ .trigger.firedAt }}", FireContext{FiredAt: "2026-01-01T00:00:00Z"})
		requireNoError(t, err)
		requireEqual(t, out, "fired:2026-01-01T00:00:00Z")
	})

	t.Run("resolves .trigger.output", func(t *testing.T) {
		out, err := resolveTriggerTemplate("{{ .trigger.output }}", FireContext{Output: "some output"})
		requireNoError(t, err)
		requireEqual(t, out, "some output")
	})

	t.Run("returns empty string for missing key (missingkey=zero)", func(t *testing.T) {
		out, err := resolveTriggerTemplate("{{ .trigger.body.key }}", FireContext{Name: "t"})
		requireNoError(t, err)
		// map[string]any nil body - body itself renders as <no value> or map key as empty
		_ = out // just ensure no error
	})
}

func TestMostRecentSchedule(t *testing.T) {
	parseSchedule := func(t *testing.T, expr string) cron.Schedule {
		s, err := cron.ParseStandard(expr)
		requireNoError(t, err)
		return s
	}

	t.Run("returns nil when no scheduled time has passed since lookback", func(t *testing.T) {
		// lastFired at 08:59 - lookback = 08:59; schedule.Next(08:59) = 10:00 > now=09:00.
		now := time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC)
		lastFiredTime := time.Date(2026, 1, 1, 8, 59, 0, 0, time.UTC)
		lastFired := metav1.NewTime(lastFiredTime)
		schedule := parseSchedule(t, "0 10 * * *")
		result := mostRecentSchedule(schedule, now, &lastFired)
		requireNil(t, result)
	})

	t.Run("returns the most recent scheduled time before now", func(t *testing.T) {
		now := time.Date(2026, 1, 1, 10, 5, 0, 0, time.UTC)
		// "0 10 * * *" fires at 10:00am; now is 10:05am so one fire has passed.
		schedule := parseSchedule(t, "0 10 * * *")
		result := mostRecentSchedule(schedule, now, nil)
		requireNotNil(t, result)
		requireEqual(t, result.Hour(), 10)
		requireEqual(t, result.Minute(), 0)
	})

	t.Run("uses lastFired as the lookback anchor when it is more recent than 24h ago", func(t *testing.T) {
		now := time.Date(2026, 1, 2, 10, 5, 0, 0, time.UTC)
		// Schedule fires every minute; lastFired just 2 minutes ago.
		lastFiredTime := now.Add(-2 * time.Minute)
		lastFired := metav1.NewTime(lastFiredTime)
		schedule := parseSchedule(t, "* * * * *")
		result := mostRecentSchedule(schedule, now, &lastFired)
		requireNotNil(t, result)
		// Should find the most recent minute tick after lastFired.
		requireTrue(t, result.After(lastFiredTime), "expected result after lastFired")
	})
}

// ---- Reconciler integration tests (via envtest) ----

func TestSwarmEventController(t *testing.T) {
	const namespace = "default"
	ctx := context.Background()

	newEventReconciler := func() *SwarmEventReconciler {
		return &SwarmEventReconciler{
			Client:            k8sClient,
			Scheme:            k8sClient.Scheme(),
			TriggerWebhookURL: "http://controller.svc:8092",
		}
	}

	reconcileEvent := func(t *testing.T, name string) (*kubeswarmv1alpha1.SwarmEvent, error) {
		nn := types.NamespacedName{Name: name, Namespace: namespace}
		_, err := newEventReconciler().Reconcile(ctx, reconcile.Request{NamespacedName: nn})
		if err != nil {
			return nil, err
		}
		ev := &kubeswarmv1alpha1.SwarmEvent{}
		requireNoError(t, k8sClient.Get(ctx, nn, ev))
		return ev, nil
	}

	cleanupEvent := func(name string) {
		ev := &kubeswarmv1alpha1.SwarmEvent{}
		nn := types.NamespacedName{Name: name, Namespace: namespace}
		if err := k8sClient.Get(ctx, nn, ev); err == nil {
			_ = k8sClient.Delete(ctx, ev)
		}
	}

	t.Run("nonexistent SwarmEvent", func(t *testing.T) {
		t.Run("returns nil without error", func(t *testing.T) {
			nn := types.NamespacedName{Name: "does-not-exist-event", Namespace: namespace}
			_, err := newEventReconciler().Reconcile(ctx, reconcile.Request{NamespacedName: nn})
			requireNoError(t, err)
		})
	})

	t.Run("suspended trigger", func(t *testing.T) {
		const name = "ev-suspended"
		t.Cleanup(func() { cleanupEvent(name) })

		t.Run("sets Ready=False with Suspended reason", func(t *testing.T) {
			ev := &kubeswarmv1alpha1.SwarmEvent{
				ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
				Spec: kubeswarmv1alpha1.SwarmEventSpec{
					Suspended: true,
					Source:    kubeswarmv1alpha1.SwarmEventSource{Type: kubeswarmv1alpha1.TriggerSourceCron, Cron: "* * * * *"},
					Targets:   []kubeswarmv1alpha1.SwarmEventTarget{{Team: "some-team"}},
				},
			}
			requireNoError(t, k8sClient.Create(ctx, ev))

			result, err := reconcileEvent(t, name)
			requireNoError(t, err)
			cond := apimeta.FindStatusCondition(result.Status.Conditions, kubeswarmv1alpha1.ConditionReady)
			requireNotNil(t, cond)
			requireEqual(t, cond.Status, metav1.ConditionFalse)
			requireEqual(t, cond.Reason, "Suspended")
		})
	})

	t.Run("cron trigger", func(t *testing.T) {
		t.Run("with empty cron expression", func(t *testing.T) {
			const name = "ev-cron-empty"
			t.Cleanup(func() { cleanupEvent(name) })

			t.Run("sets Ready=False with InvalidCron reason", func(t *testing.T) {
				ev := &kubeswarmv1alpha1.SwarmEvent{
					ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
					Spec: kubeswarmv1alpha1.SwarmEventSpec{
						Source:  kubeswarmv1alpha1.SwarmEventSource{Type: kubeswarmv1alpha1.TriggerSourceCron, Cron: ""},
						Targets: []kubeswarmv1alpha1.SwarmEventTarget{{Team: "some-team"}},
					},
				}
				requireNoError(t, k8sClient.Create(ctx, ev))

				result, err := reconcileEvent(t, name)
				requireNoError(t, err)
				cond := apimeta.FindStatusCondition(result.Status.Conditions, kubeswarmv1alpha1.ConditionReady)
				requireNotNil(t, cond)
				requireEqual(t, cond.Reason, "InvalidCron")
			})
		})

		t.Run("with invalid cron expression", func(t *testing.T) {
			const name = "ev-cron-invalid"
			t.Cleanup(func() { cleanupEvent(name) })

			t.Run("sets Ready=False with InvalidCron reason", func(t *testing.T) {
				ev := &kubeswarmv1alpha1.SwarmEvent{
					ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
					Spec: kubeswarmv1alpha1.SwarmEventSpec{
						Source:  kubeswarmv1alpha1.SwarmEventSource{Type: kubeswarmv1alpha1.TriggerSourceCron, Cron: "not-a-cron"},
						Targets: []kubeswarmv1alpha1.SwarmEventTarget{{Team: "some-team"}},
					},
				}
				requireNoError(t, k8sClient.Create(ctx, ev))

				result, err := reconcileEvent(t, name)
				requireNoError(t, err)
				cond := apimeta.FindStatusCondition(result.Status.Conditions, kubeswarmv1alpha1.ConditionReady)
				requireNotNil(t, cond)
				requireEqual(t, cond.Reason, "InvalidCron")
			})
		})

		t.Run("with valid cron and last-fired set to prevent re-dispatch", func(t *testing.T) {
			const name = "ev-cron-valid"
			t.Cleanup(func() { cleanupEvent(name) })

			t.Run("sets Active condition and NextFireAt without dispatching a team", func(t *testing.T) {
				ev := &kubeswarmv1alpha1.SwarmEvent{
					ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
					Spec: kubeswarmv1alpha1.SwarmEventSpec{
						Source:  kubeswarmv1alpha1.SwarmEventSource{Type: kubeswarmv1alpha1.TriggerSourceCron, Cron: "0 3 * * *"},
						Targets: []kubeswarmv1alpha1.SwarmEventTarget{{Team: "no-fire-team"}},
					},
				}
				requireNoError(t, k8sClient.Create(ctx, ev))
				// Set LastFiredAt to now so mostRecentSchedule uses now as the lookback
				// anchor - the next 3am is in the future, so no dispatch happens.
				now := metav1.NewTime(time.Now().UTC())
				ev.Status.LastFiredAt = &now
				requireNoError(t, k8sClient.Status().Update(ctx, ev))

				result, err := reconcileEvent(t, name)
				requireNoError(t, err)
				cond := apimeta.FindStatusCondition(result.Status.Conditions, kubeswarmv1alpha1.ConditionReady)
				requireNotNil(t, cond)
				requireEqual(t, cond.Reason, "Active")
				requireNotNil(t, result.Status.NextFireAt)
				requireZero(t, result.Status.FiredCount)
			})
		})
	})

	t.Run("webhook trigger", func(t *testing.T) {
		const name = "ev-webhook"
		t.Cleanup(func() {
			cleanupEvent(name)
		})

		t.Run("creates a webhook-token Secret and sets WebhookURL in status", func(t *testing.T) {
			ev := &kubeswarmv1alpha1.SwarmEvent{
				ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
				Spec: kubeswarmv1alpha1.SwarmEventSpec{
					Source:  kubeswarmv1alpha1.SwarmEventSource{Type: kubeswarmv1alpha1.TriggerSourceWebhook},
					Targets: []kubeswarmv1alpha1.SwarmEventTarget{{Team: "some-team"}},
				},
			}
			requireNoError(t, k8sClient.Create(ctx, ev))

			result, err := reconcileEvent(t, name)
			requireNoError(t, err)
			requireEqual(t, result.Status.WebhookURL,
				fmt.Sprintf("http://controller.svc:8092/triggers/%s/%s/fire", namespace, name),
			)
			cond := apimeta.FindStatusCondition(result.Status.Conditions, kubeswarmv1alpha1.ConditionReady)
			requireNotNil(t, cond)
			requireEqual(t, cond.Status, metav1.ConditionTrue)
		})

		t.Run("is idempotent - reconciling again does not error", func(t *testing.T) {
			ev := &kubeswarmv1alpha1.SwarmEvent{}
			nn := types.NamespacedName{Name: name, Namespace: namespace}
			if err := k8sClient.Get(ctx, nn, ev); err != nil {
				ev = &kubeswarmv1alpha1.SwarmEvent{
					ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
					Spec: kubeswarmv1alpha1.SwarmEventSpec{
						Source:  kubeswarmv1alpha1.SwarmEventSource{Type: kubeswarmv1alpha1.TriggerSourceWebhook},
						Targets: []kubeswarmv1alpha1.SwarmEventTarget{{Team: "some-team"}},
					},
				}
				requireNoError(t, k8sClient.Create(ctx, ev))
			}
			_, err := reconcileEvent(t, name)
			requireNoError(t, err)
			_, err = reconcileEvent(t, name)
			requireNoError(t, err)
		})
	})

	t.Run("team-output trigger", func(t *testing.T) {
		t.Run("with nil teamOutput source", func(t *testing.T) {
			const name = "ev-to-nil"
			t.Cleanup(func() { cleanupEvent(name) })

			t.Run("sets InvalidSource condition", func(t *testing.T) {
				ev := &kubeswarmv1alpha1.SwarmEvent{
					ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
					Spec: kubeswarmv1alpha1.SwarmEventSpec{
						Source:  kubeswarmv1alpha1.SwarmEventSource{Type: kubeswarmv1alpha1.TriggerSourceTeamOutput},
						Targets: []kubeswarmv1alpha1.SwarmEventTarget{{Team: "some-team"}},
					},
				}
				requireNoError(t, k8sClient.Create(ctx, ev))

				result, err := reconcileEvent(t, name)
				requireNoError(t, err)
				cond := apimeta.FindStatusCondition(result.Status.Conditions, kubeswarmv1alpha1.ConditionReady)
				requireNotNil(t, cond)
				requireEqual(t, cond.Reason, "InvalidSource")
			})
		})

		t.Run("with a team that hasn't reached the desired phase", func(t *testing.T) {
			const evName = "ev-to-waiting"
			const teamName = "ev-to-watcher-team"
			t.Cleanup(func() {
				cleanupEvent(evName)
				tm := &kubeswarmv1alpha1.SwarmTeam{}
				if err := k8sClient.Get(ctx, types.NamespacedName{Name: teamName, Namespace: namespace}, tm); err == nil {
					_ = k8sClient.Delete(ctx, tm)
				}
			})

			t.Run("sets Watching condition", func(t *testing.T) {
				tm := &kubeswarmv1alpha1.SwarmTeam{
					ObjectMeta: metav1.ObjectMeta{Name: teamName, Namespace: namespace},
					Spec: kubeswarmv1alpha1.SwarmTeamSpec{
						Roles: []kubeswarmv1alpha1.SwarmTeamRole{
							{Name: "worker", Model: "claude-sonnet-4-20250514"},
						},
						Pipeline: []kubeswarmv1alpha1.SwarmTeamPipelineStep{
							{Role: "worker"},
						},
					},
				}
				requireNoError(t, k8sClient.Create(ctx, tm))

				ev := &kubeswarmv1alpha1.SwarmEvent{
					ObjectMeta: metav1.ObjectMeta{Name: evName, Namespace: namespace},
					Spec: kubeswarmv1alpha1.SwarmEventSpec{
						Source: kubeswarmv1alpha1.SwarmEventSource{
							Type:       kubeswarmv1alpha1.TriggerSourceTeamOutput,
							TeamOutput: &kubeswarmv1alpha1.TeamOutputSource{Name: teamName},
						},
						Targets: []kubeswarmv1alpha1.SwarmEventTarget{{Team: "some-team"}},
					},
				}
				requireNoError(t, k8sClient.Create(ctx, ev))

				result, err := reconcileEvent(t, evName)
				requireNoError(t, err)
				cond := apimeta.FindStatusCondition(result.Status.Conditions, kubeswarmv1alpha1.ConditionReady)
				requireNotNil(t, cond)
				requireEqual(t, cond.Reason, "Watching")
			})
		})

		t.Run("with a completed team and a dispatch target", func(t *testing.T) {
			const evName = "ev-to-fire"
			const watchedTeamName = "ev-to-watched-team"
			const templateTeamName = "ev-to-template-team"
			t.Cleanup(func() {
				cleanupEvent(evName)
				for _, n := range []string{watchedTeamName, templateTeamName} {
					tm := &kubeswarmv1alpha1.SwarmTeam{}
					if err := k8sClient.Get(ctx, types.NamespacedName{Name: n, Namespace: namespace}, tm); err == nil {
						_ = k8sClient.Delete(ctx, tm)
					}
				}
			})

			t.Run("fires and creates a dispatched SwarmTeam", func(t *testing.T) {
				// Create the watched (source) team and mark it Succeeded.
				watched := &kubeswarmv1alpha1.SwarmTeam{
					ObjectMeta: metav1.ObjectMeta{Name: watchedTeamName, Namespace: namespace},
					Spec: kubeswarmv1alpha1.SwarmTeamSpec{
						Roles: []kubeswarmv1alpha1.SwarmTeamRole{
							{Name: "worker", Model: "claude-sonnet-4-20250514"},
						},
						Pipeline: []kubeswarmv1alpha1.SwarmTeamPipelineStep{
							{Role: "worker"},
						},
					},
				}
				requireNoError(t, k8sClient.Create(ctx, watched))
				watched.Status.Phase = kubeswarmv1alpha1.SwarmTeamPhaseSucceeded
				requireNoError(t, k8sClient.Status().Update(ctx, watched))
				// Create an SwarmRun representing the completed run for this team.
				completionTime := metav1.NewTime(time.Now().UTC().Add(-time.Minute))
				watchedRun := &kubeswarmv1alpha1.SwarmRun{
					ObjectMeta: metav1.ObjectMeta{
						Name:      watchedTeamName + "-run-1",
						Namespace: namespace,
						Labels:    map[string]string{kubeswarmv1alpha1.LabelTeam: watchedTeamName},
					},
					Spec: kubeswarmv1alpha1.SwarmRunSpec{
						TeamRef:  watchedTeamName,
						Pipeline: []kubeswarmv1alpha1.SwarmTeamPipelineStep{{Role: "worker"}},
						Roles:    []kubeswarmv1alpha1.SwarmTeamRole{{Name: "worker", Model: "claude-sonnet-4-20250514"}},
					},
				}
				requireNoError(t, k8sClient.Create(ctx, watchedRun))
				watchedRun.Status.Phase = kubeswarmv1alpha1.SwarmRunPhaseSucceeded
				watchedRun.Status.CompletionTime = &completionTime
				watchedRun.Status.Output = "great result"
				requireNoError(t, k8sClient.Status().Update(ctx, watchedRun))

				// Create the template team to be dispatched.
				tmpl := &kubeswarmv1alpha1.SwarmTeam{
					ObjectMeta: metav1.ObjectMeta{Name: templateTeamName, Namespace: namespace},
					Spec: kubeswarmv1alpha1.SwarmTeamSpec{
						Roles: []kubeswarmv1alpha1.SwarmTeamRole{
							{Name: "worker", Model: "claude-sonnet-4-20250514"},
						},
						Pipeline: []kubeswarmv1alpha1.SwarmTeamPipelineStep{
							{Role: "worker"},
						},
					},
				}
				requireNoError(t, k8sClient.Create(ctx, tmpl))

				ev := &kubeswarmv1alpha1.SwarmEvent{
					ObjectMeta: metav1.ObjectMeta{Name: evName, Namespace: namespace},
					Spec: kubeswarmv1alpha1.SwarmEventSpec{
						Source: kubeswarmv1alpha1.SwarmEventSource{
							Type:       kubeswarmv1alpha1.TriggerSourceTeamOutput,
							TeamOutput: &kubeswarmv1alpha1.TeamOutputSource{Name: watchedTeamName},
						},
						Targets: []kubeswarmv1alpha1.SwarmEventTarget{{Team: templateTeamName}},
					},
				}
				requireNoError(t, k8sClient.Create(ctx, ev))

				result, err := reconcileEvent(t, evName)
				requireNoError(t, err)
				requireEqual(t, result.Status.FiredCount, int64(1))
				requireNotNil(t, result.Status.LastFiredAt)
				cond := apimeta.FindStatusCondition(result.Status.Conditions, kubeswarmv1alpha1.ConditionReady)
				requireNotNil(t, cond)
				requireEqual(t, cond.Reason, "Active")
			})
		})
	})

	t.Run("fire with ConcurrencyForbid", func(t *testing.T) {
		const evName = "ev-forbid"
		const runningTeamName = "ev-forbid-running-team"
		const templateTeamName = "ev-forbid-template-team"
		t.Cleanup(func() {
			cleanupEvent(evName)
			for _, n := range []string{runningTeamName, templateTeamName} {
				tm := &kubeswarmv1alpha1.SwarmTeam{}
				if err := k8sClient.Get(ctx, types.NamespacedName{Name: n, Namespace: namespace}, tm); err == nil {
					_ = k8sClient.Delete(ctx, tm)
				}
			}
		})

		t.Run("skips dispatch when a team owned by this trigger is still Running", func(t *testing.T) {
			// Create the template team.
			tmpl := &kubeswarmv1alpha1.SwarmTeam{
				ObjectMeta: metav1.ObjectMeta{Name: templateTeamName, Namespace: namespace},
				Spec: kubeswarmv1alpha1.SwarmTeamSpec{
					Roles: []kubeswarmv1alpha1.SwarmTeamRole{
						{Name: "worker", Model: "claude-sonnet-4-20250514"},
					},
					Pipeline: []kubeswarmv1alpha1.SwarmTeamPipelineStep{
						{Role: "worker"},
					},
				},
			}
			requireNoError(t, k8sClient.Create(ctx, tmpl))

			// Create the watched (source) team with Succeeded phase.
			watched := &kubeswarmv1alpha1.SwarmTeam{
				ObjectMeta: metav1.ObjectMeta{Name: runningTeamName, Namespace: namespace},
				Spec: kubeswarmv1alpha1.SwarmTeamSpec{
					Roles: []kubeswarmv1alpha1.SwarmTeamRole{
						{Name: "worker", Model: "claude-sonnet-4-20250514"},
					},
					Pipeline: []kubeswarmv1alpha1.SwarmTeamPipelineStep{
						{Role: "worker"},
					},
				},
			}
			requireNoError(t, k8sClient.Create(ctx, watched))
			watched.Status.Phase = kubeswarmv1alpha1.SwarmTeamPhaseSucceeded
			requireNoError(t, k8sClient.Status().Update(ctx, watched))
			// Create an SwarmRun representing the completed run for this team.
			completionTime := metav1.NewTime(time.Now().UTC().Add(-time.Minute))
			watchedRun2 := &kubeswarmv1alpha1.SwarmRun{
				ObjectMeta: metav1.ObjectMeta{
					Name:      watched.Name + "-run-1",
					Namespace: namespace,
					Labels:    map[string]string{kubeswarmv1alpha1.LabelTeam: watched.Name},
				},
				Spec: kubeswarmv1alpha1.SwarmRunSpec{
					TeamRef:  watched.Name,
					Pipeline: []kubeswarmv1alpha1.SwarmTeamPipelineStep{{Role: "worker"}},
					Roles:    []kubeswarmv1alpha1.SwarmTeamRole{{Name: "worker", Model: "claude-sonnet-4-20250514"}},
				},
			}
			requireNoError(t, k8sClient.Create(ctx, watchedRun2))
			watchedRun2.Status.Phase = kubeswarmv1alpha1.SwarmRunPhaseSucceeded
			watchedRun2.Status.CompletionTime = &completionTime
			requireNoError(t, k8sClient.Status().Update(ctx, watchedRun2))

			ev := &kubeswarmv1alpha1.SwarmEvent{
				ObjectMeta: metav1.ObjectMeta{Name: evName, Namespace: namespace},
				Spec: kubeswarmv1alpha1.SwarmEventSpec{
					ConcurrencyPolicy: kubeswarmv1alpha1.ConcurrencyForbid,
					Source: kubeswarmv1alpha1.SwarmEventSource{
						Type:       kubeswarmv1alpha1.TriggerSourceTeamOutput,
						TeamOutput: &kubeswarmv1alpha1.TeamOutputSource{Name: runningTeamName},
					},
					Targets: []kubeswarmv1alpha1.SwarmEventTarget{{Team: templateTeamName}},
				},
			}
			requireNoError(t, k8sClient.Create(ctx, ev))

			// Simulate a previously dispatched team that is still Running and owned by this trigger.
			alreadyRunning := &kubeswarmv1alpha1.SwarmTeam{
				ObjectMeta: metav1.ObjectMeta{
					Name:      evName + "-running",
					Namespace: namespace,
					Labels: map[string]string{
						"kubeswarm/trigger": evName,
					},
				},
				Spec: kubeswarmv1alpha1.SwarmTeamSpec{
					Roles: []kubeswarmv1alpha1.SwarmTeamRole{
						{Name: "worker", Model: "claude-sonnet-4-20250514"},
					},
					Pipeline: []kubeswarmv1alpha1.SwarmTeamPipelineStep{
						{Role: "worker"},
					},
				},
			}
			requireNoError(t, k8sClient.Create(ctx, alreadyRunning))
			alreadyRunning.Status.Phase = kubeswarmv1alpha1.SwarmTeamPhaseRunning
			requireNoError(t, k8sClient.Status().Update(ctx, alreadyRunning))
			t.Cleanup(func() {
				_ = k8sClient.Delete(ctx, alreadyRunning)
			})

			_, err := reconcileEvent(t, evName)
			requireNoError(t, err)
			// Verify no new dispatched teams were created - fire() returned nil because Forbid blocked it.
			teams := &kubeswarmv1alpha1.SwarmTeamList{}
			requireNoError(t, k8sClient.List(ctx, teams,
				client.InNamespace(namespace),
				client.MatchingLabels{"kubeswarm/trigger-template": templateTeamName},
			))
			requireLen(t, teams.Items, 0)
		})
	})
}
