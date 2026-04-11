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
	"encoding/json"
)

const (
	// DefaultMaxDetailBytes is the default maximum size for detail.input and
	// detail.output fields before truncation is applied.
	DefaultMaxDetailBytes = 8192

	truncatedSuffix = "[truncated]"
)

// Truncator enforces a maximum byte size on detail["input"] and detail["output"]
// fields. When a field exceeds the limit, it is replaced with a truncated
// string representation plus a "[truncated]" suffix.
type Truncator struct {
	maxBytes int
}

// NewTruncator creates a Truncator with the given maximum byte size.
// If maxBytes is <= 0, truncation is disabled.
func NewTruncator(maxBytes int) *Truncator {
	return &Truncator{maxBytes: maxBytes}
}

// Truncate checks detail["input"] and detail["output"] fields. If either
// exceeds maxBytes when serialized, it is replaced with a truncated string
// plus "[truncated]". Returns the (possibly modified) detail map and true
// if any truncation occurred.
func (t *Truncator) Truncate(detail map[string]any) (map[string]any, bool) {
	if t.maxBytes <= 0 || detail == nil {
		return detail, false
	}

	truncated := false

	for _, field := range []string{"input", "output"} {
		val, ok := detail[field]
		if !ok {
			continue
		}

		// Get the string representation to check length.
		var s string
		switch v := val.(type) {
		case string:
			s = v
		default:
			// For non-string values (maps, slices, etc.), serialize to JSON.
			data, err := json.Marshal(v)
			if err != nil {
				continue
			}
			s = string(data)
		}

		if len(s) > t.maxBytes {
			detail[field] = s[:t.maxBytes-len(truncatedSuffix)] + truncatedSuffix
			truncated = true
		}
	}

	return detail, truncated
}
