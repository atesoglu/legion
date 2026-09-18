# Investigation model

Status: partially built. The case → investigation → task → mocked-agent →
evidence/finding loop described below exists in code
(`investigation/controller`, `investigation/worker`,
`investigation/internal/{store,queue}`), with two seeded agents
(`device_investigation_agent`, `velocity_investigation_agent`), each tool
call checked by a real capability runtime (ADR-005,
`control-plane/capability`) before it runs — but no real tool or model
behind either agent, see §7 and §10 for exactly what that does and does not
mean. Still documented intent only: the `RegisterAgent` API, a relationship
rule/agent, observability and cost.

## 1. Where this fits

`REVIEW` used to be where Legion's story ended: the caller received reason
codes and a contribution breakdown, and nothing further happened. This
document describes what happens next.

```text
Risk Subject → Evidence → Signals → Decision   (decision-model.md, built)
                                        │
                                        ▼  REVIEW only, async, off-budget
                              Case → Investigation → Findings → Result
                                        (this document, partially built)
```

The two halves are deliberately separate systems joined by one identifier:
`decision_id`. Nothing in Zone 6 can change a `DecisionOutcome`, and nothing
in Zone 2/3 knows whether a case was ever opened for its output.

## 2. Why this is asynchronous, and why that is non-negotiable

Legion's request deadline is 80 ms end-to-end (ADR-009), unmeasured but
unmoved by this document. A synchronous case-creation write inside that budget
would compete with the 12 ms feature fetch and the 8 ms agent fan-out for a
share of a budget that already has none to spare.

`EvaluateTransactionResponse` therefore never carries a `case_id`. A caller
that needs one queries for it, joined by `decision_id`, after the fact. This
is the one deliberate structural deviation from the source material this
document is otherwise faithful to — that material returns `case_id` inline,
which is affordable only because its own scoring/decision latency target
(200–500 ms) is roughly six times looser than Legion's.

The trigger mechanism reuses the orchestrator's existing off-critical-path
lineage writer (see `decision-model.md` §7): when `DecisionOutcome.decision ==
REVIEW`, the same asynchronous write that persists lineage also publishes a
small `CaseTrigger` message. The orchestrator's request-handling goroutine
never waits on Zone 6 in any way.

## 3. Domain objects

Extending `domain-model.md`'s glossary, scoped to Zone 6:

| Term | Meaning |
|---|---|
| **Case** | One investigable unit, opened for a transaction that reached `REVIEW` (or was explicitly escalated). Has a status and a priority. |
| **Investigation** | One attempt at investigating a case. A case may have more than one over time. |
| **Task** | One agent invocation within an investigation, with its own lifecycle. |
| **Agent definition** | A registered logical agent: prompt, allowed tools, model policy, version, enabled (ADR-017). Distinct from the three deterministic Rust engines, which stay on the static registry. |
| **Tool execution** | One tool call a worker made on an agent's behalf: arguments, result, status, duration. Complete auditability of what a worker actually did, independent of what the agent concluded from it. |
| **Evidence** | A fact gathered during investigation. Exists independently of any agent's interpretation. |
| **Agent finding** | One agent's observation, hypothesis and confidence, referencing the evidence it is based on. |
| **Investigation audit event** | An append-only record of what happened, when, to what — the Zone 6 analogue of an operational log, but retained for audit rather than for operations. |

The distinction that matters most, adopted exactly as the source material
states it because it is correct:

```text
Evidence  ≠  Observation  ≠  Hypothesis  ≠  Final decision
```

An agent's conclusion always cites the evidence it came from. Evidence never
depends on any agent having drawn a conclusion from it — an analyst must be
able to ask "what was actually observed" independent of what any agent made of
it.

## 4. Lifecycles

### Case

```text
OPEN → INVESTIGATING → WAITING → COMPLETED
                              → ESCALATED
                              → CLOSED
```

Case creation is idempotent on the transaction's idempotency key (ADR-019),
because the task queue's at-least-once delivery (ADR-017) means a `CaseTrigger`
may be delivered more than once, and it must never open two cases for one
transaction.

### Task

```text
PENDING ──► RUNNING ──► COMPLETED
               │
               ├──► FAILED        no retry can change the outcome
               │
               └──► RETRYING      another attempt may survive it
                       │
                       ├──► lease expiry redelivers ──► RUNNING
                       │
                       └──► attempts spent ──────────► DEAD_LETTER
```

