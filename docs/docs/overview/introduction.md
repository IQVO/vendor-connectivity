---
id: introduction
title: Introduction
sidebar_label: Introduction
sidebar_position: 1
---

# Vendor Connectivity

`vendor-connectivity` is a **Supporting** bounded context in the **Network** area of the
warehouse-systems fleet (the retail network, CloudEvents tier `partner`). It is the
network's **Open Host Service** for the vendors that operate its fulfillment nodes: it
publishes the **Vendor API**, whose JSON schemas are the Published Language, and it runs the
protocol behind it. Vendors register nodes, declare each node's operational profile, send
inventory feeds, poll purchase orders, acknowledge each one **once and in full** before
`ackBy`, request labels and confirm shipment. Every write except the label request is
asynchronous: it returns `202` with a `transactionId` that the vendor reconciles with
`GET /vendor/transactions/{transactionId}`.

Inside the network the context turns an order-sourcing release into a **purchase order**,
runs its acknowledgement clock, and reports the vendor's answer back as events. The
warehouse-systems fleet is one such vendor (`V-WS`); its `network-fulfillment` context
reaches the network only through this API, and this context never calls into the fleet
([ADR 0001](/docs/adr/0001-vendor-connectivity-bounded-context)).

:::info What runs on develop
Three Go binaries share one application service: `cmd/api` (Vendor REST API, transactional
outbox relay, five Kafka consumers, three background loops), `cmd/mcp` (read-only MCP over
Streamable HTTP, nine tools) and `cmd/a2a` (read-only A2A agent, two skills). A fourth
deployable, `web/`, is the `vendor_connectivity_mfe` Module Federation remote for the
warehouse console. It runs on real Postgres and real Kafka only: there is no in-memory mode
and no log sink, and `DATABASE_URL`, `KAFKA_BROKERS`, five consumer group ids and four
neighbour URLs are mandatory for `cmd/api`. REST, MCP and A2A are unauthenticated, as
everywhere in the fleet.
:::

## What the context owns

| Concept | Where it lives | Rule in one line |
| --- | --- | --- |
| **VendorAccount** | `internal/domain/vendor` | A vendor's standing (`ACTIVE` or `SUSPENDED`) and its acknowledgement window, 1 h to 24 h. A suspended vendor's writes are refused. |
| **PurchaseOrder** | `internal/domain/purchaseorder` | One order per `assignmentId` and `releaseNo`, issued from an order-sourcing release. States `ISSUED`, `ACK_PROCESSING`, `ACCEPTED`, `REJECTED`, `EXPIRED`, `SHIPMENT_PROCESSING`, `SHIPPED`, `CANCELLED`. Acknowledge in full, once, before `ackBy`. |
| **Submission** | `internal/domain/submission` | One asynchronous vendor write (six kinds) and its outcome, set once: `PROCESSING`, `SUCCESS`, `FAILURE`. |
| **Local copies** | `vendor_nodes`, `listings`, `inbound_shipments`, `catalog_packaging` | Event-fed read models of other contexts' facts, not aggregate state ([ADR 0003](/docs/adr/0003-consumed-contracts-and-local-read-models), [ADR 0005](/docs/adr/0005-inbound-shipments-receipts-and-catalog-packaging)). |
| **Inbound shipments and receipts** | `internal/domain/inbound` | A seller's cartons announced by seller-inbound, polled by the vendor and answered with a carton-level receipt. |

`ackBy = min(issuedAt + vendor ackWindow, requiredShipBy - node handlingTime)`: the vendor
never gets more time to answer than it has left to pick and pack.

## What it does not own

- **Choosing the node.** `order-sourcing` owns assignment and reassignment; this context
  reports the vendor's answer and never picks another node.
- **The node registry and listings.** `node-topology` and `offer-availability` own them;
  vendor writes are relayed to them over REST and the answers come back as events.
- **Labels and tracking.** `shipping-and-tracking` issues the label (and holds the ship-to
  address); the Vendor API only ever carries `labelRef`, `trackingNumber` and `carrier`, never
  an address or other PII.
- **Measuring the vendor.** `node-performance` owns the scorecard; this context relays it.
- **Authentication.** There is none. The vendor names itself in each request and ownership is
  a consistency check ([API overview](/docs/api-reference)).
- **Payments and invoicing, returns authorisation, inventory placement.** Out of scope.
- **Anything inside the warehouse.** No retail-network context reads a `warehouse.*` topic or
  calls a fleet service.

## Classification and tier

- **Supporting subdomain, partner tier.** A direct-fulfillment-style protocol is industry
  practice; the value is contract stability, not cleverness
  ([Subdomain classification](/docs/ddd/subdomain-classification)).
- CloudEvents types look like
  `com.retailnetwork.partner.vendor-connectivity.<entity>.<EventName>`.

## Main capabilities

1. Onboard, suspend and reinstate a vendor account (`POST /vendors`, `:suspend`, `:reinstate`).
2. Turn `FulfillmentReleasedToNode` into a purchase order with a computed `ackBy`; cancel it
   on `FulfillmentCancelled` while it is still `ISSUED`; expire it when `ackBy` passes.
3. Accept the vendor's whole-order acknowledgement, label request and shipment confirmation,
   each atomically with its outbox events.
4. Relay node registration, profile and inventory writes to node-topology and
   offer-availability, with bounded retry when a neighbour is down.
5. Expose polling reads for purchase orders and inbound shipments, and a carton-level inbound
   receipt.
6. Publish eleven event types on `retailnetwork.vendor-connectivity.events` (and
   `PurchaseOrderCancelled` on the analytics topic) through the transactional outbox.
7. Offer simulation knobs (`TX_PROCESSING_DELAY`, `TX_FAILURE_RATE`, `PO_REDELIVERY_RATE`,
   `RATE_LIMIT_RPS`) so a vendor integration can be exercised against a hostile network.

## Where to go next

| You want to | Read |
| --- | --- |
| See the layers, binaries and data stores | [Architecture](/docs/overview/architecture), [Runtime](/docs/overview/runtime) |
| Run it and make the first calls | [Quickstart](/docs/overview/quickstart) |
| Know every setting | [Configuration](/docs/operations/configuration) |
| Deploy and operate it | [Runbook](/docs/operations/runbook), [Observability](/docs/operations/observability), [Before the first release](/docs/operations/before-first-release) |
| Fix something that broke | [Troubleshooting](/docs/operations/troubleshooting) |
| Know what tests and CI gates exist | [Testing](/docs/development/testing) |
| See who talks to whom | [Integration](/docs/ecosystem/integration) |
| See the use cases and invariants | [Use cases](/docs/ddd/use-cases) |
| Read the design in ddd-crew artifacts | [DDD artifacts](/docs/ddd/ddd-artifacts), [Ubiquitous language](/docs/ddd/ubiquitous-language) |
| Read the contracts | [API overview](/docs/api-reference), [Event catalogue](/docs/api-reference/events), [MCP tools](/docs/mcp/tools) |
| Know why it is built this way | [ADR index](/docs/adr) |
