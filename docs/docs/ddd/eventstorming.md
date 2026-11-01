---
id: eventstorming
title: EventStorming
sidebar_label: EventStorming
sidebar_position: 6
description: "Process-level EventStorming boards for Vendor Connectivity - issue and acknowledge, transaction outcomes, the clock and cancellation, labels and shipment, onboarding - with the hotspots that the code resolved or left open."
---

# EventStorming

Process-level boards in the ddd-crew
[EventStorming glossary & cheat sheet](https://github.com/ddd-crew/eventstorming-glossary-cheat-sheet)
notation. Every sticky is a command, aggregate, event, policy or read model
of the built service. Hotspots are the gaps the aggregator's model raised; the inventory at the end says which the code resolved and which stay open. The boards read top to
bottom (`flowchart TB`) so each stays inside the page width. The
network-wide board
is on [EventStorming big picture](https://iqvo.github.io/warehouse-docs/network/strategic-design/eventstorming-big-picture).

## Legend

| Sticky | Colour | Meaning |
| --- | --- | --- |
| Command | blue `#a5d8ff` | An intent: a vendor write, a policy's action, a sweep |
| Aggregate | yellow `#ffec99` | `VendorAccount`, `PurchaseOrder`, `Submission` |
| Domain Event | orange `#ffc078` | A published fact of this context, or a consumed fact of a neighbour |
| Policy | lilac `#d0bfff` | "Whenever X, then Y" |
| Read Model | green `#b2f2bb` | `VendorNodeView`, `ListingView`, or a query the vendor reads |
| External System | pink `#fcc2d7` | The vendor (the fleet) or a sibling context |
| Hotspot | red `#ff8787` | A gap or open question; marked *resolved* where the code settled it |
| Actor | pale yellow `#fff59d` | A person or a timer |

```mermaid
flowchart LR
    A["Actor"]:::actor
    C["Command"]:::command
    G["Aggregate"]:::aggregate
    E["Domain Event"]:::event
    P["Policy"]:::policy
    R["Read Model"]:::readmodel
    X["External System"]:::external
    H["Hotspot"]:::hotspot
    classDef actor fill:#fff59d,stroke:#b59f00,color:#000
    classDef command fill:#a5d8ff,stroke:#1c7ed6,color:#000
    classDef aggregate fill:#ffec99,stroke:#f08c00,color:#000
    classDef event fill:#ffc078,stroke:#e8590c,color:#000
    classDef policy fill:#d0bfff,stroke:#7048e8,color:#000
    classDef readmodel fill:#b2f2bb,stroke:#2f9e44,color:#000
    classDef external fill:#fcc2d7,stroke:#c2255c,color:#000
    classDef hotspot fill:#ff8787,stroke:#c92a2a,color:#000
```

Source: `internal/application/usecases/service.go` and `internal/domain/*/events.go`.

## 1. Issue and acknowledge

```mermaid
flowchart TB
    OS["order-sourcing"]:::external
    E0["FulfillmentReleasedToNode"]:::event
    P1["Policy: IssueOnRelease<br/>VENDOR node only,<br/>idempotent on<br/>assignmentId + releaseNo"]:::policy
    R1["VendorNodeView"]:::readmodel
    C1["IssuePurchaseOrder<br/>(purchaseorder.Issue)"]:::command
    G1["PurchaseOrder"]:::aggregate
    E1["PurchaseOrderIssued"]:::event
    H1["Hotspot H1, resolved: handlingTime<br/>comes from NodeOperational-<br/>ProfileDeclared"]:::hotspot
    V["Vendor system<br/>network-fulfillment"]:::external
    R2["Purchase orders<br/>poll with nextToken"]:::readmodel
    C2["SubmitAcknowledgement<br/>whole order"]:::command
    C3["Receive submission<br/>ACKNOWLEDGEMENT"]:::command
    G2["Submission<br/>PROCESSING"]:::aggregate
    OS --> E0 --> P1
    R1 --> P1
    P1 --> C1 --> G1 --> E1
    C1 -.- H1
    G1 --> R2 --> V
    V --> C2 --> G1
    C2 --> C3 --> G2
    classDef actor fill:#fff59d,stroke:#b59f00,color:#000
    classDef command fill:#a5d8ff,stroke:#1c7ed6,color:#000
    classDef aggregate fill:#ffec99,stroke:#f08c00,color:#000
    classDef event fill:#ffc078,stroke:#e8590c,color:#000
    classDef policy fill:#d0bfff,stroke:#7048e8,color:#000
    classDef readmodel fill:#b2f2bb,stroke:#2f9e44,color:#000
    classDef external fill:#fcc2d7,stroke:#c2255c,color:#000
    classDef hotspot fill:#ff8787,stroke:#c92a2a,color:#000
```

Source: `internal/application/usecases/orders.go` (`IssueOnRelease`, `SubmitAcknowledgement`) and `internal/domain/purchaseorder/events.go`.

Omitted: the synchronous refusals at receipt (`ErrPartialAcknowledgement`,
`ErrAlreadyAcknowledged`, `ErrAckWindowClosed`, `ErrNodeNotOwnedByVendor`),
shown on [Use Cases](/docs/ddd/use-cases); the
`PO_REDELIVERY_RATE` re-delivery on poll.

## 2. Transaction outcomes

```mermaid
flowchart TB
    T["Submission processor<br/>after notBefore"]:::actor
    P0["Policy: Knobs<br/>TX_PROCESSING_DELAY,<br/>TX_FAILURE_RATE"]:::policy
    C1["ProcessDueSubmissions"]:::command
    G1["Submission"]:::aggregate
    E1["SubmissionProcessed"]:::event
    G2["PurchaseOrder"]:::aggregate
    E2["PurchaseOrderAccepted"]:::event
    E3["PurchaseOrderRejected"]:::event
    E4["VendorShipmentConfirmed"]:::event
    OS["order-sourcing"]:::external
    NP["node-performance"]:::external
    ST["shipping-and-tracking"]:::external
    T --> C1
    P0 --> C1
    C1 --> G1 --> E1 --> NP
    C1 --> G2
    G2 -->|ACCEPT| E2
    G2 -->|REJECT| E3
    G2 -->|shipment| E4
    G2 -.->|FAILURE: back to<br/>ISSUED or ACCEPTED| G2
    E2 --> OS
    E3 --> OS
    E4 --> OS
    E4 --> ST
    classDef actor fill:#fff59d,stroke:#b59f00,color:#000
    classDef command fill:#a5d8ff,stroke:#1c7ed6,color:#000
    classDef aggregate fill:#ffec99,stroke:#f08c00,color:#000
    classDef event fill:#ffc078,stroke:#e8590c,color:#000
    classDef policy fill:#d0bfff,stroke:#7048e8,color:#000
    classDef readmodel fill:#b2f2bb,stroke:#2f9e44,color:#000
    classDef external fill:#fcc2d7,stroke:#c2255c,color:#000
    classDef hotspot fill:#ff8787,stroke:#c92a2a,color:#000
```

Source: `internal/application/usecases/submissions.go` (`ProcessDueSubmissions`, `finish`) and `internal/domain/*/events.go`.

Omitted: node-performance also consumes `PurchaseOrderAccepted`,
`PurchaseOrderRejected` and `VendorShipmentConfirmed`; the relay kinds
(`INVENTORY`, `NODE_REGISTRATION`, `NODE_PROFILE`) are on board 5, and inbound receipts (a sixth kind, ADR 0005) are not drawn here.

## 3. The clock and cancellation

```mermaid
flowchart TB
    T["Expiry sweep timer"]:::actor
    P1["Policy: ExpireUnacknowledged<br/>ISSUED past ackBy"]:::policy
    C1["ExpireUnacknowledged"]:::command
    G1["PurchaseOrder"]:::aggregate
    E1["PurchaseOrderExpired"]:::event
    OS1["order-sourcing<br/>ReassignOnRefusal"]:::external
    OS2["order-sourcing"]:::external
    E0["FulfillmentCancelled"]:::event
    P2["Policy: CancelOnSourcingCancel<br/>only while ISSUED"]:::policy
    C2["Cancel"]:::command
    E2["PurchaseOrderCancelled"]:::event
    A["analytics only"]:::external
    H2["Hotspot H2: cancel after<br/>acknowledgement is refused,<br/>nobody is told"]:::hotspot
    T --> P1 --> C1 --> G1
    G1 --> E1 --> OS1
    OS2 --> E0 --> P2 --> C2 --> G1
    G1 --> E2 --> A
    C2 -.- H2
    classDef actor fill:#fff59d,stroke:#b59f00,color:#000
    classDef command fill:#a5d8ff,stroke:#1c7ed6,color:#000
    classDef aggregate fill:#ffec99,stroke:#f08c00,color:#000
    classDef event fill:#ffc078,stroke:#e8590c,color:#000
    classDef policy fill:#d0bfff,stroke:#7048e8,color:#000
    classDef readmodel fill:#b2f2bb,stroke:#2f9e44,color:#000
    classDef external fill:#fcc2d7,stroke:#c2255c,color:#000
    classDef hotspot fill:#ff8787,stroke:#c92a2a,color:#000
```

Source: `internal/application/usecases/orders.go` (`ExpireUnacknowledged`, `CancelOnSourcingCancel`) and `cmd/api/background.go`.

Omitted: node-performance also consumes `PurchaseOrderExpired`.

## 4. Labels and shipment

```mermaid
flowchart TB
    V["Vendor system<br/>network-fulfillment"]:::external
    C1["RequestLabel"]:::command
    G1["PurchaseOrder<br/>ACCEPTED"]:::aggregate
    ST["shipping-and-tracking<br/>IssueLabel"]:::external
    H3["Hotspot H3: label is<br/>synchronous, every other<br/>write is 202"]:::hotspot
    C2["SubmitShipmentConfirmation<br/>a label per package"]:::command
    C3["Receive submission<br/>SHIPMENT_CONFIRMATION"]:::command
    G2["Submission"]:::aggregate
    C4["ProcessDueSubmissions"]:::command
    E1["VendorShipmentConfirmed"]:::event
    X["order-sourcing,<br/>shipping-and-tracking,<br/>node-performance"]:::external
    V --> C1 --> G1
    C1 --> ST
    C1 -.- H3
    V --> C2 --> G1
    C2 --> C3 --> G2 --> C4 --> G1
    G1 --> E1 --> X
    classDef actor fill:#fff59d,stroke:#b59f00,color:#000
    classDef command fill:#a5d8ff,stroke:#1c7ed6,color:#000
    classDef aggregate fill:#ffec99,stroke:#f08c00,color:#000
    classDef event fill:#ffc078,stroke:#e8590c,color:#000
    classDef policy fill:#d0bfff,stroke:#7048e8,color:#000
    classDef readmodel fill:#b2f2bb,stroke:#2f9e44,color:#000
    classDef external fill:#fcc2d7,stroke:#c2255c,color:#000
    classDef hotspot fill:#ff8787,stroke:#c92a2a,color:#000
```

Source: `internal/application/usecases/orders.go` (`RequestLabel`, `SubmitShipmentConfirmation`) and `internal/domain/purchaseorder/events.go`.

Omitted: `LabelIssued`, which shipping-and-tracking publishes and this
context does not consume.

## 5. Onboarding, nodes and inventory

```mermaid
flowchart TB
    PM["Network partner<br/>manager"]:::actor
    C1["OnboardVendor,<br/>SuspendVendor,<br/>ReinstateVendor"]:::command
    G1["VendorAccount"]:::aggregate
    E1["VendorOnboarded,<br/>VendorSuspended,<br/>VendorReinstated"]:::event
    H4["Hotspot H4, resolved: suspend and<br/>reinstate are REST actions, raise<br/>events, block vendor writes"]:::hotspot
    V["Vendor system<br/>network-fulfillment"]:::external
    C2["Receive submission<br/>NODE_REGISTRATION,<br/>NODE_PROFILE, INVENTORY"]:::command
    G2["Submission"]:::aggregate
    C3["ProcessDueSubmissions"]:::command
    NT["node-topology<br/>RegisterNode,<br/>DeclareOperationalProfile"]:::external
    OA["offer-availability<br/>ApplyInventoryFeed"]:::external
    E2["NodeRegistered, ProfileDeclared,<br/>NodeActivated, NodeSuspended,<br/>NodeRetired"]:::event
    E3["ListingCreated"]:::event
    R1["VendorNodeView"]:::readmodel
    R2["ListingView"]:::readmodel
    H5["Hotspot H5, resolved: the transaction<br/>answer carries the new nodeId"]:::hotspot
    H6["Hotspot H6, open: caller identity<br/>without auth"]:::hotspot
    PM --> C1 --> G1 --> E1
    C1 -.- H4
    V --> C2 --> G2 --> C3
    C3 --> NT
    C3 --> OA
    NT --> E2 --> R1
    OA --> E3 --> R2
    G2 -.- H5
    V -.- H6
    classDef actor fill:#fff59d,stroke:#b59f00,color:#000
    classDef command fill:#a5d8ff,stroke:#1c7ed6,color:#000
    classDef aggregate fill:#ffec99,stroke:#f08c00,color:#000
    classDef event fill:#ffc078,stroke:#e8590c,color:#000
    classDef policy fill:#d0bfff,stroke:#7048e8,color:#000
    classDef readmodel fill:#b2f2bb,stroke:#2f9e44,color:#000
    classDef external fill:#fcc2d7,stroke:#c2255c,color:#000
    classDef hotspot fill:#ff8787,stroke:#c92a2a,color:#000
```

Source: `internal/application/usecases/vendors.go`, `submissions.go` and `internal/domain/vendor/events.go`.

Omitted: the three account events have no consumer yet (`VendorOnboarded` was drawn to node-performance in the aggregator's model; the code only publishes it); `SubmissionProcessed`
goes out for every processed write (board 2).

## Sticky inventory

| Sticky | Kind | Source in the code |
| --- | --- | --- |
| Vendor system (network-fulfillment) | External System | The Vendor API in `internal/adapters/inbound/http`; the fleet is one vendor, `V-WS` |
| order-sourcing, node-topology, offer-availability, shipping-and-tracking, node-performance | External System | `internal/adapters/inbound/kafka/consumers.go` (consumed), `outbound/httpclient/clients.go` (called) and the events' consumers on [Integration](/docs/ecosystem/integration) |
| Network partner manager, expiry sweep timer, submission processor | Actor | `POST /vendors`; `startBackground` in `cmd/api/background.go` (the sweeper and the processor loops) |
| `OnboardVendor`, `SuspendVendor`, `ReinstateVendor`, `IssueOnRelease`, `SubmitAcknowledgement`, `RequestLabel`, `SubmitShipmentConfirmation`, `ExpireUnacknowledged`, `CancelOnSourcingCancel`, `ProcessDueSubmissions` | Command | Methods of `usecases.Service` |
| `VendorAccount`, `PurchaseOrder`, `Submission` | Aggregate | `internal/domain/vendor`, `purchaseorder`, `submission` |
| Published events | Domain Event | `internal/adapters/outbound/kafka/encoder.go`; see [Events](/docs/api-reference/events) |
| Consumed events | Domain Event | `internal/adapters/inbound/kafka/consumers.go`, `inbound_consumers.go` |
| `IssueOnRelease`, `CancelOnSourcingCancel`, `ExpireUnacknowledged`, `Knobs` | Policy | `usecases.Service` and `usecases.Knobs` |
| `VendorNodeView`, `ListingView`, purchase-order poll | Read Model | `network.Node`, `network.Listing`; `GET /vendor/purchase-orders` |
| H1: `ackBy` needs `handlingTime` | Hotspot, resolved | `NodeOperationalProfileDeclared` is consumed and stored as `handling_time_minutes` on `vendor_nodes`; `purchaseorder.ComputeAckBy` uses it |
| H2: cancel after acknowledgement is refused | Hotspot, open (one race) | `ErrCancelAfterAcknowledgement`; `CancelOnSourcingCancel` logs and publishes nothing; an acceptance after order-sourcing cancelled a `RELEASED` assignment is only logged there |
| H3: label is synchronous | Hotspot, kept by design | `POST /vendor/shipping-labels` answers `200` with the labels; it calls shipping-and-tracking inline |
| H4: suspension had no route, event or effect | Hotspot, resolved | `POST /vendors/{vendorId}:suspend` and `:reinstate`, `VendorSuspended` and `VendorReinstated`, `vendor.Account.EnsureActive` before every write |
| H5: vendor never learns its `nodeId` | Hotspot, resolved | `GET /vendor/transactions/{transactionId}` returns `nodeId` (`result_node_id`) |
| H6: caller identity without auth | Hotspot, open | `vendorId` is a body field and a required query parameter; `ErrNodeNotOwnedByVendor` is a consistency check, not authentication (in-cluster boundary) |
