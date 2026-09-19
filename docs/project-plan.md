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

**Where these signals go is decided by ADR-020**, which postdates this
section. This list is the *what*; the ADR is the *where*, and it overrides
this section wherever the two differ. In particular: metrics reach Prometheus
by OTLP push to a collector rather than by being scraped, logs and traces are
stored in Elasticsearch and read in Kibana, the decision path is sampled
while the investigation path is not, and Zone 3 emits nothing at all.

**Partially built.** Of the metrics listed below, the latency percentiles are
now instrumented (`legion.decision.duration` end to end at the gateway, plus
per-stage and per-agent histograms in the orchestrator, with ADR-009's 80 ms
deadline as an explicit bucket boundary). Fallback rate, model timeout and
error rates, feature-lookup latency, queue depth, CPU and memory are not.
The OpenTelemetry span tree below is now built for the decision path and the
Redis Streams hops it crosses into the investigation plane: gRPC unary
interceptors start and propagate spans, casetrigger and the task queue carry
the same context as extra fields, and every service exports every span
(`AlwaysSample`) -- sampling is decided centrally by the collector's
`tail_sampling` processor (errors always kept, 10% of the rest, 100% of the
investigation path), not per process. Correlation identifiers are the span's
own `trace_id`/`span_id`, attached to log lines via the slog handler that has
existed since step 1. `deploy/observability/` (§28's build-order table above)
proves a live trace -- one gateway span, its error status intact -- reaches
Elasticsearch through that exact pipeline. What that local run does not
exercise is a live multi-hop trace across services together; gRPC and Redis
Streams context continuity are each proven by a unit test instead.

All requests should carry correlation identifiers.

OpenTelemetry should provide (unchanged aspirational list; what actually
exists today is a gRPC client span per outbound call, not this exact tree --
"Feature Store span" has no separate span of its own, for instance, since the
feature fetch happens inside the orchestrator's own server span rather than
over gRPC; "Rust Engine span" is real, one client span per agent call; the
behavioral agent, inference and aggregation spans await Phase 3, which is
when those calls first exist to have a span at all):

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

What is actually emitted today, which is the authoritative list because the
one above is a wish:

```text
legion.decision.duration          gateway, end to end, by outcome
legion.evaluation.duration        orchestrator total, by decision
legion.evaluation.stage.duration  by stage
legion.agent.duration             by agent, and signal vs failure

legion.lineage.entries            written | dropped | failed
legion.casetrigger.publications   published | dropped | failed
legion.investigation.tasks        completed | retrying | failed | dead_letter
legion.capability.checks          by verdict
legion.gateway.dedup.claims       new | replayed | conflict | in_flight
legion.breaker.transitions        open | closed
```

Latency instruments are in seconds, the base unit Prometheus specifies, with
ADR-009's 80 ms deadline as an explicit bucket boundary. Names carry no
`_total` suffix: the Prometheus exporter adds it, and spelling it here would
produce `_total_total`.

No metric carries an identifier as an attribute — not `transaction_id`,
`account_id`, `decision_id`, `case_id`, `investigation_id`, `task_id` or
`caller_id`. `agent_id` appears only on decision-path metrics, where the agent
set is small and fixed by configuration (ADR-014); Zone 6 anticipates
thousands of logical agents, so its metrics carry no agent attribute and
per-agent questions are answered from the `tasks` table instead.

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

Shipped initially as one denormalised table (`decision_lineage`); ADR-018's
normalised, queryable schema (`decisions`, `agent_evaluations`,
`execution_spans`, `decision_failures`, `governed_versions`) replaced it via
a real migration tool (`internal/platform/migrate`, golang-migrate), which
§45 (Zone 6) and future evaluation/replay work query directly rather than
deserialising a blob per row.

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
├── investigation/             Zone 6 · Go · async, off the 80ms budget (ADR-017)
│   ├── controller/
│   └── worker/
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

Restructured 2026-09-16 to fold in the investigation/case-management scope
of §45-51 below and the strengthened observability (§28) and failure-injection
(§30) content that motivated it. The restructuring swaps the original Phase 2
and Phase 3: the capability/tool runtime now exists before any model-backed
agent runs, deterministic and investigation-side alike, rather than after only
the behavioural agent's. See ADR-017 for why, and the "AFTER THAT" discussion
that preceded it — the source material's own milestone order (tool registry
before the first real agent, before the shared LLM) independently confirmed
the same conclusion.

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

