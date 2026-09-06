# Legion

## A Self-Hosted, Zero-Trust, Hybrid AI Risk-Decisioning Platform for Real-Time Financial Transactions

**License:** MIT
**Primary Languages:** Go + Rust
**Primary Deployment:** Kubernetes on Hetzner
**Inference:** Self-hosted, model-agnostic SLMs
**Initial Domain:** Payment fraud/risk
**Future Domain:** Insurance claims and other structured risk domains

---

# 1. Executive Summary

Legion is an open-source, self-hosted risk-decisioning platform designed for real-time financial authorization and fraud detection.

The platform orchestrates a swarm of small, specialized risk agents operating inside a tightly controlled Kubernetes environment. Agents are deliberately narrow in scope and receive only the data and capabilities required to perform their specific task.

Legion combines:

* deterministic, high-performance Rust risk computation;
* Go-based API and orchestration services;
* self-hosted, domain-targeted Small Language Models (SLMs);
* real-time feature enrichment;
* deterministic policy and aggregation;
* strict request deadlines;
* zero-trust workload isolation;
* capability-based data access;
* model and policy versioning;
* complete decision lineage;
* deterministic fallback behavior;
* reproducible fraud simulation and evaluation.

The system is explicitly designed around the principle:

> **AI generates bounded risk signals; deterministic policy determines the financial decision.**

The LLM is therefore not the ultimate authority for an authorization decision.

---

# 2. Project Motivation

Legion is primarily an engineering and research project designed to explore how AI can be safely integrated into latency-sensitive financial risk systems.

The project demonstrates practical understanding of:

* payment fraud detection;
* real-time risk scoring;
* distributed systems;
* low-latency architecture;
* Go concurrency and service development;
* Rust performance engineering;
* Kubernetes;
* zero-trust security;
* model serving;
* structured LLM inference;
* observability;
* resilience engineering;
* model governance;
* synthetic fraud simulation;
* quantitative evaluation.

The goal is not to build a production banking platform.

The goal is to build a **credible, technically rigorous miniature of the engineering problems encountered when introducing AI into financial risk infrastructure**.

---

# 3. Problem Statement

Real-time financial authorization creates several competing requirements.

## 3.1 Latency vs. Reasoning

Traditional deterministic rules are extremely fast and predictable but struggle with subtle behavioral patterns.

General-purpose LLM pipelines provide contextual reasoning but can introduce:

* inference latency;
* queueing latency;
* compute cost;
* nondeterministic execution time;
* malformed output;
* model availability risks.

Legion therefore uses AI selectively rather than universally.

---

## 3.2 Security and Data Minimization

Financial transaction data can contain highly sensitive information.

Legion therefore follows a self-hosted processing model in which sensitive transaction information remains inside infrastructure controlled by the deploying organization.

Agents do not receive unrestricted access to internal infrastructure.

They interact with resources through an explicit capability boundary.

---

## 3.3 AI Reliability

LLM output cannot be treated as inherently authoritative.

Legion therefore separates:

**AI signal generation**

from

**financial decision authority**.

The final authorization decision is produced by deterministic policy logic.

---

## 3.4 Auditability

Every risk decision should be explainable through structured evidence.

A decision should be traceable to:

* input identifiers;
* feature values;
* agent versions;
* model versions;
* prompt versions;
* policy versions;
* reason codes;
* risk scores;
* fallback state;
* execution timing.

---

# 4. Core Architectural Principles

## Principle 1 — Go Owns Orchestration

Go is responsible for:

* API gateways;
* gRPC endpoints;
* request routing;
* request validation;
* authentication/authorization;
* deadline propagation;
* rate limiting;
* feature-store access;
* agent orchestration;
* fan-out/fan-in;
* resilience mechanisms;
* service lifecycle;
* health checks;
* infrastructure-oriented workers.

---

## Principle 2 — Rust Owns Computation

Rust is responsible for:

* deterministic risk computation;
* velocity analysis;
* device analysis;
* geo-spatial analysis;
* feature transformations;
* scoring;
* deterministic aggregation;
* latency-sensitive data processing.

Rust components should initially use safe Rust.

`unsafe` code must not be introduced merely for performance aesthetics.

Any non-trivial optimization must be justified through profiling or benchmarking.

---

