//go:build e2e

package e2e

import (
	"testing"

	agentv1 "github.com/atesoglu/legion/protocol/gen/go/legion/agent/v1"
	dataplanev1 "github.com/atesoglu/legion/protocol/gen/go/legion/dataplane/v1"
	gatewayv1 "github.com/atesoglu/legion/protocol/gen/go/legion/gateway/v1"
	riskv1 "github.com/atesoglu/legion/protocol/gen/go/legion/risk/v1"
)

// startPipeline brings up the sentinel, the three engines and the orchestrator,
// wired to each other exactly as they are in a deployment.
func startPipeline(t *testing.T) *harness {
	t.Helper()

	h := newHarness(t)
	sentinel := h.startRust("legion-sentinel", "sentinel")
	velocity := h.startRust("legion-velocity", "velocity")
	device := h.startRust("legion-device", "device")
	geo := h.startRust("legion-geo", "geo")

	h.startGo("control-plane/orchestrator", "orchestrator", []string{
		"LEGION_AGENT_ENDPOINTS=velocity=" + velocity + ",device=" + device + ",geo=" + geo,
		"LEGION_SENTINEL_ENDPOINT=" + sentinel,
	})

	h.waitReady()
	return h
}

// TestATransactionSurvivesTheWholeChain is the test the repository did not
// have: Go dialling Rust, four services agreeing on one contract, and a
// decision coming back.
func TestATransactionSurvivesTheWholeChain(t *testing.T) {
	h := startPipeline(t)
	client := gatewayv1.NewDecisionServiceClient(h.dial(h.endpoints["orchestrator"]))

	ctx, cancel := callContext(t)
	defer cancel()

	response, err := client.EvaluateTransaction(ctx, &gatewayv1.EvaluateTransactionRequest{
		Transaction: transaction(),
	})
	if err != nil {
		t.Fatalf("EvaluateTransaction: %v", err)
	}

	if response.GetDecisionId() == "" {
		t.Error("no decision identifier was assigned")
	}

	outcome := response.GetOutcome()
	if outcome == nil {
		t.Fatal("no outcome was returned")
	}
	if outcome.GetDecision() == riskv1.Decision_DECISION_UNSPECIFIED {
		t.Error("the decision was left unspecified")
	}

	// Every registered agent must appear, whether or not it contributed.
	// An outage must never look like a policy with fewer agents in it.
	seen := map[string]bool{}
	for _, contribution := range outcome.GetContributions() {
		seen[contribution.GetAgentId()] = true
	}
	for _, agent := range []string{"velocity", "device", "geo", "behavioral"} {
		if !seen[agent] {
			t.Errorf("agent %q is missing from the contributions", agent)
		}
	}
}

// TestTheDecisionIsHonestAboutMissingFeatures pins current behaviour: nothing
// fetches features yet, so no deterministic rule can be evaluated, every agent
// reports a failure and the fallback policy decides.
//
// This assertion is expected to change when the feature store lands. That is
// the point: it will fail loudly rather than drift.
func TestTheDecisionIsHonestAboutMissingFeatures(t *testing.T) {
	h := startPipeline(t)
	client := gatewayv1.NewDecisionServiceClient(h.dial(h.endpoints["orchestrator"]))

	ctx, cancel := callContext(t)
	defer cancel()

	response, err := client.EvaluateTransaction(ctx, &gatewayv1.EvaluateTransactionRequest{
		Transaction: transaction(),
	})
	if err != nil {
		t.Fatalf("EvaluateTransaction: %v", err)
	}
	outcome := response.GetOutcome()

	if outcome.GetDegradation() != riskv1.DegradationState_DEGRADATION_STATE_FALLBACK {
		t.Errorf("degradation = %v, want FALLBACK", outcome.GetDegradation())
	}
	if outcome.GetDecision() != riskv1.Decision_DECISION_REVIEW {
		t.Errorf("decision = %v, want REVIEW", outcome.GetDecision())
	}
	if !outcome.GetPolicy().GetFallback() {
		t.Error("the outcome does not record that the fallback policy decided")
	}

	if !hasReason(outcome, riskv1.ReasonCode_REASON_CODE_FALLBACK_POLICY_USED) {
		t.Error("REASON_CODE_FALLBACK_POLICY_USED is missing")
	}
	if !hasReason(outcome, riskv1.ReasonCode_REASON_CODE_FEATURE_STORE_DEGRADED) {
		t.Error("REASON_CODE_FEATURE_STORE_DEGRADED is missing")
	}
}

