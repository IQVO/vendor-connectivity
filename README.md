# vendor-connectivity

Retail network, bounded context **Vendor Connectivity** (Supporting, partner tier): the network's Open Host Service for vendors that operate fulfillment nodes.

## Disclaimer

This project is a domain-driven design study. It is not associated with, endorsed by or affiliated with any company, and it uses no company's code, data, logos or trademarks. All names, figures and examples are illustrative or synthetic.

Publishes the Vendor API and its Published Language: vendors register nodes, declare their operational profile, send inventory feeds, poll purchase orders, acknowledge them in full inside the acknowledgement window, request labels and confirm shipment. Every write is an asynchronous transaction a vendor reconciles.

The design is proposed in [retail-network-docs](https://iqvo.github.io/retail-network-docs/contexts/vendor-connectivity); `model/model.yaml` there is the single source of truth.
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
| `GET /vendor/transactions/{transactionId}` | outcome of a write |
| `POST /vendors`, `GET /vendors/{vendorId}`, `POST /vendors/{vendorId}:suspend`, `:reinstate` | vendor account |
| `GET /healthz`, `GET /readyz` | liveness, readiness |

## Events

Contract: [`apis/asyncapi.yaml`](apis/asyncapi.yaml), catalogue in [ADR 0004](docs/adr/0004-cloudevents-envelope-and-type-catalogue.md).
Published as CloudEvents 1.0 structured JSON, type `com.retailnetwork.partner.vendor-connectivity.<entity>.<Event>` (`vendoraccount`, `purchaseorder`, `submission`), `subject` and Kafka key = aggregate id, to `retailnetwork.vendor-connectivity.events`:
`VendorOnboarded`, `VendorSuspended`, `VendorReinstated`, `PurchaseOrderIssued`, `PurchaseOrderAccepted`, `PurchaseOrderRejected`, `PurchaseOrderExpired`, `VendorShipmentConfirmed`, `SubmissionProcessed`;
`PurchaseOrderCancelled` goes only to `retailnetwork.vendor-connectivity.analytics`. Delivery is at-least-once with a stable event id (transactional outbox).

Consumed (idempotent on the CloudEvents id, dead-lettered to `<topic>.dlq`): node-topology `NodeRegistered`, `NodeOperationalProfileDeclared`, `NodeActivated`, `NodeSuspended`, `NodeRetired`; offer-availability `ListingCreated`; order-sourcing `FulfillmentReleasedToNode`, `FulfillmentCancelled`.

## Run it

Real PostgreSQL and Kafka only. `DATABASE_URL`, `KAFKA_BROKERS`, the three `*_CONSUMER_GROUP` ids and the four neighbour base URLs are mandatory (a boot error names the missing variable); the embedded migrations run at boot, the outbox relay and the consumers are always on.

```sh
export DATABASE_URL=postgres://user:***@localhost:5432/vendor_connectivity?sslmode=disable
export KAFKA_BROKERS=localhost:29092   # whatever your broker advertises
export NODE_CONSUMER_GROUP=vendor-connectivity-node LISTING_CONSUMER_GROUP=vendor-connectivity-listing SOURCING_CONSUMER_GROUP=vendor-connectivity-sourcing
export NODE_TOPOLOGY_URL=http://localhost:8081 OFFER_AVAILABILITY_URL=http://localhost:8082 SHIPPING_AND_TRACKING_URL=http://localhost:8083 NODE_PERFORMANCE_URL=http://localhost:8084
go run ./cmd/api                       # listens on :8080 (HTTP_ADDR)
```

All settings, the probes, the shutdown order and the outbox runbook: [docs/runtime.md](docs/runtime.md). Packaging checklist: [docs/before-first-release.md](docs/before-first-release.md).

## Develop

`make check` (format, vet, build, lint, tests incl. the godog scenarios in `features/`), `make coverage` (90% over `internal/domain` and `internal/application`), `make mutation` (gremlins per domain package), `make arch-test`, `make integration` (testcontainers Postgres and Kafka; needs Docker), `make vuln`. Agent guides: `.claude/rules/` and `.claude/skills/`.
