# Vendor Connectivity runbook

**vendor-connectivity** is the retail network's Open Host Service for vendors that
operate fulfillment nodes: vendors register nodes, declare operational profiles, send
inventory feeds, poll purchase orders, acknowledge them in full inside the
acknowledgement window, request labels and confirm shipments. Every vendor write is an
asynchronous transaction the vendor reconciles via `GET /vendor/transactions/{id}`.
Sources: `README.md`, `docs/adr/0002` (aggregates), `0003` (consumed contracts),
`0004` (event catalogue), `docs/runtime.md` (probes, shutdown, outbox).

## Runtime shape

One component, one process: the `api` Deployment runs `cmd/api` (chart
`charts/vendor-connectivity`, `replicaCount: 1`, HPA off by default; no MCP, frontend,
projector or reports component). Inside that process (`cmd/api/main.go`):

- REST Vendor API on `:8080` (`internal/adapters/inbound/http/server.go`): reads and
  writes under `/vendor/*`, `RATE_LIMIT_RPS` limiter, every write behind an
  `Idempotency-Key` header (24h replay).
- Transactional outbox relay draining `outbox_events` to Kafka
  (`internal/adapters/outbound/outbox/relay.go`).
- Three Kafka consumers: node (`retailnetwork.node-topology.events`), listing
  (`retailnetwork.offer-availability.events`), sourcing
  (`retailnetwork.order-sourcing.events`) — `internal/adapters/inbound/kafka/consumers.go`.
- Three loops: submission processor (due async writes), expiry sweeper (orders past
  `ackBy`), 24h retention sweeper (`startBackground` in `cmd/api/main.go`).

If the relay, a consumer or a loop returns on its own, the process exits non-zero so
the orchestrator restarts it (`watchWorkers`); it never sits "alive" with a dead worker.

## Dependencies

- **Postgres** — `DATABASE_URL` (pooled DSN, PgBouncer in-cluster) and
  `MIGRATIONS_DATABASE_URL` (direct DSN for the boot migrations only: golang-migrate
  holds a session advisory lock, which breaks behind a transaction-pooling proxy).
  Both from the Secret `vendor-connectivity-db` (chart `database.existingSecret`; the
  migrations key is `optional: true`, the binary falls back to `DATABASE_URL`).
  Embedded migrations run at boot with backoff (`internal/bootretry`).
- **Kafka** — `KAFKA_BROKERS` (required): relay sink, the five consumers, `/readyz`.
  Topics are auto-created by the first writer (`AllowAutoTopicCreation: true`,
  `internal/adapters/outbound/kafka/writer.go`). Publishes
  `retailnetwork.vendor-connectivity.events` (`.../outbound/kafka/encoder.go`);
  dead letters go to `<topic>.dlq` (`.../inbound/kafka/deadletter.go`).
- **Four sibling REST services** (required, no defaults; in-cluster
  `http://<context>.warehouse-systems.svc.cluster.local:80`): `NODE_TOPOLOGY_URL`
  (node registration, operational profiles), `OFFER_AVAILABILITY_URL` (inventory
  feeds), `SHIPPING_AND_TRACKING_URL` (labels), `NODE_PERFORMANCE_URL` (scorecards).
  Adapter: `internal/adapters/outbound/httpclient/client.go`.

## Health

`internal/adapters/inbound/http/readiness.go`, wired in `cmd/api/main.go`:

- `GET /healthz` — liveness only: `200 {"status":"ok"}` while the process serves, even
  while draining or with every dependency down.
- `GET /readyz` — `200 {"status":"ready"}` only while the drain gate is open, Postgres
  answers `PING` and a Kafka broker answers a topic-less metadata request (probing
  never creates topics). Verdict cached 3s, one round bounded 2s. Draining: `503
  {"status":"not_ready"}`. Dependency down: `503 application/problem+json`
  `dependency-unavailable` naming `postgres` and/or `kafka`.

Writes with a dependency down, per code:

- **Postgres down** — writes fail: aggregate, idempotency row and outbox rows commit in
  ONE Postgres transaction, so nothing durable can happen (`errors.go` falls through
  to the internal-error problem). `/readyz` 503 pulls the pod from Kong first.
