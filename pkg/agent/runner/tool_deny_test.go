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
	"errors"
	"strings"
	"testing"

	agenterrors "github.com/kubeswarm/kubeswarm/pkg/agent/errors"
)

func TestCheckToolDenied_EmptyPatterns(t *testing.T) {
	err := checkToolDenied("filesystem/write_file", []string{})
	if err != nil {
		t.Errorf("expected nil for empty denyPatterns, got %v", err)
	}
}

func TestCheckToolDenied_NilPatterns(t *testing.T) {
	err := checkToolDenied("filesystem/write_file", nil)
	if err != nil {
		t.Errorf("expected nil for nil denyPatterns, got %v", err)
	}
}

func TestCheckToolDenied_ExactMatch(t *testing.T) {
	err := checkToolDenied("filesystem/write_file", []string{"filesystem/write_file"})
	if err == nil {
		t.Fatal("expected error for exact match, got nil")
	}
}

func TestCheckToolDenied_GlobMatch(t *testing.T) {
	err := checkToolDenied("shell/bash", []string{"shell/*"})
	if err == nil {
		t.Fatal("expected error for glob match shell/* against shell/bash, got nil")
	}
}

func TestCheckToolDenied_NoMatch(t *testing.T) {
	err := checkToolDenied("filesystem/read_file", []string{"shell/*", "network/http_request"})
	if err != nil {
		t.Errorf("expected nil when tool does not match any pattern, got %v", err)
	}
}

func TestCheckToolDenied_MalformedGlob(t *testing.T) {
	// A malformed glob pattern (e.g. unclosed bracket) should be treated as
	// a non-match rather than causing a panic.
	err := checkToolDenied("filesystem/read_file", []string{"[invalid"})
	if err != nil {
		t.Errorf("expected nil for malformed glob pattern, got %v", err)
	}
}

func TestCheckToolDenied_MultiplePatterns_AnyMatches(t *testing.T) {
	err := checkToolDenied("network/http_request", []string{
		"shell/*",
		"network/*",
		"filesystem/write_file",
	})
	if err == nil {
		t.Fatal("expected error when tool matches one of multiple patterns, got nil")
	}
}

func TestCheckToolDenied_MCPStyleToolName(t *testing.T) {
	err := checkToolDenied("github__list_issues", []string{"github__*"})
	if err == nil {
		t.Fatal("expected error for MCP-style tool name matching github__*, got nil")
	}
}

func TestCheckToolDenied_ErrorIsAgentError(t *testing.T) {
	err := checkToolDenied("shell/bash", []string{"shell/*"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	var ae *agenterrors.AgentError
	if !errors.As(err, &ae) {
		t.Fatalf("expected *agenterrors.AgentError, got %T", err)
	}
	if ae.Code != agenterrors.ErrToolDenied {
		t.Errorf("Code = %q, want %q", ae.Code, agenterrors.ErrToolDenied)
	}
}

func TestCheckToolDenied_ErrorMessageContainsToolAndPattern(t *testing.T) {
	err := checkToolDenied("shell/bash", []string{"shell/*"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	msg := err.Error()
	if !strings.Contains(msg, "shell/bash") {
		t.Errorf("error message %q does not contain tool name %q", msg, "shell/bash")
	}
	if !strings.Contains(msg, "shell/*") {
		t.Errorf("error message %q does not contain matching pattern %q", msg, "shell/*")
	}
}
