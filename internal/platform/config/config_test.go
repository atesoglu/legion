package config

import (
	"testing"
	"time"
)

func TestLoadServiceDefaults(t *testing.T) {
	svc, err := LoadService("gateway", ":9000")
	if err != nil {
		t.Fatalf("LoadService: %v", err)
	}
	if svc.RequestDeadline != DefaultRequestDeadline {
		t.Errorf("RequestDeadline = %s, want %s", svc.RequestDeadline, DefaultRequestDeadline)
	}
	if svc.ListenAddress != ":9000" {
		t.Errorf("ListenAddress = %q, want %q", svc.ListenAddress, ":9000")
	}
}

func TestLoadServiceOverridesDeadline(t *testing.T) {
	t.Setenv("LEGION_REQUEST_DEADLINE", "50ms")

	svc, err := LoadService("gateway", ":9000")
	if err != nil {
		t.Fatalf("LoadService: %v", err)
	}
	if svc.RequestDeadline != 50*time.Millisecond {
		t.Errorf("RequestDeadline = %s, want 50ms", svc.RequestDeadline)
	}
}

func TestLoadServiceRejectsUnparsableDuration(t *testing.T) {
	t.Setenv("LEGION_REQUEST_DEADLINE", "eighty")

	if _, err := LoadService("gateway", ":9000"); err == nil {
		t.Fatal("expected an error for an unparsable duration")
	}
}

// A grace period shorter than the request deadline would cut off in-flight
// decisions during a rollout, so it is rejected at startup rather than
// discovered during a deploy.
func TestLoadServiceRejectsGracePeriodBelowDeadline(t *testing.T) {
	t.Setenv("LEGION_REQUEST_DEADLINE", "80ms")
	t.Setenv("LEGION_SHUTDOWN_GRACE_PERIOD", "10ms")

	if _, err := LoadService("gateway", ":9000"); err == nil {
		t.Fatal("expected an error when grace period is below the request deadline")
	}
}

func TestLoadServiceRejectsUnknownLogLevel(t *testing.T) {
	t.Setenv("LEGION_LOG_LEVEL", "verbose")

	if _, err := LoadService("gateway", ":9000"); err == nil {
		t.Fatal("expected an error for an unknown log level")
	}
}
