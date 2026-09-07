// Package breaker implements the per-dependency circuit breaker.
//
// A breaker answers a different question from a deadline: not "how long may
// this request take" but "should we send traffic to this dependency at all".
// Timing out on every request against a dead dependency spends the whole budget
// discovering something already known. See docs/deadline-model.md §5.
package breaker

import (
	"sync"
	"time"
)

// State is the breaker's position in its cycle.
type State int

const (
	// Closed passes traffic and watches the failure ratio.
	Closed State = iota
	// Open fails immediately without attempting a call.
	Open
	// HalfOpen admits a single probe to test recovery.
	HalfOpen
)

// String renders the state for logs and metrics.
func (s State) String() string {
	switch s {
	case Closed:
		return "closed"
	case Open:
		return "open"
	case HalfOpen:
		return "half_open"
	default:
		return "unknown"
	}
}

// Config governs when a breaker opens and when it probes.
type Config struct {
	// MinimumRequests is the number of outcomes required before the ratio is
	// considered. Without it, the first failed request opens the breaker.
	MinimumRequests int

	// FailureRatio in (0, 1] at which the breaker opens.
	FailureRatio float64

	// Window over which outcomes are counted.
	Window time.Duration

	// Cooldown is how long the breaker stays open before admitting a probe.
	Cooldown time.Duration
}

// Default returns a conservative starting configuration. Like every other
// threshold in Legion, these are configured values awaiting measurement.
func Default() Config {
	return Config{
		MinimumRequests: 10,
		FailureRatio:    0.5,
		Window:          10 * time.Second,
		Cooldown:        5 * time.Second,
	}
}

// Breaker tracks one dependency. It is safe for concurrent use, which matters
// because the fan-out calls every agent in parallel.
type Breaker struct {
	config Config
	now    func() time.Time

	mu            sync.Mutex
	state         State
	successes     int
	failures      int
	windowStart   time.Time
	openedAt      time.Time
	probeInFlight bool
}

// New creates a closed breaker.
func New(config Config) *Breaker {
	return newWithClock(config, time.Now)
}

func newWithClock(config Config, now func() time.Time) *Breaker {
	return &Breaker{
		config:      config,
		now:         now,
		state:       Closed,
		windowStart: now(),
	}
}

// Allow reports whether a call may be attempted, and the state that decided it.
//
// Exactly one caller is admitted while half-open. Everything else is refused
// until that probe reports back, so a recovering dependency is not immediately
// buried by the traffic that broke it.
func (b *Breaker) Allow() (bool, State) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.state == Open && b.now().Sub(b.openedAt) >= b.config.Cooldown {
		b.state = HalfOpen
		b.probeInFlight = false
	}

	switch b.state {
	case Closed:
		return true, Closed
	case HalfOpen:
		if b.probeInFlight {
			return false, HalfOpen
		}
		b.probeInFlight = true
		return true, HalfOpen
	default:
		return false, Open
	}
}

// Record reports the outcome of an attempted call.
func (b *Breaker) Record(success bool) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.state == HalfOpen {
		b.probeInFlight = false
		if success {
			b.reset()
		} else {
			b.trip()
		}
		return
	}

	if b.now().Sub(b.windowStart) >= b.config.Window {
		b.successes, b.failures = 0, 0
		b.windowStart = b.now()
	}

	if success {
		b.successes++
	} else {
		b.failures++
	}

	total := b.successes + b.failures
	if total < b.config.MinimumRequests {
		return
	}
	if float64(b.failures)/float64(total) >= b.config.FailureRatio {
		b.trip()
	}
}

// State reports the breaker's current position, applying any elapsed cooldown.
func (b *Breaker) State() State {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.state == Open && b.now().Sub(b.openedAt) >= b.config.Cooldown {
		return HalfOpen
	}
	return b.state
}

func (b *Breaker) trip() {
	b.state = Open
	b.openedAt = b.now()
	b.successes, b.failures = 0, 0
	b.windowStart = b.now()
}

func (b *Breaker) reset() {
	b.state = Closed
	b.successes, b.failures = 0, 0
	b.windowStart = b.now()
}
