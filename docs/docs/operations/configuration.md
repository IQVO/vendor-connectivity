---
id: configuration
title: Configuration
sidebar_label: Configuration
sidebar_position: 1
---

# Configuration

`vendor-connectivity` has **three binaries** (`cmd/api`, `cmd/mcp`, `cmd/a2a`) and
each is configured only through environment variables. There is no config file, no
flag and no secret-manager integration in the repository. REST, MCP and A2A are
unauthenticated fleet-wide, so there is no token or key to configure; access control is
the in-cluster network boundary.

The context runs on real Postgres and (in `cmd/api`) real Kafka only. Anything it cannot
run without is a boot error that names the variable, raised before a connection is
opened or the listener binds. Test-only variables do not exist in this repository
(the integration tests start their own containers).

The tables below list every variable each binary reads. A variable read by several
binaries has a row in each table with that binary's own default.

## `cmd/api`

Source: `cmd/api/main.go` (`loadRuntime`, `parseSettings`, `run`), `cmd/api/telemetry.go`,
`internal/adapters/outbound/telemetry/telemetry.go`.

| Variable | Default | Required? | Meaning |
| --- | --- | --- | --- |
| `HTTP_ADDR` | `:8080` | no | Listen address of the Vendor REST API (also serves `/healthz` and `/readyz`). |
| `DATABASE_URL` | none | **yes** | Postgres DSN (the pooled one). Missing = boot error `DATABASE_URL is required: this context runs on real Postgres, there is no in-memory mode`. Embedded migrations run at boot; repositories, outbox and the `Idempotency-Key` store live here. |
| `MIGRATIONS_DATABASE_URL` | value of `DATABASE_URL` | no | Direct (non-pooled) DSN used only for the boot migrations, because golang-migrate holds a session advisory lock that does not survive transaction pooling. Empty = `DATABASE_URL`. |
| `KAFKA_BROKERS` | none | **yes** | Comma-separated broker list (entries trimmed, blanks dropped). Missing = boot error `KAFKA_BROKERS is required: this context runs on real Kafka`. Used by the five consumers, the outbox relay and the `/readyz` metadata check. In the fleet's kind cluster the single broker is `localhost:9092` from the host. |
| `EVENT_PUBLISHER` | `kafka` | no | Only `kafka` (or empty) is accepted. `log` is a boot error (`EVENT_PUBLISHER=log is not supported`); any other value is a boot error. The variable stays because deployment manifests set it. |
| `NODE_CONSUMER_GROUP` | none | **yes** | Stable Kafka consumer group id of the node-topology consumer. Missing = boot error `NODE_CONSUMER_GROUP is required: the consumer always runs on real Kafka`. |
| `LISTING_CONSUMER_GROUP` | none | **yes** | Group id of the offer-availability listing consumer. |
| `SOURCING_CONSUMER_GROUP` | none | **yes** | Group id of the order-sourcing consumer. |
| `INBOUND_CONSUMER_GROUP` | none | **yes** | Group id of the seller-inbound shipment consumer ([ADR 0005](/docs/adr/0005-inbound-shipments-receipts-and-catalog-packaging)). |
| `PACKAGING_CONSUMER_GROUP` | none | **yes** | Group id of the catalogue-packaging consumer (offer-availability topic, ADR 0005). |
| `NODE_TOPOLOGY_URL` | none | **yes** | Base URL of node-topology (node registration, operational profiles). Missing = boot error `NODE_TOPOLOGY_URL is required: the in-organization call it names has no default`. |
| `OFFER_AVAILABILITY_URL` | none | **yes** | Base URL of offer-availability (inventory feeds). |
| `SHIPPING_AND_TRACKING_URL` | none | **yes** | Base URL of shipping-and-tracking (labels). |
| `NODE_PERFORMANCE_URL` | none | **yes** | Base URL of node-performance (scorecards). |
| `TX_PROCESSING_DELAY` | `0` | no | Least time a submission stays `PROCESSING`. A Go duration (`500ms`); zero is allowed, negative or unparsable is a boot error. Simulation knob, keep unset in normal operation. |
| `TX_FAILURE_RATE` | `0` | no | Share (0..1) of submissions that fail on purpose with `ErrSimulatedFailure`. Out of range is a boot error. Simulation knob. |
| `PO_REDELIVERY_RATE` | `0` | no | Chance (0..1) that a purchase-order poll page repeats its first order. Out of range is a boot error. Simulation knob. |
| `RATE_LIMIT_RPS` | `0` | no | Requests per second over `/vendor/*`; `0` = unlimited. Negative or unparsable is a boot error. |
| `SUBMISSION_POLL_INTERVAL` | `1s` | no | How often the submission processor handles due submissions. A positive Go duration; invalid or `0` is a boot error. |
| `EXPIRY_SWEEP_INTERVAL` | `30s` | no | How often the expiry sweeper expires orders not acknowledged by `ackBy`. A positive Go duration; invalid or `0` is a boot error. |
| `OUTBOX_RELAY_INTERVAL` | `1s` | no | Pause between relay passes. A Go duration or plain seconds (`2`, `0.5`). Unparsable or non-positive logs a WARN and uses the default. |
| `OUTBOX_MAX_ATTEMPTS` | `20` | no | Failures after which the relay stops claiming an outbox row. `0` = unlimited. Negative or unparsable logs a WARN and uses `20`. |
| `SHUTDOWN_DRAIN_DELAY` | `5s` | no | After SIGINT/SIGTERM `/readyz` flips to 503 and the process waits this long before closing the listener. `0` disables the wait. Negative or unparsable logs a WARN and uses `5s`. Read when the signal arrives. |
| `LOG_LEVEL` | `info` | no | `debug`, `info`, `warn` (or `warning`), `error`, case-insensitive; other values fall back to `info`. Logs are JSON on stdout. |
| `OTEL_SERVICE_NAME` | `vendor-connectivity` | no | `service.name` of spans and metrics. |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | `localhost:4317` | no | Collector OTLP/gRPC endpoint, `host:port` or full URL (a bare `host:port` becomes `http://host:port`). Export never blocks: an absent Collector costs telemetry, never availability. |
| `SERVICE_VERSION` | `dev` | no | `service.version` resource attribute. |
| `ENVIRONMENT` | `local` | no | `deployment.environment.name` resource attribute. |