## Principle 3 — AI Produces Signals, Not Decisions

LLM agents produce structured risk evidence.

They do not directly authorize, review, or decline a transaction.

Example:

```text
Behavioral Agent
       │
       ▼
BehaviorScore = 78
       │
       ▼
Deterministic Aggregator
       │
       ▼
Policy Engine
       │
       ├── ALLOW
       ├── REVIEW
       └── DECLINE
```

---

## Principle 4 — Agents Are Capability-Bounded

An agent must never receive unrestricted infrastructure access.

An agent cannot directly:

* access Redis;
* query arbitrary databases;
* access the public Internet;
* access arbitrary Kubernetes services;
* retrieve unrestricted transaction history;
* access unrelated secrets;
* invoke arbitrary APIs.

Instead, the agent communicates through a predefined harness/capability layer.

---

## Principle 5 — Contracts Are Language-Neutral

Protobuf/gRPC contracts are the authoritative inter-service contracts.

Go and Rust implementations must consume generated types from the same schemas.

Neither language may independently redefine the wire contract.

---

## Principle 6 — Measure Before Optimizing

Legion must not equate:

* Rust with zero latency;
* Go with zero allocation;
* Native binaries with automatic performance;
* asynchronous code with automatic scalability.

Performance claims must be supported by benchmark data.

---

# 5. High-Level Architecture

```text
                         Payment Transaction
                                │
                                ▼
                    ┌───────────────────────┐
                    │      Go Gateway       │
                    │                       │
                    │ HTTP/gRPC             │
                    │ Auth                  │
                    │ Validation            │
                    │ Rate Limiting         │
                    │ Deadline Propagation  │
                    └───────────┬───────────┘
                                │
                                ▼
                    ┌───────────────────────┐
                    │   Go Orchestrator     │
                    │                       │
                    │ Feature Retrieval     │
                    │ Agent Coordination    │
                    │ Fan-out / Fan-in      │
                    │ Resilience             │
                    └───────────┬───────────┘
                                │
                ┌───────────────┼────────────────┐
                │               │                │
                ▼               ▼                ▼
        ┌──────────────┐ ┌──────────────┐ ┌───────────────┐
        │ Rust Velocity│ │ Rust Device │ │ Rust Geo      │
        │ Engine       │ │ Engine      │ │ Engine        │
        └──────────────┘ └──────────────┘ └───────────────┘
                │               │                │
                └───────────────┼────────────────┘
                                │
                                ▼
                     ┌────────────────────┐
                     │ Behavioral Agent   │
                     │                    │
                     │ SLM / vLLM         │
                     │ Structured Output  │
                     └──────────┬─────────┘
                                │
                                ▼
                     ┌────────────────────┐
                     │ Rust Risk Engine   │
                     │                    │
                     │ Normalization      │
                     │ Aggregation        │
                     │ Policy Evaluation  │
                     └──────────┬─────────┘
                                │
                                ▼
                         Risk Decision
                                │
                   ┌────────────┼────────────┐
                   ▼            ▼            ▼
                 ALLOW        REVIEW       DECLINE
```

The initial implementation should avoid unnecessarily splitting every deterministic component into a separate network service.

The preferred architecture is one Rust data-plane process containing multiple specialized computational engines.

Service decomposition may be introduced later if benchmarks or operational requirements justify it.

---

# 6. Agent Model

Legion uses specialized agents rather than a single generalized fraud agent.

## Initial Agents

### Velocity Agent

**Implementation:** Rust

Evaluates:

* transaction frequency;
* burst behavior;
* rolling transaction counts;
* account velocity;
* amount velocity;
* short-window anomalies.

Example signals:

```text
VELOCITY_SPIKE
RAPID_TRANSACTION_SEQUENCE
HIGH_AMOUNT_VELOCITY
```

---

### Device Agent

**Implementation:** Rust

Evaluates:

* device fingerprint changes;
* known device consistency;
* device/account relationships;
* device reuse patterns;
* suspicious device transitions.

Example signals:

```text
NEW_DEVICE
DEVICE_ACCOUNT_MISMATCH
DEVICE_REUSE_ANOMALY
```

---

### Geo Agent

**Implementation:** Rust

Evaluates:

* country changes;
* impossible travel;
* geographic distance;
* temporal feasibility;
* IP/location inconsistencies.

