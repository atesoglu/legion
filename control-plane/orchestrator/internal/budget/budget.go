// Package budget divides one request deadline across the stages of an
// evaluation.
//
// The deadline is established once, at the gateway, and every stage receives a
// share of what remains rather than a fresh allowance. Elapsed time is
// subtracted, never reset. See docs/deadline-model.md.
package budget

import (
	"context"
	"time"
)

// Plan is the configured apportionment for the orchestrator's own stages.
//
// The values are an initial partition, to be revised from measured stage
// distributions in Phase 1. They are not a claim about achievable latency.
type Plan struct {
	// Agents is the window shared by the parallel agent fan-out. It is a
	// window, not a per-agent allowance: the slowest agent closes it.
	Agents time.Duration

	// Sentinel is protected. It is never lent to another stage, because the
	// sentinel is the only component that can produce a decision at all.
	Sentinel time.Duration
}

// Default returns the apportionment documented in the deadline model.
func Default() Plan {
	return Plan{
		Agents:   8 * time.Millisecond,
		Sentinel: 3 * time.Millisecond,
	}
}

// Remaining reports the time left on ctx, or fallback when it carries no
// deadline. A context already past its deadline yields zero, never a negative.
func Remaining(ctx context.Context, fallback time.Duration) time.Duration {
	deadline, ok := ctx.Deadline()
	if !ok {
		return fallback
	}
	remaining := time.Until(deadline)
	if remaining < 0 {
		return 0
	}
	return remaining
}

// AgentWindow returns the window to grant the agent fan-out, and whether the
// fan-out can run at all.
//
// The sentinel's share is withheld first. When too little time is left to
// consult any agent, the evaluation proceeds without them rather than
// overrunning: a decision on partial evidence within the deadline is worth more
// than a complete one the caller has stopped waiting for.
func (p Plan) AgentWindow(remaining time.Duration) (time.Duration, bool) {
	usable := remaining - p.Sentinel
	if usable <= 0 {
		return 0, false
	}
	if usable < p.Agents {
		return usable, true
	}
	return p.Agents, true
}

// SentinelWindow returns the protected window for the decision itself, and
// whether any time remains for it.
//
// A false here means the request cannot produce a decision within its deadline,
// which the caller must surface as DEADLINE_EXCEEDED rather than as a decision.
func (p Plan) SentinelWindow(remaining time.Duration) (time.Duration, bool) {
	if remaining <= 0 {
		return 0, false
	}
	if remaining < p.Sentinel {
		return remaining, true
	}
	return p.Sentinel, true
}
