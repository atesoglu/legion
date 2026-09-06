# ADR-004: Deterministic final decision authority

**Status:** Accepted
**Date:** 2026-09-04
**Phase:** 0

## Context

Legion uses a language model to assess behavioural risk. Language models are
non-deterministic under sampling, sensitive to prompt phrasing, vulnerable to
injection from text an adversary controls, and capable of producing confident
nonsense. They are also genuinely useful at spotting patterns in sequences that
rules describe badly.

A payment decision must be explainable, reproducible, auditable and stable. It
must be defensible to a customer who was declined, to an analyst investigating a
false negative, and to a regulator asking how the outcome was reached.

These two sets of properties are incompatible if the model decides.

## Decision

**AI produces bounded risk signals. Deterministic software owns the final
financial decision.**

Concretely:

1. Only `SentinelService`, implemented in Rust, maps scores to `ALLOW`,
   `REVIEW` or `DECLINE`.
2. No agent contract can express a decision. `AgentService` returns a
   `RiskSignal` or a `Failure`. There is no field in any type a model can
   populate that carries a `Decision`.
3. The behavioural signal is one weighted input among four, capped at its
   configured weight (initially 30 %).
4. Model output is validated against a JSON Schema and range-checked before it
   enters the pipeline; invalid output is rejected outright, not repaired.
5. The sentinel performs no I/O. Its output is a pure function of its request.

## Alternatives considered

**Model decides directly.** Simplest, and what a demo would do. Rejected: not
reproducible, not explainable in the terms a decision needs, and one successful
prompt injection away from being an attacker-controlled authorisation system.

**Model as final arbiter with deterministic guardrails** — the model decides,
rules can override. Rejected because it inverts responsibility. The guardrails
end up encoding the real policy anyway, but now it is split across two places
and the interaction between them is hard to reason about.

**No AI at all.** Entirely defensible, and Legion is explicitly required to be
useful in this state — Phase 1 is a complete deterministic system. Rejected as
the end state because behavioural sequence anomalies are genuinely hard to
express as rules, and the interesting engineering question is how to use a model
safely, not whether to avoid one.

**Model output as a hard filter** (any model concern forces `REVIEW`). Rejected:
it gives the model unbounded influence in one direction, which is exactly the
direction an attacker would use for denial of service.

## Trade-offs

- The model's usefulness is capped by its weight. A genuinely excellent
  behavioural signal cannot single-handedly stop fraud that the deterministic
  agents rate as benign.
- Deterministic policy needs tuning, and tuning needs measurement. Weights and
  thresholds become artefacts that must be evaluated (Phase 5) rather than
  learned.
- Some detection capability is given up in exchange for reproducibility. That
  is the trade being made, and it is made knowingly.

## Consequences

- Every decision is reproducible from lineage: same inputs, same
  `GovernedVersions`, same outcome for the deterministic portion.
- The system degrades gracefully when the model is unavailable — it loses 30 %
  of weight, not its ability to decide.
- Prompt injection is bounded in impact rather than merely being defended
  against (threat model T-05).
- Reason codes are structured and deterministic. Model prose is never the
  authoritative explanation.
- The behavioural agent's contribution must be *measured* to justify its
  existence. If Phase 5 shows it does not improve precision or recall, it should
  be removed rather than kept for appearances.

## Revisit if

Never, in the sense that the decision boundary itself is non-negotiable.
Reopening it requires the project owner's explicit approval. What may change is
the model's *weight*, on evidence from the evaluation framework.
