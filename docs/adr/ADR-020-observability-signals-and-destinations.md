# ADR-020: Observability signals and their destinations

**Status:** Accepted
**Date:** 2026-09-18
**Phase:** 4

## Context

Legion has no observability of any kind. There are no metrics, no traces, and
no collection of the structured logs it already writes. The 80 ms budget
ADR-009 commits to has never been measured. Several failure paths are
deliberately silent by design — the lineage writer drops on a full queue, the
case-trigger publisher does the same, an investigation task that exhausts its
attempts reaches `DEAD_LETTER` with nothing watching — and each of those was
documented as an accepted gap on the assumption that observability would make
them visible later. Later is now.

Nothing has been decided about where any of this goes. `project-plan.md` §28
says "OpenTelemetry **should** provide" and lists span and metric names; §50
adds investigation-path metrics and cost; the target repository tree contains
a `deploy/observability/` directory that does not exist. No backend is named
anywhere in the repository. Nineteen ADRs cover Go, Rust, protobuf, the
feature store, the language model, the deadline, Kubernetes, the zero-trust
model, shadow mode, replay, the agent set, packaging, the investigation
plane, the lineage schema and idempotency. Observability is the one major
technology choice with no record.

Four constraints shape the decision, and they are not the usual ones:

1. **Legion already has a domain-specific trace of the decision path.**
   `DecisionLineage` carries an `ExecutionSpan` per stage — `feature_fetch`,
   `deterministic_agents`, `sentinel` — plus the deadline and total elapsed,
   persisted to PostgreSQL and normalised into `execution_spans` by ADR-018.
   It is unsampled, durable, access-controlled and correlated by
   `decision_id`. A distributed trace of the same path would be a second,
   worse copy of it. What lineage does *not* capture is the time *between*
   stages — transport, connection setup, queueing — and anything in Zone 6,
   which it does not reach at all.

2. **Telemetry must not be able to affect a decision.** The decision path has
   an 80 ms budget (ADR-009) and fails closed on nothing but its own
   dependencies. An exporter that blocks, retries or allocates unboundedly on
   that path converts an observability outage into a decision outage. This is
   the same reasoning ADR-007 uses to keep lineage off the critical path.

3. **Self-hosting is a premise, not a preference.** ADR-008 self-hosts the
   language model specifically so that transaction data never leaves the
   deployment. Telemetry carries request shape, timing, error detail and
   correlation identifiers. A hosted telemetry vendor would be an
   unaudited egress path for exactly the data the rest of the architecture
   is arranged to contain.

4. **The data plane is Rust.** Whatever is chosen needs a working Rust story,
   not only a Go one, and the two ecosystems are not at the same level of
   maturity for this.

A fifth constraint is dormant but will not stay that way: ADR-011's zero-trust
model has to express what may connect to what. Whether telemetry is pulled or
pushed decides whether the monitoring plane needs inbound reach into every
zone, including Zone 3, which is otherwise reachable only from Zone 2.

## Decision

Observability is not one decision. It is four, and they are recorded
separately because bundling them is how the wrong storage ends up holding the
wrong signal.

### 1. Emission: OTLP, push, outbound-only

Every Legion process emits metrics and traces as OTLP to an OpenTelemetry
Collector. No Legion process holds a connection to a storage system, knows a
backend's address, or is reconfigured when a backend changes. The Collector is
the only component that knows where anything is stored.

This is chosen over letting a monitoring system scrape each service because it
inverts the direction of the connection. A scrape is inbound into every zone;
an OTLP push is outbound from every zone. The zero-trust network policy
ADR-011 requires is then "every zone may reach the collector, the collector may
reach nothing", rather than an exception carved into each zone for a
monitoring namespace. Zone 3 in particular keeps the property that nothing
outside Zone 2 opens a connection to it.

Export is asynchronous and bounded on every path: batching processors, a
periodic metric reader, bounded queues, and drop rather than block when a
queue is full. A collector that is down or slow degrades observability and
nothing else. This is the same posture, and for the same reason, as the
lineage writer's bounded channel.