Postgres, not the queue, is the source of truth for this state machine. A
worker writes `RUNNING` before doing any work, so a crash mid-task is visible
and recoverable, never silently lost.

The queue message is acknowledged only once the task's outcome is durable in
Postgres. A delivery that could not conclude — a database outage during the
claim, the agent's writes, or the completion — leaves the message pending so
the lease recovers it. The cost of that choice is that a redelivery re-runs
the agent's work, which at-least-once delivery implies anyway and
`max_attempts` bounds.

The three ways a task can stop are deliberately distinct, because they mean
different things to whoever reads the row later:

| State | Meaning | Terminal | Message |
|---|---|---|---|
| `RETRYING` | This attempt failed for a reason another attempt might survive — a database blip, a tool error. | No | Left pending; lease recovery brings it back. |
| `FAILED` | No number of retries changes the answer: the agent is not registered, or the capability runtime denied its tool. | Yes | Acknowledged. |
| `DEAD_LETTER` | Retried until `max_attempts` was spent. | Yes | Acknowledged. |

**There is no separate retry queue, and no `RETRYING → PENDING` re-dispatch.**
A retry is just the lease recovery that already covers a crashed worker: the
message was never acknowledged, so `XAUTOCLAIM` hands it back and the worker
claims the task again, incrementing `attempt`. One mechanism covers both a
crash and a failure, which is why neither needs the controller's involvement.

`WAITING` is designed (it is in the `TaskStatus` proto enum) but not built:
no agent can request a follow-up tool call or a second agent yet, because
every agent's tool call is still mocked. Nothing writes that state.

### Worker

```text
READ TASK → CLAIM TASK → LOAD AGENT → LOAD CONTEXT
    → EXECUTE TOOLS → CALL LLM → VALIDATE OUTPUT
    → STORE FINDINGS → MARK COMPLETE
```

A worker is generic: it does not know which logical agent it will run next
until it loads the task. This is what decouples worker count from agent
count — see §6.

## 5. Agent activation strategy

Not every registered agent runs for every case. The controller is initially
rule-based:

```text
if device_risk_signal > threshold:
    activate("device_investigation_agent")

if velocity_risk_signal > threshold:
    activate("velocity_investigation_agent")

if relationship_signal_present:
    activate("relationship_investigation_agent")
```

A quiet case might activate one agent; a case with several concurrent signals
might activate five. No case activates every registered agent — see §6 for
why this property is load-bearing, not cosmetic.

The controller selects and aggregates. It never decides. It has no field it
can populate with `ALLOW`/`REVIEW`/`DECLINE`, and its output is a
recommendation a human or a downstream process may act on, never an automatic
reversal of the original `DecisionOutcome` (ADR-004, restated here for Zone 6).

## 6. Agents are configuration here too, at a different scale

ADR-014 established that the (deterministic, synchronous) agent set is
configuration, realised as a small static registry. Zone 6 needs the same
principle to hold at a much larger N — "thousands of logical agents" is a
plausible future for an investigation platform in a way it never was for
three engines called inside an 8 ms window.

Two mechanisms make that concretely true rather than aspirational (ADR-017):

- **Agent definitions are Postgres rows**, hot-registerable without deploying
  anything, distinct from the static env-var registry the real-time agents
  use.
- **Workers are generic and scale on queue depth**, not on agent count. A
  worker pool of a fixed size can serve an arbitrarily large registered agent
  population, because at any moment only the *active* tasks for the agents a
  case actually activated consume a worker.

```text
Logical agents   ≠   Active agents   ≠   Tasks   ≠   Workers
```

Illustrative, not a target: 10,000 registered agents, 500 active
investigations, 2,000 active tasks, 100 workers. These numbers are determined
by benchmarking (Phase 7), not assumed.

## 7. Tool registry and capability boundary

An investigation agent is capability-bounded exactly as the behavioural agent
is (`capability-model.md`), not by a separate, looser mechanism invented for
Zone 6. A tool call is checked against the agent's `allowed_tools`
(ADR-017) before it runs; the LLM is never permitted to select an arbitrary
Python function, shell command, SQL statement or network destination — it
selects from a closed, named tool registry, and the runtime — not the model —
decides whether the call is authorised.

