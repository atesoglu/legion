// Package evaluation coordinates one risk evaluation.
//
// It decides what work happens, in what order, with what share of the remaining
// deadline, and what to do when a dependency does not answer. It never maps a
// score to a decision: that authority belongs to the sentinel (ADR-004).
//
// Nothing here branches on a particular agent identifier. Fan-out, breaking,
// exclusion and assembly are written once and iterate over the registry
// (ADR-014).
package evaluation

import (
	"context"
	"sort"
	"sync"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/atesoglu/legion/control-plane/orchestrator/internal/breaker"
	"github.com/atesoglu/legion/control-plane/orchestrator/internal/budget"
	"github.com/atesoglu/legion/control-plane/orchestrator/internal/features"
	"github.com/atesoglu/legion/control-plane/orchestrator/internal/registry"
	"github.com/atesoglu/legion/internal/platform/observability"
	agentv1 "github.com/atesoglu/legion/protocol/gen/go/legion/agent/v1"
	commonv1 "github.com/atesoglu/legion/protocol/gen/go/legion/common/v1"
	dataplanev1 "github.com/atesoglu/legion/protocol/gen/go/legion/dataplane/v1"
	investigationv1 "github.com/atesoglu/legion/protocol/gen/go/legion/investigation/v1"
	riskv1 "github.com/atesoglu/legion/protocol/gen/go/legion/risk/v1"
)

// contractVersion identifies the protobuf contract set recorded on every
// lineage entry. It is a stand-in for real contract versioning: the contracts
// have no automated version stamp yet, so this is a configured constant, not a
// measured or generated one.
const contractVersion = "v1"

// AgentClient invokes one agent. It exists so that fan-out can be tested
// without a network, and so that no transport detail leaks into coordination.
type AgentClient interface {
	Evaluate(ctx context.Context, in *agentv1.EvaluateRequest) (*agentv1.EvaluateResponse, error)
}

// SentinelClient invokes the decision authority.
type SentinelClient interface {
	Decide(ctx context.Context, in *dataplanev1.DecideRequest) (*dataplanev1.DecideResponse, error)
}

// LineageStore persists the DecisionLineage of a completed evaluation.
//
// It is consulted after the sentinel has already answered, so a slow or down
// store degrades lineage, never the decision. See internal/lineage.
type LineageStore interface {
	Record(entry *riskv1.DecisionLineage)
}

// CaseTriggerPublisher notifies the investigation plane of a REVIEW decision
// (ADR-017 section 1.3). Like LineageStore, it is consulted only after the
// sentinel has already answered; a slow or down investigation queue degrades
// case creation, never the decision. See
// control-plane/orchestrator/internal/casetrigger.
type CaseTriggerPublisher interface {
	Publish(trigger *investigationv1.CaseTrigger)
}

// Coordinator runs evaluations against a configured agent set.
type Coordinator struct {
	registry     *registry.Registry
	clients      map[string]AgentClient
	breakers     map[string]*breaker.Breaker
	sentinel     SentinelClient
	store        features.Store
	lineage      LineageStore
	caseTriggers CaseTriggerPublisher
	plan         budget.Plan
	fallback     time.Duration
}

// Options configures a Coordinator.
type Options struct {
	// Registry is the configured agent set.
	Registry *registry.Registry

	// Clients must contain one entry per registered agent.
	Clients map[string]AgentClient

	// Sentinel is the decision authority.
	Sentinel SentinelClient

	// Store supplies the evidence agents reason over. The control plane holds
	// the only connection to it; no agent has one (ADR-005).
	Store features.Store

	// Lineage persists every evaluation's DecisionLineage (ADR-007, ADR-013).
	Lineage LineageStore

	// CaseTriggers notifies the investigation plane of a REVIEW decision
	// (ADR-017 section 1.3).
	CaseTriggers CaseTriggerPublisher

	// Plan is the budget apportionment. Zero values take the documented default.
	Plan budget.Plan

	// RequestDeadline applies when a caller supplies no deadline of its own.
	RequestDeadline time.Duration

	// Breaker configures every agent's circuit breaker.
	Breaker breaker.Config
}

