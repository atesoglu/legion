package ratelimit

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

func limiter() (*Limiter, *clock) {
	c := &clock{now: time.Unix(0, 0)}
	return newWithClock(Config{Rate: 10, Burst: 5}, c.Now), c
}

func TestABurstIsAdmittedThenRefused(t *testing.T) {
	l, _ := limiter()

	for i := range 5 {
		if !l.Allow("acquirer-a") {
			t.Fatalf("request %d was refused inside the burst", i)
		}
	}
	if l.Allow("acquirer-a") {
		t.Fatal("a request past the burst was admitted")
	}
}

func TestTokensRefillOverTime(t *testing.T) {
	l, c := limiter()
	for range 5 {
		l.Allow("acquirer-a")
	}

	c.advance(200 * time.Millisecond) // two tokens at 10/s

	if !l.Allow("acquirer-a") || !l.Allow("acquirer-a") {
		t.Fatal("refilled tokens were not admitted")
	}
	if l.Allow("acquirer-a") {
		t.Fatal("more tokens were admitted than had refilled")
	}
}

func TestRefillDoesNotExceedTheBurst(t *testing.T) {
	l, c := limiter()
	l.Allow("acquirer-a")

	c.advance(time.Hour)

	for i := range 5 {
		if !l.Allow("acquirer-a") {
			t.Fatalf("request %d was refused after a long idle period", i)
		}
	}
	if l.Allow("acquirer-a") {
		t.Fatal("an idle caller accumulated more than the burst")
	}
}

func TestOneCallerCannotExhaustAnother(t *testing.T) {
	l, _ := limiter()
	for range 6 {
		l.Allow("noisy")
	}

	// A single misbehaving integration must not become everyone's outage.
	if !l.Allow("quiet") {
		t.Fatal("one caller's burst refused another caller")
	}
}

func TestAFirstRequestFindsAFullBucket(t *testing.T) {
	l, c := limiter()
	c.advance(time.Hour)

	if !l.Allow("new-caller") {
		t.Fatal("a caller's first request was refused")
	}
}

func TestIdleCallersAreForgotten(t *testing.T) {
	l, c := limiter()
	l.Allow("transient")
	if l.Tracked() != 1 {
		t.Fatalf("tracked = %d, want 1", l.Tracked())
	}

	c.advance(2 * time.Hour)
	l.Forget(time.Hour)

	// Otherwise the map grows with every distinct caller ever seen.
	if l.Tracked() != 0 {
		t.Fatalf("tracked = %d after expiry, want 0", l.Tracked())
	}
}

func TestActiveCallersAreNotForgotten(t *testing.T) {
	l, c := limiter()
	l.Allow("steady")

	c.advance(time.Minute)
	l.Forget(time.Hour)

	if l.Tracked() != 1 {
		t.Fatalf("tracked = %d, want 1", l.Tracked())
	}
}

func TestConcurrentUseIsSafe(t *testing.T) {
	l := New(Default())

	var wg sync.WaitGroup
	for i := range 50 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			l.Allow("caller")
			l.Forget(time.Hour)
			l.Tracked()
			_ = i
		}(i)
	}
	wg.Wait()
}
