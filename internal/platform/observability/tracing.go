package observability

import (
	"context"

	"go.opentelemetry.io/otel"
	otelcodes "go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

// tracerName scopes every span this package creates to the same
// instrumentation library as the metrics in counter.go and latency.go.
const tracerName = meterName

func init() {
	// W3C trace context, set unconditionally and independent of Config.
	// Extract/Inject on an invalid (no-op) span context are no-ops, so this
	// is safe whether or not a real TracerProvider is ever installed.
	otel.SetTextMapPropagator(propagation.TraceContext{})
}

// grpcMetadataCarrier adapts gRPC metadata to propagation.TextMapCarrier, so
// a trace context can travel as ordinary gRPC metadata (ADR-020: trace
// context propagates across gRPC).
type grpcMetadataCarrier metadata.MD

func (c grpcMetadataCarrier) Get(key string) string {
	values := metadata.MD(c).Get(key)
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

func (c grpcMetadataCarrier) Set(key, value string) {
	metadata.MD(c).Set(key, value)
}

func (c grpcMetadataCarrier) Keys() []string {
	keys := make([]string, 0, len(c))
	for k := range c {
		keys = append(keys, k)
	}
	return keys
}

// UnaryServerInterceptor extracts an incoming trace context (if any) and
// starts a server span for the RPC, so a call arriving from an instrumented
// caller continues that caller's trace rather than starting a new one.
func UnaryServerInterceptor() grpc.UnaryServerInterceptor {
	return func(
		ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler,
	) (any, error) {
		md, ok := metadata.FromIncomingContext(ctx)
		if !ok {
			md = metadata.MD{}
		}
		ctx = otel.GetTextMapPropagator().Extract(ctx, grpcMetadataCarrier(md))

		ctx, span := otel.Tracer(tracerName).Start(ctx, info.FullMethod, trace.WithSpanKind(trace.SpanKindServer))
		defer span.End()

		resp, err := handler(ctx, req)
		if err != nil {
			span.RecordError(err)
			span.SetStatus(otelcodes.Error, err.Error())
		}
		return resp, err
	}
}

// UnaryClientInterceptor starts a client span for the outbound call and
// injects its trace context into the request's gRPC metadata, so the callee
// can continue the same trace.
func UnaryClientInterceptor() grpc.UnaryClientInterceptor {
	return func(
		ctx context.Context, method string, req, reply any,
		cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption,
	) error {
		ctx, span := otel.Tracer(tracerName).Start(ctx, method, trace.WithSpanKind(trace.SpanKindClient))
		defer span.End()

		md, ok := metadata.FromOutgoingContext(ctx)
		if ok {
			md = md.Copy()
		} else {
			md = metadata.MD{}
		}
		otel.GetTextMapPropagator().Inject(ctx, grpcMetadataCarrier(md))
		ctx = metadata.NewOutgoingContext(ctx, md)

		err := invoker(ctx, method, req, reply, cc, opts...)
		if err != nil {
			span.RecordError(err)
			span.SetStatus(otelcodes.Error, err.Error())
		}
		return err
	}
}

// mapCarrier adapts a plain string map to propagation.TextMapCarrier, so a
// trace context can travel as extra fields on a Redis Streams entry -- the
// investigation plane's only transport (ADR-020: trace context propagates
// across the Redis Streams queue hops too).
type mapCarrier map[string]string

func (c mapCarrier) Get(key string) string { return c[key] }
func (c mapCarrier) Set(key, value string) { c[key] = value }
func (c mapCarrier) Keys() []string {
	keys := make([]string, 0, len(c))
	for k := range c {
		keys = append(keys, k)
	}
	return keys
}

// InjectFields returns the trace-context fields (traceparent, and tracestate
// if present) for the span active in ctx, suitable for adding alongside a
// queue message's own fields. Empty when ctx carries no valid span, which is
// harmless: ExtractContext on an empty map is a no-op.
func InjectFields(ctx context.Context) map[string]string {
	carrier := mapCarrier{}
	otel.GetTextMapPropagator().Inject(ctx, carrier)
	return carrier
}

// ExtractContext returns ctx with the trace context carried by fields (as
// produced by InjectFields) restored, so a message consumer can continue the
// producer's trace. A nil or empty fields map returns ctx unchanged.
func ExtractContext(ctx context.Context, fields map[string]string) context.Context {
	if len(fields) == 0 {
		return ctx
	}
	return otel.GetTextMapPropagator().Extract(ctx, mapCarrier(fields))
}

// StartConsumerSpan starts a span for one unit of queue-triggered work,
// spanning kind CONSUMER per OpenTelemetry's messaging semantic conventions.
// Callers are expected to have already called ExtractContext so this
// continues the producer's trace rather than starting a new one.
func StartConsumerSpan(ctx context.Context, name string) (context.Context, trace.Span) {
	return otel.Tracer(tracerName).Start(ctx, name, trace.WithSpanKind(trace.SpanKindConsumer))
}

// RecordOutcome sets a span's status from err: OK when nil, Error otherwise.
// It does not end the span -- callers still own that with defer span.End().
func RecordOutcome(span trace.Span, err error) {
	if err != nil {
		span.RecordError(err)
		span.SetStatus(otelcodes.Error, err.Error())
		return
	}
	span.SetStatus(otelcodes.Ok, "")
}
