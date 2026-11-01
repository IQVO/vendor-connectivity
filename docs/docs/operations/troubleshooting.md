---
id: troubleshooting
title: Troubleshooting
sidebar_label: Troubleshooting
sidebar_position: 4
---

# Troubleshooting

Symptom, cause, check, fix, built from the failure modes visible in the code. Log
messages are quoted from the source; REST problem slugs from
`internal/adapters/inbound/http/errors.go`. SQL probes assume the Postgres schema in the
[Runbook](/docs/operations/runbook). Nothing here was captured from a live run of this
branch. The context has **no circuit breaker**: the four sibling REST calls are plain
HTTP calls with timeouts (`internal/adapters/outbound/httpclient/client.go`), and the
dependencies that can fail are Postgres, Kafka and those four services.

## Boot

| Symptom | Likely cause | Check | Fix |
| --- | --- | --- | --- |
| Process exits 1 at once, `service exited with error` with `DATABASE_URL is required: this context runs on real Postgres, there is no in-memory mode` | `DATABASE_URL` unset | The `error` field of the ERROR line (`cmd/api/main.go`) | Set it ([Configuration](/docs/operations/configuration)). There is no in-memory runtime to fall back to |
| Exits 1 with `KAFKA_BROKERS is required: this context runs on real Kafka (outbox relay, consumers, /readyz)` | Empty or blank `KAFKA_BROKERS` | Same line | Set `host:port[,host:port]`. Only `cmd/api` needs it; `cmd/mcp` and `cmd/a2a` never read it |
| Exits 1 with `<NAME> is required: the consumer always runs on real Kafka` (`NODE_CONSUMER_GROUP`, `LISTING_CONSUMER_GROUP`, `SOURCING_CONSUMER_GROUP`, `INBOUND_CONSUMER_GROUP`, `PACKAGING_CONSUMER_GROUP`) | A consumer group id is unset. The first missing one is reported, then the next boot reports the next | Pod env; chart `config.*ConsumerGroup` | Set all five stable names (the chart refuses to render without the first three plus the two inbound ones) |
| Exits 1 with `<NAME> is required: the in-organization call it names has no default` | One of `NODE_TOPOLOGY_URL`, `OFFER_AVAILABILITY_URL`, `SHIPPING_AND_TRACKING_URL`, `NODE_PERFORMANCE_URL` is unset. `cmd/mcp` and `cmd/a2a` refuse the same four | Pod env | Set the base URLs; in-cluster they are `http://<context>.warehouse-systems.svc.cluster.local:80` |
| Exits 1 with `EVENT_PUBLISHER=log is not supported: this context publishes to real Kafka` or `EVENT_PUBLISHER: unknown value "x" (want kafka)` | A manifest still sets the retired `log` publisher | Pod env | Remove the variable or set `kafka` |
| Exits 1 with `TX_PROCESSING_DELAY: "abc" is not a valid duration (want a positive Go duration such as 500ms)`, or `SUBMISSION_POLL_INTERVAL` / `EXPIRY_SWEEP_INTERVAL` with the same text | Not a Go duration, or negative; zero is refused for the two intervals (it is allowed only for `TX_PROCESSING_DELAY`) | `cmd/api/config.go` `parseDuration` | Use `500ms`, `2s`, ... or unset for the default |
| Exits 1 with `TX_FAILURE_RATE: "2" is not a number between 0 and 1` (same for `PO_REDELIVERY_RATE`) or `RATE_LIMIT_RPS: "x" is not a number >= 0` | Malformed or out-of-range knob | Pod env | Fix the value; leave the simulation knobs unset in production |
| A typo in `OUTBOX_RELAY_INTERVAL`, `OUTBOX_MAX_ATTEMPTS` or `SHUTDOWN_DRAIN_DELAY` does **not** stop the boot | These three log a WARN (`invalid OUTBOX_RELAY_INTERVAL; using the default`, `invalid OUTBOX_MAX_ATTEMPTS; using the default`, `ignoring invalid SHUTDOWN_DRAIN_DELAY`) and use the default | Boot logs | Fix the value; the WARN names it |
| Exits 1 after a few seconds: `run migrations (after N attempts): ...` or `ping postgres (after N attempts): ...` | Postgres unreachable, wrong DSN, or the migration itself fails. The retry exists because the first dial of a sidecar-injected pod can be reset (`internal/bootretry`) | WARN lines `retrying` with the operation and error; `psql` from the same network | Fix the DSN or the network |
| Migrations fail or hang behind PgBouncer | `DATABASE_URL` is a transaction pooler, which cannot hold golang-migrate's session advisory lock | Is the DSN a pooler address? | Set `MIGRATIONS_DATABASE_URL` to a direct DSN (`cmd/api`, `cmd/mcp` and `cmd/a2a` all read it and fall back to `DATABASE_URL`) |
| Every later boot fails with a migration error after one failed boot | A partial migration left `schema_migrations.dirty = true`; golang-migrate refuses to continue | `SELECT version, dirty FROM schema_migrations` | Repair the schema by hand, then force the version with the golang-migrate tooling; back up first |
| The pod restarts on its own with `<name> stopped unexpectedly: exiting so the orchestrator restarts the service` | A worker (relay, a consumer or a loop) returned on its own; for a consumer, `consumer stopped` with the fetch error precedes it. A Kafka connection that stays unreachable for more than 10 consecutive fetch attempts fails the consumer on purpose (`maxTransientFetchFailures`) | The ERROR `consumer stopped` / `outbox relay stopped` line before the exit | Restore the broker. Short stalls (idle read timeouts, resets) are retried with 200 ms-to-5 s backoff and logged as `... broker connection stalled or lost; fetching again after the backoff` |

