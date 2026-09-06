// Package config resolves service configuration from the environment.
//
// Configuration is explicit: every setting has a documented default, an
// environment variable, and a validation rule. Nothing reads the environment
// outside this package, so a service's full configuration surface is one file.
package config

import (
	"fmt"
	"os"
	"time"
)

// Service holds the settings every Legion Go service needs. Service-specific
// settings live next to the service that owns them.
type Service struct {
	// Name is the logical service name used in logs, traces and metrics.
	Name string

	// ListenAddress is the gRPC listen address.
	ListenAddress string

	// RequestDeadline is the end-to-end budget granted to a decision request
	// when the caller does not supply a shorter one. It is also the ceiling:
	// a caller may ask for less, never for more.
	//
	// See docs/deadline-model.md.
	RequestDeadline time.Duration

	// ShutdownGracePeriod is how long in-flight requests may finish after a
	// termination signal before the process exits. It must exceed
	// RequestDeadline, otherwise shutdown itself becomes a source of
	// truncated decisions.
	ShutdownGracePeriod time.Duration

	// LogLevel is one of debug, info, warn, error.
	LogLevel string
}

// Defaults for a Legion Go service. The 80ms request deadline is the platform
// target defined in ADR-009; it is a configured value, not a measured claim.
const (
	DefaultRequestDeadline     = 80 * time.Millisecond
	DefaultShutdownGracePeriod = 10 * time.Second
	DefaultLogLevel            = "info"
)

// LoadService reads configuration for the named service. Environment variables
// are prefixed with LEGION_, for example LEGION_REQUEST_DEADLINE.
func LoadService(name, defaultListenAddress string) (Service, error) {
	svc := Service{
		Name:          name,
		ListenAddress: envString("LEGION_LISTEN_ADDRESS", defaultListenAddress),
		LogLevel:      envString("LEGION_LOG_LEVEL", DefaultLogLevel),
	}

	var err error
	if svc.RequestDeadline, err = envDuration("LEGION_REQUEST_DEADLINE", DefaultRequestDeadline); err != nil {
		return Service{}, err
	}
	if svc.ShutdownGracePeriod, err = envDuration("LEGION_SHUTDOWN_GRACE_PERIOD", DefaultShutdownGracePeriod); err != nil {
		return Service{}, err
	}
	if err := svc.validate(); err != nil {
		return Service{}, err
	}
	return svc, nil
}

func (s Service) validate() error {
	if s.ListenAddress == "" {
		return fmt.Errorf("config: listen address must not be empty")
	}
	if s.RequestDeadline <= 0 {
		return fmt.Errorf("config: request deadline must be positive, got %s", s.RequestDeadline)
	}
	if s.ShutdownGracePeriod <= s.RequestDeadline {
		return fmt.Errorf(
			"config: shutdown grace period (%s) must exceed request deadline (%s)",
			s.ShutdownGracePeriod, s.RequestDeadline,
		)
	}
	switch s.LogLevel {
	case "debug", "info", "warn", "error":
	default:
		return fmt.Errorf("config: unsupported log level %q", s.LogLevel)
	}
	return nil
}

func envString(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return fallback
}

func envDuration(key string, fallback time.Duration) (time.Duration, error) {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return fallback, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, fmt.Errorf("config: %s: %w", key, err)
	}
	return d, nil
}
