// Package deadline establishes the request budget every downstream stage
// inherits.
//
// The gateway is the only origin. A caller may ask for less time than the
// configured maximum; it can never ask for more. Every stage after this point
// receives a share of what remains, and elapsed time is subtracted rather than
// reset. See docs/deadline-model.md §3.
package deadline

import (
	"context"
	"time"
)

// Establish derives the context every downstream call inherits.
//
// requested is the caller's declared budget; zero or negative means it did not
// ask. maximum is the configured server ceiling.
//
// A caller that has already imposed a shorter gRPC deadline keeps it: the
// returned context never extends an existing one, because a deadline the caller
// set is a promise Legion has already made to them.
func Establish(
	ctx context.Context,
	requested time.Duration,
	maximum time.Duration,
) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, Effective(ctx, requested, maximum, time.Now()))
}

// Effective computes the budget Establish would apply.
//
// Separated so the arithmetic can be asserted directly rather than inferred
// from a context's remaining time.
func Effective(
	ctx context.Context,
	requested time.Duration,
	maximum time.Duration,
	now time.Time,
) time.Duration {
	effective := maximum
	if requested > 0 && requested < effective {
		effective = requested
	}

	if inherited, ok := ctx.Deadline(); ok {
		remaining := inherited.Sub(now)
		if remaining < effective {
			effective = remaining
		}
	}

	if effective < 0 {
		return 0
	}
	return effective
}

// Expired reports whether the budget is already spent, so that the caller can
// be told rather than sent a decision it has stopped waiting for.
func Expired(ctx context.Context) bool {
	if ctx.Err() != nil {
		return true
	}
	inherited, ok := ctx.Deadline()
	return ok && !inherited.After(time.Now())
}
