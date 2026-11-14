# ADR 0014: return advices and return outcomes

## Status

Accepted (2026-11-14). Extends ADR 0002 (a seventh `SubmissionKind`), ADR 0003 (a
third event-fed read model of a sibling context), ADR 0004 (a twelfth published
type) and ADR 0005 (the same poll/submit seam as inbound shipments).

## Context

The customer-returns context (network ADR 0012) owns the shopper-facing
`ReturnAuthorization`: it decides the disposition, issues reverse labels and
publishes the authorization, label and cancellation facts on
`retailnetwork.customer-returns.events`. The facility that receives, grades and
restocks the returned units is a vendor-operated returns centre (or the origin
fulfilment centre), and vendors learn about network work and answer back ONLY
through this Vendor API (Published Language) — exactly how purchase orders and
inbound shipments already cross (ADR 0005). The customer-returns repo is built in
parallel with this slice, so the consumed shapes below are the written contract,
kept as tolerant local copies until the producer merges.

## Decision

### Vendor API (additive paths only; the purchase-order paths are untouched)

- `GET /vendor/return-advices?vendorId=&readyAfter=&nodeId=&cursor=&limit=`: the
  `AUTHORIZED` return advices of the vendor's nodes, oldest first, keyset-paged by
  `(readyAt, authorizationId)` exactly like the inbound-shipment poll (ADR 0005).
  `readyAfter` compares with `readyAt`, the instant the advice became the vendor's
  business (the authorization's `authorizedAt`). `nodeId` of another vendor is
  `node-not-owned-by-vendor`. An advice leaves the poll when its outcome is
  recorded (`POST /vendor/return-outcomes`) or when customer-returns cancels the
  authorization.
- `GET /vendor/return-advices/{authorizationId}?vendorId=`: one advice, also for a
  closed one (`state` `CANCELLED` or `OUTCOME_RECORDED`); an advice of another
  vendor's node, or unknown, is `not-found`.
- The advice body is `{authorizationId, orderId, groupNo, vendorId, nodeId,
  disposition, method, state, expiresAt, lines[{lineNo, catalogItemId, quantity,
  ownerRef?}], trackingNumbers[], readyAt}`. `lines` is the authorization's line
  list as the advice saw it; `trackingNumbers` grows by `ReturnLabelIssued`.
- `POST /vendor/return-outcomes` (Idempotency-Key): body `{vendorId, authorizationId,
  nodeId, receivedAt, lines[{catalogItemId, ownerRef?, qtyRestockable, qtyUnsellable}]}`.
  Answer `202` + `transactionId`, `SubmissionKind` `RETURN_OUTCOME`, reconciled with
  `GET /vendor/transactions/{id}`. `vendorId` is in the body like in every other
  write of this API (G32); the orchestrator's contract body did not list it (the
  ADR 0005 receipt made the same call).

### Outcome rules (error mapping by the existing problem table)

| Rule | Outcome |
| --- | --- |
| vendor suspended | 409 `vendor-suspended` |
| unknown vendor / unknown authorization / unknown node | 404 `not-found` |
| node not the vendor's | 404 `node-not-owned-by-vendor` |
| `nodeId` is not the advice's node | 422 `validation-failed` |
| advice not `AUTHORIZED` (outcome recorded, authorization cancelled) | 409 `illegal-transition` |
| `authorizationId`/`nodeId` not a valid id; `receivedAt` missing; no lines or more than 50 | 422 |
| duplicate `catalogItemId` (with the same `ownerRef`) in the lines | 422 |
| `qtyRestockable`/`qtyUnsellable` outside 0..2147483647 | 422 |

Lines the advice never authorized are NOT refused here: goods on the dock are
never turned away (the ADR 0005 rule); customer-returns records them as
`unexpected` and offer-availability does not restock them.

### Event published

`com.retailnetwork.partner.vendor-connectivity.returnoutcome.VendorReturnOutcomeSubmitted`,
entity `returnoutcome`, subject and Kafka key = `authorizationId`, stream `events`,
data `{transactionId, vendorId, authorizationId, nodeId, receivedAt,
lines[{catalogItemId, ownerRef?, qtyRestockable, qtyUnsellable}]}`. It is appended
to the outbox in the SAME unit of work that saves the submission and moves the
advice to `OUTCOME_RECORDED`, so a `202` always has its event and the advice leaves
the poll atomically. Consumer: customer-returns (completes the authorization).

