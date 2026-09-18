# Threat model

Status: Phase 2. Threats and intended mitigations are documented here.
Mitigations marked *planned* are not implemented. A mitigation without a test
is an assumption, and Phase 6 exists to convert them into evidence.

What is enforced today: at the edge, callers are authenticated, every field
is validated, unbounded strings and un-pseudonymised identifiers are
rejected, each caller is rate limited, and a resubmission is deduplicated
(ADR-019). The capability runtime (ADR-005) exists and is enforced for
investigation-plane tool calls (`investigation/worker`), with real limits —
see T-14. Network isolation, workload identity (mTLS), and capability
enforcement on the real-time deterministic/behavioural path are not
implemented.

## Scope

In scope: the Legion platform — gateway, orchestrator, capability runtime,
Rust data plane, behavioural agent, inference runtime, feature store, lineage
store, and the deployment that hosts them.

Out of scope: the calling system's own security, the correctness of upstream
tokenisation, the physical security of the cluster, and the model's training
provenance.

Likelihood and impact are engineering judgements for a self-hosted deployment
of this shape, not actuarial estimates.

## Summary

| ID | Threat | Impact | Likelihood | Primary mitigation |
|---|---|---|---|---|
| T-01 | Malicious agent | High | Low | Capability model, deterministic decision authority |
| T-02 | Compromised agent | High | Medium | No ambient credentials, scoped capabilities, no egress |
| T-03 | Compromised model runtime | High | Medium | Output validation, bounded weight, no decision authority |
| T-04 | Model manipulation (weights, registry) | High | Low | Checksum pinning, governed versions, shadow evaluation |
| T-05 | Prompt injection | Medium | **High** | Untrusted text as data, schema-constrained output, bounded weight |
| T-06 | Unauthorised feature access | Medium | Medium | Verb-based capability broker, allow-listed names |
| T-07 | Data exfiltration | High | Low | No agent egress, no credentials, audited access |
| T-08 | Credential theft | High | Low | Workload identity, credentials only in the control plane |
| T-09 | Lateral movement | High | Low | Default-deny network, per-operation authorisation |
| T-10 | Feature poisoning | High | Medium | Write path separation, bounded rules, anomaly detection |
| T-11 | Malformed inference output | Medium | High | Strict validation, controlled rejection |
| T-12 | Denial of service | High | Medium | Rate limiting, deadlines, breakers, load shedding |
| T-13 | Replay attack | Medium | Medium | Idempotency keys (ADR-019, implemented at the gateway), transaction-level deduplication |
| T-14 | Malicious or compromised investigation agent | High | Medium | Same capability boundary as T-01/T-02, extended to Zone 6 (ADR-017) |
| T-15 | Cross-case data access | High | Medium | Per-task capability scoping, explicit cross-case access tests |

---

## T-01 — Malicious agent

An agent is deployed, or modified, with the deliberate intent to influence
decisions or extract data.

- **Impact.** High. An agent that could set its own score arbitrarily could
  wave through fraudulent transactions.
- **Likelihood.** Low. Requires the ability to deploy or modify a workload.
- **Mitigation.** An agent's maximum influence is its configured weight — 30 %
  at most. It cannot produce a `Decision`; no field it can populate carries one
  (ADR-004). Its `signal_enabled` flag can be cleared without redeployment. It
  holds no credentials and cannot reach state directly.
- **Detection.** Score distribution per agent monitored over time; a signal
  that suddenly saturates is visible. Shadow-mode comparison before any agent
  version becomes authoritative. Capability audit for out-of-pattern access.
- **Residual risk.** A malicious agent operating *within* its weight can still
  bias outcomes at the margin, especially near a threshold boundary. Detection
  is statistical, so a subtle, low-volume bias may persist for some time.

## T-02 — Compromised agent

An agent process is taken over through a dependency vulnerability or a
deserialisation flaw.

