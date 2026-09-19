package evaluation

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/atesoglu/legion/control-plane/orchestrator/internal/breaker"
	"github.com/atesoglu/legion/control-plane/orchestrator/internal/budget"
	"github.com/atesoglu/legion/control-plane/orchestrator/internal/features"
	"github.com/atesoglu/legion/control-plane/orchestrator/internal/registry"
	agentv1 "github.com/atesoglu/legion/protocol/gen/go/legion/agent/v1"
	commonv1 "github.com/atesoglu/legion/protocol/gen/go/legion/common/v1"
	dataplanev1 "github.com/atesoglu/legion/protocol/gen/go/legion/dataplane/v1"
	investigationv1 "github.com/atesoglu/legion/protocol/gen/go/legion/investigation/v1"
	riskv1 "github.com/atesoglu/legion/protocol/gen/go/legion/risk/v1"
)

type fakeAgent struct {
	score        int32
	err          error
	delay        time.Duration
	version      *commonv1.Version
	calls        atomic.Int32
	lastFeatures atomic.Int32
}

func (f *fakeAgent) Evaluate(ctx context.Context, in *agentv1.EvaluateRequest) (*agentv1.EvaluateResponse, error) {
	f.calls.Add(1)
	f.lastFeatures.Store(int32(len(in.GetFeatures().GetFeatures())))
	if f.delay > 0 {
		select {
		case <-time.After(f.delay):
		case <-ctx.Done():
			return nil, status.Error(codes.DeadlineExceeded, "deadline")
		}
	}
	if f.err != nil {
		return nil, f.err
	}
	return &agentv1.EvaluateResponse{
		Result: &agentv1.EvaluateResponse_Signal{
			Signal: &riskv1.RiskSignal{Score: uint32(f.score), Confidence: 100, AgentVersion: f.version},
		},
	}, nil
}

type fakeSentinel struct {
	seen     []*riskv1.AgentEvaluation
	degrade  bool
	err      error
	decision riskv1.Decision
}

func (f *fakeSentinel) Decide(_ context.Context, in *dataplanev1.DecideRequest) (*dataplanev1.DecideResponse, error) {
	if f.err != nil {
		return nil, f.err
	}
	f.seen = in.GetEvaluations()
	f.degrade = in.GetFeatureStoreDegraded()
	decision := f.decision
	if decision == riskv1.Decision_DECISION_UNSPECIFIED {
		decision = riskv1.Decision_DECISION_ALLOW
	}
	return &dataplanev1.DecideResponse{
		Result: &dataplanev1.DecideResponse_Outcome{
			Outcome: &riskv1.DecisionOutcome{Decision: decision},
		},
	}, nil
}

type fakeStore struct {
	features []*riskv1.Feature
	err      error
	calls    atomic.Int32
}

func (f *fakeStore) Fetch(_ context.Context, requests []features.Request) ([]*riskv1.Feature, error) {
	f.calls.Add(1)
	if f.err != nil {
		return nil, f.err
	}
	if f.features != nil {
		return f.features, nil
	}
	// Default: every requested feature is present and fresh.
	out := make([]*riskv1.Feature, 0, len(requests))
	for _, request := range requests {
		out = append(out, &riskv1.Feature{
			Name:      request.Name,
			Window:    request.Window,
			Value:     &riskv1.FeatureValue{Kind: &riskv1.FeatureValue_Count{Count: 1}},
			Freshness: riskv1.FeatureFreshness_FEATURE_FRESHNESS_FRESH,
		})
	}
	return out, nil
}

type fakeLineageStore struct {
	mu       sync.Mutex
	recorded []*riskv1.DecisionLineage
}

func (f *fakeLineageStore) Record(entry *riskv1.DecisionLineage) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.recorded = append(f.recorded, entry)
}

func (f *fakeLineageStore) entries() []*riskv1.DecisionLineage {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]*riskv1.DecisionLineage(nil), f.recorded...)
}

type fakeCaseTriggerPublisher struct {
	mu        sync.Mutex
	published []*investigationv1.CaseTrigger
}

func (f *fakeCaseTriggerPublisher) Publish(_ context.Context, trigger *investigationv1.CaseTrigger) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.published = append(f.published, trigger)
}

func (f *fakeCaseTriggerPublisher) triggers() []*investigationv1.CaseTrigger {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]*investigationv1.CaseTrigger(nil), f.published...)
}

