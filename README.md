# Vendor Connectivity

Retail network, bounded context **Vendor Connectivity** (Supporting Subdomain,
partner tier): the network's **Open Host Service** for vendors that operate
fulfillment nodes. Publishes the Vendor API and its Published Language:
vendors register nodes, declare their operational profile, send inventory
feeds, poll purchase orders, acknowledge them in full inside the
acknowledgement window, request labels and confirm shipment, and poll the
SSCC/GTIN trees of seller inbound shipments and report what their dock
received. Every write is an asynchronous transaction a vendor reconciles. It
owns the vendor accounts, purchase orders (as the vendor sees them),
submissions and inbound receipts — the topology facts stay in
`node-topology`, listings in `offer-availability`, transport in
`shipping-and-tracking`; this service is the single seam the
warehouse-systems fleet uses to reach the retail network.

## Disclaimer

This project is a domain-driven design study. It is not associated with, endorsed by or affiliated with any company, and it uses no company's code, data, logos or trademarks. All names, figures and examples are illustrative or synthetic.

## Documentation

The context is documented on the fleet-wide warehouse-docs site, in the
retail-network section: **<https://iqvo.github.io/warehouse-docs/network/contexts/vendor-connectivity>**
(the model under `network/model/model.yaml` in IQVO/warehouse-docs is the
single source of truth). Fleet-wide network pages:
[strategic design](https://iqvo.github.io/warehouse-docs/network/strategic-design/subdomain-classification),
[the CloudEvents event standard](https://iqvo.github.io/warehouse-docs/network/strategic-design/event-standard-cloudevents),
the [Vendor API](https://iqvo.github.io/warehouse-docs/network/integration/vendor-api)
and the [WMS/WES integration seam](https://iqvo.github.io/warehouse-docs/network/integration/wms-wes-integration).
This repository's own references: [`docs/runtime.md`](docs/runtime.md) (all
settings, the probes, the shutdown order and the outbox runbook) and the
packaging checklist [`docs/before-first-release.md`](docs/before-first-release.md).

Page map (on the warehouse-docs site, under `network/contexts/vendor-connectivity/`):

| Area | Pages |
|---|---|
| Business context | [Business context](https://iqvo.github.io/warehouse-docs/network/contexts/vendor-connectivity/business-context), [Use cases](https://iqvo.github.io/warehouse-docs/network/contexts/vendor-connectivity/use-cases) |
| Language & model | [Ubiquitous language](https://iqvo.github.io/warehouse-docs/network/contexts/vendor-connectivity/ubiquitous-language), [Domain events](https://iqvo.github.io/warehouse-docs/network/contexts/vendor-connectivity/domain-events), [Domain message flow](https://iqvo.github.io/warehouse-docs/network/contexts/vendor-connectivity/domain-message-flow), [Class diagram](https://iqvo.github.io/warehouse-docs/network/contexts/vendor-connectivity/class-diagram), [Sequence diagrams](https://iqvo.github.io/warehouse-docs/network/contexts/vendor-connectivity/sequence-diagrams), [Entity-relationship](https://iqvo.github.io/warehouse-docs/network/contexts/vendor-connectivity/entity-relationship) |
| DDD canvases | [Core domain chart](https://iqvo.github.io/warehouse-docs/network/contexts/vendor-connectivity/core-domain-chart), [Bounded context canvas](https://iqvo.github.io/warehouse-docs/network/contexts/vendor-connectivity/bounded-context-canvas), [Aggregate design canvas](https://iqvo.github.io/warehouse-docs/network/contexts/vendor-connectivity/aggregate-design-canvas), [EventStorming](https://iqvo.github.io/warehouse-docs/network/contexts/vendor-connectivity/eventstorming), [Context map](https://iqvo.github.io/warehouse-docs/network/contexts/vendor-connectivity/context-map) |

## What it does

- **Vendors**: onboard a vendor with its acknowledgement window (1h to 24h); suspend and reinstate it. A suspended vendor's writes are refused.
- **Purchase orders**: every `FulfillmentReleasedToNode` from order-sourcing becomes a purchase order with an `ackBy` deadline (`min(issuedAt + ackWindow, requiredShipBy - handlingTime)`). The vendor polls, acknowledges in full (accept or reject), asks for labels and confirms the shipment. Unacknowledged orders expire; an order still unacknowledged is cancelled when order-sourcing cancels the assignment.
- **Transactions**: every write except a label is answered `202` with a `transactionId` and processed afterwards (`PROCESSING` -> `SUCCESS` or `FAILURE`); the vendor reads `GET /vendor/transactions/{transactionId}`. Simulation knobs (`TX_PROCESSING_DELAY`, `TX_FAILURE_RATE`, `PO_REDELIVERY_RATE`, `RATE_LIMIT_RPS`) let a vendor integration meet a hostile network.
- **Nodes and inventory**: node registration and operational profiles go to node-topology, inventory feeds to offer-availability, labels to shipping-and-tracking and scorecards come from node-performance (four synchronous in-org calls); node and listing data are local read models fed by events.

Aggregates, invariants and every assumption the model left open: [docs/adr/0002](docs/adr/0002-aggregates-and-invariants.md). Consumed contracts: [docs/adr/0003](docs/adr/0003-consumed-contracts-and-local-read-models.md).

## Architecture

Hexagonal / ports & adapters; dependencies point inward only:

```
cmd/api/                      composition root: REST adapter, five Kafka consumers, the submission and expiry loops, the outbox relay
cmd/mcp/                      read-only MCP server (Streamable HTTP, ADR 0006)
cmd/a2a/                      read-only A2A agent (JSON-RPC over HTTP, ADR 0009)
internal/
  domain/                     pure business logic — vendor, purchaseorder, submission, inbound, network, shared (+ shared/gs1)
  application/                ports, repository, requests, usecases, views, gateway, outbox
  adapters/
    inbound/http/             chi handlers, DTOs, problem+json mapping, readiness
    inbound/kafka/            the five consumers
    inbound/mcp/              MCP tools (read-only)
    inbound/a2a/              A2A skills (read-only)
    inbound/requestlog/       request logging middleware
    kafka/cloudevents/        CloudEvents 1.0 codec
    outbound/httpclient/      the four synchronous neighbour calls
    outbound/postgres/        repos, embedded migrations, pgtx UnitOfWork
    outbound/outbox/          transactional outbox + relay with attempt cap
    outbound/kafka/           integration and analytics publishers
    outbound/memory/          test doubles only, never linked into a binary
    outbound/clock/, ids/, repocontract/, telemetry/
  architecture/, bdd/         the fitness tests, BDD wiring
  bootretry/                  bounded boot retry
web/                          the vendor_connectivity_mfe Module Federation remote
features/                     godog BDD
```

The layering is enforced as an executable test, not a convention:
`internal/architecture` (`make arch-test`) fails the build if the domain ever
imports an outer layer or an adapter reaches sideways into its sibling.

## Domain events

Contract: [`apis/asyncapi.yaml`](apis/asyncapi.yaml), catalogue in [ADR 0004](docs/adr/0004-cloudevents-envelope-and-type-catalogue.md).
Published as CloudEvents 1.0 structured JSON, type `com.retailnetwork.partner.vendor-connectivity.<entity>.<Event>` (`vendoraccount`, `purchaseorder`, `submission`, `inboundreceipt`), `subject` and Kafka key = aggregate id:

| Topic | Events |
|---|---|
| `retailnetwork.vendor-connectivity.events` (integration) | `VendorOnboarded`, `VendorSuspended`, `VendorReinstated`, `PurchaseOrderIssued`, `PurchaseOrderAccepted`, `PurchaseOrderRejected`, `PurchaseOrderExpired`, `VendorShipmentConfirmed`, `SubmissionProcessed`, `InboundReceiptSubmitted` (subject and key = `shipmentId`) |
| `retailnetwork.vendor-connectivity.analytics` (analytics only) | `PurchaseOrderCancelled` |

Delivery is at-least-once with a stable event id (transactional outbox).

**Consumed** (13 types from four producers over five consumer groups,
idempotent on the CloudEvents id, dead-lettered to `<topic>.dlq`):

| Topic | Events |
|---|---|
| `retailnetwork.node-topology.events` | `NodeRegistered`, `NodeOperationalProfileDeclared`, `NodeActivated`, `NodeSuspended`, `NodeRetired` |
| `retailnetwork.offer-availability.events` | `ListingCreated` (listings consumer); `CatalogItemPackagingDeclared`, `CatalogItemPackLevelRetired` (packaging consumer) |
| `retailnetwork.seller-inbound.events` | `InboundShipmentLabelled`, `InboundShipmentShipped`, `InboundShipmentReceived` |
| `retailnetwork.order-sourcing.events` | `FulfillmentReleasedToNode`, `FulfillmentCancelled` |

## Running it

Real PostgreSQL and Kafka only. `DATABASE_URL`, `KAFKA_BROKERS`, the five `*_CONSUMER_GROUP` ids and the four neighbour base URLs are mandatory (a boot error names the missing variable); the embedded migrations run at boot, the outbox relay and the consumers are always on.

```sh
export DATABASE_URL=postgres://user:***@localhost:5432/vendor_connectivity?sslmode=disable
export KAFKA_BROKERS=localhost:29092   # whatever your broker advertises
export NODE_CONSUMER_GROUP=vendor-connectivity-node LISTING_CONSUMER_GROUP=vendor-connectivity-listing SOURCING_CONSUMER_GROUP=vendor-connectivity-sourcing INBOUND_CONSUMER_GROUP=vendor-connectivity-inbound PACKAGING_CONSUMER_GROUP=vendor-connectivity-packaging
export NODE_TOPOLOGY_URL=http://localhost:8081 OFFER_AVAILABILITY_URL=http://localhost:8082 SHIPPING_AND_TRACKING_URL=http://localhost:8083 NODE_PERFORMANCE_URL=http://localhost:8084
go run ./cmd/api                       # listens on :8080 (HTTP_ADDR)
```

### Configuration

| Env var | Default | Meaning |
|---|---|---|
| `DATABASE_URL` | **required** | Postgres DSN of the service's pool |
| `MIGRATIONS_DATABASE_URL` | `DATABASE_URL` | Direct DSN for the boot migrations (golang-migrate holds a session advisory lock, which breaks behind a transaction-pooling proxy) |
| `KAFKA_BROKERS` | **required** | Comma-separated `host:port` list (relay, consumers and `/readyz`) |
| `NODE_CONSUMER_GROUP` … `PACKAGING_CONSUMER_GROUP` | **required** | Stable group ids of the node-topology, offer-availability (listings), order-sourcing, seller-inbound (shipments) and offer-availability (packaging) consumers |
| `NODE_TOPOLOGY_URL`, `OFFER_AVAILABILITY_URL`, `SHIPPING_AND_TRACKING_URL`, `NODE_PERFORMANCE_URL` | **required** | Base URLs of the four synchronous in-org calls (ADR 0002 A24) |
| `EVENT_PUBLISHER` | `kafka` | Only `kafka` is accepted; any other value is a boot error |
| `HTTP_ADDR` | `:8080` | Listen address |
| `TX_PROCESSING_DELAY` | `0` | Least time a submission stays `PROCESSING` (Go duration) |
| `TX_FAILURE_RATE` | `0` | Share (0..1) of submissions that end `FAILURE` with `ErrSimulatedFailure` |
| `PO_REDELIVERY_RATE` | `0` | Chance (0..1) that a poll page repeats its first order |
| `RATE_LIMIT_RPS` | `0` | Requests a second over `/vendor/*`; `0` = unlimited. Over the limit: 429 `rate-limited` |
| `SUBMISSION_POLL_INTERVAL` | `1s` | How often due submissions are processed |
| `EXPIRY_SWEEP_INTERVAL` | `30s` | How often unacknowledged orders past `ackBy` are expired |
| `OUTBOX_RELAY_INTERVAL` | `1s` | Poll interval of the relay (Go duration or seconds) |
| `OUTBOX_MAX_ATTEMPTS` | `20` | Failures before the relay excludes a row; `0` = unlimited |
| `SHUTDOWN_DRAIN_DELAY` | `5s` | Wait between flipping `/readyz` to 503 and closing the listener; `0` disables |
| `LOG_LEVEL` | `info` | `debug`/`info`/`warn`/`error` |
| `OTEL_SERVICE_NAME` | `vendor-connectivity` (`-mcp` for `cmd/mcp`, `-a2a` for `cmd/a2a`) | `service.name` (ADR 0008) |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | `localhost:4317` | Collector OTLP/gRPC; export never blocks |
| `SERVICE_VERSION` / `ENVIRONMENT` | `dev` / `local` | `service.version` / `deployment.environment.name` |

`cmd/mcp` adds `MCP_ADDR` (`:8090`); `cmd/a2a` adds `A2A_ADDR` (`:8093`) and
the mandatory `A2A_AGENT_URL` (the absolute URL the Agent Card advertises;
the chart defaults it to the in-cluster Service).

## Observability

One pipeline for logs, metrics and traces (ADR 0008): stdout JSON logs with `trace_id`/`span_id` and a per-request line (`request_id`, route pattern, status, `duration_ms`); OTLP/gRPC metrics (request duration, Go runtime, and the business counter `vendor.submissions.processed` by `outcome` and, on failure, `reason`) and traces to the Collector at `OTEL_EXPORTER_OTLP_ENDPOINT`; `traceparent` in the outbox headers so a consumer continues the request's trace. Env: `OTEL_SERVICE_NAME`, `OTEL_EXPORTER_OTLP_ENDPOINT`, `SERVICE_VERSION`, `ENVIRONMENT`, `LOG_LEVEL`; chart values `otel.*` and `environment`. No Collector is needed to run: export never blocks.

## API

Contract: [`apis/openapi.yaml`](apis/openapi.yaml). No authentication; a vendor names itself with `vendorId` (in the body of a write, as a query parameter on a read), checked for consistency only. Errors are RFC 9457 `application/problem+json` (`type` is a slug such as `not-found`, `illegal-transition`, `validation-failed`, `partial-acknowledgement`, `ack-window-closed`, `vendor-suspended`). Every write needs an `Idempotency-Key` header: a repeat with the same request replays the first response for 24 hours.

| Method and path | Operation |
| --- | --- |
| `GET /vendor/purchase-orders`, `GET /vendor/purchase-orders/{poNumber}` | poll, read |
| `POST /vendor/acknowledgements` | acknowledge in full (202) |
| `POST /vendor/inventory` | inventory feed (202) |
| `POST /vendor/nodes`, `GET /vendor/nodes`, `GET /vendor/nodes/{nodeId}` | register (202), list, read |
| `PUT /vendor/nodes/{nodeId}/profile`, `GET /vendor/nodes/{nodeId}/scorecard` | declare a profile (202), scorecard |
| `POST /vendor/shipping-labels` | labels, answered synchronously (200) |
| `POST /vendor/shipment-confirmations` | confirm shipment (202) |
| `GET /vendor/inbound-shipments`, `GET /vendor/inbound-shipments/{shipmentId}` | poll the SHIPPED inbound shipments of the vendor's nodes, read one (pallet > carton > item tree) |
| `POST /vendor/inbound-receipts` | report an inbound receipt (202, `INBOUND_RECEIPT`) |
| `GET /vendor/transactions/{transactionId}` | outcome of a write |
| `POST /vendors`, `GET /vendors/{vendorId}`, `POST /vendors/{vendorId}:suspend`, `:reinstate` | vendor account |
| `GET /healthz`, `GET /readyz` | liveness, readiness |

### MCP server (read-only)

`cmd/mcp` is a second binary exposing the same nine reads to the AI ecosystem over the official MCP Go SDK, Streamable HTTP only, at `/mcp` and `/` with an open `/healthz` ([ADR 0006](docs/adr/0006-mcp-server-adoption.md)). It is read-only by construction — the adapter holds exactly the read methods the REST GETs call — and it never dials Kafka, runs the relay, the consumers or the loops. No auth (fleet-wide decision); access control is the ClusterIP boundary, and Kong rewrites `/api/mcp/vendor-connectivity` to `/mcp`.

| Tool | Read |
| --- | --- |
| `poll_purchase_orders` | the vendor's orders, paged with `nextToken`, narrowable to a node and an issue date |
| `get_purchase_order` | one order: windows, lines, acknowledgement, labels, tracking |
| `poll_inbound_shipments` | the shipments bound for the vendor's nodes, paged, with their pallet-and-carton trees |
| `get_inbound_shipment` | one inbound shipment: pallets, cartons, item lines in eaches |
| `list_vendor_nodes`, `get_vendor_node` | the vendor's fulfilment nodes |
| `get_node_scorecard` | a node's rolling scorecards (relayed from node-performance) |
| `get_transaction` | the outcome of an asynchronous write, by its 202 `transactionId` |
| `get_vendor` | the vendor account, its window and state |

```sh
export DATABASE_URL=... NODE_TOPOLOGY_URL=... OFFER_AVAILABILITY_URL=... SHIPPING_AND_TRACKING_URL=... NODE_PERFORMANCE_URL=...
go run ./cmd/mcp                          # listens on :8090 (MCP_ADDR)
```

### A2A agent (read-only)

`cmd/a2a` is a third binary: an Agent2Agent (A2A) agent over the official `a2aproject/a2a-go/v2` SDK, JSON-RPC at `/` and `/a2a`, the Agent Card at `/.well-known/agent-card.json`, an open `/healthz` ([ADR 0009](docs/adr/0009-a2a-inbound-adapter.md)). A2A carries *delegation* of a goal, never data sync: facts still leave as CloudEvents on Kafka. The agent is message-only and read-only, like the MCP charter it is derived from (it holds `Reads` with exactly two methods, and a fitness test pins it); it never dials Kafka, runs the relay, the consumers or the loops, and contains no outbound A2A client. No auth (fleet-wide decision). Domain rejections are answered in-band as RFC 7807-shaped problems with the REST slugs and statuses, never as JSON-RPC errors; arguments are decoded strictly.

| Skill | Read |
| --- | --- |
| `get_purchase_order` | where one order stands: state, windows, lines, acknowledgement, labels, tracking |
| `get_node_scorecard` | how one of the vendor's nodes performs (relayed from node-performance) |

```sh
export DATABASE_URL=... NODE_TOPOLOGY_URL=... OFFER_AVAILABILITY_URL=... SHIPPING_AND_TRACKING_URL=... NODE_PERFORMANCE_URL=...
export A2A_AGENT_URL=http://localhost:8093/   # mandatory: the URL the Agent Card advertises
go run ./cmd/a2a                              # listens on :8093 (A2A_ADDR)
```

## Helm chart

`charts/vendor-connectivity/` deploys the `api` component by default (Deployment, Service, ConfigMap, Secret or existing secret, ServiceAccount; HPA, Ingress and HTTPRoute are off by default), plus three optional components, all OFF by default: `mcp` (the read-only MCP server of [ADR 0006](docs/adr/0006-mcp-server-adoption.md), with its own Service and an off-by-default `/api/mcp/vendor-connectivity` HTTPRoute), `a2a` (the read-only A2A agent of [ADR 0009](docs/adr/0009-a2a-inbound-adapter.md) on `:8093`, `A2A_AGENT_URL` defaulting to its in-cluster Service and overridable by `a2a.agentUrl`, with an off-by-default `/api/a2/vendor-connectivity` HTTPRoute rewritten to `/`) and `frontend` (the web remote's nginx pod, ClusterIP only). The chart refuses to render without `database.url` or `database.existingSecret`, `kafka.brokers`, the five `config.*ConsumerGroup` ids (stable, service-prefixed) and the four `config.*Url` neighbour base URLs, and with `terminationGracePeriodSeconds` under 40; `ci/all-components-values.yaml` turns every component on for lint.

```sh
helm template vc charts/vendor-connectivity -f charts/vendor-connectivity/ci/default-values.yaml   # dummy values, nothing is dialled
bash charts/vendor-connectivity/tests/render_test.sh && python3 charts/vendor-connectivity/tests/test_service_selectors.py
```

The CI `helm-lint` job runs `ct lint` (pull requests); `chart-tests` runs the
render and selector tests.

### Running the MCP server in Kubernetes

`mcp.enabled=true` adds the read-only MCP Deployment + Service behind the
`/api/mcp/vendor-connectivity` HTTPRoute (rewritten to `/mcp`);
`a2a.enabled=true` adds the A2A agent behind `/api/a2/vendor-connectivity`
(rewritten to `/`). Both are unauthenticated by fleet decision — the
in-cluster ClusterIP boundary is the access control — and both reuse
`database.*` and the four `config.*Url` neighbour values like the api.

## Operator micro-frontend (web/)

`web/` is the `vendor_connectivity_mfe` Module Federation remote ([ADR 0007](docs/adr/0007-console-remote.md)): four read-only screens over the REST GETs (the purchase-order list with `nextToken` paging, one order's lifecycle and acknowledgement transaction, the inbound shipments with their pallet-and-carton trees, and the vendor's nodes with their scorecards). The shell mounts it under `/vendor-connectivity/*` and serves it at `/mfes/vendor-connectivity/`; the API base comes from the console's runtime `apiOrigin`. `@warehouse/ui-kit` is a sibling checkout:

```sh
cd web && npm ci && npm run dev            # standalone on :4021
docker build --build-context uikit=../../warehouse-ui-kit -t warehouse/vendor-connectivity-frontend:local .
```

## Local development / quality gate

`make check` (format, vet, build, lint, tests incl. the godog scenarios in `features/`), `make check-fast` (Stop hook), `make coverage` (90% over `internal/domain` and `internal/application`), `make mutation` (gremlins per domain package), `make arch-test`, `make integration` (testcontainers Postgres and Kafka; needs Docker), `make vuln` (govulncheck). lefthook (`lefthook install` once per clone) runs `fmt-check`, `vet`, `lint` on pre-commit and `make check` on pre-push. Agent guides: `.claude/rules/` and `.claude/skills/`.

## Tests

- unit + handler tests: `make test` (`go test ./... -race`, CI job `test`,
  with the 90 % coverage gate in the same job)
- BDD acceptance: godog/Gherkin in `features/` (`make bdd`, CI job `bdd`)
- integration: testcontainers Postgres + Kafka (`make integration`, CI job
  `integration`; needs only Docker)
- mutation: gremlins per `internal/domain` package (CI job `mutation-fast`;
  the exhaustive `mutation` job is scheduled)
- architecture fitness: `internal/architecture` (CI job `arch-test`)
- API contract lint (CI job `api-lint`), spec/model drift (`drift`,
  scheduled), `lint`, `guide-lint`, `complexity`, `vuln`, `web` (the
  vitest/oxlint/tsc suite of `web/`), `helm-lint` + `chart-tests` (chart),
  `docker-publish` + `release` (main only)

## Architecture Decision Records

Index: [`docs/adr/index.md`](docs/adr/index.md) — 9 ADRs (0001–0009): the
bounded context (0001), aggregates and invariants (0002), consumed contracts
and local read models (0003), the CloudEvents catalogue (0004), inbound
shipments, receipts and catalog packaging (0005), the MCP server (0006), the
console remote (0007), the fleet observability standard (0008), the A2A
inbound adapter (0009).

## License

[MIT](LICENSE).
