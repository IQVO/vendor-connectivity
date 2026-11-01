---
id: sequence-diagrams
title: Sequence Diagrams
sidebar_label: Sequence Diagrams
sidebar_position: 9
description: "One UML sequence per Vendor Connectivity command use case, from inbound adapter through aggregate, repository and outbox to Kafka, with error, version and idempotency branches."
---

# Sequence Diagrams

One UML sequence per command use case on
[Use Cases](/docs/ddd/use-cases). Shared conventions:

- A repository saves with `WHERE version = :expected`; zero rows updated is a version conflict
  (`ErrConcurrentUpdate`, `409 concurrent-update`), and `Service.atomically` re-runs the unit once.
- `Outbox` rows are written **in the same database transaction** as the
  aggregate (`UoW`); a separate relay reads unpublished rows and produces
  them to Kafka `retailnetwork.vendor-connectivity.events`, then marks them
  published. The relay is drawn once per diagram as `Relay + Kafka`.
- Consumers claim the CloudEvents `id` in `processed_events` in the same
  transaction as their effect.
- Every `/vendor/...` request passes the rate limiter first (`429` with
  `Retry-After`); it is drawn only in diagram 4.
- Every write route sits behind the transactional `Idempotency-Key` middleware, which opens the
  Postgres transaction the use case then joins; a replay of the same key and body returns the stored
  answer without running the use case, and a 5xx rolls everything back.

Event names are short; the full types are on
[Domain Events](/docs/ddd/domain-events).

## 1. OnboardVendor

```mermaid
%%{init: {"sequence": {"wrap": true, "width": 120, "actorMargin": 24, "messageMargin": 28, "boxMargin": 6, "noteMargin": 6, "mirrorActors": false}}}%%
sequenceDiagram
    autonumber
    participant H as http POST /vendors
    participant UC as OnboardVendor
    participant A as VendorAccount
    participant R as VendorRepository
    participant OB as Outbox
    participant K as Relay + Kafka
    H->>UC: Execute(vendorId, name, ackWindowMinutes)
    UC->>R: Get(vendorId)
    alt account exists
        R-->>UC: existing account
        UC-->>H: 200 existing account unchanged, no event
    else new vendor
        UC->>A: Onboard(vendorId, name, ackWindow)
        alt ackWindow outside 1h to 24h
            A-->>UC: ErrAckWindowOutOfRange
            UC-->>H: 422 ack-window-out-of-range
        else valid
            A-->>UC: VendorAccount ACTIVE
            Note over R,OB: one transaction
            UC->>R: Save(account)
            UC->>OB: append VendorOnboarded
            UC-->>H: 201 account
            OB->>K: relay publishes VendorOnboarded, key vendorId
        end
    end
```

Source: `internal/application/usecases/vendors.go` (`OnboardVendor`) and `internal/adapters/inbound/http/server.go`.

Omitted: the `Idempotency-Key` replay (returns the stored answer); body
validation (`422 validation-failed`).

## 2. SuspendVendor and ReinstateVendor

```mermaid
%%{init: {"sequence": {"wrap": true, "width": 120, "actorMargin": 24, "messageMargin": 28, "boxMargin": 6, "noteMargin": 6, "mirrorActors": false}}}%%
sequenceDiagram
    autonumber
    participant C as operator, suspend or reinstate action of POST /vendors
    participant UC as SuspendVendor or ReinstateVendor
    participant A as VendorAccount
    participant R as VendorRepository
    C->>UC: Execute(vendorId)
    UC->>R: Get(vendorId)
    alt unknown
        R-->>UC: not found
        UC-->>C: 404 not-found
    else found
        R-->>UC: account, version n
        UC->>A: Suspend() or Reinstate()
        alt already in the target state
            A-->>UC: no-op
            UC-->>C: unchanged
        else transition
            A-->>UC: SUSPENDED or ACTIVE
            UC->>R: Save(account, expected n)
            alt version conflict
                R-->>UC: stale
                UC-->>C: 409 concurrent-update
            else saved
                UC-->>C: 200 account, event appended to the outbox in the same transaction
            end
        end
    end
```

