# ADR 0008: Adopt the fleet observability standard (logs, metrics, traces)

## Status

Accepted (2026-10-26).

## Context

vendor-connectivity logged JSON with `log/slog` and nothing else on the request
path: no trace or span id on a log line, no request id, no request log, no
metrics, no server spans, and no OpenTelemetry settings in the Helm chart. The
only trace-related piece was the outbox stamping `traceparent` into the stored
headers, which had no producer of spans and no consumer. The warehouse-systems
services (inventory-storage, wes-work-planning) already share one pipeline and
one convention, recorded in their ADRs "standard metrics convention"
(inventory-storage ADR 0016, wes-work-planning ADR 0013):

- pods export OTLP/gRPC to the in-cluster Collector
  (`otel-collector.observability.svc.cluster.local:4317`), which feeds Jaeger
  (traces) and a Prometheus exporter (metrics);
- logs stay on stdout as JSON for Alloy and Loki.

Without the same wiring a vendor call could not be followed from the REST
request through the Kafka event to the consumer in another service, a log line
could not be joined to its trace, and no dashboard could show request rate,
errors or latency. A service-specific setup was rejected (every repo should
answer the same query the same way), as were the OTel auto-instrumentation
agents (they cannot follow a trace across `segmentio/kafka-go`, nor name spans by
the chi route pattern). The same shape was adopted by customer-ordering
(its ADR 0007).

## Decision

Tier 1 (identical in every repo) and Tier 2 (this context's business metric):

1. **Telemetry adapter.** `internal/adapters/outbound/telemetry` holds `Setup`
   (OTLP/gRPC traces and metrics, W3C TraceContext + Baggage propagator, Go
   runtime metrics; exporters dial lazily, so no Collector means dropped
   telemetry, never a blocked boot or request; returns a shutdown func) and
   `WithTraceContext` (stamps `trace_id`/`span_id` on a log record written with a
   span-carrying context). Only the composition roots (`cmd/api`, `cmd/mcp`) and
   the other adapters import it; `internal/domain` and `internal/application`
   never import OpenTelemetry.
2. **Logger.** `slog.New(telemetry.WithTraceContext(slog.NewJSONHandler(os.Stdout, ...)))`,
   `LOG_LEVEL`, uppercase levels. Code on a request or message path uses the
   `*Context` slog variants; the bare variants have no span.
3. **HTTP middleware order** on both chi routers (REST and MCP):
   `RequestID` -> `otelchi.Middleware(serviceName, WithChiRoutes(r))` ->
   `otelchimetric.NewServerRequestDuration` -> `RequestLogger` -> `Recoverer`;
   the existing `rejectNUL`, rate limit and Idempotency-Key middleware stay after
   them. `telemetry.Setup` runs before the router is built (otelchimetric binds
   the global MeterProvider at construction) and is flushed last on shutdown.
   The request line carries `method`, `route` (the chi pattern, never the raw
   path, which holds po numbers and ids), `status`, `duration_ms`, `bytes` and
   `request_id`; 5xx is ERROR, 4xx WARN, the rest INFO; `/healthz` and `/readyz`
   are not logged. `RequestLogger` lives in `internal/adapters/inbound/requestlog`,
   not in the REST adapter, because `cmd/mcp` may not import the REST adapter
   (the MCP fitness test) and both routers must log identically.
4. **Traces over Kafka.** `OutboxRepo.Insert` stamps `traceparent` (and
   `tracestate`/`baggage` when present) from the active span into the stored
   outbox headers, inside the transaction that writes the aggregate; it existed
   before this ADR and is now covered by a unit and an integration test. The
   relay publishes the headers unchanged as Kafka headers; each of the five
   inbound consumers extracts them and runs every handling attempt in a consumer
   span named `<topic> process`, so the use case's logs carry the publisher's
   trace id. Only extra headers are added: the CloudEvents attributes and body
   are unchanged (ADR 0004).
5. **Environment contract.** `cmd/api` and `cmd/mcp` read `OTEL_SERVICE_NAME`
   (defaults `vendor-connectivity` and `vendor-connectivity-mcp`),
   `OTEL_EXPORTER_OTLP_ENDPOINT` (default `telemetry.DefaultEndpoint`),
   `SERVICE_VERSION`, `ENVIRONMENT` and `LOG_LEVEL`.
6. **Chart.** `otel.enabled`, `otel.endpoint`, `otel.serviceName` render
   `OTEL_EXPORTER_OTLP_ENDPOINT` and `OTEL_SERVICE_NAME` (`<name>-mcp` for the
   mcp component); `SERVICE_VERSION` is the image tag, or the chart appVersion
   when empty, for the api and the mcp component; `environment` and
   `mcp.environment` render `ENVIRONMENT`. `extraEnv` and `mcp.extraEnv` are
   refused when they set a variable the chart owns.
   `charts/vendor-connectivity/tests/render_test.sh` asserts it and scans `cmd/`
   for the variables the binaries read. The frontend component serves static
   files and gets none of this.
7. **Tier 2 business metric.** One counter, `vendor.submissions.processed`,
   unit `{submission}`, attribute `outcome` (`succeeded` | `failed`) and, on a
   failure, `reason` (the Err code the vendor reads in the transaction, for
   example `ErrUpstreamUnavailable`, `ErrSimulatedFailure`,
   `ErrMalformedPayload`). `ports.SubmissionMetrics` is the application port;
   `ProcessSubmission` calls it once per submission that reached an outcome,
   after the unit of work committed (a pass that leaves the submission
   `PROCESSING` records nothing); the OTel implementation is in the telemetry
   adapter and is injected from `cmd/api`; a nil `Deps.Metrics` is a no-op.

   Why this one: every Vendor API write except a label returns `202` plus a
   `transactionId` and takes effect only when the transaction reports `SUCCESS`
   (apis/openapi.yaml, ADR 0001), so submissions are the primary command
   pipeline of the context and the only place where a vendor is told "your write
   did not apply". A rising `failed` share with `ErrUpstreamUnavailable` means a
   neighbour stopped answering, not that vendors send bad data. Counting at the
   processing step, not at the HTTP edge, keeps request rate and errors with
   otelchimetric and gives a metric the domain actually decides.

Skipped from the reference: nothing; the repo has an MCP server, consumers, an
outbox and a chart. One consequence of the repo's shape: five consumers, not
three.

Not part of this ADR: dashboards and alert rules, which live in warehouse-infra
and are generated per deployed context from the instrument names above.

### Assumptions

- O1: the metric name prefix is `vendor` (the Ubiquitous
  Language noun of the context), not `vendor_connectivity`.
- O2: the `reason` attribute reuses the Err code already shown to vendors; it
  is a closed, low-cardinality set (the use case sets one of the `Code*`
  constants or the neighbour's refusal code).

## Consequences

- A request, its log lines, the events it enqueued and the consumers that handle
  them in other services share one trace id; `trace_id` in Loki jumps to Jaeger.
- Request rate, errors and duration come from `http.server.request.duration`
  (otelchimetric) with no hand-rolled counter; submission health from
  `vendor.submissions.processed`.
- Every new log call on a request or message path must use a `*Context` variant,
  or its line carries no trace id.
- A consumer span is created per handling attempt, so a retried message shows as
  several spans under the same parent.
- The trace parent of a consumed event is the span active at the outbox write
  (the REST request or the upstream consumer span); the relay adds no span.
