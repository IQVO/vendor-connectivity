# ADR 0013: Cancel the purchase order when a release is withdrawn

## Status

Accepted (2026-11-10). Slice C1-VC of the cross-order consolidation design.
Builds on [ADR 0003](./0003-consumed-contracts-and-local-read-models.md) (the
consumed `FulfillmentReleasedToNode` and `FulfillmentCancelled` contracts),
[ADR 0004](./0004-cloudevents-envelope-and-type-catalogue.md) (the type
catalogue) and [ADR 0012](./0012-purchase-order-lines-name-the-stock-owner.md)
(per-line owner). Additive only: no dataschema version changes, no migration,
no Vendor API change.

## Context

order-sourcing is adding cross-order consolidation: at release time the
assignment that is due becomes the CARRIER and compatible queued assignments
of the same shopper and address become FOLLOWERS. One
`FulfillmentReleasedToNode` (carrier id, union lines, optional member list)
produces ONE purchase order here. The vendor sees one package; this context
keeps one `assignmentId`, `orderId`, `groupNo` and `tier` per order, all of
them the carrier's.

A merge is reversible until the vendor acknowledges. When a promise revision
makes the merged delivery time impossible for a member, order-sourcing
dissolves the merge: the pending release is withdrawn, the survivors go back to
the queue and are released again, alone or merged with someone else. This
context must then cancel the ISSUED purchase order of the withdrawn release,
or the vendor would pick goods that order-sourcing no longer wants shipped as
one package.

order-sourcing publishes a new integration event for this, on its existing
topic:

- type `com.retailnetwork.commerce.order-sourcing.assignment.FulfillmentReleaseWithdrawn`
- dataschema `urn:retailnetwork:order-sourcing:events:FulfillmentReleaseWithdrawn:v1`
- subject and Kafka key: the carrier's `assignmentId`
- `data`: `assignmentId` (the carrier), `releaseNo`, `orderId`, `groupNo` (the
  carrier's), `reason`, `withdrawnAt` (RFC 3339 UTC).
- `reason` is `PROMISE_REVISED` only (product decision: a cancel of a member of
  a live, unlocked merge is refused by order-sourcing instead of withdrawing,
  so `MEMBER_CANCELLED` does not exist). The value is carried into the cancel
  reason as an opaque string; this context does not branch on it.

This context builds against that written contract, never against
order-sourcing code. The existing `FulfillmentCancelled` policy cannot serve:
it cancels the LATEST order of an assignment, and a withdrawn release does not
cancel the carrier assignment, which is released again under the next release
number.

## Decision

The contract, verbatim:

1. New consumed type `com.retailnetwork.commerce.order-sourcing.assignment.FulfillmentReleaseWithdrawn`
   (topic `retailnetwork.order-sourcing.events`, the topic this context already
   consumes; no new subscription) dispatches to the use case
   `usecases.CancelOnReleaseWithdrawn`.
2. The use case looks the order up with `Orders.FindByRelease(assignmentId,
   releaseNo)`. Not found is a no-op (the release was never a vendor order, or
   was dropped).
3. For an ISSUED order it calls `Cancel` with the reason
   `release withdrawn: <reason>` (`release withdrawn` when the reason is
   empty), sanitised and capped like every cancel reason (control characters
   removed, at most 200 characters). The order raises `PurchaseOrderCancelled`
   (analytics stream) in the same transaction as the save.
4. Any other state is logged and left as it is, exactly as
   `CancelOnSourcingCancel` does: `ErrCancelAfterAcknowledgement` (the vendor
   already accepted) and `ErrIllegalTransition` (rejected, expired, already
   cancelled) are logged at INFO and the message is acknowledged, not returned.
5. The consumer is idempotent on the CloudEvents id (the `processed_events`
   inbox) and dispatches on the FULL type string. Decoding is tolerant: unknown
   fields are ignored.
6. A merged `FulfillmentReleasedToNode` needs NO code change. The tolerant
   decoder already ignores the optional `members` array and the per-line
   `assignmentId`; lines of a merged release are the union in member order with
   no `lineNo`, so the purchase order numbers them by position (1..n), a
   `listingId` that two members both ordered appears on two lines (duplicates
   are legal) and each line keeps its own `ownerRef` (ADR 0012). The release
   carries the carrier's fulfilment class (`STANDARD` for a consolidated
   group), so `ValidTier` never sees a fifth tier. This is pinned by tests
   below, not by new code.
7. Catalogue: ADR 0004 gets the consumed row; `apis/asyncapi.yaml` gets the
   consumed message, operation entry and payload schema; the consumer constant
   is `TypeReleaseWithdrawn` in `internal/adapters/inbound/kafka/consumers.go`.

How it is built:

- `requests.ReleaseWithdrawn{AssignmentID, ReleaseNo, Reason}` carries the
  facts the policy uses.
- `internal/application/usecases/orders_withdrawn.go` holds the use case (a
  new file: `orders.go` is already over the 300-line gate).
- The handler lives in `SourcingHandlers` beside the release and cancel
  handlers.

Tests:

- use case: ISSUED order is cancelled with the prefixed reason; an ACCEPTED
  order stays and publishes nothing; an unknown release is a no-op; a
  redelivered event id changes nothing; a stale release number does not touch
  the newer order of the same assignment; the reason is sanitised.
- consumer: a merged release (duplicate `listingId` from two members, two
  owners, unknown `members`, per-line `assignmentId`) issues ONE order with
  positional line numbers and the carrier's ids; the withdraw cancels the
  ISSUED order and leaves an ACCEPTED one; unknown fields on the withdraw are
  ignored; a payload that cannot decode is dead-lettered.
- the architecture catalogue test keeps the asyncapi and ADR 0004 in step.

## Consequences

- A promise revision that dissolves a merge cancels the vendor's order here as
  soon as the event arrives. Nothing is cancelled that the vendor already
  accepted.
- Known residual, owned by order-sourcing (its ADR 0015 documents it): the
  vendor may acknowledge in the one-message-hop window between order-sourcing
  dissolving and this context cancelling. The order is then ACCEPTED, the
  withdraw leaves it (decision 4), order-sourcing ignores the stale
  acceptance, and the vendor holds an order for the old union while the
  survivors are released again. This is the same window that exists today for
  a cancel after release; it is stated, not closed, here.
- Orders are looked up by `(assignmentId, releaseNo)`, never "latest", so a
  withdraw that arrives after the carrier was released again cannot cancel
  the new order.
- Old order-sourcing versions never send the type; the handler is dead code
  until they do. The ordering between this deploy and order-sourcing's is
  free: deploy this consumer first.
- No new topic, consumer group, environment variable, secret or chart value;
  the deployment is unchanged. The Vendor API and the MCP and A2A surfaces are
  unchanged.