**Status: Complete.**

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
* unit/integration tests;
* decision lineage, constructed and persisted (added after the initial
  Phase 1 pass; see ADR-018 for the schema this now targets).

Target:

**A complete transaction → risk decision pipeline without LLM inference.**

**Status: Complete.** Decision lineage ships to ADR-018's normalised schema
in `internal/lineage`, applied via a real migration tool
(`internal/platform/migrate`) as Phase 2 work; not a Phase 1 regression.

---

## Phase 2 — Capability Runtime, Task Infrastructure & Case Management

Restructured to absorb the original Phase 3 (capability runtime) and the
investigation/case-management scope of §45-49, sequenced *before* the
behavioural SLM agent (now Phase 3) so that the riskiest component — a
model-backed agent — never runs without the enforcement boundary already in
place (ADR-005, ADR-017).

Implement:

* transaction idempotency keys (ADR-019) — a prerequisite, not an
  afterthought: case creation in this same phase needs the same key to avoid
  duplicate cases;
* restricted capability protocol, capability authorization, agent isolation,
  resource mediation, capability audit trail, denial handling (the original
  Phase 3 scope, moved here);
* Postgres-backed agent registry (ADR-017), covering behavioural and
  investigation agents — the three deterministic engines stay on the static
  registry;
* case creation on `REVIEW` (Zone 6, ADR-017), asynchronous, off the 80 ms
  budget;
* the investigation task queue (Redis Streams, ADR-017) and the generic
  worker pool;
* a rule-based investigation controller (§48) and at least one real
  investigation agent (§45) exercised end to end, initially with a mocked
  inference call so the whole loop — case, task, tool call, evidence,
  finding, report — is proven before the shared model exists;
* evidence and finding persistence, kept structurally distinct from each
  other and from the decision itself (§16 of domain-model.md's investigation
  entities, added alongside this restructuring).

Target:

**Agents — deterministic, investigation-side, and eventually behavioural —
operate without direct access to infrastructure resources, and a `REVIEW`
decision produces a real, auditable case.**