Misconfiguration is refused one variable at a time (the first problem found is
returned), for example with an empty environment:

```text
DATABASE_URL is required: this context runs on real Postgres, there is no in-memory mode
```

With the mandatory variables set, the five consumers, the three loops and the outbox
relay all start; nothing is switched off by configuration.

## `cmd/mcp`

Source: `cmd/mcp/main.go`, `cmd/mcp/telemetry.go`. Read-only MCP over Streamable HTTP;
it starts no relay, consumer or loop and never dials Kafka
([ADR 0006](/docs/adr/0006-mcp-server-adoption)).

| Variable | Default | Required? | Meaning |
| --- | --- | --- | --- |
| `MCP_ADDR` | `:8090` | no | Listen address of the MCP server. |
| `DATABASE_URL` | none | **yes** | Postgres DSN of the service database `cmd/api` owns. Missing = boot error `DATABASE_URL is required: this context runs on real Postgres`. |
| `MIGRATIONS_DATABASE_URL` | value of `DATABASE_URL` | no | Direct DSN for the idempotent boot migrations (the MCP binary can boot on a fresh database before `cmd/api` has). |
| `NODE_TOPOLOGY_URL` | none | **yes** | The shared `Service` is built with all four neighbour clients, so all four URLs are required, as in `cmd/api`. |
| `OFFER_AVAILABILITY_URL` | none | **yes** | As above. |
| `SHIPPING_AND_TRACKING_URL` | none | **yes** | As above. |
| `NODE_PERFORMANCE_URL` | none | **yes** | As above; `get_node_scorecard` relays the scorecard from node-performance. |
| `LOG_LEVEL` | `info` | no | As `cmd/api`. |
| `OTEL_SERVICE_NAME` | `vendor-connectivity-mcp` | no | `service.name` of spans and metrics. |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | `localhost:4317` | no | As `cmd/api`. |
| `SERVICE_VERSION` | `dev` | no | As `cmd/api`. |
| `ENVIRONMENT` | `local` | no | As `cmd/api`. |

## `cmd/a2a`

Source: `cmd/a2a/main.go`, `cmd/a2a/telemetry.go`. Read-only A2A agent over JSON-RPC
2.0; no relay, consumer, loop or Kafka ([ADR 0009](/docs/adr/0009-a2a-inbound-adapter)).

