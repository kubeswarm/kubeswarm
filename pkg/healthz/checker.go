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

// Package healthz provides named health checkers compatible with
// controller-runtime's healthz framework.
package healthz

import (
	"context"
	"fmt"
	"net/http"
)

// Probe checks whether a subsystem is healthy.
type Probe interface {
	// Check returns nil if healthy, or an error describing the failure.
	Check(ctx context.Context) error
}

// Role identifies which subsystem is being health-checked.
type Role string

const (
	RoleQueue  Role = "queue"
	RoleStream Role = "stream"
	RoleSpend  Role = "spend"
	RoleAudit  Role = "audit"
)

// Checker implements the controller-runtime healthz.HealthChecker interface
// for a named subsystem role. It is safe for concurrent use.
type Checker struct {
	role  Role
	probe Probe
}

// NewChecker returns a health checker for the given role and probe.
func NewChecker(role Role, probe Probe) *Checker {
	return &Checker{
		role:  role,
		probe: probe,
	}
}

// Check calls the underlying probe with the request context.
// Returns nil if healthy, or an error describing the failure.
func (c *Checker) Check(req *http.Request) error {
	if err := c.probe.Check(req.Context()); err != nil {
		return fmt.Errorf("%s unhealthy: %w", c.role, err)
	}
	return nil
}

// Name returns the checker name as the role string.
func (c *Checker) Name() string {
	return string(c.role)
}