- **Impact.** High if the agent holds credentials; bounded if it does not.
- **Likelihood.** Medium. Agents parse externally influenced data.
- **Mitigation.** This is the threat the capability model exists for. No
  feature-store credential, no Kubernetes API access, no filesystem write, no
  network egress, no service discovery. Every action is a verb on
  `CapabilityService`, scoped to one evaluation, budgeted and audited. Rust
  agents forbid `unsafe`, removing memory-corruption classes outright.
- **Detection.** Capability denial rate per agent; unexpected capability use;
  invocation-budget exhaustion; egress attempts blocked at the network layer.
- **Residual risk.** The attacker still sees the feature values legitimately
  granted for evaluations flowing through that agent, one subject at a time.
  Capabilities bound scope, not sensitivity.

## T-03 — Compromised model runtime

The inference runtime is taken over and returns attacker-chosen output.

- **Impact.** High if model output were trusted; bounded because it is not.
- **Likelihood.** Medium. Model serving stacks are large and fast-moving.
- **Mitigation.** Output is validated against a JSON Schema, range-checked, and
  restricted to behavioural reason codes (500–599) before it can influence
  anything. The resulting signal carries at most 30 % weight. The runtime has
  no egress and no credentials. Weights are checksum-pinned.
- **Detection.** Schema validation failure rate; behavioural score distribution
  shift; checksum mismatch at load; comparison against shadow evaluation.
- **Residual risk.** A compromised runtime returning *valid, plausible*
  signals is indistinguishable from a working one on a per-request basis. Only
  distributional monitoring catches it, and only over time.

## T-04 — Model manipulation

Weights, quantisation or the served model are swapped for a different artefact.

- **Impact.** High. Silent behaviour change across every decision.
- **Likelihood.** Low. Requires access to the registry or the runtime's storage.
- **Mitigation.** `model_checksum` is verified at load and recorded in
  `GovernedVersions` on every decision. A model change is a governed change:
  version, checksum, quantisation and runtime version are pinned, and the
  change is evaluated in shadow mode before it is authoritative.
- **Detection.** Checksum mismatch; unexpected `GovernedVersions` in lineage;
  shadow-versus-production divergence.
- **Residual risk.** An attacker who can also alter the recorded expected
  checksum defeats the check. That requires control-plane compromise, which is
  outside what any of these controls survive.

## T-05 — Prompt injection

Payer- or merchant-controlled text instructs the model to produce a low risk
score, reveal its instructions, or assert a decision.

- **Impact.** Medium. Bounded by the behavioural signal's weight.
- **Likelihood.** High. This should be assumed to be attempted continuously.
  Any free text on a financial transaction is adversarial input to an LLM.
- **Mitigation.** Free text is carried as `UntrustedText`, with its source,
  so no consumer can handle it without acknowledging what it is. It is passed
  as clearly delimited data, never as instructions, and never concatenated into
  the instruction region of a prompt. Output is schema-constrained, so the model
  cannot emit prose in place of a signal, cannot emit a decision, and cannot
  emit reason codes outside the behavioural range. `PROMPT_INJECTION_SUSPECTED`
  is recorded when instruction-shaped content or decision assertions appear.
  The deterministic agents never see free text at all.
- **Detection.** Injection-suspected rate; schema violation rate; behavioural
  score distribution conditioned on the presence of free text; a dedicated
  injection corpus in the adversarial suite (Phase 6).
- **Residual risk.** A successful injection can still move the behavioural
  score within its valid range. Against a transaction sitting near a threshold,
  30 % of weight can change the outcome. This is the strongest argument for
  keeping the model's weight bounded and the decision deterministic.

## T-06 — Unauthorised feature access

An agent reads features about subjects or dimensions it has no business seeing.

- **Impact.** Medium. Privacy exposure and a foothold for profiling.
- **Likelihood.** Medium. It is the natural next step after T-02.
- **Mitigation.** There is no key-addressable interface. `FeatureQuery` cannot
  express a key, a scan, a cursor, or a subject other than the current one.
  Names and windows are allow-listed per agent. Invocations are budgeted.
- **Detection.** `DENIED_CONSTRAINT` and `DENIED_SCOPE` verdicts; invocation
  counts above the normal profile.
