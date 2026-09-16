# ADR-017: Investigation plane — zone, agent registry and task queue

**Status:** Accepted
**Date:** 2026-09-16
**Phase:** 2 (restructured)

## Context

`domain-model.md` §7 originally declared: "Legion does not model case
management beyond emitting `REVIEW`." That was a deliberate Phase-1 boundary,
not an oversight — Phase 1 had to prove the deterministic path stood on its
own before anything downstream was justified.

A `REVIEW` outcome today is a dead end. The caller receives reason codes and a
contribution breakdown, and nothing in Legion ever looks at that transaction
again. A real risk platform needs the other half: a human or an automated
investigation must be able to gather further evidence, consult specialised
agents (some of them model-backed), and reach a documented conclusion — and
that conclusion, its evidence and its reasoning must be as auditable as the
original decision.

This ADR reverses the Phase-1 non-goal deliberately, in the direction of a
proposal reviewed against Legion's own constraints (see the session that
produced this ADR for the source material and the explicit rejections below).
It covers three interlocking questions together, deliberately, rather than as
three separate ADRs: where the investigation plane lives, how its agents are
registered, and how work is delivered to them. None of the three would ever
be revisited in isolation from the other two, and splitting them produced
three files that mostly cross-referenced each other rather than standing on
their own — a sign they were one decision, not three.

## Decision

### 1. Zone and repository placement

Investigation is a new trust zone — **Zone 6** — added after Zone 5 (state) in
the zone list in `security-boundaries.md` and `architecture.md`. It is async,
Go, Postgres-backed, and it does not touch the synchronous decision path's
latency budget (ADR-009) in any way.

1. **New top-level directory `investigation/`**, sibling to `gateway/`,
   `control-plane/` and `data-plane/`, per the repository-layout rule that a
   zone directory holds deployables and nothing else:
   - `investigation/controller/` — the investigation controller: creates a
     case when a decision is `REVIEW`, selects which agents to activate
     (initially rule-based, mirroring the deterministic-first philosophy of
     ADR-004), creates tasks, aggregates findings into a result.
   - `investigation/worker/` — the generic worker: claims a task, loads the
     referenced agent definition, executes its permitted tools, calls the
     shared inference runtime when the agent is model-backed, validates
     structured output, persists evidence and findings.
2. **Case creation is asynchronous and does not appear in
   `EvaluateTransactionResponse`.** This is the load-bearing deviation from
   the source material, which returns `case_id` inline. Legion's request
   deadline is 80 ms end-to-end; the source material's is 200-500 ms —
   roughly six times looser specifically because it assumes a synchronous
   database write on the hot path is affordable. It is not, here. A caller
   that needs to know about a case queries it separately, joined by
   `decision_id`, which is already the lineage correlation key.
3. **The trigger reuses the lineage writer, not a second synchronous
   dependency.** The orchestrator's existing off-critical-path lineage
   writer (`internal/lineage`, shipped for decision lineage) additionally
   publishes a small `CaseTrigger{decision_id, transaction_id, outcome}`
   message to a Redis Streams stream (§3, below) when
   `DecisionOutcome.Decision == REVIEW`. The orchestrator's request handler
   is unaware this happens; it still returns as soon as the sentinel
   answers.
4. **State lives in Postgres**, in the investigation plane's own tables
   (`cases`, `investigations`, `tasks`, `evidence`, `agent_findings`,
   `tool_executions`, `investigation_audit_events`), separate from the
   `decision_lineage`/reproducibility tables (ADR-018). They may share a
   Postgres instance initially; they are not the same schema, because a case
   evolves after the decision that created it and lineage must never be
   mutated once written.
5. **Investigation agents are still capability-bounded.** An investigation
   agent that touches a tool goes through the same deny-by-default posture
   ADR-005 established for the behavioural agent, not a separate, looser
   boundary invented for Zone 6. The agent registry (§2, below) is the Zone
   6-specific instance of that same idea.
6. **The investigation controller does not decide anything.** It selects
   which agents run and aggregates their findings into a report. It has no
   type it can populate with `ALLOW`/`REVIEW`/`DECLINE` — that authority
   stays where ADR-004 put it, and an investigation can at most recommend
   that a human change a case's status, never re-open the original decision.

### 2. Agent registry

ADR-014 established that the agent set is configuration, not code, and Phase 1
realises that as `registry.Parse` reading `LEGION_AGENT_ENDPOINTS` once at
orchestrator startup. That is the right amount of mechanism for three
deterministic Rust engines: they are separate deployables (ADR-006), their
identity is their endpoint, and restarting the orchestrator to add one is
cheap because there are so few of them.

It is the wrong amount of mechanism for Zone 6. An investigation agent is a
`system_prompt`, an `allowed_tools` list and a model policy — data, not a
deployable — and the whole point of "logical agents are configuration, not
processes" (restated from the source material, and consistent with ADR-014's
own premise) is that registering one must not require deploying anything or
restarting a service. A static, startup-parsed env var cannot do that.

