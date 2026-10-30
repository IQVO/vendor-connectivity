# vendor-connectivity

Retail network, bounded context **Vendor Connectivity** (Supporting, partner tier): the network's Open Host Service for vendors that operate fulfillment nodes.

## Disclaimer

This project is a domain-driven design study. It is not associated with, endorsed by or affiliated with any company, and it uses no company's code, data, logos or trademarks. All names, figures and examples are illustrative or synthetic.

Publishes the Vendor API and its Published Language: vendors register nodes, declare their operational profile, send inventory feeds, poll purchase orders, acknowledge them in full inside the acknowledgement window, request labels and confirm shipment, and poll the SSCC/GTIN trees of seller inbound shipments and report what their dock received. Every write is an asynchronous transaction a vendor reconciles.

The design is proposed in [retail-network docs](https://iqvo.github.io/warehouse-docs/network/contexts/vendor-connectivity); `model/model.yaml` there is the single source of truth.
This repository is a service of the retail network, not of the warehouse-systems fleet; the fleet reaches the network only through the
`vendor-connectivity` Vendor API.

## What it does

- **Vendors**: onboard a vendor with its acknowledgement window (1h to 24h); suspend and reinstate it. A suspended vendor's writes are refused.
- **Purchase orders**: every `FulfillmentReleasedToNode` from order-sourcing becomes a purchase order with an `ackBy` deadline (`min(issuedAt + ackWindow, requiredShipBy - handlingTime)`). The vendor polls, acknowledges in full (accept or reject), asks for labels and confirms the shipment. Unacknowledged orders expire; an order still unacknowledged is cancelled when order-sourcing cancels the assignment.
- **Transactions**: every write except a label is answered `202` with a `transactionId` and processed afterwards (`PROCESSING` -> `SUCCESS` or `FAILURE`); the vendor reads `GET /vendor/transactions/{transactionId}`. Simulation knobs (`TX_PROCESSING_DELAY`, `TX_FAILURE_RATE`, `PO_REDELIVERY_RATE`, `RATE_LIMIT_RPS`) let a vendor integration meet a hostile network.
- **Nodes and inventory**: node registration and operational profiles go to node-topology, inventory feeds to offer-availability, labels to shipping-and-tracking and scorecards come from node-performance (four synchronous in-org calls); node and listing data are local read models fed by events.

Aggregates, invariants and every assumption the model left open: [docs/adr/0002](docs/adr/0002-aggregates-and-invariants.md). Consumed contracts: [docs/adr/0003](docs/adr/0003-consumed-contracts-and-local-read-models.md).

## REST API

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

## Events

Contract: [`apis/asyncapi.yaml`](apis/asyncapi.yaml), catalogue in [ADR 0004](docs/adr/0004-cloudevents-envelope-and-type-catalogue.md).
Published as CloudEvents 1.0 structured JSON, type `com.retailnetwork.partner.vendor-connectivity.<entity>.<Event>` (`vendoraccount`, `purchaseorder`, `submission`, `inboundreceipt`), `subject` and Kafka key = aggregate id, to `retailnetwork.vendor-connectivity.events`:
`VendorOnboarded`, `VendorSuspended`, `VendorReinstated`, `PurchaseOrderIssued`, `PurchaseOrderAccepted`, `PurchaseOrderRejected`, `PurchaseOrderExpired`, `VendorShipmentConfirmed`, `SubmissionProcessed`, `InboundReceiptSubmitted` (subject and key = `shipmentId`);
`PurchaseOrderCancelled` goes only to `retailnetwork.vendor-connectivity.analytics`. Delivery is at-least-once with a stable event id (transactional outbox).

Consumed (idempotent on the CloudEvents id, dead-lettered to `<topic>.dlq`): node-topology `NodeRegistered`, `NodeOperationalProfileDeclared`, `NodeActivated`, `NodeSuspended`, `NodeRetired`; offer-availability `ListingCreated`, `CatalogItemPackagingDeclared`, `CatalogItemPackLevelRetired`; seller-inbound `InboundShipmentLabelled`, `InboundShipmentShipped`, `InboundShipmentReceived`; order-sourcing `FulfillmentReleasedToNode`, `FulfillmentCancelled`.

## Run it

Real PostgreSQL and Kafka only. `DATABASE_URL`, `KAFKA_BROKERS`, the five `*_CONSUMER_GROUP` ids and the four neighbour base URLs are mandatory (a boot error names the missing variable); the embedded migrations run at boot, the outbox relay and the consumers are always on.

```sh
export DATABASE_URL=postgres://user:***@localhost:5432/vendor_connectivity?sslmode=disable
export KAFKA_BROKERS=localhost:29092   # whatever your broker advertises
export NODE_CONSUMER_GROUP=vendor-connectivity-node LISTING_CONSUMER_GROUP=vendor-connectivity-listing SOURCING_CONSUMER_GROUP=vendor-connectivity-sourcing INBOUND_CONSUMER_GROUP=vendor-connectivity-inbound PACKAGING_CONSUMER_GROUP=vendor-connectivity-packaging
export NODE_TOPOLOGY_URL=http://localhost:8081 OFFER_AVAILABILITY_URL=http://localhost:8082 SHIPPING_AND_TRACKING_URL=http://localhost:8083 NODE_PERFORMANCE_URL=http://localhost:8084
go run ./cmd/api                       # listens on :8080 (HTTP_ADDR)
```

All settings, the probes, the shutdown order and the outbox runbook: [docs/runtime.md](docs/runtime.md). Packaging checklist: [docs/before-first-release.md](docs/before-first-release.md).

## MCP server (read-only)

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

## A2A agent (read-only)

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

## Web console remote

`web/` is the `vendor_connectivity_mfe` Module Federation remote ([ADR 0007](docs/adr/0007-console-remote.md)): four read-only screens over the REST GETs (the purchase-order list with `nextToken` paging, one order's lifecycle and acknowledgement transaction, the inbound shipments with their pallet-and-carton trees, and the vendor's nodes with their scorecards). The shell mounts it under `/vendor-connectivity/*` and serves it at `/mfes/vendor-connectivity/`; the API base comes from the console's runtime `apiOrigin`.

```sh
cd web && npm ci && npm run dev            # standalone on :4021
docker build --build-context uikit=../../warehouse-ui-kit -t warehouse/vendor-connectivity-frontend:local .
```

## Deploy (Helm)

`charts/vendor-connectivity/` deploys the `api` component by default (Deployment, Service, ConfigMap, Secret or existing secret, ServiceAccount; HPA, Ingress and HTTPRoute are off by default), plus three optional components, all OFF by default: `mcp` (the read-only MCP server of [ADR 0006](docs/adr/0006-mcp-server-adoption.md), with its own Service and an off-by-default `/api/mcp/vendor-connectivity` HTTPRoute), `a2a` (the read-only A2A agent of [ADR 0009](docs/adr/0009-a2a-inbound-adapter.md) on `:8093`, `A2A_AGENT_URL` defaulting to its in-cluster Service and overridable by `a2a.agentUrl`, with an off-by-default `/api/a2a/vendor-connectivity` HTTPRoute rewritten to `/`) and `frontend` (the web remote's nginx pod, ClusterIP only). The chart refuses to render without `database.url` or `database.existingSecret`, `kafka.brokers`, the three `config.*ConsumerGroup` ids (stable, service-prefixed) and the four `config.*Url` neighbour base URLs, and with `terminationGracePeriodSeconds` under 40; `ci/all-components-values.yaml` turns every component on for lint.

```sh
helm template vc charts/vendor-connectivity -f charts/vendor-connectivity/ci/default-values.yaml   # dummy values, nothing is dialled
bash charts/vendor-connectivity/tests/render_test.sh && python3 charts/vendor-connectivity/tests/test_service_selectors.py
```

## Observability

One pipeline for logs, metrics and traces (ADR 0008): stdout JSON logs with `trace_id`/`span_id` and a per-request line (`request_id`, route pattern, status, `duration_ms`); OTLP/gRPC metrics (request duration, Go runtime, and the business counter `vendor.submissions.processed` by `outcome` and, on failure, `reason`) and traces to the Collector at `OTEL_EXPORTER_OTLP_ENDPOINT`; `traceparent` in the outbox headers so a consumer continues the request's trace. Env: `OTEL_SERVICE_NAME`, `OTEL_EXPORTER_OTLP_ENDPOINT`, `SERVICE_VERSION`, `ENVIRONMENT`, `LOG_LEVEL`; chart values `otel.*` and `environment`. No Collector is needed to run: export never blocks.

## Develop

`make check` (format, vet, build, lint, tests incl. the godog scenarios in `features/`), `make coverage` (90% over `internal/domain` and `internal/application`), `make mutation` (gremlins per domain package), `make arch-test`, `make integration` (testcontainers Postgres and Kafka; needs Docker), `make vuln`. Agent guides: `.claude/rules/` and `.claude/skills/`.