// TestAnInvalidRequestIsRejectedBeforeAnyAgentIsConsulted checks the boundary
// rather than the pipeline: a malformed subject must not start an evaluation.
func TestAnInvalidRequestIsRejectedBeforeAnyAgentIsConsulted(t *testing.T) {
	h := startPipeline(t)
	client := gatewayv1.NewDecisionServiceClient(h.dial(h.endpoints["orchestrator"]))

	ctx, cancel := callContext(t)
	defer cancel()

	if _, err := client.EvaluateTransaction(ctx, &gatewayv1.EvaluateTransactionRequest{}); err == nil {
		t.Fatal("a request with no transaction was accepted")
	}
}

// TestAnEngineScoresOverTheWire proves an engine's rules survive serialisation:
// the same features that fire card testing in its unit tests must fire it
// across a gRPC boundary.
func TestAnEngineScoresOverTheWire(t *testing.T) {
	h := newHarness(t)
	velocity := h.startRust("legion-velocity", "velocity")
	h.waitReady()

	client := agentv1.NewAgentServiceClient(h.dial(velocity))

	ctx, cancel := callContext(t)
	defer cancel()

	response, err := client.Evaluate(ctx, &agentv1.EvaluateRequest{
		EvaluationId: "evaluation-1",
		Subject:      transaction(),
		Features:     cardTestingFeatures(),
	})
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}

	signal := response.GetSignal()
	if signal == nil {
		t.Fatalf("expected a signal, got failure %v", response.GetFailure())
	}
	if signal.GetAgentId() != "velocity" {
		t.Errorf("agent_id = %q, want velocity", signal.GetAgentId())
	}
	if signal.GetScore() != 85 {
		t.Errorf("score = %d, want 85", signal.GetScore())
	}
	if signal.GetConfidence() != 100 {
		t.Errorf("confidence = %d, want 100", signal.GetConfidence())
	}
	if len(signal.GetEvidence()) == 0 {
		t.Error("the signal carries no evidence")
	}
}

// TestAnEngineWithoutFeaturesReportsFailureNotSafety is the property that
// matters most operationally: silence during an outage must not read as
// "no risk observed".
func TestAnEngineWithoutFeaturesReportsFailureNotSafety(t *testing.T) {
	h := newHarness(t)
	velocity := h.startRust("legion-velocity", "velocity")
	h.waitReady()

	client := agentv1.NewAgentServiceClient(h.dial(velocity))

	ctx, cancel := callContext(t)
	defer cancel()

	response, err := client.Evaluate(ctx, &agentv1.EvaluateRequest{
		EvaluationId: "evaluation-1",
		Subject:      transaction(),
	})
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}

	if response.GetSignal() != nil {
		t.Fatalf("an unassessable evaluation returned a score of %d", response.GetSignal().GetScore())
	}
	if response.GetFailure() == nil {
		t.Fatal("no failure was reported")
	}
}

// TestTheDeviceEngineAssessesTheSubjectAlone covers the one engine that can
// answer with no features at all, because its integrity rule reads the subject.
func TestTheDeviceEngineAssessesTheSubjectAlone(t *testing.T) {
	h := newHarness(t)
	device := h.startRust("legion-device", "device")
	h.waitReady()

	client := agentv1.NewAgentServiceClient(h.dial(device))

	ctx, cancel := callContext(t)
	defer cancel()

	response, err := client.Evaluate(ctx, &agentv1.EvaluateRequest{
		EvaluationId: "evaluation-1",
		Subject:      compromisedDevice(),
	})
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}

	signal := response.GetSignal()
	if signal == nil {
		t.Fatalf("expected a signal, got failure %v", response.GetFailure())
	}
	if signal.GetScore() != 90 {
		t.Errorf("score = %d, want 90", signal.GetScore())
	}
	if signal.GetDegraded() != true {
		t.Error("a signal assessed without features should be marked degraded")
	}
}

