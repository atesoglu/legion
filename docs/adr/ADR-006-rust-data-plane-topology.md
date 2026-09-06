# ADR-006: Rust data plane process topology

**Status:** Accepted
**Date:** 2026-09-04
**Phase:** 1

## Context

Legion has four Rust components: the velocity, device and geo engines, and the
sentinel. How many processes they occupy, and where the boundaries fall, is a
deployment decision that the contracts should not encode.

Every additional process costs manifests, dashboards, alerts, rollout
coordination and on-call surface. Every additional process boundary converts a
function call into a serialisation and a hop inside an 80 ms budget. Those costs
are real, and they are the reason not to split by reflex.

Two things about Legion make them cheaper here than they usually are.

The first is that the orchestrator already addresses every component as a
distinct logical service over its own contract. It makes the same number of
calls regardless of how the components are packaged, so consolidation removes no
round trip — it only converts a network call into a loopback one.

The second is that the three engine calls are issued **in parallel**, inside a
single shared 8 ms window. Separating them adds no serial time to the request.

What differs sharply between the components is failure semantics. The failure
model permits an engine to fail: its signal is excluded, the remaining weights
are renormalised, and the evaluation continues as `PARTIAL`. It permits nothing
of the sort for the sentinel, which is the only component that can produce a
decision at all — hence the deadline model's rule that the sentinel always
runs.

The release profile sets `panic = "abort"`. Co-located components therefore die
together, and a panic is caused by an input to one component's logic, not by a
condition shared across them.

## Decision

Each of the four Rust components is its own process and its own deployment.

```text
Velocity engine │ Device engine │ Geo engine        Sentinel
─────────────────────────────────────────────       ─────────────────────────
may fail · signal excluded, weights renormalised    must not fail · no
independent faults, independent degradation         decision without it
```

They are called **engines**. There is no intermediate "engine server" tier: each
engine crate is itself the deployable, and `data-plane/engines/velocity`,
`data-plane/engines/device` and `data-plane/engines/geo` map one-to-one onto
workloads. The sentinel sits outside `engines/`, so the directory layout carries
the may-fail / must-not-fail split rather than leaving it to documentation.

Two properties drive this.

**The sentinel must not share a fate with anything permitted to fail.** Under
`panic = "abort"`, co-locating it with a component that is allowed to die makes
the "sentinel always runs" invariant unenforceable.

**Engine faults are not correlated.** Infrastructure faults are — a lost node
takes everything on it — but those are not what process isolation protects
against. Software faults are per-engine, and consolidating the engines converts
three independent, individually survivable degradations into one correlated
failure that removes all three deterministic signals at once. That will usually
breach the policy's minimum included weight and force a `FALLBACK` where three
separate processes would have produced a `PARTIAL`.

The gRPC contracts remain unchanged (`AgentService` for the engines,
`SentinelService` for the sentinel). Topology stays a matter of configuration.

**This justification is structural, not measured.** It follows from failure
semantics already accepted elsewhere in the design. Nothing in Legion has been
benchmarked, no independent-scaling claim is made, and no latency claim is made.

## Alternatives considered

**All four in one process.** The original form of this decision, justified on
the grounds that consolidation "avoids three network hops". That argument does
not survive inspection: the orchestrator addresses each component as a separate
logical service, so it makes the same number of calls either way. Set against a
claim that turned out to be false is the fate-sharing problem above. Rejected.

**Three engines together, sentinel separate.** Fixes the invariant that matters
most and costs only one extra deployment. Rejected because it treats the three
engines as interchangeable for failure purposes, which they are not: the faults
that kill a process under `panic = "abort"` are exactly the ones that differ
between them. It buys the cheapest half of the isolation and leaves the
correlated-degradation problem in place.

**One process, one merged contract.** Cheaper still, and rejected because it
would hard-code a deployment choice into the protocol, making any later change a
contract migration.

**Link Rust into the Go orchestrator via cgo/FFI.** Removes the hops entirely
and would probably be fastest. Rejected: it couples two build systems and
deployment lifecycles, makes the boundary invisible, gives up process-level
fault isolation between control plane and data plane, and mixes Go's runtime
with foreign code in ways that complicate profiling. Worth revisiting only if
measurement shows the hops are a material share of the budget.

## Trade-offs

- **Four deployments instead of one.** Four sets of manifests, dashboards,
  alerts and circuit breakers, and four things that can be misconfigured. This
  is the strongest argument against this decision and it does not go away.
- **A resource floor per workload.** Four pods each holding a minimum request,
  rather than one. At low traffic this is straightforwardly wasteful.
- **More boundaries to secure.** Each engine needs its own workload identity,
  mTLS peer authorisation and network policy (ADR-011).
- **No scaling claim is being made.** The engines may well have near-identical
  resource profiles. The justification here is fault isolation and release
  independence, not throughput, and nothing has been measured.
- **Latency cost is near zero, not zero.** The three engine calls were already
  parallel and already separate calls, but they now cross a network rather than
  a loopback boundary, and the slowest of the three still closes the window.

## Consequences

- The workspace produces four data plane binaries. Each engine crate is both
  a library and a binary; the sentinel is the fourth.
- The orchestrator holds four endpoints with independent circuit breakers. It
  already had to, since it was forbidden from assuming co-location.
- Losing one engine costs one signal. The failure model's `PARTIAL` path is the
  common case rather than the theoretical one.
- Zone 3 contains four workloads, and Legion has seven deployables in total,
  which ADR-010 reflects.
- Each engine is released, rolled back, resourced and scaled on its own.

## Revisit if any of these becomes true

- The operational cost of four workloads outweighs the isolation in practice, in
  which case they are consolidated and this ADR records the toil that justified
  it. Consolidation is as legitimate a direction as separation.
- Measurement shows the per-engine hops are a material share of the 80 ms
  budget.
- Two engines converge to the point that they are always released together and
  always scaled together, at which point they are one component, not two.

A change to this topology after Phase 1 has shipped will be recorded in a
superseding ADR with the measurement that justified it.
