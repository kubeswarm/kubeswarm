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

package audit

import (
	"path"
)

// RedactedValue is the replacement string used when a field matches a redaction pattern.
const RedactedValue = "[REDACTED]"

// Redactor applies glob-based redaction to audit event detail fields.
// Patterns use dot-separated paths matched via Go's path.Match, where *
// matches exactly one path segment.
type Redactor struct {
	patterns []string
}

// NewRedactor creates a Redactor with the given glob patterns.
// Patterns are matched against dot-separated paths within the detail map.
// The patterns should include the "detail." prefix to match the RFC spec.
// If patterns is empty or nil, the Redactor is a no-op.
func NewRedactor(patterns []string) *Redactor {
	return &Redactor{patterns: patterns}
}

// Redact applies redaction to the given detail map in place and returns it.
// It walks the map recursively, building dot-separated paths prefixed with
// "detail." and checking each leaf against all patterns.
func (r *Redactor) Redact(detail map[string]any) map[string]any {
	if len(r.patterns) == 0 || detail == nil {
		return detail
	}
	redactMap(r.patterns, "detail", detail)
	return detail
}

// redactMap walks a map recursively, building dot-separated paths.
func redactMap(patterns []string, prefix string, m map[string]any) {
	for key, val := range m {
		fullPath := prefix + "." + key

		switch v := val.(type) {
		case map[string]any:
			if matchesAny(patterns, fullPath) {
				m[key] = RedactedValue
			} else {
				redactMap(patterns, fullPath, v)
			}
		default:
			if matchesAny(patterns, fullPath) {
				m[key] = RedactedValue
			}
		}
	}
}

// matchesAny returns true if the given dot-separated path matches any of the
// provided glob patterns using path.Match. Dots are converted to slashes so
// that path.Match treats each segment independently.
func matchesAny(patterns []string, dotPath string) bool {
	slashPath := dotToSlash(dotPath)
	for _, pattern := range patterns {
		slashPattern := dotToSlash(pattern)
		if matched, err := path.Match(slashPattern, slashPath); err == nil && matched {
			return true
		}
	}
	return false
}

// dotToSlash converts a dot-separated path to a slash-separated path.
func dotToSlash(s string) string {
	b := make([]byte, len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == '.' {
			b[i] = '/'
		} else {
			b[i] = s[i]
		}
	}
	return string(b)
}
