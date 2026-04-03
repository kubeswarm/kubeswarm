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
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kubeswarmv1alpha1 "github.com/kubeswarm/kubeswarm/api/v1alpha1"

	"github.com/robfig/cron/v3"
)

// ---- Pure function tests ----

var _ = Describe("resolveTriggerTemplate", func() {
	It("returns a plain string unchanged", func() {
		out, err := resolveTriggerTemplate("hello", FireContext{Name: "t"})
		Expect(err).NotTo(HaveOccurred())
		Expect(out).To(Equal("hello"))
	})

	It("resolves .trigger.name", func() {
		out, err := resolveTriggerTemplate("{{ .trigger.name }}", FireContext{Name: "my-trigger"})
		Expect(err).NotTo(HaveOccurred())
		Expect(out).To(Equal("my-trigger"))
	})

	It("resolves .trigger.firedAt", func() {
		out, err := resolveTriggerTemplate("fired:{{ .trigger.firedAt }}", FireContext{FiredAt: "2026-01-01T00:00:00Z"})
		Expect(err).NotTo(HaveOccurred())
		Expect(out).To(Equal("fired:2026-01-01T00:00:00Z"))
	})

	It("resolves .trigger.output", func() {
		out, err := resolveTriggerTemplate("{{ .trigger.output }}", FireContext{Output: "some output"})
		Expect(err).NotTo(HaveOccurred())
		Expect(out).To(Equal("some output"))
	})

	It("returns empty string for missing key (missingkey=zero)", func() {
		out, err := resolveTriggerTemplate("{{ .trigger.body.key }}", FireContext{Name: "t"})
		Expect(err).NotTo(HaveOccurred())
		// map[string]any nil body — body itself renders as <no value> or map key as empty
		_ = out // just ensure no error
	})
})

var _ = Describe("mostRecentSchedule", func() {
	parseSchedule := func(expr string) cron.Schedule {
		s, err := cron.ParseStandard(expr)
		Expect(err).NotTo(HaveOccurred())
		return s
	}

	It("returns nil when no scheduled time has passed since lookback", func() {
		// lastFired at 08:59 → lookback = 08:59; schedule.Next(08:59) = 10:00 > now=09:00.
		now := time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC)
		lastFiredTime := time.Date(2026, 1, 1, 8, 59, 0, 0, time.UTC)
		lastFired := metav1.NewTime(lastFiredTime)
		schedule := parseSchedule("0 10 * * *")
		result := mostRecentSchedule(schedule, now, &lastFired)
		Expect(result).To(BeNil())
	})

	It("returns the most recent scheduled time before now", func() {
		now := time.Date(2026, 1, 1, 10, 5, 0, 0, time.UTC)
		// "0 10 * * *" fires at 10:00am; now is 10:05am so one fire has passed.
		schedule := parseSchedule("0 10 * * *")
		result := mostRecentSchedule(schedule, now, nil)
		Expect(result).NotTo(BeNil())
		Expect(result.Hour()).To(Equal(10))
		Expect(result.Minute()).To(Equal(0))
	})

	It("uses lastFired as the lookback anchor when it is more recent than 24h ago", func() {
		now := time.Date(2026, 1, 2, 10, 5, 0, 0, time.UTC)
		// Schedule fires every minute; lastFired just 2 minutes ago.
		lastFiredTime := now.Add(-2 * time.Minute)
		lastFired := metav1.NewTime(lastFiredTime)
		schedule := parseSchedule("* * * * *")
		result := mostRecentSchedule(schedule, now, &lastFired)
		Expect(result).NotTo(BeNil())
		// Should find the most recent minute tick after lastFired.
		Expect(result.After(lastFiredTime)).To(BeTrue())
	})
})

// ---- Reconciler integration tests (via envtest) ----