- **Residual risk.** An agent processing a high volume of evaluations
  accumulates a picture of the population one legitimate subject at a time.

## T-07 — Data exfiltration

Data is moved out of the platform.

- **Impact.** High.
- **Likelihood.** Low, given the egress posture.
- **Mitigation.** The agent sandbox has no egress. The data plane makes no
  outbound calls. Only the control plane reaches state. Logs deliberately
  exclude identifiers, amounts and locations, so log shipping is not an
  exfiltration channel. Data at rest is pseudonymous.
- **Detection.** Blocked egress attempts; anomalous outbound volume from the
  control plane; capability audit volume per agent.
- **Residual risk.** Exfiltration through a legitimate response channel — an
  agent encoding data into a signal's evidence field — is possible. Evidence
  fields are structurally typed rather than free-form, which limits bandwidth
  but does not eliminate it.

## T-08 — Credential theft

An attacker obtains a credential and reuses it.

- **Impact.** High.
- **Likelihood.** Low, because there are few credentials to steal.
- **Mitigation.** The feature-store credential exists only in Zone 2. Agents
  and the data plane have none. Service-to-service authentication uses
  short-lived workload identity rather than long-lived secrets. Secrets are
  never mounted into the agent sandbox.
- **Detection.** Feature-store access from an unexpected identity; certificate
  issuance anomalies.
- **Residual risk.** Control-plane compromise yields the credential. Nothing in
  this model prevents that; it is the trust anchor.

## T-09 — Lateral movement

A foothold in one workload is used to reach others.

- **Impact.** High.
- **Likelihood.** Low.
- **Mitigation.** Default-deny networking with explicit per-pair allowances.
  Authorisation is per operation, not per connection, so network reachability
  alone achieves nothing. No shell in agent or data-plane images. Non-root,
  read-only root filesystem, all capabilities dropped.
- **Detection.** Denied connection attempts; authentication failures between
  workloads; unexpected process execution.
- **Residual risk.** Legitimate paths remain: agent → capability runtime,
  orchestrator → data plane. These are the paths an attacker will use.

## T-10 — Feature poisoning

An adversary shapes the feature store's contents through their own transaction
behaviour, or by writing to it directly, so that later fraud appears normal.

- **Impact.** High, and slow to notice.
- **Likelihood.** Medium. Behavioural shaping requires no access at all — it
  requires patience, and it is what "low and slow" fraud is.
- **Mitigation.** Agents hold no write capability; the write path is separate
  from the read path. Rules are bounded so that a single feature cannot
  dominate a score. `LOW_AND_SLOW_PATTERN` exists specifically to detect
  gradual normalisation. Lifetime and long-window features are used alongside
  short ones so recent behaviour cannot fully define the baseline.
- **Detection.** Feature distribution monitoring per account cohort; scenario
  coverage in the adversarial suite (Phase 6); replay of a poisoned dataset
  against an updated policy.
- **Residual risk.** Substantial. Patient behavioural shaping against a system
  that learns from behaviour is an open problem, not a solved one. Legion's
  position is that deterministic rules with long windows are harder to shape
  than an online-learning model, not that shaping is prevented.

## T-11 — Malformed inference output

The model returns output that does not conform to the schema, is out of range,
or is prose.

- **Impact.** Medium.
- **Likelihood.** High. This is normal behaviour for language models under
  distribution shift, not an attack.
- **Mitigation.** Reject the response entirely. No partial parsing, no
  repair, no inference of intent from prose. At most one retry, and only if the
  remaining budget allows. `MODEL_OUTPUT_INVALID`, degradation `PARTIAL`, and
  the decision proceeds on the deterministic signals.
- **Detection.** Validation failure rate, alerted as a model-health metric.
- **Residual risk.** A persistently malformed model reduces the system to its
  deterministic signals. That is a degraded but correct state, and the
  fallback rate makes it visible.

## T-12 — Denial of service

Traffic volume, inference queue saturation or a slow dependency exhausts
capacity.

