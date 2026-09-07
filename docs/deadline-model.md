# Deadline model

Status: Phase 1. Apportionment, propagation and circuit breaking are
implemented; the gateway is the only origin and the sentinel's share is withheld
before any other stage is granted one. **Nothing has been measured.** The
allocations below remain an initial partition, not an observed distribution.

## 1. The target

> Approximately **80 ms end-to-end P99** for the synchronous risk decision path,
> measured at the gateway, under realistic concurrency.

This is a target and a **request deadline**. It is not a claim, and it is not a
per-component allowance. The number is currently unmeasured; see §7.

## 2. One budget, divided

The deadline is established once, at the gateway, and divided as the request
descends. Every stage receives a *share of what remains*, never a fresh 80 ms.

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
stage distributions in Phase 1, and the revision will be recorded here with the
numbers that justified it.

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

## 7. Current status

Not measured. The 80 ms figure is a configured default
(`config.DefaultRequestDeadline`) and a design target. The stage allocation in
§2 is an estimate, not an observation. If measurement shows the target is not
achievable with this architecture, this document will record the measurement
and the target will change — a measured failure is more useful than an invented
success.