func subject() *riskv1.Transaction {
	return &riskv1.Transaction{
		Account: &riskv1.Account{Id: &commonv1.PseudonymousId{Value: "acct-1"}},
		Device:  &riskv1.Device{Id: &commonv1.PseudonymousId{Value: "dev-1"}},
	}
}

func agents() *registry.Registry {
	r, err := registry.New([]registry.Agent{
		{ID: "velocity", Endpoint: "v:1"},
		{ID: "device", Endpoint: "d:1"},
		{ID: "geo", Endpoint: "g:1"},
	})
	if err != nil {
		panic(err)
	}
	return r
}

func coordinator(t *testing.T, clients map[string]AgentClient, sentinel SentinelClient) *Coordinator {
	t.Helper()
	return coordinatorWithStore(t, clients, sentinel, &fakeStore{})
}

func coordinatorWithStore(
	t *testing.T,
	clients map[string]AgentClient,
	sentinel SentinelClient,
	store features.Store,
) *Coordinator {
	t.Helper()
	c, err := New(Options{
		Registry:        agents(),
		Clients:         clients,
		Sentinel:        sentinel,
		Store:           store,
		Lineage:         &fakeLineageStore{},
		CaseTriggers:    &fakeCaseTriggerPublisher{},
		RequestDeadline: 80 * time.Millisecond,
		Breaker:         breaker.Default(),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

func healthy() map[string]AgentClient {
	return map[string]AgentClient{
		"velocity": &fakeAgent{score: 10},
		"device":   &fakeAgent{score: 20},
		"geo":      &fakeAgent{score: 30},
	}
}

func TestNewRefusesARegisteredAgentWithNoClient(t *testing.T) {
	_, err := New(Options{
		Registry:     agents(),
		Clients:      map[string]AgentClient{"velocity": &fakeAgent{}},
		Sentinel:     &fakeSentinel{},
		Store:        &fakeStore{},
		Lineage:      &fakeLineageStore{},
		CaseTriggers: &fakeCaseTriggerPublisher{},
	})
	if err == nil {
		t.Fatal("New accepted a registry it could not serve")
	}
}

func TestNewRefusesToRunWithoutAFeatureStore(t *testing.T) {
	_, err := New(Options{
		Registry:     agents(),
		Clients:      healthy(),
		Sentinel:     &fakeSentinel{},
		Lineage:      &fakeLineageStore{},
		CaseTriggers: &fakeCaseTriggerPublisher{},
	})
	if err == nil {
		t.Fatal("New accepted a coordinator with no feature store")
	}
}

func TestNewRefusesToRunWithoutALineageStore(t *testing.T) {
	_, err := New(Options{
		Registry:     agents(),
		Clients:      healthy(),
		Sentinel:     &fakeSentinel{},
		Store:        &fakeStore{},
		CaseTriggers: &fakeCaseTriggerPublisher{},
	})
	if err == nil {
		t.Fatal("New accepted a coordinator with no lineage store")
	}
}

func TestNewRefusesToRunWithoutACaseTriggerPublisher(t *testing.T) {
	_, err := New(Options{
		Registry: agents(),
		Clients:  healthy(),
		Sentinel: &fakeSentinel{},
		Store:    &fakeStore{},
		Lineage:  &fakeLineageStore{},
	})
	if err == nil {
		t.Fatal("New accepted a coordinator with no case trigger publisher")
	}
}

func TestEveryRegisteredAgentIsConsulted(t *testing.T) {
	clients := healthy()
	sentinel := &fakeSentinel{}
	c := coordinator(t, clients, sentinel)

	if _, _, err := c.Evaluate(context.Background(), evaluationOf(&riskv1.Transaction{})); err != nil {
		t.Fatalf("Evaluate: %v", err)
	}

	if got := len(sentinel.seen); got != 3 {
		t.Fatalf("evaluations = %d, want 3", got)
	}
	for id, client := range clients {
		if n := client.(*fakeAgent).calls.Load(); n != 1 {
			t.Fatalf("agent %s called %d times, want 1", id, n)
		}
	}
}

func TestEvaluationsReachTheSentinelInAgentOrder(t *testing.T) {
	sentinel := &fakeSentinel{}
	c := coordinator(t, healthy(), sentinel)

	if _, _, err := c.Evaluate(context.Background(), evaluationOf(&riskv1.Transaction{})); err != nil {
		t.Fatalf("Evaluate: %v", err)
	}

	want := []string{"device", "geo", "velocity"}
	for i, id := range want {
		if got := sentinel.seen[i].GetAgentId(); got != id {
			t.Fatalf("evaluation %d = %q, want %q", i, got, id)
		}
	}
}

func TestAFailedAgentIsReportedRatherThanOmitted(t *testing.T) {
	clients := healthy()
	clients["geo"] = &fakeAgent{err: status.Error(codes.Unavailable, "down")}
	sentinel := &fakeSentinel{}
	c := coordinator(t, clients, sentinel)

	if _, _, err := c.Evaluate(context.Background(), evaluationOf(&riskv1.Transaction{})); err != nil {
		t.Fatalf("Evaluate: %v", err)
	}

	// An outage must not look like a policy with fewer agents in it.
	if got := len(sentinel.seen); got != 3 {
		t.Fatalf("evaluations = %d, want 3", got)
	}
	var geo *riskv1.AgentEvaluation
	for _, evaluation := range sentinel.seen {
		if evaluation.GetAgentId() == "geo" {
			geo = evaluation
		}
	}
	if geo.GetFailure().GetKind() != commonv1.FailureKind_FAILURE_KIND_DEPENDENCY_UNAVAILABLE {
		t.Fatalf("geo failure = %v, want dependency unavailable", geo.GetFailure().GetKind())
	}
}

func TestASlowAgentDoesNotDelayTheOthersBeyondTheWindow(t *testing.T) {
	clients := healthy()
	clients["geo"] = &fakeAgent{score: 30, delay: time.Second}
	sentinel := &fakeSentinel{}
	c := coordinator(t, clients, sentinel)

	started := time.Now()
	if _, _, err := c.Evaluate(context.Background(), evaluationOf(&riskv1.Transaction{})); err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	elapsed := time.Since(started)

	// The window closes when it closes; a slow agent cannot extend it.
	if elapsed > 200*time.Millisecond {
		t.Fatalf("evaluation took %v, want the agent window to bound it", elapsed)
	}
	if len(sentinel.seen) != 3 {
		t.Fatalf("evaluations = %d, want 3", len(sentinel.seen))
	}
}

func TestAnOpenBreakerSkipsTheCallEntirely(t *testing.T) {
	clients := healthy()
	broken := &fakeAgent{err: status.Error(codes.Unavailable, "down")}
	clients["geo"] = broken

	c, err := New(Options{
		Registry:        agents(),
		Clients:         clients,
		Sentinel:        &fakeSentinel{},
		Store:           &fakeStore{},
		Lineage:         &fakeLineageStore{},
		CaseTriggers:    &fakeCaseTriggerPublisher{},
		RequestDeadline: 80 * time.Millisecond,
		Breaker: breaker.Config{
			MinimumRequests: 2,
			FailureRatio:    0.5,
			Window:          time.Minute,
			Cooldown:        time.Minute,
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	for range 3 {
		if _, _, err := c.Evaluate(context.Background(), evaluationOf(&riskv1.Transaction{})); err != nil {
			t.Fatalf("Evaluate: %v", err)
		}
	}

	// Two calls trip the breaker; the third must not be attempted.
	if got := broken.calls.Load(); got != 2 {
		t.Fatalf("broken agent called %d times, want 2", got)
	}
}

func TestAnExhaustedBudgetSkipsAgentsButStillDecides(t *testing.T) {
	clients := healthy()
	sentinel := &fakeSentinel{}
	c := coordinator(t, clients, sentinel)

	// Only the sentinel's protected share is left.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Millisecond)
	defer cancel()

	outcome, _, err := c.Evaluate(ctx, evaluationOf(&riskv1.Transaction{}))
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if outcome == nil {
		t.Fatal("no outcome produced")
	}
	for id, client := range clients {
		if n := client.(*fakeAgent).calls.Load(); n != 0 {
			t.Fatalf("agent %s called %d times with no budget, want 0", id, n)
		}
	}
	if len(sentinel.seen) != 3 {
		t.Fatalf("evaluations = %d, want every agent recorded as skipped", len(sentinel.seen))
	}
}

func TestAnExpiredDeadlineProducesAnErrorNotADecision(t *testing.T) {
	c := coordinator(t, healthy(), &fakeSentinel{})

	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()

	if _, _, err := c.Evaluate(ctx, evaluationOf(&riskv1.Transaction{})); status.Code(err) != codes.DeadlineExceeded {
		t.Fatalf("error = %v, want DeadlineExceeded", err)
	}
}

func TestAnUnavailableSentinelIsAnErrorNotAnAllow(t *testing.T) {
	c := coordinator(t, healthy(), &fakeSentinel{err: errors.New("down")})

	if _, _, err := c.Evaluate(context.Background(), evaluationOf(&riskv1.Transaction{})); err == nil {
		t.Fatal("Evaluate returned a decision without the sentinel")
	}
}

func TestFeaturesReachTheAgentsThatNeedThem(t *testing.T) {
	clients := healthy()
	store := &fakeStore{}
	c := coordinatorWithStore(t, clients, &fakeSentinel{}, store)

	if _, _, err := c.Evaluate(context.Background(), evaluationOf(subject())); err != nil {
		t.Fatalf("Evaluate: %v", err)
	}

	if store.calls.Load() != 1 {
		t.Fatalf("store fetched %d times, want 1", store.calls.Load())
	}
	for id, client := range clients {
		if got := client.(*fakeAgent).lastFeatures.Load(); got == 0 {
			t.Fatalf("agent %s received no features", id)
		}
	}
}

func TestFreshEvidenceIsNotReportedAsDegraded(t *testing.T) {
	sentinel := &fakeSentinel{}
	c := coordinatorWithStore(t, healthy(), sentinel, &fakeStore{})

	if _, _, err := c.Evaluate(context.Background(), evaluationOf(subject())); err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if sentinel.degrade {
		t.Fatal("a fully fresh feature set was reported as degraded")
	}
}

func TestAStoreOutageDegradesTheDecisionRatherThanFailingIt(t *testing.T) {
	clients := healthy()
	sentinel := &fakeSentinel{}
	c := coordinatorWithStore(t, clients, sentinel, &fakeStore{err: errors.New("store down")})

	outcome, _, err := c.Evaluate(context.Background(), evaluationOf(subject()))
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if outcome == nil {
		t.Fatal("a store outage produced no decision")
	}
	if !sentinel.degrade {
		t.Error("the sentinel was not told the feature store was unavailable")
	}
	// Agents must still be consulted: some can assess the subject alone.
	for id, client := range clients {
		if client.(*fakeAgent).calls.Load() != 1 {
			t.Errorf("agent %s was not consulted during a store outage", id)
		}
	}
}

func TestStaleEvidenceIsReportedAsDegraded(t *testing.T) {
	sentinel := &fakeSentinel{}
	store := &fakeStore{features: []*riskv1.Feature{{
		Name:      "transactions",
		Freshness: riskv1.FeatureFreshness_FEATURE_FRESHNESS_STALE,
	}}}
	c := coordinatorWithStore(t, healthy(), sentinel, store)

	if _, _, err := c.Evaluate(context.Background(), evaluationOf(subject())); err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if !sentinel.degrade {
		t.Fatal("a stale feature set was not reported as degraded")
	}
}

func TestThePlanDefaultsWhenUnset(t *testing.T) {
	c := coordinator(t, healthy(), &fakeSentinel{})
	if c.plan != budget.Default() {
		t.Fatalf("plan = %+v, want the documented default", c.plan)
	}
}

func TestEvaluateRecordsLineageForEveryDecision(t *testing.T) {
	sentinel := &fakeSentinel{}
	c := coordinator(t, healthy(), sentinel)

	outcome, lineage, err := c.Evaluate(context.Background(), evaluationOf(subject()))
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}

	store := c.lineage.(*fakeLineageStore)
	entries := store.entries()
	if len(entries) != 1 {
		t.Fatalf("recorded lineage entries = %d, want 1", len(entries))
	}
	if entries[0] != lineage {
		t.Fatal("the entry persisted is not the entry returned to the caller")
	}

	entry := entries[0]
	if entry.GetDecisionId() != "e1" {
		t.Errorf("decision_id = %q, want %q", entry.GetDecisionId(), "e1")
	}
	if entry.GetOutcome() != outcome {
		t.Error("lineage outcome is not the decision that was returned")
	}
	if len(entry.GetEvaluations()) != 3 {
		t.Fatalf("lineage evaluations = %d, want 3", len(entry.GetEvaluations()))
	}
	if len(entry.GetSpans()) != 3 {
		t.Fatalf("lineage spans = %d, want 3 (feature_fetch, deterministic_agents, sentinel)", len(entry.GetSpans()))
	}
	if entry.GetDeadline().AsDuration() <= 0 {
		t.Error("lineage deadline was not recorded")
	}
	// Not asserted > 0: an in-memory evaluation against fakes can complete
	// within the host clock's resolution, so only presence is checked here.
	if entry.GetTotalElapsed() == nil {
		t.Error("lineage total_elapsed was not recorded")
	}
}

func TestLineageGovernedVersionsCarryThePolicyAndCatalogue(t *testing.T) {
	sentinel := &fakeSentinel{}
	c := coordinator(t, healthy(), sentinel)

	_, lineage, err := c.Evaluate(context.Background(), evaluationOf(subject()))
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}

	versions := lineage.GetVersions()
	if versions.GetFeatureCatalogue().GetVersion() != features.CatalogueVersion {
		t.Errorf("feature_catalogue version = %q, want %q",
			versions.GetFeatureCatalogue().GetVersion(), features.CatalogueVersion)
	}
	if versions.GetContract() == nil {
		t.Error("contract version was not recorded")
	}
	// The fake sentinel does not populate a policy ref; a real one always
	// does (see decide.rs), so this only proves the field is wired through.
	if versions.GetModel() != nil || versions.GetPrompt() != nil || versions.GetOutputSchema() != nil {
		t.Error("model/prompt/output-schema versions were set despite no behavioural agent existing")
	}
}

func TestLineageRecordsAgentVersionsFromSuccessfulSignalsOnly(t *testing.T) {
	clients := healthy()
	clients["velocity"] = &fakeAgent{score: 10, version: &commonv1.Version{Name: "velocity", Version: "1.2.3"}}
	clients["geo"] = &fakeAgent{err: errors.New("down")}
	sentinel := &fakeSentinel{}
	c := coordinator(t, clients, sentinel)

	_, lineage, err := c.Evaluate(context.Background(), evaluationOf(subject()))
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}

	// The failed agent (geo) and the version-less one (device) contribute no
	// version; only the one that actually answered with a version does.
	versions := lineage.GetVersions().GetAgents()
	if len(versions) != 1 {
		t.Fatalf("agent versions = %d, want 1", len(versions))
	}
	if versions[0].GetName() != "velocity" || versions[0].GetVersion() != "1.2.3" {
		t.Fatalf("agent version = %+v, want velocity 1.2.3", versions[0])
	}
}

func TestLineageIncludesAFailureForEveryFailedAgent(t *testing.T) {
	clients := healthy()
	clients["geo"] = &fakeAgent{err: errors.New("down")}
	sentinel := &fakeSentinel{}
	c := coordinator(t, clients, sentinel)

	_, lineage, err := c.Evaluate(context.Background(), evaluationOf(subject()))
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}

	if len(lineage.GetFailures()) != 1 {
		t.Fatalf("lineage failures = %d, want 1", len(lineage.GetFailures()))
	}
}

func TestLineageIsRecordedEvenWhenNotReturnedInline(t *testing.T) {
	// include_lineage is a server/gateway-level concern (whether it is
	// attached to the response); the coordinator always builds and persists
	// it, since replay and evaluation need every decision, not only the ones
	// a caller asked to see inline.
	sentinel := &fakeSentinel{}
	c := coordinator(t, healthy(), sentinel)

	_, lineage, err := c.Evaluate(context.Background(), evaluationOf(subject()))
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if lineage == nil {
		t.Fatal("Evaluate returned no lineage")
	}
	if len(c.lineage.(*fakeLineageStore).entries()) != 1 {
		t.Fatal("lineage was returned but not persisted")
	}
}

func TestAReviewDecisionPublishesACaseTrigger(t *testing.T) {
	sentinel := &fakeSentinel{decision: riskv1.Decision_DECISION_REVIEW}
	c := coordinator(t, healthy(), sentinel)

	_, lineage, err := c.Evaluate(context.Background(), evaluationOf(subject()))
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}

	triggers := c.caseTriggers.(*fakeCaseTriggerPublisher).triggers()
	if len(triggers) != 1 {
		t.Fatalf("case triggers published = %d, want 1", len(triggers))
	}
	if got := triggers[0].GetDecisionId(); got != lineage.GetDecisionId() {
		t.Fatalf("trigger decision_id = %q, want %q", got, lineage.GetDecisionId())
	}
}

func TestAnAllowDecisionPublishesNoCaseTrigger(t *testing.T) {
	sentinel := &fakeSentinel{decision: riskv1.Decision_DECISION_ALLOW}
	c := coordinator(t, healthy(), sentinel)

	if _, _, err := c.Evaluate(context.Background(), evaluationOf(subject())); err != nil {
		t.Fatalf("Evaluate: %v", err)
	}

	if triggers := c.caseTriggers.(*fakeCaseTriggerPublisher).triggers(); len(triggers) != 0 {
		t.Fatalf("case triggers published = %d, want 0 for an ALLOW decision", len(triggers))
	}
}

func TestAShadowEvaluationIsRecordedAsShadow(t *testing.T) {
	c := coordinator(t, healthy(), &fakeSentinel{})

	_, lineage, err := c.Evaluate(context.Background(), Request{
		EvaluationID: "e1",
		Subject:      subject(),
		Shadow:       true,
	})
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}

	if !lineage.GetShadow() {
		t.Error("a shadow request was recorded as production lineage (ADR-012): " +
			"shadow decisions must never be mixable into production metrics")
	}
	if entries := c.lineage.(*fakeLineageStore).entries(); !entries[0].GetShadow() {
		t.Error("the persisted entry does not carry shadow, only the returned one")
	}
}