## Probes and readiness

| Symptom | Likely cause | Check | Fix |
| --- | --- | --- | --- |
| `/readyz` is 503 with `{"status":"not_ready"}` | The process is shutting down; the drain gate flips on SIGINT/SIGTERM | Is the pod terminating? | Nothing; the replacement takes over |
| `/readyz` is 503 `application/problem+json` slug `dependency-unavailable` naming `postgres` and/or `kafka` | Postgres does not answer a ping, or no Kafka broker answers a topic-less metadata request. A missing topic never causes this: topics are auto-created by the first writer | The `/readyz` body; `internal/adapters/inbound/http/readiness.go` | Restore the dependency. The verdict is cached 3 s with a 2 s bound, so recovery is automatic |
| `/readyz` is 200 but requests fail with 500 `internal-error` | A single statement failing for another reason (constraint, timeout, bug); readiness only proves the database answers a ping | The request log line (`status`, `route`, `request_id`), then the ERROR next to it; the problem body never carries the cause | Fix the cause shown in the log |
| `cmd/mcp` or `cmd/a2a` pod is never ready though `cmd/api` is | They expose only `/healthz` (liveness, always 200 while serving); a pod that restarts is failing at boot, not at probe time | Boot error line `mcp server exited with error` or its A2A counterpart | Same fixes as Boot: `DATABASE_URL` and the four neighbour URLs |

## REST writes

