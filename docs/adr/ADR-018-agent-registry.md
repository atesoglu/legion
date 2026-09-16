# ADR-018: Postgres-backed agent registry for behavioural and investigation agents

**Status:** Accepted
**Date:** 2026-09-16
**Phase:** 2 (restructured)

## Context

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

## Decision

Behavioural and investigation-side agents are registered as rows in a new
Postgres table, `agent_definitions`, owned by Zone 6:

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
agents" in the sense this ADR is about: each is its own deployable process
with its own workload identity (ADR-006), consulted synchronously inside the
80 ms budget, and registering one is inseparable from deploying it. Putting
them in `agent_definitions` would suggest a Postgres read belongs on the
decision path, which ADR-009's budget forbids. `registry.Registry` and
`agent_definitions` are two different mechanisms for the same principle
(ADR-014), applied to two different kinds of agent.

## Alternatives considered

**Extend the static env-var registry to cover behavioural/investigation
agents too.** Rejected: it cannot express hot-reload, cannot carry a prompt of
any real length sanely, and re-parsing on every request or on a signal is
worse ceremony than a database row.

**A plugin or dynamic-dispatch loading model.** Already rejected by ADR-014
for the same reasons; nothing about Zone 6 changes that calculus. Agents,
including investigation agents, are processes (the generic worker), not code
loaded into one.

**One registry table for all agents, deterministic engines included.**
Rejected per the Decision above: it would put a Postgres dependency between
the orchestrator and a decision it must make in single-digit milliseconds.

## Trade-offs

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

## Consequences

- `agent_definitions` versioning is what ADR-020's `GovernedVersions.agents`
  and `GovernedVersions.prompt` are populated from for investigation agents,
  the same way the deterministic engines' `CARGO_PKG_VERSION` populates it for
  them today.
- Registering a new investigation agent is a data change (a row plus a
  container image the worker knows how to invoke through the tool registry),
  not an orchestrator change — the ADR-014 property, now true for Zone 6 too.
- A capability/tool check (ADR-005, extended for Zone 6 per ADR-017) is still
  mandatory before any tool call; `allowed_tools` in this table is the
  configuration that check is enforced against, not a substitute for
  enforcement.

## Revisit if

- The number of behavioural/investigation agents stays small enough (a
  handful) that the operational cost of a second registry mechanism exceeds
  what it buys — in which case folding it back into one mechanism is worth
  reconsidering.
- Hot-reload of `agent_definitions` proves to need stronger consistency than
  "the next task pulls the current row" (for example, mid-flight tasks
  observing a version change), which would need an explicit versioning
  contract on task creation.