// TestTheSentinelDecidesOverTheWire drives the decision authority directly with
// constructed evaluations, so the aggregation arithmetic is proven to survive
// the contract rather than only the unit tests.
func TestTheSentinelDecidesOverTheWire(t *testing.T) {
	h := newHarness(t)
	sentinel := h.startRust("legion-sentinel", "sentinel")
	h.waitReady()

	client := dataplanev1.NewSentinelServiceClient(h.dial(sentinel))

	cases := []struct {
		name  string
		score uint32
		want  riskv1.Decision
	}{
		{"quiet activity is allowed", 10, riskv1.Decision_DECISION_ALLOW},
		{"the review band holds", 50, riskv1.Decision_DECISION_REVIEW},
		{"a strong signal declines", 90, riskv1.Decision_DECISION_DECLINE},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			ctx, cancel := callContext(t)
			defer cancel()

			response, err := client.Decide(ctx, &dataplanev1.DecideRequest{
				EvaluationId: "evaluation-1",
				Subject:      transaction(),
				Evaluations:  agreeingAgents(testCase.score),
			})
			if err != nil {
				t.Fatalf("Decide: %v", err)
			}

			outcome := response.GetOutcome()
			if outcome == nil {
				t.Fatalf("expected an outcome, got failure %v", response.GetFailure())
			}
			if outcome.GetDecision() != testCase.want {
				t.Errorf("decision = %v, want %v", outcome.GetDecision(), testCase.want)
			}
			if outcome.GetAggregateScore() != testCase.score {
				t.Errorf("aggregate = %d, want %d", outcome.GetAggregateScore(), testCase.score)
			}
			if outcome.GetThresholds().GetReviewAt() != 40 {
				t.Errorf("review_at = %d, want 40", outcome.GetThresholds().GetReviewAt())
			}
		})
	}
}

// TestTheSentinelRenormalisesOverTheWire proves the property the decision model
// cares about most: a dead agent must not make a strong score look weak.
func TestTheSentinelRenormalisesOverTheWire(t *testing.T) {
	h := newHarness(t)
	sentinel := h.startRust("legion-sentinel", "sentinel")
	h.waitReady()

	client := dataplanev1.NewSentinelServiceClient(h.dial(sentinel))

	ctx, cancel := callContext(t)
	defer cancel()

	// Three of four agents agree on 80; the fourth is absent entirely.
	response, err := client.Decide(ctx, &dataplanev1.DecideRequest{
		EvaluationId: "evaluation-1",
		Subject:      transaction(),
		Evaluations:  agreeingAgents(80)[:3],
	})
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	outcome := response.GetOutcome()

	if got := outcome.GetAggregateScore(); got != 80 {
		t.Errorf("aggregate = %d, want 80: the missing agent's weight was not renormalised", got)
	}
	if outcome.GetDegradation() != riskv1.DegradationState_DEGRADATION_STATE_PARTIAL {
		t.Errorf("degradation = %v, want PARTIAL", outcome.GetDegradation())
	}
}

// agreeingAgents returns one signal per configured agent, all with the same
// score, so the aggregate equals that score whatever the weights are.
func agreeingAgents(score uint32) []*riskv1.AgentEvaluation {
	agents := []string{"velocity", "device", "geo", "behavioral"}
	out := make([]*riskv1.AgentEvaluation, 0, len(agents))
	for _, agent := range agents {
		out = append(out, &riskv1.AgentEvaluation{
			AgentId: agent,
			Result: &riskv1.AgentEvaluation_Signal{
				Signal: &riskv1.RiskSignal{
					AgentId:    agent,
					Score:      score,
					Confidence: 100,
				},
			},
		})
	}
	return out
}

func hasReason(outcome *riskv1.DecisionOutcome, want riskv1.ReasonCode) bool {
	for _, code := range outcome.GetReasonCodes() {
		if code == want {
			return true
		}
	}
	return false
}
