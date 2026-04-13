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
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kubeswarmv1alpha1 "github.com/kubeswarm/kubeswarm/api/v1alpha1"
)

// policyTestEnv holds a dedicated envtest environment for SwarmPolicy controller tests.
type policyTestEnv struct {
	client client.Client
	env    *envtest.Environment
	ctx    context.Context
	cancel context.CancelFunc
}

func setupPolicyTestEnv(t *testing.T) *policyTestEnv {
	t.Helper()

	if err := kubeswarmv1alpha1.AddToScheme(scheme.Scheme); err != nil {
		t.Fatalf("add scheme: %v", err)
	}

	te := &envtest.Environment{
		CRDDirectoryPaths:     []string{filepath.Join("..", "..", "config", "crd", "bases")},
		ErrorIfCRDPathMissing: true,
	}
	if dir := getFirstFoundEnvTestBinaryDir(); dir != "" {
		te.BinaryAssetsDirectory = dir
	}

	cfg, err := te.Start()
	if err != nil {
		t.Fatalf("start envtest: %v", err)
	}

	c, err := client.New(cfg, client.Options{Scheme: scheme.Scheme})
	if err != nil {
		te.Stop() //nolint:errcheck
		t.Fatalf("create client: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())

	t.Cleanup(func() {
		cancel()
		deadline := time.After(30 * time.Second)
		done := make(chan error, 1)
		go func() { done <- te.Stop() }()
		select {
		case err := <-done:
			if err != nil {
				t.Logf("stop envtest: %v", err)
			}
		case <-deadline:
			t.Log("envtest stop timed out")
		}
	})

	return &policyTestEnv{client: c, env: te, ctx: ctx, cancel: cancel}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func (e *policyTestEnv) reconciler() *SwarmPolicyReconciler {
	return &SwarmPolicyReconciler{Client: e.client, Scheme: e.client.Scheme()}
}

func (e *policyTestEnv) reconcilerWithBatch(batchSize int) *SwarmPolicyReconciler {
	return &SwarmPolicyReconciler{Client: e.client, Scheme: e.client.Scheme(), PolicyBatchSize: batchSize}
}

const policyTestNS = "default"

func (e *policyTestEnv) reconcile(t *testing.T, name string) reconcile.Result {
	t.Helper()
	result, err := e.reconciler().Reconcile(e.ctx, reconcile.Request{
		NamespacedName: types.NamespacedName{Name: name, Namespace: policyTestNS},
	})
	if err != nil {
		t.Fatalf("reconcile %s: %v", name, err)
	}
	return result
}

func (e *policyTestEnv) createPolicy(t *testing.T, pol *kubeswarmv1alpha1.SwarmPolicy) {
	t.Helper()
	if err := e.client.Create(e.ctx, pol); err != nil {
		t.Fatalf("create policy %s: %v", pol.Name, err)
	}
	t.Cleanup(func() { _ = e.client.Delete(e.ctx, pol) })
}

func (e *policyTestEnv) createAgent(t *testing.T, agent *kubeswarmv1alpha1.SwarmAgent) {
	t.Helper()
	if err := e.client.Create(e.ctx, agent); err != nil {
		t.Fatalf("create agent %s: %v", agent.Name, err)
	}
	t.Cleanup(func() { _ = e.client.Delete(e.ctx, agent) })
}

func (e *policyTestEnv) getPolicy(t *testing.T, name string) *kubeswarmv1alpha1.SwarmPolicy {
	t.Helper()
	pol := &kubeswarmv1alpha1.SwarmPolicy{}
	if err := e.client.Get(e.ctx, types.NamespacedName{Name: name, Namespace: policyTestNS}, pol); err != nil {
		t.Fatalf("get policy %s: %v", name, err)
	}
	return pol
}

func (e *policyTestEnv) getAgent(t *testing.T, name string) *kubeswarmv1alpha1.SwarmAgent {
	t.Helper()
	agent := &kubeswarmv1alpha1.SwarmAgent{}
	if err := e.client.Get(e.ctx, types.NamespacedName{Name: name, Namespace: policyTestNS}, agent); err != nil {
		t.Fatalf("get agent %s: %v", name, err)
	}
	return agent
}

func (e *policyTestEnv) getNamespace(t *testing.T, name string) *corev1.Namespace {
	t.Helper()
	ns := &corev1.Namespace{}
	if err := e.client.Get(e.ctx, types.NamespacedName{Name: name}, ns); err != nil {
		t.Fatalf("get namespace %s: %v", name, err)
	}
	return ns
}

func makePolObj(name string, spec kubeswarmv1alpha1.SwarmPolicySpec) *kubeswarmv1alpha1.SwarmPolicy {
	return &kubeswarmv1alpha1.SwarmPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: policyTestNS},
		Spec:       spec,
	}
}

func makeAgtObj(name string) *kubeswarmv1alpha1.SwarmAgent {
	return &kubeswarmv1alpha1.SwarmAgent{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: policyTestNS},
		Spec: kubeswarmv1alpha1.SwarmAgentSpec{
			Model:  "claude-sonnet-4-6",
			Prompt: &kubeswarmv1alpha1.AgentPrompt{Inline: "You are helpful."},
		},
	}
}