// New builds a Coordinator, refusing any registered agent that has no client.
//
// This is the startup validation ADR-014 requires: with the agent set as
// configuration, a missing client is a runtime failure where a hardcoded set
// would have failed to compile, so it must be caught before serving.
func New(options Options) (*Coordinator, error) {
	if options.Registry == nil {
		return nil, status.Error(codes.FailedPrecondition, "evaluation: no agent registry configured")
	}
	if options.Sentinel == nil {
		return nil, status.Error(codes.FailedPrecondition, "evaluation: no sentinel client configured")
	}
	if options.Store == nil {
		return nil, status.Error(codes.FailedPrecondition, "evaluation: no feature store configured")
	}
	if options.Lineage == nil {
		return nil, status.Error(codes.FailedPrecondition, "evaluation: no lineage store configured")
	}
	if options.CaseTriggers == nil {
		return nil, status.Error(codes.FailedPrecondition, "evaluation: no case trigger publisher configured")
	}

	breakers := make(map[string]*breaker.Breaker)
	for _, agent := range options.Registry.Agents() {
		if _, ok := options.Clients[agent.ID]; !ok {
			return nil, status.Errorf(codes.FailedPrecondition,
				"evaluation: agent %q is registered but has no client", agent.ID)
		}
		breakers[agent.ID] = breaker.New(options.Breaker)
	}

	plan := options.Plan
	if plan.Agents == 0 || plan.Sentinel == 0 || plan.Features == 0 {
		plan = budget.Default()
	}

	return &Coordinator{
		registry:     options.Registry,
		clients:      options.Clients,
		breakers:     breakers,
		sentinel:     options.Sentinel,
		store:        options.Store,
		lineage:      options.Lineage,
		caseTriggers: options.CaseTriggers,
		plan:         plan,
		fallback:     options.RequestDeadline,
	}, nil
}

// stageLatency and evaluationLatency are the same measurements lineage
// already records per decision (ExecutionSpan), aggregated. Lineage answers
// "how long did decision X take"; these answer "what does the distribution
// look like", which is what docs/deadline-model.md section 6 asks for and
// what no per-decision record can give without a scan.
//
// agent_id is a permitted attribute here and nowhere in Zone 6: the
// decision-path agent set is small and fixed by configuration (ADR-014),
// where investigation agents are expected in the thousands.
var (
	stageLatency = observability.NewLatency(
		"legion.evaluation.stage.duration",
		"Time spent in each stage of an evaluation, by stage.",
	)
	evaluationLatency = observability.NewLatency(
		"legion.evaluation.duration",
		"Total orchestrator time for one evaluation, excluding the gateway's own work.",
	)
	agentLatency = observability.NewLatency(
		"legion.agent.duration",
		"Time an agent took to answer, by agent and by whether it produced a signal.",
	)
)

// Request is one evaluation's per-request switches, translated from the
// wire's EvaluationOptions so this package stays transport-free.
//
// A new switch belongs here rather than as another positional parameter:
// Shadow was silently dropped for exactly that reason, because there was no
// obvious place to put it and a fifth argument looked worse than omitting it.
type Request struct {
	EvaluationID string
	Subject      *riskv1.Transaction
	PolicyID     string

	// Shadow marks a non-authoritative evaluation (ADR-012): processed
	// identically, recorded identically, never acted upon.
	Shadow bool
}

