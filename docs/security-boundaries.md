# Security boundaries

Status: Phase 1. The Zone 0 boundary is enforced: callers authenticate, every
field is validated, and an identifier that is not a pseudonym is rejected before
it enters the platform. Everything between zones is still open — services speak
plaintext gRPC and hold no workload identity. Capability enforcement is Phase 3;
network, identity and workload hardening are Phase 4.

## 1. Trust zones

```text
┌─────────────────────────────────────────────────────────────────┐
│ Zone 0 — Untrusted                                              │
│ Calling systems, payers, merchants, third-party enrichment.     │
│ All input is hostile until validated.                           │
└───────────────────────────────┬─────────────────────────────────┘
                                │  mTLS + authn + validation
┌───────────────────────────────▼─────────────────────────────────┐
│ Zone 1 — Edge                                                   │
│ Gateway. Terminates transport, authenticates, validates,        │
│ rate limits, establishes the deadline. No risk logic.           │
└───────────────────────────────┬─────────────────────────────────┘
                                │
┌───────────────────────────────▼─────────────────────────────────┐
│ Zone 2 — Control plane                                          │
│ Orchestrator, capability runtime. Holds the only feature-store  │
│ credential. Trust anchor for capability enforcement.            │
└──────────┬───────────────────────────────────┬──────────────────┘
           │                                   │
┌──────────▼───────────────┐   ┌───────────────▼──────────────────┐
│ Zone 3 — Data plane      │   │ Zone 4 — Agent sandbox           │
│ Rust engines, sentinel.  │   │ Behavioral agent, SLM runtime.   │
│ No I/O, no credentials.  │   │ No credentials, no egress.       │
│ Decision authority.      │   │ Processes untrusted text.        │
└──────────────────────────┘   └──────────────────────────────────┘
                                           │
┌──────────────────────────────────────────▼──────────────────────┐
│ Zone 5 — State                                                  │
│ Feature store, lineage store. Reachable only from Zone 2.       │
└─────────────────────────────────────────────────────────────────┘
```

Zone 4 is the most interesting boundary. The behavioural agent and the model
runtime process content that an adversary may have authored. They are therefore
treated as *potentially adversarial themselves*, not merely as components that
handle adversarial data.

The same zones, with the components inside them and the calls between them, are
drawn in [architecture §3](architecture.md#3-component-map).

## 2. Boundary rules

| Boundary | Rule |
|---|---|
| Zone 0 → 1 | mTLS or OIDC; every field validated; unbounded strings rejected; rate limited per caller |
| Zone 1 → 2 | mTLS with workload identity; the gateway cannot reach anything but the orchestrator |
| Zone 2 → 3 | mTLS; the data plane accepts requests only from the orchestrator |
| Zone 2 → 5 | The only path to state. The credential exists here and nowhere else |
| Zone 4 → 2 | Only via `CapabilityService`. Deny by default, scoped per evaluation, fully audited |
| Zone 4 → 5 | **Forbidden.** No direct path exists; network policy denies it |
| Zone 4 → internet | **Forbidden.** No egress |
| Zone 3 → anywhere | The sentinel makes no outbound calls at all |

## 3. What "zero trust" means here, and what it does not

Legion does not claim zero trust because Kubernetes `NetworkPolicy` objects
exist. Network policy is table stakes and stops only network-layer reachability.

The claims Legion intends to support, each with a test in Phase 3 or Phase 6:

- **No implicit trust from network position.** Being inside the cluster grants
  nothing; every call is authenticated by workload identity.
- **No ambient credentials in agents.** Agents have no secret to steal — see
  [capability-model.md](capability-model.md).
- **Authorisation per operation, not per connection.** Each capability
  invocation is checked against the manifest, not just at connection time.
- **Deny by default at both the network and application layers.** The
  application-layer denial is the one that matters, because it survives a
  misconfigured network policy.

What Legion does **not** claim: that the platform is unbreakable, that a
compromised control plane is survivable, or that any of this constitutes
compliance with a framework.

## 4. Workload hardening (Phase 4 targets)

- Non-root, no privilege escalation, all Linux capabilities dropped
- Read-only root filesystem; writable `emptyDir` only where genuinely required
- Distroless or scratch images; no shell in the agent or data-plane images
- CPU and memory requests and limits on every workload
- Default-deny `NetworkPolicy` with explicit per-pair allowances
- Egress restricted; the agent sandbox has none
- Workload identity (SPIFFE/SPIRE or the platform equivalent) instead of
  long-lived credentials in secrets
- Secrets mounted only in Zone 2, never in Zone 3 or 4

## 5. Data handling

### What Legion receives

Pseudonymous identifiers, tokenised instruments, coarse locations, amounts,
timestamps and low-cardinality categoricals. Legion does not receive primary
account numbers, names, addresses, or raw device fingerprints.

### Pseudonymisation

Persistent identifiers are HMAC-SHA-256 under a rotating platform key,
namespaced by `IdentifierDomain` (see
[domain-model.md](domain-model.md) §4). Unsalted digests are not used: the
input domain of payment identifiers is small enough to enumerate, so a plain
hash is reversible in practice.

**Pseudonymisation is not anonymisation.** Pseudonymous data remains personal
data. The HMAC key is a re-identification key, and its existence means the
GDPR obligations that apply to the source data continue to apply. Legion treats
pseudonymisation as a mitigation that reduces the impact of a data-plane
compromise, not as a legal transformation.

### Logs and lineage

Two channels with different rules:

| | Operational logs | Decision lineage |
|---|---|---|
| Contains | Service, stage, latency, error kind, counts | Decision, scores, reason codes, versions, timings, pseudonymous IDs |
| Never contains | Any identifier, amount, location, or model text | Transaction payload, feature values beyond cited evidence, model free text |
| Retention | Short | Long, for audit and replay |
| Access | Operators | Controlled; separate from operations |

Model output free text is not persisted. Only the validated structured signal
enters the pipeline and the lineage.

## 6. Model and prompt as security artefacts

The model runtime is self-hosted so that transaction-derived evidence never
leaves the deployment. That is a data-residency control, not a safety control:
a locally hosted model can still be manipulated.

- Model weights are pinned by checksum and recorded in `GovernedVersions`.
- Prompts are versioned artefacts under review, never edited in place in
  production.
- Untrusted text is passed as data, clearly delimited, never as instructions.
- Model output is validated against a JSON Schema and range-checked before it
  can influence anything.
- The model has no capability to assert a decision, and no `Decision` field
  exists in any type it can populate.

## 7. Regulatory positioning

Legion is **not** claimed to be GDPR, PCI DSS, DORA or EU AI Act compliant, and
none of local inference, pseudonymisation, Kubernetes, encryption or access
control makes it so. Compliance is a property of an organisation's processes,
evidence and scope, assessed by people, not a property of a repository.

What this project documents instead:

- The technical controls that exist, and where they are enforced
- The data it receives and the data it deliberately does not
- The security assumptions each control depends on
- Auditability: decision lineage, capability audit, version pinning
- Model governance: what is tracked and how a decision is reproduced
- Residual risks, in [threat-model.md](threat-model.md)

Where an obligation is plausibly relevant — for example the EU AI Act's
treatment of systems used in creditworthiness or fraud contexts, or DORA's
operational-resilience testing expectations — the sensible engineering response
is the same either way: bounded model influence, deterministic and explainable
decisions, versioned artefacts, reproducible outcomes and tested failure modes.
Those are built because they are good engineering, and they happen to be the
evidence a compliance process would ask for.
