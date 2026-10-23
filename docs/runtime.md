# Runtime: configuration, probes, shutdown and the outbox

`cmd/api` is the only binary. It runs on real PostgreSQL and real Kafka; there is no
alternative runtime and no log sink.

## Environment

| Variable | Default | Meaning |
| --- | --- | --- |
| `DATABASE_URL` | required | Postgres DSN of the service's pool. A boot error names it when unset. |
| `MIGRATIONS_DATABASE_URL` | `DATABASE_URL` | Direct DSN for the boot migrations (golang-migrate holds a session advisory lock, which breaks behind a transaction-pooling proxy). |
| `KAFKA_BROKERS` | required | Comma-separated `host:port` list (relay, consumers and `/readyz`). A boot error names it when unset or blank. |
| `NODE_CONSUMER_GROUP`, `LISTING_CONSUMER_GROUP`, `SOURCING_CONSUMER_GROUP` | required | Stable consumer group ids of the node-topology, offer-availability and order-sourcing consumers. A boot error names the missing one. |
| `NODE_TOPOLOGY_URL`, `OFFER_AVAILABILITY_URL`, `SHIPPING_AND_TRACKING_URL`, `NODE_PERFORMANCE_URL` | required | Base URLs of the four synchronous in-org calls (ADR 0002 A24). |
| `EVENT_PUBLISHER` | `kafka` | Only `kafka` is accepted; any other value is a boot error. |
| `HTTP_ADDR` | `:8080` | Listen address. |
| `TX_PROCESSING_DELAY` | `0` | Least time a submission stays `PROCESSING` (Go duration). |
| `TX_FAILURE_RATE` | `0` | Share (0..1) of submissions that end `FAILURE` with `ErrSimulatedFailure`. |
| `PO_REDELIVERY_RATE` | `0` | Chance (0..1) that a poll page repeats its first order. |
| `RATE_LIMIT_RPS` | `0` | Requests a second over `/vendor/*`; `0` = unlimited. Over the limit: 429 `rate-limited`. |
| `SUBMISSION_POLL_INTERVAL` | `1s` | How often due submissions are processed. |
| `EXPIRY_SWEEP_INTERVAL` | `30s` | How often unacknowledged orders past `ackBy` are expired. |
| `OUTBOX_RELAY_INTERVAL` | `1s` | Poll interval of the relay (Go duration or seconds). |
| `OUTBOX_MAX_ATTEMPTS` | `20` | Failures before the relay excludes a row; `0` = unlimited. |
| `SHUTDOWN_DRAIN_DELAY` | `5s` | Wait between flipping `/readyz` to 503 and closing the listener; `0` disables. |
| `LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error`. |

Configuration errors are raised before anything is opened or bound.

## Boot and stop

1. Validate the environment. 2. Run the embedded migrations and ping Postgres, both with
backoff. 3. Start the outbox relay, the three consumers and the three loops (submission
processor, expiry sweeper, daily retention sweeper). 4. Serve.

On SIGINT/SIGTERM: `/readyz` flips to 503 `not_ready`, wait `SHUTDOWN_DRAIN_DELAY`, drain
HTTP (10 s), stop and await the consumers and loops (10 s), then the relay (10 s), close the
pool. If a consumer, a loop or the relay returns on its own, the process exits non-zero so
the orchestrator restarts it.

## Probes

- `GET /healthz` is liveness: 200 `{"status":"ok"}` while the process serves, even while
  draining or while a dependency is down.
- `GET /readyz` is 200 only if the drain gate is open, Postgres answers a ping and a Kafka
  broker answers a metadata request. The request names no topics (an empty, non-nil list), so
  it never creates a topic; a fresh connection is dialled on every check. While draining it
  answers 503 `not_ready`; while a dependency is down, 503 `application/problem+json` of type
  `dependency-unavailable` naming it. Verdicts are cached 3 s.

## The outbox and the consumers in operation

- Events are written in the same transaction as the aggregates and published later, so a write
  is accepted while Kafka is down; `outbox_events.published_at IS NULL` rows wait and are
  published, in id order, when the broker is back. Delivery is at-least-once with a stable
  CloudEvents `id`.
- A row that failed `OUTBOX_MAX_ATTEMPTS` times is skipped (logged once at ERROR with id,
  topic, type and key) and stays in the table with `attempts` and `last_error`. Fix the cause,
  then re-arm it: `UPDATE outbox_events SET attempts = 0 WHERE id = <id>;`.
- Consumers claim each CloudEvents id in `processed_events` in the same transaction as their
  writes. A transient failure is retried, then published to `<topic>.dlq`; a payload that can
  never decode goes there at once. A release for a node the view does not know yet is retried
  (ADR 0002 A4).
- Idempotency keys older than 24 h are purged by the daily retention sweep (an expired key is also
  replaced on its next use); processed submissions are purged after 30 days by the same sweep.
