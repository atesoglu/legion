package observability

import (
	"context"
	"testing"

	"go.opentelemetry.io/otel"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

// collected installs a provider with a manual reader and returns everything
// it gathers, so a test can assert on what a counter actually produced rather
// than on the fact that Add did not panic.
func collected(t *testing.T, record func(context.Context)) metricdata.ResourceMetrics {
	t.Helper()

	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	otel.SetMeterProvider(provider)
	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })

	record(context.Background())

	var gathered metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &gathered); err != nil {
		t.Fatalf("collecting metrics: %v", err)
	}
	return gathered
}

func sumFor(t *testing.T, gathered metricdata.ResourceMetrics, name string) int64 {
	t.Helper()

	for _, scope := range gathered.ScopeMetrics {
		for _, m := range scope.Metrics {
			if m.Name != name {
				continue
			}
			sum, ok := m.Data.(metricdata.Sum[int64])
			if !ok {
				t.Fatalf("metric %q is %T, want an int64 sum", name, m.Data)
			}
			var total int64
			for _, point := range sum.DataPoints {
				total += point.Value
			}
			return total
		}
	}
	t.Fatalf("metric %q was never recorded", name)
	return 0
}

// TestACounterDeclaredBeforeTheProviderStillRecords is the ordering hazard
// this type exists for: every service builds its dependencies in main and only
// then calls runtime.Serve, which installs the provider, so instruments
// declared in constructors always predate it.
func TestACounterDeclaredBeforeTheProviderStillRecords(t *testing.T) {
	counter := NewCounter("legion.test.declared_early", "declared before any provider existed")

	gathered := collected(t, func(ctx context.Context) {
		counter.Inc(ctx)
		counter.Add(ctx, 4)
	})

	if got := sumFor(t, gathered, "legion.test.declared_early"); got != 5 {
		t.Errorf("counter total = %d, want 5", got)
	}
}

func TestCounterAttributesAreRecorded(t *testing.T) {
	counter := NewCounter("legion.test.attributed", "carries bounded attributes")

	gathered := collected(t, func(ctx context.Context) {
		counter.Inc(ctx, AgentID("velocity"))
		counter.Inc(ctx, AgentID("device"))
		counter.Inc(ctx, AgentID("device"))
	})

	var found map[string]int64
	for _, scope := range gathered.ScopeMetrics {
		for _, m := range scope.Metrics {
			if m.Name != "legion.test.attributed" {
				continue
			}
			sum, ok := m.Data.(metricdata.Sum[int64])
			if !ok {
				t.Fatalf("metric is %T, want an int64 sum", m.Data)
			}
			found = make(map[string]int64, len(sum.DataPoints))
			for _, point := range sum.DataPoints {
				value, _ := point.Attributes.Value("agent_id")
				found[value.AsString()] = point.Value
			}
		}
	}
	if found["velocity"] != 1 || found["device"] != 2 {
		t.Errorf("per-agent totals = %v, want velocity=1 device=2", found)
	}
}

// TestACounterWithNoProviderIsHarmless covers the path every unit test in the
// repository takes: no provider is installed, and recording must neither
// panic nor allocate a pipeline.
func TestACounterWithNoProviderIsHarmless(t *testing.T) {
	counter := NewCounter("legion.test.no_provider", "recorded with nothing installed")
	counter.Inc(context.Background())
	counter.Add(context.Background(), 3, Stage("sentinel"))
}
