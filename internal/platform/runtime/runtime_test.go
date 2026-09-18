package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"testing"

	"go.opentelemetry.io/otel/trace"
)

func loggedRecord(t *testing.T, ctx context.Context) map[string]any {
	t.Helper()

	var buffer bytes.Buffer
	handler := traceContext{Handler: slog.NewJSONHandler(&buffer, nil)}
	slog.New(handler).InfoContext(ctx, "decision recorded")

	var record map[string]any
	if err := json.Unmarshal(buffer.Bytes(), &record); err != nil {
		t.Fatalf("log line did not unmarshal: %v\n%s", err, buffer.String())
	}
	return record
}

func TestALogLineCarriesTheTraceItBelongsTo(t *testing.T) {
	traceID, err := trace.TraceIDFromHex("4bf92f3577b34da6a3ce929d0e0e4736")
	if err != nil {
		t.Fatalf("trace id: %v", err)
	}
	spanID, err := trace.SpanIDFromHex("00f067aa0ba902b7")
	if err != nil {
		t.Fatalf("span id: %v", err)
	}

	ctx := trace.ContextWithSpanContext(context.Background(), trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    traceID,
		SpanID:     spanID,
		TraceFlags: trace.FlagsSampled,
	}))

	record := loggedRecord(t, ctx)
	if record["trace_id"] != "4bf92f3577b34da6a3ce929d0e0e4736" {
		t.Errorf("trace_id = %v, want the span context's trace id", record["trace_id"])
	}
	if record["span_id"] != "00f067aa0ba902b7" {
		t.Errorf("span_id = %v, want the span context's span id", record["span_id"])
	}
}

func TestALogLineWithoutASpanCarriesNoTraceFields(t *testing.T) {
	// Most of Legion's logging predates tracing and passes no context. It
	// must stay clean rather than gain empty correlation fields that look
	// like a lost trace.
	record := loggedRecord(t, context.Background())

	if _, ok := record["trace_id"]; ok {
		t.Errorf("trace_id was added without a span in context: %v", record)
	}
	if _, ok := record["span_id"]; ok {
		t.Errorf("span_id was added without a span in context: %v", record)
	}
}

func TestTraceFieldsSurviveWithAttrsAndWithGroup(t *testing.T) {
	// slog.New(...).With(...) returns a new handler; a wrapper that forgets to
	// re-wrap loses trace correlation for every logger derived from it, which
	// is every Legion service logger since NewLogger calls With("service").
	traceID, _ := trace.TraceIDFromHex("4bf92f3577b34da6a3ce929d0e0e4736")
	spanID, _ := trace.SpanIDFromHex("00f067aa0ba902b7")
	ctx := trace.ContextWithSpanContext(context.Background(), trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    traceID,
		SpanID:     spanID,
		TraceFlags: trace.FlagsSampled,
	}))

	var buffer bytes.Buffer
	base := traceContext{Handler: slog.NewJSONHandler(&buffer, nil)}
	slog.New(base).With("service", "gateway").InfoContext(ctx, "decision recorded")

	var record map[string]any
	if err := json.Unmarshal(buffer.Bytes(), &record); err != nil {
		t.Fatalf("log line did not unmarshal: %v", err)
	}
	if record["trace_id"] != "4bf92f3577b34da6a3ce929d0e0e4736" {
		t.Errorf("a derived logger lost trace correlation: %v", record)
	}
	if record["service"] != "gateway" {
		t.Errorf("a derived logger lost its attributes: %v", record)
	}
}