- **Kafka down** — writes are STILL accepted: events are committed to `outbox_events`
  and the relay retries every `OUTBOX_RELAY_INTERVAL`; only `/readyz` goes 503.

## Configuration

Chart `charts/vendor-connectivity` — each env var below maps 1:1 to a `values.yaml`
`config.*` key rendered into the ConfigMap/container env (checksum-annotated so a
change rolls the pods); secret keys come from the `vendor-connectivity-db` Secret.

| Env var | Chart value | Default / meaning |
| --- | --- | --- |
| `DATABASE_URL` | `database.existingSecret` | required, pooled Postgres DSN |
| `MIGRATIONS_DATABASE_URL` | `database.migrationsExistingSecretKey` | optional, direct DSN for boot migrations |
| `KAFKA_BROKERS` | `kafka.brokers` | required, comma-separated `host:port` |
| `NODE_CONSUMER_GROUP` | `config.nodeConsumerGroup` | required, stable group id (node-topology) |
| `LISTING_CONSUMER_GROUP` | `config.listingConsumerGroup` | required (offer-availability) |
| `SOURCING_CONSUMER_GROUP` | `config.sourcingConsumerGroup` | required (order-sourcing) |
| `INBOUND_CONSUMER_GROUP` | `config.inboundConsumerGroup` | required (seller-inbound shipments, ADR 0005) |
| `PACKAGING_CONSUMER_GROUP` | `config.packagingConsumerGroup` | required (offer-availability packaging, ADR 0005) |
| `NODE_TOPOLOGY_URL` | `config.nodeTopologyUrl` | required, sibling base URL |
| `OFFER_AVAILABILITY_URL` | `config.offerAvailabilityUrl` | required |
| `SHIPPING_AND_TRACKING_URL` | `config.shippingAndTrackingUrl` | required |
| `NODE_PERFORMANCE_URL` | `config.nodePerformanceUrl` | required |
| `EVENT_PUBLISHER` | `config.eventPublisher` | `kafka` only; anything else is a boot error |
| `HTTP_ADDR` | `config.httpAddr` | `:8080` |
| `LOG_LEVEL` | `config.logLevel` | `info` (`debug|info|warn|error`) |
| `OUTBOX_RELAY_INTERVAL` | `config.outboxRelayInterval` | `1s` |
| `OUTBOX_MAX_ATTEMPTS` | `config.outboxMaxAttempts` | `20`; `0` = unlimited |
| `SUBMISSION_POLL_INTERVAL` | `config.submissionPollInterval` | `1s` |
| `EXPIRY_SWEEP_INTERVAL` | `config.expirySweepInterval` | `30s` |
| `SHUTDOWN_DRAIN_DELAY` | `config.shutdownDrainDelay` | `5s`; `0` disables |
| `TX_PROCESSING_DELAY` | `config.txProcessingDelay` | `0` — fault-injection knob |
| `TX_FAILURE_RATE` | `config.txFailureRate` | `0` — fault-injection knob |
| `PO_REDELIVERY_RATE` | `config.poRedeliveryRate` | `0` — fault-injection knob |
| `RATE_LIMIT_RPS` | `config.rateLimitRps` | `0` = unlimited over `/vendor/*` |

Honest note: `TX_PROCESSING_DELAY` (submissions stay `PROCESSING` at least this long),
`TX_FAILURE_RATE` (share of submissions that fail ON PURPOSE with
`ErrSimulatedFailure`) and `PO_REDELIVERY_RATE` (chance a poll page repeats its first
order) are simulation knobs so a vendor integration can exercise a hostile network
(`internal/application/usecases/service.go`, `submissions.go`). They inject failures;
they are not operational tuning — keep them unset in normal operation.

## Alerts and dashboards

- Grafana dashboard: uid `warehouse-vendor-connectivity` (fleet convention, configmap
  `grafana-dashboards-warehouse` in namespace `observability`).
- Pod-restart alerting covers every pod in `warehouse-systems`
  (`WarehousePodRestarting` on `kube_pod_container_status_restarts_total`,
  warehouse-infra `terraform/alerting/warehouse-alert-rules.json`).
- Logs are JSON on stdout; Alloy maps the pod label `app.kubernetes.io/name` to the
  Loki label `app`, so query Loki with `{namespace="warehouse-systems",
  app="vendor-connectivity"}`.
