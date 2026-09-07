// Package ratelimit bounds how much work one caller can ask for.
//
// The limit is per caller rather than global: a single misbehaving integration
// must not be able to exhaust the platform for everyone else, and a shared
// global limit turns one caller's incident into an outage for the rest.
//
// It is per process. Two gateway replicas each admit the configured rate, so
// the effective limit is the configured one multiplied by the replica count.
// A distributed limiter is a Phase 4 concern; until then the configured value
// is a per-replica value and is documented as such.
package ratelimit

import (
	"sync"
	"time"
)

// Config governs the token bucket applied to each caller.
type Config struct {
	// Rate is the sustained requests per second permitted per caller.
	Rate float64

	// Burst is how many requests may arrive at once before the sustained rate
	// applies. A burst of one would reject normal traffic that happens to be
	// bunched, which is most traffic.
	Burst float64
}

// Default returns a conservative starting configuration awaiting measurement.
func Default() Config {
	return Config{Rate: 100, Burst: 200}
}

// Limiter admits or refuses requests per caller.
type Limiter struct {
	config Config
	now    func() time.Time

	mu      sync.Mutex
	buckets map[string]*bucket
}

type bucket struct {
	tokens   float64
	lastSeen time.Time
}

// New creates a limiter.
func New(config Config) *Limiter {
	return newWithClock(config, time.Now)
}

func newWithClock(config Config, now func() time.Time) *Limiter {
	return &Limiter{
		config:  config,
		now:     now,
		buckets: make(map[string]*bucket),
	}
}

// Allow reports whether this caller may proceed, consuming a token if so.
func (l *Limiter) Allow(caller string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	held, known := l.buckets[caller]
	if !known {
		// A caller's first request finds a full bucket, so an idle
		// integration is never penalised for having been idle.
		held = &bucket{tokens: l.config.Burst, lastSeen: now}
		l.buckets[caller] = held
	}

	elapsed := now.Sub(held.lastSeen).Seconds()
	if elapsed > 0 {
		held.tokens += elapsed * l.config.Rate
		if held.tokens > l.config.Burst {
			held.tokens = l.config.Burst
		}
	}
	held.lastSeen = now

	if held.tokens < 1 {
		return false
	}
	held.tokens--
	return true
}

// Forget drops callers idle for longer than ttl.
//
// Without it the map grows with every distinct caller ever seen, which is a
// slow memory leak keyed by something an attacker partly controls.
func (l *Limiter) Forget(ttl time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()

	cutoff := l.now().Add(-ttl)
	for caller, held := range l.buckets {
		if held.lastSeen.Before(cutoff) {
			delete(l.buckets, caller)
		}
	}
}

// Tracked reports how many callers currently hold a bucket.
func (l *Limiter) Tracked() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.buckets)
}