**Status: Complete**, with gaps carried into later phases rather than hidden:
transaction idempotency (ADR-019), the capability runtime (ADR-005, one real
caller — `investigation/worker` — the fixed-`Capability`-enum path has none
yet), and the investigation plane (ADR-017: controller, generic worker, task
queue, two seeded mocked agents) all ship. Not done: a `RegisterAgent` API
(the manifest/registry are static Go configuration and Postgres seeding, not
a registration flow), mTLS-asserted `workload_id`, durable capability audit
storage, and any investigation agent's tool/model call being real rather
than mocked (Phase 3's shared inference runtime is what that needs).

---

## Phase 3 — Behavioral SLM Agent & Shared Inference

The original Phase 2 scope, now sequenced after Phase 2's capability runtime
exists, so the model-backed agent is capability-bounded from its first
invocation rather than retrofitted.

Implement:

* model runtime (self-hosted, ADR-008);
* behavioral agent;
* structured model output;
* model timeout;
* model fallback;
* prompt versioning;
* model metadata;
* behavioral evaluation;
* the shared inference runtime wired into the investigation workers from
  Phase 2, replacing the mocked call.

Target:

**Hybrid deterministic + AI risk scoring, and investigation agents backed by
the same shared model server.**

---

## Phase 4 — Kubernetes, Zero-Trust Deployment & Observability

**Phase 4 is being taken in two parts, and only the first is in progress.**
The observability half (ADR-020) plus the packaging ADR-016 already specifies
comes first, because every remaining gap carried forward from Phases 1 and 2
terminates in it: a silent lineage-queue drop can only be fixed by counting
it, a dead-lettered investigation task is invisible, and ADR-009's 80 ms
budget has never been measured. The cluster half — manifests, Helm, network
policies, KEDA — and ADR-011's mTLS/workload identity are deliberately
deferred, since neither can be verified without a cluster.

Progress within the first half:

| | Status |
|---|---|
| OpenTelemetry pipeline in the shared process lifecycle | Built |
| Counters for the previously silent failures | Built |
| Latency histograms, end to end and per stage and agent | Built |
| ADR-016 packaging: Dockerfiles, `deploy/services.yaml`, the buildable-vs-declared check | Built |
| `deploy/observability/`: collector, Prometheus, Grafana, Elasticsearch, Kibana, Filebeat | Built, verified: a running `gateway` container's counter was read back from Prometheus's own API, and its log line from Elasticsearch |
| Tracing and correlation identifiers | Built, verified for one hop: gRPC interceptors and Redis Streams field propagation carry a trace across the decision and investigation planes; a live single-hop trace (error status included) was read back from Elasticsearch through the collector's `tail_sampling` processor. Multi-hop continuity is unit-tested, not exercised live |
| Kubernetes manifests, Helm, network policies, KEDA | Deferred (cluster half) |

The observability half is otherwise complete. **Backlog, added after the
fact rather than part of the original plan**: a dashboard in Grafana and
alerting on top of it (and/or Kibana) -- every signal above is collectible
and was verified reaching its destination, but nothing renders it for a
human or pages anyone when it moves. Appended to the end of the backlog on
purpose: every other Phase 4 observability item existed as a named gap
before this document said so; this one didn't, and should not be read as if
it had been.

The local stack proves the pipeline works end to end; no real deployment runs
it continuously, so no figure from §28's list can be reported from a live
system yet. Zone 3 emits nothing by design (ADR-020).

Implement:

* Kubernetes manifests;
* Helm;
* network policies;
* workload isolation, extended to Zone 6;
* secret management;
* resource limits;
* health probes;
* KEDA, including worker autoscaling on investigation queue depth (§50);
* **observability**, to the full depth specified in §28 and restated in
  `investigation-model.md` §"Observability and cost": correlation identifiers
  on every request (`request_id`, `trace_id`, `transaction_id`, `case_id`,
  `investigation_id`, `task_id`, `agent_id`), structured JSON logs, the full
  metrics list in §28 extended with the task/queue/LLM/tool metrics in
  `investigation-model.md`, and OpenTelemetry spans covering the
  investigation path (controller → task → worker → tool → LLM → finding) the
  same way they cover the decision path.

Target:

**Self-hosted, isolated deployment on Hetzner, and an operator can trace any
transaction end to end through both the decision and the investigation it may
have opened.**

---

## Phase 5 — Evaluation & Replay

Implement:

* fraud scenario DSL;
* replay engine;
* shadow mode;
* benchmark suite;
* statistical evaluation;
* model comparison;
* policy comparison;
* investigation-quality evaluation (§58's "agent quality" category: correct
  evidence, correct interpretation, no fabricated evidence, valid structured
  output, appropriate tool usage), extended to the investigation agents added
  in Phase 2/3.

Target:

**Reproducible evidence that the system works, for both the decision and the
investigation it may open.**

---

## Phase 6 — Adversarial & Resilience Engineering

Implement, to the full depth specified in §30 and `failure-model.md`:

* adaptive fraud scenarios;
* chaos/failure testing, **including the investigation plane specifically**:
  worker crash mid-task, task-queue redelivery of an already-completed task,
  the investigation Redis instance unavailable or slow, database failure
  during case/evidence write, inference timeout and malformed response inside
  an investigation (as distinct from inside the real-time decision, which
  Phase 1 already covers), and a malformed or duplicate task message;
* model degradation tests;
* latency attacks;
* queue saturation, for both the deterministic decision path (already
  covered) and the investigation task queue (new);
* capability abuse tests, extended to investigation-side tool calls
  (unauthorised tool, arbitrary SQL, shell execution, arbitrary HTTP,
  cross-case data access — §60's security acceptance tests, folded in here
  rather than treated as a separate category);
* recovery testing.

Target:

**Demonstrate that Legion remains safe under hostile and degraded conditions,
across both the decision path and the investigation path.**

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
* benchmark methodology, including cost-per-investigation (§41's cost
  targets, restated in `investigation-model.md`);
* known limitations;
* "What Didn't Work" documentation.

The benchmark work has a starting list. `architecture.md` §9's "Scaling limits
that are reasoned, not measured" names four properties of the code that are
plausible limits under load and that nothing has yet observed — the serial,
unbatched lineage writer; the per-agent breaker mutex; the dedup key
allocation; and the request coalescing a feature cache will need once one
exists. Each says what is known and what is only inferred, so a benchmark can
confirm or dismiss them rather than rediscover them.

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
ADR-017: Why an investigation plane, why it is asynchronous, why its agent
          registry is Postgres-backed, and why its task queue is Redis
          Streams on a separate instance from the feature store?
ADR-018: Why normalise the lineage schema, and why supersede rather than edit it?
ADR-019: Why transaction idempotency keys, and why at the gateway?
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

Legion follows six core rules:

```text
1. Deterministic code owns financial decisions.

2. AI produces bounded risk signals.

3. Agents receive capabilities, not unrestricted infrastructure access.

4. Go orchestrates; Rust computes.

5. Every important claim is backed by measurable evidence.

6. Investigation is asynchronous and never borrows from the decision's budget.
```

The project should deliberately favor **simple, explainable, benchmarked engineering over unnecessary complexity**.

The goal is not to build the largest possible system.

The goal is to build a system sophisticated enough that an experienced financial-services engineer can inspect it and say:

> **"This person understands the constraints."**

---

# 45. Investigation Architecture

Added 2026-09-16 (ADR-017). A `REVIEW` decision is not the end of the
transaction's story; it is the point at which Legion hands off from
deterministic real-time scoring to asynchronous, evidence-based investigation.

```text
DecisionOutcome.decision == REVIEW
        │
        ▼  (async: lineage writer publishes CaseTrigger, ADR-017)
   Case created
        │
        ▼
   Investigation started
        │
        ▼
   Investigation Controller
        │
        │  signal analysis → agent selection (rule-based initially)
        ▼
   Task(s) created ──► Redis Streams (investigation.tasks, ADR-017)
        │
        ▼
   Generic Worker
        │
        │  load agent definition (ADR-017) → check tool authorization (ADR-005)
        │  → execute tool(s) → call shared inference (Phase 3) → validate output
        ▼
   Evidence + Agent Finding persisted
        │
        ▼
   Controller aggregates findings ──► additional agents if warranted
        │
        ▼
   Investigation Result / Case Summary
```

This is explicitly the same shape as the source material's investigation
loop (transaction → controller → task queue → worker → tool/LLM → findings →
evidence → result), translated to Go and to Legion's trust-zone model. The one
structural deviation, and the one that matters most, is that **nothing in this
diagram is on the caller's critical path.** `EvaluateTransactionResponse`
returns as soon as the sentinel answers; everything below the first arrow
happens after the caller has already received `REVIEW` and moved on.

## 45.1 Agent activation strategy

Not every registered investigation agent runs for every case. The initial
strategy is rule-based, mirroring the source material directly:

```text
if device_risk_signal > threshold:
    activate("device_investigation_agent")

if velocity_risk_signal > threshold:
    activate("velocity_investigation_agent")

if relationship_signal_present:
    activate("relationship_investigation_agent")
```

A case with unremarkable signals might activate one agent; a case with several
concurrent signals might activate five. No case activates every registered
agent — this is what makes "hundreds or thousands of logical agents" a
property the architecture can actually hold, rather than a claim that only
survives at small N. The controller does not decide the case; it decides what
gets *looked at*.

## 45.2 Task lifecycle

```text
PENDING ──► RUNNING ──► COMPLETED
               │
               ├──► FAILED        no retry can change the outcome
               │
               └──► RETRYING      another attempt may survive it
                       │
                       ├──► lease expiry redelivers ──► RUNNING
                       │
                       └──► attempts spent ──────────► DEAD_LETTER
```

The database (not the queue) is the source of truth for this state machine,
per ADR-017. A worker transitions a task to `RUNNING` in Postgres before doing
any work, so a crash between claiming a task and finishing it is visible and
recoverable rather than silently lost.

There is no separate retry queue: a `RETRYING` task's queue message is simply
never acknowledged, so the same lease recovery that covers a crashed worker
redelivers it. `WAITING` is in the `TaskStatus` enum but unbuilt — see
`investigation-model.md` §4.

## 45.3 What the controller is not

The investigation controller selects agents and aggregates findings. It has
no `Decision` type available to it, cannot alter `decisions.decision`, and its
"recommendation" is a field on the case a human or a downstream process may
act on — never an automatic re-authorization or reversal of the original
outcome. This is the same authority boundary ADR-004 draws for the sentinel,
restated for a component several steps further downstream.

---

# 46. Investigation Domain Objects

These extend the entities in `domain-model.md`, scoped to Zone 6 and
introduced by ADR-017:

```text
Case                — one per transaction that reached REVIEW (or was
                       explicitly escalated); tracks status and priority.
Investigation       — one attempt at investigating a Case; a case may have
                       more than one over time (re-opened, escalated).
Task                — one agent invocation within an investigation.
AgentDefinition      — a registered logical agent (ADR-017): prompt, allowed
                       tools, model policy, version, enabled.
ToolExecution        — one tool call a worker made on an agent's behalf, with
                       arguments, result, status and duration — complete
                       auditability of what a worker actually did.
Evidence             — a fact gathered during investigation, independent of
                       any agent's interpretation of it.
AgentFinding         — one agent's observation, hypothesis and confidence,
                       referencing the Evidence it is based on.
InvestigationAuditEvent — an append-only record of what happened, when, to
                       what, for the same reason `audit_events` exists in the
                       source material: TASK_CREATED, TASK_COMPLETED,
                       TOOL_CALLED, AGENT_COMPLETED, ANALYST_DECISION, etc.
```

The distinction the source material draws is adopted exactly as stated,
because it is correct and Legion has no better version of it:

```text
Evidence  ≠  Observation  ≠  Hypothesis  ≠  Final decision
```

Evidence is what was gathered. An observation is what an agent noticed in it.
A hypothesis is what an agent concluded from the observation. None of the
three is the decision, and the decision was already made, deterministically,
before any of this ran.

## 46.1 Case states

```text
OPEN → INVESTIGATING → WAITING → COMPLETED
                              → ESCALATED
                              → CLOSED
```

Case creation is idempotent on `(idempotency_key)` (ADR-019): the queue's own
at-least-once delivery (ADR-017) must not be able to open two cases for one
transaction.

---

# 47. Agent Registry (Postgres-backed)

ADR-017's schema, restated here alongside the other domain objects:

```text
agent_definitions
    agent_id, version, name, description, system_prompt,
    allowed_tools (jsonb), model_policy (jsonb), configuration (jsonb),
    enabled, created_at
    UNIQUE(agent_id, version)
```

Registering an agent does not require deploying anything: it is a row plus a
worker image capable of interpreting `agent_id`'s tool and model policy. The
three deterministic Rust engines are **not** in this table — see ADR-017 for
why they stay on the static, startup-parsed registry from Phase 1.

The rollout sequence for a new investigation agent mirrors the one ADR-014
already established for the real-time agents, applied here:

| # | Step | Affects investigation outcomes? |
|---|---|---|
| 1 | Register at `enabled = false` | No |
| 2 | Deploy the worker capability the agent needs (tools, model policy) | No |
| 3 | Enable in shadow: runs, findings recorded, not surfaced to analysts | No |
| 4 | Compare shadow findings against analyst-labelled outcomes | No |
| 5 | Enable for real | **Yes** |

---

# 48. Task Queue and Worker Model

ADR-017's mechanism, restated operationally:

```text
Investigation Controller
        │  publish
        ▼
  investigation.tasks  (Redis Streams, separate instance from the
                         feature store's Redis)
        │  consumer group
        ▼
  Generic Worker (any number, horizontally scaled on queue depth)
        │
        │  READ → CLAIM → LOAD AGENT → LOAD CONTEXT → EXECUTE TOOLS
        │  → CALL LLM (Phase 3+) → VALIDATE OUTPUT → STORE FINDINGS
        │  → MARK COMPLETE
        ▼
  Postgres (source of truth for task state)
```

A worker is generic: it does not know in advance which logical agent it will
execute next. It loads that from `agent_definitions` (§47) per task. This is
the mechanism that makes "many logical agents, few processes" true rather
than aspirational — the number of workers is an autoscaling decision (Phase 4,
KEDA on queue depth), independent of how many agents are registered.

---

# 49. Evidence, Findings and Investigation Result

An investigation's output is a report, not a decision:

```json
{
  "case_id": "uuid",
  "risk_signals": ["SHARED_DEVICE", "HIGH_VELOCITY"],
  "evidence_count": 8,
  "findings_count": 4,
  "investigation_status": "COMPLETED"
}
```

Findings reference the evidence they are based on (`evidence_ids`), never the
other way around — evidence must be able to exist and be queried without a
finding ever having been drawn from it, so an analyst can always ask "what was
actually observed" independent of what any agent concluded from it.

---

# 50. Observability and Cost (Phase 4 addendum)

Everything in this section is Phase 4 scope (§28, §34) — recorded here in
full because it originates from the source material and must not be lost
between now and Phase 4, not because it changes when Phase 4 happens.

**Correlation identifiers**, present on every investigation-path request:
`request_id`, `trace_id`, `transaction_id`, `case_id`, `investigation_id`,
`task_id`, `agent_id` — extending the decision-path tracing §28 already
specifies.

**Metrics**, additive to §28's list:

```text
cases_created_total
investigations_total

tasks_created_total
tasks_completed_total
tasks_failed_total
tasks_retried_total

queue_depth
worker_active_tasks

llm_requests_total
llm_latency_ms
llm_tokens_total

tool_calls_total
tool_latency_ms
```

**Cost is a first-class metric, not an afterthought.** The system must be
able to answer, per investigation:

```text
LLM tokens / investigation
LLM cost / investigation
compute cost / 1,000 transactions
database cost
worker cost
GPU utilisation
```

This is worth stating explicitly because Legion has never had a component
with a variable per-invocation cost before — the deterministic engines and
the sentinel cost a fixed amount of CPU time regardless of what they decide.
An investigation's cost depends on how many agents it activated and how much
each one made the model do, which is a genuinely new kind of thing to measure.

**"First-class" is not the same as "a metric", and ADR-020 splits this list
in two.** Aggregates — tokens by agent, request rates, GPU utilisation — are
metrics and go to Prometheus. Anything keyed by a single investigation is a
business record and goes to PostgreSQL beside the case, because a metric
store has short retention, aggregates by design, and cannot answer "what did
case X cost" months later. The phrasing above is retained because it came
from the source material; the destination is the ADR's.

---

# 51. Testing Taxonomy (adopted from the source material)

Extends §36 with the six-category taxonomy, because it is a clearer
organisation than §36's language/layer-based one and both are worth having:

```text
Functional      — transaction processing, scoring, decision, case creation,
                   agent execution, tool execution, investigation completion.

Reliability     — retry, timeout, worker crash, duplicate task, database
                   failure, LLM failure. (Phase 6, folded into §30.)

Security        — unauthorized tool, invalid agent permission, invalid API
                   credentials, data isolation, cross-case access. (Phase 6.)

Performance     — TPS, latency, concurrent investigations, queue depth, LLM
                   concurrency. (Phase 7, folded into §31.)

Reproducibility — same transaction, same feature snapshot, same model, same
                   rules, same policy → same score/decision. (Phase 5, §22.)

Agent quality   — correct evidence, correct interpretation, no fabricated
                   evidence, valid structured output, appropriate tool usage.
                   (Phase 5, new — Legion has no prior category for grading
                   what an agent concluded, only what it was allowed to do.)
```

The critical end-to-end acceptance test the source material specifies (a
transaction with high device risk and high velocity, traced all the way
through case creation, agent activation, task execution, evidence, findings
and investigation completion, with every version recorded) is adopted as
Legion's own Phase 2/3 acceptance test, extending `test/e2e`'s existing
"starts the real binaries and speaks gRPC to them" posture to the
investigation plane once it exists.

