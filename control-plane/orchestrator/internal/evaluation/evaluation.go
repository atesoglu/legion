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

	"github.com/atesoglu/legion/control-plane/orchestrator/internal/breaker"
	"github.com/atesoglu/legion/control-plane/orchestrator/internal/budget"
	"github.com/atesoglu/legion/control-plane/orchestrator/internal/features"
	"github.com/atesoglu/legion/control-plane/orchestrator/internal/registry"
	agentv1 "github.com/atesoglu/legion/protocol/gen/go/legion/agent/v1"
	commonv1 "github.com/atesoglu/legion/protocol/gen/go/legion/common/v1"
	dataplanev1 "github.com/atesoglu/legion/protocol/gen/go/legion/dataplane/v1"
	riskv1 "github.com/atesoglu/legion/protocol/gen/go/legion/risk/v1"
)

// AgentClient invokes one agent. It exists so that fan-out can be tested
// without a network, and so that no transport detail leaks into coordination.
type AgentClient interface {
	Evaluate(ctx context.Context, in *agentv1.EvaluateRequest) (*agentv1.EvaluateResponse, error)
}

// SentinelClient invokes the decision authority.
type SentinelClient interface {
	Decide(ctx context.Context, in *dataplanev1.DecideRequest) (*dataplanev1.DecideResponse, error)
}

// Coordinator runs evaluations against a configured agent set.
type Coordinator struct {
	registry *registry.Registry
	clients  map[string]AgentClient
	breakers map[string]*breaker.Breaker
	sentinel SentinelClient
	store    features.Store
	plan     budget.Plan
	fallback time.Duration
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
		registry: options.Registry,
		clients:  options.Clients,
		breakers: breakers,
		sentinel: options.Sentinel,
		store:    options.Store,
		plan:     plan,
		fallback: options.RequestDeadline,
	}, nil
}

// Evaluate gathers signals for one subject and returns the sentinel's decision.
//
// It returns an error only when no decision can be produced within the
// deadline. A dependency that fails is a degraded decision, not an error.
func (c *Coordinator) Evaluate(
	ctx context.Context,
	evaluationID string,
	subject *riskv1.Transaction,
	policyID string,
) (*riskv1.DecisionOutcome, error) {
	featureSet := c.fetchFeatures(ctx, subject)

	evaluations := c.gather(ctx, evaluationID, subject, featureSet,
		budget.Remaining(ctx, c.fallback))

	sentinelWindow, ok := c.plan.SentinelWindow(budget.Remaining(ctx, c.fallback))
	if !ok {
		return nil, status.Error(codes.DeadlineExceeded,
			"evaluation: deadline expired before a decision could be produced")
	}

	decideCtx, cancel := context.WithTimeout(ctx, sentinelWindow)
	defer cancel()

	response, err := c.sentinel.Decide(decideCtx, &dataplanev1.DecideRequest{
		EvaluationId:         evaluationID,
		Subject:              subject,
		Evaluations:          evaluations,
		PolicyId:             policyID,
		FeatureStoreDegraded: featureSet.GetDegraded(),
		Budget:               durationpb.New(sentinelWindow),
	})
	if err != nil {
		return nil, status.Errorf(codes.Unavailable, "evaluation: sentinel unavailable: %v", err)
	}

	outcome := response.GetOutcome()
	if outcome == nil {
		return nil, status.Error(codes.Internal, "evaluation: sentinel returned no outcome")
	}
	return outcome, nil
}

// fetchFeatures retrieves the evidence for one subject.
//
// It never returns an error. A store that cannot answer produces a feature set
// in which every value is explicitly unavailable, because an agent must be able
// to tell "there is nothing" from "I could not find out" — and because a
// decision on degraded evidence is worth more than no decision.
func (c *Coordinator) fetchFeatures(
	ctx context.Context,
	subject *riskv1.Transaction,
) *riskv1.FeatureSet {
	requests := features.Plan(subject)
	set := &riskv1.FeatureSet{CatalogueVersion: features.CatalogueVersion}

	window, ok := c.plan.FeatureWindow(budget.Remaining(ctx, c.fallback))
	if !ok {
		set.Features = features.Unavailable(requests)
		set.Degraded = true
		return set
	}

	fetchCtx, cancel := context.WithTimeout(ctx, window)
	defer cancel()

	fetched, err := c.store.Fetch(fetchCtx, requests)
	if err != nil {
		set.Features = features.Unavailable(requests)
		set.Degraded = true
		return set
	}

	set.Features = append(fetched, features.Derived(subject, time.Now())...)
	set.Degraded = anyDegraded(set.Features)
	return set
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
func (c *Coordinator) gather(
	ctx context.Context,
	evaluationID string,
	subject *riskv1.Transaction,
	supplied *riskv1.FeatureSet,
	remaining time.Duration,
) []*riskv1.AgentEvaluation {
	agents := c.registry.Agents()

	window, ok := c.plan.AgentWindow(remaining)
	if !ok {
		return exhausted(agents)
	}

	fanCtx, cancel := context.WithTimeout(ctx, window)
	defer cancel()

	results := make([]*riskv1.AgentEvaluation, len(agents))
	var wg sync.WaitGroup

	for i, agent := range agents {
		wg.Add(1)
		go func(index int, agent registry.Agent) {
			defer wg.Done()
			results[index] = c.invoke(fanCtx, agent, evaluationID, subject, supplied, window)
		}(i, agent)
	}
	wg.Wait()

	sort.Slice(results, func(i, j int) bool {
		return results[i].GetAgentId() < results[j].GetAgentId()
	})
	return results
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
