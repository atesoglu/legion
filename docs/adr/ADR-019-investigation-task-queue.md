# ADR-019: Redis Streams as the investigation task queue

**Status:** Accepted
**Date:** 2026-09-16
**Phase:** 2 (restructured)

## Context

The investigation controller (ADR-017) creates one or more tasks per case and
must hand them to a pool of generic workers, which may run on different
processes and may crash mid-task. This needs delivery (at-least-once),
consumer groups (so multiple workers share the backlog without duplicating
work), and a way to detect and recover a worker that dies while holding a
task.

Legion already depends on Redis for the feature store (ADR-007). The question
is whether investigation tasking reuses that dependency, and with what
mechanism.

## Decision

Redis Streams is the task-delivery mechanism, on a **separate Redis instance
from the feature store**, consumed via consumer groups by the generic worker
pool.

- **Postgres is the source of truth for task state** (`PENDING`, `RUNNING`,
  `RETRYING`, `COMPLETED`, `FAILED`, `DEAD_LETTER`); Redis Streams is delivery
  only. A worker claiming a task writes `RUNNING` to Postgres before doing any
  work, so task state survives a Redis restart and a queue replay can never
  become the only record that a task happened.
- **Separate instance from the feature store**, not a separate keyspace on
  the same one. The feature store is on the 80 ms decision path; the task
  queue is not, and is expected to carry a very different load shape
  (potentially large fan-out per case, no per-request latency budget). Sharing
  an instance would let investigation load degrade real-time feature lookups,
  which is exactly the correlated-failure mistake ADR-006 already rejected
  for the Rust engines, applied here to a dependency instead of a process.
- **Lease-based recovery.** A claimed message that is not acknowledged within
  a lease window is eligible for another consumer to claim (Redis Streams'
  pending-entries list gives this directly). Combined with Postgres' `attempt`
  and `max_attempts` columns, this is what turns a worker crash into a retry
  rather than a lost task.
- **One stream to start:** `investigation.tasks`. Splitting by agent kind or
  priority is deferred until a real load pattern justifies it.

## Alternatives considered

**Kafka.** The natural choice at real scale, and what the source material
itself treats as premature. Rejected for the same reason ADR-007 rejects it
for feature delivery: no current requirement justifies the operational
commitment, and Redis Streams already provides consumer groups and
at-least-once delivery.

**A Postgres-native queue (`SELECT ... FOR UPDATE SKIP LOCKED`).** Keeps the
whole system on one dependency. Rejected for now: it reinvents what Redis
Streams already does simply, and couples task throughput to the same
Postgres instance that must also absorb case, evidence and lineage writes.
Worth revisiting if a second infrastructure dependency turns out to be the
worse trade — see Revisit if.

**Direct gRPC push from the controller to a worker pool.** No queue at all;
the controller calls a worker directly. Rejected: it requires the controller
to track worker liveness and capacity itself, which a queue exists precisely
to avoid, and it has no natural redelivery story for a worker that dies
mid-task.

**Sharing the feature-store Redis instance.** Rejected per the Decision above
— correlated blast radius between a hot-path dependency and a fire-and-forget
one.

## Trade-offs

- **A second Redis instance is a second thing to operate and monitor**,
  including its own memory sizing, separate from the feature store's.
- **At-least-once delivery means every task handler must be idempotent** —
  the same task ID may be claimed and executed more than once under crash
  scenarios, and the worker lifecycle must tolerate that (see the task's
  `attempt` counter and the case-level idempotency discussion in
  `investigation-model.md`).
- **Redis Streams' pending-entries-list recovery is coarser than a purpose-
  built job scheduler.** Priority queuing, fairness across cases, and
  fine-grained backoff policies are not first-class here and would need
  application-level logic in the controller/worker if they become necessary.

## Consequences

- Failure injection (Phase 6, per the source material's chaos scenarios now
  folded into `project-plan.md` §30/§34) must include: worker crash mid-task,
  queue redelivery of an already-completed task, and the queue's own Redis
  instance being unavailable or slow — each with a defined, tested outcome,
  the same discipline `failure-model.md` already holds the real-time path to.
- Task state queries (`GET /investigations/{id}`-style status, task counts by
  state) are Postgres queries, not Redis queries, because Redis here is
  transient delivery, not queryable state.
- Worker count scales independently of logical agent count, which is what
  makes "thousands of logical agents without thousands of processes" a
  property of the architecture rather than an aspiration — the task queue is
  the mechanism ADR-014's "agent set is configuration" needed all along to be
  true at scale, and Phase 1 never needed it because it never had more agents
  than it could afford to call synchronously.

## Revisit if

- Task throughput or fan-out per case grows to the point that a second
  Postgres-adjacent write path (task-state transitions) becomes the
  bottleneck rather than the queue — at which point a purpose-built queue
  (Kafka, or a managed equivalent) is evaluated on a benchmark, not a guess.
- Redis Streams' delivery guarantees prove insufficient in failure-injection
  testing (Phase 6) — for example, silent message loss under a specific
  failure mode — in which case the queue technology, not just its
  configuration, is reopened.
