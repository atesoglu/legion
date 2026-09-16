# ADR-018: Normalised reproducibility schema for decision lineage

**Status:** Accepted (supersedes the schema shipped in commit `47edb19`)
**Date:** 2026-09-16
**Phase:** 2 (restructured; migration required before Phase 2 investigation
work depends on querying lineage)

## Context

The lineage work that shipped in commit `47edb19` persists one row per
decision in `decision_lineage`: a handful of indexed columns (`decision_id`,
`transaction_id`, `decided_at`, `decision`, `shadow`) plus the entire
`DecisionLineage` protobuf message marshalled into a `bytea` column. That was
the right amount of schema for the job at the time — the job was "prove
lineage is constructed and persisted at all" — and it was explicitly
documented as provisional (`internal/lineage/postgres.go`'s own comment: "no
migration tool yet").

ADR-007 already named the reason this store exists: "write-heavy, queried
analytically." A `bytea` blob is not queryable analytically — every question
("what was `velocity`'s score distribution last week", "how many decisions
used the fallback policy") requires deserialising every row in application
code. That is fine for zero consumers. It stops being fine the moment
anything queries the store for more than "does this decision_id exist" —
which is exactly what Zone 6 (ADR-017), replay (ADR-013) and evaluation are
for.

## Decision

`decision_lineage` is replaced by a normalised set of tables, one per
concept the `DecisionLineage` message already distinguishes:

```text
decisions
    id (= decision_id), transaction_id, decided_at, decision,
    aggregate_score, degradation_state, policy_id, policy_version,
    shadow, deadline_ms, total_elapsed_ms

agent_evaluations
    id, decision_id (FK), agent_id, outcome (signal|failure),
    score, confidence, weight_basis_points, weighted_contribution,
    included, exclusion_reason, agent_version, observed_latency_ms

execution_spans
    id, decision_id (FK), stage, elapsed_ms, budget_ms

decision_failures
    id, decision_id (FK), kind, component, message, elapsed_ms, retryable

governed_versions
    decision_id (FK), artefact (agent|policy|feature_catalogue|model|
    prompt|output_schema|contract), name, version, digest
```

The full `DecisionLineage` protobuf message is **still stored**, in a
`raw_lineage bytea` column on `decisions`, as the authoritative,
byte-for-byte record replay (ADR-013) reconstructs from. The relational tables
are a queryable projection of it, not a second source of truth: on any
disagreement, `raw_lineage` wins, and a projection/reconstruction mismatch is
a bug in the writer, not an ambiguity to resolve at query time.

This mirrors the source material's separation of `feature_snapshots` /
`scores` / `decisions`, adapted to Legion's single `DecisionLineage` message
rather than reproduced as independent tables with independent write paths —
Legion has one writer (the orchestrator's lineage package) and one message
type; splitting the write path into several independently-populated tables
the way the source material does would reintroduce the "which table is
authoritative" question this design avoids.

A real migration tool (see Consequences) accompanies this schema; the
"idempotent `CREATE TABLE IF NOT EXISTS`" approach shipped in commit `47edb19`
does not extend to five related tables with foreign keys.

## Alternatives considered

**Leave the blob schema as-is and add columns to it as query needs arise.**
Rejected: `agent_evaluations` and `governed_versions` are naturally
one-to-many per decision; bolting repeated data into extra blob columns just
delays the same normalisation while accumulating query-time deserialisation
debt in the meantime.

**Fully replicate the source material's `scores`/`decisions` table split,
with `scores` written by the sentinel path and `decisions` written
separately.** Rejected: Legion's sentinel performs no I/O by design (ADR-002,
ADR-006) and never will; only the orchestrator writes lineage. Two tables
implies two writers only if two components produce the data, which is not
Legion's shape.

**A separate analytical store (columnar warehouse, e.g. ClickHouse) fed from
Postgres.** Rejected as premature, for the same reason ADR-007 rejects Kafka
for feature delivery: no current query volume justifies a second storage
technology, and Postgres already gives adequate analytical query performance
at Legion's scale.

## Trade-offs

- **A migration is now owed on already-shipped code.** This is a real cost
  of getting the schema right the second time rather than the first; it is
  paid once and is why this ADR exists rather than a silent edit to the
  `postgres.go` shipped in commit `47edb19` (see the ADR index's own rule:
  once implemented, supersede, do not edit history).
- **Five tables instead of one is more schema to reason about** for anyone
  writing a query, though each is narrower and more obviously named than a
  blob.
- **Write cost increases**: one decision now produces one `decisions` row,
  N `agent_evaluations` rows, 3 `execution_spans` rows, up to 7
  `governed_versions` rows, and 0+ `decision_failures` rows, instead of one
  row. This must be measured against the write-heavy claim in ADR-007 before
  it is trusted at production volume — no such measurement exists yet.

## Consequences

- A real migration tool is required (`golang-migrate` or equivalent) once
  this ships; the informal DDL-at-startup approach is retired for the
  lineage schema specifically. Investigation-plane tables (ADR-017) adopt the
  same tool from their first migration rather than repeating the informal
  approach.
- `internal/lineage` gains a normalising write path: one `INSERT` becomes a
  transaction inserting into five tables. This is strictly heavier than the
  shipped blob write and must be re-benchmarked against the "off the critical
  path, bounded queue" design ADR-007 and the original lineage work already
  established — the asynchrony is unchanged, but the per-entry writer cost is
  not.
- Zone 6 (ADR-017), replay (ADR-013) and evaluation all query the relational
  tables directly rather than deserialising `raw_lineage` per row, which is
  the entire point of doing this now rather than after Phase 5 depends on it.

## Revisit if

- Write-path benchmarking (owed regardless, per the still-open latency
  measurement question) shows the five-table write meaningfully threatens the
  lineage writer's "off the critical path" property even under its own
  asynchronous queue — in which case batching writes or reverting to a
  blob-plus-selective-columns hybrid is reconsidered.
- Query patterns turn out to need denormalised read models anyway (for
  example, a per-agent score-distribution view refreshed periodically) — that
  is a materialised view added on top of this schema, not a reason to undo it.
