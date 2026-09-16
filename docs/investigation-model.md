# Investigation model

Status: Documented intent only (ADR-017 to ADR-019). Nothing in this document
is built. It exists so that Zone 6 is specified before it is coded, the same
discipline `decision-model.md` held Zone 2/3 to in Phase 0.

## 1. Where this fits

`REVIEW` used to be where Legion's story ended: the caller received reason
codes and a contribution breakdown, and nothing further happened. This
document describes what happens next.

```text
Risk Subject → Evidence → Signals → Decision   (decision-model.md, built)
                                        │
                                        ▼  REVIEW only, async, off-budget
                              Case → Investigation → Findings → Result
                                        (this document, not built)
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
PENDING → RUNNING → COMPLETED
             │
             ├──► WAITING (needs a follow-up tool call or agent)
             │
             └──► lease expiry / worker crash
                       │
                       ▼
                   RETRYING → PENDING
                       │
                 max_attempts exceeded
                       │
                       ▼
                     FAILED → DEAD_LETTER
```

Postgres, not the queue, is the source of truth for this state machine. A
worker writes `RUNNING` before doing any work, so a crash mid-task is visible
and recoverable, never silently lost.

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
  (Phase 5).
- It does not claim an investigation agent's hypothesis is correct. Confidence
  is self-reported, exactly as it is for a `RiskSignal`, and is not validated
  against ground truth until Phase 5's evaluation framework exists.
- It does not claim this replaces a human analyst. The investigation result
  is evidence for one, not a replacement of one.
