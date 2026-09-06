# ADR-005: Capability-based agent access

**Status:** Accepted
**Date:** 2026-09-04
**Phase:** 3

## Context

Agents are the least trustworthy components in Legion. The deterministic ones
parse externally influenced data; the behavioural one runs a language model over
text an adversary may have written.

The conventional arrangement gives each workload the credentials it needs —
a Redis connection string in a secret, network policy permitting the connection
— and trusts the application to ask only for what it should. Under that
arrangement, compromising one narrow agent yields a Redis client with access to
the whole keyspace. The blast radius of a single component failure is the entire
feature store.

## Decision

Agents receive no credentials and no infrastructure access. Every action an
agent may take is an explicit capability, brokered by a capability runtime in
the control plane, denied by default.

- Capabilities are verbs (`GET_VELOCITY_FEATURES`), not resources (a Redis
  connection).
- Grants are declared in reviewable `AgentManifest` configuration and bound to
  a platform-asserted `workload_id`, never to a self-declared identity.
- Every capability is scoped to a single in-flight `evaluation_id`.
- Constraints narrow grants further: allow-listed feature names, allow-listed
  windows, per-evaluation invocation budgets.
- Every invocation is audited, allowed or denied.
- `SUBMIT_EVALUATION` is a separate capability, so an agent can be deployed and
  observed while being structurally unable to influence a decision.

Full specification: [capability-model.md](../capability-model.md).

## Alternatives considered

**Direct feature-store access with per-agent credentials and ACLs.** Simplest
and lowest latency. Rejected: Redis ACLs are key-pattern based, so the security
boundary becomes a naming convention. A key-addressable interface also lets a
compromised agent enumerate, and enumeration is the whole game.

**Network policy alone.** Restrict which agents can reach the store. Rejected:
it controls reachability, not authorisation. An agent that legitimately needs
the store gets everything in it.

**A service mesh with authorisation policies.** Real capability, at the cost of
a large operational dependency, and still coarse — it authorises RPC methods,
not per-evaluation scope or feature-name allow-lists. Rejected as premature.

**OPA / external policy engine.** Flexible and auditable. Rejected for now: an
extra network hop inside the request budget, and the policy surface here is
small and closed enough that a typed enum and a manifest express it better than
a general policy language.

## Trade-offs

- The capability runtime is a new component on the hot path, and a new single
  point of failure. It is in the control plane, which already holds the store
  credential, so it concentrates rather than adds trust — but it is now the
  thing that must not be compromised.
- Pull-style capability calls add round trips. Mitigated by serving the
  deterministic agents push-style: the orchestrator fetches features once and
  passes them in `EvaluateRequest`. The capabilities still exist and are still
  enforced, because the pull path is what a misbehaving agent would use.
- Adding a capability is a reviewed change to a protobuf enum, which is
  deliberate friction.

## Consequences

- A compromised agent sees the feature values legitimately granted for the
  evaluations flowing through it, one subject at a time — and nothing else.
- Capability denial rate becomes a security metric, alerted on rather than
  logged.
- Phase 3 tests must assert that a denied operation *did not happen*, not merely
  that an error was returned.
- Capabilities bound scope, not sensitivity. An agent still sees real data
  within its grant, and this model does not change that.

## Revisit if

The runtime's latency cost proves material against the budget, or the grant
surface grows past what a manifest can express clearly — at which point a
policy engine becomes the better tool.
