package broker

import (
	"sync"
	"testing"
	"time"

	agentv1 "github.com/atesoglu/legion/protocol/gen/go/legion/agent/v1"
	riskv1 "github.com/atesoglu/legion/protocol/gen/go/legion/risk/v1"
)

type fakeAudit struct {
	mu      sync.Mutex
	entries []*agentv1.CapabilityAudit
}

func (f *fakeAudit) Audit(entry *agentv1.CapabilityAudit) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.entries = append(f.entries, entry)
}

func (f *fakeAudit) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.entries)
}

func testBroker(t *testing.T, manifests []*agentv1.AgentManifest) (*Broker, *fakeAudit) {
	t.Helper()
	audit := &fakeAudit{}
	b, err := New(manifests, audit)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return b, audit
}

func TestNewRejectsADuplicateWorkloadID(t *testing.T) {
	_, err := New([]*agentv1.AgentManifest{
		{Identity: identity("velocity")},
		{Identity: identity("velocity")},
	}, nil)
	if err == nil {
		t.Fatal("New accepted two manifests for the same workload_id")
	}
}

func TestNewRejectsAManifestWithNoWorkloadID(t *testing.T) {
	_, err := New([]*agentv1.AgentManifest{{Identity: &agentv1.AgentIdentity{}}}, nil)
	if err == nil {
		t.Fatal("New accepted a manifest with no workload_id")
	}
}

func TestAnUnknownWorkloadIsDeniedNotGranted(t *testing.T) {
	b, _ := testBroker(t, Default())

	verdict, _ := b.CheckCapability(identity("impersonator"), "eval-1",
		agentv1.Capability_CAPABILITY_SUBMIT_EVALUATION, nil, riskv1.FeatureWindow_FEATURE_WINDOW_UNSPECIFIED)
	if verdict != agentv1.CapabilityVerdict_CAPABILITY_VERDICT_DENIED_NOT_GRANTED {
		t.Fatalf("verdict = %v, want DENIED_NOT_GRANTED", verdict)
	}
}

func TestAGrantedCapabilityIsAllowed(t *testing.T) {
	b, audit := testBroker(t, Default())

	verdict, _ := b.CheckCapability(identity("velocity"), "eval-1",
		agentv1.Capability_CAPABILITY_SUBMIT_EVALUATION, nil, riskv1.FeatureWindow_FEATURE_WINDOW_UNSPECIFIED)
	if verdict != agentv1.CapabilityVerdict_CAPABILITY_VERDICT_ALLOWED {
		t.Fatalf("verdict = %v, want ALLOWED", verdict)
	}
	if audit.count() != 1 {
		t.Fatalf("audit entries = %d, want 1", audit.count())
	}
}

func TestACapabilityNotInTheManifestIsDeniedNotGranted(t *testing.T) {
	b, _ := testBroker(t, Default())

	// velocity is never granted GET_DEVICE_FEATURES.
	verdict, _ := b.CheckCapability(identity("velocity"), "eval-1",
		agentv1.Capability_CAPABILITY_GET_DEVICE_FEATURES, nil, riskv1.FeatureWindow_FEATURE_WINDOW_UNSPECIFIED)
	if verdict != agentv1.CapabilityVerdict_CAPABILITY_VERDICT_DENIED_NOT_GRANTED {
		t.Fatalf("verdict = %v, want DENIED_NOT_GRANTED", verdict)
	}
}

func TestAMissingEvaluationIDIsDeniedScope(t *testing.T) {
	b, _ := testBroker(t, Default())

	verdict, _ := b.CheckCapability(identity("velocity"), "",
		agentv1.Capability_CAPABILITY_SUBMIT_EVALUATION, nil, riskv1.FeatureWindow_FEATURE_WINDOW_UNSPECIFIED)
	if verdict != agentv1.CapabilityVerdict_CAPABILITY_VERDICT_DENIED_SCOPE {
		t.Fatalf("verdict = %v, want DENIED_SCOPE", verdict)
	}
}

func TestAFeatureNameOutsideTheAllowListIsDeniedConstraint(t *testing.T) {
	b, _ := testBroker(t, Default())

	verdict, _ := b.CheckCapability(identity("velocity"), "eval-1",
		agentv1.Capability_CAPABILITY_GET_VELOCITY_FEATURES,
		[]string{"unique_locations"}, riskv1.FeatureWindow_FEATURE_WINDOW_UNSPECIFIED)
	if verdict != agentv1.CapabilityVerdict_CAPABILITY_VERDICT_DENIED_CONSTRAINT {
		t.Fatalf("verdict = %v, want DENIED_CONSTRAINT", verdict)
	}
}

func TestAFeatureNameWithinTheAllowListIsAllowed(t *testing.T) {
	b, _ := testBroker(t, Default())

	verdict, _ := b.CheckCapability(identity("velocity"), "eval-1",
		agentv1.Capability_CAPABILITY_GET_VELOCITY_FEATURES,
		[]string{"transactions"}, riskv1.FeatureWindow_FEATURE_WINDOW_UNSPECIFIED)
	if verdict != agentv1.CapabilityVerdict_CAPABILITY_VERDICT_ALLOWED {
		t.Fatalf("verdict = %v, want ALLOWED", verdict)
	}
}

