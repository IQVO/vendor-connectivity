---
id: observability
title: Observability
sidebar_label: Observability
sidebar_position: 3
---

# Observability

The fleet pipeline ([ADR 0008](/docs/adr/0008-fleet-observability-standard)) is:
pod, then OTLP/gRPC to the OpenTelemetry Collector, then Jaeger (traces) and Prometheus
(metrics); logs are JSON on stdout, collected by Alloy into Loki. All three binaries
(`cmd/api`, `cmd/mcp`, `cmd/a2a`) bring telemetry up before they build their router
(`cmd/*/telemetry.go`, `internal/adapters/outbound/telemetry/telemetry.go`). Export never
blocks: the gRPC exporters connect lazily, so a missing Collector costs telemetry and
nothing else.

## Exporter configuration

| Variable | Default | Effect |
| --- | --- | --- |
| `OTEL_SERVICE_NAME` | `vendor-connectivity` (`cmd/api`), `vendor-connectivity-mcp`, `vendor-connectivity-a2a` | `service.name` of spans and metrics, and the `service_name` label in Prometheus. |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | `localhost:4317` | Collector OTLP/gRPC endpoint, `host:port` or URL; a bare `host:port` is rewritten to `http://host:port`. The chart sets `otel-collector.observability.svc.cluster.local:4317`. |
| `SERVICE_VERSION` | `dev` | `service.version` resource attribute (the chart passes the image tag). |
| `ENVIRONMENT` | `local` | `deployment.environment.name` resource attribute (chart: `production`). |
| `LOG_LEVEL` | `info` | Log threshold; see [Configuration](/docs/operations/configuration). |

Metrics are pushed every 15 s by a periodic reader. On shutdown the providers are flushed
with a 5 s deadline; a failed flush is a WARN (`telemetry flush failed on shutdown`), never
a non-zero exit. Exporter errors are logged at DEBUG (`opentelemetry error`), so set
`LOG_LEVEL=debug` to hunt a pipeline problem. With `otel.enabled=false` the chart renders
no `OTEL_*` variables and the binaries fall back to `localhost:4317`.

## Metrics

| Instrument | Type | Unit | Attributes | Emitted by |
| --- | --- | --- | --- | --- |
| `vendor.submissions.processed` | counter (int64) | `{submission}` | `outcome` = `succeeded` or `failed`; `reason` (only when failed) = the `Err` code the vendor reads, such as `ErrUpstreamUnavailable` or `ErrSimulatedFailure` | `cmd/api` (the submission processor). Source: `internal/adapters/outbound/telemetry/submission_metrics.go`. |
| `http.server.request.duration` (otelchi metric middleware, base config of the service name) | histogram | seconds | `http.route` and the status and method attributes the otelchi metric package sets (the dashboard queries it by `http_route`) | every HTTP binary. Source: `otelchimetric.NewServerRequestDuration` in `internal/adapters/inbound/http/server.go`, `cmd/mcp/router.go`, `cmd/a2a/router.go`. |
| Go runtime metrics (goroutines, GC, heap) | gauges and counters from `go.opentelemetry.io/contrib/instrumentation/runtime` | per instrument | none of its own | every binary, started by `telemetry.Setup`. |

`vendor.submissions.processed` is the context's only business instrument. It counts a
submission when its transaction reaches `SUCCESS` or `FAILURE`; Prometheus exposes it as
`vendor_submissions_processed_total`. A rising `failed` share with
`reason=ErrUpstreamUnavailable` means a neighbour context stopped answering, not that
vendors sent bad data (see [Troubleshooting](/docs/operations/troubleshooting)).

There is no instrument for outbox depth, consumer lag, dead letters or idempotency
replays. Read those from the database and logs (see the [Runbook](/docs/operations/runbook)).

## Traces

