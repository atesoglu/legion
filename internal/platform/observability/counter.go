package observability

import (
	"context"
	"sync"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// Counter is a monotonic count of events, resolved on first use.
//
// The laziness is not an optimisation, it removes an ordering hazard. Every
// Legion service builds its dependencies in main and only then calls
// runtime.Serve, which is what installs the meter provider; an instrument
// built in a constructor therefore predates the provider that should own it.
// Deferring to first Add means a package may declare its metrics wherever it
// likes and still record into the real pipeline, without every service having
// to remember to start telemetry before anything else.
//
// A failure to build the instrument is swallowed deliberately. Metrics exist
// to observe failures, not to cause them, and there is no caller for whom a
// broken counter is worth an error return.
type Counter struct {
	name        string
	description string

	once    sync.Once
	counter metric.Int64Counter
}

// NewCounter declares a counter. Names are dotted, OpenTelemetry style; the
// collector translates them for Prometheus, which is also where a counter
// gains its _total suffix -- spelling it here produces _total_total.
func NewCounter(name, description string) *Counter {
	return &Counter{name: name, description: description}
}

// Add records n occurrences.
//
// No identifier may be passed as an attribute -- see the note on the
// attribute constructors in this package.
func (c *Counter) Add(ctx context.Context, n int64, attrs ...attribute.KeyValue) {
	c.once.Do(func() {
		counter, err := otel.Meter(meterName).Int64Counter(c.name, metric.WithDescription(c.description))
		if err != nil {
			return
		}
		c.counter = counter
	})
	if c.counter == nil {
		return
	}
	c.counter.Add(ctx, n, metric.WithAttributes(attrs...))
}

// Inc records one occurrence.
func (c *Counter) Inc(ctx context.Context, attrs ...attribute.KeyValue) {
	c.Add(ctx, 1, attrs...)
}

// meterName scopes every instrument this package creates to one
// instrumentation library, so the collector can attribute them.
const meterName = "github.com/atesoglu/legion"
