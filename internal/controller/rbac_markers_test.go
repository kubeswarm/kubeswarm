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

package controller

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	rbacv1 "k8s.io/api/rbac/v1"
	sigsyaml "sigs.k8s.io/yaml"
)

// TestRBACMarkersMatchRoleYAML asserts that every +kubebuilder:rbac marker
// declared in internal/controller/*.go is represented by a matching
// (apiGroup, resource, verb) triple in config/rbac/role.yaml.
//
// This guards against a silent-drop failure mode in controller-gen: when
// rbac markers sit inside a type's godoc (no blank line between the marker
// block and the type declaration), the collector classifies them as
// type-level and never promotes them to the package-level set that the
// rbac generator reads. make manifests exits 0, nothing warns you, and
// the missing permissions only surface as a runtime 403.
func TestRBACMarkersMatchRoleYAML(t *testing.T) {
	controllerDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	rolePath := filepath.Join(controllerDir, "..", "..", "config", "rbac", "role.yaml")

	granted, err := loadGrantedTriples(rolePath)
	if err != nil {
		t.Fatalf("load role.yaml: %v", err)
	}

	entries, err := os.ReadDir(controllerDir)
	if err != nil {
		t.Fatalf("read controller dir: %v", err)
	}

	var missing []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(controllerDir, e.Name())) //nolint:gosec // test-only; paths are os.ReadDir entries under a fixed directory.
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		for _, m := range parseRBACMarkers(string(data)) {
			for _, g := range m.groups {
				for _, res := range m.resources {
					for _, v := range m.verbs {
						key := rbacTriple(g, res, v)
						if _, ok := granted[key]; !ok {
							missing = append(missing, e.Name()+": groups="+g+" resources="+res+" verbs="+v)
						}
					}
				}
			}
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		t.Fatalf("RBAC markers not represented in config/rbac/role.yaml (run `make manifests`):\n  %s",
			strings.Join(missing, "\n  "))
	}
}

func loadGrantedTriples(path string) (map[string]struct{}, error) {
	data, err := os.ReadFile(path) //nolint:gosec // test-only; path is a fixed project-relative location.
	if err != nil {
		return nil, err
	}
	var role rbacv1.ClusterRole
	if err := sigsyaml.Unmarshal(data, &role); err != nil {
		return nil, err
	}
	granted := make(map[string]struct{})
	for _, r := range role.Rules {
		for _, g := range r.APIGroups {
			for _, res := range r.Resources {
				for _, v := range r.Verbs {
					granted[rbacTriple(g, res, v)] = struct{}{}
				}
			}
		}
	}
	return granted, nil
}

// rbacTriple normalises a (group, resource, verb) tuple into a comparable key.
// controller-gen rewrites "core" to the empty string, so we do the same when
// building both sides of the comparison.
func rbacTriple(group, resource, verb string) string {
	if group == "core" {
		group = ""
	}
	return group + "|" + resource + "|" + verb
}

type rbacMarker struct {
	groups, resources, verbs []string
}

var rbacMarkerRe = regexp.MustCompile(`(?m)^//\s*\+kubebuilder:rbac:(.+)$`)

// parseRBACMarkers extracts +kubebuilder:rbac markers from Go source. It is
// intentionally restricted to the marker shape used in this repo
// (groups/resources/verbs fields, comma-separated, each value a
// semicolon-separated list). Markers that omit any of the three fields are
// skipped.
func parseRBACMarkers(src string) []rbacMarker {
	var out []rbacMarker
	for _, match := range rbacMarkerRe.FindAllStringSubmatch(src, -1) {
		m := rbacMarker{}
		for part := range strings.SplitSeq(match[1], ",") {
			k, v, ok := strings.Cut(part, "=")
			if !ok {
				continue
			}
			v = strings.Trim(strings.TrimSpace(v), `"`)
			values := strings.Split(v, ";")
			switch strings.TrimSpace(k) {
			case "groups":
				m.groups = values
			case "resources":
				m.resources = values
			case "verbs":
				m.verbs = values
			}
		}
		if len(m.groups) == 0 || len(m.resources) == 0 || len(m.verbs) == 0 {
			continue
		}
		out = append(out, m)
	}
	return out
}
