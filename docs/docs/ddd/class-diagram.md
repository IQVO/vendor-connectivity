---
id: class-diagram
title: Class Diagram
sidebar_label: Class Diagram
sidebar_position: 7
description: "UML class diagrams of Vendor Connectivity's aggregates, value objects, events, policies and read models, and its hexagonal ports-and-adapters layout, as declared in internal/."
---

# Class Diagram

UML class diagrams of the Go packages `internal/domain/vendor`, `internal/domain/purchaseorder` and
`internal/domain/submission`, with the ports of `internal/application/ports`. Fields are shown `-`
(unexported), methods `+`; names are the Go identifiers. Aggregates are rebuilt with `Restore(Snapshot)`,
which validates every invariant, and changed through methods that return the domain events they raise.

## 1. VendorAccount

```mermaid
classDiagram
    direction TB
    class Account {
        <<AggregateRoot>>
        -id ID
        -name string
        -ackWindowMins int
        -state State
        -onboardedAt time.Time
        -suspendedAt time.Time
        -reason string
        -version int64
        +Onboard(NewAccount, now)$ Account, events
        +Restore(Snapshot)$ Account
        +Suspend(reason, now) events
        +Reinstate(now) events
        +EnsureActive() error
        +AckWindow() time.Duration
        +AckWindowMinutes() int
        +Snapshot() Snapshot
    }
    class ID {
        <<ValueObject>>
        string
    }
    class State {
        <<Enumeration>>
        ACTIVE
        SUSPENDED
    }
    class VendorRepository {
        <<Port>>
        +Get(ctx, id) Account
        +Save(ctx, account) error
    }
    Account --> ID : identity
    Account --> State
    VendorRepository ..> Account : persists
```

Source: `internal/domain/vendor/vendor.go` and `internal/application/ports/ports.go` (`VendorRepository`).

Omitted: `ErrAckWindowOutOfRange` (from `Onboard`) and `ErrVendorSuspended` (from `EnsureActive`);
`ErrNodeNotOwnedByVendor`, which the use cases check against the node view. The rate limit is not on the
account: it is one process-wide bucket in the HTTP adapter.

## 2. PurchaseOrder

```mermaid
classDiagram
    direction TB
    class PurchaseOrder {
        <<AggregateRoot>>
        -number Number
        -vendorID string
        -nodeID string
        -assignmentID string
        -orderID string
        -groupNo int
        -tier Tier
        -releaseNo int
        -issuedAt time.Time
        -requiredShipBy time.Time
        -ackBy time.Time
        -lines Line[]
        -state State
        -ack Ack nullable
        -labels Label[]
        -shipment Shipment nullable
        -cancelReason string
        -cancelledAt time.Time
        -version int64
        +Issue(NewOrder, now)$ PurchaseOrder, events
        +SubmitAcknowledgement(AckInput, now) events
        +ApplyAckOutcome(success, now) events
        +LabelPlan(packageNos) have, missing
        +AddLabels(labels) error
        +SubmitShipmentConfirmation(ShipInput) events
        +ApplyShipmentOutcome(success, now) events
        +Expire(now) events
        +Cancel(reason, now) events
    }
    class Line {
        <<ValueObject>>
        LineNo int
        ListingID string
        Quantity int
    }
    class Ack {
        <<ValueObject>>
        Decision Decision
        ReasonCode RejectReason
        TransactionID string
    }
    class Label {
        <<ValueObject>>
        PackageNo int
        LabelRef string
        TrackingNumber string
        Carrier string
    }
    class Shipment {
        <<ValueObject>>
        TransactionID string
        ShippedAt time.Time
        TrackingNumbers string[]
    }
    class RejectReason {
        <<Enumeration>>
        OUT_OF_STOCK
        UNKNOWN_LISTING
        CANNOT_MEET_SHIP_BY
        DISCONTINUED
    }
    class State {
        <<Enumeration>>
        ISSUED
        ACK_PROCESSING
        ACCEPTED
        REJECTED
        EXPIRED
        SHIPMENT_PROCESSING
        SHIPPED
        CANCELLED
    }
    class PurchaseOrderRepository {
        <<Port>>
        +Get(ctx, number) PurchaseOrder
        +FindByRelease(ctx, assignmentID, releaseNo) PurchaseOrder
        +FindLatestByAssignment(ctx, assignmentID) PurchaseOrder
        +List(ctx, filter) PurchaseOrder[]
        +ListExpirable(ctx, now, limit) PurchaseOrder[]
        +Save(ctx, po) error
    }
    PurchaseOrder "1" *-- "1..500" Line : lines
    PurchaseOrder "1" *-- "0..1" Ack : ack
    PurchaseOrder "1" *-- "0..100" Label : labels
    PurchaseOrder "1" *-- "0..1" Shipment : shipment
    PurchaseOrder --> State
    Ack --> RejectReason
    PurchaseOrderRepository ..> PurchaseOrder : persists
```

Source: `internal/domain/purchaseorder/purchaseorder.go`, `commands.go`, `commands_ack.go`, `commands_labels.go`, `commands_shipment.go` and `internal/application/ports/ports.go` (`PurchaseOrderRepository`).

