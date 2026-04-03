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
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sync"
)

// ToolCallDeduplicator tracks fingerprints of executed tool calls within a single task.
// The dedup set is task-local: create one per RunTask invocation and discard after.
// Fail-open: if fingerprinting panics (it won't, but defensively), the call proceeds.
// All methods are safe for concurrent use.
type ToolCallDeduplicator struct {
	mu   sync.Mutex
	seen map[string]struct{}
}

func newDeduplicator() *ToolCallDeduplicator {
	return &ToolCallDeduplicator{seen: make(map[string]struct{})}
}

// IsDuplicate returns true if this (toolName, input) combination was already
// executed in this task. If not seen before, it records the fingerprint and
// returns false so the call proceeds normally.
func (d *ToolCallDeduplicator) IsDuplicate(toolName string, input json.RawMessage) bool {
	fp := fingerprintCall(toolName, input)
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, ok := d.seen[fp]; ok {
		return true
	}
	d.seen[fp] = struct{}{}
	return false
}

// fingerprintCall hashes tool name + canonicalized JSON args.
// JSON is re-marshaled after unmarshaling to eliminate whitespace differences.
func fingerprintCall(toolName string, input json.RawMessage) string {
	h := sha256.New()
	h.Write([]byte(toolName))
	h.Write([]byte{0}) // separator

	// Canonicalize: unmarshal then re-marshal to normalize key order and whitespace.
	var v any
	if err := json.Unmarshal(input, &v); err == nil {
		if b, err := json.Marshal(v); err == nil {
			h.Write(b)
			return hex.EncodeToString(h.Sum(nil))
		}
	}
	// Fallback: hash raw bytes if JSON is malformed.
	h.Write(input)
	return hex.EncodeToString(h.Sum(nil))
}