**Built, with real limits.** `control-plane/capability` runs the pipeline
above: `investigation/worker` calls `CheckToolCapability` before every tool
execution, and a denial actually stops it and fails the task permanently
(§4's `FAILED`, not a retry — asking the same broker the same question gets
the same answer) rather than logging a warning and continuing. What is not
real yet: the tool call itself is still one canned, mocked result per task,
so the check gates a fake action, not an arbitrary one; and `workload_id` is
asserted by the caller, a Phase 1 stand-in for the mTLS peer identity
ADR-011 will eventually provide.

The grant does **not** come from `agent_definitions`, and that is not an
oversight. `allowed_tools` is a declaration owned by Zone 6 — whose worker
holds write credentials for that database — while the grant is reviewed Go
source in Zone 2 that nothing in Zone 6 can reach. Reading one from the
other would let the constrained component write its own constraint (T-14).
What is required is that the declaration never exceeds the grant, and
`test/e2e/capability_registry_test.go` asserts exactly that in CI: every
tool a seeded agent declares is checked against the running capability
runtime, and no agent may hold a tool only another agent declares. See
`capability-model.md` §4 for the direction that is still undetected.

## 8. Observability and cost

Deferred to Phase 4 in sequencing, specified now so it is not lost in the
meantime (see `project-plan.md` §50 for the full list). In short: every
investigation-path request carries `case_id`, `investigation_id`, `task_id`
and `agent_id` alongside the correlation IDs the decision path already
carries; task/queue/tool/LLM metrics are additive to the existing metrics
list; and **cost per investigation is a first-class, queryable metric** —
the first component Legion has ever had whose per-invocation cost is
variable rather than fixed CPU time.

## 9. Testing

The source material's six-category taxonomy (Functional, Reliability,
Security, Performance, Reproducibility, Agent quality) is adopted in
`project-plan.md` §51. "Agent quality" — correct evidence, no fabricated
evidence, appropriate tool usage — is new: Legion previously only had a
category for grading what an agent was *allowed* to do, never what it
*concluded*.

The critical end-to-end acceptance test: a transaction with high device risk
and high velocity, traced automatically through decision → case →
investigation → agent activation → task execution → evidence → findings →
investigation completion, with every version recorded. This becomes Legion's
Phase 2/3 acceptance test, the investigation-plane analogue of
`test/e2e`'s existing "starts the real binaries" pipeline test.

## 10. What this model does not claim

- It does not claim the rule-based activation strategy is good triage. It is
  a starting point, evaluated the same way risk-scoring thresholds are
  (Phase 5). Two rules exist today (device and velocity score over a
  threshold), one per seeded agent; no relationship rule exists because no
  relationship investigation agent is seeded.
- It does not claim an investigation agent's hypothesis is correct. Confidence
  is self-reported, exactly as it is for a `RiskSignal`, and is not validated
  against ground truth until Phase 5's evaluation framework exists. Today it
  is not even a real hypothesis: both seeded agents' findings are canned
  output, read from their `AgentDefinition.configuration`, not a model's
  conclusion.
- It does not claim this replaces a human analyst. The investigation result
  is evidence for one, not a replacement of one.
- It does not claim the capability check gates anything but a mocked tool
  call — see §7. Nor does it claim the capability runtime's grants are
  complete: nothing detects a grant no agent declares, because the manifest
  is deliberately not enumerable over the wire.
- It does not claim case creation is safe from duplication across callers.
  A case is deduplicated on `idempotency_key` alone (see `CaseTrigger`'s
  proto comment), not `(caller, idempotency_key)` as the gateway's own dedup
  is; two different callers reusing the same key would collide into one
  case. This is a known, accepted gap, not an oversight discovered later.
- It does not claim the task state machine in §4 is fully implemented.
  `WAITING` exists in the proto enum but nothing writes it, because no agent
  can request follow-up work while every tool call is mocked. Retry backoff
  is the queue's fixed 30-second lease, not a policy: there is no
  exponential backoff, no per-agent retry budget, and no alert when a task
  reaches `DEAD_LETTER`.
- It does not claim a retried task re-runs cleanly. A task that failed after
  writing some of its evidence writes it again on the next attempt, because
  the agent's writes are not transactional and nothing deduplicates them.
  At-least-once delivery implies this; `max_attempts` bounds it.
