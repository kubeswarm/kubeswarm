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
	"strings"
	"testing"
	"unicode/utf8"
)

// TestSandbox_StoreAndRecall verifies that a large result is stored and can be
// recalled by its returned ID, returning the exact original content.
func TestSandbox_StoreAndRecall(t *testing.T) {
	s := newSandbox(100, 50, 100000)
	if s == nil {
		t.Fatal("newSandbox returned nil")
	}

	largeResult := strings.Repeat("abcdefghij", 200) // 2000 bytes, well over threshold
	id, digest := s.Store("my_tool", largeResult)
	if id == "" {
		t.Fatal("Store() returned empty id for result exceeding threshold")
	}
	if digest == "" {
		t.Fatal("Store() returned empty digest for sandboxed result")
	}

	recalled, ok := s.Recall(id)
	if !ok {
		t.Fatalf("Recall(%q) returned false", id)
	}
	if recalled != largeResult {
		t.Errorf("Recall returned %d bytes, want %d bytes", len(recalled), len(largeResult))
	}
}

// TestSandbox_SmallResultPassesThrough verifies that a result under the
// threshold is not sandboxed - Store returns empty id and empty digest.
func TestSandbox_SmallResultPassesThrough(t *testing.T) {
	s := newSandbox(1024, 50, 100000)

	smallResult := "hello world" // well under 1024 bytes
	id, digest := s.Store("my_tool", smallResult)
	if id != "" {
		t.Errorf("Store() returned id %q for small result, want empty", id)
	}
	if digest != "" {
		t.Errorf("Store() returned non-empty digest for small result: %q", digest)
	}
}

// TestSandbox_ThresholdBoundary verifies that a result exactly at the threshold
// is NOT sandboxed, but one byte over IS sandboxed.
func TestSandbox_ThresholdBoundary(t *testing.T) {
	const threshold int32 = 100
	s := newSandbox(threshold, 50, 100000)

	// Exactly at threshold - should NOT be sandboxed.
	exactResult := strings.Repeat("x", int(threshold))
	id, _ := s.Store("tool_a", exactResult)
	if id != "" {
		t.Errorf("result exactly at threshold was sandboxed (id=%q), want pass-through", id)
	}

	// One byte over threshold - should be sandboxed.
	overResult := strings.Repeat("x", int(threshold)+1)
	id, digest := s.Store("tool_b", overResult)
	if id == "" {
		t.Error("result one byte over threshold was not sandboxed, want sandboxed")
	}
	if digest == "" {
		t.Error("digest is empty for sandboxed result")
	}
}

// TestSandbox_RecallInvalidID verifies that recalling a non-existent ID
// returns false and an empty string.
func TestSandbox_RecallInvalidID(t *testing.T) {
	s := newSandbox(100, 50, 100000)

	result, ok := s.Recall("result-999")
	if ok {
		t.Error("Recall() returned true for non-existent ID")
	}
	if result != "" {
		t.Errorf("Recall() returned non-empty result %q for non-existent ID", result)
	}
}

// TestSandbox_SequentialIDs verifies that multiple stores produce sequential
// IDs in the format result-1, result-2, etc.
func TestSandbox_SequentialIDs(t *testing.T) {
	s := newSandbox(10, 5, 100000) // low threshold so everything gets sandboxed

	largeResult := strings.Repeat("x", 20)
	expectedIDs := []string{"result-1", "result-2", "result-3"}

	for i, wantID := range expectedIDs {
		id, _ := s.Store("tool", largeResult)
		if id != wantID {
			t.Errorf("store #%d: id = %q, want %q", i+1, id, wantID)
		}
	}
}

// TestSandbox_MaxTotalBytesExceeded verifies that when total stored bytes
// exceed the cap, new results pass through (fail-open) instead of being
// sandboxed.
func TestSandbox_MaxTotalBytesExceeded(t *testing.T) {
	const threshold int32 = 10
	const maxTotal int32 = 50
	s := newSandbox(threshold, 5, maxTotal)

	// Store enough to exceed maxTotalBytes.
	big := strings.Repeat("x", 30) // 30 bytes each; two fills 60 > 50
	id1, _ := s.Store("tool", big)
	if id1 == "" {
		t.Fatal("first store should succeed (30 bytes < 50 cap)")
	}

	id2, _ := s.Store("tool", big)
	if id2 == "" {
		t.Fatal("second store should succeed (total 60 bytes, check happens before or after)")
	}

	// At this point we have stored 60 bytes, exceeding the 50-byte cap.
	// The next store should fail-open: pass through without sandboxing.
	id3, digest3 := s.Store("tool", big)
	if id3 != "" {
		t.Errorf("third store should pass through (fail-open) after exceeding cap, got id=%q", id3)
	}
	if digest3 != "" {
		t.Errorf("third store should return empty digest when cap exceeded, got %q", digest3)
	}
}

// TestBuildDigest_Format verifies the digest contains the sandbox ID, tool name,
// result size, a "(truncated)" marker for the preview, and sandbox_recall instruction.
func TestBuildDigest_Format(t *testing.T) {
	result := strings.Repeat("abcdef", 100) // 600 bytes
	digest := buildDigest("result-1", "file_read", result, 50)

	checks := []struct {
		label    string
		contains string
	}{
		{"sandbox ID", "result-1"},
		{"tool name", "file_read"},
		{"truncated marker", "(truncated)"},
		{"recall instruction", "sandbox_recall"},
	}
	for _, c := range checks {
		if !strings.Contains(digest, c.contains) {
			t.Errorf("digest missing %s (%q):\n%s", c.label, c.contains, digest)
		}
	}

	// Should contain size information.
	if !strings.Contains(digest, "600") {
		t.Errorf("digest missing result size (600):\n%s", digest)
	}
}

// TestBuildDigest_PreviewTruncatesAtUTF8Boundary verifies the preview does not
// split multi-byte UTF-8 characters. Uses a string with multi-byte runes near
// the preview boundary.
func TestBuildDigest_PreviewTruncatesAtUTF8Boundary(t *testing.T) {
	// Each rune is 3 bytes (CJK character). With previewBytes=10, we can fit
	// at most 3 full runes (9 bytes). The preview must not split the 4th rune.
	cjk := strings.Repeat("\u4e16", 20) // 60 bytes of 3-byte runes
	digest := buildDigest("result-1", "tool", cjk, 10)

	// Extract the preview from the digest. It should be valid UTF-8.
	if !utf8.ValidString(digest) {
		t.Error("digest contains invalid UTF-8, preview likely split a multi-byte character")
	}

	// The preview portion should not contain the replacement character,
	// which would indicate a truncation mid-rune.
	if strings.ContainsRune(digest, utf8.RuneError) {
		t.Error("digest contains RuneError, preview likely split a multi-byte character")
	}
}

// TestBuildDigest_TokenEstimate verifies the digest includes an approximate
// tokens-saved estimate.
func TestBuildDigest_TokenEstimate(t *testing.T) {
	// 4000 bytes at ~4 chars/token = ~1000 tokens.
	result := strings.Repeat("x", 4000)
	digest := buildDigest("result-1", "tool", result, 50)

	// Should mention "tokens" somewhere in the digest.
	if !strings.Contains(strings.ToLower(digest), "token") {
		t.Errorf("digest missing token estimate:\n%s", digest)
	}
}