The Collector is not a Legion zone. It is platform infrastructure that
receives from every zone and may initiate connections to none of them.

### 2. Logs: emitted to stdout, shipped to Elasticsearch, read in Kibana

Legion services already write structured JSON to stdout, and that stays the
emission contract. The destination is Elasticsearch, queried through Kibana.

Emission and destination are separate choices, and only the destination was
missing. Writing to stdout is not a weaker form of log management; it is a
decoupling. The process hands a line to the operating system and is finished
with it. If the log pipeline is down, the lines are still on disk and a
shipper catches up when it recovers, and `docker logs` still works for
whoever is debugging at the time. An in-process Elasticsearch or OTLP log
exporter moves that buffer inside the decision path, where it is subject to
constraint 2.

Logs gain `trace_id` and `span_id` fields when a trace context is present, so
a log line and the trace it belongs to can be joined in Kibana. Correlation
does not require in-process log export.

The existing rule stands and is now enforceable rather than aspirational:
Legion logs are operational, not evidential. Transaction payloads,
identifiers and model output never reach them. Auditable detail belongs in
decision lineage, which has its own retention and access controls.

Collection is a deployment concern, not an application one. Legion services
are containerised per ADR-016, so the container runtime captures stdout and
Filebeat reads the resulting log files. No Legion process opens a connection
to Elasticsearch, and removing Elasticsearch entirely would change no Legion
code.

### 3. Metrics: OTLP to the Collector, stored in Prometheus, explored in Grafana

Services push OTLP metrics to the Collector. The Collector exposes a
Prometheus exposition endpoint, and Prometheus scrapes that one endpoint.
Grafana reads Prometheus.

Prometheus is a pull-based system; "pushing to Prometheus" is not a thing that
exists, and the arrangement above is what push actually means in practice. The
property that matters — no inbound connection into any Legion zone — is
preserved, because the single scrape target is the Collector, which holds no
risk logic and sits in no zone.

Metric temporality is cumulative. OTel SDKs can be configured for delta
temporality and some default to it; delta counters silently produce wrong
values through a Prometheus exporter, and it is not obvious from the
dashboards that they are wrong.

**No identifier may ever be a metric label.** Not `transaction_id`, not
`account_id`, not `decision_id`, `case_id`, `investigation_id` or `task_id`,
pseudonymous or otherwise. This is two rules that happen to coincide: an
unbounded label destroys the metric store, and an identifier in a metric is
an egress path for data the threat model keeps out of logs. Labels are drawn
only from bounded sets — `service`, `agent_id`, `decision`, `reason_code`,
`degradation`, `verdict`, `status`, `outcome`. Per-entity questions are
answered from lineage and the investigation schema, which are built for them.

### 4. Traces: OTLP to the Collector, stored in Elasticsearch, read in Kibana

Traces go to Elasticsearch, alongside the logs, and are read in Kibana.

This adds no storage technology. ADR-018 rejected a columnar analytical store
for lineage on the grounds that no current query volume justifies a second
storage technology, and that reasoning applies unchanged here: Elasticsearch
is already deployed for logs, trace volume is low, and putting traces beside
the logs makes `trace_id` correlation a query rather than an integration.

Sampling is deliberately asymmetric, because the two paths are not in the
same position:

- **Decision path (Zones 1–3): sampled, with errors always sampled.** Lineage
  already records every stage of every decision, unsampled and durably. A
  trace here adds transport and queueing time, not stage timing, and paying
  100 % of the cost inside an 80 ms budget to re-record what is already
  recorded is not a good trade.
- **Investigation path (Zone 6): sampled at 100 %.** It is asynchronous, off
  the budget, low volume, multi-hop, and there is no lineage equivalent. It is
  the part of the platform with no visibility at all.

Trace context propagates across gRPC and across the Redis Streams queue hops,
so a case can be followed from the decision that opened it to the finding that
closed it.