- No business metrics are instrumented: `go.mod` has no Prometheus client and the code
  exports no metrics. Any business/event panels on the dashboard stay empty until the
  service instruments itself; alerting today can only see pod restarts, not lag,
  outbox depth or transaction outcomes.

## Failure modes

- **Pod crash-loops.** Diagnosis: `kubectl -n warehouse-systems logs deploy/vendor-connectivity`
  — a missing/invalid env var is a boot error naming the variable; migrations and the
  first ping retry with backoff before the process gives up. The pod also restarts by
  design when the relay, a consumer or a loop dies (`watchWorkers`). Action: fix the
  ConfigMap/Secret (or the dependency), then `rollout restart`.
- **Postgres unreachable.** Symptom: `/readyz` 503 naming `postgres`; loops log
  `loop pass failed` and retry next tick; REST writes fail (see Health). Action:
  check PgBouncer and the `vendor-connectivity-db` Secret; recovery is automatic.
- **Kafka unreachable.** Symptom: `/readyz` 503 naming `kafka`; REST writes are STILL
  accepted and queue in `outbox_events` (`published_at IS NULL`); the relay retries
  each pass. Action: restore brokers; the backlog drains itself — watch the oldest
  unpublished row age (see next item) while it does.
- **Outbox relay stuck / a row parked at the attempt cap.** A row whose `attempts`
  reached `OUTBOX_MAX_ATTEMPTS` (default 20) is EXCLUDED from future claims
  (`AND attempts < $2` in `internal/adapters/outbound/postgres/outbox_repository.go`)
  and stays in `outbox_events` with `attempts`/`last_error` for inspection; the relay
  logs `outbox relay: row reached the attempt cap and is excluded from future claims;
  re-arm it with attempts = 0 after fixing the cause` once at ERROR
  (`relay.go`, `AttemptCapReachedMsg`). Later rows are NOT blocked (head-of-line
  safe). Re-arm after fixing the cause:
  `UPDATE outbox_events SET attempts = 0 WHERE id = <id>;`
- **Consumer lag or retry logs.** The three consumed topics belong to their
  producers; if a producer has not written yet, its topic does not exist (topics are
  auto-created by the first writer) and the consumer only logs reader retries —
  benign. A transient handler failure retries the SAME message with capped
  exponential backoff (200ms doubling, cap 5s) up to 5 attempts, then dead-letters it
  to `<topic>.dlq`; a payload that can never be applied is dead-lettered at once
  (`internal/adapters/inbound/kafka/kafka.go`, `deadletter.go`). Effects are
  idempotent on the CloudEvents id (`processed_events`), so replaying a `.dlq` topic
  re-applies safely only if the original was never committed — inspect
  `processed_events` first.
- **A sibling REST URL unreachable.** The adapter maps an outage/timeout/5xx to
  `ports.ErrUpstreamUnavailable` (`httpclient/client.go`). Asynchronous vendor writes
  that need that sibling (node registration, profiles -> node-topology; inventory ->
  offer-availability) DEGRADE, not fail: the submission stays `PROCESSING` and is
  retried on later passes (`classify` in `usecases/submissions.go`). The synchronous
  endpoints answer `503 upstream-unavailable` (`errors.go`): `POST
  /vendor/shipping-labels` (shipping-and-tracking) and `GET
  /vendor/nodes/{id}/scorecard` (node-performance). Consumers and the REST API itself
  are unaffected.

## Operational actions

```sh
kubectl -n warehouse-systems rollout restart deploy/vendor-connectivity
kubectl -n warehouse-systems logs deploy/vendor-connectivity --follow
```

Logs in Grafana Loki: `{namespace="warehouse-systems", app="vendor-connectivity"}`
(JSON lines; `level`, `loop`, `consumer`, `error` fields). Restarts are graceful:
`/readyz` flips to 503, `SHUTDOWN_DRAIN_DELAY` (5s) waits, HTTP drains (10s), then
consumers/loops stop, the relay LAST (so nothing committed is stranded); the chart's
`terminationGracePeriodSeconds` must stay above drain delay + 30s. Any `config.*`
value change rolls the pods automatically (ConfigMap checksum annotation).