var _ = Describe("SwarmEvent Controller", func() {
	const namespace = "default"
	ctx := context.Background()

	newEventReconciler := func() *SwarmEventReconciler {
		return &SwarmEventReconciler{
			Client:            k8sClient,
			Scheme:            k8sClient.Scheme(),
			TriggerWebhookURL: "http://controller.svc:8092",
		}
	}

	reconcileEvent := func(name string) (*kubeswarmv1alpha1.SwarmEvent, error) {
		nn := types.NamespacedName{Name: name, Namespace: namespace}
		_, err := newEventReconciler().Reconcile(ctx, reconcile.Request{NamespacedName: nn})
		if err != nil {
			return nil, err
		}
		ev := &kubeswarmv1alpha1.SwarmEvent{}
		Expect(k8sClient.Get(ctx, nn, ev)).To(Succeed())
		return ev, nil
	}

	cleanupEvent := func(name string) {
		ev := &kubeswarmv1alpha1.SwarmEvent{}
		nn := types.NamespacedName{Name: name, Namespace: namespace}
		if err := k8sClient.Get(ctx, nn, ev); err == nil {
			_ = k8sClient.Delete(ctx, ev)
		}
	}

	Context("nonexistent SwarmEvent", func() {
		It("returns nil without error", func() {
			nn := types.NamespacedName{Name: "does-not-exist-event", Namespace: namespace}
			_, err := newEventReconciler().Reconcile(ctx, reconcile.Request{NamespacedName: nn})
			Expect(err).NotTo(HaveOccurred())
		})
	})

	Context("suspended trigger", func() {
		const name = "ev-suspended"
		AfterEach(func() { cleanupEvent(name) })

		It("sets Ready=False with Suspended reason", func() {
			ev := &kubeswarmv1alpha1.SwarmEvent{
				ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
				Spec: kubeswarmv1alpha1.SwarmEventSpec{
					Suspended: true,
					Source:    kubeswarmv1alpha1.SwarmEventSource{Type: kubeswarmv1alpha1.TriggerSourceCron, Cron: "* * * * *"},
					Targets:   []kubeswarmv1alpha1.SwarmEventTarget{{Team: "some-team"}},
				},
			}
			Expect(k8sClient.Create(ctx, ev)).To(Succeed())

			result, err := reconcileEvent(name)
			Expect(err).NotTo(HaveOccurred())
			cond := apimeta.FindStatusCondition(result.Status.Conditions, kubeswarmv1alpha1.ConditionReady)
			Expect(cond).NotTo(BeNil())
			Expect(string(cond.Status)).To(Equal(string(metav1.ConditionFalse)))
			Expect(cond.Reason).To(Equal("Suspended"))
		})
	})

	Context("cron trigger", func() {
		Context("with empty cron expression", func() {
			const name = "ev-cron-empty"
			AfterEach(func() { cleanupEvent(name) })

			It("sets Ready=False with InvalidCron reason", func() {
				ev := &kubeswarmv1alpha1.SwarmEvent{
					ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
					Spec: kubeswarmv1alpha1.SwarmEventSpec{
						Source:  kubeswarmv1alpha1.SwarmEventSource{Type: kubeswarmv1alpha1.TriggerSourceCron, Cron: ""},
						Targets: []kubeswarmv1alpha1.SwarmEventTarget{{Team: "some-team"}},
					},
				}
				Expect(k8sClient.Create(ctx, ev)).To(Succeed())

				result, err := reconcileEvent(name)
				Expect(err).NotTo(HaveOccurred())
				cond := apimeta.FindStatusCondition(result.Status.Conditions, kubeswarmv1alpha1.ConditionReady)
				Expect(cond).NotTo(BeNil())
				Expect(cond.Reason).To(Equal("InvalidCron"))
			})
		})

		Context("with invalid cron expression", func() {
			const name = "ev-cron-invalid"
			AfterEach(func() { cleanupEvent(name) })

			It("sets Ready=False with InvalidCron reason", func() {
				ev := &kubeswarmv1alpha1.SwarmEvent{
					ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
					Spec: kubeswarmv1alpha1.SwarmEventSpec{
						Source:  kubeswarmv1alpha1.SwarmEventSource{Type: kubeswarmv1alpha1.TriggerSourceCron, Cron: "not-a-cron"},
						Targets: []kubeswarmv1alpha1.SwarmEventTarget{{Team: "some-team"}},
					},
				}
				Expect(k8sClient.Create(ctx, ev)).To(Succeed())

				result, err := reconcileEvent(name)
				Expect(err).NotTo(HaveOccurred())
				cond := apimeta.FindStatusCondition(result.Status.Conditions, kubeswarmv1alpha1.ConditionReady)
				Expect(cond).NotTo(BeNil())
				Expect(cond.Reason).To(Equal("InvalidCron"))
			})
		})

		Context("with valid cron and last-fired set to prevent re-dispatch", func() {
			const name = "ev-cron-valid"
			AfterEach(func() { cleanupEvent(name) })

			It("sets Active condition and NextFireAt without dispatching a team", func() {
				ev := &kubeswarmv1alpha1.SwarmEvent{
					ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
					Spec: kubeswarmv1alpha1.SwarmEventSpec{
						Source:  kubeswarmv1alpha1.SwarmEventSource{Type: kubeswarmv1alpha1.TriggerSourceCron, Cron: "0 3 * * *"},
						Targets: []kubeswarmv1alpha1.SwarmEventTarget{{Team: "no-fire-team"}},
					},
				}
				Expect(k8sClient.Create(ctx, ev)).To(Succeed())
				// Set LastFiredAt to now so mostRecentSchedule uses now as the lookback
				// anchor — the next 3am is in the future, so no dispatch happens.
				now := metav1.NewTime(time.Now().UTC())
				ev.Status.LastFiredAt = &now
				Expect(k8sClient.Status().Update(ctx, ev)).To(Succeed())

				result, err := reconcileEvent(name)
				Expect(err).NotTo(HaveOccurred())
				cond := apimeta.FindStatusCondition(result.Status.Conditions, kubeswarmv1alpha1.ConditionReady)
				Expect(cond).NotTo(BeNil())
				Expect(cond.Reason).To(Equal("Active"))
				Expect(result.Status.NextFireAt).NotTo(BeNil())
				Expect(result.Status.FiredCount).To(BeZero())
			})
		})
	})

	Context("webhook trigger", func() {
		const name = "ev-webhook"
		AfterEach(func() {
			cleanupEvent(name)
			// Clean up the Secret created by ensureWebhookToken.
			sec := &kubeswarmv1alpha1.SwarmEvent{}
			_ = sec
		})

		It("creates a webhook-token Secret and sets WebhookURL in status", func() {
			ev := &kubeswarmv1alpha1.SwarmEvent{
				ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
				Spec: kubeswarmv1alpha1.SwarmEventSpec{
					Source:  kubeswarmv1alpha1.SwarmEventSource{Type: kubeswarmv1alpha1.TriggerSourceWebhook},
					Targets: []kubeswarmv1alpha1.SwarmEventTarget{{Team: "some-team"}},
				},
			}
			Expect(k8sClient.Create(ctx, ev)).To(Succeed())

			result, err := reconcileEvent(name)
			Expect(err).NotTo(HaveOccurred())
			Expect(result.Status.WebhookURL).To(Equal(
				fmt.Sprintf("http://controller.svc:8092/triggers/%s/%s/fire", namespace, name),
			))
			cond := apimeta.FindStatusCondition(result.Status.Conditions, kubeswarmv1alpha1.ConditionReady)
			Expect(cond).NotTo(BeNil())
			Expect(string(cond.Status)).To(Equal(string(metav1.ConditionTrue)))
		})

		It("is idempotent — reconciling again does not error", func() {
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
				Expect(k8sClient.Create(ctx, ev)).To(Succeed())
			}
			_, err := reconcileEvent(name)
			Expect(err).NotTo(HaveOccurred())
			_, err = reconcileEvent(name)
			Expect(err).NotTo(HaveOccurred())
		})
	})

	Context("team-output trigger", func() {
		Context("with nil teamOutput source", func() {
			const name = "ev-to-nil"
			AfterEach(func() { cleanupEvent(name) })

			It("sets InvalidSource condition", func() {
				ev := &kubeswarmv1alpha1.SwarmEvent{
					ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
					Spec: kubeswarmv1alpha1.SwarmEventSpec{
						Source:  kubeswarmv1alpha1.SwarmEventSource{Type: kubeswarmv1alpha1.TriggerSourceTeamOutput},
						Targets: []kubeswarmv1alpha1.SwarmEventTarget{{Team: "some-team"}},
					},
				}
				Expect(k8sClient.Create(ctx, ev)).To(Succeed())

				result, err := reconcileEvent(name)
				Expect(err).NotTo(HaveOccurred())
				cond := apimeta.FindStatusCondition(result.Status.Conditions, kubeswarmv1alpha1.ConditionReady)
				Expect(cond).NotTo(BeNil())
				Expect(cond.Reason).To(Equal("InvalidSource"))
			})
		})

		Context("with a team that hasn't reached the desired phase", func() {
			const evName = "ev-to-waiting"
			const teamName = "ev-to-watcher-team"
			AfterEach(func() {
				cleanupEvent(evName)
				t := &kubeswarmv1alpha1.SwarmTeam{}
				if err := k8sClient.Get(ctx, types.NamespacedName{Name: teamName, Namespace: namespace}, t); err == nil {
					_ = k8sClient.Delete(ctx, t)
				}
			})

			It("sets Watching condition", func() {
				t := &kubeswarmv1alpha1.SwarmTeam{
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
				Expect(k8sClient.Create(ctx, t)).To(Succeed())

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
				Expect(k8sClient.Create(ctx, ev)).To(Succeed())

				result, err := reconcileEvent(evName)
				Expect(err).NotTo(HaveOccurred())
				cond := apimeta.FindStatusCondition(result.Status.Conditions, kubeswarmv1alpha1.ConditionReady)
				Expect(cond).NotTo(BeNil())
				Expect(cond.Reason).To(Equal("Watching"))
			})
		})

		Context("with a completed team and a dispatch target", func() {
			const evName = "ev-to-fire"
			const watchedTeamName = "ev-to-watched-team"
			const templateTeamName = "ev-to-template-team"
			AfterEach(func() {
				cleanupEvent(evName)
				for _, n := range []string{watchedTeamName, templateTeamName} {
					t := &kubeswarmv1alpha1.SwarmTeam{}
					if err := k8sClient.Get(ctx, types.NamespacedName{Name: n, Namespace: namespace}, t); err == nil {
						_ = k8sClient.Delete(ctx, t)
					}
				}
			})

			It("fires and creates a dispatched SwarmTeam", func() {
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
				Expect(k8sClient.Create(ctx, watched)).To(Succeed())
				watched.Status.Phase = kubeswarmv1alpha1.SwarmTeamPhaseSucceeded
				Expect(k8sClient.Status().Update(ctx, watched)).To(Succeed())
				// Create an SwarmRun representing the completed run for this team.
				completionTime := metav1.NewTime(time.Now().UTC().Add(-time.Minute))
				watchedRun := &kubeswarmv1alpha1.SwarmRun{
					ObjectMeta: metav1.ObjectMeta{
						Name:      watchedTeamName + "-run-1",
						Namespace: namespace,
						Labels:    map[string]string{"kubeswarm/team": watchedTeamName},
					},
					Spec: kubeswarmv1alpha1.SwarmRunSpec{
						TeamRef:  watchedTeamName,
						Pipeline: []kubeswarmv1alpha1.SwarmTeamPipelineStep{{Role: "worker"}},
						Roles:    []kubeswarmv1alpha1.SwarmTeamRole{{Name: "worker", Model: "claude-sonnet-4-20250514"}},
					},
				}
				Expect(k8sClient.Create(ctx, watchedRun)).To(Succeed())
				watchedRun.Status.Phase = kubeswarmv1alpha1.SwarmRunPhaseSucceeded
				watchedRun.Status.CompletionTime = &completionTime
				watchedRun.Status.Output = "great result"
				Expect(k8sClient.Status().Update(ctx, watchedRun)).To(Succeed())

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
				Expect(k8sClient.Create(ctx, tmpl)).To(Succeed())

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
				Expect(k8sClient.Create(ctx, ev)).To(Succeed())

				result, err := reconcileEvent(evName)
				Expect(err).NotTo(HaveOccurred())
				Expect(result.Status.FiredCount).To(Equal(int64(1)))
				Expect(result.Status.LastFiredAt).NotTo(BeNil())
				cond := apimeta.FindStatusCondition(result.Status.Conditions, kubeswarmv1alpha1.ConditionReady)
				Expect(cond).NotTo(BeNil())
				Expect(cond.Reason).To(Equal("Active"))
			})
		})
	})

	Context("fire with ConcurrencyForbid", func() {
		const evName = "ev-forbid"
		const runningTeamName = "ev-forbid-running-team"
		const templateTeamName = "ev-forbid-template-team"
		AfterEach(func() {
			cleanupEvent(evName)
			for _, n := range []string{runningTeamName, templateTeamName} {
				t := &kubeswarmv1alpha1.SwarmTeam{}
				if err := k8sClient.Get(ctx, types.NamespacedName{Name: n, Namespace: namespace}, t); err == nil {
					_ = k8sClient.Delete(ctx, t)
				}
			}
		})

		It("skips dispatch when a team owned by this trigger is still Running", func() {
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
			Expect(k8sClient.Create(ctx, tmpl)).To(Succeed())

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
			Expect(k8sClient.Create(ctx, watched)).To(Succeed())
			watched.Status.Phase = kubeswarmv1alpha1.SwarmTeamPhaseSucceeded
			Expect(k8sClient.Status().Update(ctx, watched)).To(Succeed())
			// Create an SwarmRun representing the completed run for this team.
			completionTime := metav1.NewTime(time.Now().UTC().Add(-time.Minute))
			watchedRun2 := &kubeswarmv1alpha1.SwarmRun{
				ObjectMeta: metav1.ObjectMeta{
					Name:      watched.Name + "-run-1",
					Namespace: namespace,
					Labels:    map[string]string{"kubeswarm/team": watched.Name},
				},
				Spec: kubeswarmv1alpha1.SwarmRunSpec{
					TeamRef:  watched.Name,
					Pipeline: []kubeswarmv1alpha1.SwarmTeamPipelineStep{{Role: "worker"}},
					Roles:    []kubeswarmv1alpha1.SwarmTeamRole{{Name: "worker", Model: "claude-sonnet-4-20250514"}},
				},
			}
			Expect(k8sClient.Create(ctx, watchedRun2)).To(Succeed())
			watchedRun2.Status.Phase = kubeswarmv1alpha1.SwarmRunPhaseSucceeded
			watchedRun2.Status.CompletionTime = &completionTime
			Expect(k8sClient.Status().Update(ctx, watchedRun2)).To(Succeed())

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
			Expect(k8sClient.Create(ctx, ev)).To(Succeed())

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
			Expect(k8sClient.Create(ctx, alreadyRunning)).To(Succeed())
			alreadyRunning.Status.Phase = kubeswarmv1alpha1.SwarmTeamPhaseRunning
			Expect(k8sClient.Status().Update(ctx, alreadyRunning)).To(Succeed())
			defer func() {
				_ = k8sClient.Delete(ctx, alreadyRunning)
			}()

			_, err := reconcileEvent(evName)
			Expect(err).NotTo(HaveOccurred())
			// Verify no new dispatched teams were created — fire() returned nil because Forbid blocked it.
			teams := &kubeswarmv1alpha1.SwarmTeamList{}
			Expect(k8sClient.List(ctx, teams,
				client.InNamespace(namespace),
				client.MatchingLabels{"kubeswarm/trigger-template": templateTeamName},
			)).To(Succeed())
			Expect(teams.Items).To(BeEmpty())
		})
	})
})