- **Impact.** High. An unavailable risk system either blocks payments or forces
  a bypass, and the bypass is what the attacker wants.
- **Likelihood.** Medium.
- **Mitigation.** Per-caller rate limiting at the gateway. Hard request
  deadlines that free resources rather than queueing indefinitely. Circuit
  breakers so a dead dependency is not retried into the ground. Bounded
  inference concurrency with explicit `OVERLOADED` shedding. The deterministic
  path is cheap and survives loss of the model.
- **Detection.** Queue depth, shed rate, breaker state, saturation metrics,
  fallback rate.
- **Residual risk.** Under sufficient load the system enters fallback for a
  large share of traffic. That is a deliberate, observable degradation, but the
  detection quality measured in Phase 5 does not apply while it lasts.

## T-13 — Replay attack

A previously seen transaction, or a previously observed decision, is submitted
again to obtain a favourable outcome.

- **Impact.** Medium.
- **Likelihood.** Medium.
- **Mitigation.** **Implemented.** `Transaction.idempotency_key` (ADR-019) is
  required and enforced at the gateway: `(caller, idempotency_key)` is the
  dedup key, held in a Redis store separate from the feature store, retained
  for 24 hours. A resubmission with the same key and the same transaction
  body returns the original `EvaluateTransactionResponse` rather than a new
  evaluation; a resubmission with the same key and a materially different
  body is rejected with `INVALID_ARGUMENT`. This mitigates application-level
  resubmission specifically. It does not depend on transport security: today
  transport is plaintext gRPC with shared-key caller authentication (mTLS is
  Phase 4, ADR-011), so a network-level replay of the request is still
  possible up to the transport boundary — it would simply be recognised and
  collapsed by this same dedup key on arrival, not silently re-evaluated.
  Not yet covered: the dedup store's own availability is not measured, and a
  concurrent (rather than sequential) identical resubmission that arrives
  before the first has stored its result returns `ABORTED` rather than the
  eventual answer, which is a caller-visible gap worth revisiting if it
  proves to matter in practice.
- **Detection.** Duplicate-subject rate; decision reuse attempts. Both are now
  counted — `legion.gateway.dedup.claims` distinguishes a new claim from a
  replay, a body conflict and a concurrent in-flight duplicate — but nothing
  collects the counter yet and no threshold alerts on it; see the
  observability gap in `architecture.md` §9.
- **Residual risk.** Legion does not control the caller's use of its response.
  If the calling system does not bind a decision to the transaction it
  requested, replay is possible outside Legion's boundary. That boundary is
  now the full extent of the residual risk; it no longer also includes
  resubmission inside Legion's own boundary.

## T-14 — Malicious or compromised investigation agent

An investigation agent (Zone 6, ADR-017) is deployed, modified, or
compromised with the intent to fabricate evidence, suppress a finding, or
exfiltrate data gathered during a case.

- **Impact.** High. A fabricated or suppressed finding can misdirect a human
  analyst's conclusion about a transaction already flagged as suspicious.
- **Likelihood.** Medium — the same likelihood class as T-02, since an
  investigation agent is architecturally the same shape as the behavioural
  agent (a model-backed component reasoning over content it did not
  originate), just running later and against richer context.
