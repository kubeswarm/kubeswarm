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

package budget

import (
	"context"
	"testing"
)

func TestNoopStore(t *testing.T) {
	s, err := NewStore("redis://localhost:6379", 0, "ns", "agent")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Check(context.Background()); err != nil {
		t.Errorf("noop Check should never return an error, got %v", err)
	}
	if err := s.Record(context.Background(), "task-1", 1000); err != nil {
		t.Errorf("noop Record should never return an error, got %v", err)
	}
}

func TestNoopStore_EmptyNamespace(t *testing.T) {
	// No namespace — local dev / swarm run — should return noop regardless of limit.
	s, err := NewStore("redis://localhost:6379", 10000, "", "agent")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := s.(noopStore); !ok {
		t.Error("expected noopStore when namespace is empty")
	}
}

func TestNoopStore_EmptyAgentName(t *testing.T) {
	s, err := NewStore("redis://localhost:6379", 10000, "ns", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := s.(noopStore); !ok {
		t.Error("expected noopStore when agentName is empty")
	}
}

func TestParseTokens(t *testing.T) {
	cases := []struct {
		member string
		want   int64
	}{
		{"task-abc:1500", 1500},
		{"task-with:colon:in:id:250", 250},
		{"notokencount", 0},
		{"task:abc", 0}, // "abc" is not a number
		{"", 0},
		{":0", 0},
		{":999", 999},
	}
	for _, tc := range cases {
		got := parseTokens(tc.member)
		if got != tc.want {
			t.Errorf("parseTokens(%q) = %d, want %d", tc.member, got, tc.want)
		}
	}
}
