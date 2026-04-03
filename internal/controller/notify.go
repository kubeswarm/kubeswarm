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
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"sync"
	"text/template"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kubeswarmv1alpha1 "github.com/kubeswarm/kubeswarm/api/v1alpha1"
	"github.com/kubeswarm/kubeswarm/pkg/costs"
)

// +kubebuilder:rbac:groups="",resources=secrets,verbs=get

// NotifyPayload is the JSON body sent to every notification channel.
type NotifyPayload struct {
	// Event is the notification trigger type.
	Event kubeswarmv1alpha1.NotifyEvent `json:"event"`
	// Team is the SwarmTeam name (or SwarmAgent name for AgentDegraded).
	Team      string `json:"team"`
	Namespace string `json:"namespace"`
	// Phase is the terminal phase of the run.
	Phase string `json:"phase"`
	// RunName is the SwarmRun name.
	RunName string `json:"runName,omitempty"`
	// FailedStep is the name of the first step that reached Failed phase.
	FailedStep string `json:"failedStep,omitempty"`
	// TotalTokens is the sum of tokens for the run.
	TotalTokens int64 `json:"totalTokens,omitempty"`
	// DurationMs is wall-clock time from StartTime to CompletionTime.
	DurationMs int64 `json:"durationMs,omitempty"`
	// Output is the resolved pipeline output, capped at 500 characters.
	Output string `json:"output,omitempty"`
	// Message is a human-readable summary line.
	Message string `json:"message"`
	// DashboardURL is a deep-link to the team in dashboard (optional).
	DashboardURL string `json:"dashboardURL,omitempty"`
}

// rateLimitKey uniquely identifies a (namespace, team, event) tuple for rate limiting.
type rateLimitKey struct {
	namespace string
	team      string
	event     kubeswarmv1alpha1.NotifyEvent
}

// notifyRateLimiter tracks the last fire time per (namespace, team, event) key.
type notifyRateLimiter struct {
	mu    sync.Mutex
	fired map[rateLimitKey]time.Time
}

func newNotifyRateLimiter() *notifyRateLimiter {
	return &notifyRateLimiter{fired: make(map[rateLimitKey]time.Time)}
}

// allow returns true when the event may fire (respects the configured window).
func (rl *notifyRateLimiter) allow(key rateLimitKey, windowSecs int) bool {
	if windowSecs == 0 {
		return true
	}
	rl.mu.Lock()
	defer rl.mu.Unlock()
	last, ok := rl.fired[key]
	if !ok {
		return true
	}
	return time.Since(last) >= time.Duration(windowSecs)*time.Second
}

func (rl *notifyRateLimiter) record(key rateLimitKey) {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	rl.fired[key] = time.Now()
}

// NotifyDispatcher dispatches SwarmNotify policies on behalf of controllers.
type NotifyDispatcher struct {
	client      client.Client
	httpClient  *http.Client
	rateLimiter *notifyRateLimiter
}

// NewNotifyDispatcher creates a dispatcher with a 10-second HTTP timeout.
func NewNotifyDispatcher(c client.Client) *NotifyDispatcher {
	return &NotifyDispatcher{
		client:      c,
		httpClient:  &http.Client{Timeout: 10 * time.Second},
		rateLimiter: newNotifyRateLimiter(),
	}
}

// DispatchRun fires notifications for a terminal SwarmRun phase transition.
// It looks up the parent SwarmTeam's notifyRef and dispatches to all matching channels.
// Best-effort: errors are logged but do not affect the reconcile result.
func (d *NotifyDispatcher) DispatchRun(ctx context.Context, run *kubeswarmv1alpha1.SwarmRun) {
	logger := ctrl.Log.WithName("notify").WithValues("run", run.Name, "namespace", run.Namespace)

	// Resolve the parent team.
	team := &kubeswarmv1alpha1.SwarmTeam{}
	if err := d.client.Get(ctx, types.NamespacedName{
		Name:      run.Spec.TeamRef,
		Namespace: run.Namespace,
	}, team); err != nil {
		logger.V(1).Info("could not fetch parent team for notification", "team", run.Spec.TeamRef)
		return
	}

	if team.Spec.NotifyRef == nil {
		return
	}

	event := runPhaseToEvent(run.Status.Phase)
	if event == "" {
		return
	}

	policy := &kubeswarmv1alpha1.SwarmNotify{}
	if err := d.client.Get(ctx, types.NamespacedName{
		Name:      team.Spec.NotifyRef.Name,
		Namespace: run.Namespace,
	}, policy); err != nil {
		logger.Error(err, "could not fetch SwarmNotify policy", "policy", team.Spec.NotifyRef.Name)
		return
	}

	if !policyMatchesEvent(policy, event) {
		return
	}

	key := rateLimitKey{namespace: run.Namespace, team: run.Spec.TeamRef, event: event}
	if !d.rateLimiter.allow(key, policy.Spec.RateLimitSeconds) {
		logger.V(1).Info("notification rate-limited", "event", event)
		return
	}
	d.rateLimiter.record(key)

	payload := buildRunPayload(run, event)
	d.dispatch(ctx, payload, policy)
}

