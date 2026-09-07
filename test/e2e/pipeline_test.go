//go:build e2e

package e2e

import (
	"context"
	"strings"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	agentv1 "github.com/atesoglu/legion/protocol/gen/go/legion/agent/v1"
	commonv1 "github.com/atesoglu/legion/protocol/gen/go/legion/common/v1"
	dataplanev1 "github.com/atesoglu/legion/protocol/gen/go/legion/dataplane/v1"
	gatewayv1 "github.com/atesoglu/legion/protocol/gen/go/legion/gateway/v1"
	riskv1 "github.com/atesoglu/legion/protocol/gen/go/legion/risk/v1"
)

// apiKey is the credential the harness configures the gateway with.
const apiKey = "e2e-caller-key-that-is-long-enough-to-pass"

// startPipeline brings up the feature store, the sentinel, the three engines,
// the orchestrator and the gateway, wired to each other exactly as they are in
// a deployment.
func startPipeline(t *testing.T, history []storedFeature) *harness {
	t.Helper()

	h := newHarness(t)
	store := h.startFeatureStore(history)
	sentinel := h.startRust("legion-sentinel", "sentinel")
	velocity := h.startRust("legion-velocity", "velocity")
	device := h.startRust("legion-device", "device")
	geo := h.startRust("legion-geo", "geo")

	orchestrator := h.startGo("control-plane/orchestrator", "orchestrator", []string{
		"LEGION_AGENT_ENDPOINTS=velocity=" + velocity + ",device=" + device + ",geo=" + geo,
		"LEGION_SENTINEL_ENDPOINT=" + sentinel,
		"LEGION_FEATURE_STORE=" + store,
	})

	h.startGo("gateway", "gateway", []string{
		"LEGION_ORCHESTRATOR_ENDPOINT=" + orchestrator,
		"LEGION_API_KEYS=e2e-caller=" + apiKey,
	})

	h.waitReady()
	return h
}

// authenticated returns a context carrying the harness's caller credential.
func authenticated(t *testing.T) (context.Context, context.CancelFunc) {
	t.Helper()
	ctx, cancel := callContext(t)
	return metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+apiKey), cancel
}

func gatewayClient(t *testing.T, h *harness) gatewayv1.DecisionServiceClient {
	t.Helper()
	return gatewayv1.NewDecisionServiceClient(h.dial(h.endpoints["gateway"]))
}

// decide sends one transaction through the entire platform, edge first.
func decide(t *testing.T, h *harness, subject *riskv1.Transaction) *gatewayv1.EvaluateTransactionResponse {
	t.Helper()

	ctx, cancel := authenticated(t)
	defer cancel()

	response, err := gatewayClient(t, h).EvaluateTransaction(ctx,
		&gatewayv1.EvaluateTransactionRequest{Transaction: subject})
	if err != nil {
		t.Fatalf("EvaluateTransaction: %v", err)
	}
	return response
}

