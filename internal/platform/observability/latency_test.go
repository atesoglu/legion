package observability

import (
	"context"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

func histogramFor(t *testing.T, gathered metricdata.ResourceMetrics, name string) metricdata.HistogramDataPoint[float64] {
	t.Helper()

	for _, scope := range gathered.ScopeMetrics {
		for _, m := range scope.Metrics {
			if m.Name != name {
				continue
			}
			histogram, ok := m.Data.(metricdata.Histogram[float64])
			if !ok {
				t.Fatalf("metric %q is %T, want a float64 histogram", name, m.Data)
			}
			if len(histogram.DataPoints) != 1 {
				t.Fatalf("metric %q has %d data points, want 1", name, len(histogram.DataPoints))
			}
			if m.Unit != "s" {
				t.Errorf("metric %q unit = %q, want \"s\": Prometheus specifies seconds for durations", name, m.Unit)
			}
			return histogram.DataPoints[0]
		}
	}
	t.Fatalf("metric %q was never recorded", name)
	return metricdata.HistogramDataPoint[float64]{}
}

// TestTheDeadlineIsABucketBoundary is the property the bucket choice exists
// for. ADR-009's budget is 80 ms, so "what fraction of requests finished
// inside it" must be a bucket count rather than an interpolation across a
// bucket that straddles the number.
func TestTheDeadlineIsABucketBoundary(t *testing.T) {
	latency := NewLatency("legion.test.deadline_bucket", "bucket boundaries")

	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	otel.SetMeterProvider(provider)
	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })

	latency.Record(context.Background(), 3500*time.Microsecond)

	var gathered metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &gathered); err != nil {
		t.Fatalf("collecting metrics: %v", err)
	}
	point := histogramFor(t, gathered, "legion.test.deadline_bucket")

	var found bool
	for _, bound := range point.Bounds {
		if bound == 0.08 {
			found = true
		}
	}
	if !found {
		t.Errorf("0.08 is not a bucket boundary: %v", point.Bounds)
	}
}

// TestASubMillisecondStageIsNotFlattened guards against the default
// OpenTelemetry buckets, whose lowest boundaries assume milliseconds. Against
// seconds they put the entire decision path in the first bucket, which looks
// like a working histogram and measures nothing.
func TestASubMillisecondStageIsNotFlattened(t *testing.T) {
	latency := NewLatency("legion.test.resolution", "sub-millisecond resolution")

	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	otel.SetMeterProvider(provider)
	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })

	// The measured stage distribution: hundreds of microseconds.
	for _, elapsed := range []time.Duration{550 * time.Microsecond, 1100 * time.Microsecond, 90 * time.Millisecond} {
		latency.Record(context.Background(), elapsed)
	}

	var gathered metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &gathered); err != nil {
		t.Fatalf("collecting metrics: %v", err)
	}
	point := histogramFor(t, gathered, "legion.test.resolution")

	if point.Count != 3 {
		t.Fatalf("count = %d, want 3", point.Count)
	}

	// Two samples are under a millisecond and one is past the deadline; a
	// histogram that cannot separate them is not measuring this system.
	var belowAMillisecond, pastTheDeadline uint64
	for i, bound := range point.Bounds {
		if bound <= 0.001 {
			belowAMillisecond += point.BucketCounts[i]
		}
		if bound == 0.08 {
			pastTheDeadline = point.Count - cumulativeTo(point, i)
		}
	}
	if belowAMillisecond != 1 {
		t.Errorf("samples at or below 1 ms = %d, want 1 (550us; 1100us is above)", belowAMillisecond)
	}
	if pastTheDeadline != 1 {
		t.Errorf("samples past the 80 ms deadline = %d, want 1", pastTheDeadline)
	}
}

func cumulativeTo(point metricdata.HistogramDataPoint[float64], index int) uint64 {
	var total uint64
	for i := 0; i <= index; i++ {
		total += point.BucketCounts[i]
	}
	return total
}

func TestALatencyWithNoProviderIsHarmless(t *testing.T) {
	latency := NewLatency("legion.test.no_provider_latency", "recorded with nothing installed")
	latency.Record(context.Background(), time.Millisecond, Stage("sentinel"))
}