// dispatch sends the payload to all configured channels in the policy.
func (d *NotifyDispatcher) dispatch(ctx context.Context, payload NotifyPayload, policy *kubeswarmv1alpha1.SwarmNotify) {
	logger := ctrl.Log.WithName("notify")
	for i, ch := range policy.Spec.Channels {
		go func(idx int, channel kubeswarmv1alpha1.NotifyChannelSpec) {
			var err error
			for attempt := range 3 {
				if attempt > 0 {
					time.Sleep(time.Duration(attempt*attempt) * time.Second)
				}
				switch channel.Type {
				case kubeswarmv1alpha1.NotifyChannelWebhook:
					err = d.dispatchWebhook(ctx, payload, channel)
				case kubeswarmv1alpha1.NotifyChannelSlack:
					err = d.dispatchSlack(ctx, payload, channel)
				default:
					err = fmt.Errorf("unknown channel type: %s", channel.Type)
				}
				if err == nil {
					logger.V(1).Info("notification dispatched", "channel", idx, "type", channel.Type, "event", payload.Event)
					d.updateDispatchStatus(ctx, policy, idx, payload.Event, true, "")
					return
				}
				logger.V(1).Info("dispatch attempt failed", "channel", idx, "attempt", attempt+1, "error", err)
			}
			logger.Error(err, "all notification dispatch attempts failed", "channel", idx)
			d.updateDispatchStatus(ctx, policy, idx, payload.Event, false, err.Error())
		}(i, ch)
	}
}

