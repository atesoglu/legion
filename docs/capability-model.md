# Capability model

Status: partially built. `control-plane/capability` runs the enforcement
pipeline this document describes (identity, grant, scope, constraint,
budget, in that order, every call audited) and it has one real caller
today: `investigation/worker` calls `CheckToolCapability` before every
mocked tool execution. What is still only documented intent: a
`RegisterAgent`/manifest API (the manifest is a static Go default,
`control-plane/capability/internal/broker/manifest.go`); mTLS/workload
identity (`workload_id` is asserted by the caller today, a Phase 1 stand-in,
same posture the rest of the platform takes pending ADR-011); and durable
audit storage (every check is logged and counted by verdict in
`legion.capability.checks`, but nothing persists the audit record itself —
and ADR-020,
which decides where metrics, logs and traces go, deliberately does not cover
this: a capability audit record is evidential, like lineage, not operational
like a log, so its store is still undecided).
This model governs Zone 6 investigation agents (ADR-017) as well as the
behavioural agent — there is one enforcement mechanism, not one per zone.

**Contract inconsistency, discovered rather than designed.** §3 below
describes exactly one RPC per capability
(`GetTransaction`/`GetVelocityFeatures`/etc.), which is
`capability_service.proto`'s `CapabilityService` — specified since Phase 0,
never implemented. The capability runtime that got built instead exposes a
generic `CheckCapability` RPC (`capability.proto`'s
`CapabilityRuntimeService`), added without noticing `CapabilityService`
already existed for this exact purpose. `CheckToolCapability` (the same
service) is not a duplicate — investigation's tool grants have no
equivalent in `CapabilityService` — but `CheckCapability`'s fixed-enum path
is genuinely redundant with a more specific, already-specified contract, and
neither has a real caller yet. See `architecture.md` §6 for the fuller
account and the recommendation: implement `CapabilityService` against
`internal/broker`'s existing pipeline when the behavioural agent needs it,
rather than carrying both.

## 1. Problem

An agent is the least trustworthy component in a risk platform. The
deterministic ones execute rules; the behavioural one executes a language model
over text that a fraudster may have written. If an agent holds a Redis
password, a service account token or unrestricted egress, then compromising one
narrow component compromises the platform's data.

The usual arrangement — every workload gets connection strings from a secret,
network policy allows what it needs, and the application is trusted to ask only
for what it should — fails at exactly the moment it matters. A compromised
agent with a Redis client can scan the keyspace.

Legion's position: **a compromised agent must not become a compromised
platform.**

## 2. Design

Agents hold no credentials. They have one outbound interface — the capability
runtime — and it exposes verbs, not resources.

```text
        Agent
          │  gRPC, mTLS, workload identity
          ▼
  Capability Runtime  ─── deny by default
          │            ─── verify identity, grant, constraint, scope, budget
          │            ─── audit every invocation, allowed and denied
          ▼
  Feature store / subject store
```

What an agent does **not** get:

| Not granted | Consequence |
|---|---|
| Feature-store credentials | Cannot read a key it was not given a verb for; cannot scan |
| Database credentials | No direct data access at all |
| Kubernetes API access | Cannot enumerate, read secrets, or schedule |
| Egress to the internet | Cannot exfiltrate; the model runtime is reached via an explicit internal route |
| Arbitrary filesystem access | Read-only root filesystem, no persistent volume |
| Service discovery | Cannot find services it was not configured to reach |
| Another evaluation's data | Every capability is scoped to one `evaluation_id` |

## 3. Capabilities

The closed set, from `legion.agent.v1.Capability`:

```text
GET_TRANSACTION          read the subject of the current evaluation
GET_ACCOUNT_FEATURES     read account features for the current subject
GET_VELOCITY_FEATURES    read velocity features for the current subject
GET_DEVICE_FEATURES      read device features for the current subject
GET_GEO_FEATURES         read geographic features for the current subject
SUBMIT_EVALUATION        return a signal for the current evaluation
```

`SUBMIT_EVALUATION` is separate on purpose. An agent without it can be deployed
and observed while being structurally unable to influence any decision.

Adding a member to this enum is a security-relevant change requiring review. It
is not a routine schema edit.

## 4. Grants and constraints

An `AgentManifest` is reviewable configuration. It is not something an agent
sends about itself, and the runtime binds grants to the platform-asserted
`workload_id`, never to a self-declared `agent_id`.

A `CapabilityConstraint` narrows a grant further:

- `allowed_feature_names` — an allow-list of feature names
- `allowed_windows` — which windows may be requested
- `max_invocations` — per-evaluation call budget; zero means the runtime
  default, which is never unlimited

