# ADR-008: Self-hosted small language model

**Status:** Accepted
**Date:** 2026-09-04
**Phase:** 2

## Context

The behavioural agent needs to assess patterns that rules express badly —
sequences of activity that look wrong in combination without any single element
crossing a threshold.

Two constraints dominate. First, the data is transaction-derived and personal;
sending it to a third-party API creates a data-transfer and processing
relationship that a bank would have to justify, and that Legion would rather not
create. Second, the request budget for the behavioural stage is around 35 ms,
which rules out anything involving a round trip to an external provider.

## Decision

The behavioural agent uses a self-hosted small language model. The initial
runtime is vLLM; the architecture is model-agnostic and runtime-agnostic.

Constraints on the integration:

- The model is reached only through the behavioural agent, never directly by
  the orchestrator or the sentinel.
- Output is JSON constrained by a JSON Schema, validated and range-checked
  before it can influence anything.
- The model is pinned by name, version, checksum, quantisation and runtime
  version, all recorded in `GovernedVersions` on every decision.
- Prompts are versioned artefacts, reviewed and evaluated like code.
- The runtime has no egress and holds no credentials.
- Its signal carries at most its configured weight; it cannot decide (ADR-004).

## Alternatives considered

**Hosted LLM API.** Better models, no infrastructure. Rejected: transaction data
would leave the deployment, latency is not controllable within a 35 ms budget,
and the platform's behaviour would depend on a vendor's silent model updates —
which destroys the version-pinning that decision reproducibility depends on.

**A large self-hosted model.** Better reasoning, considerably more GPU and more
latency. Rejected: the task is narrow and structured. Model capability is not
the binding constraint; latency and governance are.

**A classical ML model (gradient boosting) instead.** Faster, cheaper, more
predictable, and probably better on tabular features. Genuinely a strong
alternative, and honesty requires saying so. It is not chosen here because it
does not address the sequence-reasoning gap this agent exists for, and because
demonstrating *how to constrain a language model safely* is one of the project's
explicit goals. If Phase 5 shows the behavioural agent does not improve
precision or recall, the correct response is to remove it, not to defend it.

**No behavioural agent.** The Phase 1 system, which must be useful on its own.
Retained as the permanent fallback, not as the end state.

## Trade-offs

- **Operational cost.** GPU capacity, model lifecycle, runtime upgrades, and a
  new failure surface.
- **Latency variance.** Inference is the least predictable stage by a wide
  margin. It gets the largest budget and the strictest abandonment rule.
- **Non-determinism.** Sampled output is not bit-reproducible. This is why
  replay distinguishes deterministic replay from full replay (ADR-013).
- **New attack surface.** Prompt injection (T-05), compromised runtime (T-03),
  model manipulation (T-04). Each is bounded by the decision authority
  constraint rather than eliminated.

## Consequences

- Model governance is mandatory, not optional: name, version, checksum,
  quantisation, runtime version, prompt version, schema version and evaluation
  dataset version are all tracked.
- Structured output is a hard requirement. A model that cannot be constrained to
  a schema cannot be used.
- The system must remain fully functional with the model unavailable, and the
  fallback path is exercised in tests rather than assumed.
- The behavioural agent's value must be *measured*. "The AI helps" is not a
  claim this project is allowed to make without a precision/recall comparison.

## Revisit if

Measurement shows the behavioural signal does not improve detection quality; or
inference cannot fit the budget under realistic concurrency; or a classical
model is shown to do the same job better, in which case it should replace the
SLM rather than sit alongside it.