//go:fix inline
func i64(v int64) *int64 { return new(v) }

//go:fix inline
func i32(v int32) *int32 { return new(v) }

// ---------------------------------------------------------------------------
// Integration tests
// ---------------------------------------------------------------------------

func TestPolicyController_Nonexistent(t *testing.T) {
	e := setupPolicyTestEnv(t)

	t.Run("nonexistent policy returns no error", func(t *testing.T) {
		r := e.reconciler()
		_, err := r.Reconcile(e.ctx, reconcile.Request{
			NamespacedName: types.NamespacedName{Name: "nonexistent", Namespace: policyTestNS},
		})
		if err != nil {
			t.Fatalf("expected no error, got: %v", err)
		}
	})
}

func TestPolicyController_CompliantAgent(t *testing.T) {
	e := setupPolicyTestEnv(t)

	agent := makeAgtObj("compliant-agent")
	e.createAgent(t, agent)

	pol := makePolObj("compliant-pol", kubeswarmv1alpha1.SwarmPolicySpec{
		EnforcementMode: kubeswarmv1alpha1.PolicyEnforcementAudit,
		Limits:          &kubeswarmv1alpha1.PolicyLimits{MaxDailyTokens: i64(100000)},
	})
	e.createPolicy(t, pol)
	e.reconcile(t, pol.Name)

	t.Run("Enforcing is True", func(t *testing.T) {
		p := e.getPolicy(t, pol.Name)
		cond := apimeta.FindStatusCondition(p.Status.Conditions, kubeswarmv1alpha1.ConditionEnforcing)
		if cond == nil || cond.Status != metav1.ConditionTrue {
			t.Errorf("expected Enforcing=True, got %v", cond)
		}
		if cond != nil && cond.Reason != "Active" {
			t.Errorf("Reason = %q, want Active", cond.Reason)
		}
	})

	t.Run("Conflicting is False", func(t *testing.T) {
		p := e.getPolicy(t, pol.Name)
		cond := apimeta.FindStatusCondition(p.Status.Conditions, kubeswarmv1alpha1.ConditionConflicting)
		if cond == nil || cond.Status != metav1.ConditionFalse {
			t.Errorf("expected Conflicting=False, got %v", cond)
		}
	})

	t.Run("PolicyCompliant is True on agent", func(t *testing.T) {
		a := e.getAgent(t, agent.Name)
		cond := apimeta.FindStatusCondition(a.Status.Conditions, kubeswarmv1alpha1.ConditionPolicyCompliant)
		if cond == nil || cond.Status != metav1.ConditionTrue {
			t.Errorf("expected PolicyCompliant=True, got %v", cond)
		}
	})

	t.Run("policy-compliant label is true", func(t *testing.T) {
		a := e.getAgent(t, agent.Name)
		if v := a.Labels[kubeswarmv1alpha1.LabelPolicyCompliant]; v != labelValueTrue {
			t.Errorf("label = %q, want true", v)
		}
	})

	t.Run("policy-governed label on namespace", func(t *testing.T) {
		n := e.getNamespace(t, policyTestNS)
		if v := n.Labels[kubeswarmv1alpha1.LabelPolicyGoverned]; v != labelValueTrue {
			t.Errorf("namespace label = %q, want true", v)
		}
	})

	t.Run("status counts", func(t *testing.T) {
		p := e.getPolicy(t, pol.Name)
		if p.Status.AgentCount < 1 {
			t.Errorf("AgentCount = %d, want >= 1", p.Status.AgentCount)
		}
		if p.Status.CompliantCount < 1 {
			t.Errorf("CompliantCount = %d, want >= 1", p.Status.CompliantCount)
		}
	})

	t.Run("EffectivePolicy populated", func(t *testing.T) {
		p := e.getPolicy(t, pol.Name)
		if p.Status.EffectivePolicy == nil {
			t.Fatal("expected EffectivePolicy")
		}
		if p.Status.EffectivePolicy.Limits == nil || p.Status.EffectivePolicy.Limits.MaxDailyTokens == nil {
			t.Fatal("expected MaxDailyTokens")
		}
		if *p.Status.EffectivePolicy.Limits.MaxDailyTokens != 100000 {
			t.Errorf("MaxDailyTokens = %d, want 100000", *p.Status.EffectivePolicy.Limits.MaxDailyTokens)
		}
	})

	t.Run("ObservedGeneration", func(t *testing.T) {
		p := e.getPolicy(t, pol.Name)
		if p.Status.ObservedGeneration != p.Generation {
			t.Errorf("ObservedGeneration = %d, want %d", p.Status.ObservedGeneration, p.Generation)
		}
	})
}