Source: `internal/application/usecases/vendors.go` (`SuspendVendor`, `ReinstateVendor`) and `handleVendorAction` in `internal/adapters/inbound/http/handlers.go`.

Omitted: the outbox append and the relay, which work as in diagram 1 (`VendorSuspended` or `VendorReinstated`, key `vendorId`); a no-op raises no event. A suspended vendor's later writes answer `409 vendor-suspended`.

## 3. IssuePurchaseOrder (policy IssueOnRelease)

```mermaid
%%{init: {"sequence": {"wrap": true, "width": 120, "actorMargin": 24, "messageMargin": 28, "boxMargin": 6, "noteMargin": 6, "mirrorActors": false}}}%%
sequenceDiagram
    autonumber
    participant KC as kafka consumer order-sourcing
    participant UC as IssuePurchaseOrder
    participant A as PurchaseOrder
    participant R as PurchaseOrderRepository and views
    participant OB as Outbox and inbox
    participant K as Relay + Kafka
    KC->>UC: FulfillmentReleasedToNode, CloudEvents id
    UC->>OB: claim id in processed_events
    alt id already processed
        UC-->>KC: ack, nothing to do
    else new event
        UC->>R: VendorNodeView.Get(nodeId)
        alt node unknown yet
            UC-->>KC: retryable error, same message retried, dead-lettered after 5 attempts
        else not a VENDOR node
            Note over UC,OB: commit inbox row only
            UC-->>KC: ack
        else vendor node
            UC->>R: FindByRelease(assignmentId, releaseNo)
            alt PO exists
                UC-->>KC: commit inbox row, ack
            else new release
                UC->>R: VendorRepository.Get(vendorId)
                UC->>A: Issue(release, ackWindow, handlingTime)
                A-->>UC: ISSUED, ackBy fixed
                Note over R,OB: one transaction
                UC->>R: Save(po, version 0)
                UC->>OB: append PurchaseOrderIssued, commit inbox row
                UC-->>KC: ack
                OB->>K: relay publishes PurchaseOrderIssued, key poNumber
            end
        end
    end
```

Source: `internal/application/usecases/orders.go` (`IssueOnRelease`, `issue`), `service.go` (`consume`) and `internal/adapters/inbound/kafka/consumers.go`.

Omitted: where `handlingTime` comes from (the node view, fed by `NodeOperationalProfileDeclared`); a vendor with no account or an invalid release is logged and dropped; `PO_REDELIVERY_RATE`, which acts on the poll, not here.

## 4. SubmitAcknowledgement

```mermaid
%%{init: {"sequence": {"wrap": true, "width": 120, "actorMargin": 24, "messageMargin": 28, "boxMargin": 6, "noteMargin": 6, "mirrorActors": false}}}%%
sequenceDiagram
    autonumber
    participant H as http POST /vendor/acknowledgements
    participant UC as SubmitAcknowledgement
    participant A as PurchaseOrder
    participant R as Repos and views
    participant S as Submission
    participant OB as Outbox
    H->>H: rate limiter, RATE_LIMIT_RPS
    alt over the limit
        H-->>H: 429 rate-limited, Retry-After
    else allowed
        H->>H: Idempotency-Key middleware, lookup (route, key)
        alt key seen with the same body
            H-->>H: replay the stored answer, same transactionId
        else new key
            H->>UC: Execute(vendorId, poNumber, decision, lines) in the middleware transaction
            UC->>R: vendor active check, PurchaseOrderRepository.Get(poNumber), owner check
            alt unknown PO or other vendor
                UC-->>H: 404 not-found or node-not-owned-by-vendor, 409 vendor-suspended
            else owned
                UC->>A: SubmitAcknowledgement(decision, lines, txId, now)
                alt rule broken
                    A-->>UC: ErrPartialAcknowledgement or ErrAlreadyAcknowledged or ErrAckWindowClosed
                    UC-->>H: 422 partial-acknowledgement or 409 already-acknowledged or ack-window-closed
                else valid
                    A-->>UC: ACK_PROCESSING
                    UC->>S: Receive(vendorId, ACKNOWLEDGEMENT, payload, now, delay)
                    Note over R,OB: one transaction, no event yet
                    UC->>R: Save(po, expected version), Save(submission)
                    alt version conflict
                        R-->>UC: stale, rolled back
                        UC-->>H: 409 concurrent-update
                    else committed
                        UC-->>H: 202 transactionId
                    end
                end
            end
        end
    end
```