// Evaluate gathers signals for one subject and returns the sentinel's
// decision, plus the lineage recorded for it.
//
// It returns an error only when no decision can be produced within the
// deadline. A dependency that fails is a degraded decision, not an error.
func (c *Coordinator) Evaluate(
	ctx context.Context,
	req Request,
) (*riskv1.DecisionOutcome, *riskv1.DecisionLineage, error) {
	evaluationID := req.EvaluationID
	subject := req.Subject

	started := time.Now()
	deadline := budget.Remaining(ctx, c.fallback)

	featureStarted := time.Now()
	featureSet, featureWindow, featureFailure := c.fetchFeatures(ctx, subject)
	featureElapsed := time.Since(featureStarted)

	agentsStarted := time.Now()
	evaluations, agentWindow := c.gather(ctx, evaluationID, subject, featureSet,
		budget.Remaining(ctx, c.fallback))
	agentsElapsed := time.Since(agentsStarted)

	sentinelWindow, ok := c.plan.SentinelWindow(budget.Remaining(ctx, c.fallback))
	if !ok {
		return nil, nil, status.Error(codes.DeadlineExceeded,
			"evaluation: deadline expired before a decision could be produced")
	}

	decideCtx, cancel := context.WithTimeout(ctx, sentinelWindow)
	defer cancel()

	sentinelStarted := time.Now()
	response, err := c.sentinel.Decide(decideCtx, &dataplanev1.DecideRequest{
		EvaluationId:         evaluationID,
		Subject:              subject,
		Evaluations:          evaluations,
		PolicyId:             req.PolicyID,
		FeatureStoreDegraded: featureSet.GetDegraded(),
		Budget:               durationpb.New(sentinelWindow),
	})
	sentinelElapsed := time.Since(sentinelStarted)
	if err != nil {
		return nil, nil, status.Errorf(codes.Unavailable, "evaluation: sentinel unavailable: %v", err)
	}

	outcome := response.GetOutcome()
	if outcome == nil {
		return nil, nil, status.Error(codes.Internal, "evaluation: sentinel returned no outcome")
	}

	totalElapsed := time.Since(started)
	stageLatency.Record(ctx, featureElapsed, observability.Stage("feature_fetch"))
	stageLatency.Record(ctx, agentsElapsed, observability.Stage("deterministic_agents"))
	stageLatency.Record(ctx, sentinelElapsed, observability.Stage("sentinel"))
	evaluationLatency.Record(ctx, totalElapsed, observability.Decision(outcome.GetDecision().String()))

	entry := c.buildLineage(lineageInput{
		decisionID:     evaluationID,
		subject:        subject,
		decidedAt:      time.Now(),
		outcome:        outcome,
		evaluations:    evaluations,
		featureSet:     featureSet,
		featureFailure: featureFailure,
		shadow:         req.Shadow,
		deadline:       deadline,
		totalElapsed:   totalElapsed,
		spans: []*riskv1.ExecutionSpan{
			span("feature_fetch", featureElapsed, featureWindow),
			span("deterministic_agents", agentsElapsed, agentWindow),
			span("sentinel", sentinelElapsed, sentinelWindow),
		},
	})
	c.lineage.Record(entry)

	// Off the critical path, same as lineage: the request has already been
	// decided, and a down investigation queue must not be able to affect it.
	//
	// A shadow decision opens no case: ADR-012 requires a shadow result to be
	// recorded and never acted upon, and dispatching investigation agents to
	// an analyst-visible case is acting upon it.
	if outcome.GetDecision() == riskv1.Decision_DECISION_REVIEW && !req.Shadow {
		c.caseTriggers.Publish(&investigationv1.CaseTrigger{
			DecisionId:     evaluationID,
			TransactionId:  subject.GetId(),
			Outcome:        outcome,
			IdempotencyKey: subject.GetIdempotencyKey(),
		})
	}

	return outcome, entry, nil
}

// lineageInput collects everything gathered during one Evaluate call that the
// lineage entry needs, so buildLineage stays a pure function of its input.
type lineageInput struct {
	decisionID     string
	subject        *riskv1.Transaction
	decidedAt      time.Time
	outcome        *riskv1.DecisionOutcome
	evaluations    []*riskv1.AgentEvaluation
	featureSet     *riskv1.FeatureSet
	featureFailure *commonv1.Failure
	shadow         bool
	deadline       time.Duration
	totalElapsed   time.Duration
	spans          []*riskv1.ExecutionSpan
}

