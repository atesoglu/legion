# Failure model

Status: Phase 1 (real-time decision path) / Phase 2 (investigation plane,
partially built — see §3's "Investigation plane (Zone 6)" below). The
degraded paths on the decision path are implemented — an agent that fails is
excluded and the remaining weights renormalised, a store that cannot answer
leaves every feature explicitly unavailable, and an exhausted budget skips
stages rather than starving the decision. Systematic failure injection, which is
what turns these from claims into tested behaviour, is Phase 6.

## 1. Principle

A risk system's behaviour when it is broken is part of its design, not an
accident of exception handling. Every dependency failure in Legion maps to a
defined outcome, an explicit degradation state, and a reason code that reaches
the caller.

Two failure modes are unacceptable and are treated as defects:

- **Silent degradation.** Deciding as if evidence were present when it was not.
  A missing velocity feature must never be read as zero.
- **Total failure on partial loss.** Refusing to decide because one of four
  agents is unavailable, when the policy has enough evidence to decide.

## 2. Failure classification

`legion.common.v1.FailureKind` is the closed vocabulary:

| Kind | Meaning |
|---|---|
| `DEADLINE_EXCEEDED` | The stage budget expired before a result arrived. |
| `CIRCUIT_OPEN` | The breaker for this dependency is open; no call attempted. |
| `DEPENDENCY_ERROR` | Reachable, returned an error. |
| `DEPENDENCY_UNAVAILABLE` | Not reachable. |
| `INVALID_RESPONSE` | Received but failed schema or range validation. |
| `INVALID_REQUEST` | The request was malformed or semantically invalid. |
| `CAPABILITY_DENIED` | A required capability was not granted. |
| `OVERLOADED` | Deliberate load shedding. |
| `INTERNAL` | Unexpected. Always alertable, should be rare. |

## 3. Defined behaviours

### Feature store unavailable or slow

| | |
|---|---|
| Detection | Timeout on the feature-fetch budget, connection error, or open breaker. |
| Response | Serve from the local cache where the value is within the policy's staleness window; mark those features `STALE`. Features with no usable cached value are marked `UNAVAILABLE`, not zero. |
| Degradation | `PARTIAL` if agents can still produce meaningful signals; `FALLBACK` if the policy's minimum included weight is not reachable. |
| Reason code | `FEATURE_STORE_DEGRADED` |
| Agent obligation | Every agent must branch on `FeatureFreshness`. An agent whose rules depend entirely on unavailable features returns a `Failure`, not a zero score. |

### SLM unavailable

| | |
|---|---|
| Detection | Connection failure, or open breaker on the inference runtime. |
| Response | Proceed with the deterministic agents. Renormalise weights over the remaining 70 %. |
| Degradation | `PARTIAL` |
| Reason code | `MODEL_UNAVAILABLE` |
| Rationale | The deterministic pipeline is the product. The system must be useful without AI; the model is an enhancement, and an enhancement that can take down the platform is a liability. |

### SLM slow

| | |
|---|---|
| Detection | The behavioural agent's budget window expires. |
| Response | Abandon the inference call, cancel it, and decide on what is available. Inference never borrows from the sentinel's budget. |
| Degradation | `PARTIAL` |
| Reason code | `MODEL_TIMEOUT` |

### Malformed SLM response

| | |
|---|---|
| Detection | JSON Schema validation failure, score out of `[0, 100]`, unknown reason code, or reason codes outside the behavioural range. |
| Response | Reject the response entirely. Do not repair it, do not parse it partially, do not infer intent from prose. Retry once only if the remaining budget allows. |
| Degradation | `PARTIAL` |
| Reason code | `MODEL_OUTPUT_INVALID`, plus `PROMPT_INJECTION_SUSPECTED` when the output contains instruction-shaped content or attempts to assert a decision. |
| Rationale | A model that returns something unparseable is a model whose output cannot be trusted for *this* request. Salvaging it is how injected content enters the pipeline. |

### Deterministic agent unavailable, slow, or crashed

| | |
|---|---|
| Detection | Timeout, transport error, or process restart. |
| Response | Exclude the signal and renormalise. Continue only if the policy's minimum included weight is still met. |
| Degradation | `PARTIAL`, or `FALLBACK` below the threshold. |
| Reason code | `AGENT_UNAVAILABLE`, `AGENT_TIMEOUT` |
| Note | Each engine is its own process (ADR-006), so a fault takes one signal rather than all three. Losing every deterministic engine at once means an infrastructure failure, not a software one, and falls back when the minimum included weight is no longer met. |

### Capability denied

| | |
|---|---|
| Detection | The capability runtime returns `CapabilityDenied`. |
| Response | The agent's signal is excluded. The denial is audited as a security event, not merely a failure. |
| Degradation | `PARTIAL` |
| Reason code | `CAPABILITY_DENIED` |
| Note | A capability denial in steady state means an agent is asking for something it was never granted. That is either a misconfiguration or a compromise, and it is alerted on. |

### Request deadline exhausted mid-flight

| | |
|---|---|
| Detection | Remaining budget falls below a stage's minimum viable time. |
| Response | Skip the stage. If the sentinel cannot run, return `DEADLINE_EXCEEDED` to the caller — no decision at all is preferable to a decision the caller will not receive in time. |
| Reason code | `DEADLINE_BUDGET_EXHAUSTED` |

### Invalid transaction

| | |
|---|---|
| Detection | Gateway validation. |
| Response | Reject with `INVALID_ARGUMENT`. No evaluation is started, no lineage beyond the rejection is emitted, no resources are consumed downstream. |
| Reason code | None — this is not a risk outcome. |

### Investigation plane (Zone 6) — partially built

Zone 6 (ADR-017) is partially built: the controller, generic worker, task
queue and Postgres schema all run, proven end to end by `test/e2e`. The
table below records each documented failure mode's actual status, not just
its intent — some are real, tested behaviour; one is a known, currently
incorrect gap found during a documentation review, not fixed yet:

| Failure | Response | Status |
|---|---|---|
| Worker crash mid-task | Lease expires; task is claimable by another worker via `XAUTOCLAIM`; `attempt` increments (`investigation-model.md` §4). | **Implemented**, unit-tested (`investigation/internal/queue`). |
| Task queue redelivery of a completed task | The task handler is idempotent; a duplicate delivery must not duplicate evidence, findings, or a case. | **Implemented**: `investigation/worker` checks `store.IsTerminal` before doing any work and no-ops on a task already finished. |
| Investigation Redis instance unavailable or slow | New tasks cannot be dispatched; already-`RUNNING` tasks continue independently since Postgres, not Redis, holds their state. Case/investigation status remains queryable throughout. | **Plausible, not exercised.** No failure-injection test forces this; the reasoning follows from the code but is unverified. |
| Database unavailable during case/evidence/finding write | Do not acknowledge the queue message; the task remains claimable once the database recovers. | **Not implemented — currently incorrect.** `investigation/worker`'s `runLoop` acknowledges every message unconditionally after `handleTask` returns, regardless of whether the work inside it succeeded. A Postgres outage during a claim or write is logged and the task is left in whatever state it reached, but the message is still acked, so it is never redelivered: the task is silently dropped, not retried. Found during a documentation accuracy pass (2026-09-18); not yet fixed. |
| Inference timeout or malformed response inside an investigation | Same posture as the real-time path's `MODEL_TIMEOUT`/`MODEL_OUTPUT_INVALID` (above), scoped to one task rather than one decision: the task fails or retries: the case is not blocked, since other tasks in the same investigation are independent. | **Not applicable yet.** No investigation agent calls a real model; this failure mode has nothing to happen to. |
| Malformed or duplicate task message | Rejected by the worker without executing any tool or model call; recorded as an `InvestigationAuditEvent`. | **Partially implemented.** A malformed envelope is dropped (logged, not audited as an `InvestigationAuditEvent` — no such event type is written for this case). A duplicate delivery is handled, per the row above. |

Crucially, **none of the above can affect a `DecisionOutcome`.** Zone 6 fails
after the decision that created the case in question has already been made
and returned; a failure here degrades investigation quality or timeliness,
never a financial decision.

## 4. Fallback policy

When the minimum included weight is not met, the fallback policy decides. It is
a separate, deliberately conservative configuration, and its use is always
visible:

- `DecisionOutcome.degradation = FALLBACK`
- `PolicyRef.fallback = true`
- `REASON_CODE_FALLBACK_POLICY_USED`

The fallback policy is not "allow everything" and not "decline everything".
Both are failure modes with a business cost: the first is an open door during
an outage, the second is an outage that becomes a customer incident. The
intended shape is amount- and channel-banded — small, authenticated,
card-present transactions allow; large, unauthenticated, cross-border ones
review — with the precise bands set as configuration and evaluated in Phase 5.

Fallback rate is a first-class metric. A system frequently in fallback is a
system whose measured detection quality does not apply.

## 5. What is not a failure

- A score of 0 is a successful evaluation.
- `FeatureFreshness.ABSENT` is a successful lookup of a value that does not
  exist.
- `DECISION_DECLINE` is a successful decision.
- A `REVIEW` outcome is not a hedge; it is an outcome with an operational cost
  that Phase 5 measures.

## 6. Verification

None of the above is believed until it is injected. Phase 6 exercises each row
of §3 under load and asserts, for each: the decision produced, the degradation
state, the reason codes, the observed latency, the breaker transitions and the
emitted lineage. A documented failure mode without a test is an assumption —
the investigation-plane table above included, once Zone 6 exists to test.
