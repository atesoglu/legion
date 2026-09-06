# ADR-002: Rust for deterministic computation

**Status:** Accepted
**Date:** 2026-09-04
**Phase:** 0

## Context

The data plane evaluates rules over windowed features, normalises scores,
aggregates them under configured weights, and applies policy thresholds. This
work is CPU-bound, runs inside a tight tail-latency budget, and — critically —
must produce identical output for identical input, across machines and across
months, or the replay engine (ADR-013) proves nothing.

It is also the part of the system where a numeric error is a financial error. A
score that wraps, saturates unexpectedly, or drifts through floating-point
accumulation is a wrong decision about someone's money.

## Decision

Rust implements the velocity, device and geo engines and the sentinel.

Rust owns: deterministic risk calculations, feature transformations,
normalisation, aggregation and policy evaluation.

The workspace forbids `unsafe` (`unsafe_code = "forbid"`), and denies
`unwrap`, `expect`, `panic!` and `arithmetic_side_effects` on the decision
path.

## Alternatives considered

**Go, with care.** Achievable, and the pragmatic choice for a team that only
knows Go. Rejected because GC pauses land in the P99 tail this project is
explicitly about, and because Go cannot express `Score` as a type that cannot
hold 137.

**C++.** Comparable performance. Rejected: no memory-safety guarantee by
default, a heavier build and dependency story, and no benefit here that Rust
does not also provide.

**A rules engine or DSL interpreter.** Attractive for changing rules without
deploying. Rejected for now: an interpreter adds latency, makes determinism
harder to guarantee, and moves risk logic into configuration that is harder to
test than code. Policy *parameters* are configuration; policy *logic* is code.

## Trade-offs

- Two-language cost, as in ADR-001.
- Rust iteration is slower: longer compiles, and a borrow checker that charges
  up front for the guarantees it provides.
- Forbidding `unsafe` gives up optimisations that might matter later. That is
  deliberate: `unsafe` is not a performance strategy. Relaxing it requires a
  profile showing a real bottleneck, a benchmark showing the gain, a review, and
  an ADR superseding this one.
- Denying `arithmetic_side_effects` means every arithmetic operation on the
  decision path must state its overflow behaviour explicitly. This is verbose,
  and it is the point.

## Consequences

- Invariants live in types: `Score` cannot be constructed out of range,
  `Thresholds` cannot be constructed non-ascending, and `Decision` is reachable
  only through a policy.
- The sentinel performs no I/O whatsoever. It is a pure function of its
  request, which is what makes replay meaningful.
- Scores are integers. Floating-point weighted sums are not reproducible across
  compilers and evaluation orders, so they are not used.
- The Go/Rust serialisation boundary is a measured cost, not a free one.

## Revisit if

Profiling shows Rust computation is not on the critical path at all — in which
case the second language is not earning its cost — or if a genuine bottleneck
is found that only `unsafe` can address.
