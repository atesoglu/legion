# Deadline model

Status: Phase 1. Apportionment, propagation and circuit breaking are
implemented; the gateway is the only origin and the sentinel's share is withheld
before any other stage is granted one. **Nothing has been measured under load.**
§7 records a first local measurement of an idle, single-threaded run — a floor,
not a validation — and §8 records the storage defect that reading it exposed.
The allocations below remain an initial partition, not an observed distribution.

## 1. The target

> Approximately **80 ms end-to-end P99** for the synchronous risk decision path,
> measured at the gateway, under realistic concurrency.

This is a target and a **request deadline**. It is not a claim, and it is not a
per-component allowance. It has never been measured under realistic
concurrency, which is the only condition the target is stated for; see §7.

## 2. One budget, divided

The deadline is established once, at the gateway, and divided as the request
descends. Every stage receives a *share of what remains*, never a fresh 80 ms.

Investigation (Zone 6, ADR-017) is not a stage of this budget and never will
be. It starts, if it starts at all, only after the sentinel has already
answered and the caller has already received a response; it is asynchronous
by construction, not merely by current implementation status. Nothing in this
document changes, and nothing added for Zone 6 may add a synchronous call
into it — see `investigation-model.md` §2 for why that boundary is treated as
non-negotiable rather than an optimisation to revisit.

```text
Client
  │  deadline = min(caller_requested, server_max = 80 ms)
  ▼
Gateway ─────────── 3 ms   authn, validation, transport
  │
  ▼
Orchestrator ────── 4 ms   budgeting, assembly, response
  ├── Feature fetch ────── 12 ms
  ├── Deterministic agents  8 ms   (parallel: velocity, device, geo)
  ├── Behavioral agent ─── 35 ms   (prompt, inference, validation)
  └── Sentinel ─────────── 3 ms
  │
  ▼
Reserve ─────────── 15 ms  absorbs scheduling, GC, network variance, retries
```

The reserve is deliberate. A budget that sums exactly to the deadline is a
budget that misses the deadline whenever anything is slightly slow, which under
load is constantly. Roughly 19 % of the budget is unallocated and belongs to the
tail.

These allocations are an initial partition. They will be revised from measured
stage distributions, and the revision will be recorded here with the numbers
that justified it. §7 is the first such measurement and does not justify a
revision: no stage exceeded its allocation, but nothing has yet run under the
load that would make the partition matter.

## 3. Propagation rules

1. **The gateway is the only origin.** It computes
   `deadline = min(caller_deadline, configured_maximum)`. A caller may ask for
   less time; it can never ask for more.
2. **Every downstream call carries the remaining budget**, as a gRPC deadline
   on the transport and as `budget` in the request message. The transport
   deadline is authoritative; the message field exists so an agent can shape
   its own work — for example, returning a partial signal rather than
   overrunning.
3. **Elapsed time is subtracted, not reset.** A stage that took 20 ms leaves
   20 ms less for everything after it. If the remaining budget falls below a
   stage's minimum viable time, the stage is skipped and
   `REASON_CODE_DEADLINE_BUDGET_EXHAUSTED` is recorded.
4. **Cancellation propagates.** When the client disconnects or the deadline
   expires, `context` cancellation reaches every in-flight call. Ignoring
   cancellation is a defect, not an optimisation.
5. **Nothing returns late.** If the deadline expires, the gateway returns
   `DEADLINE_EXCEEDED`. It does not return a decision the caller has stopped
   waiting for and might still act upon.
6. **The sentinel always runs.** Its budget is protected: it is the only
   component that can produce a decision at all, and it is CPU-bound and small.
   If the behavioural agent consumes its own budget and overruns, it is
   abandoned, not accommodated. This is also why the sentinel does not share a
   process with components that are permitted to fail (ADR-006).

## 4. Parallelism

The three deterministic agents run concurrently and share one budget window;
the window closes when the slowest finishes or the window expires. Agents that
have not answered are excluded with `REASON_CODE_AGENT_TIMEOUT`.

The behavioural agent is invoked concurrently with the deterministic ones where
its inputs allow, precisely because it is the largest and least predictable
consumer. Its 35 ms window is not additive with the 8 ms deterministic window.

## 5. Deadlines are not circuit breakers

These answer different questions and are implemented separately.

| | Deadline | Circuit breaker |
|---|---|---|
| Question | How long may *this request* take? | Should we send traffic to this dependency *at all*? |
| Scope | One request | One dependency, across requests |
| State | None | Closed → Open → Half-Open → Closed |
| Effect on breach | This request degrades or fails | Subsequent calls fail immediately without being attempted |
| Reason code | `AGENT_TIMEOUT`, `MODEL_TIMEOUT`, `DEADLINE_BUDGET_EXHAUSTED` | `FAILURE_KIND_CIRCUIT_OPEN` |

A circuit breaker is not a substitute for a deadline: an open breaker does not
help a request that is already slow for another reason. A deadline is not a
substitute for a breaker: timing out on every request against a dead dependency
spends the entire budget discovering something already known.

```text
        failure ratio over window exceeded
Closed ──────────────────────────────────► Open
   ▲                                         │
   │                                         │ cooldown elapsed
   │  probe succeeded                        ▼
   └──────────────────────── Half-Open ◄─────┘
                                  │ probe failed
                                  └──────────► Open
```

Breaker state is per dependency, and opening one is a degraded mode with an
explicit reason code — never a silent one.

## 6. What must be measured

No latency claim will be made until these are published, per stage and
end-to-end, under realistic concurrency:

- P50, P95, P99, P99.9
- Throughput (TPS) at the concurrency the percentiles were measured at
- Error rate
- Timeout rate
- Fallback rate
- Budget-exhaustion rate per stage

Averages will not be reported alone. A mean latency figure hides exactly the
behaviour an 80 ms P99 target is about.

ADR-020 decides where these come from: histograms exported by OTLP to a
collector and stored in Prometheus, per stage and end-to-end. The histograms
now exist — `legion.decision.duration` at the gateway, which is where the
target is stated for, plus `legion.evaluation.duration`,
`legion.evaluation.stage.duration` and `legion.agent.duration` in the
orchestrator — with 0.08 seconds as an explicit bucket boundary, so "what
fraction finished inside the budget" is a bucket count rather than an
interpolation across one. **Nothing collects them**, so none of the figures
above can be reported yet.

The stage numbers also have a second, older source —
`DecisionLineage.ExecutionSpan` has recorded the elapsed time and the budget
granted for `feature_fetch`, `deterministic_agents` and `sentinel` on every
decision since Phase 1, unsampled and durably. §7 is the first reading of it,
and §8 is what that reading exposed about how it is stored. The two are
complementary rather than redundant: lineage answers "how long did decision X
take", the histograms answer "what does the distribution look like", and
neither answers the other's question without a scan.

## 7. Current status

**Partially measured, for the first time, and only in a local environment.**

The 80 ms figure remains a configured default (`config.DefaultRequestDeadline`)
and a design target. What follows is not a validation of it. It was obtained
by driving 60 sequential transactions through the real binaries in the `e2e`
harness and reading the lineage those decisions recorded — the first time
anything has read `ExecutionSpan` back.

Observed, on one developer machine, no concurrency:

| Measured at | p50 | p95 | max |
|---|---|---|---|
| Gateway round trip (client-observed) | 3.56 ms | 4.91 ms | 5.46 ms |
| Orchestrator total (`total_elapsed`) | 1.62 ms | 2.26 ms | 4.26 ms |
| `feature_fetch` (budget 12 ms) | ~0 | 0.54 ms | 1.79 ms |
| `deterministic_agents` (budget 8 ms) | 0.55 ms | 1.10 ms | 1.12 ms |
| `sentinel` (budget 3 ms) | 0.55 ms | 1.12 ms | 1.58 ms |

What this does and does not support:

- **No stage exceeded its allocation**, and none came close: each used under
  15 % of its budget. The §2 partition is not refuted. It is also not
  confirmed, because nothing here stressed it.
- **The environment is not production.** The feature store is an in-process
  `miniredis` rather than a real Redis over a network, there is no
  concurrency, no contention, no GC pressure worth the name, and payloads are
  fixtures. Every number above is a floor.
- **Roughly 55 % of the request is spent outside the orchestrator.** The
  gateway round trip is 3.56 ms at p50 while the orchestrator accounts for
  1.62 ms of it. Authentication, validation, the dedup store round trip and
  two gRPC hops cost more than the entire risk pipeline. That is the opposite
  of where attention has gone so far, and it is the strongest argument these
  numbers make.
- **The first request cost 10.25 ms**, about three times steady state,
  because gRPC dials lazily across four hops. Real, and invisible to any
  percentile taken over a warm process.
- **The measurement is near this machine's clock resolution.** `feature_fetch`
  has a p50 of zero with a p95 of 0.54 ms, which is a quantisation artefact
  rather than a free lookup. Stage timings in the hundreds of microseconds
  are at the edge of what `time.Since` resolves here.

If measurement under load shows the target is not achievable with this
architecture, this document will record that and the target will change — a
measured failure is more useful than an invented success. That has not
happened; nothing has been run under load.

## 8. The recorded timings are stored at the wrong resolution

Reading `ExecutionSpan` back exposed a defect in how it is persisted.

`DecisionLineage` carries each span as a protobuf `Duration`, so the
authoritative `decisions.raw_lineage` blob holds nanoseconds and every number
in §7 came from it. ADR-018's normalised projection, however, stores
`execution_spans.elapsed_ms` and `decisions.total_elapsed_ms` as integer
milliseconds. Across the 61 decisions above, every stage's `elapsed_ms` column
held exactly **two distinct values** — 0 and 1 — and `total_elapsed_ms` held
three.

The queryable projection therefore cannot answer the question §6 requires of
it. No percentile, distribution or budget-utilisation figure can be computed
from a column whose entire observed range is `{0, 1}`, which is why §7 is
sourced from the blob instead.

This matters beyond convenience. ADR-020 samples decision-path traces
specifically on the grounds that lineage already records stage timings
unsampled and durably. That argument holds only if the unsampled record can
express the timings, and today the queryable half of it cannot.

The fix is a unit change and a migration, not a redesign: store nanoseconds,
which is exactly what the source `Duration` carries and what `BIGINT` holds
without strain. Migration `0002` does that for all six duration columns —
`deadline_ns`, `total_elapsed_ns`, `observed_latency_ns`, `elapsed_ns`,
`budget_ns` and the failure `elapsed_ns` — rather than only the two that
provoked it, because utilisation is elapsed over budget and a schema mixing
units in one ratio invites the arithmetic mistake it would then be used to
investigate.

**One thing is deliberately unresolved.** ADR-018's Decision section sketches
these tables by column name, and those names still read `_ms`. That ADR has
been implemented, and `docs/adr/README.md` requires a superseding ADR rather
than an edit once a decision is built. Whether this counts as superseding a
decision — the ADR did spell the unit into the column names — or as fixing an
implementation that defeated the ADR's own stated intent of making lineage
queryable is a judgement call, and it is recorded here rather than settled
quietly in either direction.