func TestExceedingMaxInvocationsIsDeniedBudget(t *testing.T) {
	manifests := []*agentv1.AgentManifest{{
		Identity: identity("velocity"),
		Grants: []*agentv1.CapabilityGrant{
			{Capability: agentv1.Capability_CAPABILITY_SUBMIT_EVALUATION,
				Constraint: &agentv1.CapabilityConstraint{MaxInvocations: 2}},
		},
	}}
	b, _ := testBroker(t, manifests)

	for i := 0; i < 2; i++ {
		verdict, _ := b.CheckCapability(identity("velocity"), "eval-1",
			agentv1.Capability_CAPABILITY_SUBMIT_EVALUATION, nil, riskv1.FeatureWindow_FEATURE_WINDOW_UNSPECIFIED)
		if verdict != agentv1.CapabilityVerdict_CAPABILITY_VERDICT_ALLOWED {
			t.Fatalf("call %d: verdict = %v, want ALLOWED", i, verdict)
		}
	}

	verdict, _ := b.CheckCapability(identity("velocity"), "eval-1",
		agentv1.Capability_CAPABILITY_SUBMIT_EVALUATION, nil, riskv1.FeatureWindow_FEATURE_WINDOW_UNSPECIFIED)
	if verdict != agentv1.CapabilityVerdict_CAPABILITY_VERDICT_DENIED_BUDGET {
		t.Fatalf("verdict = %v, want DENIED_BUDGET", verdict)
	}
}

func TestBudgetIsScopedPerEvaluation(t *testing.T) {
	manifests := []*agentv1.AgentManifest{{
		Identity: identity("velocity"),
		Grants: []*agentv1.CapabilityGrant{
			{Capability: agentv1.Capability_CAPABILITY_SUBMIT_EVALUATION,
				Constraint: &agentv1.CapabilityConstraint{MaxInvocations: 1}},
		},
	}}
	b, _ := testBroker(t, manifests)

	if verdict, _ := b.CheckCapability(identity("velocity"), "eval-1",
		agentv1.Capability_CAPABILITY_SUBMIT_EVALUATION, nil, riskv1.FeatureWindow_FEATURE_WINDOW_UNSPECIFIED); verdict != agentv1.CapabilityVerdict_CAPABILITY_VERDICT_ALLOWED {
		t.Fatalf("eval-1: verdict = %v, want ALLOWED", verdict)
	}
	if verdict, _ := b.CheckCapability(identity("velocity"), "eval-2",
		agentv1.Capability_CAPABILITY_SUBMIT_EVALUATION, nil, riskv1.FeatureWindow_FEATURE_WINDOW_UNSPECIFIED); verdict != agentv1.CapabilityVerdict_CAPABILITY_VERDICT_ALLOWED {
		t.Fatalf("eval-2: verdict = %v, want ALLOWED (a different evaluation must not share eval-1's budget)", verdict)
	}
}

func TestAGrantedToolIsAllowed(t *testing.T) {
	b, _ := testBroker(t, Default())

	verdict, _ := b.CheckToolCapability(identity("device_investigation_agent"), "task-1", "lookup_device_history")
	if verdict != agentv1.CapabilityVerdict_CAPABILITY_VERDICT_ALLOWED {
		t.Fatalf("verdict = %v, want ALLOWED", verdict)
	}
}

func TestAnUngrantedToolIsDeniedNotGranted(t *testing.T) {
	b, _ := testBroker(t, Default())

	// device_investigation_agent is never granted the velocity agent's tool.
	verdict, _ := b.CheckToolCapability(identity("device_investigation_agent"), "task-1", "lookup_transaction_velocity")
	if verdict != agentv1.CapabilityVerdict_CAPABILITY_VERDICT_DENIED_NOT_GRANTED {
		t.Fatalf("verdict = %v, want DENIED_NOT_GRANTED", verdict)
	}
}

func TestAToolCallWithNoScopeIsDeniedScope(t *testing.T) {
	b, _ := testBroker(t, Default())

	verdict, _ := b.CheckToolCapability(identity("device_investigation_agent"), "", "lookup_device_history")
	if verdict != agentv1.CapabilityVerdict_CAPABILITY_VERDICT_DENIED_SCOPE {
		t.Fatalf("verdict = %v, want DENIED_SCOPE", verdict)
	}
}

func TestForgetDropsIdleBudgetCounters(t *testing.T) {
	manifests := []*agentv1.AgentManifest{{
		Identity: identity("velocity"),
		Grants: []*agentv1.CapabilityGrant{
			{Capability: agentv1.Capability_CAPABILITY_SUBMIT_EVALUATION,
				Constraint: &agentv1.CapabilityConstraint{MaxInvocations: 1}},
		},
	}}
	b, _ := testBroker(t, manifests)

	clock := time.Now()
	b.now = func() time.Time { return clock }

	b.CheckCapability(identity("velocity"), "eval-1",
		agentv1.Capability_CAPABILITY_SUBMIT_EVALUATION, nil, riskv1.FeatureWindow_FEATURE_WINDOW_UNSPECIFIED)

	clock = clock.Add(time.Hour)
	b.Forget(time.Minute)

	// The budget was forgotten, so the same evaluation may spend it again --
	// this is a real trade-off (a very stale evaluation's budget resets),
	// not a bug: see Forget's doc comment.
	verdict, _ := b.CheckCapability(identity("velocity"), "eval-1",
		agentv1.Capability_CAPABILITY_SUBMIT_EVALUATION, nil, riskv1.FeatureWindow_FEATURE_WINDOW_UNSPECIFIED)
	if verdict != agentv1.CapabilityVerdict_CAPABILITY_VERDICT_ALLOWED {
		t.Fatalf("verdict after Forget = %v, want ALLOWED", verdict)
	}
}