func TestPolicyController_NonCompliantAgent(t *testing.T) {
	e := setupPolicyTestEnv(t)

	agent := makeAgtObj("violating-agent")
	agent.Spec.Guardrails = &kubeswarmv1alpha1.AgentGuardrails{
		Limits: &kubeswarmv1alpha1.GuardrailLimits{DailyTokens: 200000},
	}
	e.createAgent(t, agent)

	pol := makePolObj("strict-pol", kubeswarmv1alpha1.SwarmPolicySpec{
		EnforcementMode: kubeswarmv1alpha1.PolicyEnforcementEnforce,
		Limits:          &kubeswarmv1alpha1.PolicyLimits{MaxDailyTokens: i64(50000)},
	})
	e.createPolicy(t, pol)
	e.reconcile(t, pol.Name)

	t.Run("PolicyCompliant is False", func(t *testing.T) {
		a := e.getAgent(t, agent.Name)
		cond := apimeta.FindStatusCondition(a.Status.Conditions, kubeswarmv1alpha1.ConditionPolicyCompliant)
		if cond == nil {
			t.Fatal("expected PolicyCompliant condition")
		}
		if cond.Status != metav1.ConditionFalse {
			t.Errorf("status = %s, want False", cond.Status)
		}
		if cond.Reason != "NonCompliant" {
			t.Errorf("reason = %q, want NonCompliant", cond.Reason)
		}
	})

	t.Run("policy-compliant label is false", func(t *testing.T) {
		a := e.getAgent(t, agent.Name)
		if v := a.Labels[kubeswarmv1alpha1.LabelPolicyCompliant]; v != labelValueFalse {
			t.Errorf("label = %q, want false", v)
		}
	})

	t.Run("CompliantCount is 0", func(t *testing.T) {
		p := e.getPolicy(t, pol.Name)
		if p.Status.CompliantCount != 0 {
			t.Errorf("CompliantCount = %d, want 0", p.Status.CompliantCount)
		}
	})
}

