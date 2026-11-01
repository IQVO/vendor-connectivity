---
id: aggregate-design-canvas
title: Aggregate Design Canvas
sidebar_label: Aggregate Design Canvas
sidebar_position: 4
description: "The ddd-crew Aggregate Design Canvas for VendorAccount, PurchaseOrder and Submission, with states, invariants, policies, commands, events and estimates."
---

# Aggregate Design Canvas

Following the ddd-crew [Aggregate Design Canvas v1.1](https://github.com/ddd-crew/aggregate-design-canvas).
Three aggregate roots: **VendorAccount**, **PurchaseOrder** and
**Submission**. The Go packages are `internal/domain/vendor`,
`internal/domain/purchaseorder` and `internal/domain/submission`; the Postgres tables are `vendor_accounts`, `purchase_orders` and `submissions`.

## VendorAccount

### 1. Name

`VendorAccount` (`vendor.Account`), identified by `vendorId`.

### 2. Description

A vendor organisation, its standing and its ack window. The ack window feeds every purchase
order's `ackBy`. The nodes a vendor may operate come from the `VendorNodeView` read model
(`vendor_nodes`, fed by node-topology), not from a collection inside the aggregate. The rate
limit is not part of the account: it is one process-wide bucket (`RATE_LIMIT_RPS`).

### 3. State Transitions

```mermaid
stateDiagram-v2
    [*] --> ACTIVE: OnboardVendor
    ACTIVE --> SUSPENDED: SuspendVendor
    SUSPENDED --> ACTIVE: ReinstateVendor
```

Source: `internal/domain/vendor/vendor.go` (`Onboard`, `Suspend`, `Reinstate`) and `internal/application/usecases/vendors.go`.

Omitted: nothing. There is no terminal state and no delete; suspending a suspended vendor, or reinstating an active one, is a no-op that raises no event.

### 4. Enforced Invariants

| Invariant | Enforced by |
| --- | --- |
| `ackWindow` is between 1h and 24h (60 to 1440 minutes, also a database check) | `ErrAckWindowOutOfRange` |
| A vendor acts only on its own nodes and orders | `ErrNodeNotOwnedByVendor` (checked against `VendorNodeView` for node-scoped calls and against the order's `vendorId` for PO-scoped calls) |
| A suspended vendor's writes are refused; its reads are not | `ErrVendorSuspended` (`requireActive` before every vendor write) |

### 5. Corrective Policies

None. The only coupling is the guard above: a `SUSPENDED` vendor's writes answer `409 vendor-suspended`, its reads (polls, transaction status, scorecard) still work, and purchase orders already issued stay as they are.

### 6. Handled Commands

| Command | Issued by | Channel |
| --- | --- | --- |
| `OnboardVendor` | Network partner manager | REST `POST /vendors` |
| `SuspendVendor` | operator | REST `POST /vendors/{vendorId}:suspend` (optional `reason`) |
| `ReinstateVendor` | operator | REST `POST /vendors/{vendorId}:reinstate` |

### 7. Created Events

| Event | Raised when | Full CloudEvents type |
| --- | --- | --- |
| `VendorOnboarded` | `OnboardVendor` creates the account | `com.retailnetwork.partner.vendor-connectivity.vendoraccount.VendorOnboarded` |
| `VendorSuspended` | `SuspendVendor` on an `ACTIVE` account | `com.retailnetwork.partner.vendor-connectivity.vendoraccount.VendorSuspended` |
| `VendorReinstated` | `ReinstateVendor` on a `SUSPENDED` account | `com.retailnetwork.partner.vendor-connectivity.vendoraccount.VendorReinstated` |

### 8. Throughput

*Estimate:* rare. Onboarding and suspension are human acts.

### 9. Size

*Estimate:* tens of vendors. One small row each; one event in its life.

## PurchaseOrder

### 1. Name

`PurchaseOrder` (`purchaseorder.PurchaseOrder`), identified by `poNumber`.
`poNumber` is idempotent on `assignmentId + releaseNo`.

### 2. Description

The network's instruction to one vendor node to ship one shipment group in
full by `requiredShipBy`. It owns the protocol and its clock: the deadline
`ackBy`, the single whole-order acknowledgement, the label gate and the
shipment confirmation. Value objects: 
`Line{LineNo, ListingID, Quantity}`, `Ack{Decision (Accept or Reject), ReasonCode}`,
`Label{PackageNo, LabelRef, TrackingNumber, Carrier}`, `Shipment{TrackingNumbers, ShippedAt}` and
`RejectReason` (`OUT_OF_STOCK`, `UNKNOWN_LISTING`, `CANNOT_MEET_SHIP_BY`, `DISCONTINUED`).

### 3. State Transitions

```mermaid
stateDiagram-v2
    [*] --> ISSUED: IssuePurchaseOrder
    ISSUED --> ACK_PROCESSING: SubmitAcknowledgement
    ACK_PROCESSING --> ACCEPTED: ProcessSubmission, SUCCESS and ACCEPT
    ACK_PROCESSING --> REJECTED: ProcessSubmission, SUCCESS and REJECT
    ACK_PROCESSING --> ISSUED: ProcessSubmission, FAILURE
    ISSUED --> EXPIRED: ExpireUnacknowledgedOrders
    ISSUED --> CANCELLED: CancelPurchaseOrder
    ACCEPTED --> SHIPMENT_PROCESSING: SubmitShipmentConfirmation
    SHIPMENT_PROCESSING --> SHIPPED: ProcessSubmission, SUCCESS
    SHIPMENT_PROCESSING --> ACCEPTED: ProcessSubmission, FAILURE
    REJECTED --> [*]
    EXPIRED --> [*]
    CANCELLED --> [*]
    SHIPPED --> [*]
```

Source: `internal/domain/purchaseorder/purchaseorder.go`, `commands.go`, `commands_ack.go`, `commands_shipment.go` (the `State` constants and the transitions) and `internal/application/usecases/orders.go`, `submissions.go`.

Omitted: `RequestLabel`, which changes no state (it adds `Label`s to an
`ACCEPTED` order). The qualifiers after `ProcessSubmission` (outcome and
decision) say which of the three `ACK_PROCESSING` transitions applies (`ApplyAckOutcome`).

### 4. Enforced Invariants

| Invariant | Enforced by |
| --- | --- |
| An acknowledgement covers every line with one decision (fill-or-kill) | `ErrPartialAcknowledgement` |
| Acknowledged at most once (an acknowledgement still processing counts) | `ErrAlreadyAcknowledged` |
| No acknowledgement after `ackBy` (judged on the receipt instant) | `ErrAckWindowClosed` |
| No label before `ACCEPTED` | `ErrLabelBeforeAcceptance` |
| No shipment confirmation without a label for every package | `ErrConfirmWithoutLabel` |
| Cancel only while `ISSUED` (an `EXPIRED` or `CANCELLED` order is an illegal transition) | `ErrCancelAfterAcknowledgement` |
| Tracking numbers on a shipment confirmation equal the order's labelled ones | `ErrConfirmWithoutLabel` |
| `ackBy = min(issuedAt + ackWindow, requiredShipBy - handlingTime)`, fixed at issue | computed by `purchaseorder.Issue` (`ComputeAckBy`); a business decision, no `Err...` |

### 5. Corrective Policies

| Policy | Rule | Effect on this aggregate |
| --- | --- | --- |
| `IssueOnRelease` | On `FulfillmentReleasedToNode` for a `VENDOR` node, issue a purchase order (`poNumber` idempotent on `assignmentId + releaseNo`) | `IssuePurchaseOrder` |
| `CancelOnSourcingCancel` | On `FulfillmentCancelled`, cancel the `ISSUED` purchase order of the assignment's latest release | `Cancel`; past `ISSUED` it is logged and left alone |
| `ExpireUnacknowledged` | Sweep: `ISSUED` past `ackBy` becomes `EXPIRED` | `ExpireUnacknowledgedOrders` |
| `SimulationKnobs` | `PO_REDELIVERY_RATE` re-delivers purchase orders on poll | None on state; vendors must be idempotent on `poNumber` |
| order-sourcing `ReassignOnRefusal` (neighbour, from the aggregator model) | On `PurchaseOrderRejected` or `PurchaseOrderExpired`, exclude the node and re-queue | A later release may issue a new purchase order to another node |
| order-sourcing `LockOnCommit` (neighbour, from the aggregator model) | On `PurchaseOrderAccepted`, lock the assignment | None here; the accepted order is the path lock |

### 6. Handled Commands

| Command | Issued by | Channel |
| --- | --- | --- |
| `IssuePurchaseOrder` | policy `IssueOnRelease` | Kafka `FulfillmentReleasedToNode` |
| `SubmitAcknowledgement` | vendor | Vendor API `POST /vendor/acknowledgements` |
| `RequestLabel` | vendor | Vendor API `POST /vendor/shipping-labels` |
| `SubmitShipmentConfirmation` | vendor | Vendor API `POST /vendor/shipment-confirmations` |
| `ExpireUnacknowledgedOrders` | policy `ExpireUnacknowledged` (sweep) | internal timer |
| `CancelPurchaseOrder` | policy `CancelOnSourcingCancel` | Kafka `FulfillmentCancelled` |
| `ProcessSubmission` (transitions only) | the submission processor | internal, after `notBefore` |

### 7. Created Events

| Event | Raised when | Full CloudEvents type |
| --- | --- | --- |
| `PurchaseOrderIssued` | `IssuePurchaseOrder` | `com.retailnetwork.partner.vendor-connectivity.purchaseorder.PurchaseOrderIssued` |
| `PurchaseOrderAccepted` | `ACK_PROCESSING` to `ACCEPTED` | `com.retailnetwork.partner.vendor-connectivity.purchaseorder.PurchaseOrderAccepted` |
| `PurchaseOrderRejected` | `ACK_PROCESSING` to `REJECTED` | `com.retailnetwork.partner.vendor-connectivity.purchaseorder.PurchaseOrderRejected` |
| `PurchaseOrderExpired` | `ISSUED` to `EXPIRED` | `com.retailnetwork.partner.vendor-connectivity.purchaseorder.PurchaseOrderExpired` |
| `PurchaseOrderCancelled` | `ISSUED` to `CANCELLED` | `com.retailnetwork.partner.vendor-connectivity.purchaseorder.PurchaseOrderCancelled` |
| `VendorShipmentConfirmed` | `SHIPMENT_PROCESSING` to `SHIPPED` | `com.retailnetwork.partner.vendor-connectivity.purchaseorder.VendorShipmentConfirmed` |

Moves to `ACK_PROCESSING` and `SHIPMENT_PROCESSING`, and a failed
transaction's move back, raise no `PurchaseOrder...` event; the
`Submission` raises `SubmissionProcessed` for the latter.

### 8. Throughput

*Estimate:* medium; polling reads dominate. Writes per order: one issue,
one or more acknowledgement attempts (a failed transaction returns it to
`ISSUED`), one label request per package batch, one or more shipment
confirmation attempts. Concurrency is real: the vendor's write, the expiry
sweep, a cancellation and the submission processor can touch the same
order within seconds, so the row carries a `version` for
optimistic concurrency (`ErrConcurrentUpdate`, re-run once by the use case).

### 9. Size

*Estimate:* one row per release to a vendor node, plus a handful of lines
and one label per package. Lifetime: from issue to `SHIPPED` (hours to
days) or to a terminal refusal. Events per instance: 2 to 3 (issued, then
accepted and shipped, or rejected, expired or cancelled).

## Submission

### 1. Name

`Submission` (`submission.Submission`), identified by `transactionId`.

### 2. Description

One asynchronous vendor write (acknowledgement, inventory feed, node
registration, profile declaration, shipment confirmation, inbound receipt) and its
processing outcome. Value objects: `Kind` (`ACKNOWLEDGEMENT`, `INVENTORY`,
`NODE_REGISTRATION`, `NODE_PROFILE`, `SHIPMENT_CONFIRMATION`, `INBOUND_RECEIPT`) and
`Error{Code, Message, Path}`. It is what
`GET /vendor/transactions/{transactionId}` returns.

### 3. State Transitions

```mermaid
stateDiagram-v2
    [*] --> PROCESSING: ReceiveSubmission
    PROCESSING --> SUCCESS: ProcessSubmission
    PROCESSING --> FAILURE: ProcessSubmission
    SUCCESS --> [*]
    FAILURE --> [*]
```

Source: `internal/domain/submission/submission.go` (`Receive`, `Complete`) and `internal/application/usecases/submissions.go`.

Omitted: nothing. A retry by the vendor is a new submission with a new
`transactionId` (unless it reuses the `Idempotency-Key`, which returns the
first one).

### 4. Enforced Invariants

| Invariant | Enforced by |
| --- | --- |
| The outcome is set once | `ErrSubmissionAlreadyProcessed` |
| Processing never starts before `notBefore = receivedAt + TX_PROCESSING_DELAY` | `ErrSubmissionNotDue` |

### 5. Corrective Policies

- `SimulationKnobs`: `TX_PROCESSING_DELAY` sets `notBefore`;
  `TX_FAILURE_RATE` makes a share of processed submissions end in
  `FAILURE`.
- A downstream refusal (node-topology, offer-availability) ends the
  submission in `FAILURE` with the owner's `Err...` code in
  `SubmissionError.code`. A downstream outage leaves it `PROCESSING` for the
  next processor pass, up to 5 passes (`DefaultSubmissionMaxAttempts`), after which it ends in
  `FAILURE` with `ErrUpstreamUnavailable`.

### 6. Handled Commands

| Command | Issued by | Channel |
| --- | --- | --- |
| `ReceiveSubmission` | every asynchronous vendor write | Vendor API `POST /vendor/acknowledgements`, `POST /vendor/inventory`, `POST /vendor/nodes`, `PUT /vendor/nodes/{nodeId}/profile`, `POST /vendor/shipment-confirmations`, `POST /vendor/inbound-receipts` |
| `ProcessSubmission` | the submission processor | internal, after `notBefore` |

### 7. Created Events

| Event | Raised when | Full CloudEvents type |
| --- | --- | --- |
| `SubmissionProcessed` | `PROCESSING` to `SUCCESS` or `FAILURE` | `com.retailnetwork.partner.vendor-connectivity.submission.SubmissionProcessed` |

### 8. Throughput

*Estimate:* medium-high. One submission per vendor write; inventory feeds
per node every few minutes dominate.

### 9. Size

*Estimate:* retained 30 days. A submission is small: kind, status, the vendor's body verbatim (at most 4 MiB), the order or node it concerns and zero or more errors (at most 50). One event in its life.

## Read models

| Read model | Source events | Used for | Why it is not an aggregate |
| --- | --- | --- | --- |
| `VendorNodeView` (`vendor_nodes`) | node-topology `NodeRegistered`, `NodeOperationalProfileDeclared`, `NodeActivated`, `NodeSuspended`, `NodeRetired` | Which vendor owns a node (`ErrNodeNotOwnedByVendor`), whether a release targets a `VENDOR` node (`IssueOnRelease`), `GET /vendor/nodes/{nodeId}` | node-topology owns the node; this is a local, event-fed copy with no rules of its own |
| `ListingView` (`listings`) | offer-availability `ListingCreated` | Refusing an inventory line whose listing belongs to another node, and resolving a PO line's catalogue item for `packaging[]` | offer-availability owns the listing; this context only looks it up |

## Inbound receipts

> **Built**: [ADR 0005](/docs/adr/0005-inbound-shipments-receipts-and-catalog-packaging) of this repository realises the aggregator's seller-inbound design (inbound shipments, inbound receipts and catalogue packaging).

**Submission** has the sixth kind `INBOUND_RECEIPT` and the command `SubmitInboundReceipt` (REST `POST /vendor/inbound-receipts`, `202` and a `transactionId`, like every vendor write). An `INBOUND_RECEIPT` names a shipment in `InboundShipmentView` addressed to a node of the submitting vendor: an unknown vendor, shipment or node is `ErrNotFound` (404 `not-found`), `ErrNodeNotOwnedByVendor` applies to the node, and a receipt that disagrees with the shipment (wrong destination node, a carton not on the manifest unless it is `UNEXPECTED`, an item not in the carton) is `validation-failed`, while a shipment that has not shipped is `illegal-transition` (`Shipment.CheckReceipt`). New event `InboundReceiptSubmitted`, appended to the outbox in the same unit of work that accepts the submission, so a `202` always has its event (ADR 0005). This context does not apply the receipt to the shipment: `seller-inbound` owns that.

Further read models: `InboundShipmentView` (`inbound_shipments`) (from `InboundShipmentLabelled`, `InboundShipmentShipped`, `InboundShipmentReceived`; the poll returns only `SHIPPED` shipments of the vendor's nodes) and `CatalogPackagingView` (`catalog_packaging`, from `CatalogItemPackagingDeclared`, `CatalogItemPackLevelRetired`), exposed on each purchase order line as `packaging[]`.