// TestATransactionSurvivesTheWholeChain is the test the repository did not
// have: Go dialling Rust, five services agreeing on one contract, and a
// decision coming back.
func TestATransactionSurvivesTheWholeChain(t *testing.T) {
	h := startPipeline(t, settledHistory())
	response := decide(t, h, transaction())

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

// TestSettledHistoryIsAllowed proves evidence now reaches the engines: an
// unremarkable account scores low enough to allow, which was impossible while
// nothing fetched features.
func TestSettledHistoryIsAllowed(t *testing.T) {
	h := startPipeline(t, settledHistory())
	outcome := decide(t, h, transaction()).GetOutcome()

	if outcome.GetDecision() != riskv1.Decision_DECISION_ALLOW {
		t.Errorf("decision = %v, want ALLOW (aggregate %d)",
			outcome.GetDecision(), outcome.GetAggregateScore())
	}
	if outcome.GetPolicy().GetFallback() {
		t.Error("the fallback policy decided despite sufficient evidence")
	}

	// The deterministic engines contributed; only the unregistered behavioural
	// agent is missing, so the evaluation is partial rather than clean.
	for _, contribution := range outcome.GetContributions() {
		if contribution.GetAgentId() == "behavioral" {
			continue
		}
		if !contribution.GetIncluded() {
			t.Errorf("agent %q was excluded: %v",
				contribution.GetAgentId(), contribution.GetExclusionReason())
		}
	}
}

// TestCardTestingHistoryRaisesTheDecision is the end-to-end proof that evidence
// changes outcomes: the same subject, with a different history in the store,
// crosses from ALLOW into REVIEW.
func TestCardTestingHistoryRaisesTheDecision(t *testing.T) {
	h := startPipeline(t, cardTestingHistory())
	outcome := decide(t, h, transaction()).GetOutcome()

	if outcome.GetDecision() != riskv1.Decision_DECISION_REVIEW {
		t.Errorf("decision = %v, want REVIEW (aggregate %d)",
			outcome.GetDecision(), outcome.GetAggregateScore())
	}
	if !hasReason(outcome, riskv1.ReasonCode_REASON_CODE_CARD_TESTING_PATTERN) {
		t.Errorf("card testing was not among the reasons: %v", outcome.GetReasonCodes())
	}

	// The reason must be attributable to the agent that found it.
	for _, contribution := range outcome.GetContributions() {
		if contribution.GetAgentId() == "velocity" && contribution.GetScore() != 85 {
			t.Errorf("velocity scored %d, want 85", contribution.GetScore())
		}
	}
}

// TestAnEmptyStoreIsAbsenceNotIgnorance covers the newest account there can be.
// The store answers, and answers that there is no history; that is a fact the
// engines can act on, not a reason to stop deciding.
func TestAnEmptyStoreIsAbsenceNotIgnorance(t *testing.T) {
	h := startPipeline(t, nil)
	outcome := decide(t, h, transaction()).GetOutcome()

	if outcome.GetPolicy().GetFallback() {
		t.Error("an account with no history fell back instead of being assessed")
	}
	if hasReason(outcome, riskv1.ReasonCode_REASON_CODE_FEATURE_STORE_DEGRADED) {
		t.Error("absence of history was reported as store degradation")
	}
	// A device with no history at all is new, which is a real observation.
	if !hasReason(outcome, riskv1.ReasonCode_REASON_CODE_NEW_DEVICE) {
		t.Errorf("a first-ever device was not reported as new: %v", outcome.GetReasonCodes())
	}
}

// TestAnInvalidRequestIsRejectedBeforeAnyAgentIsConsulted checks the boundary
// rather than the pipeline: a malformed subject must not start an evaluation.
func TestAnInvalidRequestIsRejectedBeforeAnyAgentIsConsulted(t *testing.T) {
	h := startPipeline(t, settledHistory())

	ctx, cancel := authenticated(t)
	defer cancel()

	_, err := gatewayClient(t, h).EvaluateTransaction(ctx,
		&gatewayv1.EvaluateTransactionRequest{})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("error = %v, want InvalidArgument", err)
	}
}

// TestAnUnauthenticatedCallerReachesNothing is the edge's first job: Zone 0
// must prove who it is before consuming any platform resource.
func TestAnUnauthenticatedCallerReachesNothing(t *testing.T) {
	h := startPipeline(t, settledHistory())

	ctx, cancel := callContext(t)
	defer cancel()

	_, err := gatewayClient(t, h).EvaluateTransaction(ctx,
		&gatewayv1.EvaluateTransactionRequest{Transaction: transaction()})
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("error = %v, want Unauthenticated", err)
	}
}

func TestAWrongCredentialIsRefused(t *testing.T) {
	h := startPipeline(t, settledHistory())

	base, cancel := callContext(t)
	defer cancel()
	ctx := metadata.AppendToOutgoingContext(base,
		"authorization", "Bearer not-the-configured-key-but-long-enough")

	_, err := gatewayClient(t, h).EvaluateTransaction(ctx,
		&gatewayv1.EvaluateTransactionRequest{Transaction: transaction()})
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("error = %v, want Unauthenticated", err)
	}
}

// TestAnUnpseudonymisedIdentifierIsRefusedAtTheEdge is the control that keeps
// cardholder data out of the platform entirely.
func TestAnUnpseudonymisedIdentifierIsRefusedAtTheEdge(t *testing.T) {
	h := startPipeline(t, settledHistory())

	subject := transaction()
	subject.Instrument = &riskv1.Instrument{
		Id: &commonv1.PseudonymousId{
			Value:      "4111111111111111",
			KeyVersion: "v1",
			Domain:     commonv1.IdentifierDomain_IDENTIFIER_DOMAIN_INSTRUMENT,
		},
	}

	ctx, cancel := authenticated(t)
	defer cancel()

	_, err := gatewayClient(t, h).EvaluateTransaction(ctx,
		&gatewayv1.EvaluateTransactionRequest{Transaction: subject})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("error = %v, want InvalidArgument", err)
	}
	if strings.Contains(err.Error(), "4111111111111111") {
		t.Fatal("the rejection echoed the value that must not enter the platform")
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