Behavioural and investigation-side agents are therefore registered as rows in
a new Postgres table, `agent_definitions`, owned by Zone 6:

```text
agent_id, version, name, description, system_prompt, allowed_tools (jsonb),
model_policy (jsonb), configuration (jsonb), enabled, created_at
UNIQUE(agent_id, version)
```

This table is the source of truth for:

- the behavioural agent's prompt and model policy (moving it out of the
  orchestrator's static configuration, where it does not belong once it is
  runtime-mutable),
- every investigation-side agent Zone 6's workers execute.

It is registered and versioned through an API (`RegisterAgent`,
`ListAgents`, `SetEnabled`), not edited by hand — mirroring the source
material's `POST /agents` and the six-step, decision-preserving rollout ADR-014
already specifies (register at weight/priority zero, observe, shadow, then
promote).

**The three deterministic Rust engines are explicitly excluded from this
table and stay in the static env-var registry.** They are not "logical
agents" in the sense this section is about: each is its own deployable process
with its own workload identity (ADR-006), consulted synchronously inside the
80 ms budget, and registering one is inseparable from deploying it. Putting
them in `agent_definitions` would suggest a Postgres read belongs on the
decision path, which ADR-009's budget forbids. `registry.Registry` and
`agent_definitions` are two different mechanisms for the same principle
(ADR-014), applied to two different kinds of agent.

### 3. Task queue

The investigation controller creates one or more tasks per case and must hand
them to a pool of generic workers, which may run on different processes and
may crash mid-task. This needs delivery (at-least-once), consumer groups (so
multiple workers share the backlog without duplicating work), and a way to
detect and recover a worker that dies while holding a task.

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

**Fold Zone 6 into Zone 2 (`control-plane/investigation/`).** Simpler
repository layout. Rejected: it would give the orchestrator — currently
scoped to "decide, never investigate" — a second responsibility, and blur
exactly the boundary ADR-004 depends on being legible. The orchestrator hands
off a `CaseTrigger`; it does not own what happens to it.

**The source material's Python/FastAPI/SQLAlchemy stack for these
services.** Rejected outright and non-negotiably: `architecture.md` §2 states
plainly that no Python exists anywhere near the transaction path, and Zone 6,
while asynchronous, is still Legion infrastructure, not a notebook. Go is used
for the same reason ADR-001 gives: I/O-bound coordination work with a lot of
policy about when things happen.

