package observability

import (
	"context"
	"testing"
	"time"
)

func TestNoEndpointYieldsAWorkingNoOpProvider(t *testing.T) {
	// A service must start and serve without a collector: metrics are never
	// a startup dependency, and the e2e suite runs without one.
	provider, err := Start(context.Background(), Config{ServiceName: "test"})
	if err != nil {
		t.Fatalf("Start with no endpoint: %v", err)
	}

	counter, err := provider.Meter("test").Int64Counter("decisions_total")
	if err != nil {
		t.Fatalf("building an instrument on the no-op provider: %v", err)
	}
	counter.Add(context.Background(), 1)

	if err := provider.Shutdown(context.Background()); err != nil {
		t.Errorf("Shutdown: %v", err)
	}
}

func TestAnUnreachableCollectorDoesNotFailStartup(t *testing.T) {
	// The exporter dials lazily, which is what makes "a collector that is
	// down degrades observability and nothing else" true at startup rather
	// than only in principle.
	provider, err := Start(context.Background(), Config{
		ServiceName: "test",
		Endpoint:    "127.0.0.1:1", // nothing listens here
	})
	if err != nil {
		t.Fatalf("Start against an unreachable collector: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = provider.Shutdown(ctx)
	})

	counter, err := provider.Meter("test").Int64Counter("decisions_total")
	if err != nil {
		t.Fatalf("building an instrument: %v", err)
	}
	counter.Add(context.Background(), 1)
}

func TestShutdownIsSafeOnANilProvider(t *testing.T) {
	// Serve keeps a nil provider when the pipeline could not be built, and
	// still calls Shutdown on the way out.
	var provider *Provider
	if err := provider.Shutdown(context.Background()); err != nil {
		t.Errorf("Shutdown on a nil provider: %v", err)
	}
}

func TestAttributeKeysAreSpelledOneWay(t *testing.T) {
	// These exist so a label is not "agent" in one service and "agent_id" in
	// another; a renamed label silently splits a time series in two.
	for _, tc := range []struct {
		got  string
		want string
	}{
		{string(AgentID("velocity").Key), "agent_id"},
		{string(Decision("ALLOW").Key), "decision"},
		{string(Outcome("SIGNAL").Key), "outcome"},
		{string(Reason("AGENT_TIMEOUT").Key), "reason"},
		{string(Stage("sentinel").Key), "stage"},
	} {
		if tc.got != tc.want {
			t.Errorf("attribute key = %q, want %q", tc.got, tc.want)
		}
	}
}