Example signals:

```text
IMPOSSIBLE_TRAVEL
COUNTRY_CHANGE
GEO_VELOCITY_ANOMALY
```

---

### Behavioral Agent

**Implementation:** Model-agnostic SLM runtime

Evaluates:

* contextual transaction behavior;
* interaction between multiple signals;
* unusual behavioral combinations;
* historical profile deviations.

The model receives sanitized structured evidence rather than unrestricted access to transaction infrastructure.

The output must conform to a strict structured schema.

Example:

```json
{
  "risk_score": 78,
  "confidence_score": 0.91,
  "reason_codes": [
    "BEHAVIORAL_DEVIATION",
    "UNUSUAL_AMOUNT_PATTERN"
  ]
}
```

---

# 7. Agent Capability Protocol

Agents interact with the platform through a restricted capability interface.

Example capabilities:

```text
get_transaction()
get_account_features()
get_velocity_features()
get_device_features()
get_geo_features()
submit_evaluation()
```

The capability layer performs:

* authorization;
* data minimization;
* schema validation;
* audit logging;
* request correlation;
* timeout enforcement.

The long-term objective is to make the agent runtime unaware of underlying storage systems.

For example:

```text
Agent
  │
  │ get_account_features()
  ▼
Capability Harness
  │
  ▼
Feature Store
```

rather than:

```text
Agent
  │
  └── direct Redis connection
```

---

# 8. Request Deadline Model

The authorization request has a single end-to-end target:

> **P99 < 80ms**

The 80ms value represents the total request deadline, not an independent timeout granted to each component.

Example conceptual budget:

```text
Go Gateway             ~2ms
Feature retrieval      ~5ms
Rust deterministic     ~1ms
SLM inference         ~50ms
Aggregation            <1ms
Network/serialization  remaining budget
```

These are engineering budgets rather than guaranteed measurements.

Actual performance must be established through benchmarking.

The remaining request deadline must propagate through all downstream operations.

If the request arrives with:

```text
80ms remaining
```

and feature retrieval consumes:

```text
8ms
```

the behavioral inference request should receive approximately:

```text
72ms
```

minus the required orchestration overhead.

---

# 9. Failure and Degraded-Mode Architecture

The system must remain capable of producing a safe decision when AI inference fails.

Fallback conditions include:

* inference timeout;
* inference cancellation;
* unavailable model server;
* malformed model output;
* schema validation failure;
* capability failure;
* internal agent error.

Example:

```text
Transaction
    │
    ▼
Behavioral Agent
    │
    ├── success ────────────────┐
    │                           │
    └── timeout/failure         │
            │                   │
            ▼                   │
      Deterministic             │
      Fallback                  │
            │                   │
            └──────────┬────────┘
                       ▼
                 Risk Aggregator
```

The **timeout/deadline mechanism is separate from the circuit breaker**.

The circuit breaker tracks repeated failures and can transition through:

```text
CLOSED
   ↓
OPEN
   ↓
HALF-OPEN
   ↓
CLOSED
```

This distinction must be preserved in the implementation.

---

# 10. Risk Scoring

Each risk component produces a normalized score:

```text
VelocityScore ∈ [0,100]

DeviceScore ∈ [0,100]

GeoScore ∈ [0,100]

BehaviorScore ∈ [0,100]
```

The initial weighted model may be:

```text
FinalScore =
    0.30 × VelocityScore
  + 0.25 × DeviceScore
  + 0.15 × GeoScore
  + 0.30 × BehaviorScore
```

Weights must be configurable rather than embedded throughout the codebase.

Initial decision thresholds:

```text
0–39    ALLOW
40–69   REVIEW
70–100  DECLINE
```

These thresholds are configuration, not permanent domain truth.

The project must demonstrate how changing the policy affects outcomes.

---

# 11. Reason Codes and Evidence

Risk decisions must use deterministic reason codes.

Example:

```text
VELOCITY_SPIKE
NEW_DEVICE
IMPOSSIBLE_TRAVEL
AMOUNT_OUTLIER
BEHAVIORAL_DEVIATION
```

Structured evidence should accompany each reason.

Example:

```json
{
  "code": "VELOCITY_SPIKE",
  "observed": 14,
  "threshold": 10,
  "window_seconds": 600
}
```

