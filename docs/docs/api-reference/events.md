---
id: events
title: Event catalogue
sidebar_label: Event catalogue
sidebar_position: 2
---

# Event catalogue

Every Kafka message this service produces or consumes is a CloudEvents 1.0 event in
**structured mode**: the Kafka value is the whole JSON envelope and the header `content-type` is
`application/cloudevents+json; charset=UTF-8`. Envelopes are built and decoded only in
`internal/adapters/kafka/cloudevents` ([ADR 0004](/docs/adr/0004-cloudevents-envelope-and-type-catalogue)).
The contract is `apis/asyncapi.yaml` (AsyncAPI 3.0.0); field-level detail is on
[Domain events](/docs/ddd/domain-events). The tables below are the pinned contract and the
encoder, not observed traffic.

## Envelope

| Attribute | Value |
| --- | --- |
| `specversion` | `1.0` |
| `id` | UUID v4, minted once when the event is appended to the outbox and stored with the row, so a relay retry republishes the same `id` |
| `source` | `/retailnetwork/vendor-connectivity` |
| `type` | `com.retailnetwork.partner.vendor-connectivity.<entity>.<EventName>`; entity `vendoraccount`, `purchaseorder`, `submission` or `inboundreceipt` |
| `subject` | the aggregate root id (see the table); also the Kafka message key, so one subject's events stay ordered on one partition |
| `time` | the domain occurrence time, RFC 3339 UTC |
| `datacontenttype` | `application/json` |
| `dataschema` | `urn:retailnetwork:vendor-connectivity:<events\|analytics>:<EventName>:v1` |

`data` is camelCase; timestamps are RFC 3339 UTC; optional fields are omitted when absent (never
`null`); arrays are always present. No event carries a ship-to address, name or phone.

## Published

Eleven types, all through the transactional outbox and the always-on relay (at least once). Ten
go to `retailnetwork.vendor-connectivity.events`; `PurchaseOrderCancelled` goes **only** to
`retailnetwork.vendor-connectivity.analytics`, which no repository consumes. Source:
`internal/adapters/outbound/kafka/encoder.go` and the `events.go` of each domain package.

| Type (after the common prefix) | Subject and key | Raised when | `data` |
| --- | --- | --- | --- |
| `vendoraccount.VendorOnboarded` | `vendorId` | `OnboardVendor` creates an account | `vendorId`, `name`, `ackWindowMinutes` |
| `vendoraccount.VendorSuspended` | `vendorId` | `SuspendVendor` | `vendorId`, `suspendedAt`, `reason?` |
| `vendoraccount.VendorReinstated` | `vendorId` | `ReinstateVendor` | `vendorId`, `reinstatedAt` |
| `purchaseorder.PurchaseOrderIssued` | `poNumber` | a release becomes a purchase order | `poNumber`, `vendorId`, `nodeId`, `assignmentId`, `releaseNo`, `requiredShipBy`, `ackBy`, `lines[{lineNo, listingId, quantity}]` |
| `purchaseorder.PurchaseOrderAccepted` | `poNumber` | an `ACCEPT` acknowledgement transaction succeeded | `poNumber`, `assignmentId`, `releaseNo`, `nodeId`, `acceptedAt`, `transactionId` |
| `purchaseorder.PurchaseOrderRejected` | `poNumber` | a `REJECT` acknowledgement transaction succeeded | `poNumber`, `assignmentId`, `releaseNo`, `nodeId`, `reasonCode`, `rejectedAt` |
| `purchaseorder.PurchaseOrderExpired` | `poNumber` | the expiry sweeper found an `ISSUED` order past `ackBy` | `poNumber`, `assignmentId`, `releaseNo`, `nodeId`, `ackBy` |
| `purchaseorder.VendorShipmentConfirmed` | `poNumber` | a shipment confirmation transaction succeeded | `poNumber`, `assignmentId`, `nodeId`, `trackingNumbers[]`, `shippedAt`, `requiredShipBy` |
| `purchaseorder.PurchaseOrderCancelled` (**analytics topic only**) | `poNumber` | `FulfillmentCancelled` cancelled an `ISSUED` order | `poNumber`, `assignmentId`, `reason` |
| `submission.SubmissionProcessed` | `transactionId` | any asynchronous write reached `SUCCESS` or `FAILURE` | `transactionId`, `vendorId`, `kind`, `status`, `errorCount` |
| `inboundreceipt.InboundReceiptSubmitted` | `shipmentId` | an inbound receipt was accepted (same transaction as the submission) | `transactionId`, `vendorId`, `nodeId`, `shipmentId`, `receivedAt`, `cartons[{sscc, status, items[{gtin, qtyEaches}], complianceCodes[]}]` |

