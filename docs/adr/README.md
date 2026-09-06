# Architecture Decision Records

An ADR records a decision that was expensive to make and would be expensive to
reverse. It states the context that forced the decision, what was decided, what
was rejected and why, and what the decision costs.

An ADR is not documentation of the implementation. If the implementation drifts
from the ADR, either the implementation is wrong or the ADR has been
superseded — and once a decision has been implemented, a superseding ADR is
written rather than the original edited.

Before a decision has been implemented there is nothing to be archaeological
about, and an ADR may be revised in place. A superseded/superseding pair
describing two designs neither of which was ever built is confusion, not
history.

## Index

| ADR | Title | Status | Phase |
|---|---|---|---|
| [001](ADR-001-go-for-orchestration.md) | Go for orchestration | Accepted | 0 |
| [002](ADR-002-rust-for-deterministic-computation.md) | Rust for deterministic computation | Accepted | 0 |
| [003](ADR-003-protobuf-grpc-contracts.md) | Protobuf and gRPC as authoritative contracts | Accepted | 0 |
| [004](ADR-004-deterministic-decision-authority.md) | Deterministic final decision authority | Accepted | 0 |
| [005](ADR-005-capability-based-agent-access.md) | Capability-based agent access | Accepted | 3 |
| [006](ADR-006-rust-data-plane-topology.md) | Rust data plane process topology | Accepted | 1 |
| [007](ADR-007-feature-store.md) | Redis/Dragonfly as the feature store | Accepted | 1 |
| [008](ADR-008-self-hosted-slm.md) | Self-hosted small language model | Accepted | 2 |
| [009](ADR-009-end-to-end-deadline.md) | An 80 ms end-to-end request deadline | Accepted | 1 |
| [010](ADR-010-kubernetes-deployment.md) | Kubernetes as the deployment target | Accepted | 4 |
| [011](ADR-011-zero-trust-network-model.md) | Zero-trust workload model | Accepted | 4 |
| [012](ADR-012-shadow-mode.md) | Shadow mode as the change mechanism | Accepted | 5 |
| [013](ADR-013-replay-architecture.md) | Replay architecture | Accepted | 5 |
| [014](ADR-014-agent-set-is-configuration.md) | The agent set is configuration | Accepted | 1 |
| [015](ADR-015-component-private-packages.md) | Component-private packages | Accepted | 1 |
| [016](ADR-016-deployment-identity-and-packaging.md) | Deployment identity and packaging | Accepted | 4 |

"Accepted" means the decision has been made and is binding on implementation.
It does not mean the thing has been built; the phase column says when it is
built.

## Template

See [template.md](template.md).