The system should never depend on an LLM-generated prose explanation as the authoritative explanation for a financial decision.

---

# 12. Feature Store

Redis or Dragonfly will provide real-time feature storage.

Initial features:

```text
tx_count_10m
tx_count_1h
avg_amount_30d
max_amount_30d
device_count_30d
country_count_30d
last_transaction_timestamp
last_country
last_device
```

The feature store must support actual rolling-window semantics rather than merely storing static counters.

The implementation must document:

* key design;
* TTL strategy;
* sorted sets/counters where appropriate;
* update semantics;
* consistency assumptions;
* expiration behavior;
* failure behavior.

Feature lookup latency is a benchmark target, not a guaranteed sub-millisecond property.

---

# 13. Pseudonymization and Sensitive Data

Sensitive identifiers must be pseudonymized before being sent to agents that do not require their original representation.

Use HMAC-SHA-256 for persistent pseudonymous identifiers where appropriate.

Do not use unsalted SHA-256 as a substitute for proper pseudonymization.

Secrets must never be:

* committed to Git;
* embedded in binaries;
* hard-coded in source;
* exposed through logs.

Pseudonymization must not be represented as automatically eliminating GDPR obligations.

The project documentation should distinguish between:

* encryption;
* hashing;
* pseudonymization;
* anonymization;
* access control;
* data minimization.

---

# 14. LLM / SLM Architecture

The behavioral reasoning layer must remain model-agnostic.

Candidate models may include small, locally deployable instruction models in the approximately 1B–3B parameter class.

The exact model should be selected based on:

* latency;
* accuracy;
* structured-output reliability;
* memory consumption;
* hardware requirements;
* domain evaluation;
* licensing suitability.

Inference should be performed locally through a dedicated inference runtime such as vLLM where appropriate.

The system must not hard-code business logic around a particular model.

---

# 15. Structured Model Output

The model output must be constrained through schema validation / guided decoding where supported.

The distinction must remain explicit:

```text
Transport:
    Protobuf over gRPC

Model output constraint:
    JSON Schema / structured decoding
```

JSON Schema must not cause internal gRPC payloads to be serialized as JSON.

The behavioral agent output must be validated before entering the deterministic aggregation pipeline.

---

# 16. Model Governance

Every deployed model must have identifiable metadata:

```text
model_name
model_version
model_checksum
quantization
runtime_version
prompt_version
schema_version
evaluation_dataset_version
```

A risk decision should be reproducible against the corresponding:

```text
Model
+
Prompt
+
Agent version
+
Feature version
+
Policy version
+
Schema version
```

---

# 17. Prompt Governance

Prompts are versioned artifacts.

A behavioral decision must identify:

```text
prompt_version
model_version
agent_version
schema_version
```

Prompt changes should be evaluated against the same benchmark suite as model changes.

---

# 18. Synthetic Fraud Simulation

Legion will include a reproducible synthetic transaction generator.

The generator must support:

* deterministic seeds;
* configurable TPS;
* configurable account populations;
* configurable fraud rates;
* configurable anomaly frequency;
* scenario selection;
* reproducible transaction streams.

Initial fraud scenarios:

### Card Testing

Rapid low-value authorization attempts.

### Velocity Attack

Large transaction bursts against an account/card/device.

### Account Takeover

New device + new geography + behavioral deviation.

### Impossible Travel

Geographically impossible transaction transitions.

### Amount Outlier

Transaction amount significantly outside the historical baseline.

### Coordinated Accounts

Multiple accounts exhibiting correlated suspicious behavior.

### Low-and-Slow Fraud

Subtle activity designed to remain below simple velocity thresholds.

---

# 19. Fraud Scenario DSL

The simulator should eventually support scenario definitions such as:

```yaml
scenario:
  name: account_takeover

  population:
    accounts: 10000

  attack:
    start_after: 10m
    device_change: true
    geo_change: true
    velocity_multiplier: 5
    amount_multiplier: 3
```

This allows the evaluation framework to reproduce the same attack conditions across different engine versions.

---

# 20. Evaluation Framework

Accuracy must not be demonstrated using a single generic "accuracy percentage."

The evaluation framework must report:

```text
Precision
Recall
F1
False Positive Rate
False Negative Rate
Detection Rate
Decision Latency
Detection Latency
```

False positives must receive particular attention because incorrectly blocking legitimate transactions has significant customer and business impact.

---

# 21. Shadow Mode

Legion will support shadow evaluation.

A production-like transaction stream can be evaluated without allowing Legion to affect the authoritative decision.

Example:

```text
                 Transaction
                      │
             ┌────────┴────────┐
             ▼                 ▼
      Production Decision   Legion
                                │
                                ▼
                         Shadow Decision
                                │
                                ▼
                          Comparison
```

The system should report:

* decision disagreement;
* score differences;
* reason-code differences;
* latency;
* fallback frequency.

---

# 22. Replay Engine

Historical or synthetic transaction streams can be replayed against different versions of Legion.

Example:

```text
Dataset v12
     │
     ├── Legion v1.0
     ├── Legion v1.1
     └── Legion v1.2
```

This enables regression testing and model/policy comparison.

---

# 23. Adversarial Simulation

A later phase should introduce adaptive fraud simulation.

The simulator attempts to find transaction strategies that minimize risk scores while achieving attacker objectives.

The objective is to explore:

* evasion;
* threshold gaming;
* slow attacks;
* signal manipulation;
* multi-account coordination;
* feature poisoning assumptions.

This becomes an experimental component rather than a production attack mechanism.

---

# 24. Security Architecture

Legion will operate under a zero-trust model.

Kubernetes workloads should have:

* default-deny network policies;
* explicit service-to-service permissions;
* no unnecessary public egress;
* minimal container privileges;
* read-only filesystems where practical;
* minimal Linux capabilities;
* non-root execution;
* resource limits;
* secret isolation;
* workload identity where available.

The system should document its threat model and attack surface.

---

# 25. Threat Model

The project should explicitly consider threats such as:

```text
Malicious agent
Compromised agent
Compromised model runtime
Prompt injection
Data exfiltration
Unauthorized feature access
Credential theft
Lateral movement
Model manipulation
Malformed inference output
Denial of service
Feature poisoning
Replay attacks
```

The threat model should document:

```text
Asset
Threat
Attack Surface
Control
Residual Risk
```

---

# 26. Kubernetes Architecture

Initial deployment target:

**Hetzner Kubernetes environment**

The cluster should contain logically separated workloads for:

```text
Gateway
Orchestrator
Rust Risk Engine
Inference Runtime
Feature Store
Observability
Replay/Evaluation
```

The system should avoid excessive microservice decomposition.

A component becomes a separate service when there is a demonstrable:

* scaling requirement;
* security boundary;
* fault-isolation requirement;
* resource isolation requirement;
* deployment independence;
* architectural reason.

---

# 27. Autoscaling

KEDA may be used where queue-driven scaling is appropriate.

Scaling signals may include:

* inference queue depth;
* request rate;
* active requests;
* CPU;
* memory;
* GPU utilization where applicable.

Autoscaling decisions must be benchmarked.

Do not assume a single metric such as CPU utilization is sufficient for inference workloads.

---

# 28. Observability

All requests should carry correlation identifiers.

OpenTelemetry should provide:

```text
Transaction
   │
   ├── Gateway span
   ├── Orchestrator span
   ├── Feature Store span
   ├── Rust Engine span
   ├── Behavioral Agent span
   ├── Inference span
   └── Aggregation span
```

Important metrics:

```text
P50 latency
P95 latency
P99 latency
P99.9 latency
TPS
fallback rate
model timeout rate
model error rate
feature lookup latency
agent execution latency
queue depth
CPU
memory
```

Logs must avoid leaking sensitive transaction data.

---

# 29. Decision Lineage

Every decision should be reconstructable through structured metadata.

Example:

```text
decision_id
transaction_id_hash
timestamp
agent_versions
model_version
prompt_version
feature_version
policy_version
risk_scores
reason_codes
fallback_state
execution_times
final_decision
```

The project should demonstrate how this lineage can be used during an investigation.

---

# 30. Failure Injection

The project should intentionally simulate:

* Redis unavailable;
* Redis slow;
* inference unavailable;
* inference slow;
* malformed inference response;
* agent crash;
* network partition;
* overloaded inference queue;
* invalid transaction;
* capability denial.

