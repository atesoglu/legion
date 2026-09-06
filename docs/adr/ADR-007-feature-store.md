# ADR-007: Redis/Dragonfly as the feature store

**Status:** Accepted
**Date:** 2026-09-04
**Phase:** 1

## Context

Agents need windowed aggregates — transactions in the last five minutes, unique
devices in 24 hours, summed amount today — within a feature-fetch budget of
roughly 12 ms including network, for a batch of features, at the P99.

The store is read on every transaction and written on every transaction. It is
a cache of derived state, not a system of record: everything in it is
recomputable from the event history.

## Decision

Redis, with Dragonfly as a drop-in alternative, is the initial feature store.
Access is exclusively through the control plane; no agent holds a connection.

Features are modelled as rolling-window aggregates, not static values. The
concrete data structures — sorted sets for time windows, HyperLogLog for
approximate distinct counts, hashes for point-in-time attributes — are a Phase 1
implementation decision, informed by benchmarking rather than assumption.

Feature *definitions* are versioned; a value carries the version that produced
it, and values from different definition versions are not comparable.

## Alternatives considered

**PostgreSQL.** Already understood, transactional, and capable of window
queries. Rejected for the hot path: per-request aggregate queries at this
latency and rate are not what it is good at. It remains the right choice for
the lineage store, which is write-heavy and queried analytically.

**A managed feature store (Feast, Tecton).** Purpose-built, with a point-in-time
correctness story that matters for training. Rejected: a large dependency whose
main value is the training/serving skew problem, which Legion does not have —
there is no trained model consuming these features. Self-hosting cost outweighs
the benefit at this stage.

**In-process state in the Rust data plane.** Fastest possible. Rejected:
features must survive restarts and be shared across replicas, which makes this a
distributed state problem rather than a caching one.

**Kafka plus a materialised view.** The natural shape at large scale. Rejected
as premature — a streaming platform is a significant operational commitment, and
no current requirement justifies it.

**Dragonfly instead of Redis from the start.** Better multi-core utilisation and
a compatible protocol. Deferred rather than rejected: Redis is the baseline
because it is the better-understood reference point, and Dragonfly can be
compared against it with the same benchmarks. Choosing on a benchmark is more
useful than choosing on a claim.

## Trade-offs

- **In-memory means bounded capacity.** Feature retention has a memory cost, and
  window sizes are therefore a capacity decision as well as a detection one.
- **Availability.** A store outage degrades every evaluation simultaneously.
  The failure model requires a local cache with an explicit staleness window and
  `FEATURE_STORE_DEGRADED`.
- **Approximation.** HyperLogLog trades exactness for memory. Where used, the
  error bound becomes part of the feature definition, because a rule comparing
  an approximate distinct count against a threshold of 3 is not measuring what
  it appears to measure.
- **Write amplification.** Every transaction updates several windows. Write cost
  is on the critical path and must be measured.

## Consequences

- Feature-store latency is measured, not assumed: P50/P95/P99 for local and
  networked deployment, hit and miss rates, and behaviour under degradation.
  This is a Phase 1 benchmark, and no latency claim precedes it.
- `FeatureFreshness` distinguishes fresh, stale, absent and unavailable, so an
  outage cannot masquerade as an absence of risk.
- The store is treated as recomputable. Losing it degrades detection quality
  temporarily; it does not lose truth.

## Revisit if

Benchmarks show the store cannot meet the feature-fetch budget under realistic
concurrency, memory cost makes the required windows unaffordable, or feature
computation outgrows what a key-value store can express.