**Synchronous case creation, `case_id` returned inline (the source
material's own design).** Rejected specifically because Legion's deadline is
roughly six times tighter. A synchronous Postgres write inside an 80 ms
budget that already spends 12 ms on a feature fetch and 8 ms on agent fan-out
is not a decision this project gets to make differently just because another
spec made it.

**Event-sourcing the whole platform to get investigation triggers "for
free".** Rejected for the same reason ADR-013 rejects it for replay: a
disproportionate architectural commitment when a small, targeted mechanism
(the lineage writer already emits one more message) achieves the goal.

**Extend the static env-var registry to cover behavioural/investigation
agents too.** Rejected: it cannot express hot-reload, cannot carry a prompt of
any real length sanely, and re-parsing on every request or on a signal is
worse ceremony than a database row.

**A plugin or dynamic-dispatch agent-loading model.** Already rejected by
ADR-014 for the same reasons; nothing about Zone 6 changes that calculus.
Agents, including investigation agents, are processes (the generic worker),
not code loaded into one.

**One registry table for all agents, deterministic engines included.**
Rejected: it would put a Postgres dependency between the orchestrator and a
decision it must make in single-digit milliseconds.

**Kafka for the task queue.** The natural choice at real scale, and what the
source material itself treats as premature. Rejected for the same reason
ADR-007 rejects it for feature delivery: no current requirement justifies the
operational commitment, and Redis Streams already provides consumer groups
and at-least-once delivery.

**A Postgres-native queue (`SELECT ... FOR UPDATE SKIP LOCKED`).** Keeps the
whole system on one dependency. Rejected for now: it reinvents what Redis
Streams already does simply, and couples task throughput to the same
Postgres instance that must also absorb case, evidence and lineage writes.
Worth revisiting if a second infrastructure dependency turns out to be the
worse trade — see Revisit if.

**Direct gRPC push from the controller to a worker pool, no queue at all.**
Rejected: it requires the controller to track worker liveness and capacity
itself, which a queue exists precisely to avoid, and it has no natural
redelivery story for a worker that dies mid-task.

**Sharing the feature-store Redis instance for tasking.** Rejected per §3
above — correlated blast radius between a hot-path dependency and a
fire-and-forget one.

## Trade-offs

- **A new trust zone is a new thing to secure.** Zone 6 needs its own network
  policy, workload identity and Postgres credential story in Phase 4, exactly
  like Zones 2-5 did.
- **Two Postgres schemas to reason about.** Lineage (ADR-018) and
  investigation state are deliberately not merged; a reader must know which
  one a given fact lives in.
- **The 80 ms budget buys silence, not speed, for the caller.** A caller
  cannot learn `case_id` synchronously. Anything that wants it must poll or
  subscribe, which is a real integration cost the source material's design
  does not have.
- **Investigation can lag the decision.** Because the trigger travels through
  a queue, there is a window — expected to be low milliseconds to low
  seconds — between a `REVIEW` decision and a case existing. Nothing in Legion
  may assume the case exists synchronously with the decision.
- **A second registry to keep straight.** Anyone extending the agent set now
  has to know which of two mechanisms it belongs to, based on "is it on the
  80 ms path" rather than "is it an agent."
- **`agent_definitions` availability now gates Zone 6, not Zone 2.** A
  Postgres outage stops investigations from starting; it must never be able
  to stop a real-time decision, and nothing in this ADR wires it anywhere
  near that path.
- **Prompts as database rows need the same review discipline they had as
  files.** A `system_prompt` column is easy to edit without the scrutiny a
  pull request would have forced; process, not the schema, has to hold this
  line.
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

- A new protobuf package, `legion.investigation.v1`, is required: `Case`,
  `Investigation`, `Task`, `Evidence`, `AgentFinding`, `ToolExecution`, and the
  service contracts the controller and workers implement.
- `docs/investigation-model.md` is the cross-cutting model document for this
  zone, the same role `decision-model.md` plays for Zone 2/3.
- The Phase 2 target changes shape: Phase 2 now delivers case creation, the
  task queue, the capability/tool runtime and a first working (possibly
  mocked-LLM) investigation agent end to end, before the shared inference
  runtime is wired in. See the restructured `project-plan.md` §34.
- `domain-model.md` §7's non-goal is narrowed: Legion still does not model
  the payment lifecycle beyond authorisation (capture, settlement,
  chargeback, dispute), and still does not model customers, only accounts.
  It no longer excludes case management.
- `agent_definitions` versioning is what `GovernedVersions.agents` and
  `GovernedVersions.prompt` are populated from for investigation agents
  (ADR-018), the same way the deterministic engines' `CARGO_PKG_VERSION`
  populates it for them today.
- Registering a new investigation agent is a data change (a row plus a
  container image the worker knows how to invoke through the tool registry),
  not an orchestrator change — the ADR-014 property, now true for Zone 6 too.
- A capability/tool check (ADR-005) is mandatory before any tool call;
  `allowed_tools` in `agent_definitions` is the configuration that check is
  enforced against, not a substitute for enforcement.
- Failure injection (Phase 6, per the source material's chaos scenarios now
  folded into `project-plan.md` §30/§34) must include: worker crash mid-task,
  queue redelivery of an already-completed task, and the queue's own Redis
  instance being unavailable or slow — each with a defined, tested outcome,
  the same discipline `failure-model.md` already holds the real-time path to.
- Task state queries (status, task counts by state) are Postgres queries,
  not Redis queries, because Redis here is transient delivery, not queryable
  state.
- Worker count scales independently of logical agent count, which is what
  makes "thousands of logical agents without thousands of processes" a
  property of the architecture rather than an aspiration — the task queue is
  the mechanism ADR-014's "agent set is configuration" needed all along to be
  true at scale, and Phase 1 never needed it because it never had more agents
  than it could afford to call synchronously.

## Revisit if

- The investigation plane's load profile turns out to require the case
  creation to be visible to the caller within the same request after all —
  in which case the 80 ms deadline itself, not this ADR, is what has to move,
  and that is ADR-009's decision to revisit, not this one's.
- Zone 6 grows large enough that a single Postgres instance shared with
  lineage becomes a capacity or blast-radius problem, at which point separate
  instances are a configuration change, not a redesign.
- The number of behavioural/investigation agents stays small enough (a
  handful) that the operational cost of a second registry mechanism exceeds
  what it buys — in which case folding it back into one mechanism is worth
  reconsidering.
- Hot-reload of `agent_definitions` proves to need stronger consistency than
  "the next task pulls the current row" (for example, mid-flight tasks
  observing a version change), which would need an explicit versioning
  contract on task creation.
- Task throughput or fan-out per case grows to the point that a second
  Postgres-adjacent write path (task-state transitions) becomes the
  bottleneck rather than the queue — at which point a purpose-built queue
  (Kafka, or a managed equivalent) is evaluated on a benchmark, not a guess.
- Redis Streams' delivery guarantees prove insufficient in failure-injection
  testing (Phase 6) — for example, silent message loss under a specific
  failure mode — in which case the queue technology, not just its
  configuration, is reopened.