| Symptom | Likely cause | Check | Fix |
| --- | --- | --- | --- |
| 422 `idempotency-key-required` | A write under `/vendor` without an `Idempotency-Key` header, or one longer than 128 characters | The request headers | Send a unique key of 1 to 128 characters per logical request |
| 422 `idempotency-key-reused` | The same key and route were used with a different body (compared by SHA-256 of the body) | `SELECT route, key, created_at FROM idempotency_keys WHERE key = '<key>'` | Use a new key for a new request; replay the identical body to get the stored answer |
| A retried write returns the old answer, not a new one | Working as designed: every response below 500 (4xx included) is stored for 24 h and replayed for the same key and body. A 5xx is rolled back and never stored | `idempotency_keys.status_code` | Use a fresh key to try again after fixing the cause |
| 429 `rate-limited` with `Retry-After` | `RATE_LIMIT_RPS` is set and the per-process token bucket (burst = max(1, rps)) is empty | The `Retry-After` header | Back off, or raise/unset the variable (0 = unlimited). The bucket is per pod, not shared |
| 422 `validation-failed` | Malformed JSON, unknown field, wrong type or a failed domain validation | The `detail` of the problem | Fix the request against `apis/openapi.yaml` |
| 422 `partial-acknowledgement` | The acknowledgement did not cover every purchase order line with one decision | Line count of the order | Send one decision per line |
| 422 `ack-window-out-of-range` | The vendor onboarding request asks for an acknowledgement window outside 1 h to 24 h (`ackWindowMinutes` 60 to 1440) | Request body | Use a value in range |
| 404 `node-not-owned-by-vendor` | The node exists but belongs to another vendor, or the local node copy has not caught up | `SELECT * FROM vendor_nodes WHERE node_id = '<id>'` | Check the vendor id; if the node is new, wait for the `NodeRegistered` event to reach the node consumer |
| 404 `not-found` on a purchase order | The order was never released to this vendor: `FulfillmentReleasedToNode` has not been consumed, or it failed | `SELECT po_number, state FROM purchase_orders ORDER BY issued_at DESC LIMIT 5`; consumer logs | See "Consumers" below |
| 409 `vendor-suspended` | The vendor account is suspended | `SELECT state FROM vendor_accounts WHERE vendor_id = '<id>'` | Reinstate it with `POST /vendors/{vendorId}:reinstate` (with an `Idempotency-Key`); see [use cases](/docs/ddd/use-cases). Reads stay allowed while suspended |
| 409 `already-acknowledged`, `ack-window-closed`, `cancel-after-acknowledgement`, `label-before-acceptance`, `confirm-without-label`, `illegal-transition` | The purchase order is in a state that does not allow the command (domain invariants in `internal/domain/purchaseorder`) | `GET /vendor/purchase-orders/{poNumber}` for its state | Follow the order lifecycle: acknowledge in full inside the window, then label, then confirm |
| 409 `concurrent-update` | Two writers raced on the same aggregate. The service already re-runs a unit once on a lost optimistic-concurrency check | A repeat of the request | Re-fetch and retry |
| 409 `submission-not-due` / `submission-already-processed` | Internal skip results of the submission processor; seen only when something calls the processor directly | Not expected on a vendor route | Ignore; the loop skips them |
| 503 `upstream-unavailable` on `POST /vendor/shipping-labels` or `GET /vendor/nodes/{nodeId}/scorecard` | The synchronous neighbour (shipping-and-tracking, node-performance) timed out, was unreachable or answered 5xx | `SHIPPING_AND_TRACKING_URL` / `NODE_PERFORMANCE_URL`; the neighbour's own health | Fix or restore the neighbour, then retry; the label call is idempotent through the `Idempotency-Key` |
| 502 `upstream-refused` | The neighbour answered a 4xx; the adapter turns it into a refusal carrying the neighbour's problem slug (`internal/adapters/outbound/httpclient/client.go`). A call that exceeds the 5 s client timeout is `upstream-unavailable` instead | The `detail` | Fix the request or the neighbour's rules |

## Asynchronous transactions

Vendor writes for nodes, profiles and inventory (and acknowledgements and shipment
confirmations) return a `transactionId` and are executed later by the submission
processor (`SUBMISSION_POLL_INTERVAL`, default 1 s).

| Symptom | Likely cause | Check | Fix |
| --- | --- | --- | --- |
| A transaction stays `PROCESSING` | `TX_PROCESSING_DELAY` holds it; or its neighbour is down and the processor retries each pass; or the processor loop is failing | `SELECT transaction_id, kind, status, attempts, not_before FROM submissions WHERE status = 'PROCESSING'`; `loop pass failed` with `loop=submission processor` | Remove the delay knob; restore the neighbour. After 5 passes without an outcome (`DefaultSubmissionMaxAttempts`, not configurable by env) the transaction fails with `ErrUpstreamUnavailable` and `vendor.submissions.processed{outcome=failed,reason=ErrUpstreamUnavailable}` counts it |
| Transactions fail with `ErrSimulatedFailure` | `TX_FAILURE_RATE` is above 0 and the knob fires on purpose | Pod env | Unset it. It is a vendor-integration test knob, not an error |
| Transaction fails with `ErrMalformedPayload` | The stored payload no longer decodes into the request type | `submissions.payload` | Resubmit with a valid body |
| `processed submission found its order in another state; order left as is` (WARN) | The order moved on (cancelled or expired) between receipt and processing | `transactionId`, `poNumber`, `state` fields of the line | Nothing; the submission completes and the order is left as is |
| Orders sit past `ackBy` without expiring | The expiry sweeper is not running or failing (`EXPIRY_SWEEP_INTERVAL`, default 30 s) | `loop running` with `loop=expiry sweeper` at boot; `loop pass failed` | Fix the error shown; the pass is idempotent |
| `submissions` or `idempotency_keys` grow without bound | The 24 h retention sweeper is failing (processed submissions are purged after 30 days, idempotency keys after 24 h) | `loop pass failed` with `loop=retention sweeper` | Fix the error shown |