### 5. Cost is recorded in PostgreSQL, not in the metric store

`project-plan.md` §50 lists "LLM cost / investigation" beside
`llm_tokens_total`. These are different kinds of fact and only one of them is
a metric.

Aggregate cost — tokens per second, total tokens by agent, request rates —
is a metric and lives in Prometheus. Per-investigation cost is a business
record: it is asked about per entity, long after the fact, and it must
survive as long as the case does. It lives in PostgreSQL beside the
investigation, in the same shape `tool_executions.duration_ms` already takes.

A metric store cannot answer "what did case X cost" and should not be asked
to. This is written down because the plan currently implies it will be.

### 6. The local stack runs with authentication on

The development stack under `deploy/observability/` runs Elasticsearch with
its 8.x defaults intact: TLS between components and real credentials, not
`xpack.security.enabled=false`. It costs a certificate bootstrap step and a
secrets file that would otherwise not exist.

The reason is that a development stack with security disabled is not a smaller
version of the deployment, it is a different system, and every problem it
hides — a missing credential, an untrusted CA, a client that silently falls
back to plaintext — is discovered in the environment least able to absorb it.
This is the same argument ADR-016 makes for keeping distinct ports locally and
in cluster: the local path and the real one should differ in as few respects
as possible.

## Alternatives considered

**Prometheus scraping each service directly.** The conventional arrangement,
simpler by one component, and it removes a failure domain: a dead collector
cannot lose metrics that were never pushed to it. Rejected because of what it
does to the network policy. Scraping requires inbound reach from the
monitoring plane into every zone, including the Rust data plane that ADR-011
otherwise isolates to Zone 2 callers. Trading a zero-trust property for one
fewer container is the wrong direction in this phase specifically, since the
network policies are being written in it.

**Logstash, completing the classic ELK stack.** Rejected as weight without a
job. Logstash earns its place when logs need transformation in flight; Legion
emits JSON that is already in its final shape, and Elasticsearch ingest
pipelines cover the remainder. A shipper reading the log files and writing to
Elasticsearch is the whole requirement. This is a deliberate departure from
"an ELK stack" as usually named: the E and the K are load-bearing here and the
L is not.

**In-process log export over OTLP, unifying all three signals in one
pipeline.** Genuinely attractive: one mechanism, automatic trace correlation,
no file shipping. Rejected on constraint 2. It moves the log buffer inside the
process, so a collector outage becomes in-process memory pressure and then
dropped logs on the decision path. Trace correlation, the main prize, is
available anyway by putting `trace_id` in the log record.

**Elastic APM for metrics as well as traces and logs**, collapsing to a single
storage technology. Rejected because Elasticsearch is a poor time-series
store at a metric's write rate and retention, and because Prometheus's query
model is what alerting and SLO work actually need. Storing traces in
Elasticsearch is reuse; storing metrics there would be misuse.

**Grafana Tempo for traces**, pairing with the Grafana already being deployed
and far cheaper per span at volume. Rejected only as the *first* choice, and
it is the named escape hatch. With OTLP and a Collector in between, moving
traces from Elasticsearch to Tempo is a Collector configuration change and
touches no Legion code. That indirection is a large part of why decision 1 is
worth its extra component.

**A hosted telemetry vendor.** Operationally the least work by a wide margin.
Rejected on constraint 3. Legion self-hosts a language model specifically so
transaction data cannot leave the deployment; routing timing, error and
correlation data to a third party through a different door would make that
effort decorative.

**Deferring tracing entirely and shipping only metrics and logs.** Defensible,
and close to correct for the decision path, where lineage already records what
a trace would. Rejected because Zone 6 has no lineage, no spans and no
visibility of any kind, and it is the part of the platform about to grow a
model-backed agent with variable latency and variable cost.

## Trade-offs

- **Four new components to operate** — Collector, Prometheus, Grafana,
  Elasticsearch, Kibana and a log shipper — against a platform that currently
  has eight. The observability stack is not smaller than the thing it
  observes, which is uncomfortable and normal.