Omitted: the errors (`ErrPartialAcknowledgement`, `ErrAlreadyAcknowledged`, `ErrAckWindowClosed`,
`ErrLabelBeforeAcceptance`, `ErrConfirmWithoutLabel`, `ErrCancelAfterAcknowledgement`); the `Tier`
enumeration (`SAME_DAY`, `NEXT_DAY`, `TWO_DAY`, `STANDARD`), the `Decision` enumeration (`ACCEPT`,
`REJECT`), `Number` and the read accessors. `ApplyAckOutcome` and `ApplyShipmentOutcome` are the
`ProcessSubmission` transitions split by kind; `LabelPlan` splits the requested packages into the labels
already held and those still to issue, and `AddLabels` stores what shipping-and-tracking returned.
`Number` is derived by `NumberFor(assignmentID, releaseNo)`.

## 3. Submission

```mermaid
classDiagram
    direction TB
    class Submission {
        <<AggregateRoot>>
        -id ID
        -vendorID string
        -kind Kind
        -status Status
        -payload bytes
        -poNumber string
        -nodeID string
        -resultNodeID string
        -receivedAt time.Time
        -notBefore time.Time
        -processedAt time.Time
        -errs Error[]
        -attempts int
        -version int64
        +Receive(NewSubmission, now, delay)$ Submission
        +Restore(Snapshot)$ Submission
        +Due(now) bool
        +RecordAttempt() int
        +Complete(Outcome, now) events
    }
    class Kind {
        <<Enumeration>>
        ACKNOWLEDGEMENT
        INVENTORY
        NODE_REGISTRATION
        NODE_PROFILE
        SHIPMENT_CONFIRMATION
        INBOUND_RECEIPT
    }
    class Status {
        <<Enumeration>>
        PROCESSING
        SUCCESS
        FAILURE
    }
    class Error {
        <<ValueObject>>
        Code string
        Message string
        Path string
    }
    class SubmissionRepository {
        <<Port>>
        +Get(ctx, id) Submission
        +ListDue(ctx, now, limit) Submission[]
        +Save(ctx, s) error
        +DeleteProcessedBefore(ctx, t) int
    }
    Submission --> Kind
    Submission --> Status
    Submission "1" *-- "0..50" Error : errors
    SubmissionRepository ..> Submission : persists
```

Source: `internal/domain/submission/submission.go` and `internal/application/ports/ports.go` (`SubmissionRepository`).

Omitted: the errors `ErrSubmissionAlreadyProcessed` (from `Complete`) and `ErrSubmissionNotDue`
(`Due` is false before `notBefore`). `poNumber` and `nodeID` are by-value links, not references. The
payload is the vendor's request body verbatim (at most 4 MiB).

## 4. Domain events

Purchase-order acknowledgement events:

```mermaid
classDiagram
    direction TB
    class PurchaseOrderIssued {
        <<DomainEvent>>
        PONumber
        VendorID
        NodeID
        AssignmentID
        ReleaseNo
        RequiredShipBy
        AckBy
        Lines
    }
    class PurchaseOrderAccepted {
        <<DomainEvent>>
        PONumber
        AssignmentID
        ReleaseNo
        NodeID
        AcceptedAt
        TransactionID
    }
    class PurchaseOrderRejected {
        <<DomainEvent>>
        PONumber
        AssignmentID
        ReleaseNo
        NodeID
        ReasonCode
        RejectedAt
    }
    class PurchaseOrderExpired {
        <<DomainEvent>>
        PONumber
        AssignmentID
        ReleaseNo
        NodeID
        AckBy
    }
```

Source: `internal/domain/purchaseorder/events.go`.

Account, cancellation, shipment, submission and inbound receipt events:

```mermaid
classDiagram
    direction TB
    class VendorOnboarded {
        <<DomainEvent>>
        VendorID
        Name
        AckWindowMinutes
    }
    class VendorSuspended {
        <<DomainEvent>>
        VendorID
        Reason
    }
    class VendorReinstated {
        <<DomainEvent>>
        VendorID
    }
    class PurchaseOrderCancelled {
        <<DomainEvent>>
        PONumber
        AssignmentID
        Reason
    }
    class VendorShipmentConfirmed {
        <<DomainEvent>>
        PONumber
        AssignmentID
        NodeID
        TrackingNumbers
        ShippedAt
        RequiredShipBy
    }
    class SubmissionProcessed {
        <<DomainEvent>>
        TransactionID
        VendorID
        Kind
        Status
        ErrorCount
    }
    class ReceiptSubmitted {
        <<DomainEvent>>
        TransactionID
        VendorID
        NodeID
        ShipmentID
        ReceivedAt
        Cartons
    }
```

Source: `internal/domain/vendor/events.go`, `internal/domain/purchaseorder/events.go`, `internal/domain/submission/events.go` and `internal/domain/inbound/receipt.go` (`ReceiptSubmitted` is published as `InboundReceiptSubmitted`).

