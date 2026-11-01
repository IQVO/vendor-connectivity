---
id: use-cases
title: Use cases
sidebar_label: Use cases
sidebar_position: 2
description: "Every application use case of Vendor Connectivity: trigger, inputs, invariants and the events it raises, read from internal/application/usecases."
---

# Use cases

One method on `usecases.Service` per command, query and consumer policy
(`internal/application/usecases`). Every command runs in **one unit of work** that saves
the aggregates and appends their events to the transactional outbox
(`Service.atomically` re-runs the unit once on a lost optimistic-concurrency race). Every
consumer policy additionally claims the CloudEvents `id` in `processed_events` inside the
same unit (`Service.consume`), so a duplicate delivery is skipped and a rollback un-claims
the id. The runtime view of the main flows is on [Sequence diagrams](/docs/ddd/sequence-diagrams).

## Conventions

- **Identity without authentication.** Nothing authenticates a caller. The vendor names
  itself: `vendorId` is in every write body and a required query parameter on every read
  (`apis/openapi.yaml`). Ownership is a consistency check, not security: a node or order of
  another vendor is `404 node-not-owned-by-vendor`, so a vendor cannot probe another's
  data.
- **Errors** are RFC 9457 `application/problem+json`; the slug table is in
  `internal/adapters/inbound/http/errors.go` and summarised on the
  [API overview](/docs/api-reference).
- **Idempotency-Key.** Every state-changing `POST`/`PUT` carries one (middleware, 24 h
  replay); see [Troubleshooting](/docs/operations/troubleshooting) for its failure modes.
- **Rate limit.** Every `/vendor/*` route may answer `429 rate-limited` before the use case
  runs (`RATE_LIMIT_RPS`).
- **Synchronous refusal versus failed transaction.** A rule this context can check at
  receipt is refused synchronously and creates no transaction. A refusal by a neighbour
  during processing ends the transaction in `FAILURE` with the neighbour's `Err...` code in
  the submission error (`classify` in `submissions.go`). A neighbour outage leaves the
  submission `PROCESSING` for up to 5 passes (`DefaultSubmissionMaxAttempts`), then fails it
  with `ErrUpstreamUnavailable`.
- **Active vendor.** Every vendor write calls `requireActive` first: an unknown vendor is
  `404 not-found`, a suspended one `409 vendor-suspended`. Reads only need the vendor to
  exist (`requireVendor`).

## Vendor account

Source: `internal/application/usecases/vendors.go`, aggregate `internal/domain/vendor`.

| Use case | Trigger | Inputs | Invariants | Events |
| --- | --- | --- | --- | --- |
| `OnboardVendor` | `POST /vendors` | `vendorId`, `name`, `ackWindowMinutes` | The window is 60 to 1440 minutes (`ErrAckWindowOutOfRange`). Onboarding an existing `vendorId` returns the account unchanged with `200` instead of `201`. | `VendorOnboarded` |
| `SuspendVendor` | `POST /vendors/{vendorId}:suspend` | optional `reason` | ACTIVE moves to SUSPENDED; suspending a suspended vendor is a no-op that raises nothing. | `VendorSuspended` |
| `ReinstateVendor` | `POST /vendors/{vendorId}:reinstate` | none | SUSPENDED moves to ACTIVE; no-op when already active. | `VendorReinstated` |
| `GetVendor` | `GET /vendors/{vendorId}` | `vendorId` | Allowed while suspended. | none |

A suspended vendor's writes are refused with `ErrVendorSuspended`; its reads stay open.

## Purchase orders (consumers and sweeper)

Source: `usecases/orders.go`, aggregate `internal/domain/purchaseorder`.

| Use case | Trigger | Inputs | Invariants | Events |
| --- | --- | --- | --- | --- |
| `IssueOnRelease` (command `IssuePurchaseOrder`) | Kafka `FulfillmentReleasedToNode` on `retailnetwork.order-sourcing.events` | `assignmentId`, `releaseNo`, `orderId`, `groupNo`, `nodeId`, `requiredShipBy`, `tier`, `lines` (`listingId`, `quantity`) | The node must be in the local node view (`ports.ErrNodeNotKnown` is a **retryable** error, so a release that races its `NodeRegistered` is retried and then dead-lettered, never lost). A node that is not vendor-operated is ignored. `poNumber = "PO-" + first 16 hex of sha256(assignmentId:releaseNo)`, so a second delivery finds the existing order and stops. A node whose vendor has no account, or a release that fails validation, is logged (`release for a node whose vendor has no account; dropped`, `release is not a valid purchase order; dropped`) and dropped. `ackBy = min(issuedAt + vendor ackWindow, requiredShipBy - node handlingTime)`. Lines are numbered 1..n; at most 500 lines. | `PurchaseOrderIssued` |
| `CancelOnSourcingCancel` | Kafka `FulfillmentCancelled` | `assignmentId`, `reason` | Cancels the latest order of the assignment only while it is `ISSUED`. Past acknowledgement (`ErrCancelAfterAcknowledgement`), `EXPIRED` or `CANCELLED` orders stay as they are and the refusal is logged at INFO (`purchase order not cancelled`). An unknown assignment is a no-op. The reason is trimmed, control characters replaced, capped at 200 characters, default `cancelled by order-sourcing`. | `PurchaseOrderCancelled` (analytics topic only) |
| `ExpireUnacknowledged` | the expiry sweeper, every `EXPIRY_SWEEP_INTERVAL` (30 s) | none | Every `ISSUED` order with `ackBy` in the past (batch of 100) becomes `EXPIRED`, one unit per order. Orders acknowledged meanwhile, lost races and missing rows are skipped with an INFO line. | `PurchaseOrderExpired` |