The system should demonstrate safe degraded behavior.

---

# 31. Benchmarking

Benchmarking is a first-class project component.

Measure:

### Application Performance

```text
TPS
P50
P95
P99
P99.9
```

### Resource Consumption

```text
CPU
RAM
allocations/request
GC behavior where applicable
container footprint
```

### Infrastructure

```text
network overhead
serialization overhead
feature-store latency
inference queueing
model inference time
```

### Decision Quality

```text
Precision
Recall
F1
FPR
FNR
```

---

# 32. Comparative Language Benchmarks

Where technically useful, benchmark equivalent components implemented in Go and Rust.

The purpose is not to prove that one language is universally superior.

The purpose is to understand:

* runtime behavior;
* latency;
* allocations;
* concurrency;
* memory usage;
* operational complexity.

Benchmark results must determine architectural decisions rather than preconceived language preferences.

---

# 33. Project Structure

The initial repository should evolve toward:

```text
Legion/
│
├── gateway/                 Zone 1 · Go
│
├── control-plane/            Zone 2 · Go
│   ├── orchestrator/
│   └── capability/
│
├── data-plane/               Zone 3 · Rust
│   ├── sentinel/
│   └── engines/
│       ├── velocity/
│       ├── device/
│       └── geo/
│
├── agents/                   Zone 4
│   └── behavioral/
│
├── crates/                   Rust · shared libraries
│   ├── common/
│   ├── platform/
│   └── proto/
│
├── internal/platform/        Go · shared plumbing
│
├── protocol/
│   ├── protobuf/
│   └── gen/go/
│
├── policy/                   versioned decision input
├── model-registry/           versioned model, prompt and schema artefacts
│
├── test/                     cross-service suites only
│
├── tools/
│   ├── simulator/
│   ├── replay/
│   ├── evaluation/
│   └── benchmarks/
│
├── deploy/
│   ├── docker/
│   ├── kubernetes/
│   ├── helm/
│   ├── terraform/
│   └── observability/
│
├── docs/
│   └── adr/
│
├── Cargo.toml                Rust workspace root
├── go.mod
└── README.md
```

The organising rules, in order of precedence:

1. **Zone directories hold deployables and nothing else.** A directory below the
   root is a zone; a directory inside one is a service that ships. `gateway/` is
   the exception and stays flat, because Zone 1 holds exactly one component
   permanently.
2. **Libraries live outside the zones.** `crates/` is shared Rust that deploys
   nowhere; `internal/` is the Go equivalent, placed where the Go compiler
   requires it. A library inside a zone would make every consumer appear to
   depend on that zone.
3. **Group by kind, not by name.** Services, deployment assets (`deploy/`),
   development tooling (`tools/`), cross-service tests (`test/`) and versioned
   data (`policy/`, `model-registry/`) are five different things and do not
   belong side by side at the root.
4. **One home per concern.** There is no `security/` directory: the threat model
   and trust boundaries live in `docs/`, and a second home guarantees drift.
   There is no `feature-store/` directory either — Redis is a dependency, not
   code, and its client belongs to the orchestrator, the only component holding
   the credential. There is no `agents/inference/`: the inference runtime is a
   self-hosted model server, so it is an image in `deploy/` and an artefact
   reference in `model-registry/`, not source.
5. **Component-private code lives under `<component>/internal/`** (ADR-015).
   Shared code is promoted into `internal/platform/` only once a second consumer
   exists.

`test/` holds only suites that span process boundaries — the shadow, replay,
failure-injection and adversarial scenarios of Phases 5 and 6, which start
several services and assert on a decision and its lineage. Unit tests stay where
their language puts them: beside the package in Go, inside the crate in Rust.
The distinction matters because the failure model states that a documented
failure mode without a test is an assumption, and those tests cannot live in any
single package.

This is the target architecture, not a requirement to implement every directory in the first iteration. Directories arrive when they have contents; an empty directory that promises work is worse than no directory.

---

# 34. Development Phases

## Phase 0 — Architecture & Contracts

Establish:

* domain terminology;
* architecture;
* Protobuf contracts;
* agent capability model;
* security boundaries;
* threat model;
* decision model;
* repository structure.

**No Kubernetes or LLM infrastructure is required yet.**

---