Omitted: the `shared.Header` every event embeds (aggregate version and occurrence time) and the
CloudEvents envelope; the payload field names are on [Domain events](/docs/ddd/domain-events).

## 5. Use cases and read models

```mermaid
classDiagram
    direction LR
    class Service {
        <<ApplicationService>>
        +OnboardVendor()
        +SuspendVendor()
        +ReinstateVendor()
        +IssueOnRelease()
        +CancelOnSourcingCancel()
        +ExpireUnacknowledged()
        +SubmitAcknowledgement()
        +RequestLabel()
        +SubmitShipmentConfirmation()
        +SubmitInboundReceipt()
        +ProcessDueSubmissions()
        +PurgeSubmissions()
    }
    class Knobs {
        <<Config>>
        TxProcessingDelay
        TxFailureRate
        PORedeliveryRate
    }
    class Node {
        <<ReadModel>>
        ID
        Operator
        VendorID
        VendorNodeCode
        NodeType
        Status
        HandlingTimeMinutes
        ProfileRevision
    }
    class Listing {
        <<ReadModel>>
        ID
        NodeID
        CatalogItemID
    }
    class Shipment_inbound {
        <<ReadModel>>
        ID
        SellerID
        NodeID
        Status
        Cartons
    }
    class PackLevel {
        <<ReadModel>>
        CatalogItemID
        Level
        GTIN
        UnitsPerPack
        Active
    }
    Service ..> PurchaseOrder : issue, cancel, expire
    Service ..> Submission : receive, process
    Service ..> Node : reads and feeds
    Service ..> Listing : reads and feeds
    Service ..> Shipment_inbound : reads and feeds
    Service ..> PackLevel : reads and feeds
    Knobs ..> Submission : notBefore and failures
    class PurchaseOrder {
        <<AggregateRoot>>
    }
    class Submission {
        <<AggregateRoot>>
    }
```

Source: `internal/application/usecases/service.go` (`Service`, `Knobs`, `Deps`), `internal/domain/network/network.go` (`Node`, `Listing`) and `internal/domain/inbound` (`Shipment`, `PackLevel`).

Omitted: the consumer policies of the read models (`OnNodeRegistered`, `OnListingCreated`, ...) and the
query methods; `RATE_LIMIT_RPS` acts in the HTTP adapter, not on an aggregate. `<<ReadModel>>` is an
extra stereotype for the four local views; the inbound shipment type is named `Shipment` in its package
and renamed here to avoid a clash with the purchase order's `Shipment` value object.

## 6. Hexagonal ports and adapters

```mermaid
%%{init: {"flowchart": {"nodeSpacing": 14, "rankSpacing": 36, "padding": 6}}}%%
flowchart TB
    IN["<b>internal/adapters/inbound</b><br/>http: Vendor API /vendor/..., /vendors, rate limiter, Idempotency-Key<br/>kafka: 5 consumers (node, listing, sourcing, inbound, packaging)<br/>mcp, a2a: read-only agents<br/>cmd/api loops: submission processor, expiry sweeper, retention"]
    APP["<b>internal/application/usecases</b><br/>OnboardVendor, SuspendVendor, ReinstateVendor<br/>IssueOnRelease, CancelOnSourcingCancel, ExpireUnacknowledged<br/>SubmitAcknowledgement, RequestLabel, SubmitShipmentConfirmation<br/>SubmitInventory, RegisterVendorNode, DeclareNodeProfile, SubmitInboundReceipt<br/>ProcessDueSubmissions, PurgeSubmissions, polls and gets"]
    DOM["<b>internal/domain</b><br/>vendor.Account<br/>purchaseorder.PurchaseOrder<br/>submission.Submission<br/>network, inbound"]
    PORTS["<b>internal/application/ports</b><br/>VendorRepository, PurchaseOrderRepository, SubmissionRepository<br/>NodeViewRepository, ListingViewRepository,<br/>InboundShipmentViewRepository, CatalogPackagingViewRepository<br/>NodeTopology, OfferAvailability, ShippingTracking, NodePerformance<br/>EventOutbox, ProcessedEvents, UnitOfWork, Clock, IDGenerator, Random"]
    PG["<b>internal/adapters/outbound/postgres</b><br/>repositories, view tables,<br/>outbox_events, processed_events, idempotency_keys"]
    KO["<b>internal/adapters/outbound/outbox + kafka</b><br/>relay to retailnetwork.vendor-connectivity.events<br/>and .analytics"]
    HC["<b>internal/adapters/outbound/httpclient</b><br/>node-topology, offer-availability,<br/>shipping-and-tracking, node-performance"]
    IN --> APP
    APP --> DOM
    APP --> PORTS
    PORTS --- PG
    PORTS --- HC
    PG --> KO
```

Source: `cmd/api/wire_adapters.go` and `background.go` (wiring), `internal/application/ports/ports.go` (the ports) and `internal/architecture/architecture_test.go` (the dependency rule).

Omitted: the in-memory adapters (test doubles, never imported by `cmd/**`), `bootretry`, the CloudEvents
package and telemetry.
