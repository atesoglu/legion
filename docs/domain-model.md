# Domain model

Status: Phase 0. This document fixes the vocabulary. Where a word is used
loosely in the payments industry, Legion picks one meaning and uses only that.

## 1. The core abstraction

Legion's model has four layers, in one direction:

```text
Risk Subject  ──►  Evidence  ──►  Signals  ──►  Decision
```

- A **risk subject** is the thing being assessed.
- **Evidence** is what is known about it.
- **Signals** are bounded opinions derived from evidence.
- A **decision** is the single authoritative outcome.

In the payment domain the risk subject is a **transaction**. The abstraction is
stated in these terms because the same shape later admits an insurance claim as
a risk subject, with documents and prior claims as evidence. That extension is
explicitly out of scope until the payment system is good; see
[project-plan.md](project-plan.md) §38. Nothing has been abstracted in advance
to accommodate it.

## 2. Glossary

| Term | Meaning in Legion |
|---|---|
| **Risk subject** | The entity an evaluation is about. Today: a `Transaction`. |
| **Transaction** | One payment authorisation request. The unit of decision. |
| **Account** | The party whose risk is assessed. Identified pseudonymously. |
| **Instrument** | The tokenised payment instrument. Legion never sees a PAN. |
| **Device** | The endpoint that initiated the transaction. |
| **Location** | A coarse geographic position with an explicit accuracy radius and an explicit source. |
| **Merchant** | The acceptor of the transaction. |
| **Evidence** | Any input to a signal: the subject itself, features, or model-derived observations. |
| **Feature** | A named, windowed, versioned aggregate derived from history. |
| **Feature window** | The rolling interval a feature covers, from a closed set. |
| **Freshness** | Whether a feature value is fresh, stale, absent or unavailable. These are four different things. |
| **Agent** | A narrow component that turns evidence into exactly one signal. |
| **Signal** | A bounded opinion: a score in `[0, 100]`, a confidence, reason codes and evidence. |
| **Reason code** | A stable machine-readable justification. The primary explanation channel. |
| **Agent evaluation** | The envelope around one agent invocation: either a signal or a failure. |
| **Policy** | The versioned configuration mapping signals to a decision: weights, thresholds, inclusion rules, fallback. |
| **Decision** | `ALLOW`, `REVIEW` or `DECLINE`. Produced only by the sentinel. |
| **Degradation state** | How complete the evaluation was: none, partial, or fallback. |
| **Decision lineage** | The record of everything that determined a decision. |
| **Capability** | A single permitted action an agent may perform. |
| **Shadow mode** | Evaluating without the result being authoritative for the caller. |
| **Replay** | Re-running a recorded dataset against a specific platform version. |

## 3. Distinctions that matter

Several pairs of concepts are routinely conflated. Legion keeps them apart, in
the type system where possible.

**Absent vs unavailable.** A feature that is `ABSENT` genuinely has no value —
a first-ever transaction on a new account has no 24-hour history. A feature
that is `UNAVAILABLE` has an unknown value because the store did not answer.
Treating the second as zero is how velocity rules stop firing during an outage,
silently, exactly when fraud is easiest. `FeatureFreshness` forces every
consumer to distinguish them.

**Score vs decision.** A score is a bounded opinion; a decision is an
authoritative outcome. Rust's type system carries this: `Score` cannot be
constructed out of range, and `Decision` is only reachable through
`Thresholds::classify`, which requires a policy.

**Signal absence vs zero risk.** An agent that observed nothing suspicious
returns a score of 0. An agent that could not run returns a `Failure`. These
produce different degradation states and different reason codes, and the second
may change the decision. An agent must never express failure as a zero score.

**Confidence vs score.** Score is how risky. Confidence is how much the agent
trusts its own score. Policy may use low confidence to down-weight or exclude a
signal. Confidence never raises a score.

**Trusted vs untrusted text.** Any string originating from a payer, a merchant
or a third-party enrichment service is wrapped in `UntrustedText`. It is never
interpolated into a prompt as instructions and never used to build a query. The
wrapper exists so that no consumer can handle it without noticing what it is;
see [threat-model.md](threat-model.md) T-05.

**Occurred vs observed.** `Transaction.occurred_at` is when the transaction
happened in the source system. Velocity windows are computed against it, not
against Legion's clock, so that a queueing delay upstream cannot erase a burst.

## 4. Identity and pseudonymisation

Legion receives no primary account numbers and no raw device fingerprints.
Persistent identifiers arrive, or are converted on ingest, as
`PseudonymousId`: an HMAC-SHA-256 of the source identifier under a rotating
platform key, namespaced by `IdentifierDomain`.

Three consequences are deliberate:

- **Keyed, not plain.** An unsalted SHA-256 of a card token or an IBAN is
  reversible by enumeration; the input domain is small. A keyed construction is
  not.
- **Namespaced.** The domain is part of the HMAC input, so the same raw string
  in two domains produces two different pseudonyms and cannot be cross-joined
  by accident.
