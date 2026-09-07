package deadline

import (
	"context"
	"testing"
	"time"
)

var now = time.Unix(1_700_000_000, 0)

const maximum = 80 * time.Millisecond

func TestACallerThatAsksForNothingGetsTheMaximum(t *testing.T) {
	got := Effective(context.Background(), 0, maximum, now)
	if got != maximum {
		t.Fatalf("effective = %v, want %v", got, maximum)
	}
}

func TestACallerMayAskForLess(t *testing.T) {
	got := Effective(context.Background(), 20*time.Millisecond, maximum, now)
	if got != 20*time.Millisecond {
		t.Fatalf("effective = %v, want 20ms", got)
	}
}

func TestACallerCanNeverAskForMore(t *testing.T) {
	// The whole point of the gateway being the only origin.
	got := Effective(context.Background(), time.Hour, maximum, now)
	if got != maximum {
		t.Fatalf("effective = %v, want %v", got, maximum)
	}
}

func TestAnInheritedDeadlineIsNeverExtended(t *testing.T) {
	ctx, cancel := context.WithDeadline(context.Background(), now.Add(10*time.Millisecond))
	defer cancel()

	// The caller's transport deadline is a promise already made to them.
	got := Effective(ctx, 60*time.Millisecond, maximum, now)
	if got != 10*time.Millisecond {
		t.Fatalf("effective = %v, want 10ms", got)
	}
}

func TestTheShortestConstraintWins(t *testing.T) {
	ctx, cancel := context.WithDeadline(context.Background(), now.Add(50*time.Millisecond))
	defer cancel()

	got := Effective(ctx, 5*time.Millisecond, maximum, now)
	if got != 5*time.Millisecond {
		t.Fatalf("effective = %v, want 5ms", got)
	}
}

func TestAnAlreadyExpiredDeadlineYieldsNoBudget(t *testing.T) {
	ctx, cancel := context.WithDeadline(context.Background(), now.Add(-time.Second))
	defer cancel()

	if got := Effective(ctx, 0, maximum, now); got != 0 {
		t.Fatalf("effective = %v, want 0", got)
	}
}

func TestEstablishBoundsTheContext(t *testing.T) {
	ctx, cancel := Establish(context.Background(), 20*time.Millisecond, maximum)
	defer cancel()

	inherited, ok := ctx.Deadline()
	if !ok {
		t.Fatal("Establish returned a context with no deadline")
	}
	if remaining := time.Until(inherited); remaining > 20*time.Millisecond {
		t.Fatalf("remaining = %v, want no more than 20ms", remaining)
	}
}

func TestExpiredRecognisesASpentBudget(t *testing.T) {
	live, cancelLive := context.WithTimeout(context.Background(), time.Minute)
	defer cancelLive()
	if Expired(live) {
		t.Error("a live context was reported as expired")
	}

	spent, cancelSpent := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancelSpent()
	if !Expired(spent) {
		t.Error("a spent context was not reported as expired")
	}

	cancelled, cancelNow := context.WithCancel(context.Background())
	cancelNow()
	if !Expired(cancelled) {
		t.Error("a cancelled context was not reported as expired")
	}
}
