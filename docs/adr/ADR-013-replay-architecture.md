# ADR-013: Replay architecture

**Status:** Accepted
**Date:** 2026-09-04
**Phase:** 5

## Context

"Auditable AI" is easy to claim and hard to demonstrate. The demonstrable
version is: take a recorded dataset, run it through a named version of the
system, and get the same decisions every time — then run it through the next
version and show exactly what changed and why.

This requires that a decision be a function of recorded inputs and pinned
versions, with nothing else influencing it. That property has to be designed in;
it cannot be added later to a system whose decision path reads global state.

## Decision

Legion provides a replay engine:

```text
dataset ──► replay ──► Legion version N   ──► decisions ─┐
                                                          ├─► diff
dataset ──► replay ──► Legion version N+1 ──► decisions ─┘
```

The design constraints that make this possible are already in place:

- The sentinel performs no I/O. It is a pure function of `DecideRequest`
  (ADR-002, ADR-006).
- Scores are integers, and rounding mode and summation order are part of the
  contract (decision model §3).
- `GovernedVersions` pins agents, policy, feature catalogue, model, prompt,
  output schema and contract set on every decision.
- Features are replayed from recorded values, not recomputed, so a change to
  the feature store cannot silently change a historical replay.

Two replay modes, reported separately:

| Mode | Behavioural signal | Guarantee |
|---|---|---|
| **Deterministic replay** | Replayed from the recorded signal | Bit-identical `DecisionOutcome` for identical inputs and versions |
| **Full replay** | Model re-invoked | Not bit-identical; used to evaluate model or prompt changes |

Conflating these would be dishonest, because only the first is reproducible.

Replay supports comparison across policy changes, model changes, prompt changes,
feature-definition changes and agent changes. A diff reports decision changes,
score deltas, reason code changes, and the direction of each change against
labels where labels exist.

## Alternatives considered

**Log-based reconstruction.** Reconstruct decisions from operational logs.
Rejected: logs deliberately exclude the data needed, and adding it would turn
the log stream into a second uncontrolled copy of sensitive data.

**Event sourcing the whole platform.** Would give replay for free. Rejected as
disproportionate: full event sourcing is a large architectural commitment, and
recording the decision inputs achieves the goal for the decision path
specifically.

**Re-running against live state.** Simplest. Rejected: state has moved on, so
the result is neither reproducible nor comparable. This is the failure mode most
"we can replay" claims actually have.

**Storing full transaction payloads for replay.** Highest fidelity. Rejected as
the default: it is a large, sensitive dataset. The replay dataset holds
pseudonymous identifiers and recorded feature values under separate access
control, which is enough to reproduce a decision.

## Trade-offs

- **Storage.** Every decision's inputs, signals and versions are retained. That
  is a real cost and a real data-protection consideration, and it is why the
  dataset is pseudonymous and access-controlled.
- **Rigidity.** Determinism constrains implementation: no wall-clock reads on
  the decision path, no map iteration order dependence, no floating-point
  accumulation. These constraints are permanent.
- **Partial guarantee.** Full replay is not reproducible, and no amount of
  engineering makes sampled model output deterministic. The honest response is
  to report the two modes separately.
- **Version compatibility.** Replaying old data against new code requires
  contract compatibility, which is what `buf breaking` in CI protects.

## Consequences

- Determinism is a tested property, not an aspiration: replaying the same
  dataset twice must produce identical output, and this becomes a CI check.
- Feature values are recorded, not recomputed, during replay.
- Replay is the evidence behind any claim about the effect of a change, and
  pairs with shadow mode (ADR-012): replay compares versions on historical data,
  shadow compares them on live traffic.
- The reproducibility claim is stated precisely and narrowly, because a claim
  broader than the guarantee is worse than no claim.

## Revisit if

Storage cost of the replay dataset becomes prohibitive, in which case sampling
or retention windows are introduced — and the sampling is recorded so that
replay results are not mistaken for full coverage.