Source: `internal/application/usecases/orders.go` (`SubmitAcknowledgement`), `internal/adapters/inbound/http/ratelimit.go` and `idempotency.go`.

Omitted: the relay, because nothing is published at receipt;
`PurchaseOrderAccepted` or `PurchaseOrderRejected` follows in diagram 10.
The version-conflict branch is retried once inside the use case before it
is returned. A 4xx answer is stored by the middleware and replayed for the same key.

## 5. RequestLabel

```mermaid
%%{init: {"sequence": {"wrap": true, "width": 120, "actorMargin": 24, "messageMargin": 28, "boxMargin": 6, "noteMargin": 6, "mirrorActors": false}}}%%
sequenceDiagram
    autonumber
    participant H as http POST /vendor/shipping-labels
    participant UC as RequestLabel
    participant A as PurchaseOrder
    participant R as PurchaseOrderRepository
    participant ST as shipping-and-tracking
    H->>UC: Execute(vendorId, poNumber, packages)
    UC->>R: Get(poNumber), owner check
    alt unknown PO or other vendor
        UC-->>H: 404
    else owned
        UC->>A: LabelPlan(packageNos)
        alt not ACCEPTED
            A-->>UC: ErrLabelBeforeAcceptance
            UC-->>H: 409 label-before-acceptance
        else ACCEPTED
            loop each packageNo without a stored label
                UC->>ST: POST /labels IssueLabel poNumber, assignmentId, packageNo
                alt label already issued there
                    ST-->>UC: existing labelRef, trackingNumber, carrier
                else issued
                    ST-->>UC: labelRef, trackingNumber, carrier
                end
            end
            UC->>A: AddLabels(labels), in its own unit of work
            UC->>R: Save(po, expected version)
            alt version conflict
                R-->>UC: stale
                UC-->>H: 409 concurrent-update, labels are safe to request again
            else saved
                UC-->>H: 200 labels, no address
            end
        end
    end
```

Source: `internal/application/usecases/orders.go` (`RequestLabel`, `issueLabels`, `storeLabels`) and `internal/adapters/outbound/httpclient/clients.go`.

Omitted: outbox and relay, because this context publishes nothing here
(shipping-and-tracking publishes `LabelIssued`). shipping-and-tracking
unavailable answers `503 upstream-unavailable`, a refusal `502 upstream-refused`; labels
issued before the failure are stored and returned on the next request.

## 6. SubmitShipmentConfirmation

```mermaid
%%{init: {"sequence": {"wrap": true, "width": 120, "actorMargin": 24, "messageMargin": 28, "boxMargin": 6, "noteMargin": 6, "mirrorActors": false}}}%%
sequenceDiagram
    autonumber
    participant H as http POST /vendor/shipment-confirmations
    participant UC as SubmitShipmentConfirmation
    participant A as PurchaseOrder
    participant R as Repos and views
    participant S as Submission
    H->>H: Idempotency-Key middleware, lookup (route, key)
    alt key seen with the same body
        H-->>H: replay the stored answer, same transactionId
    else new key
        H->>UC: Execute(vendorId, poNumber, trackingNumbers, shippedAt)
        UC->>R: vendor active check, Get(poNumber), owner check
        UC->>A: SubmitShipmentConfirmation(trackingNumbers, shippedAt, txId)
        alt tracking numbers differ from the labelled ones, or the order is not yet ACCEPTED
            A-->>UC: ErrConfirmWithoutLabel
            UC-->>H: 409 confirm-without-label
        else any other state
            A-->>UC: illegal transition
            UC-->>H: 409 illegal-transition
        else valid
            A-->>UC: SHIPMENT_PROCESSING
            UC->>S: Receive(vendorId, SHIPMENT_CONFIRMATION, payload, now, delay)
            Note over R,S: one transaction, no event yet
            UC->>R: Save(po, expected version), Save(submission)
            alt version conflict
                UC-->>H: 409 concurrent-update
            else committed
                UC-->>H: 202 transactionId
            end
        end
    end
```

