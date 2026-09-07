# Decision model

Status: Phase 1. The model is implemented: the value types are in
`crates/common`, the aggregation with it, and the sentinel applies it. The
weights and thresholds below are configured defaults awaiting Phase 5
measurement.

## 1. Decision authority

One component maps scores to outcomes: the Rust sentinel. This is the single
most important constraint in Legion, and it is enforced structurally rather
than by convention:

- The `Decision` enum lives in `legion.risk.v1` and is only ever *written* by
  `SentinelService`. No agent contract can express one — `AgentService` returns
  a `RiskSignal` or a `Failure`, and neither can carry a decision.
- The orchestrator receives a `DecisionOutcome` and forwards it. It has no
  thresholds and no weights.
- In Rust, `Decision` is only reachable through `Thresholds::classify`, which
  cannot be constructed without an ascending threshold pair.

The model, therefore, cannot decide. Neither can the orchestrator, the gateway,
or any single agent. See ADR-004.

## 2. Scoring

Every agent emits a score in the closed integer interval `[0, 100]` and a
confidence in `[0, 100]`.

Integers, not floats. Two runs of the same input must produce the same bits, on
different machines, in different languages, months apart, or the replay engine
proves nothing. Floating point weighted sums do not offer that guarantee across
compilers and evaluation orders.

Score `0` means "no elevated risk observed". It does not mean "no opinion". An
agent with no opinion returns a `Failure`; see [failure-model.md](failure-model.md).

## 3. Weighting

Signals are combined by a configured weight per agent, expressed in basis
points so the arithmetic stays integral.

Initial illustrative weights:

| Agent | Weight | Basis points |
|---|---|---|
| Velocity | 30 % | 3000 |
| Device | 25 % | 2500 |
| Geo | 15 % | 1500 |
| Behavioral | 30 % | 3000 |

These are a starting configuration, not a claim about correct fraud weighting.
They are configuration values that will be tuned against the evaluation
framework in Phase 5, and every published change to them will be accompanied by
a measured precision/recall comparison.

The behavioural signal carries the same weight as the largest deterministic
one, and no more. It is bounded evidence, not an oracle.

### Aggregation

```text
aggregate = Σ (score_i × weight_i) / Σ weight_i        over included signals
```

Three rules make this deterministic:

1. **Renormalisation.** When a signal is excluded, the remaining weights are
   renormalised over the included set. A missing agent must not silently reduce
   every score by its weight, because that would make an outage look like
   safety.
2. **Rounding.** Division rounds half-to-even at the final step only. Rounding
   mode is part of the contract, not an implementation detail.
3. **Ordering.** Contributions are summed in a fixed agent order, independent
   of the order responses arrived in.

Every included and excluded signal appears in `DecisionOutcome.contributions`
with its score, weight and weighted contribution, so the arithmetic of any
decision can be checked by hand.

### Inclusion rules

A signal is included when the agent returned a signal, the agent's
`signal_enabled` flag is set in its manifest, and its confidence meets the
policy's minimum. Otherwise it is excluded with a `ReasonCode` recorded in
`SignalContribution.exclusion_reason`.

Policy defines a minimum included weight — the share of total configured weight
that must be present for the normal policy to apply. Below it, the fallback
policy decides.

## 4. Thresholds

```text
0  – 39   ALLOW
40 – 69   REVIEW
70 – 100  DECLINE
```

Bands are half-open at the top: `score >= decline_at` is `DECLINE`,
`review_at <= score < decline_at` is `REVIEW`, `score < review_at` is `ALLOW`.
The boundary convention is tested in `crates/common`.

The thresholds actually in force are recorded on the outcome
(`PolicyThresholds`), rather than being assumed by whoever reads it later.

These are initial configurable policy values. They are not a claim about
universally correct fraud thresholds, and the false-positive cost of the
`REVIEW` band is a Phase 5 measurement, not an assertion.

## 5. Degradation

`DecisionOutcome.degradation` states how complete the evaluation was:

| State | Meaning |
|---|---|
| `NONE` | All configured agents contributed; all features fresh. |
| `PARTIAL` | At least one signal missing or degraded; the policy permitted deciding on the rest. |
| `FALLBACK` | Too little evidence for the normal policy; the fallback policy decided. |

Degradation is an operational fact carried on the decision, not a footnote in a
log. A caller can therefore treat a `PARTIAL` allow differently from a clean
one if it chooses.

## 6. Reason codes

Every non-trivial decision carries machine-readable reason codes, defined in
`legion/risk/v1/reason_code.proto` and grouped by origin:

| Range | Origin |
|---|---|
| 100–199 | Velocity |
| 200–299 | Device |
| 300–399 | Geography |
| 400–499 | Amount and cross-account patterns |
| 500–599 | Behavioural (model-derived) |
| 600–699 | Authentication |
| 900–999 | Platform state and degradation |

The 900 range is structurally different from the rest: those codes never come
from a risk assessment. They record that the platform itself was not operating
normally — `MODEL_UNAVAILABLE`, `FEATURE_STORE_DEGRADED`,
`FALLBACK_POLICY_USED`, `DEADLINE_BUDGET_EXHAUSTED`. Separating them means an
analyst can always tell "this looked like fraud" from "we could not see
properly".

Codes are append-only. Numbers are never reused and meanings are never
redefined; a changed meaning requires a new code, because historical decisions
and evaluation datasets are interpreted through these numbers.

Natural-language text produced by a model is never the authoritative
explanation of a financial decision. It may be recorded as evidence; it is not
the reason.

## 7. Lineage and reproducibility

Every evaluation emits a `DecisionLineage` containing the decision, every agent
evaluation (successful or not), the timing of each stage against its budget,
observed failures, and `GovernedVersions` — the pinned versions of the agents,
policy, feature catalogue, model, prompt, output schema and contract set.

The reproducibility claim Legion makes is precise:

> Given the same input, the same feature values, and the same `GovernedVersions`,
> the deterministic portion of the pipeline produces an identical
> `DecisionOutcome`.

The behavioural signal is not claimed to be bit-reproducible; sampled model
output is not deterministic in general. What is reproducible is everything
downstream of it: given the same validated behavioural signal, the decision is
identical. Replay therefore distinguishes *deterministic replay* (agents
replayed from recorded signals) from *full replay* (the model re-invoked), and
reports them separately. See ADR-013.

Lineage deliberately excludes the transaction payload. It carries pseudonymous
identifiers and version pins; the payload is recoverable from the authorised
replay dataset, which has its own access controls.

## 8. Shadow mode

A request marked `shadow` is processed identically and recorded identically,
but the caller does not treat the result as authoritative. Shadow lineage is
tagged so that shadow decisions can never be mixed into production metrics by
accident. This is how a policy, model, prompt or weight change is measured
before it can decline anyone. See ADR-012.

## 9. What this model does not claim

- It does not claim the weights or thresholds are correct. They are configured
  defaults awaiting measurement.
- It does not claim that a deterministic decision is a *good* decision. It
  claims the decision is reproducible, explainable and attributable.
- It does not claim regulatory adequacy. Determinism, lineage and versioning
  are technical controls that support an audit; they are not compliance.
