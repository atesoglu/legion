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

	"github.com/atesoglu/legion/internal/platform/config"
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
	return slog.New(handler).With("service", cfg.Name)
}

// SignalContext returns a context cancelled on SIGINT or SIGTERM.
func SignalContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}

// Serve runs start, waits for a termination signal, then calls stop with a
// context bounded by the configured grace period.
//
// start must block until the server stops. stop must be safe to call once.
func Serve(cfg config.Service, log *slog.Logger, start func() error, stop func(context.Context) error) error {
	ctx, cancel := SignalContext()
	defer cancel()

	errs := make(chan error, 1)
	go func() { errs <- start() }()

	log.Info("service started", "listen_address", cfg.ListenAddress, "request_deadline", cfg.RequestDeadline.String())

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