Intended initial manifests:

| Agent | Capabilities | Notable constraints |
|---|---|---|
| Velocity | `GET_VELOCITY_FEATURES`, `SUBMIT_EVALUATION` | Velocity feature names only |
| Device | `GET_DEVICE_FEATURES`, `SUBMIT_EVALUATION` | Device feature names only |
| Geo | `GET_GEO_FEATURES`, `SUBMIT_EVALUATION` | Geo feature names only |
| Behavioral | `GET_TRANSACTION`, `GET_ACCOUNT_FEATURES`, `SUBMIT_EVALUATION` | Small named allow-list; low `max_invocations` |

Investigation agents (Zone 6, ADR-017) are manifested the same way, except
their grants name tools rather than a fixed capability enum member per
feature family — a tool registry is a database of grantable verbs, which is
exactly what this section already describes in the abstract. The enforcement
order in §5 is unchanged; only the shape of the grantable verb differs.

**A tool grant is not read from `agent_definitions`, deliberately.** That
table's `allowed_tools` column is a *declaration* — which tools an agent
intends to call — owned by Zone 6, whose worker holds write credentials for
that database. The *grant* lives in the capability runtime's own reviewed
manifest, in Zone 2, where nothing in Zone 6 can reach it. Sourcing the
grant from the declaration would let the component being constrained define
its own constraint, which is the exact failure T-14 exists to prevent.

The two must still agree in one direction: everything declared must be
granted, or an agent's first call to its second tool is denied at runtime as
if the capability runtime were broken. That invariant is asserted by
`test/e2e/capability_registry_test.go`, which queries the seeded
`agent_definitions` rows and asks the running capability runtime about every
tool each one declares — and separately proves no agent is granted a tool
only another agent declares. It runs in CI. The reverse direction (a grant
no agent declares) is not detected: the manifest is not enumerable over the
wire, and making it so would tell a caller what exists, which §5 forbids.

The behavioural agent — the one with a language model in it — holds the
narrowest useful set. It cannot read velocity, device or geo features directly;
it reasons over what it is given.

On the hot path the deterministic agents are served push-style: the
orchestrator fetches features once and passes them in `EvaluateRequest`, so
capability calls do not add round trips inside the budget. The capabilities
still exist and are still enforced, because the pull path is what an agent
would use if it were doing something it should not.

## 5. Enforcement

Every invocation is checked, in this order, before the operation runs:

1. **Identity** — mTLS peer identity resolves to a known `workload_id`.
2. **Grant** — the manifest for that identity holds the capability. Otherwise
   `DENIED_NOT_GRANTED`.
3. **Scope** — `evaluation_id` names an evaluation that is in flight and
   assigned to this agent. Otherwise `DENIED_SCOPE`.
4. **Constraint** — requested names and windows are within the allow-list.
   Otherwise `DENIED_CONSTRAINT`.
5. **Budget** — the per-evaluation invocation budget is not exhausted.
   Otherwise `DENIED_BUDGET`.

Denials return a `CapabilityDenied` carrying the verdict and nothing else. The
agent learns that it was refused; it learns nothing about what exists.

Properties the model is required to have, and that Phase 3 tests assert:

- **Deny by default.** Absence of a grant is a denial. There is no wildcard.
- **Explicit.** Every permission is a named enum member in a reviewed manifest.
- **Scoped.** No capability reaches beyond the current evaluation.
- **Auditable.** Every invocation emits a `CapabilityAudit`, allowed or denied.
- **Testable.** Each denial verdict has a test that proves the operation did
  not happen, not merely that an error was returned.
- **Extensible.** Adding a capability does not require changing enforcement.

## 6. Audit

`CapabilityAudit` records the evaluation, the identity, the capability, the
verdict and the timing. Audit records contain no feature values and no subject
data — the audit trail must not become a second copy of the data it protects.

A denial in steady state is a security signal: an agent asked for something it
was never granted. Denial rate per agent is alerted on, not merely logged.

## 7. Threats this addresses, and what remains

Addressed: T-01 malicious agent, T-02 compromised agent, T-06 unauthorised
feature access, T-07 data exfiltration, T-08 credential theft, T-09 lateral
movement. See [threat-model.md](threat-model.md).

Not addressed by this model:

- A **compromised capability runtime** is a compromised platform. The runtime
  is the trust anchor; it is small, in the control plane, and holds the only
  feature-store credential.
- An agent still receives real feature values within its grant. Capabilities
  bound *scope*, not *sensitivity*.
- A behavioural agent that returns a plausible but manipulated signal is acting
  within its capabilities. That is bounded by weighting and by deterministic
  decision authority (ADR-004), not by this model.
