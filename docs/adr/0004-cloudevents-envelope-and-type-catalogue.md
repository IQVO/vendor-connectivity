# ADR 0004: CloudEvents envelope and type catalogue

## Status

Accepted (2026-10-23).

## Context

Every Kafka message in the retail network is a CloudEvents 1.0 event in structured
mode (retail-network-docs `model/model.yaml` `conventions.cloudevents`; the repo
rule `.claude/rules/fleet/cloudevents.md`). This ADR is the catalogue of every
type vendor-connectivity publishes. `TestEventCatalogueMatchesContract` checks it
against `apis/asyncapi.yaml`. The types vendor-connectivity consumes are listed in
ADR 0003.

## Decision

### Envelope

- `specversion` `1.0`; `id` a UUID persisted with the outbox row (stable across
  relay retries; consumers dedupe on it); `source`
  `/retailnetwork/vendor-connectivity`; `subject` the aggregate root id
  (`vendorId`, `poNumber` or `transactionId`); `time` the instant of the fact, in
  UTC; `datacontenttype` `application/json`; `dataschema`
  `urn:retailnetwork:vendor-connectivity:<stream>:<EventName>:v1` with stream
  `events` or `analytics`.
- Kafka value = the JSON event format; header
  `content-type: application/cloudevents+json; charset=UTF-8`; Kafka key = the
  subject, so all events of one purchase order stay ordered on one partition.
- `data` is camelCase; optional fields are omitted when unset (never `null`);
  arrays are always present; timestamps are RFC 3339 UTC.
- Built and decoded only through `internal/adapters/kafka/cloudevents`.
- `<entity>` is the raising aggregate, lowercase, no separators: `vendoraccount`,
  `purchaseorder`, `submission`. Tier is `partner`.

### Published types

Nine types go to the topic `retailnetwork.vendor-connectivity.events`.
`PurchaseOrderCancelled` is the one analytics-only event of the model ("analytics
only" consumers) and goes ONLY to `retailnetwork.vendor-connectivity.analytics`.

```text
type: com.retailnetwork.partner.vendor-connectivity.vendoraccount.VendorOnboarded stream: events
type: com.retailnetwork.partner.vendor-connectivity.vendoraccount.VendorSuspended stream: events
type: com.retailnetwork.partner.vendor-connectivity.vendoraccount.VendorReinstated stream: events
type: com.retailnetwork.partner.vendor-connectivity.purchaseorder.PurchaseOrderIssued stream: events
type: com.retailnetwork.partner.vendor-connectivity.purchaseorder.PurchaseOrderAccepted stream: events
type: com.retailnetwork.partner.vendor-connectivity.purchaseorder.PurchaseOrderRejected stream: events
type: com.retailnetwork.partner.vendor-connectivity.purchaseorder.PurchaseOrderExpired stream: events
type: com.retailnetwork.partner.vendor-connectivity.purchaseorder.PurchaseOrderCancelled stream: analytics
type: com.retailnetwork.partner.vendor-connectivity.purchaseorder.VendorShipmentConfirmed stream: events
type: com.retailnetwork.partner.vendor-connectivity.submission.SubmissionProcessed stream: events
```

| Event | Subject / key | Raised when | `data` | Consumers |
|---|---|---|---|---|
| `VendorOnboarded` | `vendorId` | OnboardVendor is saved | `{vendorId, name, ackWindowMinutes}` | node-performance |
| `VendorSuspended` | `vendorId` | SuspendVendor is saved (Gap G30) | `{vendorId, suspendedAt, reason?}` | none yet |
| `VendorReinstated` | `vendorId` | ReinstateVendor is saved (Gap G30) | `{vendorId, reinstatedAt}` | none yet |
| `PurchaseOrderIssued` | `poNumber` | IssuePurchaseOrder is saved (state ISSUED) | `{poNumber, vendorId, nodeId, assignmentId, releaseNo, requiredShipBy, ackBy, lines[{lineNo, listingId, quantity}]}` | node-performance |
| `PurchaseOrderAccepted` | `poNumber` | the ACCEPT acknowledgement transaction succeeded | `{poNumber, assignmentId, releaseNo, nodeId, acceptedAt, transactionId}` | order-sourcing, node-performance |
| `PurchaseOrderRejected` | `poNumber` | the REJECT acknowledgement transaction succeeded | `{poNumber, assignmentId, releaseNo, nodeId, reasonCode, rejectedAt}` | order-sourcing, node-performance |
| `PurchaseOrderExpired` | `poNumber` | the sweep expired an ISSUED order past `ackBy` | `{poNumber, assignmentId, releaseNo, nodeId, ackBy}` | order-sourcing, node-performance |
| `PurchaseOrderCancelled` | `poNumber` | CancelPurchaseOrder is saved (analytics stream) | `{poNumber, assignmentId, reason}` | analytics only |
| `VendorShipmentConfirmed` | `poNumber` | the shipment confirmation transaction succeeded | `{poNumber, assignmentId, nodeId, trackingNumbers[], shippedAt, requiredShipBy}` | order-sourcing, shipping-and-tracking, node-performance |
| `SubmissionProcessed` | `transactionId` | a submission left PROCESSING (SUCCESS or FAILURE) | `{transactionId, vendorId, kind, status, errorCount}` | node-performance |

Payload rules:

- The `data` field lists are exactly the model's `publishes[].data` lists, plus the
  two events of Gap G30 (`VendorSuspended`, `VendorReinstated`; their stream is an
  assumption, see ADR 0002 A14). Wire types are recorded in `apis/asyncapi.yaml`.
- No event carries `orderId`, a ship-to address, a name or a phone. `orderId` stays in
  this service and goes only into the in-organization `IssueLabel` call (Gap G2).
- `PurchaseOrderAccepted` is the path lock (retail-network ADR 0005): it is written in
  the same transaction that moves the order to ACCEPTED.
- A breaking payload change is a new `.v2` type and dataschema, never a mutation of v1.

## Consequences

- Consumers need nothing but this catalogue and `apis/asyncapi.yaml`; there is no
  shared Go code.
- Adding a published type requires a line here in the same commit as its asyncapi
  message, or the architecture fitness test fails.