// dispatchWebhook sends the payload as a JSON POST to the configured URL.
func (d *NotifyDispatcher) dispatchWebhook(ctx context.Context, payload NotifyPayload, ch kubeswarmv1alpha1.NotifyChannelSpec) error {
	if ch.Webhook == nil {
		return fmt.Errorf("webhook channel has no webhook config")
	}

	// Resolve URL: prefer URLFrom over URL.
	targetURL := ch.Webhook.URL
	if targetURL == "" && ch.Webhook.URLFrom != nil {
		secret := &corev1.Secret{}
		ref := ch.Webhook.URLFrom
		if err := d.client.Get(ctx, types.NamespacedName{
			Name:      ref.Name,
			Namespace: payload.Namespace,
		}, secret); err != nil {
			return fmt.Errorf("fetching webhook URL secret %q: %w", ref.Name, err)
		}
		urlBytes, ok := secret.Data[ref.Key]
		if !ok {
			return fmt.Errorf("secret %q has no key %q", ref.Name, ref.Key)
		}
		targetURL = string(urlBytes)
	}
	if targetURL == "" {
		return fmt.Errorf("webhook channel has no URL configured")
	}

	var body []byte
	var err error
	if ch.Template != "" {
		rendered, terr := renderTemplate(ch.Template, payload)
		if terr != nil {
			return fmt.Errorf("rendering webhook template: %w", terr)
		}
		body = []byte(rendered)
	} else {
		body, err = json.Marshal(payload)
		if err != nil {
			return fmt.Errorf("marshaling payload: %w", err)
		}
	}

	method := ch.Webhook.Method
	if method == "" {
		method = http.MethodPost
	}

	req, err := http.NewRequestWithContext(ctx, method, targetURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	for _, h := range ch.Webhook.Headers {
		val := h.Value
		if val == "" && h.ValueFrom != nil {
			secret := &corev1.Secret{}
			if err := d.client.Get(ctx, types.NamespacedName{
				Name:      h.ValueFrom.Name,
				Namespace: payload.Namespace,
			}, secret); err == nil {
				if v, ok := secret.Data[h.ValueFrom.Key]; ok {
					val = string(v)
				}
			}
		}
		if val != "" {
			req.Header.Set(h.Name, val)
		}
	}

	resp, err := d.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("sending webhook: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode >= 400 {
		return fmt.Errorf("webhook returned %d", resp.StatusCode)
	}
	return nil
}

// dispatchSlack sends a Slack Block Kit message to the configured incoming webhook URL.
func (d *NotifyDispatcher) dispatchSlack(ctx context.Context, payload NotifyPayload, ch kubeswarmv1alpha1.NotifyChannelSpec) error {
	if ch.Slack == nil {
		return fmt.Errorf("slack channel has no slack config")
	}

	// Resolve the webhook URL from the Secret.
	secret := &corev1.Secret{}
	ref := ch.Slack.WebhookURLFrom
	if err := d.client.Get(ctx, types.NamespacedName{
		Name:      ref.Name,
		Namespace: payload.Namespace,
	}, secret); err != nil {
		return fmt.Errorf("fetching slack webhook secret %q: %w", ref.Name, err)
	}
	urlBytes, ok := secret.Data[ref.Key]
	if !ok {
		return fmt.Errorf("secret %q has no key %q", ref.Name, ref.Key)
	}
	webhookURL := string(urlBytes)

	var text string
	if ch.Template != "" {
		rendered, err := renderTemplate(ch.Template, payload)
		if err != nil {
			return fmt.Errorf("rendering slack template: %w", err)
		}
		text = rendered
	} else {
		text = defaultSlackText(payload)
	}

	slackBody := map[string]any{
		"text": text,
		"blocks": []map[string]any{
			{
				"type": "section",
				"text": map[string]any{
					"type": "mrkdwn",
					"text": text,
				},
			},
		},
	}
	body, err := json.Marshal(slackBody)
	if err != nil {
		return fmt.Errorf("marshaling slack body: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, webhookURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("creating slack request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := d.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("sending to slack: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode >= 400 {
		return fmt.Errorf("slack returned %d", resp.StatusCode)
	}
	return nil
}

// updateDispatchStatus patches the SwarmNotify status.lastDispatches entry for the given channel.
func (d *NotifyDispatcher) updateDispatchStatus(
	ctx context.Context,
	policy *kubeswarmv1alpha1.SwarmNotify,
	channelIdx int,
	event kubeswarmv1alpha1.NotifyEvent,
	succeeded bool,
	errMsg string,
) {
	now := metav1.Now()
	result := kubeswarmv1alpha1.NotifyDispatchResult{
		ChannelIndex: channelIdx,
		LastFiredAt:  &now,
		LastEvent:    event,
		Succeeded:    succeeded,
		Error:        errMsg,
	}

	// Fetch fresh copy to avoid conflicts.
	fresh := &kubeswarmv1alpha1.SwarmNotify{}
	if err := d.client.Get(ctx, types.NamespacedName{Name: policy.Name, Namespace: policy.Namespace}, fresh); err != nil {
		return
	}

	// Replace or append.
	found := false
	for i, r := range fresh.Status.LastDispatches {
		if r.ChannelIndex == channelIdx {
			fresh.Status.LastDispatches[i] = result
			found = true
			break
		}
	}
	if !found {
		fresh.Status.LastDispatches = append(fresh.Status.LastDispatches, result)
	}

	_ = d.client.Status().Update(ctx, fresh) //nolint:errcheck
}

// runPhaseToEvent maps an SwarmRun terminal phase to the corresponding NotifyEvent.
func runPhaseToEvent(phase kubeswarmv1alpha1.SwarmRunPhase) kubeswarmv1alpha1.NotifyEvent {
	switch phase {
	case kubeswarmv1alpha1.SwarmRunPhaseSucceeded:
		return kubeswarmv1alpha1.NotifyOnTeamSucceeded
	case kubeswarmv1alpha1.SwarmRunPhaseFailed:
		return kubeswarmv1alpha1.NotifyOnTeamFailed
	}
	return ""
}

// policyMatchesEvent returns true when the policy should fire for the given event.
func policyMatchesEvent(policy *kubeswarmv1alpha1.SwarmNotify, event kubeswarmv1alpha1.NotifyEvent) bool {
	if len(policy.Spec.On) == 0 {
		return true // empty On = fire for all events
	}
	return slices.Contains(policy.Spec.On, event)
}

// buildRunPayload constructs a NotifyPayload from a terminal SwarmRun.
func buildRunPayload(run *kubeswarmv1alpha1.SwarmRun, event kubeswarmv1alpha1.NotifyEvent) NotifyPayload {
	p := NotifyPayload{
		Event:     event,
		Team:      run.Spec.TeamRef,
		Namespace: run.Namespace,
		Phase:     string(run.Status.Phase),
		RunName:   run.Name,
	}

	if run.Status.TotalTokenUsage != nil {
		p.TotalTokens = run.Status.TotalTokenUsage.TotalTokens
	}
	if run.Status.StartTime != nil && run.Status.CompletionTime != nil {
		p.DurationMs = run.Status.CompletionTime.Time.Sub(run.Status.StartTime.Time).Milliseconds()
	}

	output := run.Status.Output
	if len(output) > 500 {
		output = output[:500] + "..."
	}
	p.Output = output

	// Find first failed step.
	for _, st := range run.Status.Steps {
		if st.Phase == kubeswarmv1alpha1.SwarmFlowStepPhaseFailed {
			p.FailedStep = st.Name
			break
		}
	}

	// Build human-readable message.
	switch event {
	case kubeswarmv1alpha1.NotifyOnTeamSucceeded:
		p.Message = fmt.Sprintf("Team %q/%q succeeded", run.Namespace, run.Spec.TeamRef)
	case kubeswarmv1alpha1.NotifyOnTeamFailed:
		if p.FailedStep != "" {
			p.Message = fmt.Sprintf("Team %q/%q failed at step %q", run.Namespace, run.Spec.TeamRef, p.FailedStep)
		} else {
			p.Message = fmt.Sprintf("Team %q/%q failed", run.Namespace, run.Spec.TeamRef)
		}
	default:
		p.Message = fmt.Sprintf("Team %q/%q: %s", run.Namespace, run.Spec.TeamRef, event)
	}

	return p
}

// defaultSlackText formats the default Slack message text for a payload.
func defaultSlackText(p NotifyPayload) string {
	emoji := eventEmoji(p.Event)
	lines := fmt.Sprintf("%s *%s*  `%s / %s`\n", emoji, p.Event, p.Namespace, p.Team)
	if p.DurationMs > 0 {
		lines += fmt.Sprintf("Duration: %dms", p.DurationMs)
	}
	if p.TotalTokens > 0 {
		lines += fmt.Sprintf("  ·  Tokens: %d", p.TotalTokens)
	}
	lines += "\n"
	if p.FailedStep != "" {
		lines += fmt.Sprintf("Failed step: `%s`\n", p.FailedStep)
	}
	if p.Output != "" {
		lines += p.Output
	}
	return lines
}

func eventEmoji(event kubeswarmv1alpha1.NotifyEvent) string {
	switch event {
	case kubeswarmv1alpha1.NotifyOnTeamSucceeded:
		return ":white_check_mark:"
	case kubeswarmv1alpha1.NotifyOnTeamFailed, kubeswarmv1alpha1.NotifyOnTeamTimedOut:
		return ":x:"
	case kubeswarmv1alpha1.NotifyOnBudgetExceeded, kubeswarmv1alpha1.NotifyOnDailyLimitReached:
		return ":money_with_wings:"
	case kubeswarmv1alpha1.NotifyOnBudgetWarning:
		return ":warning:"
	case kubeswarmv1alpha1.NotifyOnAgentDegraded:
		return ":warning:"
	}
	return ":bell:"
}

// DispatchTest fires a synthetic TeamFailed payload through all channels of the
// given SwarmNotify policy. Used by the dashboard Test button. Rate limiting is
// intentionally bypassed for test dispatches.
func (d *NotifyDispatcher) DispatchTest(ctx context.Context, policy *kubeswarmv1alpha1.SwarmNotify) {
	payload := NotifyPayload{
		Event:     kubeswarmv1alpha1.NotifyOnTeamFailed,
		Team:      "test-team",
		Namespace: policy.Namespace,
		Phase:     "Failed",
		RunName:   "test-run",
		Message:   fmt.Sprintf("Test notification from SwarmNotify %q/%q", policy.Namespace, policy.Name),
	}
	d.dispatch(ctx, payload, policy)
}

// DispatchBudget fires a budget notification for a phase transition on an SwarmBudget.
// Best-effort: errors are logged but do not affect the reconcile result.
func (d *NotifyDispatcher) DispatchBudget(
	ctx context.Context,
	budget *kubeswarmv1alpha1.SwarmBudget,
	decision costs.BudgetDecision,
	event kubeswarmv1alpha1.NotifyEvent,
	policy *kubeswarmv1alpha1.SwarmNotify,
) {
	if !policyMatchesEvent(policy, event) {
		return
	}

	key := rateLimitKey{namespace: budget.Namespace, team: budget.Name, event: event}
	if !d.rateLimiter.allow(key, policy.Spec.RateLimitSeconds) {
		return
	}
	d.rateLimiter.record(key)

	payload := NotifyPayload{
		Event:     event,
		Team:      budget.Spec.Selector.Team,
		Namespace: budget.Spec.Selector.Namespace,
		Phase:     string(budget.Status.Phase),
		Message:   decision.Message,
	}
	d.dispatch(ctx, payload, policy)
}

// renderTemplate renders a Go template with the payload as context.
func renderTemplate(tmplStr string, payload NotifyPayload) (string, error) {
	tmpl, err := template.New("notify").Parse(tmplStr)
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, payload); err != nil {
		return "", err
	}
	return buf.String(), nil
}