- **Elasticsearch is expensive for traces.** Every span is indexed. At low
  volume this is irrelevant; the revisit condition below exists because it
  will not stay irrelevant if sampling is ever raised.
- **Sampled decision-path traces mean some requests have no trace.** The
  mitigation is that those requests still have complete lineage, which is a
  better record for every question except transport timing. The failure mode
  to accept is that a one-off transport stall may be unattributable.
- **The Collector is a single point of telemetry failure.** Deliberate: it is
  the price of not granting the monitoring plane inbound access to every zone.
  Its failure is bounded by decision 1 to losing telemetry, never decisions.
- **Instrumentation costs something inside the 80 ms budget**, and the cost is
  currently unknown, because measuring it requires the instrumentation being
  measured. The first thing metrics will show is the cost of metrics.
- **The Rust side is less well served.** The OpenTelemetry Rust SDK is younger
  than the Go one, and the workspace's lint posture (`unsafe_code = forbid`,
  `unwrap_used`/`panic`/`arithmetic_side_effects` denied) constrains the
  wrapper written around it.

## Consequences

- A shared Go package and a shared Rust crate own instrumentation setup, so
  that no service configures an exporter itself and every service exposes the
  same signals. For Go this belongs with the existing process-lifecycle code
  in `internal/platform/runtime`, which already owns start, stop and logging
  for all five Go services.
- **ADR-016's packaging becomes a prerequisite rather than a later phase
  item.** Shipping logs from a file requires the services to be containerised,
  which ADR-016 already specifies in full (two parameterised Dockerfiles, root
  context, `deploy/services.yaml` as the topology source) and explicitly
  leaves to Phase 4 to write. The observability work pulls that forward. It
  also exposes that ADR-016's service set predates Zone 6 and needs revising
  in place before it is implemented.
- The stack itself lives in `deploy/observability/` as Docker Compose, the
  directory the target tree in `project-plan.md` §33 already reserves for it.
  Kubernetes manifests and Helm charts for the same components are deferred
  with the rest of the cluster work.
- The silent failures documented in `failure-model.md` become counted events:
  lineage queue drops, case-trigger drops, tasks reaching `DEAD_LETTER`,
  capability denials, dedup rejections, breaker state changes. Each was
  accepted as a gap on the explicit promise of this phase.
- ADR-009's 80 ms budget becomes measurable for the first time, which means it
  also becomes falsifiable. The budget apportionment in `deadline-model.md` is
  a set of configured defaults that has never been checked against a
  measurement, and this is what checks it.
- ADR-011's network policy gains a concrete, narrow statement to express:
  every zone egresses to the Collector; the Collector ingresses from every
  zone and egresses only to storage; the monitoring plane reaches no zone.
- `execution_spans` remains unread. Nothing in this ADR makes lineage
  redundant, and nothing in it reads lineage back either — that is replay
  (ADR-013) and Phase 5, and the overlap between traces and lineage is a
  reason to build the reader, not a reason to skip it.
- Instrumenting the Rust data plane is explicitly lower priority than
  instrumenting Go. The engines are sub-millisecond pure functions whose
  latency is already observed and recorded by the orchestrator as
  `AgentEvaluation.observed_latency`; the visibility gap is in Go.

## Revisit if

- Trace volume grows enough that Elasticsearch storage cost becomes a
  material line item, at which point traces move to Tempo as a Collector
  configuration change.
- Metric cardinality or retention outgrows a single Prometheus, at which point
  it becomes a remote-write target in front of Mimir or Thanos, again without
  touching Legion code.
- Sampled decision-path traces prove insufficient to diagnose a real latency
  incident that lineage could not explain, which would be evidence that the
  overlap between traces and lineage is smaller than this ADR assumes.
- The Collector's failure domain proves too broad in practice — for instance
  if losing metrics and traces together during an incident repeatedly hides
  the incident — in which case per-signal collectors or direct scraping of a
  subset of services is reconsidered against the zero-trust cost.
