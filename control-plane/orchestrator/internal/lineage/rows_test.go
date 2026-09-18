package lineage

import (
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	commonv1 "github.com/atesoglu/legion/protocol/gen/go/legion/common/v1"
	riskv1 "github.com/atesoglu/legion/protocol/gen/go/legion/risk/v1"
)

func sampleEntry() *riskv1.DecisionLineage {
	return &riskv1.DecisionLineage{
		DecisionId:    "d-1",
		TransactionId: &commonv1.PseudonymousId{Value: "txn-1"},
		DecidedAt:     timestamppb.New(time.Unix(1_700_000_000, 0)),
		Shadow:        true,
		Deadline:      durationpb.New(80 * time.Millisecond),
		TotalElapsed:  durationpb.New(42 * time.Millisecond),
		Outcome: &riskv1.DecisionOutcome{
			Decision:       riskv1.Decision_DECISION_REVIEW,
			AggregateScore: 55,
			Degradation:    riskv1.DegradationState_DEGRADATION_STATE_PARTIAL,
			Policy: &riskv1.PolicyRef{
				PolicyId: "payments-default",
				Version:  &commonv1.Version{Name: "payments-default", Version: "0.1.0"},
				Fallback: false,
			},
			Contributions: []*riskv1.SignalContribution{
				{AgentId: "velocity", Score: 85, WeightBasisPoints: 3000, WeightedContribution: 2550, Included: true},
				{AgentId: "device", Score: 0, WeightBasisPoints: 0, WeightedContribution: 0,
					Included: false, ExclusionReason: riskv1.ReasonCode_REASON_CODE_AGENT_TIMEOUT},
			},
		},
		Evaluations: []*riskv1.AgentEvaluation{
			{
				AgentId:         "velocity",
				Result:          &riskv1.AgentEvaluation_Signal{Signal: &riskv1.RiskSignal{Score: 85, Confidence: 90, AgentVersion: &commonv1.Version{Name: "velocity", Version: "1.2.3"}}},
				ObservedLatency: durationpb.New(3 * time.Millisecond),
			},
			{
				AgentId: "device",
				Result: &riskv1.AgentEvaluation_Failure{Failure: &commonv1.Failure{
					Kind: commonv1.FailureKind_FAILURE_KIND_DEADLINE_EXCEEDED, Component: "device", Message: "timed out", Retryable: true,
				}},
				ObservedLatency: durationpb.New(8 * time.Millisecond),
			},
		},
		Spans: []*riskv1.ExecutionSpan{
			{Stage: "feature_fetch", Elapsed: durationpb.New(5 * time.Millisecond), Budget: durationpb.New(12 * time.Millisecond)},
		},
		Failures: []*commonv1.Failure{
			{Kind: commonv1.FailureKind_FAILURE_KIND_DEADLINE_EXCEEDED, Component: "device", Message: "timed out", Retryable: true},
		},
		Versions: &riskv1.GovernedVersions{
			Agents:   []*commonv1.Version{{Name: "velocity", Version: "1.2.3"}},
			Policy:   &commonv1.Version{Name: "payments-default", Version: "0.1.0"},
			Contract: &commonv1.Version{Name: "contract", Version: "v1"},
			// Model/Prompt/OutputSchema left unset: the behavioural agent did not run.
		},
	}
}

func TestBuildRowsMapsTheDecisionRow(t *testing.T) {
	r := buildRows(sampleEntry(), []byte("raw"))

	d := r.decision
	if d.ID != "d-1" || d.TransactionID != "txn-1" {
		t.Fatalf("decision identifiers = %+v", d)
	}
	if d.Decision != "DECISION_REVIEW" {
		t.Errorf("decision = %q, want DECISION_REVIEW", d.Decision)
	}
	if d.AggregateScore != 55 {
		t.Errorf("aggregate_score = %d, want 55", d.AggregateScore)
	}
	if d.PolicyID != "payments-default" || d.PolicyVersion != "0.1.0" {
		t.Errorf("policy = %+v", d)
	}
	if !d.Shadow {
		t.Error("shadow was not carried through")
	}
	if d.DeadlineNs != int64(80*time.Millisecond) || d.TotalElapsedNs != int64(42*time.Millisecond) {
		t.Errorf("deadline_ns/total_elapsed_ns = %d/%d, want %d/%d",
			d.DeadlineNs, d.TotalElapsedNs, int64(80*time.Millisecond), int64(42*time.Millisecond))
	}
	if string(d.RawLineage) != "raw" {
		t.Errorf("raw_lineage = %q, want %q", d.RawLineage, "raw")
	}
}