## Consumers and the outbox

| Symptom | Likely cause | Check | Fix |
| --- | --- | --- | --- |
| Reader retry logs only, no events applied | The producer has not written yet, so its topic does not exist (the first writer auto-creates it) | `consumer running` lines list `topic` and `brokers` | Benign until the producer publishes |
| `skipping a message that is not a valid CloudEvent` (WARN) | A record on the topic is not CloudEvents 1.0 JSON; it is committed past | `consumer`, `topic` and `error` fields | Fix the producer; nothing is retried |
| A message type is ignored silently | The consumer dispatches on the full `type` string and has no handler for it (every other event on the shared topics, such as the rest of offer-availability's catalogue) | The handler maps in `internal/adapters/inbound/kafka/consumers.go` and `inbound_consumers.go` | Expected; see [Integration](/docs/ecosystem/integration) for the types that are consumed |
| `dead-lettering an event that can never be applied` (WARN) | The `data` of a handled type does not decode. The message goes to `<topic>.dlq` at once, without retries | `type`, `event_id` and `error` fields; the `x-dlq-*` headers on the DLQ record | Fix the producer; replay only after reading `processed_events` (below) |
| `<consumer> handling failed; retrying the same message` (ERROR), then `... handling failed after the maximum attempts; dead-lettering the message` | A transient failure (database down, unit of work rolled back): 200 ms doubling to 5 s, 5 attempts, then the DLQ | The `attempt`, `max_attempts`, `partition`, `offset` fields | Restore the database; the partition is blocked until the message is handled or dead-lettered, never dropped |
| `... dead-letter publish failed; retrying the same message` repeats | The DLQ write fails (broker). A message that could not be dead-lettered is never skipped | Broker health | Restore Kafka |
| Replaying a `.dlq` record applied twice, or not at all | Effects are idempotent on the CloudEvents `id` in `processed_events` (claim and effect commit in one transaction), so a replay of an event whose first attempt rolled back applies; one that committed is skipped | `SELECT * FROM processed_events WHERE event_id = '<id>'` | Inspect before replaying |
| Events are not appearing on `retailnetwork.vendor-connectivity.events` | Rows are stuck in the outbox (`published_at IS NULL`): Kafka down, or the relay stopped | `SELECT count(*), min(created_at) FROM outbox_events WHERE published_at IS NULL`; `outbox relay running` at boot | Restore Kafka; the backlog drains itself |
| ERROR `outbox relay: row reached the attempt cap and is excluded from future claims; re-arm it with attempts = 0 after fixing the cause` | The row failed `OUTBOX_MAX_ATTEMPTS` times (default 20). It is excluded from claims (`attempts < $2`); later rows are not blocked | `outbox_id`, `topic`, `event_type`, `key`, `error` fields; `outbox_events.last_error` | Fix the cause, then `UPDATE outbox_events SET attempts = 0 WHERE id = <id>` |

## MCP and A2A

| Symptom | Likely cause | Check | Fix |
| --- | --- | --- | --- |
| MCP client gets a tool error `internal-error` ("an unexpected internal error occurred") | An unmapped failure inside a read use case | `request failed` / ERROR next to the request log line | Fix the cause; see the problem table above for the mapped errors (`not-found`, `upstream-unavailable`) |
| `get_node_scorecard` fails while every other tool works | It relays from node-performance (`NODE_PERFORMANCE_URL`); the others read the local database | Neighbour health | Restore node-performance |
| An MCP client cannot connect over stdio or SSE | Only Streamable HTTP is served, on `/mcp` and `/` | `MCP_ADDR` (default `:8090`) | Point the client at `http://<host>:8090/mcp` |
| A2A skill errors with `a2a skill failed` (ERROR) | The underlying read failed | The `error` field | Same as the matching REST read |
| A2A Agent Card lists the wrong URL | `A2A_AGENT_URL` unset or wrong | `GET /.well-known/agent-card.json` | Set `A2A_AGENT_URL` to the externally reachable URL |
| Tracing is missing but logs are fine | The Collector is unreachable; export never blocks and errors surface only at DEBUG | `LOG_LEVEL=debug`, line `opentelemetry error` | See [Observability](/docs/operations/observability) |