func TestANonShadowEvaluationIsNotRecordedAsShadow(t *testing.T) {
	c := coordinator(t, healthy(), &fakeSentinel{})

	_, lineage, err := c.Evaluate(context.Background(), evaluationOf(subject()))
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}

	if lineage.GetShadow() {
		t.Error("an authoritative decision was recorded as shadow")
	}
}

func TestAShadowReviewOpensNoCase(t *testing.T) {
	sentinel := &fakeSentinel{decision: riskv1.Decision_DECISION_REVIEW}
	c := coordinator(t, healthy(), sentinel)

	_, _, err := c.Evaluate(context.Background(), Request{
		EvaluationID: "e1",
		Subject:      subject(),
		Shadow:       true,
	})
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}

	if triggers := c.caseTriggers.(*fakeCaseTriggerPublisher).triggers(); len(triggers) != 0 {
		t.Fatalf("case triggers published = %d, want 0: a shadow result is recorded, never acted upon (ADR-012), "+
			"and dispatching investigation agents to an analyst-visible case is acting upon it", len(triggers))
	}
}

// evaluationOf is the ordinary, authoritative request almost every test
// wants; only the shadow tests spell the struct out.
func evaluationOf(subject *riskv1.Transaction) Request {
	return Request{EvaluationID: "e1", Subject: subject}
}

