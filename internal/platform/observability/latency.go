package observability

import (
	"context"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// LatencyBuckets are the explicit bucket boundaries, in seconds, for every
// Legion latency histogram.
//
// The OpenTelemetry defaults are useless here: they start at 0, 5, 10, 25
// milliseconds expressed as if the unit were milliseconds, so recording
// seconds against them puts the entire decision path in the first bucket. The
// measured distribution (docs/deadline-model.md section 7) has a p50 of about
// 3.5 ms end to end and stages in the hundreds of microseconds, so the
// resolution has to be down there.
//
// 0.08 is a boundary on purpose. It is ADR-009's deadline, so "what fraction
// of requests finished inside the budget" is a bucket count rather than an
// interpolation across one -- the number the deadline model is actually about
// should not be an estimate. The buckets above it exist to show how far past
// the deadline the tail goes, which a top bucket of 0.08 would hide.
var LatencyBuckets = []float64{
	0.0005, 0.001, 0.0025, 0.005, 0.01, 0.02, 0.04, 0.06,
	0.08,
	0.1, 0.25, 0.5, 1, 2.5,
}

// Latency records how long something took, in seconds.
//
// Seconds because that is the base unit Prometheus and OpenMetrics specify for
// durations, and the exporter names the resulting series accordingly. It is
// deliberately a different unit from the nanoseconds lineage stores: one is a
// metric, the other a per-decision record, and each uses its ecosystem's
// convention rather than a shared compromise neither wants.
//
// Like Counter, the instrument resolves on first use, because instruments are
// declared before runtime.Serve installs the provider.
type Latency struct {
	name        string
	description string

	once      sync.Once
	histogram metric.Float64Histogram
}

func NewLatency(name, description string) *Latency {
	return &Latency{name: name, description: description}
}

// Record observes one duration.
func (l *Latency) Record(ctx context.Context, elapsed time.Duration, attrs ...attribute.KeyValue) {
	l.once.Do(func() {
		histogram, err := otel.Meter(meterName).Float64Histogram(
			l.name,
			metric.WithDescription(l.description),
			metric.WithUnit("s"),
			metric.WithExplicitBucketBoundaries(LatencyBuckets...),
		)
		if err != nil {
			return
		}
		l.histogram = histogram
	})
	if l.histogram == nil {
		return
	}
	l.histogram.Record(ctx, elapsed.Seconds(), metric.WithAttributes(attrs...))
}