### Read model and consumer (no sync calls)

| View | Fed by | Consumer group env |
| --- | --- | --- |
| `ReturnAdviceView` (`vendor_return_advices`) | customer-returns `ReturnAuthorized`, `ReturnLabelIssued`, `ReturnCancelled` on `retailnetwork.customer-returns.events` | `CUSTOMER_RETURNS_CONSUMER_GROUP` |

- Types (full strings): `com.retailnetwork.commerce.customer-returns.returnauthorization.ReturnAuthorized`,
  `...returnauthorization.ReturnLabelIssued`, `...returnauthorization.ReturnCancelled`;
  consumer group `vendor-connectivity-customer-returns` (the deploy sets the env;
  the group id itself is never a literal in code, fleet rule).
- The advice's node is the event's `destinationNodeId`, else `originNodeId` (the
  ORIGIN_NODE disposition ships to the origin node); `vendorId` is that node's
  operator vendor, taken from `VendorNodeView` — never from the event, which does
  not carry it. An advice whose destination node the view does not know, or whose
  node has no operator vendor, is counted and skipped (WARN): node-topology facts
  precede any authorization in practice, and a mis-attributed advice would hand one
  vendor another's work.
- The view only moves forward: `AUTHORIZED` < `OUTCOME_RECORDED` and `AUTHORIZED` <
  `CANCELLED`, whatever the arrival order; a late `ReturnAuthorized` after either
  terminal state changes nothing. `ReturnLabelIssued` appends a tracking number it
  has not seen (idempotent); `ReturnCancelled` after an outcome does not reopen the
  advice. Every handler runs in the inbox unit of work (`processed_events`).

### Data

Migration `0005`: `submissions.kind` accepts `RETURN_OUTCOME` (the down migration
deletes outcome submissions before restoring the old check); `vendor_return_advices`
(`authorization_id` PRIMARY KEY, `lines` JSONB, `tracking_numbers` JSONB, partial
indexes for the poll on `(ready_at, authorization_id)` and
`(vendor_id, node_id, ready_at, authorization_id)` WHERE `state = 'AUTHORIZED'`).

### Runtime

`CUSTOMER_RETURNS_CONSUMER_GROUP` is mandatory like the other five groups (boot
error and chart render error naming the variable); the chart renders
`config.customerReturnsConsumerGroup`. The topic `retailnetwork.customer-returns.events`
does not exist yet while the producer repo is being created: nothing here is gated
for that, because nothing blocks — reader construction does not dial, and with a
consumer group the Kafka protocol hands the reader an EMPTY assignment for a missing
topic (the generation loop retries), so boot, readiness (`/readyz` asks for no
topic) and the other consumers are unaffected until the topic appears. The topic is
pre-created by the deploy like any other (see the deploy notes), which remains the
clean order.

## Assumptions

- A33. `readyAt` is the authorization's `authorizedAt`: the advice is the vendor's
  business from the moment the shopper may hand the package over (a DROP_OFF advice
  needs no label to be acted on; the label only adds a tracking number).
- A34. The advice state vocabulary is this context's (`AUTHORIZED`, `CANCELLED`,
  `OUTCOME_RECORDED`); customer-returns' own states (IN_TRANSIT, RECEIVED, ...)
  are not mirrored — the vendor polls until an outcome is recorded, nothing else.
- A35. The CR type strings carry tier `commerce` per the wave contract; like the
  packaging types (A28) only the exact strings are dispatched, and a producer
  renaming a field changes what the view contains, not whether the service runs.

## Consequences

- Vendors see network return authorizations as work items and answer with graded
  quantities in the same keys the rest of the API uses.
- The service stays free of synchronous calls to customer-returns.
- Until customer-returns merges, the consumed shapes are a contract on paper; the
  consumer is inert (empty topic, empty assignment) and the poll returns empty
  pages.
