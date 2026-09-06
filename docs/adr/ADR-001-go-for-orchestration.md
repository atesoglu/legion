# ADR-001: Go for orchestration

**Status:** Accepted
**Date:** 2026-09-04
**Phase:** 0

## Context

The control plane — gateway and orchestrator — is I/O-bound and
concurrency-heavy. Per request it must terminate transport, authenticate,
validate, allocate a deadline budget, fan out to four agents in parallel, fetch
features, apply circuit breakers, assemble results, and cancel everything
cleanly when the deadline expires. Under load it runs thousands of these
concurrently.

The dominant costs here are not arithmetic. They are scheduling, cancellation
correctness, connection management and operational behaviour under partial
failure.

## Decision

Go implements the gateway, the orchestrator, the capability runtime and all
background and operational services.

Go owns: transport, authn/authz, validation, deadline propagation, routing,
fan-out/fan-in, retries, circuit breaking, rate limiting, health, configuration,
graceful shutdown, background workers.

Go does not own: risk calculations, scoring, normalisation, aggregation or
policy evaluation.

## Alternatives considered

**Rust for everything.** Tempting for consistency and for tail latency. The
async ecosystem is capable, but the control plane's value is in operational
maturity — mature gRPC middleware, OpenTelemetry integration, well-understood
graceful shutdown — and the orchestrator's work is dominated by waiting, where
Rust's advantages do not apply. It would also mean a single-language project
that cannot demonstrate an informed boundary between the two.

**Go for everything.** Simpler, and viable for the first version. Rejected
because the sentinel's tail latency is exposed to GC pauses at exactly the
percentile the 80 ms target is about, and because Go's type system cannot make
an out-of-range score unrepresentable the way a Rust newtype can.

**Java or .NET.** Both have excellent banking ecosystems and would be a
defensible choice in a real institution. Rejected here: heavier runtimes, JIT
warm-up affecting early-request tail latency, and no advantage for the specific
thing this project sets out to demonstrate.

## Trade-offs

- Garbage collection introduces tail latency variance in the control plane too.
  It is accepted there because the control plane's budget is a few milliseconds
  of coordination, not the decision arithmetic.
- Two languages means two toolchains, two dependency graphs, two CI paths and a
  serialisation boundary between them.
- Go's error handling is verbose and its type system will not prevent the
  domain errors that Rust's does. This is one of the reasons the decision
  belongs on the other side of the boundary.

## Consequences

- Every request-scoped operation carries a `context.Context`, and ignoring
  cancellation is treated as a defect.
- The orchestrator must never contain a threshold, a weight, or a mapping from
  a score to a decision.
- The Go/Rust boundary is a gRPC call whose serialisation cost must be measured,
  not assumed negligible (ADR-003, ADR-006).

## Revisit if

Profiling shows control-plane GC pauses consuming a material share of the P99
budget, or the orchestrator's work turns out to be CPU-bound rather than
coordination-bound.
