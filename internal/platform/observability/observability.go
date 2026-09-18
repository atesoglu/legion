// Package observability wires the OpenTelemetry pipeline every Legion Go
// service shares (ADR-020).
//
// Signals leave a process as OTLP, pushed to a collector. No Legion process
// holds a connection to a storage system, knows a backend's address, or is
// reconfigured when a backend changes; the collector is the only component
// that knows where anything is stored. That is what lets the zero-trust
// network policy ADR-011 requires say "every zone egresses to the collector,
// the collector reaches no zone" rather than carving an inbound exception
// into each zone for a monitoring namespace.
//
// Zone 3 does not use this package. The data plane makes no outbound calls at
// all, which ADR-002, ADR-006 and ADR-013 all depend on, and an exporter is an
// outbound call; the orchestrator's AgentEvaluation.observed_latency and the
// sentinel ExecutionSpan are how Zone 3 is observed instead.
package observability

import (
	"context"
	"fmt"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/noop"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	"go.opentelemetry.io/otel/sdk/resource"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
)

const (
	// exportInterval is how often accumulated measurements are pushed. It is
	// not a sampling rate: counters and histograms accumulate continuously and
	// this only decides how often the collector hears about them.
	exportInterval = 10 * time.Second

	// exportTimeout bounds one push, so an unreachable collector cannot leave
	// the exporting goroutine waiting indefinitely.
	exportTimeout = 5 * time.Second
)

// Config is what a service needs to describe itself to the collector.
type Config struct {
	// ServiceName is the identifier ADR-016 fixes for this deployable. It is
	// the same string used as the log service field and, for agents, as
	// agent_id.
	ServiceName string

	// Endpoint is the collector's OTLP gRPC target, for example
	// "127.0.0.1:4317". Empty disables export entirely.
	Endpoint string
}

// Provider owns a service's metric pipeline and its shutdown.
type Provider struct {
	meterProvider metric.MeterProvider
	shutdown      func(context.Context) error
}

// Start builds the pipeline and installs it as the global provider.
//
// An empty Endpoint yields a no-op provider rather than an error. Metrics must
// never be a startup dependency: a developer running one service, and the e2e
// suite running all of them, must not need a collector to exist, and an
// instrument that records into a no-op provider costs nothing.
//
// Installing globally is deliberate, and is the one place this repository
// prefers global state to an explicit dependency. Instrumentation is
// cross-cutting: threading a meter provider into every constructor would make
// each one take an argument it has no conceptual need for, which is a worse
// trade than one process-wide provider set once at startup.
func Start(ctx context.Context, cfg Config) (*Provider, error) {
	if cfg.Endpoint == "" {
		provider := noop.NewMeterProvider()
		otel.SetMeterProvider(provider)
		return &Provider{
			meterProvider: provider,
			shutdown:      func(context.Context) error { return nil },
		}, nil
	}

	exporter, err := otlpmetricgrpc.New(ctx,
		otlpmetricgrpc.WithEndpoint(cfg.Endpoint),
		// The collector is reached over the cluster-internal network, which
		// ADR-011 secures at the transport layer rather than per-connection
		// here. Revisit with the rest of mTLS.
		otlpmetricgrpc.WithInsecure(),
		otlpmetricgrpc.WithTimeout(exportTimeout),
		// Prometheus is the destination (ADR-020) and it has no notion of
		// delta counters. OpenTelemetry can be configured either way and the
		// wrong choice does not fail, it silently produces wrong values on a
		// dashboard that looks plausible.
		otlpmetricgrpc.WithTemporalitySelector(func(sdkmetric.InstrumentKind) metricdata.Temporality {
			return metricdata.CumulativeTemporality
		}),
	)
	if err != nil {
		return nil, fmt.Errorf("observability: OTLP metric exporter: %w", err)
	}

	res, err := resource.Merge(resource.Default(), resource.NewWithAttributes(
		semconv.SchemaURL,
		semconv.ServiceName(cfg.ServiceName),
	))
	if err != nil {
		return nil, fmt.Errorf("observability: resource: %w", err)
	}

	provider := sdkmetric.NewMeterProvider(
		sdkmetric.WithResource(res),
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(exporter,
			sdkmetric.WithInterval(exportInterval),
			sdkmetric.WithTimeout(exportTimeout),
		)),
	)
	otel.SetMeterProvider(provider)

	return &Provider{meterProvider: provider, shutdown: provider.Shutdown}, nil
}

// Meter returns a named meter from this provider.
func (p *Provider) Meter(name string) metric.Meter {
	return p.meterProvider.Meter(name)
}

// Shutdown flushes anything pending and releases the exporter. It is safe to
// call on a provider built with no endpoint.
func (p *Provider) Shutdown(ctx context.Context) error {
	if p == nil || p.shutdown == nil {
		return nil
	}
	return p.shutdown(ctx)
}

// Attributes that appear on Legion metrics, defined here so that a label is
// spelled one way across every service.
//
// No identifier is ever a metric attribute -- not transaction_id, account_id,
// decision_id, case_id, investigation_id or task_id, pseudonymous or
// otherwise (ADR-020). An unbounded label destroys the metric store, and an
// identifier in a metric is an egress path for data the threat model keeps
// out of logs. Per-entity questions are answered from lineage and the
// investigation schema, which are built for them.
func AgentID(id string) attribute.KeyValue { return attribute.String("agent_id", id) }
func Decision(d string) attribute.KeyValue { return attribute.String("decision", d) }
func Outcome(o string) attribute.KeyValue  { return attribute.String("outcome", o) }
func Reason(r string) attribute.KeyValue   { return attribute.String("reason", r) }
func Stage(s string) attribute.KeyValue    { return attribute.String("stage", s) }
func State(s string) attribute.KeyValue    { return attribute.String("state", s) }
func Verdict(v string) attribute.KeyValue  { return attribute.String("verdict", v) }