func TestPolicyController_ConflictingPolicies(t *testing.T) {
	e := setupPolicyTestEnv(t)

	agent := makeAgtObj("conflict-agent")
	e.createAgent(t, agent)

	pol1 := makePolObj("conflict-min", kubeswarmv1alpha1.SwarmPolicySpec{
		Limits: &kubeswarmv1alpha1.PolicyLimits{MinTimeoutSeconds: i32(300)},
	})
	e.createPolicy(t, pol1)

	pol2 := makePolObj("conflict-max", kubeswarmv1alpha1.SwarmPolicySpec{
		Limits: &kubeswarmv1alpha1.PolicyLimits{MaxTimeoutSeconds: i32(60)},
	})
	e.createPolicy(t, pol2)

	e.reconcile(t, pol1.Name)

	t.Run("Conflicting is True on first policy", func(t *testing.T) {
		p := e.getPolicy(t, pol1.Name)
		cond := apimeta.FindStatusCondition(p.Status.Conditions, kubeswarmv1alpha1.ConditionConflicting)
		if cond == nil {
			t.Fatal("expected Conflicting condition")
		}
		if cond.Status != metav1.ConditionTrue {
			t.Errorf("Conflicting = %s, want True", cond.Status)
		}
		if cond.Reason != "ImpossibleConstraints" {
			t.Errorf("Reason = %q, want ImpossibleConstraints", cond.Reason)
		}
	})

	t.Run("Conflicting is True on second policy", func(t *testing.T) {
		p := e.getPolicy(t, pol2.Name)
		cond := apimeta.FindStatusCondition(p.Status.Conditions, kubeswarmv1alpha1.ConditionConflicting)
		if cond == nil {
			t.Fatal("expected Conflicting condition")
		}
		if cond.Status != metav1.ConditionTrue {
			t.Errorf("Conflicting = %s, want True", cond.Status)
		}
	})
}

func TestPolicyController_StrictestWins(t *testing.T) {
	e := setupPolicyTestEnv(t)

	agent := makeAgtObj("multi-agent")
	e.createAgent(t, agent)

	pol1 := makePolObj("multi-1", kubeswarmv1alpha1.SwarmPolicySpec{
		EnforcementMode: kubeswarmv1alpha1.PolicyEnforcementAudit,
		Limits:          &kubeswarmv1alpha1.PolicyLimits{MaxDailyTokens: i64(100000)},
	})
	e.createPolicy(t, pol1)

	pol2 := makePolObj("multi-2", kubeswarmv1alpha1.SwarmPolicySpec{
		EnforcementMode: kubeswarmv1alpha1.PolicyEnforcementEnforce,
		Limits:          &kubeswarmv1alpha1.PolicyLimits{MaxDailyTokens: i64(50000)},
	})
	e.createPolicy(t, pol2)

	e.reconcile(t, pol1.Name)

	p := e.getPolicy(t, pol1.Name)
	if p.Status.EffectivePolicy == nil {
		t.Fatal("expected EffectivePolicy")
	}
	if *p.Status.EffectivePolicy.Limits.MaxDailyTokens != 50000 {
		t.Errorf("MaxDailyTokens = %d, want 50000", *p.Status.EffectivePolicy.Limits.MaxDailyTokens)
	}
	if p.Status.EffectivePolicy.EnforcementMode != kubeswarmv1alpha1.PolicyEnforcementEnforce {
		t.Errorf("EnforcementMode = %s, want Enforce", p.Status.EffectivePolicy.EnforcementMode)
	}
}