- **Versioned.** `key_version` makes rotation possible without losing the
  ability to interpret historical pseudonyms, and makes deliberate invalidation
  possible.

Pseudonymisation reduces exposure. It does not make the data non-personal and
does not by itself discharge any data protection obligation. See
[security-boundaries.md](security-boundaries.md) §5.

## 5. Agents

Four agents are defined. Each produces exactly one signal.

| Agent | Kind | Detects | Principal reason codes |
|---|---|---|---|
| **Velocity** | Rust, deterministic | Transaction and amount frequency, failed-attempt bursts, card testing, low-and-slow accumulation | `VELOCITY_SPIKE`, `AMOUNT_VELOCITY_SPIKE`, `FAILED_ATTEMPT_VELOCITY`, `CARD_TESTING_PATTERN`, `LOW_AND_SLOW_PATTERN` |
| **Device** | Rust, deterministic | Device novelty, churn, sharing across accounts, reported integrity compromise | `NEW_DEVICE`, `DEVICE_ACCOUNT_MISMATCH`, `DEVICE_SHARED_ACROSS_ACCOUNTS`, `DEVICE_INTEGRITY_COMPROMISED`, `DEVICE_CHURN` |
| **Geo** | Rust, deterministic | Impossible travel, unusual location, mismatch against country of record | `IMPOSSIBLE_TRAVEL`, `UNUSUAL_LOCATION`, `LOCATION_ACCOUNT_MISMATCH`, `LOCATION_PRECISION_INSUFFICIENT` |
| **Behavioral** | SLM-backed | Sequence and pattern anomalies over supplied evidence only | `BEHAVIORAL_ANOMALY`, `BEHAVIORAL_SEQUENCE_ANOMALY` |

All four implement the same `AgentService` contract. The orchestrator invokes
them identically. The behavioural agent has no privileged path: its signal is
one weighted input like any other, and it holds fewer capabilities than the
deterministic agents need, not more.

An agent is *narrow* by construction. It sees one subject, holds a small set of
capabilities, and cannot enumerate, join across subjects, or persist state
between evaluations.

### Adding an agent

Four agents are defined; more are expected. Two reason-code bands — `400`
amount-and-pattern and `600` authentication — already carry codes that no agent
yet produces.

The agent set is configuration, not code (ADR-014), so introducing one touches
the orchestrator not at all. The sequence separates the cheap part from the part
that changes decisions:

| # | Step | Changes decisions? |
|---|---|---|
| 1 | Allocate a reason-code band and append the codes. Numbers are never reused | No |
| 2 | Build the workload. It implements the same `AgentService` as every other agent | No |
| 3 | Register it: `agent_id`, endpoint, weight `0`, `signal_enabled = false` | No |
| 4 | Deploy and observe. Its signal is produced, recorded in lineage, and ignored | No |
| 5 | Run it in shadow mode against real traffic (ADR-012) | No |
| 6 | Re-weight, publishing the precision/recall comparison that justifies it | **Yes** |

Steps 1 to 5 are routine and reversible. Step 6 is a policy change: weights are
zero-sum, so basis points given to a new agent are taken from the existing ones,
and every decision near a threshold moves. It is versioned, evidenced and
replayable like any other policy change.

Two constraints are worth knowing before starting. Reason-code bands are 100
wide and only `700` and `800` remain below the platform range, so the numbering
scheme needs revisiting after roughly two more agents. And each agent is its own
workload (ADR-006), so each one adds a deployment, a dashboard, an alert set, a
breaker and a workload identity — the marginal cost is operational, not
architectural.

## 6. Features

Features are the only historical evidence available to agents. They are named
in snake_case, carry their window as structured data rather than in the name,
and are versioned by definition.

Initial catalogue:

| Feature | Window | Meaning |
|---|---|---|
| `transactions` | 5m, 1h, 24h | Count of transactions for the account |
| `amount` | 5m, 24h | Summed amount in minor units |
| `failed_attempts` | 10m | Declined or failed authorisations |
| `unique_devices` | 24h, 7d | Distinct devices used by the account |
| `unique_locations` | 24h | Distinct coarse locations |
| `accounts_per_device` | 24h, 30d | Distinct accounts seen on the device |
| `account_age` | instant | Time since `Account.opened_at` |
| `device_age` | instant | Time since the device was first seen |
| `last_location` | instant | Previous location and its timestamp, for travel plausibility |

A feature value carries `definition_version`. Two values of `amount_24h`
produced by different definition versions are not comparable, and the replay
engine must refuse to compare them.

## 7. Non-goals of the domain model

Legion does not model the payment lifecycle beyond authorisation: no capture,
settlement, chargeback or dispute. It does not model case management beyond
emitting `REVIEW`. It does not model customers, only accounts. Each of these is
a real system in a real bank, and none of them is needed to demonstrate that
the decision path is sound.
