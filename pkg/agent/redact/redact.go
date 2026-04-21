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

// Package redact provides string-level PII and secret scrubbing for log output.
// It applies regex-based pattern matching to replace sensitive data with [REDACTED].
package redact

import (
	"regexp"
	"strings"
)

// Placeholder is the replacement string for redacted content.
const Placeholder = "[REDACTED]"

// piiPatterns matches common PII: email addresses, IPv4 addresses, phone numbers.
var piiPatterns = []*regexp.Regexp{
	// Email addresses.
	regexp.MustCompile(`[a-zA-Z0-9._%+\-]+@[a-zA-Z0-9.\-]+\.[a-zA-Z]{2,}`),
	// IPv4 addresses (not matching 0.0.0.0 or localhost ranges used in config).
	regexp.MustCompile(`\b(?:[1-9]\d?|1\d{2}|2[0-4]\d|25[0-5])\.(?:\d{1,3}\.){2}\d{1,3}\b`),
	// Phone numbers: +1-234-567-8901, (234) 567-8901, 234.567.8901, etc.
	regexp.MustCompile(`(?:\+\d{1,3}[\s\-]?)?\(?\d{3}\)?[\s.\-]?\d{3}[\s.\-]?\d{4}\b`),
}

// secretPatterns matches common API key and token formats.
var secretPatterns = []*regexp.Regexp{
	// sk-* keys (OpenAI, OpenRouter, Anthropic).
	regexp.MustCompile(`\bsk-[a-zA-Z0-9\-_]{10,}\b`),
	// key-* and token-* prefixed secrets.
	regexp.MustCompile(`\b(?:key|token)-[a-zA-Z0-9\-_]{10,}\b`),
	// Bearer tokens in header-like contexts.
	regexp.MustCompile(`(?i)\bBearer\s+[a-zA-Z0-9\-_.~+/]{20,}\b`),
	// AWS-style access key IDs.
	regexp.MustCompile(`\bAKIA[A-Z0-9]{16}\b`),
	// Generic long hex/base64 secrets (40+ chars, heuristic).
	regexp.MustCompile(`\b[a-fA-F0-9]{40,}\b`),
}

// Redactor scrubs sensitive data from strings.
// Zero value is safe to use (no-op).
type Redactor struct {
	redactPII     bool
	redactSecrets bool
}

// New creates a Redactor with the given options.
// When both options are false, Apply is a no-op.
func New(redactPII, redactSecrets bool) *Redactor {
	return &Redactor{
		redactPII:     redactPII,
		redactSecrets: redactSecrets,
	}
}

// Active returns true when the redactor will modify strings.
func (r *Redactor) Active() bool {
	if r == nil {
		return false
	}
	return r.redactPII || r.redactSecrets
}

// Apply scrubs sensitive data from s and returns the cleaned string.
// Nil receiver is safe (returns s unchanged).
func (r *Redactor) Apply(s string) string {
	if r == nil || (!r.redactPII && !r.redactSecrets) {
		return s
	}
	if r.redactSecrets {
		for _, re := range secretPatterns {
			s = re.ReplaceAllString(s, Placeholder)
		}
	}
	if r.redactPII {
		for _, re := range piiPatterns {
			s = re.ReplaceAllString(s, Placeholder)
		}
	}
	return s
}

// ContainsPII returns true if the string matches any PII pattern.
func ContainsPII(s string) bool {
	for _, re := range piiPatterns {
		if re.MatchString(s) {
			return true
		}
	}
	return false
}

// ContainsSecret returns true if the string matches any secret pattern.
func ContainsSecret(s string) bool {
	for _, re := range secretPatterns {
		if re.MatchString(s) {
			return true
		}
	}
	return false
}

// MaskMiddle replaces the middle portion of s with ***, keeping the first and
// last few characters visible. Useful for displaying partial keys in logs.
func MaskMiddle(s string) string {
	if len(s) <= 8 {
		return strings.Repeat("*", len(s))
	}
	return s[:4] + "***" + s[len(s)-4:]
}
