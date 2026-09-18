// Package runtime provides the process lifecycle every Legion Go service
// shares: structured logging, signal handling and bounded graceful shutdown.
//
// It contains no risk logic. Its only job is to make sure a service starts
// predictably and stops without truncating in-flight decisions.
package runtime

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"go.opentelemetry.io/otel/trace"

	"github.com/atesoglu/legion/internal/platform/config"
	"github.com/atesoglu/legion/internal/platform/observability"
)

// NewLogger returns a JSON logger tagged with the service name.
//
// Legion logs are operational, not evidential: transaction payloads,
// identifiers and model output never reach them. Auditable detail belongs in
// decision lineage, which has its own retention and access rules.
func NewLogger(cfg config.Service) *slog.Logger {
	var level slog.Level
	switch cfg.LogLevel {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		level = slog.LevelInfo
	}

	handler := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level})
	return slog.New(traceContext{Handler: handler}).With("service", cfg.Name)
}

// traceContext adds trace_id and span_id to a record logged with a context
// carrying a span, so a log line and the trace it belongs to can be joined in
// Kibana (ADR-020).
//
// It is why correlation does not require exporting logs in-process: the
// identifiers travel in the log line itself, and stdout stays the emission
// contract. Records logged without a context simply carry neither field --
// most of Legion's logging predates tracing and passes no context, which will
// change as call sites move to the *Context variants.
type traceContext struct{ slog.Handler }

func (h traceContext) Handle(ctx context.Context, record slog.Record) error {
	if sc := trace.SpanContextFromContext(ctx); sc.IsValid() {
		record.AddAttrs(
			slog.String("trace_id", sc.TraceID().String()),
			slog.String("span_id", sc.SpanID().String()),
		)
	}
	return h.Handler.Handle(ctx, record)
}

func (h traceContext) WithAttrs(attrs []slog.Attr) slog.Handler {
	return traceContext{Handler: h.Handler.WithAttrs(attrs)}
}

func (h traceContext) WithGroup(name string) slog.Handler {
	return traceContext{Handler: h.Handler.WithGroup(name)}
}

// SignalContext returns a context cancelled on SIGINT or SIGTERM.
func SignalContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}

// Serve runs start, waits for a termination signal, then calls stop with a
// context bounded by the configured grace period.
//
// start must block until the server stops. stop must be safe to call once.
//
// It also owns the metric pipeline's lifetime (ADR-020), so that every Legion
// Go service exports the same signals without its main having to arrange it.
// A collector that is absent or unreachable degrades observability and
// nothing else: Start yields a no-op provider when no endpoint is configured,
// and never fails startup for a telemetry reason.
func Serve(cfg config.Service, log *slog.Logger, start func() error, stop func(context.Context) error) error {
	ctx, cancel := SignalContext()
	defer cancel()

	provider, err := observability.Start(ctx, observability.Config{
		ServiceName: cfg.Name,
		Endpoint:    cfg.OTLPEndpoint,
	})
	if err != nil {
		log.Warn("metrics are disabled: the pipeline could not be built", "error", err)
		provider = nil
	}
	defer func() {
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), cfg.ShutdownGracePeriod)
		defer shutdownCancel()
		if err := provider.Shutdown(shutdownCtx); err != nil {
			log.Warn("metric pipeline shutdown failed", "error", err)
		}
	}()

	errs := make(chan error, 1)
	go func() { errs <- start() }()

	log.Info("service started",
		"listen_address", cfg.ListenAddress,
		"request_deadline", cfg.RequestDeadline.String(),
		"otlp_endpoint", cfg.OTLPEndpoint)

	select {
	case err := <-errs:
		return err
	case <-ctx.Done():
	}

	log.Info("shutdown signal received", "grace_period", cfg.ShutdownGracePeriod.String())

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), cfg.ShutdownGracePeriod)
	defer shutdownCancel()

	if err := stop(shutdownCtx); err != nil {
		return err
	}

	select {
	case err := <-errs:
		return err
	case <-time.After(cfg.ShutdownGracePeriod):
		log.Warn("grace period elapsed before the server stopped")
		return nil
	}
}