func TestPolicyController_RequirementBudgetRef(t *testing.T) {
	e := setupPolicyTestEnv(t)

	agent := makeAgtObj("no-budget-agent")
	e.createAgent(t, agent)

	pol := makePolObj("require-budget", kubeswarmv1alpha1.SwarmPolicySpec{
		Requirements: kubeswarmv1alpha1.PolicyRequirements{BudgetRef: true},
	})
	e.createPolicy(t, pol)
	e.reconcile(t, pol.Name)

	a := e.getAgent(t, agent.Name)
	cond := apimeta.FindStatusCondition(a.Status.Conditions, kubeswarmv1alpha1.ConditionPolicyCompliant)
	if cond == nil || cond.Status != metav1.ConditionFalse {
		t.Errorf("expected PolicyCompliant=False for missing budgetRef, got %v", cond)
	}
}

func TestPolicyController_ModelDenyList(t *testing.T) {
	e := setupPolicyTestEnv(t)

	agent := makeAgtObj("denied-model-agent")
	agent.Spec.Model = "gpt-4o-mini"
	e.createAgent(t, agent)

	pol := makePolObj("deny-model", kubeswarmv1alpha1.SwarmPolicySpec{
		Models: &kubeswarmv1alpha1.PolicyModels{Denied: []string{"gpt-*"}},
	})
	e.createPolicy(t, pol)
	e.reconcile(t, pol.Name)

	a := e.getAgent(t, agent.Name)
	cond := apimeta.FindStatusCondition(a.Status.Conditions, kubeswarmv1alpha1.ConditionPolicyCompliant)
	if cond == nil || cond.Status != metav1.ConditionFalse {
		t.Errorf("expected PolicyCompliant=False for denied model, got %v", cond)
	}
}

func TestPolicyController_DeleteCleansUp(t *testing.T) {
	e := setupPolicyTestEnv(t)

	agent := makeAgtObj("cleanup-agent")
	e.createAgent(t, agent)

	pol := makePolObj("cleanup-pol", kubeswarmv1alpha1.SwarmPolicySpec{
		Requirements: kubeswarmv1alpha1.PolicyRequirements{BudgetRef: true},
	})
	e.createPolicy(t, pol)
	e.reconcile(t, pol.Name)

	// Verify non-compliant state was set.
	a := e.getAgent(t, agent.Name)
	if _, ok := a.Labels[kubeswarmv1alpha1.LabelPolicyCompliant]; !ok {
		t.Fatal("expected label after first reconcile")
	}

	// Delete the policy.
	if err := e.client.Delete(e.ctx, pol); err != nil {
		t.Fatalf("delete policy: %v", err)
	}

	// Reconcile the deleted policy.
	r := e.reconciler()
	_, err := r.Reconcile(e.ctx, reconcile.Request{
		NamespacedName: types.NamespacedName{Name: pol.Name, Namespace: policyTestNS},
	})
	if err != nil {
		t.Fatalf("reconcile after delete: %v", err)
	}

	t.Run("PolicyCompliant condition removed", func(t *testing.T) {
		a := e.getAgent(t, agent.Name)
		cond := apimeta.FindStatusCondition(a.Status.Conditions, kubeswarmv1alpha1.ConditionPolicyCompliant)
		if cond != nil {
			t.Error("expected PolicyCompliant condition to be removed")
		}
	})

	t.Run("policy-compliant label removed", func(t *testing.T) {
		a := e.getAgent(t, agent.Name)
		if _, ok := a.Labels[kubeswarmv1alpha1.LabelPolicyCompliant]; ok {
			t.Error("expected label to be removed")
		}
	})

	t.Run("policy-governed label removed from namespace", func(t *testing.T) {
		n := e.getNamespace(t, policyTestNS)
		if _, ok := n.Labels[kubeswarmv1alpha1.LabelPolicyGoverned]; ok {
			t.Error("expected namespace label to be removed")
		}
	})
}

