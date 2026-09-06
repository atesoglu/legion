# ADR-014: The agent set is configuration

**Status:** Accepted
**Date:** 2026-09-04
**Phase:** 1

## Context

Legion defines four agents today: velocity, device, geo and behavioural. That
number will grow. Two reason-code bands — `400` amount-and-pattern and `600`
authentication — already carry codes that no agent yet produces, so the growth
is anticipated rather than hypothetical.

The contracts are already shaped for it. `RiskSignal.agent_id` is a `string`
rather than an enum, so a new agent requires no protocol change. Every agent
implements the same `AgentService`. `AgentManifest` is declared configuration
and carries `signal_enabled`, which exists so that an agent can be deployed and
observed before it is permitted to affect a decision.

None of that survives an orchestrator that names its agents in code. With
exactly three deterministic engines to call, the path of least resistance in
Phase 1 is three call sites, three client fields, three breaker declarations and
three assembly steps. That code works, passes its tests, and quietly converts
every future agent into a change to the control plane — which is precisely the
component with the least business having an opinion about which agents exist.

The cost is not the first extra agent. It is that per-agent code paths diverge:
one agent gets a bespoke timeout, another a special-cased exclusion rule, and
the claim that the orchestrator treats all agents identically stops being true.
That claim is load-bearing, because it is what stops the behavioural agent
acquiring a privileged path into the decision (ADR-004).

## Decision

The set of agents is configuration. The orchestrator contains no per-agent code
path.

Concretely, an implementation complies with this ADR when all of the following
hold.

1. Agents are held in a registry keyed by `agent_id`, each entry carrying at
   minimum an endpoint, a weight in basis points, and the agent's manifest.
2. Fan-out, deadline apportionment, circuit breaking, exclusion, renormalisation
   and contribution assembly are written once and iterate over that registry.
3. No `agent_id` value appears as a literal in orchestrator or sentinel logic.
   Agent identifiers appear in configuration, in lineage, and in tests.
4. Adding an agent to a running system is a configuration change plus a
   deployment of that agent. It is not a change to the orchestrator.
5. Summation order is derived mechanically from the registry — agents sorted by
   `agent_id` — so that the fixed-order requirement in the decision model is a
   property of the data, not of a hand-maintained list.

The one thing that is deliberately *not* configuration is the weight set as a
whole. Weights are zero-sum: introducing an agent at a non-zero weight takes
basis points from every other agent and moves every decision near a threshold.
Registering an agent is cheap; giving it weight is a policy change, versioned
and evidenced like any other.

## Alternatives considered

**Explicit per-agent code in the orchestrator.** Simpler to read at four agents,
and genuinely easier to debug when the agent set is small and static: you can
see exactly what happens to the geo call. Rejected because the agent set is
neither small nor static in intent, and because the moment two agents need
different handling, the identical-treatment property is gone and nothing in the
build will tell you.

**A plugin or dynamic-dispatch model, loading agents at runtime.** Maximum
flexibility. Rejected: it puts unreviewed code inside a trust boundary, defeats
the point of agents being separate workloads with their own identities, and
turns a deployment concern into a runtime one. Agents are processes, not
plugins.

**Code generation from the agent registry.** A build step emitting the fan-out
code from a manifest. Rejected as ceremony: the generic loop this would generate
is short enough to write once by hand, and the generator becomes another thing
to maintain and understand.

## Trade-offs

- **Generic code is harder to read than four explicit calls.** A reader must
  hold the registry in mind to know what actually happens on a request. This is
  a real cost and it is paid on every future read of the orchestrator.
- **Misconfiguration replaces miscompilation.** A wrong `agent_id` or a missing
  endpoint becomes a runtime failure rather than a build failure. Configuration
  validation at startup is therefore mandatory, not optional: an orchestrator
  that cannot resolve a registered agent must refuse to start.
- **Uniformity has to be defended.** The first genuinely agent-specific
  requirement will arrive, and it must be expressed as a *field* in the registry
  — a per-agent budget, a per-agent minimum confidence — rather than as a branch
  on `agent_id`.

## Consequences

- Introducing an agent is: allocate a reason-code band, build the workload,
  register it with weight `0` and `signal_enabled = false`, observe it in shadow
  mode (ADR-012), then re-weight with a measured precision/recall comparison.
  Only the last step touches decision behaviour.
- The orchestrator's tests exercise the registry, not the four agents that
  happen to exist. A test that names `velocity` is testing configuration.
- Per-agent operational cost is real and rises with each agent, because each is
  its own workload under ADR-006: a deployment, a dashboard, an alert set, a
  breaker and a workload identity.
- Reason-code banding becomes the binding constraint before the code does. Bands
  are 100 wide and `700` and `800` are the only ones left below the platform
  range, so the fifth and sixth new agents will force a decision about the
  numbering scheme.

## Revisit if

- Two agents genuinely require different orchestration semantics that cannot be
  expressed as registry fields. That is evidence the `AgentService` contract is
  wrong, and the contract should be fixed rather than the loop branched.
- The agent set stabilises permanently at a small number, in which case the
  indirection is costing more than it saves.