| Span | Kind | Where | Notes |
| --- | --- | --- | --- |
| Server span named after the chi route pattern | server | `otelchi.Middleware` on every HTTP binary (REST, MCP, A2A) | Mounted right after `RequestID`, before the request logger, so logs carry the trace. |
| `<topic> process` | consumer | `internal/adapters/inbound/kafka/tracing.go` (`handleTraced`) | Attributes `messaging.system=kafka`, `messaging.destination.name`, `messaging.operation.name=process`. Parented to the trace in the W3C `traceparent` header of the message; a message without it starts a new trace. A handler error is recorded on the span and sets status Error. |

Propagation is W3C Trace Context plus Baggage. The outbox row keeps the trace context of
the transaction that wrote it so the relay's publish continues the same trace
(`internal/adapters/outbound/postgres/outbox_repository.go`, `traceHeaders`).

## Logs

JSON on stdout through `slog`; a record written with a span-carrying context also carries
`trace_id` and `span_id` (`telemetry.WithTraceContext`, `slogotel.go`). Fields you can
query:

| Field | Where | Meaning |
| --- | --- | --- |
| `method`, `route`, `status`, `duration_ms`, `bytes`, `request_id` | one line per REST request, `internal/adapters/inbound/requestlog/requestlog.go` | `route` is the chi pattern, not the raw path. |
| `loop`, `interval`, `count` | `loop running`, `loop pass done`, `loop pass failed` | Names are the three loops: submission processor, expiry sweeper, retention sweeper. |
| `consumer`, `topic`, `type`, `event_id`, `error` | consumer WARN/ERROR lines | `skipping a message that is not a valid CloudEvent`, `dead-lettering an event that can never be applied`, `... handling failed; retrying the same message`. |
| `outbox_id`, `topic`, `event_type`, `key`, `attempts`, `max_attempts`, `error` | the ERROR line when a row reaches the attempt cap | Re-arm procedure in the [Runbook](/docs/operations/runbook). |
| `worker`, `drain_delay` | shutdown lines | `shutdown: readiness flipped to not-ready`, `worker did not stop before the shutdown drain deadline`. |
| `transactionId`, `poNumber`, `state` | `processed submission found its order in another state; order left as is` | WARN from the submission processor. |

The pod label `app.kubernetes.io/name` becomes the Loki label `app`:
`{namespace="warehouse-systems", app="vendor-connectivity"}`.

## Dashboards and alerts that exist

Only what is in warehouse-infra `develop` (`terraform/dashboards/contexts/vendor-connectivity.json`,
`terraform/alerting/warehouse-alert-rules.json`):

- **Dashboard** `warehouse-vendor-connectivity` ("Warehouse - Vendor Connectivity"):
  Kong request rate, 5xx ratio and p95 latency (request and upstream) for the context's
  routes; request rate and p95 duration by `http_route` from
  `http_server_request_duration_seconds` for `service_name=~"vendor-connectivity.*"`; the
  business panel `sum by (outcome) (rate(vendor_submissions_processed_total[5m]))`; Go
  goroutines and memory; and three Loki panels (live stream, volume by level, errors and
  warnings).
- **Alert group `warehouse-vendor-connectivity`**: `WarehouseKong5xxRatioHigh` (5xx above
  5 % on the context's Kong routes) and `WarehousePodRestarting` (two or more container
  restarts in 15 min for pods `vendor-connectivity-.+`).
- **Alert group `warehouse-vendor-connectivity-slo`**: `WarehouseApiAvailabilityBurnFast`
  and `WarehouseApiLatencyBurnFast` (critical, 14.4x burn over 1 h and 5 m, at least 30
  requests), `WarehouseApiAvailabilityBurnSlow` and `WarehouseApiLatencyBurnSlow` (warning,
  3x burn over 6 h and 30 m). The availability objective is 1 % errors, the latency
  objective 5 % of upstream calls over 400 ms.
- **No `WarehouseOutboxLagHigh` rule exists for this context**, and no alert is based on
  `vendor.submissions.processed`. All six rules link to the runbook path
  `docs/operations/runbook.md` on `develop`; that file now lives under `docs/docs/operations/`.