func TestPolicyController_BatchLimiting(t *testing.T) {
	e := setupPolicyTestEnv(t)

	const batchSize = 3
	agents := make([]string, 5)
	for i := range agents {
		agents[i] = fmt.Sprintf("batch-agent-%d", i)
		e.createAgent(t, makeAgtObj(agents[i]))
	}

	pol := makePolObj("batch-pol", kubeswarmv1alpha1.SwarmPolicySpec{
		Requirements: kubeswarmv1alpha1.PolicyRequirements{BudgetRef: true},
	})
	e.createPolicy(t, pol)

	r := e.reconcilerWithBatch(batchSize)
	result, err := r.Reconcile(e.ctx, reconcile.Request{
		NamespacedName: types.NamespacedName{Name: pol.Name, Namespace: policyTestNS},
	})
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if result.RequeueAfter <= 0 {
		t.Error("expected RequeueAfter > 0 when batch limit reached")
	}
}

func TestPolicyController_AgentBecomesCompliant(t *testing.T) {
	e := setupPolicyTestEnv(t)

	agent := makeAgtObj("evolving-agent")
	agent.Spec.Guardrails = &kubeswarmv1alpha1.AgentGuardrails{
		Limits: &kubeswarmv1alpha1.GuardrailLimits{DailyTokens: 75000},
	}
	e.createAgent(t, agent)

	pol := makePolObj("evolving-pol", kubeswarmv1alpha1.SwarmPolicySpec{
		Limits: &kubeswarmv1alpha1.PolicyLimits{MaxDailyTokens: i64(50000)},
	})
	e.createPolicy(t, pol)

	// First: non-compliant.
	e.reconcile(t, pol.Name)
	a := e.getAgent(t, agent.Name)
	if v := a.Labels[kubeswarmv1alpha1.LabelPolicyCompliant]; v != labelValueFalse {
		t.Fatalf("expected false, got %q", v)
	}

	// Relax.
	p := e.getPolicy(t, pol.Name)
	p.Spec.Limits.MaxDailyTokens = i64(100000)
	if err := e.client.Update(e.ctx, p); err != nil {
		t.Fatalf("update policy: %v", err)
	}

	// Second: compliant.
	e.reconcile(t, pol.Name)
	a = e.getAgent(t, agent.Name)
	cond := apimeta.FindStatusCondition(a.Status.Conditions, kubeswarmv1alpha1.ConditionPolicyCompliant)
	if cond == nil || cond.Status != metav1.ConditionTrue {
		t.Error("expected PolicyCompliant=True after relaxing policy")
	}
	if v := a.Labels[kubeswarmv1alpha1.LabelPolicyCompliant]; v != labelValueTrue {
		t.Errorf("label = %q, want true", v)
	}
}

func TestPolicyController_NoAgents(t *testing.T) {
	e := setupPolicyTestEnv(t)

	pol := makePolObj("lonely-pol", kubeswarmv1alpha1.SwarmPolicySpec{
		Limits: &kubeswarmv1alpha1.PolicyLimits{MaxDailyTokens: i64(100000)},
	})
	e.createPolicy(t, pol)
	e.reconcile(t, pol.Name)

	p := e.getPolicy(t, pol.Name)
	cond := apimeta.FindStatusCondition(p.Status.Conditions, kubeswarmv1alpha1.ConditionEnforcing)
	if cond == nil || cond.Status != metav1.ConditionTrue {
		t.Error("expected Enforcing=True even with no agents")
	}

	n := e.getNamespace(t, policyTestNS)
	if v := n.Labels[kubeswarmv1alpha1.LabelPolicyGoverned]; v != labelValueTrue {
		t.Error("expected policy-governed label even with no agents")
	}
}