## Purchase orders (vendor commands and reads)

| Use case | Trigger | Inputs | Invariants | Events |
| --- | --- | --- | --- | --- |
| `SubmitAcknowledgement` | `POST /vendor/acknowledgements` (202) | `vendorId`, `poNumber`, `decision` (`ACCEPT` or `REJECT`), `reasonCode`, `lines` (`lineNo`, `quantity`) | Order owned by the vendor; state `ISSUED`; `now` not after `ackBy` (`ErrAckWindowClosed`, also for `EXPIRED`); a second one is `ErrAlreadyAcknowledged`; every line exactly once at its ordered quantity (`ErrPartialAcknowledgement`); `ACCEPT` carries no `reasonCode`, `REJECT` needs one of `OUT_OF_STOCK`, `UNKNOWN_LISTING`, `CANNOT_MEET_SHIP_BY`, `DISCONTINUED`. Moves the order to `ACK_PROCESSING` and receives an `ACKNOWLEDGEMENT` submission in the same unit. | none at receipt |
| `RequestLabel` | `POST /vendor/shipping-labels` (200, synchronous) | `vendorId`, `poNumber`, `packages[].packageNo` | Order `ACCEPTED` (`ErrLabelBeforeAcceptance` earlier). 1 to 100 distinct package numbers from 1 to 999999. Labels already held are returned from storage; the missing ones are issued by shipping-and-tracking with the deterministic key `vc-label-<poNumber>-<packageNo>` and stored even if a later package fails. Re-requesting only labelled packages is allowed after shipment. | none |
| `SubmitShipmentConfirmation` | `POST /vendor/shipment-confirmations` (202) | `vendorId`, `poNumber`, `trackingNumbers`, `shippedAt` | Order `ACCEPTED`; the tracking numbers are exactly the order's labelled ones (`ErrConfirmWithoutLabel`, also for an order not yet accepted). Moves to `SHIPMENT_PROCESSING` and receives a `SHIPMENT_CONFIRMATION` submission. | none at receipt |
| `PollPurchaseOrders` | `GET /vendor/purchase-orders` | `vendorId`, optional `nodeId`, `createdAfter`, `nextToken`, `limit` (default 50, max 200) | Oldest first, keyset-paged by opaque `nextToken`. With `PO_REDELIVERY_RATE` a page may repeat its first order. Lines carry `packaging[]` from the catalogue view. | none |
| `GetPurchaseOrder` | `GET /vendor/purchase-orders/{poNumber}` | `vendorId`, `poNumber` | The acknowledgement decision is visible only once its transaction succeeded. | none |

## Submissions (asynchronous writes)

Source: `usecases/submissions.go`, aggregate `internal/domain/submission`.

