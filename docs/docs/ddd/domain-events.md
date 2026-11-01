---
id: domain-events
title: Domain Events
sidebar_label: Domain Events
sidebar_position: 10
description: "Every event Vendor Connectivity publishes and consumes, with full CloudEvents types, topics, keys, payload fields, producers, consumers and idempotency."
---

# Domain Events

These are **in-organization** events. Vendors never see them; vendors see
the Vendor API's Published Language
(`apis/openapi.yaml`). The convention is on
[Event standard: CloudEvents](https://iqvo.github.io/warehouse-docs/network/strategic-design/event-standard-cloudevents)
and [ADR 0007](https://iqvo.github.io/warehouse-docs/network/adr/0007-cloudevents-namespace).

## Envelope

| Attribute | Value |
| --- | --- |
| `specversion` | `1.0` |
| `id` | a UUID minted once per event and stored in the outbox row, so a redelivery carries the same `id` |
| `source` | `/retailnetwork/vendor-connectivity` |
| `type` | `com.retailnetwork.partner.vendor-connectivity.<entity>.<EventName>`; entity is `vendoraccount`, `purchaseorder`, `submission` or `inboundreceipt` (`InboundReceiptSubmitted` only) |
| `subject` | the aggregate root id: `vendorId`, `poNumber`, `transactionId`, or `shipmentId` for `InboundReceiptSubmitted` |
| `time` | the instant of the fact, UTC |
| `datacontenttype` | `application/json` |
| `dataschema` | `urn:retailnetwork:vendor-connectivity:<events\|analytics>:<EventName>:v1` |
| Topic | `retailnetwork.vendor-connectivity.events`; `PurchaseOrderCancelled` goes only to `retailnetwork.vendor-connectivity.analytics` |
| Kafka key | same as `subject`, so one purchase order's events stay in order on one partition |

## Published

Every event leaves through the transactional outbox, in the same
transaction as the aggregate change.

### VendorOnboarded

| | |
| --- | --- |
| Type | `com.retailnetwork.partner.vendor-connectivity.vendoraccount.VendorOnboarded` |
| Subject / key | `vendorId` |
| Produced by | `OnboardVendor` (REST `POST /vendors`) |
| Consumers | node-performance |

| Field | Type | Meaning |
| --- | --- | --- |
| `vendorId` | string | The vendor account id, for example `V-WS` |
| `name` | string | The vendor's name |
| `ackWindowMinutes` | integer | The vendor's ack window, 60 to 1440 |

### VendorSuspended

| | |
| --- | --- |
| Type | `com.retailnetwork.partner.vendor-connectivity.vendoraccount.VendorSuspended` |
| Subject / key | `vendorId` |
| Produced by | `SuspendVendor` (REST `POST /vendors/{vendorId}:suspend`), only when the account was `ACTIVE` |
| Consumers | none yet |

| Field | Type | Meaning |
| --- | --- | --- |
| `vendorId` | string | The vendor account id |
| `suspendedAt` | date-time | When the suspension took effect |
| `reason` | string, optional | The operator's reason |

### VendorReinstated

| | |
| --- | --- |
| Type | `com.retailnetwork.partner.vendor-connectivity.vendoraccount.VendorReinstated` |
| Subject / key | `vendorId` |
| Produced by | `ReinstateVendor` (REST `POST /vendors/{vendorId}:reinstate`), only when the account was `SUSPENDED` |
| Consumers | none yet |

| Field | Type | Meaning |
| --- | --- | --- |
| `vendorId` | string | The vendor account id |
| `reinstatedAt` | date-time | When the reinstatement took effect |

### PurchaseOrderIssued

| | |
| --- | --- |
| Type | `com.retailnetwork.partner.vendor-connectivity.purchaseorder.PurchaseOrderIssued` |
| Subject / key | `poNumber` |
| Produced by | `IssueOnRelease` (command `IssuePurchaseOrder`) |
| Consumers | node-performance |

| Field | Type | Meaning |
| --- | --- | --- |
| `poNumber` | string | The purchase order |
| `vendorId` | string | The vendor that operates the node |
| `nodeId` | string | The vendor node |
| `assignmentId` | string | order-sourcing's assignment |
| `releaseNo` | integer | The release of that assignment |
| `requiredShipBy` | date-time | The pickup cutoff to be ready for |
| `ackBy` | date-time | `min(issuedAt + ackWindow, requiredShipBy - handlingTime)` |
| `lines` | array of `{lineNo, listingId, quantity}` | The `PoLine`s |

### PurchaseOrderAccepted

| | |
| --- | --- |
| Type | `com.retailnetwork.partner.vendor-connectivity.purchaseorder.PurchaseOrderAccepted` |
| Subject / key | `poNumber` |
| Produced by | `ProcessSubmission` (acknowledgement `ACCEPT`, transaction `SUCCESS`) |
| Consumers | order-sourcing, node-performance |

| Field | Type | Meaning |
| --- | --- | --- |
| `poNumber` | string | The purchase order |
| `assignmentId` | string | order-sourcing's assignment |
| `releaseNo` | integer | The release |
| `nodeId` | string | The vendor node |
| `acceptedAt` | date-time | When the acceptance took effect |
| `transactionId` | string | The acknowledgement submission |

### PurchaseOrderRejected

| | |
| --- | --- |
| Type | `com.retailnetwork.partner.vendor-connectivity.purchaseorder.PurchaseOrderRejected` |
| Subject / key | `poNumber` |
| Produced by | `ProcessSubmission` (acknowledgement `REJECT`, transaction `SUCCESS`) |
| Consumers | order-sourcing, node-performance |

| Field | Type | Meaning |
| --- | --- | --- |
| `poNumber` | string | The purchase order |
| `assignmentId` | string | order-sourcing's assignment |
| `releaseNo` | integer | The release |
| `nodeId` | string | The vendor node |
| `reasonCode` | enum | `OUT_OF_STOCK`, `UNKNOWN_LISTING`, `CANNOT_MEET_SHIP_BY`, `DISCONTINUED` |
| `rejectedAt` | date-time | When the rejection took effect |

### PurchaseOrderExpired

| | |
| --- | --- |
| Type | `com.retailnetwork.partner.vendor-connectivity.purchaseorder.PurchaseOrderExpired` |
| Subject / key | `poNumber` |
| Produced by | `ExpireUnacknowledged` (the expiry sweeper) |
| Consumers | order-sourcing, node-performance |

| Field | Type | Meaning |
| --- | --- | --- |
| `poNumber` | string | The purchase order |
| `assignmentId` | string | order-sourcing's assignment |
| `releaseNo` | integer | The release |
| `nodeId` | string | The vendor node |
| `ackBy` | date-time | The deadline that passed |

### PurchaseOrderCancelled

| | |
| --- | --- |
| Type | `com.retailnetwork.partner.vendor-connectivity.purchaseorder.PurchaseOrderCancelled` |
| Subject / key | `poNumber` |
| Produced by | `CancelOnSourcingCancel`; published to the analytics topic only |
| Consumers | none (no repository reads the analytics topic) |

| Field | Type | Meaning |
| --- | --- | --- |
| `poNumber` | string | The purchase order |
| `assignmentId` | string | order-sourcing's assignment |
| `reason` | string | The reason carried by `FulfillmentCancelled`, trimmed and capped at 200 characters |

### VendorShipmentConfirmed

| | |
| --- | --- |
| Type | `com.retailnetwork.partner.vendor-connectivity.purchaseorder.VendorShipmentConfirmed` |
| Subject / key | `poNumber` |
| Produced by | `ProcessSubmission` (shipment confirmation, transaction `SUCCESS`) |
| Consumers | order-sourcing, shipping-and-tracking, node-performance |

| Field | Type | Meaning |
| --- | --- | --- |
| `poNumber` | string | The purchase order |
| `assignmentId` | string | order-sourcing's assignment |
| `nodeId` | string | The vendor node |
| `trackingNumbers` | array of string | One per package, each with a label |
| `shippedAt` | date-time | When the vendor says it shipped |
| `requiredShipBy` | date-time | The cutoff it was due for, so consumers can judge lateness without a lookup |

### SubmissionProcessed

| | |
| --- | --- |
| Type | `com.retailnetwork.partner.vendor-connectivity.submission.SubmissionProcessed` |
| Subject / key | `transactionId` |
| Produced by | `ProcessSubmission` (any kind, any outcome) |
| Consumers | node-performance (feed freshness, failure rates) |

| Field | Type | Meaning |
| --- | --- | --- |
| `transactionId` | string | The submission |
| `vendorId` | string | The vendor that wrote |
| `kind` | enum | `ACKNOWLEDGEMENT`, `INVENTORY`, `NODE_REGISTRATION`, `NODE_PROFILE`, `SHIPMENT_CONFIRMATION`, `INBOUND_RECEIPT` |
| `status` | enum | `SUCCESS` or `FAILURE` |
| `errorCount` | integer | Number of `SubmissionError`s |

No event carries a ship-to address, name or phone.

## Example envelope

`PurchaseOrderAccepted` is the most important event: it is the path lock
([ADR 0005](https://iqvo.github.io/warehouse-docs/network/adr/0005-vendor-acceptance-is-the-path-lock)).

```json
{
  "specversion": "1.0",
  "id": "9b2f4c1e-6a7d-4e0b-8f31-2c5d7a9e1b44",
  "source": "/retailnetwork/vendor-connectivity",
  "type": "com.retailnetwork.partner.vendor-connectivity.purchaseorder.PurchaseOrderAccepted",
  "subject": "PO-7f3c",
  "time": "2026-10-22T13:05:41Z",
  "datacontenttype": "application/json",
  "dataschema": "urn:retailnetwork:vendor-connectivity:events:PurchaseOrderAccepted:v1",
  "data": {
    "poNumber": "PO-7f3c",
    "assignmentId": "asg-41d2",
    "releaseNo": 1,
    "nodeId": "node-ws-gru1",
    "acceptedAt": "2026-10-22T13:05:41Z",
    "transactionId": "tx-01J9Z6K4M2"
  }
}
```

## Consumed

Every consumer is idempotent on the CloudEvents `id` (the
`processed_events` inbox, written in the same transaction as the effect)
and dispatches on the full `type` string.

| Producer | Full type | What this context does | Idempotency beyond the inbox |
| --- | --- | --- | --- |
| order-sourcing | `com.retailnetwork.commerce.order-sourcing.assignment.FulfillmentReleasedToNode` | Policy `IssueOnRelease`: if `nodeId` is a `VENDOR` node in `VendorNodeView`, issue a purchase order from `assignmentId`, `releaseNo`, `requiredShipBy`, `tier` and `lines` (`listingId`, `quantity`); `orderId` and `tier` are kept internally for the label call and never published; anything else in the payload is ignored. A node not yet in the view is a retryable failure | `poNumber` is derived from `assignmentId + releaseNo`; a second release event with the same pair finds the order and does nothing |
| order-sourcing | `com.retailnetwork.commerce.order-sourcing.assignment.FulfillmentCancelled` | Policy `CancelOnSourcingCancel`: `CancelPurchaseOrder` on the order of the assignment's highest `releaseNo` (no order is a no-op) | A `CANCELLED` order is left as is; a non-`ISSUED` one is refused (`ErrCancelAfterAcknowledgement`), logged at INFO, nothing published and not retried |
| node-topology | `com.retailnetwork.logistics.node-topology.node.NodeRegistered` | Upsert `VendorNodeView` (`nodeId`, `vendorId`, `vendorNodeCode`, `nodeType`, `subtype`, `operator`, `regionId`, `timezone`); a brand new node is `PROPOSED`, an existing status is kept | Upsert by `nodeId` |
| node-topology | `com.retailnetwork.logistics.node-topology.node.NodeOperationalProfileDeclared` | Store `handlingTimeMinutes` and `profileRevision` (feeds `ackBy`) | A revision not higher than the stored one is ignored |
| node-topology | `com.retailnetwork.logistics.node-topology.node.NodeActivated` | Set status `ACTIVE` in `VendorNodeView` | Last write wins unless the node is `RETIRED` |
| node-topology | `com.retailnetwork.logistics.node-topology.node.NodeSuspended` | Set status `SUSPENDED` | Last write wins unless the node is `RETIRED` |
| node-topology | `com.retailnetwork.logistics.node-topology.node.NodeRetired` | Set status `RETIRED` | Terminal: no later event changes it |
| offer-availability | `com.retailnetwork.commerce.offer-availability.listing.ListingCreated` | Save into `ListingView` (`listingId`, `catalogItemId`, `nodeId`); invalid ids are logged and ignored | Keyed by `listingId` |

## Inbound receipts

> **Built**: [ADR 0005](/docs/adr/0005-inbound-shipments-receipts-and-catalog-packaging) of this repository realises the aggregator's seller-inbound design.

### InboundReceiptSubmitted (published)

| | |
| --- | --- |
| Type | `com.retailnetwork.partner.vendor-connectivity.inboundreceipt.InboundReceiptSubmitted` |
| Subject / key | `shipmentId` |
| Produced by | `SubmitInboundReceipt`, in the same unit of work that accepts the `INBOUND_RECEIPT` submission (ADR 0005); `SubmissionProcessed` still reports the processing |
| Consumers | `seller-inbound`, `node-performance` (inbound timeliness and accuracy observations) |

| Field | Type | Meaning |
| --- | --- | --- |
| `transactionId` | string | The submission |
| `vendorId` | string | The vendor that wrote |
| `nodeId` | string | The receiving node |
| `shipmentId` | string | The inbound shipment the receipt answers |
| `receivedAt` | RFC 3339 instant | When the vendor says the cartons arrived |
| `cartons` | array | Per carton: `sscc`, `status` (`RECEIVED`, `MISSING`, `UNEXPECTED`, `DAMAGED`), `items` (`gtin`, `qtyEaches` received), `complianceCodes` |

Quantities are eaches. No event carries a ship-to address, name or phone.

### Consumed

| Producer | Full type | What this context does |
| --- | --- | --- |
| `seller-inbound` | `com.retailnetwork.partner.seller-inbound.inboundshipment.InboundShipmentLabelled` | Insert into `InboundShipmentView` (the carton manifest the vendor polls), with `sellerId` exposed unchanged (the vendor carries it as its `ownerRef`). |
| `seller-inbound` | `com.retailnetwork.partner.seller-inbound.inboundshipment.InboundShipmentShipped` | Mark the shipment `SHIPPED` in `InboundShipmentView`; only `SHIPPED` shipments are returned by the poll. |
| `seller-inbound` | `com.retailnetwork.partner.seller-inbound.inboundshipment.InboundShipmentReceived` | Mark the shipment closed in `InboundShipmentView`; it leaves the poll. |
| `offer-availability` | `com.retailnetwork.partner.offer-availability.catalogitem.CatalogItemPackagingDeclared` (the `commerce` tier spelling is accepted too) | Upsert `CatalogPackagingView` (levels, GTINs, `unitsPerPack`). |
| `offer-availability` | `com.retailnetwork.partner.offer-availability.catalogitem.CatalogItemPackLevelRetired` (or the `commerce` spelling) | Mark the level inactive in `CatalogPackagingView`. |

Source: `internal/domain/*/events.go` (event structs), `internal/adapters/outbound/kafka/encoder.go` (payloads, topics, subjects), `internal/adapters/inbound/kafka/consumers.go` and `inbound_consumers.go` (consumed types) and `apis/asyncapi.yaml`. The full catalogue is on [Events](/docs/api-reference/events).