// buildLineage assembles the DecisionLineage for one evaluation.
//
// Model, prompt and output-schema versions are left unset: the behavioural
// agent does not exist yet, and GovernedVersions documents them as present
// only when it participates.
func (c *Coordinator) buildLineage(in lineageInput) *riskv1.DecisionLineage {
	failures := make([]*commonv1.Failure, 0, len(in.evaluations)+1)
	if in.featureFailure != nil {
		failures = append(failures, in.featureFailure)
	}
	agentVersions := make([]*commonv1.Version, 0, len(in.evaluations))
	for _, evaluation := range in.evaluations {
		if failure := evaluation.GetFailure(); failure != nil {
			failures = append(failures, failure)
		}
		// A failed agent produced no signal, so no version is known for it;
		// replay must treat a missing entry here as "not observed", not as
		// evidence the agent was unversioned.
		if version := evaluation.GetSignal().GetAgentVersion(); version != nil {
			agentVersions = append(agentVersions, version)
		}
	}

	return &riskv1.DecisionLineage{
		DecisionId:    in.decisionID,
		TransactionId: in.subject.GetId(),
		DecidedAt:     timestamppb.New(in.decidedAt),
		Outcome:       in.outcome,
		Evaluations:   in.evaluations,
		Versions: &riskv1.GovernedVersions{
			Agents:           agentVersions,
			Policy:           in.outcome.GetPolicy().GetVersion(),
			FeatureCatalogue: &commonv1.Version{Name: "feature-catalogue", Version: in.featureSet.GetCatalogueVersion()},
			Contract:         &commonv1.Version{Name: "legion-protocol", Version: contractVersion},
		},
		Spans:        in.spans,
		Shadow:       in.shadow,
		Deadline:     durationpb.New(in.deadline),
		TotalElapsed: durationpb.New(in.totalElapsed),
		Failures:     failures,
	}
}

func span(stage string, elapsed, allotted time.Duration) *riskv1.ExecutionSpan {
	return &riskv1.ExecutionSpan{
		Stage:   stage,
		Elapsed: durationpb.New(elapsed),
		Budget:  durationpb.New(allotted),
	}
}

// fetchFeatures retrieves the evidence for one subject.
//
// It never returns an error. A store that cannot answer produces a feature set
// in which every value is explicitly unavailable, because an agent must be able
// to tell "there is nothing" from "I could not find out" — and because a
// decision on degraded evidence is worth more than no decision. The window
// granted and, when the fetch itself failed, a Failure describing why are
// both returned so the caller can record them in lineage.
func (c *Coordinator) fetchFeatures(
	ctx context.Context,
	subject *riskv1.Transaction,
) (*riskv1.FeatureSet, time.Duration, *commonv1.Failure) {
	requests := features.Plan(subject)
	set := &riskv1.FeatureSet{CatalogueVersion: features.CatalogueVersion}

	window, ok := c.plan.FeatureWindow(budget.Remaining(ctx, c.fallback))
	if !ok {
		set.Features = features.Unavailable(requests)
		set.Degraded = true
		return set, window, &commonv1.Failure{
			Kind:      commonv1.FailureKind_FAILURE_KIND_DEADLINE_EXCEEDED,
			Component: "feature-store",
			Message:   "no budget remained to fetch features",
		}
	}

	fetchCtx, cancel := context.WithTimeout(ctx, window)
	defer cancel()

	fetched, err := c.store.Fetch(fetchCtx, requests)
	if err != nil {
		set.Features = features.Unavailable(requests)
		set.Degraded = true
		return set, window, &commonv1.Failure{
			Kind:      commonv1.FailureKind_FAILURE_KIND_DEPENDENCY_UNAVAILABLE,
			Component: "feature-store",
			Message:   "feature store fetch failed",
		}
	}

	set.Features = append(fetched, features.Derived(subject, time.Now())...)
	set.Degraded = anyDegraded(set.Features)
	return set, window, nil
}

// anyDegraded reports whether the evaluation is working from imperfect
// evidence. Absent is not degraded: a new account genuinely has no history.
func anyDegraded(supplied []*riskv1.Feature) bool {
	for _, feature := range supplied {
		switch feature.GetFreshness() {
		case riskv1.FeatureFreshness_FEATURE_FRESHNESS_STALE,
			riskv1.FeatureFreshness_FEATURE_FRESHNESS_UNAVAILABLE,
			riskv1.FeatureFreshness_FEATURE_FRESHNESS_UNSPECIFIED:
			return true
		}
	}
	return false
}

