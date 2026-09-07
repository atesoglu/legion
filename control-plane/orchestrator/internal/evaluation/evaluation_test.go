package evaluation

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/atesoglu/legion/control-plane/orchestrator/internal/breaker"
	"github.com/atesoglu/legion/control-plane/orchestrator/internal/budget"
	"github.com/atesoglu/legion/control-plane/orchestrator/internal/registry"
	agentv1 "github.com/atesoglu/legion/protocol/gen/go/legion/agent/v1"
	commonv1 "github.com/atesoglu/legion/protocol/gen/go/legion/common/v1"
	dataplanev1 "github.com/atesoglu/legion/protocol/gen/go/legion/dataplane/v1"
	riskv1 "github.com/atesoglu/legion/protocol/gen/go/legion/risk/v1"
)

type fakeAgent struct {
	score int32
	err   error
	delay time.Duration
	calls atomic.Int32
}

func (f *fakeAgent) Evaluate(ctx context.Context, _ *agentv1.EvaluateRequest) (*agentv1.EvaluateResponse, error) {
	f.calls.Add(1)
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
			Signal: &riskv1.RiskSignal{Score: uint32(f.score), Confidence: 100},
		},
	}, nil
}

type fakeSentinel struct {
	seen    []*riskv1.AgentEvaluation
	degrade bool
	err     error
}

func (f *fakeSentinel) Decide(_ context.Context, in *dataplanev1.DecideRequest) (*dataplanev1.DecideResponse, error) {
	if f.err != nil {
		return nil, f.err
	}
	f.seen = in.GetEvaluations()
	f.degrade = in.GetFeatureStoreDegraded()
	return &dataplanev1.DecideResponse{
		Result: &dataplanev1.DecideResponse_Outcome{
			Outcome: &riskv1.DecisionOutcome{Decision: riskv1.Decision_DECISION_ALLOW},
		},
	}, nil
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
	c, err := New(Options{
		Registry:        agents(),
		Clients:         clients,
		Sentinel:        sentinel,
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
		Registry: agents(),
		Clients:  map[string]AgentClient{"velocity": &fakeAgent{}},
		Sentinel: &fakeSentinel{},
	})
	if err == nil {
		t.Fatal("New accepted a registry it could not serve")
	}
}

func TestEveryRegisteredAgentIsConsulted(t *testing.T) {
	clients := healthy()
	sentinel := &fakeSentinel{}
	c := coordinator(t, clients, sentinel)

	if _, err := c.Evaluate(context.Background(), "e1", &riskv1.Transaction{}, ""); err != nil {
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

	if _, err := c.Evaluate(context.Background(), "e1", &riskv1.Transaction{}, ""); err != nil {
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

	if _, err := c.Evaluate(context.Background(), "e1", &riskv1.Transaction{}, ""); err != nil {
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
	if _, err := c.Evaluate(context.Background(), "e1", &riskv1.Transaction{}, ""); err != nil {
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
		if _, err := c.Evaluate(context.Background(), "e1", &riskv1.Transaction{}, ""); err != nil {
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

	outcome, err := c.Evaluate(ctx, "e1", &riskv1.Transaction{}, "")
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

	if _, err := c.Evaluate(ctx, "e1", &riskv1.Transaction{}, ""); status.Code(err) != codes.DeadlineExceeded {
		t.Fatalf("error = %v, want DeadlineExceeded", err)
	}
}

func TestAnUnavailableSentinelIsAnErrorNotAnAllow(t *testing.T) {
	c := coordinator(t, healthy(), &fakeSentinel{err: errors.New("down")})

	if _, err := c.Evaluate(context.Background(), "e1", &riskv1.Transaction{}, ""); err == nil {
		t.Fatal("Evaluate returned a decision without the sentinel")
	}
}

func TestMissingFeaturesAreDeclaredToTheSentinel(t *testing.T) {
	sentinel := &fakeSentinel{}
	c := coordinator(t, healthy(), sentinel)

	if _, err := c.Evaluate(context.Background(), "e1", &riskv1.Transaction{}, ""); err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if !sentinel.degrade {
		t.Fatal("the sentinel was not told the feature store was unavailable")
	}
}

func TestThePlanDefaultsWhenUnset(t *testing.T) {
	c := coordinator(t, healthy(), &fakeSentinel{})
	if c.plan != budget.Default() {
		t.Fatalf("plan = %+v, want the documented default", c.plan)
	}
}
