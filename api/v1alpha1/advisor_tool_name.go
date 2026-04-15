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
	"fmt"
	"strings"
)

// SanitiseAdvisorToolName derives the auto-generated tool name from a connection name.
// The name is lowercased, hyphens are replaced with underscores, non-[a-z0-9_] characters
// are stripped, consecutive underscores are collapsed, and the result is prefixed with "consult_".
// Returns an error if the sanitised name is empty.
func SanitiseAdvisorToolName(connectionName string) (string, error) {
	// Lowercase and replace hyphens with underscores.
	s := strings.ToLower(connectionName)
	s = strings.ReplaceAll(s, "-", "_")

	// Strip characters not matching [a-z0-9_].
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' {
			b.WriteRune(r)
		}
	}
	s = b.String()

	// Collapse consecutive underscores.
	for strings.Contains(s, "__") {
		s = strings.ReplaceAll(s, "__", "_")
	}

	// Trim leading/trailing underscores.
	s = strings.Trim(s, "_")

	if s == "" {
		return "", fmt.Errorf("connection name %q produces an empty tool name after sanitisation", connectionName)
	}

	return "consult_" + s, nil
}

// ResolveAdvisorToolName returns the tool name for an advisor connection.
// If ContextPropagation.ToolName is set, it is used directly.
// Otherwise the name is derived from the connection name via SanitiseAdvisorToolName.
func ResolveAdvisorToolName(conn AgentConnection) (string, error) {
	if conn.ContextPropagation != nil && conn.ContextPropagation.ToolName != "" {
		return conn.ContextPropagation.ToolName, nil
	}
	return SanitiseAdvisorToolName(conn.Name)
}
