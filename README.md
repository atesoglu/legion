# Legion

A self-hosted risk-decisioning platform for real-time payment authorisation.

Go orchestrates. Rust computes. AI provides bounded signals. Deterministic
policy owns the decision. Capabilities constrain agents. Evidence validates the
claims.

**License:** MIT · **Languages:** Go, Rust · **Contracts:** Protobuf/gRPC

---

## Status: Phase 1 — Deterministic Decision Path

A transaction presented at the gateway returns a decision, with reason codes and
a per-agent contribution breakdown, and **no AI in the path**. The chain is
exercised end to end by a suite that starts the real binaries and speaks gRPC to
them.

| | |
|---|---|
| Contracts | Defined, compiling, linted |
| Documentation | Architecture, domain, decision, deadline, failure, capability, security, threat model, 16 ADRs |
| Decision path | Gateway → orchestrator → velocity, device, geo → sentinel |
| Edge controls | Authentication, validation, per-caller rate limiting, deadline origin |
| Feature store | Redis reads with explicit freshness; **nothing writes features yet** |
| Behavioural agent, capability runtime, Kubernetes, replay, evaluation, benchmarks | Not implemented |

**No performance, detection-quality or security claim in this repository is
currently supported by measurement, and none is made.** That is the point of the
phase structure: claims arrive with the evidence for them, or not at all.

The weights, thresholds and rule thresholds in use are configured defaults
awaiting the Phase 5 evaluation framework. They are not a claim about correct
fraud detection.

## The idea

Legion decides whether a payment should be allowed, reviewed or declined,
within roughly 80 ms, using four narrow agents:

```text
Transaction
    │
    ▼
Go Gateway ─────── authenticate, validate, establish the deadline
    │
    ▼
Go Orchestrator ── allocate budgets, fetch features, fan out
    │
    ├──► Rust Velocity ┐
    ├──► Rust Device   ├── deterministic
    ├──► Rust Geo      ┘
    └──► Behavioral SLM Agent ── bounded, capability-scoped, schema-validated
    │
    ▼
Rust Sentinel ──── normalise → weight → aggregate → apply policy
    │
    ▼
ALLOW / REVIEW / DECLINE + reason codes + decision lineage
```

Three design commitments shape everything else:

**A language model cannot decide.** It produces a bounded signal carrying at
most its configured weight. No type it can populate contains a `Decision`. A
successful prompt injection moves a score; it does not authorise a payment.
([ADR-004](docs/adr/ADR-004-deterministic-decision-authority.md))

**An agent holds no credentials.** It talks to one capability broker that
exposes verbs, not resources, scoped to a single evaluation, denied by default
and fully audited. A compromised agent does not become a compromised platform.
([ADR-005](docs/adr/ADR-005-capability-based-agent-access.md))

**80 ms is a deadline, not an allowance.** It is established once and divided
as the request descends. Nothing returns late, and budget exhaustion is an
explicit, observable outcome.
([deadline model](docs/deadline-model.md))

## Documentation

| Document | What it settles |
|---|---|
| [Architecture](docs/architecture.md) | Components, language responsibilities, request flow, deployment shape |
| [Domain model](docs/domain-model.md) | Terminology, agents, features, pseudonymisation |
| [Decision model](docs/decision-model.md) | Scoring, weighting, thresholds, reason codes, lineage |
| [Deadline model](docs/deadline-model.md) | Budget allocation, propagation, breakers vs deadlines |
| [Failure model](docs/failure-model.md) | Defined behaviour for every dependency failure |
| [Capability model](docs/capability-model.md) | What an agent may do, and how it is enforced |
| [Security boundaries](docs/security-boundaries.md) | Trust zones, data handling, regulatory positioning |
| [Threat model](docs/threat-model.md) | Thirteen threats with mitigation, detection and residual risk |
| [ADRs](docs/adr/README.md) | Sixteen decisions with alternatives and trade-offs |
| [Project plan](docs/project-plan.md) | The full specification and phase plan |

## Repository layout

```text
gateway/          Zone 1 · Go · authn, validation, rate limiting, deadline origin
control-plane/    Zone 2 · Go
  orchestrator/   budgets, fan-out, breakers, assembly, feature fetch
data-plane/       Zone 3 · Rust · four deployed processes
  sentinel/       aggregation, policy, decision authority
  engines/        velocity/ device/ geo/ — one process each
crates/           Rust · shared libraries
  common/         value types with enforced invariants
  engine/         feature reading and rule assessment
  platform/       config and process lifecycle
  proto/          generated bindings (built by cargo, not committed)
internal/         Go · configuration, process lifecycle, pseudonymisation
protocol/
  protobuf/       authoritative .proto contracts
  gen/go/         generated bindings (committed; CI verifies they match)
test/e2e/         cross-service suite: starts the real binaries
docs/             architecture documentation and ADRs
```

Component-private code lives under `<component>/internal/`, which makes a
cross-component import of private code a compile error ([ADR-015](docs/adr/ADR-015-component-private-packages.md)).

Directories for later phases are absent until they contain something. An empty
directory that promises work is worse than no directory.

## Building

Requires Go 1.24+, Rust 1.88+ (edition 2024), and [buf](https://buf.build) for
contract work.

```bash
make check          # everything below
make proto-lint     # buf lint + buf build
make proto-verify   # regenerating produces no diff
make go-test        # go vet + go test
make rust-test      # cargo fmt --check + clippy -D warnings + cargo test
make e2e-test       # builds the binaries, then runs the cross-service suite
```

The repository builds without buf installed: generated bindings are committed.

The cross-service suite is behind a build tag, so `go test ./...` stays a fast
unit run. It starts a sentinel, three engines, an orchestrator, a gateway and an
in-process Redis on ephemeral ports, and skips with an instruction when the Rust
binaries have not been built.

## Phases

| Phase | Contents | Status |
|---|---|---|
| 0 | Architecture, contracts, threat model, ADRs | **Complete** |
| 1 | Deterministic pipeline, feature store, pseudonymisation, cross-service tests | **Complete** |
| 2 | Behavioural SLM agent, structured output, model governance | Not started |
| 3 | Capability runtime and enforcement tests | Not started |
| 4 | Kubernetes, zero trust, observability, autoscaling | Not started |
| 5 | Fraud simulator, scenario DSL, evaluation, replay, shadow mode | Not started |
| 6 | Failure injection, adversarial scenarios, resilience measurement | Not started |
| 7 | Benchmarks, results, portfolio release | Not started |

Phase 1 delivers a system that is useful **without any AI**. That is a
requirement, not a milestone, and it is now met.

What Phase 1 deliberately does **not** include: any measurement of latency or
detection quality, a writer for the feature store, a local cache tier for store
outages, or transport security. Each belongs to a later phase and none is
claimed here.

## What this project does not claim

It is not GDPR, PCI DSS, DORA or EU AI Act compliant, and local inference,
pseudonymisation, Kubernetes and encryption do not make it so. It does not claim
low latency, high availability, zero trust or AI-powered fraud detection.

Those claims require, respectively: a latency distribution, a measured recovery,
a threat model with tests, and a precision/recall analysis including false
positives. Where this repository has them, it will show them. Where it does not,
it says so.