func TestBuildRowsMergesEvaluationsWithContributions(t *testing.T) {
	r := buildRows(sampleEntry(), nil)

	if len(r.agentEvaluations) != 2 {
		t.Fatalf("agent evaluations = %d, want 2", len(r.agentEvaluations))
	}

	var velocity, device *agentEvaluationRow
	for i := range r.agentEvaluations {
		switch r.agentEvaluations[i].AgentID {
		case "velocity":
			velocity = &r.agentEvaluations[i]
		case "device":
			device = &r.agentEvaluations[i]
		}
	}
	if velocity == nil || device == nil {
		t.Fatalf("missing agent rows: %+v", r.agentEvaluations)
	}

	if velocity.OutcomeKind != "SIGNAL" || velocity.Score != 85 || velocity.Confidence != 90 || !velocity.Included {
		t.Errorf("velocity row = %+v", velocity)
	}
	if velocity.WeightedContribution != 2550 || velocity.AgentVersion != "1.2.3" {
		t.Errorf("velocity row = %+v", velocity)
	}

	if device.OutcomeKind != "FAILURE" || device.Included {
		t.Errorf("device row = %+v", device)
	}
	if device.FailureKind != "FAILURE_KIND_DEADLINE_EXCEEDED" || device.FailureComponent != "device" {
		t.Errorf("device failure fields = %+v", device)
	}
	if device.ExclusionReason != "REASON_CODE_AGENT_TIMEOUT" {
		t.Errorf("device exclusion_reason = %q, want REASON_CODE_AGENT_TIMEOUT", device.ExclusionReason)
	}
}

func TestBuildRowsCarriesSpansAndFailures(t *testing.T) {
	r := buildRows(sampleEntry(), nil)

	if len(r.executionSpans) != 1 || r.executionSpans[0].Stage != "feature_fetch" {
		t.Fatalf("execution spans = %+v", r.executionSpans)
	}
	if got := r.executionSpans[0].ElapsedNs; got != int64(5*time.Millisecond) {
		t.Errorf("elapsed_ns = %d, want %d", got, int64(5*time.Millisecond))
	}
	if len(r.decisionFailures) != 1 || r.decisionFailures[0].Component != "device" {
		t.Fatalf("decision failures = %+v", r.decisionFailures)
	}
}

// TestBuildRowsKeepsSubMillisecondDurations is the regression this schema's
// second migration exists for: every stage of an 80 ms budget runs in
// hundreds of microseconds, and the millisecond columns this replaced
// rounded all of them to zero (docs/deadline-model.md section 8).
func TestBuildRowsKeepsSubMillisecondDurations(t *testing.T) {
	entry := sampleEntry()
	entry.TotalElapsed = durationpb.New(1616 * time.Microsecond)
	entry.Spans = []*riskv1.ExecutionSpan{
		{Stage: "sentinel", Elapsed: durationpb.New(550 * time.Microsecond), Budget: durationpb.New(3 * time.Millisecond)},
	}
	entry.Evaluations[0].ObservedLatency = durationpb.New(237 * time.Microsecond)

	r := buildRows(entry, nil)

	if got := r.decision.TotalElapsedNs; got != 1_616_000 {
		t.Errorf("total_elapsed_ns = %d, want 1616000", got)
	}
	if got := r.executionSpans[0].ElapsedNs; got != 550_000 {
		t.Errorf("elapsed_ns = %d, want 550000", got)
	}
	var velocity int64
	for _, e := range r.agentEvaluations {
		if e.AgentID == "velocity" {
			velocity = e.ObservedLatencyNs
		}
	}
	if velocity != 237_000 {
		t.Errorf("observed_latency_ns = %d, want 237000", velocity)
	}
}

func TestBuildRowsOmitsGovernedArtefactsThatDidNotParticipate(t *testing.T) {
	r := buildRows(sampleEntry(), nil)

	seen := map[string]bool{}
	for _, v := range r.governedVersions {
		seen[v.Artefact] = true
	}
	if !seen["agent"] || !seen["policy"] || !seen["contract"] {
		t.Fatalf("expected agent/policy/contract rows, got %+v", r.governedVersions)
	}
	if seen["model"] || seen["prompt"] || seen["output_schema"] {
		t.Fatalf("model/prompt/output_schema should be absent when the behavioural agent did not run: %+v", r.governedVersions)
	}
}
