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
	"path"

	agenterrors "github.com/kubeswarm/kubeswarm/pkg/agent/errors"
)

// checkToolDenied returns an error if toolName matches any deny pattern.
// Uses path.Match for glob matching. Malformed patterns are treated as non-match.
func checkToolDenied(toolName string, denyPatterns []string) error {
	for _, pattern := range denyPatterns {
		if globMatch(pattern, toolName) {
			return agenterrors.NewToolError(
				agenterrors.ErrToolDenied,
				fmt.Sprintf("tool %q denied by policy pattern %q", toolName, pattern),
				nil,
			)
		}
	}
	return nil
}

// globMatch reports whether name matches the given glob pattern using path.Match.
// A malformed pattern is treated as a non-match.
// Mirrors the same function in internal/controller/swarmpolicy_merge.go.
func globMatch(pattern, name string) bool {
	matched, err := path.Match(pattern, name)
	return err == nil && matched
}
