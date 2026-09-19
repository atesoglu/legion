# `deploy/observability/`

The ADR-020 verification stack: OTel Collector, Prometheus, Grafana,
Elasticsearch, Kibana, Filebeat, plus the `gateway` image alone so there is a
real signal source. Not the Kubernetes deployment target -- that is the
cluster half of Phase 4, deliberately parked.

## Bring it up

```
cp .env.example .env   # .env is gitignored repo-wide; this fills in the same dev-only values
docker compose up -d
```

`setup` generates a CA and a TLS cert for Elasticsearch, then creates the
`kibana_system` and `filebeat_internal` (least-privilege, not the `elastic`
superuser) passwords before the rest starts. Credentials live in `.env` --
they are dev-only, bound to `127.0.0.1`, and never leave this machine.

Wiring: `gateway` pushes OTLP metrics to `otel-collector:4317`; the collector
exposes them at `:8889` for `prometheus` to scrape. `gateway`'s stdout is read
by `filebeat` (Docker autodiscover) and shipped to `elasticsearch`, queryable
in `kibana` at <http://localhost:5601>.

## Prove signals actually flow

No other Legion service needs to run: `legion.decision.duration` is recorded
for every outcome, including a rejected call, so one unauthenticated request
against the gateway is enough.

```
curl -s http://localhost:9090/api/v1/query?query=legion_decision_duration_seconds_count
```

should return a sample with `outcome="unauthenticated"` a few seconds after
any call to `gateway`'s `EvaluateTransaction`, and the same request's log line
is queryable in Elasticsearch under `filebeat-*`.

## Bring it down

```
docker compose down -v
```

`-v` also drops the Elasticsearch/Kibana volumes and the generated certs, so
the next `up` regenerates them from scratch.
