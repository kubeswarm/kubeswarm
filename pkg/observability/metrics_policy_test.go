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

package observability

import (
	"context"
	"testing"

	"go.opentelemetry.io/otel/attribute"
)

// TestNewOperatorMetrics_Succeeds verifies that NewOperatorMetrics returns a
// non-nil struct and no error. The OTel global no-op provider is sufficient
// for instrument registration.
func TestNewOperatorMetrics_Succeeds(t *testing.T) {
	om, err := NewOperatorMetrics()
	if err != nil {
		t.Fatalf("NewOperatorMetrics() error = %v, want nil", err)
	}
	if om == nil {
		t.Fatal("NewOperatorMetrics() returned nil, want non-nil")
	}
}

// TestRecordPolicyViolation_DoesNotPanic verifies that calling
// RecordPolicyViolation with the no-op provider does not panic.
func TestRecordPolicyViolation_DoesNotPanic(t *testing.T) {
	om, err := NewOperatorMetrics()
	if err != nil {
		t.Fatalf("NewOperatorMetrics: %v", err)
	}
	// Must not panic.
	om.RecordPolicyViolation(context.Background(), "max-tokens", "agent-a", "tokenBudget")
}

// TestRecordPolicyConflict_DoesNotPanic verifies that calling
// RecordPolicyConflict with the no-op provider does not panic.
func TestRecordPolicyConflict_DoesNotPanic(t *testing.T) {
	om, err := NewOperatorMetrics()
	if err != nil {
		t.Fatalf("NewOperatorMetrics: %v", err)
	}
	om.RecordPolicyConflict(context.Background())
}

// TestRecordPolicyAdmissionRejected_DoesNotPanic verifies that calling
// RecordPolicyAdmissionRejected with the no-op provider does not panic.
func TestRecordPolicyAdmissionRejected_DoesNotPanic(t *testing.T) {
	om, err := NewOperatorMetrics()
	if err != nil {
		t.Fatalf("NewOperatorMetrics: %v", err)
	}
	om.RecordPolicyAdmissionRejected(context.Background())
}

// TestRecordPolicyAdmissionWarned_DoesNotPanic verifies that calling
// RecordPolicyAdmissionWarned with the no-op provider does not panic.
func TestRecordPolicyAdmissionWarned_DoesNotPanic(t *testing.T) {
	om, err := NewOperatorMetrics()
	if err != nil {
		t.Fatalf("NewOperatorMetrics: %v", err)
	}
	om.RecordPolicyAdmissionWarned(context.Background())
}

// TestRecordPolicyAdmissionWouldReject_DoesNotPanic verifies that calling
// RecordPolicyAdmissionWouldReject with the no-op provider does not panic.
func TestRecordPolicyAdmissionWouldReject_DoesNotPanic(t *testing.T) {
	om, err := NewOperatorMetrics()
	if err != nil {
		t.Fatalf("NewOperatorMetrics: %v", err)
	}
	om.RecordPolicyAdmissionWouldReject(context.Background())
}

// TestRecordPolicyViolation_WithExtraAttrs verifies that passing additional
// OTel attributes to RecordPolicyViolation does not panic.
func TestRecordPolicyViolation_WithExtraAttrs(t *testing.T) {
	om, err := NewOperatorMetrics()
	if err != nil {
		t.Fatalf("NewOperatorMetrics: %v", err)
	}
	om.RecordPolicyViolation(
		context.Background(),
		"cost-limit",
		"agent-b",
		"maxCostPerTask",
		attribute.String("namespace", "production"),
		attribute.String("team", "platform"),
		attribute.Int("severity", 3),
	)
}