// gather fans out to every registered agent in one shared window and collects
// the results in agent-identifier order, independent of the order they arrive.
// It also returns the window actually granted, for lineage spans.
func (c *Coordinator) gather(
	ctx context.Context,
	evaluationID string,
	subject *riskv1.Transaction,
	supplied *riskv1.FeatureSet,
	remaining time.Duration,
) ([]*riskv1.AgentEvaluation, time.Duration) {
	agents := c.registry.Agents()

	window, ok := c.plan.AgentWindow(remaining)
	if !ok {
		return exhausted(agents), 0
	}

	fanCtx, cancel := context.WithTimeout(ctx, window)
	defer cancel()

	results := make([]*riskv1.AgentEvaluation, len(agents))
	var wg sync.WaitGroup

	for i, agent := range agents {
		wg.Add(1)
		go func(index int, agent registry.Agent) {
			defer wg.Done()
			evaluation := c.invoke(fanCtx, agent, evaluationID, subject, supplied, window)
			results[index] = evaluation

			// Recorded here rather than inside invoke so that every way an
			// agent call can end -- signal, failure, open circuit, exhausted
			// budget -- is measured by one statement.
			outcome := "signal"
			if evaluation.GetSignal() == nil {
				outcome = "failure"
			}
			agentLatency.Record(ctx, evaluation.GetObservedLatency().AsDuration(),
				observability.AgentID(agent.ID), observability.Outcome(outcome))
		}(i, agent)
	}
	wg.Wait()

	sort.Slice(results, func(i, j int) bool {
		return results[i].GetAgentId() < results[j].GetAgentId()
	})
	return results, window
}

func (c *Coordinator) invoke(
	ctx context.Context,
	agent registry.Agent,
	evaluationID string,
	subject *riskv1.Transaction,
	supplied *riskv1.FeatureSet,
	window time.Duration,
) *riskv1.AgentEvaluation {
	started := time.Now()

	if allowed, state := c.breakers[agent.ID].Allow(); !allowed {
		return failed(agent.ID, commonv1.FailureKind_FAILURE_KIND_CIRCUIT_OPEN,
			"circuit "+state.String(), time.Since(started), false)
	}

	response, err := c.clients[agent.ID].Evaluate(ctx, &agentv1.EvaluateRequest{
		EvaluationId: evaluationID,
		Subject:      subject,
		Features:     supplied,
		Budget:       durationpb.New(window),
	})
	elapsed := time.Since(started)

	if err != nil {
		kind := commonv1.FailureKind_FAILURE_KIND_DEPENDENCY_ERROR
		switch status.Code(err) {
		case codes.DeadlineExceeded:
			kind = commonv1.FailureKind_FAILURE_KIND_DEADLINE_EXCEEDED
		case codes.Unavailable:
			kind = commonv1.FailureKind_FAILURE_KIND_DEPENDENCY_UNAVAILABLE
		}
		// A deadline is the request's problem, not the dependency's: counting
		// it against the breaker would open circuits during a latency spike.
		c.breakers[agent.ID].Record(kind == commonv1.FailureKind_FAILURE_KIND_DEADLINE_EXCEEDED)
		return failed(agent.ID, kind, "agent call failed", elapsed, true)
	}

	c.breakers[agent.ID].Record(true)

	if failure := response.GetFailure(); failure != nil {
		return &riskv1.AgentEvaluation{
			AgentId:         agent.ID,
			Result:          &riskv1.AgentEvaluation_Failure{Failure: failure},
			ObservedLatency: durationpb.New(elapsed),
		}
	}

	return &riskv1.AgentEvaluation{
		AgentId:         agent.ID,
		Result:          &riskv1.AgentEvaluation_Signal{Signal: response.GetSignal()},
		ObservedLatency: durationpb.New(elapsed),
	}
}

// exhausted records every agent as skipped when no time is left to consult any
// of them. The agents are still reported, so a starved evaluation is
// distinguishable from a policy with fewer agents in it.
func exhausted(agents []registry.Agent) []*riskv1.AgentEvaluation {
	out := make([]*riskv1.AgentEvaluation, 0, len(agents))
	for _, agent := range agents {
		out = append(out, failed(agent.ID, commonv1.FailureKind_FAILURE_KIND_DEADLINE_EXCEEDED,
			"no budget remained to consult this agent", 0, false))
	}
	return out
}

func failed(
	agentID string,
	kind commonv1.FailureKind,
	message string,
	elapsed time.Duration,
	retryable bool,
) *riskv1.AgentEvaluation {
	return &riskv1.AgentEvaluation{
		AgentId: agentID,
		Result: &riskv1.AgentEvaluation_Failure{
			Failure: &commonv1.Failure{
				Kind:      kind,
				Component: agentID,
				Message:   message,
				Elapsed:   durationpb.New(elapsed),
				Retryable: retryable,
			},
		},
		ObservedLatency: durationpb.New(elapsed),
	}
}
