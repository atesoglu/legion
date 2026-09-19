package observability

import (
	"context"
	"testing"

	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
	tracenoop "go.opentelemetry.io/otel/trace/noop"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

// realTracerProvider installs a real, always-sampling TracerProvider for the
// duration of one test and restores a no-op provider after, so tests do not
// leak a live provider (and its background goroutines) into each other.
func realTracerProvider(t *testing.T) trace.Tracer {
	t.Helper()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSampler(sdktrace.AlwaysSample()))
	otel.SetTracerProvider(tp)
	t.Cleanup(func() {
		_ = tp.Shutdown(context.Background())
		otel.SetTracerProvider(tracenoop.NewTracerProvider())
	})
	return tp.Tracer("test")
}

func TestInjectFieldsThenExtractContextContinuesTheSameTrace(t *testing.T) {
	tracer := realTracerProvider(t)

	ctx, producerSpan := tracer.Start(context.Background(), "producer")
	want := producerSpan.SpanContext().TraceID()
	producerSpan.End()

	fields := InjectFields(ctx)
	if len(fields) == 0 {
		t.Fatal("InjectFields produced no fields for a valid span context")
	}

	consumerCtx := ExtractContext(context.Background(), fields)
	_, consumerSpan := tracer.Start(consumerCtx, "consumer")
	defer consumerSpan.End()

	if got := consumerSpan.SpanContext().TraceID(); got != want {
		t.Fatalf("consumer span trace id = %s, want %s (same trace as the producer)", got, want)
	}
}

func TestExtractContextOnEmptyFieldsStartsNoTrace(t *testing.T) {
	tracer := realTracerProvider(t)

	// A message published before this code existed, or one that was never
	// under a span, carries no fields. ExtractContext must be a no-op, not
	// an error, so an ordinary un-traced message still processes normally.
	consumerCtx := ExtractContext(context.Background(), nil)
	_, span := tracer.Start(consumerCtx, "consumer")
	defer span.End()

	if !span.SpanContext().IsValid() {
		t.Fatal("span started from an empty-fields context should still be a valid, if new, trace")
	}
}

func TestUnaryInterceptorsPropagateTheSameTraceAcrossAGRPCCall(t *testing.T) {
	realTracerProvider(t)

	ctx, clientSpan := otel.Tracer(tracerName).Start(context.Background(), "caller")
	want := clientSpan.SpanContext().TraceID()
	defer clientSpan.End()

	var gotServerTraceID trace.TraceID
	invoker := func(
		ctx context.Context, method string, req, reply any, _ *grpc.ClientConn, _ ...grpc.CallOption,
	) error {
		// Simulates the network hop: outgoing client metadata becomes
		// incoming server metadata, exactly as gRPC transport does.
		md, _ := metadata.FromOutgoingContext(ctx)
		serverCtx := metadata.NewIncomingContext(context.Background(), md)

		handler := func(ctx context.Context, _ any) (any, error) {
			gotServerTraceID = trace.SpanContextFromContext(ctx).TraceID()
			return nil, nil
		}
		_, err := UnaryServerInterceptor()(serverCtx, nil, &grpc.UnaryServerInfo{FullMethod: method}, handler)
		return err
	}

	if err := UnaryClientInterceptor()(ctx, "/legion.test.v1.Test/Method", nil, nil, nil, invoker); err != nil {
		t.Fatalf("client interceptor: %v", err)
	}
	if gotServerTraceID != want {
		t.Fatalf("server span trace id = %s, want %s (same trace as the client)", gotServerTraceID, want)
	}
}
