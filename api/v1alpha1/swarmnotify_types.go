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

// NotifyEvent is the type of event that triggers a notification.
// +kubebuilder:validation:Enum=TeamSucceeded;TeamFailed;TeamTimedOut;BudgetWarning;BudgetExceeded;DailyLimitReached;AgentDegraded
type NotifyEvent string

const (
	NotifyOnTeamSucceeded     NotifyEvent = "TeamSucceeded"
	NotifyOnTeamFailed        NotifyEvent = "TeamFailed"
	NotifyOnTeamTimedOut      NotifyEvent = "TeamTimedOut"
	NotifyOnBudgetWarning     NotifyEvent = "BudgetWarning"
	NotifyOnBudgetExceeded    NotifyEvent = "BudgetExceeded"
	NotifyOnDailyLimitReached NotifyEvent = "DailyLimitReached"
	NotifyOnAgentDegraded     NotifyEvent = "AgentDegraded"
)

// NotifyChannelType determines which channel implementation to use.
// +kubebuilder:validation:Enum=webhook;slack
type NotifyChannelType string

const (
	NotifyChannelWebhook NotifyChannelType = "webhook"
	NotifyChannelSlack   NotifyChannelType = "slack"
)

// WebhookHeader defines a single HTTP header added to outbound webhook requests.
type WebhookHeader struct {
	// Name is the HTTP header name.
	Name string `json:"name"`
	// Value is the literal header value.
	// +optional
	Value string `json:"value,omitempty"`
	// ValueFrom reads the header value from a Secret key.
	// +optional
	ValueFrom *corev1.SecretKeySelector `json:"valueFrom,omitempty"`
}

// WebhookChannelSpec configures a generic HTTP POST notification channel.
type WebhookChannelSpec struct {
	// URL is the webhook endpoint as a literal string.
	// +optional
	URL string `json:"url,omitempty"`
	// URLFrom reads the URL from a Secret key. Takes precedence over URL.
	// +optional
	URLFrom *corev1.SecretKeySelector `json:"urlFrom,omitempty"`
	// Method is the HTTP method. Defaults to POST.
	// +kubebuilder:default=POST
	// +kubebuilder:validation:Enum=GET;POST;PUT;PATCH
	Method string `json:"method,omitempty"`
	// Headers are additional HTTP headers included in every request.
	// +optional
	Headers []WebhookHeader `json:"headers,omitempty"`
}

// SlackChannelSpec configures a Slack incoming webhook notification channel.
type SlackChannelSpec struct {
	// WebhookURLFrom reads the Slack incoming webhook URL from a Secret key.
	WebhookURLFrom corev1.SecretKeySelector `json:"webhookURLFrom"`
}

// NotifyChannelSpec defines a single notification channel.
//
// +kubebuilder:validation:XValidation:rule="self.type == 'webhook' || !has(self.webhook)",message="webhook config can only be set when type is webhook"
// +kubebuilder:validation:XValidation:rule="self.type == 'slack' || !has(self.slack)",message="slack config can only be set when type is slack"
type NotifyChannelSpec struct {
	// Type determines the channel implementation.
	// +kubebuilder:validation:Enum=webhook;slack
	Type NotifyChannelType `json:"type"`
	// Webhook configures a generic HTTP POST channel. Required when type is "webhook".
	// +optional
	Webhook *WebhookChannelSpec `json:"webhook,omitempty"`
	// Slack configures a Slack incoming webhook channel. Required when type is "slack".
	// +optional
	Slack *SlackChannelSpec `json:"slack,omitempty"`
	// Template is an optional Go template for the message body.
	// Context is NotifyPayload. Overrides the channel's default format.
	// For "webhook": template output is the raw POST body.
	// For "slack": template output replaces the default Block Kit message text.
	// +optional
	Template string `json:"template,omitempty"`
}

// SwarmNotifySpec defines the desired state of SwarmNotify.
type SwarmNotifySpec struct {
	// On lists the events that trigger notifications.
	// If empty, all events fire.
	// +optional
	On []NotifyEvent `json:"on,omitempty"`

	// Channels lists the notification targets.
	// +kubebuilder:validation:MinItems=1
	Channels []NotifyChannelSpec `json:"channels"`

	// RateLimitSeconds is the minimum interval between notifications for the
	// same (team, event) pair. Default: 300. Set to 0 to disable rate limiting.
	// +kubebuilder:default=300
	// +kubebuilder:validation:Minimum=0
	// +optional
	RateLimitSeconds int `json:"rateLimitSeconds,omitempty"`
}

// NotifyDispatchResult records the most recent dispatch attempt for one channel.
type NotifyDispatchResult struct {
	// ChannelIndex is the zero-based index of the channel in spec.channels.
	ChannelIndex int `json:"channelIndex"`
	// LastFiredAt is when the most recent dispatch attempt was made.
	// +optional
	LastFiredAt *metav1.Time `json:"lastFiredAt,omitempty"`
	// LastEvent is the event type that triggered the most recent dispatch.
	// +optional
	LastEvent NotifyEvent `json:"lastEvent,omitempty"`
	// Succeeded is false when all retry attempts failed.
	Succeeded bool `json:"succeeded"`
	// Error is the last error message when Succeeded is false.
	// +optional
	Error string `json:"error,omitempty"`
}

// SwarmNotifyStatus defines the observed state of SwarmNotify.
type SwarmNotifyStatus struct {
	// ChannelCount is the number of configured notification channels.
	ChannelCount int `json:"channelCount,omitempty"`
	// LastDispatches records the most recent dispatch result per channel index.
	// +optional
	LastDispatches []NotifyDispatchResult `json:"lastDispatches,omitempty"`
	// ObservedGeneration is the .metadata.generation this status reflects.
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// Conditions reflect the current state of the SwarmNotify.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Channels",type=integer,JSONPath=`.status.channelCount`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
// +kubebuilder:resource:scope=Namespaced,shortName={swnfy,swnfys},categories=kubeswarm

// SwarmNotify defines a reusable notification policy that routes run events to
// one or more channels (Slack, generic webhook, etc.).
type SwarmNotify struct {
	metav1.TypeMeta `json:",inline"`

	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// +required
	Spec SwarmNotifySpec `json:"spec"`

	// +optional
	Status SwarmNotifyStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// SwarmNotifyList contains a list of SwarmNotify.
type SwarmNotifyList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []SwarmNotify `json:"items"`
}

func init() {
	SchemeBuilder.Register(&SwarmNotify{}, &SwarmNotifyList{})
}