- **Mitigation.** The same capability boundary as T-01/T-02, applied to Zone
  6 (`investigation-model.md` §7): no ambient credentials, a closed tool
  registry checked before every call, every invocation audited. An
  investigation agent cannot alter a `Decision`, cannot write evidence
  bypassing the evidence/finding distinction (§3 of `investigation-model.md`),
  and its findings are always attributed and timestamped.

  **Enforced, with real limits.** `control-plane/capability` (ADR-005) runs
  the identity/grant/scope/constraint/budget pipeline, and
  `investigation/worker` calls `CheckToolCapability` before every tool
  execution; a denial genuinely stops the call and fails the task
  permanently rather than retrying into the same answer. Two gaps remain,
  neither hypothetical: (1) there is no real tool
  registry behind the check yet — the one investigation agent's "tool call"
  is still canned, deterministic output, so the check gates a mock, not an
  arbitrary action a compromised agent might otherwise take; (2)
  `workload_id` is asserted by the caller, a Phase 1 stand-in for
  the mTLS peer identity ADR-011 will provide, so a compromised worker
  process could still assert any identity it likes. This narrows, but does
  not close, T-14 until a real tool registry and mTLS both exist.

  What is deliberately **not** a gap: the grant is a static Go manifest in
  Zone 2 rather than a read of Zone 6's `agent_definitions.allowed_tools`.
  The worker holds write credentials for that database, so sourcing grants
  from it would let a compromised worker grant itself any tool — this threat,
  self-served. The two are a declaration and a grant, not one fact stored
  twice, and `test/e2e/capability_registry_test.go` asserts in CI that the
  declaration never exceeds the grant and that no agent holds a tool only
  another agent declares.
- **Detection.** Tool-call audit trail (logged, not yet persisted anywhere
  durable — and ADR-020 does not cover it: an audit record is evidential like
  lineage, not operational like a log, so where it persists is still
  undecided); `legion.capability.checks` counts every verdict, allowed and
  denied alike, though nothing collects it yet; finding-to-evidence reference
  completeness (a
  finding with no cited evidence is itself a signal); per-agent confidence
  distribution monitored the same way per-agent score distribution is
  monitored for T-01.
- **Residual risk.** Same as T-02: the attacker still sees whatever the
  agent's granted tools legitimately expose, for the cases it is activated
  on. Capabilities bound scope, not sensitivity, in Zone 6 exactly as in
  Zone 4.

## T-15 — Cross-case data access

A task, tool call or agent for one case reads or influences evidence,
findings, or state belonging to a different case.

- **Impact.** High. Case data is exactly the kind of thing a data-protection
  regime treats as sensitive, and cross-case leakage is an incident whether
  or not it was exploited maliciously.
- **Likelihood.** Medium. A generic worker executes many different logical
  agents against many different cases in sequence; a scoping bug is a
  plausible defect class, not only a deliberate attack.
- **Mitigation.** Every capability grant to a Zone 6 worker is scoped to one
  `investigation_id`/`task_id`, exactly as Zone 4 capabilities are scoped to
  one `evaluation_id` (`capability-model.md` §5). A tool call outside that
  scope is `DENIED_SCOPE`. Task claims are per-worker and per-task; no worker
  holds two tasks' contexts in the same authorised scope at once.
- **Detection.** `DENIED_SCOPE` rate per agent; an agent that triggers this
  routinely is either misconfigured or attempting enumeration.
- **Residual risk.** A defect in how the controller constructs a task's
  context (rather than in the capability check itself) could still hand a
  worker the wrong case's data outright, upstream of any capability check.
  This is a code-correctness risk the capability model does not reach by
  itself; it is why the security acceptance tests in `project-plan.md` §51
  include an explicit "agent accesses another case" test, not only a
  capability-scope test.

---

## Assumptions

These are stated so that they can be challenged:

1. Upstream tokenisation is sound; Legion never receives a PAN.
2. The control plane is not compromised. Most controls here depend on it.
3. The HMAC key is stored and rotated correctly.
4. The container images deployed are the ones built by the pipeline.
5. Kubernetes RBAC and admission control are configured by a competent
   operator; Legion documents its requirements but does not enforce them.

## Verification status

| Control | Status |
|---|---|
| Deterministic decision authority | Contract-enforced in Phase 0; tested in Phase 1 |
| Bounded model influence | Contract-enforced in Phase 0; measured in Phase 5 |
| Capability enforcement | Contract in Phase 0; runtime and tests in Phase 3 |
| Network default-deny | Phase 4 |
| Output validation | Phase 2 |
| Injection resistance | Phase 6 |
| DoS behaviour | Phase 6 |
| Feature poisoning scenarios | Phase 6 |

Nothing in this document should be read as a claim that a control works. It is
a claim about what is intended and how it will be shown.
