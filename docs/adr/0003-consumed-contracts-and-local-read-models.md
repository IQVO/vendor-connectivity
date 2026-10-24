# ADR 0003: consumed contracts, local read models, synchronous calls and relationships

## Status

Accepted (2026-10-23).

## Context

vendor-connectivity is the retail network's Open Host Service for vendors (model
`roles: Gateway / Open Host Service, Execution`). It is fed by three producers,
serves the Vendor API to one downstream (the warehouse-systems fleet's
`network-fulfillment`, Conformist + Anti-Corruption Layer) and makes four
in-organization calls the model lists under `sync_calls`. `contracts-delta.md` E2
and E4 and Gap Register items G2, G4, G12 and G32 shape the seam.

## Decision

### Relationships

| Neighbour | Direction | Pattern | Contract |
| --- | --- | --- | --- |
| network-fulfillment (fleet vendor V-WS) | calls us | Open Host Service + Published Language; downstream is Conformist + ACL | `apis/openapi.yaml`. No Kafka topic is shared, in either direction. |
| order-sourcing | Kafka in, Kafka out | Customer/Supplier | in: `FulfillmentReleasedToNode`, `FulfillmentCancelled`; out: `PurchaseOrderAccepted`, `Rejected`, `Expired`, `VendorShipmentConfirmed` |
| node-topology | Kafka in, REST out | Conformist | in: `NodeRegistered`, `NodeOperationalProfileDeclared`, `NodeActivated`, `NodeSuspended`, `NodeRetired`; out: `POST /nodes`, `PUT /nodes/{nodeId}/operational-profile` |
| offer-availability | Kafka in, REST out | Conformist | in: `ListingCreated`; out: `POST /inventory-feeds` |
| shipping-and-tracking | REST out | Customer | `POST /labels` (`IssueLabel`) |
| node-performance | REST out | Customer | `GET /scorecards/{nodeId}` (`GetScorecard`) |

### Consumed events (all on `.events`, dispatched on the FULL type, tolerant decoding)

| Type | Fields relied on | Effect |
| --- | --- | --- |
| `com.retailnetwork.logistics.node-topology.node.NodeRegistered` | nodeId, nodeType, subtype?, operator, vendorId?, vendorNodeCode?, regionId, timezone | upsert `VendorNodeView` (A13) |
| `...node-topology.node.NodeOperationalProfileDeclared` | nodeId, profileRevision, handlingTimeMinutes | `VendorNodeView.handlingTimeMinutes` for `ackBy` (E4, G4); a lower revision never overwrites a higher one |
| `...node-topology.node.NodeActivated` / `NodeSuspended` / `NodeRetired` | nodeId | `VendorNodeView.status` |
| `com.retailnetwork.commerce.offer-availability.listing.ListingCreated` | listingId, catalogItemId?, nodeId | upsert `ListingView` (A12) |
| `com.retailnetwork.commerce.order-sourcing.assignment.FulfillmentReleasedToNode` | assignmentId, releaseNo, orderId, groupNo?, nodeId, requiredShipBy, tier, lines[{listingId, quantity}] | policy `IssueOnRelease` -> `PurchaseOrderIssued` (A1, A3, A4) |
| `...order-sourcing.assignment.FulfillmentCancelled` | assignmentId, orderId?, groupNo?, reason? | policy `CancelOnSourcingCancel` (A15, A16) |

Consumer rules: one mandatory consumer group `VENDOR_CONNECTIVITY_CONSUMER_GROUP`; the inbox table
`processed_events` keyed by the CloudEvents `id`, written in the same transaction as the effect; a
message that is not CloudEvents 1.0, has an unknown type or a malformed payload is skipped past (committed)
and counted; a transient failure is retried with capped backoff and then published to `<topic>.dlq`
with `x-dlq-*` headers. The node-topology event for a node that is not yet known is the only
domain-level retry (A4). `orderId` and `tier` from the release are stored on the purchase order and never
leave the service except in the `IssueLabel` call (E2, G2); `groupNo` is stored for diagnostics.

### Local read models

- `VendorNodeView` (`network.Node`, table `vendor_nodes`): nodeId, operator, vendorId, vendorNodeCode, nodeType,
  subtype, regionId, timezone, status, handlingTimeMinutes, profileRevision. It answers ownership
  (`ErrNodeNotOwnedByVendor`), `GET /vendor/nodes` and `GET /vendor/nodes/{nodeId}`, and the handling time of `ackBy`.
- `ListingView` (`network.Listing`, table `listings`): listingId, nodeId, catalogItemId.

Neither is exposed except through the Vendor API's own schemas.

### Synchronous in-organization calls (model `sync_calls`; nothing else is called)

| Use case | Neighbour route | When |
| --- | --- | --- |
| `RegisterNode` | node-topology `POST /nodes` (operator VENDOR, `vendorRef{vendorId, vendorNodeCode}`) | `ProcessSubmission` of `NODE_REGISTRATION` |
| `DeclareOperationalProfile` | node-topology `PUT /nodes/{nodeId}/operational-profile` (`declaredBy` VENDOR) | `ProcessSubmission` of `NODE_PROFILE` |
| `ApplyInventoryFeed` | offer-availability `POST /inventory-feeds` (`feedId` = transactionId) | `ProcessSubmission` of `INVENTORY` |
| `IssueLabel` | shipping-and-tracking `POST /labels` (`{poNumber, packageNo, assignmentId, orderId, tier}`) | inside `POST /vendor/shipping-labels` (the one synchronous write, G31) |
| `GetScorecard` | node-performance `GET /scorecards/{nodeId}` | inside `GET /vendor/nodes/{nodeId}/scorecard` |

Every call carries an `Idempotency-Key` (the transaction id, or `vc-label-<poNumber>-<packageNo>`), a
per-call timeout and a bounded HTTP client; the neighbour's `application/problem+json` `type` becomes the
`Err...` code of a failed submission (A2, A10). The adapters live in `internal/adapters/outbound/<neighbour>`
behind ports in `internal/application/ports`; the fleet and the neighbours are never imported.

### Outbox, publishing and the Vendor API facts

- Every command that raises an event writes the aggregate row(s) and one `outbox_events` row per event, the
  CloudEvents envelope already encoded and its `id` minted once, in ONE Postgres transaction (ADR 0002, A27).
  The relay is always on; the attempt cap is `OUTBOX_MAX_ATTEMPTS` (default 20).
- `idempotency_keys(route, key)` with a 24 hour TTL replays the first response of every state-changing
  `POST`/`PUT`; the key is per route (model conventions).
- Vendor identity under no authentication (G32): `vendorId` in every write body and as a required query parameter on
  reads. This is a consistency check, not security. The Vendor API carries no ship-to PII, no delivery promise and no push.

## Consequences

- The hot paths (poll, acknowledge, label) read local tables; only the label and scorecard reads call a neighbour in-request.
- A neighbour that is down delays a transaction (it stays `PROCESSING`) instead of losing it.

> Amended by [ADR 0005](./0005-inbound-shipments-receipts-and-catalog-packaging.md): two more consumed producers (seller-inbound, offer-availability packaging events) and two more local read models.