| Variable | Default | Required? | Meaning |
| --- | --- | --- | --- |
| `A2A_ADDR` | `:8093` | no | Listen address of the A2A server. |
| `A2A_AGENT_URL` | none | **yes** | Absolute `http(s)` URL the Agent Card advertises for the JSON-RPC endpoint. Missing = boot error `A2A_AGENT_URL is required: the Agent Card advertises it and it has no default`; relative or non-http(s) = `A2A_AGENT_URL must be an absolute http(s) URL`. |
| `DATABASE_URL` | none | **yes** | Postgres DSN of the service database. |
| `MIGRATIONS_DATABASE_URL` | value of `DATABASE_URL` | no | Direct DSN for the boot migrations. |
| `NODE_TOPOLOGY_URL` | none | **yes** | Same four required neighbour URLs as `cmd/mcp`. |
| `OFFER_AVAILABILITY_URL` | none | **yes** | As above. |
| `SHIPPING_AND_TRACKING_URL` | none | **yes** | As above. |
| `NODE_PERFORMANCE_URL` | none | **yes** | As above; the `get_node_scorecard` skill relays from node-performance. |
| `LOG_LEVEL` | `info` | no | As `cmd/api`. |
| `OTEL_SERVICE_NAME` | `vendor-connectivity-a2a` | no | `service.name` of spans and metrics. |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | `localhost:4317` | no | As `cmd/api`. |
| `SERVICE_VERSION` | `dev` | no | As `cmd/api`. |
| `ENVIRONMENT` | `local` | no | As `cmd/api`. |

## Readiness

`GET /readyz` on `cmd/api` is 200 only when the shutdown drain gate is open **and**
Postgres answers a ping **and** a Kafka broker answers a metadata request that names no
topic (reachability only). The verdict is cached for 3 s and a round of checks is
bounded by 2 s. A failed dependency is `503` `application/problem+json`
(`dependency-unavailable`). `/healthz` is liveness only. Topics are not part of
readiness: the relay writer sets `AllowAutoTopicCreation`, so the broker creates a
topic on first write, and a consumed topic that does not exist yet only shows as
consumer retry logs. Source: `internal/adapters/inbound/http/readiness.go`, `cmd/api/main.go`
(`newReadiness`).

## Compiled-in settings (not configurable)

| Setting | Value | Source |
| --- | --- | --- |
| HTTP `ReadHeaderTimeout` | 5 s (all three binaries) | `cmd/*/main.go` |
| HTTP graceful shutdown / per-worker drain timeout | 10 s / 10 s (`cmd/api`); 10 s (`cmd/mcp`, `cmd/a2a`) | `cmd/api/main.go` (`shutdownTimeout`, `workerDrainTimeout`) |
| Retention sweep interval | 24 h | `cmd/api/main.go` (`retentionSweep`) |
| Postgres pool | 10 connections, `statement_timeout` set per connection | `internal/adapters/outbound/postgres/pool.go` |
| Boot retry (migrations, first ping) | 5 attempts, backoff 1 s, 2 s, 4 s, 8 s (15 s total), then the process refuses to boot | `internal/bootretry/bootretry.go` |
| Outbox relay batch size | 100 rows per pass | `internal/adapters/outbound/outbox/relay.go` (`DefaultBatchSize`) |
| Kafka producer | no fixed topic, `AllowAutoTopicCreation` | `internal/adapters/outbound/kafka/writer.go` |
| Submission attempts | a submission may end 5 passes without an outcome before it fails with `ErrUpstreamUnavailable`; page size 50 (max 200); processed submissions kept 30 days | `internal/application/usecases/service.go` |
| `Idempotency-Key` replay window | 24 h | `internal/adapters/inbound/http/idempotency.go` (`IdempotencyTTL`) |
| Topics produced | `retailnetwork.vendor-connectivity.events`, `retailnetwork.vendor-connectivity.analytics` | `internal/adapters/outbound/kafka/encoder.go` |
| Topics consumed | `retailnetwork.node-topology.events`, `retailnetwork.offer-availability.events`, `retailnetwork.order-sourcing.events`, `retailnetwork.seller-inbound.events` | `internal/adapters/inbound/kafka/consumers.go`, `inbound_consumers.go` |
| Dead-letter topic | `<topic>.dlq` | `internal/adapters/inbound/kafka/deadletter.go` |

## What is deliberately absent

- **No auth variables** on REST, MCP or A2A.
- **No Kafka variables on `cmd/mcp` and `cmd/a2a`**: they never publish or consume.
- **No CORS or feature-flag variables.**
- **Chart values.** `charts/vendor-connectivity` maps the `cmd/api` variables onto the
  pod (see [Runbook](/docs/operations/runbook#configuration)); the
  chart deploys `cmd/api` only.
