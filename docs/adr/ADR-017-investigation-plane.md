# ADR-017: Investigation plane as Zone 6

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

## Decision

Investigation is a new trust zone — **Zone 6** — added after Zone 5 (state) in
the zone list in `security-boundaries.md` and `architecture.md`. It is async,
Go, Postgres-backed, and it does not touch the synchronous decision path's
latency budget (ADR-009) in any way.

Concretely:

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
   message to a Redis Streams stream when `DecisionOutcome.Decision ==
   REVIEW`. The orchestrator's request handler is unaware this happens; it
   still returns as soon as the sentinel answers. See ADR-019 for the queue
   choice.
4. **State lives in Postgres**, in the investigation plane's own tables
   (`cases`, `investigations`, `tasks`, `evidence`, `agent_findings`,
   `tool_executions`, `investigation_audit_events`), separate from the
   `decision_lineage`/reproducibility tables of ADR-020. They may share a
   Postgres instance initially; they are not the same schema, because a case
   evolves after the decision that created it and lineage must never be
   mutated once written.
5. **Investigation agents are still capability-bounded.** An investigation
   agent that touches a tool goes through the same deny-by-default posture
   ADR-005 established for the behavioural agent, not a separate, looser
   boundary invented for Zone 6. A tool registry is the Zone 6-specific
   instance of that same idea (see ADR-018 for how agents backing tools are
   registered).
6. **The investigation controller does not decide anything.** It selects
   which agents run and aggregates their findings into a report. It has no
   type it can populate with `ALLOW`/`REVIEW`/`DECLINE` — that authority
   stays where ADR-004 put it, and an investigation can at most recommend
   that a human change a case's status, never re-open the original decision.

## Alternatives considered

**Fold into Zone 2 (`control-plane/investigation/`).** Simpler repository
layout. Rejected: it would give the orchestrator — currently scoped to
"decide, never investigate" — a second responsibility, and blur exactly the
boundary ADR-004 depends on being legible. The orchestrator hands off a
`CaseTrigger`; it does not own what happens to it.

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

## Trade-offs

- **A new trust zone is a new thing to secure.** Zone 6 needs its own network
  policy, workload identity and Postgres credential story in Phase 4, exactly
  like Zones 2-5 did.
- **Two Postgres schemas to reason about.** Lineage (ADR-020) and
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

## Revisit if

- The investigation plane's load profile turns out to require the case
  creation to be visible to the caller within the same request after all —
  in which case the 80 ms deadline itself, not this ADR, is what has to move,
  and that is ADR-009's decision to revisit, not this one's.
- Zone 6 grows large enough that a single Postgres instance shared with
  lineage becomes a capacity or blast-radius problem, at which point separate
  instances are a configuration change, not a redesign.
