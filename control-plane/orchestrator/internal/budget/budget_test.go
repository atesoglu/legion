package budget

import (
	"context"
	"testing"
	"time"
)

func TestRemainingFallsBackWhenThereIsNoDeadline(t *testing.T) {
	got := Remaining(context.Background(), 80*time.Millisecond)
	if got != 80*time.Millisecond {
		t.Fatalf("Remaining = %v, want 80ms", got)
	}
}

func TestRemainingNeverGoesNegative(t *testing.T) {
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()

	if got := Remaining(ctx, time.Second); got != 0 {
		t.Fatalf("Remaining = %v, want 0", got)
	}
}

func TestTheSentinelShareIsWithheldFromTheAgentWindow(t *testing.T) {
	plan := Default()

	window, ok := plan.AgentWindow(80 * time.Millisecond)
	if !ok {
		t.Fatal("AgentWindow refused a full budget")
	}
	if window != plan.Agents {
		t.Fatalf("window = %v, want %v", window, plan.Agents)
	}
}

func TestTheAgentWindowShrinksRatherThanBorrowingFromTheSentinel(t *testing.T) {
	plan := Default()

	// 8ms left: the sentinel's 3ms is protected, so agents get 5ms, not 8ms.
	window, ok := plan.AgentWindow(8 * time.Millisecond)
	if !ok {
		t.Fatal("AgentWindow refused a workable budget")
	}
	if window != 5*time.Millisecond {
		t.Fatalf("window = %v, want 5ms", window)
	}
}

func TestAgentsAreSkippedRatherThanStarvingTheSentinel(t *testing.T) {
	plan := Default()

	// Only the sentinel's own share is left. Deciding on no signals beats
	// consulting agents and having no time left to decide.
	if _, ok := plan.AgentWindow(3 * time.Millisecond); ok {
		t.Fatal("AgentWindow ran agents with only the sentinel's share left")
	}
	if _, ok := plan.AgentWindow(2 * time.Millisecond); ok {
		t.Fatal("AgentWindow ran agents past the sentinel's share")
	}
}

func TestTheSentinelStillRunsOnWhateverIsLeft(t *testing.T) {
	plan := Default()

	window, ok := plan.SentinelWindow(1 * time.Millisecond)
	if !ok {
		t.Fatal("SentinelWindow refused the last millisecond")
	}
	if window != time.Millisecond {
		t.Fatalf("window = %v, want 1ms", window)
	}
}

func TestAnExhaustedBudgetCannotProduceADecision(t *testing.T) {
	if _, ok := Default().SentinelWindow(0); ok {
		t.Fatal("SentinelWindow granted time from an exhausted budget")
	}
}