| Use case | Trigger | Inputs | Invariants | Events |
| --- | --- | --- | --- | --- |
| `RegisterVendorNode` | `POST /vendor/nodes` (202) | `vendorId`, `nodeType`, `subtype`, `vendorNodeCode`, `regionId`, `timezone`, `postalCode`, `countryCode`, optional `lat`/`lon` | A vendor may register only `FULFILLMENT_CENTER`, `INBOUND_CROSS_DOCK` or `RETURNS_CENTER`. Receives a `NODE_REGISTRATION` submission. | none at receipt |
| `DeclareNodeProfile` | `PUT /vendor/nodes/{nodeId}/profile` (202) | the operational profile (revision, hours, handling time, cutoffs, throughput cap) | The node must be owned by the vendor. Receives a `NODE_PROFILE` submission. | none at receipt |
| `SubmitInventory` | `POST /vendor/inventory` (202) | `vendorId`, `nodeId`, `fullUpdate`, `asOf`, `lines` | Node owned by the vendor; a line whose listing the view knows to belong to another node is refused; unknown listings pass. Receives an `INVENTORY` submission. | none at receipt |
| `SubmitInboundReceipt` | `POST /vendor/inbound-receipts` (202) | `vendorId`, `shipmentId`, `nodeId`, `receivedAt`, `cartons[]` (`sscc`, `status`, `items`, `complianceCodes`) | Normalised and judged against the local shipment view (`CheckReceipt`); node owned by the vendor. Carton status is `RECEIVED`, `MISSING`, `UNEXPECTED` or `DAMAGED`. Saves an `INBOUND_RECEIPT` submission and raises the event in one unit. | `InboundReceiptSubmitted` (subject: the `shipmentId`) |
| `ProcessDueSubmissions` / `ProcessSubmission` | the submission processor, every `SUBMISSION_POLL_INTERVAL` (1 s) | none (batch of 100 due submissions) | Skips not-due (`notBefore = receivedAt + TX_PROCESSING_DELAY`), already processed and lost-race submissions. Executes the write: acknowledgements, shipment confirmations and inbound receipts succeed at once; registrations call node-topology `POST /nodes`, profiles call `PUT /nodes/{nodeId}/operational-profile`, inventory calls offer-availability `POST /inventory-feeds`. With probability `TX_FAILURE_RATE` the submission fails with `ErrSimulatedFailure`. On the outcome it applies the result to the order (`ApplyAckOutcome` or `ApplyShipmentOutcome`; a failed transaction returns the order to `ISSUED` or `ACCEPTED`), upserts a newly registered node into the local view, sets the outcome once, and appends the events in the same unit. | `PurchaseOrderAccepted` or `PurchaseOrderRejected` (acknowledgement success), `VendorShipmentConfirmed` (shipment success), and always `SubmissionProcessed` |
| `PurgeSubmissions` | the retention sweeper, every 24 h | none | Deletes processed submissions older than 30 days (`DefaultSubmissionRetention`); the same pass purges `idempotency_keys` older than 24 h. | none |
| `GetTransaction` | `GET /vendor/transactions/{transactionId}` | `vendorId`, `transactionId` | A transaction of another vendor is `404`. | none |

## Node and inbound reads

| Use case | Trigger | Notes |
| --- | --- | --- |
| `ListVendorNodes`, `GetNode` | `GET /vendor/nodes`, `GET /vendor/nodes/{nodeId}` | From the local node view; ownership checked. |
| `GetScorecard` | `GET /vendor/nodes/{nodeId}/scorecard` | Relayed synchronously from node-performance (`GET /scorecards/{nodeId}`); `503 upstream-unavailable` if it does not answer. |
| `PollInboundShipments` | `GET /vendor/inbound-shipments` | Only `SHIPPED` shipments bound for the vendor's nodes, oldest shipment first, own `nextToken` bound to the vendor and node filter. |
| `GetInboundShipment` | `GET /vendor/inbound-shipments/{shipmentId}` | A shipment that has not shipped or belongs to another vendor's node is `404`. Cartons are grouped under pallets by `parentSscc`. |

## Consumer policies (local read models)

Source: `usecases/queries.go`, `usecases/inbound.go`; each runs inside `Service.consume`.

| Use case | Event consumed | Effect |
| --- | --- | --- |
| `OnNodeRegistered` | node-topology `NodeRegistered` | Upserts the node in `vendor_nodes`. |
| `OnNodeProfileDeclared` | `NodeOperationalProfileDeclared` | Stores `handlingTimeMinutes` and `profileRevision` (feeds `ackBy`). |
| `OnNodeStatus` | `NodeActivated`, `NodeSuspended`, `NodeRetired` | Sets the node status. |
| `OnListingCreated` | offer-availability `ListingCreated` | Upserts the listing in `listings` (listing to node and catalogue item). |
| `OnInboundShipmentLabelled`, `OnInboundShipmentShipped`, `OnInboundShipmentReceived` | seller-inbound shipment events | Advance the shipment view in `inbound_shipments`; status only moves forward (`LABELLED`, `SHIPPED`, `CLOSED`, `CLOSED_WITH_DISCREPANCY`). Invalid facts are logged and ignored. |
| `OnPackagingDeclared`, `OnPackLevelRetired` | offer-availability `CatalogItemPackagingDeclared`, `CatalogItemPackLevelRetired` | Maintain `catalog_packaging` (levels `EACH`, `INNER`, `CASE`, `PALLET`; a retired GTIN stays as history). An invalid GTIN is logged and ignored. |
| `IssueOnRelease`, `CancelOnSourcingCancel` | order-sourcing events | See the purchase order table above. |

The exact event types and topics are on [Integration](/docs/ecosystem/integration) and the
[event catalogue](/docs/api-reference/events).
