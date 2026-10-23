# Domain model: ubiquitous language, aggregates, events, use cases

Source of truth: retail-network-docs `model/model.yaml` (context `vendor-connectivity`) plus
the orchestrator's `contracts-delta`. Invariants, transitions and every assumption the model
left open are in `docs/adr/0002-aggregates-and-invariants.md` (assumptions A1-A27).

## Ubiquitous Language (use these exact names)

- **Vendor / VendorAccount** — a company that operates fulfillment nodes; identified by
  `vendorId`. Carries the `ackWindowMinutes` (60..1440) its purchase orders must be
  acknowledged within. `ACTIVE` or `SUSPENDED`.
- **Purchase order (PO)** — the vendor-facing form of one release of an order-sourcing
  assignment to a vendor node. Identified by `poNumber` (`PO-` + a hash of
  `assignmentId:releaseNo`, A1). The Vendor API never shows `orderId`, ship-to or promise.
- **ackBy** — `min(issuedAt + ackWindow, requiredShipBy - handlingTime)`; the last instant a
  full acknowledgement is accepted.
- **Acknowledgement** — one `ACCEPT` or `REJECT` decision that covers every line at its ordered
  quantity (fill-or-kill). `REJECT` carries a reason code.
- **Submission / transaction** — the asynchronous record of one vendor write (`transactionId`),
  `PROCESSING` then `SUCCESS` or `FAILURE`. Every write except a label returns `202` + the id.
- **Label** — a per-package shipping label obtained synchronously from shipping-and-tracking.
- **VendorNodeView / ListingView** — local read models fed by node-topology and
  offer-availability events (never read live).
- **Published Language** — the Vendor API (`apis/openapi.yaml`) as an Open Host Service.

## Aggregates

- **VendorAccount** (`internal/domain/vendor`): `ACTIVE` <-> `SUSPENDED`; window 60..1440
  minutes; a suspended vendor's writes are refused (G30).
- **PurchaseOrder** (`internal/domain/purchaseorder`, the mutation focus): `ISSUED`,
  `ACK_PROCESSING`, `ACCEPTED`, `REJECTED`, `EXPIRED`, `SHIPMENT_PROCESSING`, `SHIPPED`,
  `CANCELLED`; acknowledged once, in full, before `ackBy`; no label before acceptance; no
  shipment confirmation without a label for every package; cancel only while `ISSUED`.
- **Submission** (`internal/domain/submission`): `PROCESSING` -> `SUCCESS | FAILURE`, outcome set
  once, never processed before `notBefore`.
- `internal/domain/network`: the `Node` and `Listing` value types of the read models and the
  ownership rule (`ErrNodeNotOwnedByVendor`). `internal/domain/shared`: `Event`, `Header`,
  `ErrValidation`, `ErrIllegalTransition`, `ErrInvalidSnapshot`.

Every aggregate has `Snapshot()`, a validating `Restore(Snapshot)` and an optimistic
`Version()`; `now` and ids are passed in. `ErrNotFound` and `ErrConcurrentUpdate` are port
errors (`internal/application/ports/errors.go`). The domain imports nothing outside
`internal/domain`.

## Domain events

All ten are integration events on `retailnetwork.vendor-connectivity.events`, except
`PurchaseOrderCancelled` (analytics-only, `.analytics`); wire shape in `apis/asyncapi.yaml`:

- `VendorOnboarded`, `VendorSuspended`, `VendorReinstated` (`internal/domain/vendor/events.go`).
- `PurchaseOrderIssued`, `PurchaseOrderAccepted`, `PurchaseOrderRejected`, `PurchaseOrderExpired`,
  `PurchaseOrderCancelled`, `VendorShipmentConfirmed` (`internal/domain/purchaseorder/events.go`).
- `SubmissionProcessed` (`internal/domain/submission/events.go`).

## Key use cases (`internal/application/usecases`)

One method of `Service` per command and query; commands run in ONE `ports.UnitOfWork` (load,
domain call, save, outbox append).

- Vendor API commands: `OnboardVendor`, `SuspendVendor`, `ReinstateVendor` (`vendors.go`);
  `SubmitAcknowledgement`, `SubmitShipmentConfirmation`, `RequestLabel` (`orders.go`);
  `RegisterVendorNode`, `DeclareNodeProfile`, `SubmitInventory` (`submissions.go`).
- Background: `ProcessSubmission` / `ProcessDueSubmissions`, `PurgeSubmissions`
  (`submissions.go`), `ExpireUnacknowledged` (`orders.go`).
- Consumer policies: `IssueOnRelease`, `CancelOnSourcingCancel` (`orders.go`); `OnNodeRegistered`,
  `OnNodeProfileDeclared`, `OnNodeStatus`, `OnListingCreated` (`queries.go`).
- Queries (`queries.go`): `PollPurchaseOrders`, `GetPurchaseOrder`, `GetTransaction`, `GetNode`,
  `ListVendorNodes`, `GetScorecard` (`GetVendor` is in `vendors.go`). Read DTOs: `internal/application/views`.
- Ports (`internal/application/ports/ports.go`) are interfaces only; neighbour call shapes are
  in `internal/application/gateway`; the memory test doubles in
  `internal/adapters/outbound/memory` must pass the same suite as Postgres
  (`internal/adapters/outbound/repocontract`).
