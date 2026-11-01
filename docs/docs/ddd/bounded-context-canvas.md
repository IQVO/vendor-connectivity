---
id: bounded-context-canvas
title: Bounded Context Canvas
sidebar_label: Bounded Context Canvas
sidebar_position: 2
description: "The ddd-crew Bounded Context Canvas v5 for Vendor Connectivity, with every inbound and outbound message, its channel and its relationship."
---

# Bounded Context Canvas

Following the ddd-crew [Bounded Context Canvas v5](https://github.com/ddd-crew/bounded-context-canvas).
Every message row is checked against the router, the five consumers, the outbox encoder and the
four neighbour clients in the code.

## Canvas at a glance

Two pictures of the sections below. Arrows follow the message type:
**solid blue** commands (`cmd`), **dotted green** queries (`qry`), **thick
orange** events (`evt`). Box shapes mark the collaborator kind: rounded =
people; blue = sibling bounded context; double border = external system
(the other organization); dashed = no context consumer. Channel names are
abbreviated; the tables carry the exact route, topic and CloudEvents type.

### Inbound

```mermaid
%%{init: {"flowchart": {"nodeSpacing": 14, "rankSpacing": 40, "padding": 6}}}%%
flowchart LR
    classDef bc fill:#fff3bf,stroke:#e67700,stroke-width:1.5px,color:#000
    classDef card fill:#fff9db,stroke:#f08c00,color:#000,text-align:left
    classDef ctx fill:#e7f5ff,stroke:#1c7ed6,color:#000
    classDef actor fill:#fff4e6,stroke:#e8590c,color:#000
    classDef ext fill:#ffe3e3,stroke:#c92a2a,color:#000
    classDef self fill:#f1f3f5,stroke:#868e96,color:#000
    classDef none fill:#f8f9fa,stroke:#adb5bd,stroke-dasharray:5 5,color:#495057
    subgraph IN["Inbound - who commands, queries or feeds it"]
        direction TB
        in0[["network-fulfillment<br/>warehouse-systems,<br/>vendor V-WS"]]:::ext
        in1(["Network partner<br/>managers"]):::actor
        in2["order-sourcing"]:::ctx
        in3["node-topology"]:::ctx
        in4["offer-availability"]:::ctx
        in5["seller-inbound"]:::ctx
    end
    subgraph BC["vendor-connectivity - Supporting - partner tier"]
        direction TB
        card["<b>Purpose</b><br/>Open Host Service for vendors<br/>that operate fulfillment nodes.<br/>Publishes the Vendor API and its<br/>Published Language. Every write is<br/>a transaction the vendor reconciles.<br/><br/><b>Domain roles</b><br/>- Gateway / Open Host Service<br/>- Execution: the purchase-order<br/>protocol and its clock<br/><br/><b>Business decisions</b><br/>- Ack once, in full, before ackBy<br/>- ackBy = min of issuedAt + window<br/>and requiredShipBy - handlingTime<br/>- Label after ACCEPTED, shipment<br/>after a label<br/>- Every write: 202 + transactionId<br/>- No ship-to PII<br/>- 429 + Retry-After"]:::card
    end
    in0 -->|"cmd - Vendor API<br/>RegisterVendorNode,<br/>DeclareNodeProfile,<br/>SubmitInventory,<br/>SubmitAcknowledgement,<br/>RequestLabel, Submit-<br/>ShipmentConfirmation,<br/>SubmitInboundReceipt"| BC
    in0 -.->|"qry - Vendor API<br/>PollPurchaseOrders,<br/>GetPurchaseOrder,<br/>GetTransaction, GetNode,<br/>GetScorecard"| BC
    in1 -->|"cmd - REST<br/>OnboardVendor, SuspendVendor,<br/>ReinstateVendor"| BC
    in2 ==>|"evt - Kafka<br/>FulfillmentReleased-<br/>ToNode, Fulfillment-<br/>Cancelled"| BC
    in3 ==>|"evt - Kafka<br/>NodeRegistered,<br/>NodeOperationalProfileDeclared,<br/>NodeActivated,<br/>NodeSuspended,<br/>NodeRetired"| BC
    in4 ==>|"evt - Kafka<br/>ListingCreated,<br/>CatalogItemPackagingDeclared,<br/>CatalogItemPackLevelRetired"| BC
    in5 ==>|"evt - Kafka<br/>InboundShipmentLabelled,<br/>Shipped, Received"| BC
    linkStyle 0 stroke:#1971c2,stroke-width:1.5px
    linkStyle 1 stroke:#2f9e44,stroke-width:1.5px
    linkStyle 2 stroke:#1971c2,stroke-width:1.5px
    linkStyle 3 stroke:#e8590c,stroke-width:2.5px
    linkStyle 4 stroke:#e8590c,stroke-width:2.5px
    linkStyle 5 stroke:#e8590c,stroke-width:2.5px
    linkStyle 6 stroke:#e8590c,stroke-width:2.5px
    class BC bc
```

Source: `internal/adapters/inbound/kafka/consumers.go` and `inbound_consumers.go` (consumed), `internal/adapters/outbound/kafka/encoder.go` (published), `internal/adapters/outbound/httpclient/clients.go` (synchronous calls), `internal/adapters/inbound/http/server.go` and `apis/openapi.yaml` (Vendor API).

### Outbound

```mermaid
%%{init: {"flowchart": {"nodeSpacing": 14, "rankSpacing": 40, "padding": 6}}}%%
flowchart LR
    classDef bc fill:#fff3bf,stroke:#e67700,stroke-width:1.5px,color:#000
    classDef card fill:#fff9db,stroke:#f08c00,color:#000,text-align:left
    classDef ctx fill:#e7f5ff,stroke:#1c7ed6,color:#000
    classDef actor fill:#fff4e6,stroke:#e8590c,color:#000
    classDef ext fill:#ffe3e3,stroke:#c92a2a,color:#000
    classDef self fill:#f1f3f5,stroke:#868e96,color:#000
    classDef none fill:#f8f9fa,stroke:#adb5bd,stroke-dasharray:5 5,color:#495057
    BC["<b>vendor-connectivity<br/>Supporting - partner tier</b>"]:::bc
    subgraph OUT["Outbound - whom it calls or notifies"]
        direction TB
        out0["order-sourcing"]:::ctx
        out1["shipping-and-tracking"]:::ctx
        out2["node-performance"]:::ctx
        out3["node-topology"]:::ctx
        out4["offer-availability"]:::ctx
        out5["analytics only<br/>no context consumer"]:::none
        out6["seller-inbound"]:::ctx
    end
    BC ==>|"evt - Kafka<br/>PurchaseOrderAccepted, Rejected,<br/>Expired, VendorShipmentConfirmed"| out0
    BC -->|"cmd - REST<br/>IssueLabel"| out1
    BC ==>|"evt - Kafka<br/>VendorShipmentConfirmed"| out1
    BC ==>|"evt - Kafka<br/>VendorOnboarded, PurchaseOrderIssued,<br/>Accepted, Rejected, Expired,<br/>VendorShipmentConfirmed,<br/>SubmissionProcessed,<br/>InboundReceiptSubmitted"| out2
    BC -.->|"qry - REST<br/>GetScorecard"| out2
    BC -->|"cmd - REST<br/>RegisterNode,<br/>DeclareOperationalProfile"| out3
    BC -->|"cmd - REST<br/>ApplyInventoryFeed"| out4
    BC ==>|"evt - Kafka<br/>PurchaseOrderCancelled"| out5
    BC ==>|"evt - Kafka<br/>InboundReceiptSubmitted"| out6
    linkStyle 0 stroke:#e8590c,stroke-width:2.5px
    linkStyle 1 stroke:#1971c2,stroke-width:1.5px
    linkStyle 2 stroke:#e8590c,stroke-width:2.5px
    linkStyle 3 stroke:#e8590c,stroke-width:2.5px
    linkStyle 4 stroke:#2f9e44,stroke-width:1.5px
    linkStyle 5 stroke:#1971c2,stroke-width:1.5px
    linkStyle 6 stroke:#1971c2,stroke-width:1.5px
    linkStyle 7 stroke:#e8590c,stroke-width:2.5px
    linkStyle 8 stroke:#e8590c,stroke-width:2.5px
```

Source: `internal/adapters/inbound/kafka/consumers.go` and `inbound_consumers.go` (consumed), `internal/adapters/outbound/kafka/encoder.go` (published), `internal/adapters/outbound/httpclient/clients.go` (synchronous calls), `internal/adapters/inbound/http/server.go` and `apis/openapi.yaml` (Vendor API).

Omitted: the internal triggers that drive commands without a collaborator
(the `ExpireUnacknowledged` sweep and the submission processor that runs
`ProcessSubmission` after `notBefore`, and the retention sweeper); replies to queries; the
relationship pattern per message (see the tables); `VendorSuspended` and `VendorReinstated`, which
have no consumer yet. "Accepted, Rejected, Expired" on the arrows abbreviate the full
`PurchaseOrder...` event names.

## Name

**Vendor Connectivity** (`vendor-connectivity`, code `VC`, CloudEvents tier
`partner`).

## Purpose

The network's Open Host Service for vendors that operate fulfillment
nodes. It publishes the Vendor API and its Published Language: vendors
register nodes, declare their operational profile, send inventory feeds,
poll purchase orders, acknowledge them in full inside the acknowledgement
window, request labels and confirm shipment. Every write is an
asynchronous transaction a vendor reconciles.

## Strategic Classification

| Axis | Verdict |
| --- | --- |
| **Domain** | **Supporting**. See the [Core Domain Chart](/docs/ddd/core-domain-chart). |
| **Business Model** | Revenue-channel enabler (third-party capacity without integration cost). |
| **Evolution** | Product: the protocol shape is well known and adopted in the network's own vocabulary. |

## Domain Roles

| Role | Applies? | Why |
| --- | --- | --- |
| **Gateway / Open Host Service** | **Yes, primary** | The Vendor API is the one door for every vendor; its schemas are the Published Language (`apis/openapi.yaml`). It relays node, profile and inventory writes to their owners. |
| **Execution** | **Yes** | Owns the purchase-order protocol and its clock: issue, `ackBy`, expiry, acceptance, label gate, shipment confirmation. |
| Analysis | No | Measuring vendors is `node-performance`'s job. |
| Specification | No | Node and listing master data belong to `node-topology` and `offer-availability`. |

## Inbound Communication

| Collaborator | Message | Type | Channel | Relationship |
| --- | --- | --- | --- | --- |
| network-fulfillment (warehouse-systems, vendor `V-WS`) | RegisterVendorNode | Command | Vendor API `POST /vendor/nodes` | OHS + PL (downstream is Conformist + ACL) |
| network-fulfillment | DeclareNodeProfile | Command | Vendor API `PUT /vendor/nodes/{nodeId}/profile` | OHS + PL (downstream is Conformist + ACL) |
| network-fulfillment | SubmitInventory | Command | Vendor API `POST /vendor/inventory` | OHS + PL (downstream is Conformist + ACL) |
| network-fulfillment | SubmitAcknowledgement | Command | Vendor API `POST /vendor/acknowledgements` | OHS + PL (downstream is Conformist + ACL) |
| network-fulfillment | RequestLabel | Command | Vendor API `POST /vendor/shipping-labels` | OHS + PL (downstream is Conformist + ACL) |
| network-fulfillment | SubmitShipmentConfirmation | Command | Vendor API `POST /vendor/shipment-confirmations` | OHS + PL (downstream is Conformist + ACL) |
| network-fulfillment | PollPurchaseOrders | Query | Vendor API `GET /vendor/purchase-orders?createdAfter=&nodeId=&nextToken=` | OHS + PL |
| network-fulfillment | GetPurchaseOrder | Query | Vendor API `GET /vendor/purchase-orders/{poNumber}` | OHS + PL |
| network-fulfillment | GetTransaction | Query | Vendor API `GET /vendor/transactions/{transactionId}` | OHS + PL |
| network-fulfillment | GetNode, ListVendorNodes | Query | Vendor API `GET /vendor/nodes/{nodeId}`, `GET /vendor/nodes` | OHS + PL |
| network-fulfillment | GetScorecard | Query | Vendor API `GET /vendor/nodes/{nodeId}/scorecard` | OHS + PL |
| network-fulfillment | PollInboundShipments, GetInboundShipment | Query | Vendor API `GET /vendor/inbound-shipments`, `GET /vendor/inbound-shipments/{shipmentId}` | OHS + PL |
| network-fulfillment | SubmitInboundReceipt | Command | Vendor API `POST /vendor/inbound-receipts` | OHS + PL (downstream is Conformist + ACL) |
| Network partner managers (human) | OnboardVendor, SuspendVendor, ReinstateVendor, GetVendor | Command, Query | REST `POST /vendors`, `POST /vendors/{vendorId}:suspend`, `POST /vendors/{vendorId}:reinstate`, `GET /vendors/{vendorId}` | UI |
| order-sourcing | FulfillmentReleasedToNode | Event | Kafka `retailnetwork.order-sourcing.events`, `com.retailnetwork.commerce.order-sourcing.assignment.FulfillmentReleasedToNode` | Customer/Supplier |
| order-sourcing | FulfillmentCancelled | Event | Kafka `retailnetwork.order-sourcing.events`, `com.retailnetwork.commerce.order-sourcing.assignment.FulfillmentCancelled` | Customer/Supplier |
| node-topology | NodeRegistered | Event | Kafka `retailnetwork.node-topology.events`, `com.retailnetwork.logistics.node-topology.node.NodeRegistered` | Conformist |
| node-topology | NodeOperationalProfileDeclared | Event | Kafka `retailnetwork.node-topology.events`, `com.retailnetwork.logistics.node-topology.node.NodeOperationalProfileDeclared` | Conformist |
| node-topology | NodeActivated | Event | Kafka `retailnetwork.node-topology.events`, `com.retailnetwork.logistics.node-topology.node.NodeActivated` | Conformist |
| node-topology | NodeSuspended | Event | Kafka `retailnetwork.node-topology.events`, `com.retailnetwork.logistics.node-topology.node.NodeSuspended` | Conformist |
| node-topology | NodeRetired | Event | Kafka `retailnetwork.node-topology.events`, `com.retailnetwork.logistics.node-topology.node.NodeRetired` | Conformist |
| offer-availability | ListingCreated | Event | Kafka `retailnetwork.offer-availability.events`, `com.retailnetwork.commerce.offer-availability.listing.ListingCreated` | Conformist |
| offer-availability | CatalogItemPackagingDeclared, CatalogItemPackLevelRetired | Event | Kafka `retailnetwork.offer-availability.events`, `com.retailnetwork.partner.offer-availability.catalogitem.CatalogItemPackagingDeclared` and `...CatalogItemPackLevelRetired` (the `commerce` tier spelling is accepted too) | Conformist |
| seller-inbound | InboundShipmentLabelled, InboundShipmentShipped, InboundShipmentReceived | Event | Kafka `retailnetwork.seller-inbound.events`, `com.retailnetwork.partner.seller-inbound.inboundshipment.<Event>` | Conformist |

## Outbound Communication

| Collaborator | Message | Type | Channel | Relationship |
| --- | --- | --- | --- | --- |
| node-performance | VendorOnboarded | Event | Kafka `retailnetwork.vendor-connectivity.events`, `com.retailnetwork.partner.vendor-connectivity.vendoraccount.VendorOnboarded` | Conformist (downstream) |
| node-performance | PurchaseOrderIssued | Event | Kafka `retailnetwork.vendor-connectivity.events`, `com.retailnetwork.partner.vendor-connectivity.purchaseorder.PurchaseOrderIssued` | Conformist (downstream) |
| order-sourcing, node-performance | PurchaseOrderAccepted | Event | Kafka `retailnetwork.vendor-connectivity.events`, `com.retailnetwork.partner.vendor-connectivity.purchaseorder.PurchaseOrderAccepted` | Customer/Supplier (order-sourcing); Conformist (node-performance) |
| order-sourcing, node-performance | PurchaseOrderRejected | Event | Kafka `retailnetwork.vendor-connectivity.events`, `com.retailnetwork.partner.vendor-connectivity.purchaseorder.PurchaseOrderRejected` | Customer/Supplier (order-sourcing); Conformist (node-performance) |
| order-sourcing, node-performance | PurchaseOrderExpired | Event | Kafka `retailnetwork.vendor-connectivity.events`, `com.retailnetwork.partner.vendor-connectivity.purchaseorder.PurchaseOrderExpired` | Customer/Supplier (order-sourcing); Conformist (node-performance) |
| analytics only | PurchaseOrderCancelled | Event | Kafka `retailnetwork.vendor-connectivity.analytics` (not the events topic), `com.retailnetwork.partner.vendor-connectivity.purchaseorder.PurchaseOrderCancelled` | — (no context consumer) |
| order-sourcing, shipping-and-tracking, node-performance | VendorShipmentConfirmed | Event | Kafka `retailnetwork.vendor-connectivity.events`, `com.retailnetwork.partner.vendor-connectivity.purchaseorder.VendorShipmentConfirmed` | Customer/Supplier (order-sourcing, shipping-and-tracking); Conformist (node-performance) |
| none yet | VendorSuspended, VendorReinstated | Event | Kafka `retailnetwork.vendor-connectivity.events` | — (no context consumer) |
| node-performance | SubmissionProcessed | Event | Kafka `retailnetwork.vendor-connectivity.events`, `com.retailnetwork.partner.vendor-connectivity.submission.SubmissionProcessed` | Conformist (downstream) |
| seller-inbound, node-performance | InboundReceiptSubmitted | Event | Kafka `retailnetwork.vendor-connectivity.events`, `com.retailnetwork.partner.vendor-connectivity.inboundreceipt.InboundReceiptSubmitted` | Customer/Supplier (seller-inbound); Conformist (node-performance) |
| node-topology | RegisterNode, DeclareOperationalProfile | Command | REST (in-org) `POST /nodes`, `PUT /nodes/{nodeId}/operational-profile` | Customer/Supplier (this context is the Customer) |
| offer-availability | ApplyInventoryFeed | Command | REST (in-org) `POST /inventory-feeds` | Customer/Supplier (this context is the Customer) |
| shipping-and-tracking | IssueLabel | Command | REST (in-org) `POST /labels` | Customer/Supplier (this context is the Customer) |
| node-performance | GetScorecard | Query | REST (in-org) `GET /scorecards/{nodeId}` | Customer/Supplier (this context is the Customer) |

Full payloads: [Domain Events](/docs/ddd/domain-events).
Patterns and reasons: [Context Map](/docs/ddd/context-map).

## Ubiquitous Language

Full glossary with the fleet translation:
[Ubiquitous Language](/docs/ddd/ubiquitous-language).
Top terms: **Vendor**, **Purchase Order** (`poNumber`), **Acknowledgement**
(fill-or-kill), **ackBy**, **Required Ship-By**, **Submission /
Transaction** (`transactionId`), **Label** (never the address), **Vendor
API**.

## Business Decisions

1. **Acknowledge once, in full, before `ackBy`.** Fill-or-kill; partial
   acknowledgements are refused (`ErrPartialAcknowledgement`,
   `ErrAlreadyAcknowledged`, `ErrAckWindowClosed`).
2. **`ackBy = min(issuedAt + vendor ackWindow, requiredShipBy - node handlingTime)`.**
3. **A label is issued only for an `ACCEPTED` purchase order; a shipment is
   confirmed only after a label** (`ErrLabelBeforeAcceptance`,
   `ErrConfirmWithoutLabel`).
4. **Every vendor write returns `202` with a `transactionId`;** the write
   takes effect when its transaction succeeds.
5. **The Vendor API never carries ship-to PII.**
6. **Rate limits are part of the contract** (`429` with `Retry-After`).
7. **One release, one purchase order.** `poNumber` is idempotent on
   `assignmentId + releaseNo` (policy `IssueOnRelease`), so a redelivered
   release never issues a second order.
8. **Cancel only while `ISSUED`** (`ErrCancelAfterAcknowledgement`, policy
   `CancelOnSourcingCancel`).

## Assumptions

- `order-sourcing` releases to a vendor node early enough that `ackBy` is in
  the future (its `VirtualQueue` policy subtracts a vendor ack budget).
- The node's handling time is known to this context when it issues a
  purchase order: it arrives with `NodeOperationalProfileDeclared` (`handlingTimeMinutes`) and is
  kept in the local node view; a node with no profile yet has handling time 0.
- Vendors poll often enough to see a purchase order well before `ackBy`.
- The four simulation knobs default to well-behaved; a non-default value
  is a test setting, not production behaviour.
- One vendor per node: a node's `vendorId` comes from `NodeRegistered`.

## Verification Metrics

The service emits one business instrument, `vendor.submissions.processed` (see
[Observability](/docs/operations/observability)); the other metrics below are derived from the
events and are not implemented as instruments.

| Metric | Source | Target (judgement) |
| --- | --- | --- |
| Share of purchase orders acknowledged before `ackBy` | `PurchaseOrderAccepted` + `PurchaseOrderRejected` vs `PurchaseOrderExpired` | Expiry rate tracked per node by node-performance (`expiryRate`) |
| Acknowledgement latency (issue to accepted or rejected) | `PurchaseOrderIssued.time` to `PurchaseOrderAccepted.acceptedAt` / `PurchaseOrderRejected.rejectedAt` | p95 well inside the vendor's ack window |
| Transaction failure share by kind | `SubmissionProcessed` (`kind`, `status`, `errorCount`) | Equals `TX_FAILURE_RATE` in simulation; near 0 otherwise |
| Processing lag | `processedAt - notBefore` per submission | p95 under one processor tick |
| `429` responses per vendor | API metrics | Low and stable; a spike means a vendor is not honouring `Retry-After` |
| Shipments confirmed on time | `VendorShipmentConfirmed` (`shippedAt` vs `requiredShipBy`) | Feeds node-performance's on-time handover |
| Ship-to PII in the Vendor API | Contract review of `openapi.yaml` | Zero fields, always |

## Open Questions

Questions the aggregator's model raised, and how the code settled them:

- **Where does `ackBy` get `handlingTime`?** Settled: from `NodeOperationalProfileDeclared`
  (`OnNodeProfileDeclared`), stored on the node view. A node with no declared profile has handling
  time 0, so its `ackBy` is bounded by the vendor window and `requiredShipBy` alone.
- **Cancellation after acknowledgement (one race stays open).** order-sourcing may cancel a
  `RELEASED` assignment while its purchase order is `ACK_PROCESSING` or `ACCEPTED`; here cancel is
  refused (`ErrCancelAfterAcknowledgement`), logged at INFO (`purchase order not cancelled`) and
  nothing is published. An acceptance that arrives after order-sourcing cancelled a `RELEASED`
  assignment is only logged there, so the vendor may still ship a cancelled group.
- **Vendor suspension.** Settled: `POST /vendors/{vendorId}:suspend` and `:reinstate` exist, raise
  `VendorSuspended` and `VendorReinstated`, and a suspended vendor's writes answer
  `409 vendor-suspended` while reads stay open. No consumer reads the two events yet.
- **The label request is synchronous.** Settled: `POST /vendor/shipping-labels` returns `200` with
  the labels; it is the one write that does not return `202`.
- **How does a vendor learn its `nodeId`?** Settled: the processed `NODE_REGISTRATION` transaction
  carries the new `nodeId` (`result_node_id`).
- **Who is calling?** Settled: `vendorId` is in every write body and a required query parameter on
  every read; `ErrNodeNotOwnedByVendor` is checked on both. It is a consistency check, not security.
- **Synchronous refusal or failed transaction?** Settled: an invariant this context can check at
  receipt is refused synchronously (4xx, no transaction); a neighbour's refusal during processing
  ends the transaction in `FAILURE` with the neighbour's `Err...` code, and a neighbour outage
  leaves it `PROCESSING` for up to 5 passes.