func TestPolicyController_MixedCompliance(t *testing.T) {
	e := setupPolicyTestEnv(t)

	good := makeAgtObj("mixed-good")
	e.createAgent(t, good)

	bad := makeAgtObj("mixed-bad")
	bad.Spec.Guardrails = &kubeswarmv1alpha1.AgentGuardrails{
		Limits: &kubeswarmv1alpha1.GuardrailLimits{DailyTokens: 200000},
	}
	e.createAgent(t, bad)

	pol := makePolObj("mixed-pol", kubeswarmv1alpha1.SwarmPolicySpec{
		Limits: &kubeswarmv1alpha1.PolicyLimits{MaxDailyTokens: i64(50000)},
	})
	e.createPolicy(t, pol)
	e.reconcile(t, pol.Name)

	goodA := e.getAgent(t, good.Name)
	if v := goodA.Labels[kubeswarmv1alpha1.LabelPolicyCompliant]; v != labelValueTrue {
		t.Errorf("good agent label = %q, want true", v)
	}

	badA := e.getAgent(t, bad.Name)
	if v := badA.Labels[kubeswarmv1alpha1.LabelPolicyCompliant]; v != labelValueFalse {
		t.Errorf("bad agent label = %q, want false", v)
	}
}

func TestPolicyController_AgentToPolicy(t *testing.T) {
	e := setupPolicyTestEnv(t)

	pol := makePolObj("map-fn-pol", kubeswarmv1alpha1.SwarmPolicySpec{
		Limits: &kubeswarmv1alpha1.PolicyLimits{MaxDailyTokens: i64(100000)},
	})
	e.createPolicy(t, pol)

	r := e.reconciler()
	agent := makeAgtObj("map-fn-agent")
	requests := r.agentToPolicy(e.ctx, agent)
	found := false
	for _, req := range requests {
		if req.Name == pol.Name && req.Namespace == policyTestNS {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected policy %q in reconcile requests", pol.Name)
	}
}

// ---------------------------------------------------------------------------
// Pure function tests (no envtest needed)
// ---------------------------------------------------------------------------

func TestFormatViolations_Empty(t *testing.T) {
	if result := formatViolations(nil); result != "" {
		t.Errorf("expected empty string, got %q", result)
	}
}

func TestFormatViolations_Multiple(t *testing.T) {
	violations := []kubeswarmv1alpha1.PolicyViolation{
		{Constraint: "limits.dailyTokens", PolicyName: "p1", Message: "too many tokens"},
		{Constraint: "requirements.budgetRef", PolicyName: "p2", Message: "missing budget"},
	}
	result := formatViolations(violations)
	if len(result) == 0 {
		t.Fatal("expected non-empty result")
	}
}

func TestFormatViolations_SortedByConstraint(t *testing.T) {
	violations := []kubeswarmv1alpha1.PolicyViolation{
		{Constraint: "z-constraint", PolicyName: "p1", Message: "z msg"},
		{Constraint: "a-constraint", PolicyName: "p2", Message: "a msg"},
	}
	result := formatViolations(violations)
	if len(result) < 5 || result[:5] != "a msg" {
		t.Errorf("expected result to start with 'a msg', got %q", result)
	}
}

func TestFormatConflicts(t *testing.T) {
	conflicts := []PolicyConflict{{
		Field: "limits.timeoutSeconds", PolicyA: "pol-a", PolicyB: "pol-b",
		Message: "min 300 > max 60",
	}}
	result := formatConflicts(conflicts)
	for _, want := range []string{"limits.timeoutSeconds", "pol-a", "pol-b", "min 300 > max 60"} {
		if !containsStr(result, want) {
			t.Errorf("expected %q in %q", want, result)
		}
	}
}

func TestPolicyBatchSize_Default(t *testing.T) {
	r := &SwarmPolicyReconciler{}
	if r.policyBatchSize() != defaultPolicyBatchSize {
		t.Errorf("got %d, want %d", r.policyBatchSize(), defaultPolicyBatchSize)
	}
}

func TestPolicyBatchSize_Configured(t *testing.T) {
	r := &SwarmPolicyReconciler{PolicyBatchSize: 10}
	if r.policyBatchSize() != 10 {
		t.Errorf("got %d, want 10", r.policyBatchSize())
	}
}

func containsStr(s, substr string) bool {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
