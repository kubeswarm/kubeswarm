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

package runner

import (
	"fmt"
	"unicode/utf8"
)

// ToolResultSandbox stores large tool results in a per-task in-memory map
// and returns compact digests. The LLM retrieves full results via sandbox_recall.
type ToolResultSandbox struct {
	thresholdBytes int32
	previewBytes   int32
	maxTotalBytes  int32
	results        map[string]string
	seq            int
	totalBytes     int
}

// newSandbox creates a ToolResultSandbox with the given size thresholds.
func newSandbox(thresholdBytes, previewBytes, maxTotalBytes int32) *ToolResultSandbox {
	return &ToolResultSandbox{
		thresholdBytes: thresholdBytes,
		previewBytes:   previewBytes,
		maxTotalBytes:  maxTotalBytes,
		results:        make(map[string]string),
	}
}

// Store saves a large tool result and returns a compact digest.
// If the result is at or below the threshold, it is not sandboxed and ("", "") is returned.
// If total stored bytes would exceed maxTotalBytes, the result is not sandboxed (fail-open).
// All methods are nil-safe.
func (s *ToolResultSandbox) Store(toolName, result string) (id string, digest string) {
	if s == nil {
		return "", ""
	}
	if int32(len(result)) <= s.thresholdBytes {
		return "", ""
	}
	if s.totalBytes > int(s.maxTotalBytes) {
		return "", ""
	}
	s.seq++
	id = fmt.Sprintf("result-%d", s.seq)
	s.results[id] = result
	s.totalBytes += len(result)
	return id, buildDigest(id, toolName, result, s.previewBytes)
}

// Recall returns the stored result for the given ID, or false if not found.
// All methods are nil-safe.
func (s *ToolResultSandbox) Recall(id string) (string, bool) {
	if s == nil {
		return "", false
	}
	result, ok := s.results[id]
	return result, ok
}

// buildDigest creates a human-readable digest for a sandboxed result.
// The preview is truncated at a valid UTF-8 boundary within previewBytes.
func buildDigest(id, toolName, result string, previewBytes int32) string {
	preview := truncateUTF8(result, int(previewBytes))
	size := len(result)
	tokensSaved := size / 4

	return fmt.Sprintf("[sandboxed:%s]\ntool: %s\nsize: %s bytes (~%s tokens saved)\npreview (truncated): %s\nUse sandbox_recall(id=%q) to retrieve the full result.",
		id,
		toolName,
		formatCommas(size),
		formatCommas(tokensSaved),
		preview,
		id,
	)
}

// truncateUTF8 truncates s to at most maxBytes bytes without splitting
// a multi-byte UTF-8 character.
func truncateUTF8(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	// Walk backwards from maxBytes to find the last valid rune start.
	for maxBytes > 0 && !utf8.RuneStart(s[maxBytes]) {
		maxBytes--
	}
	return s[:maxBytes]
}

// formatCommas formats an integer with comma separators (e.g. 47832 -> "47,832").
func formatCommas(n int) string {
	if n < 0 {
		return "-" + formatCommas(-n)
	}
	s := fmt.Sprintf("%d", n)
	if len(s) <= 3 {
		return s
	}
	// Insert commas from right to left.
	var result []byte
	for i, ch := range s {
		remaining := len(s) - i
		if remaining%3 == 0 && i > 0 {
			result = append(result, ',')
		}
		result = append(result, byte(ch))
	}
	return string(result)
}