// TestEveryStageAndAgentIsTimed proves the histograms reach a real meter from
// the place they are declared, and that no stage is missing. A stage that is
// never recorded looks identical to a stage that is always fast, which is the
// failure mode worth a test rather than the arithmetic.
func TestEveryStageAndAgentIsTimed(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	otel.SetMeterProvider(provider)
	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })

	c := coordinator(t, healthy(), &fakeSentinel{})
	if _, _, err := c.Evaluate(context.Background(), evaluationOf(subject())); err != nil {
		t.Fatalf("Evaluate: %v", err)
	}

	var gathered metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &gathered); err != nil {
		t.Fatalf("collecting metrics: %v", err)
	}

	stages := attributeValues(t, gathered, "legion.evaluation.stage.duration", "stage")
	for _, want := range []string{"feature_fetch", "deterministic_agents", "sentinel"} {
		if !stages[want] {
			t.Errorf("stage %q was never timed; stages seen: %v", want, stages)
		}
	}

	agents := attributeValues(t, gathered, "legion.agent.duration", "agent_id")
	for _, want := range []string{"velocity", "device", "geo"} {
		if !agents[want] {
			t.Errorf("agent %q was never timed; agents seen: %v", want, agents)
		}
	}

	if totals := attributeValues(t, gathered, "legion.evaluation.duration", "decision"); len(totals) != 1 {
		t.Errorf("evaluation total was recorded %d times, want once per evaluation", len(totals))
	}
}

func attributeValues(t *testing.T, gathered metricdata.ResourceMetrics, metric, key string) map[string]bool {
	t.Helper()

	found := map[string]bool{}
	for _, scope := range gathered.ScopeMetrics {
		for _, m := range scope.Metrics {
			if m.Name != metric {
				continue
			}
			histogram, ok := m.Data.(metricdata.Histogram[float64])
			if !ok {
				t.Fatalf("metric %q is %T, want a float64 histogram", metric, m.Data)
			}
			for _, point := range histogram.DataPoints {
				value, _ := point.Attributes.Value(attribute.Key(key))
				found[value.AsString()] = true
			}
		}
	}
	if len(found) == 0 {
		t.Fatalf("metric %q was never recorded", metric)
	}
	return found
}
