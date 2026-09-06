# ADR-016: Deployment identity and packaging

**Status:** Accepted
**Date:** 2026-09-06
**Phase:** 4

## Context

Legion has seven deployables across two languages: the gateway, the
orchestrator, the capability runtime, three engines, the sentinel, the
behavioural agent, and a self-hosted inference runtime. Manifests for them
arrive in Phase 4, deliberately late (ADR-010), because a system whose shape is
still changing produces manifests that document a system that no longer exists.

Two decisions cannot wait that long, because both are cheap now and expensive
once seven charts exist.

The first is what each deployable is *called*. A component currently appears
under several names: a directory, a crate, a binary, a `SERVICE_NAME` in its
logs, and an `agent_id` in signals and lineage. Some of those already agree. If
the rest are chosen independently at packaging time, an operator holding a log
line, a pod name and a decision record needs a translation table to connect
them, and that table is maintained by hand and wrong within a quarter.

The second is where the trust zones go. `docs/security-boundaries.md` states the
boundaries as rules between zones — Zone 4 has no egress and no path to Zone 5,
Zone 2 holds the only feature-store credential, the gateway may reach nothing
but the orchestrator. ADR-011 requires each of those to be demonstrable by a
test rather than asserted. How zones are expressed in the cluster decides
whether such a test is a single scoped assertion or a survey of every pod.

A third fact constrains packaging: all Go services share one `go.mod` and all
Rust crates share one `Cargo.toml` and one lockfile, both at the repository
root. Every image therefore builds with the repository root as its build
context, whatever directory its `Dockerfile` sits in.

## Decision

### 1. One identifier per deployable

Each deployable has exactly one identifier, and it appears everywhere that names
the thing:

| Layer | Form |
|---|---|
| Source directory | `data-plane/engines/velocity` |
| Crate or Go package | `legion-velocity` |
| Binary | `legion-velocity` |
| Log `service` field | `velocity` |
| `agent_id` in signals and lineage | `velocity` |
| Image repository | `legion/velocity` |
| Deployment, Service, ServiceAccount | `velocity` |
| Helm values key | `services.velocity` |
| Workload identity | `spiffe://legion/velocity` |
| Label selector | `app.kubernetes.io/name: velocity` |

The identifier is a lowercase DNS-1123 label, preferably a single word. The
`legion-` prefix exists only where a global namespace demands it — crate names
on a shared registry, image repositories — and is never part of the identifier
itself.

Binding the workload identity to the same string as the `agent_id` is not
cosmetic. The capability runtime authorises grants declared in `AgentManifest`
against an identity asserted by the platform (ADR-005, ADR-011). When those two
strings are visibly the same subject, a grant can be read against an identity
without an intermediate mapping that could itself be wrong.

### 2. Zones are namespaces

```text
legion-edge      gateway
legion-control   orchestrator · capability
legion-data      sentinel · velocity · device · geo
legion-agents    behavioral · inference
```

Zone 5 is external to the cluster or lives in its own namespace with no
workloads of ours.

Network policy is written between namespaces, so the boundary table in
`security-boundaries.md` transcribes into `namespaceSelector` rules almost
line for line. The two absolute prohibitions — Zone 4 to Zone 5, and Zone 4 to
the internet — become one default-deny egress policy scoped to
`legion-agents`, which therefore applies to every future agent without anyone
remembering to attach it.

This is what makes ADR-011's testability requirement practical: "no workload in
`legion-agents` can egress" is a single scoped assertion. The equivalent claim
over pod labels in a shared namespace is weaker, because a workload missing its
label fails open.

### 3. Two Dockerfiles, parameterised, root context

```text
deploy/docker/go.Dockerfile      ARG SERVICE  → gateway · orchestrator · capability · behavioral
deploy/docker/rust.Dockerfile    ARG CRATE    → sentinel · velocity · device · geo
```

Six images from two files. Both build from the repository root; the `Dockerfile`
lives under `deploy/docker/` rather than beside `main.go` specifically so that
the root context is obvious rather than surprising.

The inference runtime is a pinned upstream model server. It is configuration and
an image reference, not something built here, and it gets its own chart because
GPU scheduling, a model volume and queue-depth autoscaling have nothing in
common with the other six.

The Rust image uses a dependency-recipe layer (`cargo-chef` or equivalent).
Seven crates on one lockfile otherwise rebuild every dependency on every source
change, and slow image builds are skipped image builds.

### 4. The service set is data

`deploy/services.yaml` declares the topology once:

```yaml
velocity:     { zone: data,    language: rust, port: 9300, agent: true }
sentinel:     { zone: data,    language: rust, port: 9600, agent: false }
gateway:      { zone: edge,    language: go,   port: 9100, agent: false }
```

One Helm chart iterates over it, with per-service overrides for the cases that
genuinely differ, rather than seven subcharts that drift. CI asserts that the
set of buildable binaries — Go main packages plus Rust binary crates — equals the
set declared here, and fails when they diverge.

That check is the same guarantee `make proto-verify` already provides for
generated bindings: a derived artefact must match its source or the build
breaks. It is also the deployment counterpart of ADR-014 — the agent set is
configuration for the orchestrator, the service set is configuration for
deployment, and in both cases adding a component is a data change.

Distinct ports (9100–9600) are retained even though in-cluster every Service has
its own DNS name and could share one port. The value is that the local
development path and the cluster differ in one fewer respect.

**No manifests, charts or Dockerfiles are written by this ADR.** It fixes the
names and the topology source; Phase 4 writes the artefacts.

## Alternatives considered

**A `Dockerfile` per service, beside its `main.go`.** The conventional layout,
and immediately familiar. Rejected: seven near-identical files drift, and their
placement implies a per-component build context that the shared `go.mod` and
shared workspace make impossible. The convention would be actively misleading
here.

**One namespace with zone labels.** Lighter to operate: no cross-namespace DNS,
no RBAC multiplication, simpler charts. Rejected because every policy must then
select correctly on its own, and a workload deployed without its zone label is
unconstrained rather than rejected. The failure mode is silent and open, which
is the wrong direction for a boundary the threat model depends on.

**A Helm subchart per service.** Better isolation between services and easier
per-service divergence. Rejected at this size: six of the seven are stateless
gRPC services differing only in name, port and zone, so subcharts would
duplicate the same template six times. The inference runtime, which genuinely
differs, does get its own chart.

**Kustomize instead of Helm.** Defensible, and avoids templating language.
Rejected for consistency with ADR-010, which already selected Helm, and because
a services list plus a loop is exactly what Helm's templating is for.

**Deriving the service list from the filesystem** rather than declaring it.
Tempting, and removes a file to maintain. Rejected: it cannot express zone, port
or whether the service is an agent, and it would silently package anything that
happened to compile.

## Trade-offs

- **Namespaces cost real operational surface.** Four namespaces mean
  cross-namespace FQDNs, RBAC repeated per namespace, and more conditional
  templating. This is the largest cost of this decision.
- **Parameterised Dockerfiles are harder to read and to grep.** A per-service
  special case has nowhere natural to go, and the first one will be tempted to
  add an `if`. When a service genuinely diverges, it gets its own file.
- **`services.yaml` is a third source of truth**, after the Go module and the
  Cargo workspace. The CI drift check is what keeps that honest, and without the
  check this decision is worse than nothing.
- **The naming spine constrains renames.** Changing an identifier now touches a
  directory, a crate, an image, a Helm key, a SPIFFE ID and recorded lineage.
  Renames become deliberate, which is the intent, but they are no longer cheap.
- **Local and cluster paths still diverge.** ADR-011 requires a development path
  that does not need the identity infrastructure, and that path will have a
  weaker security posture than the deployed one. Nothing structural fixes this;
  it is managed by keeping the differences few and named.

## Consequences

- An operator can move between a log line, a pod, a source directory and a
  decision's lineage without a lookup table.
- Adding a service is a `services.yaml` entry, a directory and a workspace
  member. It is not a new chart, and CI refuses the combination where one is
  missing.
- The boundary rules in `security-boundaries.md` become reviewable as network
  policy, because both are expressed between the same named zones.
- Every workload declares requests and limits, runs non-root with capabilities
  dropped, and has probes and a graceful shutdown that outlasts the request
  deadline (ADR-010). The shared chart template is where those stop being
  per-service diligence and become defaults.
- The identifier set is small, closed and reviewable, which makes an unexpected
  workload identity in a capability audit a meaningful signal.

## Revisit if

- A service mesh is adopted, which would supply identity and mTLS and change
  what the SPIFFE ID and namespace layout need to express (ADR-011 lists the
  conditions).
- Services diverge enough that the shared chart accumulates conditionals; at
  that point subcharts are the better structure and this ADR is superseded with
  the divergence that justified it.
- The service count grows beyond what a single `services.yaml` and one chart can
  express clearly.