Source: `internal/application/usecases/orders.go` (`SubmitShipmentConfirmation`) and `internal/domain/purchaseorder/commands_shipment.go`.

Omitted: rate limiting and the `404` branch (as in diagram 4);
`VendorShipmentConfirmed` follows in diagram 10.

## 7. ExpireUnacknowledged (the expiry sweeper)

```mermaid
%%{init: {"sequence": {"wrap": true, "width": 120, "actorMargin": 24, "messageMargin": 28, "boxMargin": 6, "noteMargin": 6, "mirrorActors": false}}}%%
sequenceDiagram
    autonumber
    participant T as expiry ticker
    participant UC as ExpireUnacknowledged
    participant A as PurchaseOrder
    participant R as PurchaseOrderRepository
    participant OB as Outbox
    participant K as Relay + Kafka
    T->>UC: every EXPIRY_SWEEP_INTERVAL
    UC->>R: ListExpirable(now, 100)
    loop each ISSUED PO past ackBy
        UC->>A: Expire(now)
        A-->>UC: EXPIRED
        Note over R,OB: one transaction
        UC->>R: Save(po, expected version)
        alt version conflict, the vendor acknowledged meanwhile
            R-->>UC: stale, rolled back
            Note over UC: skip, the next pass re-reads
        else saved
            UC->>OB: append PurchaseOrderExpired
            OB->>K: relay publishes PurchaseOrderExpired, key poNumber
        end
    end
```

Source: `internal/application/usecases/orders.go` (`ExpireUnacknowledged`) and `cmd/api/background.go` (`startBackground`).

Omitted: a PO in `ACK_PROCESSING` is not listed; it is expired on a later
pass only if its transaction fails and returns it to `ISSUED` after
`ackBy`.

## 8. CancelOnSourcingCancel

```mermaid
%%{init: {"sequence": {"wrap": true, "width": 120, "actorMargin": 24, "messageMargin": 28, "boxMargin": 6, "noteMargin": 6, "mirrorActors": false}}}%%
sequenceDiagram
    autonumber
    participant KC as kafka consumer order-sourcing
    participant UC as CancelOnSourcingCancel
    participant A as PurchaseOrder
    participant R as PurchaseOrderRepository
    participant OB as Outbox and inbox
    participant K as Relay + Kafka
    KC->>UC: FulfillmentCancelled, CloudEvents id
    UC->>OB: claim id in processed_events
    alt already processed
        UC-->>KC: ack
    else new
        UC->>R: FindLatestByAssignment(assignmentId)
        alt no PO
            UC-->>KC: commit inbox row, ack
        else PO found
            UC->>A: Cancel(reason)
            alt not ISSUED
                A-->>UC: ErrCancelAfterAcknowledgement
                Note over UC: INFO purchase order not cancelled, commit inbox row, nobody is told
                UC-->>KC: ack
            else ISSUED
                A-->>UC: CANCELLED
                Note over R,OB: one transaction
                UC->>R: Save(po, expected version)
                UC->>OB: append PurchaseOrderCancelled, commit inbox row
                UC-->>KC: ack
                OB->>K: relay publishes PurchaseOrderCancelled, key poNumber
            end
        end
    end
```

Source: `internal/application/usecases/orders.go` (`CancelOnSourcingCancel`) and `internal/domain/purchaseorder/commands.go` (`Cancel`).

Omitted: a version conflict rolls back the whole transaction, including
the inbox row, so Kafka redelivers the event and the cancel is retried.

## 9. Receiving a node registration, profile or inventory write