Consumers read on the neighbours' `origin/develop`: order-sourcing (the `purchaseorder.` types),
node-performance (all of them, including `InboundReceiptSubmitted`), shipping-and-tracking
(`VendorShipmentConfirmed`) and seller-inbound (`InboundReceiptSubmitted`). The three
`vendoraccount` events have no consumer yet (`x-no-consumer-reason` in the AsyncAPI).
See [Integration](/docs/ecosystem/integration).

## Consumed

Thirteen types from four producers, each deduplicated on the CloudEvents `id` in
`processed_events`, dispatched on the full `type`, decoded tolerantly (unknown fields ignored)
and dead-lettered to `<topic>.dlq` when they cannot be applied. Source:
`internal/adapters/inbound/kafka/consumers.go` and `inbound_consumers.go`.

| Producer and topic | Type | Fields relied on |
| --- | --- | --- |
| node-topology, `retailnetwork.node-topology.events` | `com.retailnetwork.logistics.node-topology.node.NodeRegistered` | `nodeId`, `nodeType`, `subtype`, `operator`, `vendorId`, `vendorNodeCode`, `regionId`, `timezone` |
| same | `...node.NodeOperationalProfileDeclared` | `nodeId`, `profileRevision`, `handlingTimeMinutes` |
| same | `...node.NodeActivated`, `NodeSuspended`, `NodeRetired` | `nodeId` |
| offer-availability, `retailnetwork.offer-availability.events` | `com.retailnetwork.commerce.offer-availability.listing.ListingCreated` | `listingId`, `catalogItemId`, `nodeId` |
| same | `com.retailnetwork.partner.offer-availability.catalogitem.CatalogItemPackagingDeclared` and `CatalogItemPackLevelRetired` (the `commerce` tier spelling is accepted too) | `catalogItemId`, `level`, `gtin`, `childGtin`, units per pack, timestamps |
| order-sourcing, `retailnetwork.order-sourcing.events` | `com.retailnetwork.commerce.order-sourcing.assignment.FulfillmentReleasedToNode` | `assignmentId`, `releaseNo`, `orderId`, `groupNo`, `nodeId`, `requiredShipBy`, `tier`, `lines[{listingId, quantity}]` |
| same | `...assignment.FulfillmentCancelled` | `assignmentId`, `reason` |
| seller-inbound, `retailnetwork.seller-inbound.events` | `com.retailnetwork.partner.seller-inbound.inboundshipment.InboundShipmentLabelled` | `shipmentId`, `planId`, `sellerId`, `nodeId`, `cartons[]` (SSCC, parent, kind, contents, weight, dimensions), `labelledAt` |
| same | `...inboundshipment.InboundShipmentShipped` | `shipmentId`, `planId`, `sellerId`, `nodeId`, `shippedAt` |
| same | `...inboundshipment.InboundShipmentReceived` | `shipmentId`, `sellerId`, `nodeId`, `status`, `receivedAt` |

Failure handling per message is described on [Integration](/docs/ecosystem/integration) and
[Troubleshooting](/docs/operations/troubleshooting).

## Discrepancies

- `apis/asyncapi.yaml` describes the node-topology channel as "consumed under the mandatory
  consumer group `VENDOR_CONNECTIVITY_CONSUMER_GROUP`". The code reads five variables instead,
  `NODE_CONSUMER_GROUP`, `LISTING_CONSUMER_GROUP`, `SOURCING_CONSUMER_GROUP`,
  `INBOUND_CONSUMER_GROUP` and `PACKAGING_CONSUMER_GROUP` (`cmd/api/config.go`); there is no
  `VENDOR_CONNECTIVITY_CONSUMER_GROUP` in this repository (that name is order-sourcing's variable
  for consuming this context's events).
- The AsyncAPI introduction lists `transactionId` among the possible subjects; the subject of
  `InboundReceiptSubmitted` is the `shipmentId` (`internal/domain/inbound/receipt.go`,
  `SubjectID`), as the message's own description says.