## Phase 1 — Core Risk Engine

Implement:

* Go gateway;
* Go orchestrator;
* Rust deterministic risk engine;
* Protobuf contracts;
* Redis/Dragonfly integration;
* pseudonymization;
* deterministic risk rules;
* fallback engine;
* synthetic transaction generator;
* unit/integration tests.

Target:

**A complete transaction → risk decision pipeline without LLM inference.**

---

## Phase 2 — Behavioral SLM Agent

Implement:

* model runtime;
* behavioral agent;
* structured model output;
* model timeout;
* model fallback;
* prompt versioning;
* model metadata;
* behavioral evaluation.

Target:

**Hybrid deterministic + AI risk scoring.**

---

## Phase 3 — Agent Capability Runtime

Implement:

* restricted capability protocol;
* capability authorization;
* agent isolation;
* resource mediation;
* capability audit trail;
* denial handling.

Target:

**Agents can operate without direct access to infrastructure resources.**

---

## Phase 4 — Kubernetes & Zero-Trust Deployment

Implement:

* Kubernetes manifests;
* Helm;
* network policies;
* workload isolation;
* secret management;
* resource limits;
* health probes;
* KEDA;
* observability;
* failure injection.

Target:

**Self-hosted, isolated deployment on Hetzner.**

---

## Phase 5 — Evaluation & Replay

Implement:

* fraud scenario DSL;
* replay engine;
* shadow mode;
* benchmark suite;
* statistical evaluation;
* model comparison;
* policy comparison.

Target:

**Reproducible evidence that the system works.**

---

## Phase 6 — Adversarial & Resilience Engineering

Implement:

* adaptive fraud scenarios;
* chaos/failure testing;
* model degradation tests;
* latency attacks;
* queue saturation;
* capability abuse tests;
* recovery testing.

Target:

**Demonstrate that Legion remains safe under hostile and degraded conditions.**

---

## Phase 7 — Portfolio Release

Produce:

* architecture diagrams;
* sequence diagrams;
* threat model;
* performance reports;
* fraud evaluation reports;
* model evaluation reports;
* operational documentation;
* deployment documentation;
* security documentation;
* engineering decision records;
* benchmark methodology;
* known limitations;
* "What Didn't Work" documentation.

Release under:

**MIT License**

---

# 35. Acceptance Criteria

Legion should not be considered complete merely because the services compile.

The project should eventually demonstrate:

### Functional

* transaction ingestion;
* feature enrichment;
* deterministic agents;
* behavioral SLM agent;
* risk aggregation;
* ALLOW/REVIEW/DECLINE decisions;
* fallback behavior;
* reason codes.

### Performance

* measured P50/P95/P99/P99.9 latency;
* measured throughput;
* measured resource utilization;
* documented latency budget.

### Reliability

* inference timeout fallback;
* inference failure fallback;
* feature-store failure behavior;
* agent failure behavior;
* graceful degradation.

### Security

* default-deny networking;
* restricted capabilities;
* no unnecessary public egress;
* secrets protected;
* non-root workloads;
* threat model documented.

### AI Governance

* model versioning;
* prompt versioning;
* policy versioning;
* structured outputs;
* reproducible decisions;
* shadow evaluation.

### Fraud Detection

* reproducible scenarios;
* precision/recall/F1;
* false-positive analysis;
* replay testing;
* adversarial evaluation.

---

# 36. Testing Strategy

Testing must include:

### Unit Tests

Rust:

* deterministic rules;
* scoring;
* feature transformations;
* reason-code generation.

Go:

* orchestration;
* deadlines;
* capability routing;
* error handling.

### Integration Tests

* Go ↔ Rust;
* Redis;
* Protobuf;
* inference runtime;
* capability protocol.

### Contract Tests

Ensure Go and Rust implementations remain compatible with the authoritative Protobuf contracts.

### Failure Tests

Explicitly test:

* timeout;
* cancellation;
* malformed responses;
* unavailable dependencies;
* partial agent failure.

### Load Tests

Use realistic synthetic transaction distributions.

### Security Tests

Validate:

* network isolation;
* capability restrictions;
* secret handling;
* unauthorized resource access.

---

# 37. Code Quality Requirements

Do not optimize prematurely.

Avoid allocations that are demonstrably unnecessary in hot paths.

