# ADR-012: Shadow mode as the change mechanism

**Status:** Accepted
**Date:** 2026-09-04
**Phase:** 5

## Context

Every meaningful change to Legion — a policy weight, a threshold, a rule, a
model version, a prompt, a feature definition — changes decisions about real
money. Offline evaluation on a fixed dataset is necessary but not sufficient: it
cannot show how a change behaves on live traffic distribution, at live latency,
under live degradation.

The alternative usually chosen is a percentage rollout, which means some
customers receive the untested behaviour.

## Decision

Legion supports shadow mode: a request is evaluated fully, recorded fully, and
its result is not authoritative for the caller.

```text
Production decision (authoritative)
        │
        ├── existing policy / current version
        │
        └── Legion shadow evaluation ── recorded, compared, not acted upon
```

Properties:

- A shadow request follows the identical code path. It is not a simulation.
- `EvaluationOptions.shadow` marks the request; `DecisionLineage.shadow` marks
  the record, so shadow decisions can never be mixed into production metrics by
  accident.
- Shadow and production evaluations are compared on decision agreement, risk
  score distribution, reason code distribution, latency distribution, false
  positives, false negatives and model version.
- Shadow mode is also the integration path: Legion can run alongside an existing
  fraud system, evaluating the same traffic without affecting it.

No policy, model, prompt or weight change becomes authoritative without a shadow
comparison.

## Alternatives considered

**Offline evaluation only.** Cheaper, fully reproducible, and already planned
(Phase 5). Insufficient alone: it cannot show latency under live concurrency, or
how often a change lands in the degraded paths that only occur in production.

**Canary / percentage rollout.** Standard, and appropriate for many systems.
Rejected as the *primary* mechanism because the blast radius is real customers
being wrongly declined. Shadow mode has zero customer impact and, for a
decisioning system, gives a comparison over the full traffic distribution rather
than a slice.

**A/B testing on live decisions.** Statistically strong. Rejected for the same
reason, plus it is difficult to justify deliberately applying a less-trusted
policy to a randomly chosen customer's payment.

## Trade-offs

- **Cost.** Shadow evaluation roughly doubles compute for shadowed traffic,
  including inference, which is the expensive part.
- **Capacity interaction.** Shadow traffic must never consume the budget or the
  capacity of authoritative traffic. It runs with lower priority and is shed
  first under load.
- **No feedback loop.** A shadow `DECLINE` never happens, so the outcome of the
  transaction it would have blocked is unknown. Comparison is against the
  production system's decisions and against labelled data, not against
  counterfactual ground truth. This is a genuine limitation, not a detail.
- **Storage.** Shadow lineage doubles the lineage volume for shadowed traffic.

## Consequences

- Every risk-affecting change has a documented shadow comparison before it
  becomes authoritative.
- Shadow and production metrics are separated at the source, by a field, rather
  than by a query convention that someone will eventually forget.
- Shadow mode plus replay (ADR-013) covers both directions: replay compares
  versions on historical data, shadow compares them on live traffic.

## Revisit if

The compute cost of shadowing inference proves prohibitive, in which case
shadowing is sampled rather than abandoned — and the sampling rate is recorded
with every comparison.
