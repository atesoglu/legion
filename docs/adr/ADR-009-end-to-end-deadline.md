# ADR-009: An 80 ms end-to-end request deadline

**Status:** Accepted
**Date:** 2026-09-04
**Phase:** 1

## Context

A risk decision sits inside a payment authorisation, which is itself inside a
customer's wait. The risk system's share of that budget is small and fixed;
exceeding it means the caller either times out and applies its own default —
usually approve — or the customer experiences the delay.

A system without a hard deadline does not fail; it degrades until it is
unusable, and every component believes it is behaving reasonably.

## Decision

The synchronous decision path targets **approximately 80 ms P99, end to end,
measured at the gateway, under realistic concurrency**.

Eighty milliseconds is a **request deadline**, not a per-component allowance.
It is established once at the gateway as
`min(caller_requested, configured_maximum)` and divided as the request
descends. Elapsed time is subtracted, never reset.

Full allocation and propagation rules: [deadline-model.md](../deadline-model.md).

Deadlines and circuit breakers are separate mechanisms answering separate
questions, implemented and documented independently.

## Alternatives considered

**Per-component timeouts with no propagation.** Common and wrong. Four
components with 80 ms each yields a system that can take 320 ms while every
component reports compliance.

**No deadline; best effort.** Rejected: the tail becomes unbounded exactly when
load is highest.

**A much tighter target, such as 20 ms.** Would exclude language-model
inference entirely and force a purely deterministic system. Defensible, and
possibly correct for card-present authorisation. Rejected here because it
removes the architectural problem the project exists to explore.

**A much looser target, such as 500 ms.** Comfortable, and not representative
of real authorisation constraints. Nothing interesting has to be solved.

## Trade-offs

- Eighty milliseconds constrains the architecture severely: it is why the
  deterministic path is Rust (ADR-002), why the deterministic engines are called
  in parallel rather than in sequence (ADR-006), why the model must be local and
  small (ADR-008), and why the feature store is in-memory (ADR-007).
- Inference receives the largest share of the budget and is the least
  predictable stage, so budget exhaustion under load will most often manifest as
  a missing behavioural signal.
- Roughly 19 % of the budget is held in reserve rather than allocated. A budget
  that sums exactly to the deadline misses it whenever anything is slightly
  slow.

## Consequences

- Every request-scoped call carries a deadline; ignoring cancellation is a
  defect.
- Nothing returns late. On expiry the gateway returns `DEADLINE_EXCEEDED`
  rather than a decision the caller has stopped waiting for.
- The sentinel's budget is protected. It is the only component that can produce
  a decision at all.
- Budget exhaustion is an explicit, observable outcome
  (`DEADLINE_BUDGET_EXHAUSTED`), not a silent skip.
- **The target is not claimed until measured.** P50, P95, P99, P99.9,
  throughput, error rate, timeout rate and fallback rate must all be published,
  per stage and end to end. If the target proves unachievable, the measurement
  is published and the target changes.

## Revisit if

Phase 1 benchmarks show the allocation is wrong — which is likely; the current
split is an estimate — or the target itself is unachievable with this
architecture. Either outcome is recorded with the numbers that produced it.