Use efficient data structures appropriate to the workload.

Use Rust's ownership model rather than fighting it.

Use Go's concurrency primitives idiomatically.

Do not use `Span<T>`, unsafe Rust, manual memory management, lock-free structures, or complex concurrency patterns merely to appear high-performance.

Every significant optimization should have a benchmark or profiling result supporting it.

---

# 38. Regulatory Positioning

Legion is an engineering demonstration and must not claim to automatically provide regulatory compliance.

The documentation should instead demonstrate architectural consideration of areas relevant to:

* GDPR;
* DORA;
* PCI DSS;
* EU AI governance;
* data minimization;
* auditability;
* resilience;
* third-party/model risk.

Statements must distinguish between:

**technical controls**

and

**legal/regulatory compliance obligations**.

The project should never claim that hashing data, using Kubernetes, or running an LLM locally automatically makes a deployment compliant.

---

# 39. Portfolio Positioning

The README should make the project's purpose immediately understandable.

The opening should communicate:

> **Legion is an open-source, self-hosted AI risk-decisioning platform that combines deterministic Rust risk engines, Go orchestration, and locally hosted small language models to evaluate financial transactions under strict latency, security, and reliability constraints.**

The README should immediately expose:

* architecture;
* performance;
* fraud detection results;
* security model;
* model strategy;
* deployment;
* benchmark results.

---

# 40. Engineering Decision Records

Important architectural decisions should be documented.

Examples:

```text
ADR-001: Why Go for orchestration?
ADR-002: Why Rust for deterministic risk computation?
ADR-003: Why gRPC/Protobuf?
ADR-004: Why capability-mediated agent access?
ADR-005: Why local SLM inference?
ADR-006: Why deterministic decision authority?
ADR-007: Why consolidated Rust data-plane agents?
ADR-008: Why Redis/Dragonfly?
ADR-009: Why Kubernetes?
ADR-010: Why the 80ms deadline?
```

Each ADR should document:

```text
Context
Options Considered
Decision
Trade-offs
Consequences
Benchmark Evidence
```

---

# 41. What Legion Is Not

Legion is not intended to be:

* a drop-in banking fraud platform;
* a replacement for established fraud vendors;
* a production-certified payment authorization system;
* a claim of regulatory compliance;
* an autonomous LLM making unrestricted financial decisions;
* a demonstration of technology for technology's sake.

It is a **serious engineering exploration of how AI-assisted risk decisioning can be designed under financial-system constraints**.

---

# 42. Definition of Success

The ultimate success criterion is not the number of Kubernetes pods, programming languages, models, or lines of code.

Legion succeeds when it demonstrates:

> **A transaction can enter an isolated environment, be enriched with controlled data, evaluated by specialized deterministic and AI-powered agents, produce an auditable risk signal, survive AI and infrastructure failures, and produce a reproducible deterministic decision—all while operating within a measurable low-latency budget.**

The project should prioritize **evidence over claims**.

Every important architectural claim should eventually be supported by:

* benchmarks;
* tests;
* traces;
* failure experiments;
* evaluation datasets;
* documented trade-offs.

---

# 43. Long-Term Extension: Insurance Claims

The initial implementation targets payment fraud.

The underlying architecture should remain sufficiently domain-oriented to eventually support other risk domains.

Future example:

```text
                    Legion
                         │
              ┌──────────┴──────────┐
              ▼                     ▼
          Payments              Insurance
              │                     │
       Fraud Agents          Claim Agents
       Device Agents         Document Agents
       Velocity Agents       Policy Agents
       Behavioral Agents     Anomaly Agents
```

The insurance domain must not be implemented until the payment-risk architecture has reached a mature baseline.

---

# 44. Final Architectural Philosophy

Legion follows five core rules:

```text
1. Deterministic code owns financial decisions.

2. AI produces bounded risk signals.

3. Agents receive capabilities, not unrestricted infrastructure access.

4. Go orchestrates; Rust computes.

5. Every important claim is backed by measurable evidence.
```

The project should deliberately favor **simple, explainable, benchmarked engineering over unnecessary complexity**.

The goal is not to build the largest possible system.

The goal is to build a system sophisticated enough that an experienced financial-services engineer can inspect it and say:

> **"This person understands the constraints."**
