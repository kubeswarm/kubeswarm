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

package redis

import (
	"encoding/json"
	"testing"
)

func TestResultEntry_ThinkingTokensRoundTrip(t *testing.T) {
	in := resultEntry{
		Result:         "ok",
		InputTokens:    100,
		OutputTokens:   200,
		ThinkingTokens: 321,
	}
	data, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out resultEntry
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.ThinkingTokens != 321 {
		t.Errorf("ThinkingTokens = %d, want 321", out.ThinkingTokens)
	}
	if out.InputTokens != 100 || out.OutputTokens != 200 {
		t.Errorf("other token fields corrupted: in=%d out=%d", out.InputTokens, out.OutputTokens)
	}
}

func TestResultEntry_ThinkingTokensBackwardsCompat(t *testing.T) {
	// JSON produced by an older agent that doesn't know about thinking_tokens.
	legacy := []byte(`{"result":"ok","input_tokens":10,"output_tokens":20}`)
	var out resultEntry
	if err := json.Unmarshal(legacy, &out); err != nil {
		t.Fatalf("decoding legacy payload should not error: %v", err)
	}
	if out.ThinkingTokens != 0 {
		t.Errorf("ThinkingTokens = %d, want 0 for legacy payload", out.ThinkingTokens)
	}
	if out.Result != "ok" || out.InputTokens != 10 || out.OutputTokens != 20 {
		t.Errorf("legacy fields lost: %+v", out)
	}
}
