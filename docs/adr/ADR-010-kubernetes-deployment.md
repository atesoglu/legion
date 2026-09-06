# ADR-010: Kubernetes as the deployment target

**Status:** Accepted
**Date:** 2026-09-04
**Phase:** 4

## Context

Legion has seven deployables with different resource profiles and different
trust levels: a stateless Go edge service, a stateless Go control plane, four
CPU-bound Rust workloads separated by blast radius (ADR-006), and a GPU-bound
inference runtime. The security model
depends on workload identity, default-deny networking and per-workload
hardening. The scaling model depends on workload-specific signals.

Deployment is on Hetzner, chosen for cost and for the absence of a managed
control plane doing things invisibly.

## Decision

Kubernetes is the deployment target, on Hetzner. Helm packages the workloads;
Terraform provisions the infrastructure beneath them.

Autoscaling uses KEDA where a meaningful workload signal exists — inference
queue depth, active requests, request rate — rather than an HPA on CPU
everywhere. Autoscaling is added where it is justified and benchmarked, not by
default.

Deployment happens in Phase 4, after the local architecture is stable. Building
manifests for a system whose shape is still changing produces manifests that
document a system that no longer exists.

## Alternatives considered

**Docker Compose on a single host.** Sufficient for the first phases, and used
for local development. Rejected as the target: no workload identity, no network
policy, no meaningful scaling — which removes most of what the security and
resilience work is about.

**Nomad.** Simpler, genuinely good, and a defensible choice. Rejected on
ecosystem: NetworkPolicy equivalents, SPIFFE integration, KEDA and the
observability tooling are all more mature on Kubernetes, and the target audience
for this project operates Kubernetes.

**A managed cloud platform (Cloud Run, ECS, App Runner).** Faster to operate.
Rejected: the security boundaries in this project are precisely the parts a
managed platform abstracts away, and self-hosted inference does not fit them
well.

**Bare metal with systemd.** Lowest latency, no orchestration overhead, and
honest for a latency-focused system. Rejected: no isolation story, no scaling
story, and it would not demonstrate the operational reasoning the project is
about.

## Trade-offs

- **Complexity.** Kubernetes is a large system to operate, and self-managed on
  Hetzner means owning the control plane, etcd, upgrades and CNI.
- **Latency.** Overlay networking, kube-proxy or eBPF, and pod scheduling all
  add variance to the tail this project cares about. This must be measured
  against local benchmarks rather than assumed away.
- **Cost of correctness.** Doing the security posture properly — network
  policies, identity, admission control — is real work, and doing it badly is
  worse than not claiming it.

## Consequences

- Every workload declares CPU and memory requests and limits; unbounded
  workloads are not deployed.
- Containers run non-root, with all Linux capabilities dropped, no privilege
  escalation, and a read-only root filesystem where practical.
- Readiness and liveness probes, and graceful shutdown that outlasts the request
  deadline, are requirements rather than refinements — hence the startup check
  that the shutdown grace period exceeds the request deadline.
- Benchmarks are run both locally and in-cluster, and the difference is
  reported. A latency number from a laptop is not a latency number from a
  cluster.

## Revisit if

Cluster overhead proves to consume a material share of the request budget, or
the operational cost of self-managing Kubernetes outweighs its benefit for a
system this size.
