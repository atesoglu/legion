# ADR-011: Zero-trust workload model

**Status:** Accepted
**Date:** 2026-09-04
**Phase:** 4

## Context

"Zero trust" is asserted constantly and demonstrated rarely. A cluster with
`NetworkPolicy` objects is routinely described as zero trust, when what it
actually has is network segmentation — useful, but a control on reachability,
not on authorisation.

Legion handles transaction data and runs a language model over adversarial
input. The interesting question is not whether components can reach each other,
but what a component can do once it has been compromised.

## Decision

Legion adopts a workload trust model with four properties, each of which must be
demonstrable by a test rather than asserted:

1. **No implicit trust from network position.** Being inside the cluster grants
   nothing. Every service-to-service call is authenticated with mTLS and a
   platform-asserted workload identity (SPIFFE or equivalent).
2. **No ambient credentials.** Agents and the data plane hold no secrets. The
   feature-store credential exists only in the control plane (ADR-005).
3. **Authorisation per operation.** Capability checks happen on every
   invocation, not once at connection time.
4. **Deny by default at two layers.** Network policy denies by default, and the
   capability runtime denies by default. The application-layer denial is the one
   that matters, because it survives a misconfigured network policy.

Trust zones are defined in
[security-boundaries.md](../security-boundaries.md).

## Alternatives considered

**Network policy only.** The common interpretation. Rejected: it stops
reachability, not misuse of legitimate reachability. An agent that is *supposed*
to reach the feature store gets everything in it.

**A service mesh (Istio, Linkerd) for identity and mTLS.** A real option that
provides identity, mTLS and per-method authorisation without application code.
Rejected for now as premature: a large operational dependency, a latency cost in
the sidecar path against a tight budget, and authorisation granularity that
stops at the RPC method — it cannot express "only these feature names, only for
this evaluation". The capability model needs application-layer enforcement
regardless, so the mesh would be additive rather than sufficient. If mTLS
management becomes burdensome, a mesh is the natural answer.

**Perimeter security with a trusted interior.** Rejected: it is the model whose
failure the entire threat model is about.

## Trade-offs

- **Latency.** mTLS handshakes and per-operation authorisation cost time inside
  an 80 ms budget. Connection reuse and push-style feature delivery mitigate
  this; the residual cost must be measured.
- **Operational complexity.** Certificate issuance, rotation and identity
  bootstrap are real systems to run.
- **Development friction.** Local development needs a path that does not require
  the full identity infrastructure, and that path must not become the way it is
  deployed.

## Consequences

- Claims are scoped. Legion does not claim to be unbreakable, does not claim
  that a compromised control plane is survivable, and does not claim compliance
  with any framework. It claims specific properties, each backed by a test.
- Phase 6 must include tests that attempt to violate each property: an agent
  reaching the feature store directly, an agent using another agent's identity,
  a capability call outside its evaluation scope, egress from the agent sandbox.
- The control plane is the trust anchor and is explicitly acknowledged as such
  in the threat model. Concentrating trust there is a decision, not an
  oversight.

## Revisit if

mTLS and identity management prove operationally unsustainable at this scale, or
the latency cost of per-operation authorisation is shown to be material — in
which case the answer is caching authorisation decisions within an evaluation,
not abandoning the model.
