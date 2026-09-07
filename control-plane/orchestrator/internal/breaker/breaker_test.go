package breaker

import (
	"sync"
	"testing"
	"time"
)

type clock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func testBreaker() (*Breaker, *clock) {
	c := &clock{now: time.Unix(0, 0)}
	return newWithClock(Config{
		MinimumRequests: 4,
		FailureRatio:    0.5,
		Window:          time.Second,
		Cooldown:        time.Second,
	}, c.Now), c
}

func TestAClosedBreakerPassesTraffic(t *testing.T) {
	b, _ := testBreaker()
	if allowed, state := b.Allow(); !allowed || state != Closed {
		t.Fatalf("Allow = (%v, %v), want (true, closed)", allowed, state)
	}
}

func TestFailuresBelowTheMinimumDoNotOpenTheBreaker(t *testing.T) {
	b, _ := testBreaker()
	// Three failures, all of them, but below MinimumRequests.
	for range 3 {
		b.Record(false)
	}
	if b.State() != Closed {
		t.Fatalf("state = %v, want closed", b.State())
	}
}

func TestTheBreakerOpensOnceTheRatioIsExceeded(t *testing.T) {
	b, _ := testBreaker()
	for range 4 {
		b.Record(false)
	}

	if b.State() != Open {
		t.Fatalf("state = %v, want open", b.State())
	}
	if allowed, state := b.Allow(); allowed || state != Open {
		t.Fatalf("Allow = (%v, %v), want (false, open)", allowed, state)
	}
}

func TestAHealthyMajorityKeepsTheBreakerClosed(t *testing.T) {
	b, _ := testBreaker()
	b.Record(false)
	b.Record(true)
	b.Record(true)
	b.Record(true)

	if b.State() != Closed {
		t.Fatalf("state = %v, want closed", b.State())
	}
}

func TestTheCooldownAdmitsExactlyOneProbe(t *testing.T) {
	b, c := testBreaker()
	for range 4 {
		b.Record(false)
	}
	c.advance(time.Second)

	allowed, state := b.Allow()
	if !allowed || state != HalfOpen {
		t.Fatalf("first Allow = (%v, %v), want (true, half_open)", allowed, state)
	}

	// A recovering dependency must not be buried by the traffic that broke it.
	if allowed, _ := b.Allow(); allowed {
		t.Fatal("a second caller was admitted while the probe was in flight")
	}
}

func TestASuccessfulProbeClosesTheBreaker(t *testing.T) {
	b, c := testBreaker()
	for range 4 {
		b.Record(false)
	}
	c.advance(time.Second)

	b.Allow()
	b.Record(true)

	if b.State() != Closed {
		t.Fatalf("state = %v, want closed", b.State())
	}
}

func TestAFailedProbeReopensTheBreaker(t *testing.T) {
	b, c := testBreaker()
	for range 4 {
		b.Record(false)
	}
	c.advance(time.Second)

	b.Allow()
	b.Record(false)

	if b.State() != Open {
		t.Fatalf("state = %v, want open", b.State())
	}
	// The cooldown restarts from the failed probe, not from the first trip.
	if allowed, _ := b.Allow(); allowed {
		t.Fatal("Allow admitted traffic immediately after a failed probe")
	}
}

func TestCountsDoNotAccumulateAcrossWindows(t *testing.T) {
	b, c := testBreaker()
	b.Record(false)
	b.Record(false)
	b.Record(false)

	c.advance(2 * time.Second)

	// Old failures are out of the window; these four are a fresh majority.
	b.Record(true)
	b.Record(true)
	b.Record(true)
	b.Record(true)

	if b.State() != Closed {
		t.Fatalf("state = %v, want closed", b.State())
	}
}

func TestConcurrentUseIsSafe(t *testing.T) {
	b := New(Default())

	var wg sync.WaitGroup
	for i := range 50 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			b.Allow()
			b.Record(i%2 == 0)
			b.State()
		}(i)
	}
	wg.Wait()
}