```mermaid
%%{init: {"sequence": {"wrap": true, "width": 120, "actorMargin": 24, "messageMargin": 28, "boxMargin": 6, "noteMargin": 6, "mirrorActors": false}}}%%
sequenceDiagram
    autonumber
    participant H as http POST /vendor/nodes, PUT .../profile, POST /vendor/inventory
    participant UC as receiveOnly
    participant V as VendorNodeView
    participant S as Submission
    participant R as SubmissionRepo
    H->>H: Idempotency-Key middleware, lookup (route, key)
    alt key seen with the same body
        H-->>H: replay the stored answer, same transactionId
    else new key
        H->>UC: Execute(vendorId, kind, payload)
        UC->>UC: vendor active check
        opt kind NODE_PROFILE or INVENTORY
            UC->>V: owner of nodeId
            alt unknown or other vendor
                UC-->>H: 404 not-found or node-not-owned-by-vendor
            end
        end
        UC->>S: Receive(vendorId, kind, payload, now, TX_PROCESSING_DELAY)
        S-->>UC: PROCESSING, notBefore
        UC->>R: Save(submission)
        UC-->>H: 202 transactionId
    end
```

Source: `internal/application/usecases/submissions.go` (`receiveOnly`, `RegisterVendorNode`, `DeclareNodeProfile`, `SubmitInventory`).

Omitted: rate limiting and body validation (`422 validation-failed`); outbox and
relay, because nothing is published at receipt. An inventory line whose listing belongs to another node is refused at receipt. Inbound receipts follow the same shape, with a shipment check and an `InboundReceiptSubmitted` event appended in the same unit.

## 10. ProcessSubmission

```mermaid
%%{init: {"sequence": {"wrap": true, "width": 120, "actorMargin": 24, "messageMargin": 28, "boxMargin": 6, "noteMargin": 6, "mirrorActors": false}}}%%
sequenceDiagram
    autonumber
    participant UC as ProcessDueSubmissions, on processor tick
    participant A as Submission and PurchaseOrder
    participant R as Repos
    participant NB as node-topology or offer-availability
    participant OB as Outbox
    participant K as Relay + Kafka
    UC->>R: ListDue(now, 100)
    loop each PROCESSING submission
        alt now before notBefore
            A-->>UC: ErrSubmissionNotDue, skip
        else due
            alt drawn by TX_FAILURE_RATE
                Note over UC: outcome FAILURE, simulated error
            else NODE_REGISTRATION, NODE_PROFILE or INVENTORY
                UC->>NB: POST /nodes, PUT operational-profile or POST /inventory-feeds
                alt 2xx
                    NB-->>UC: outcome SUCCESS
                else 4xx
                    NB-->>UC: outcome FAILURE, code is the owner's Err name
                else 5xx or timeout
                    Note over UC: stay PROCESSING, next tick, FAILURE ErrUpstreamUnavailable after 5 passes
                end
            else ACKNOWLEDGEMENT or SHIPMENT_CONFIRMATION
                Note over UC: outcome SUCCESS
            end
            UC->>A: Submission.Complete(outcome, now)
            alt outcome already set
                A-->>UC: ErrSubmissionAlreadyProcessed, skip
            else set
                opt PO kinds
                    UC->>A: PurchaseOrder outcome, ACCEPTED, REJECTED, ISSUED, SHIPPED or ACCEPTED
                end
                Note over R,OB: one transaction for both aggregates
                UC->>R: Save(submission, po, expected versions)
                alt version conflict
                    R-->>UC: rolled back, retried next tick
                else committed
                    UC->>OB: append SubmissionProcessed and the PO event if any
                    OB->>K: relay publishes, key transactionId or poNumber
                end
            end
        end
    end
```

Source: `internal/application/usecases/submissions.go` (`ProcessDueSubmissions`, `process`, `execute`, `finish`).

Omitted: the PO event is `PurchaseOrderAccepted`, `PurchaseOrderRejected`
or `VendorShipmentConfirmed` on success, and none on failure. Changing two
aggregates in one transaction is deliberate: `finish` saves the order, the
submission and the outbox rows in one unit of work (see [Use cases](/docs/ddd/use-cases)).
Acknowledgements, shipment confirmations and inbound receipts succeed at once; a processed
submission whose order has moved on is logged and the order left as is.
