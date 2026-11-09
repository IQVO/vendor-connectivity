# ADR 0012: Purchase-order lines name the stock owner

## Status

Accepted (2026-11-08). Closes the vendor seam of network gap G47. Builds on
[ADR 0003](./0003-consumed-contracts-and-local-read-models.md) (the consumed
`FulfillmentReleasedToNode` contract) and
[ADR 0004](./0004-cloudevents-envelope-and-type-catalogue.md) (the
`PurchaseOrderIssued` payload). Additive only: no dataschema version changes.

## Context

Seller stock in the retail network is owner-segregated (network ADR 0009:
`ownerRef` = `sellerId`). A vendor node that holds stock for several owners
must know whose stock to pick for each purchase-order line; today a
purchase-order line names only `listingId` and `quantity`, so the vendor
cannot tell an operator-owned line from a seller-owned one.

order-sourcing (branch `feature/owner-aware-sourcing`) adds an optional
`ownerRef` (string, omitted when the stock is operator-owned) to
`com.retailnetwork.commerce.order-sourcing.assignment.FulfillmentReleasedToNode`
`data.lines[]`. This context builds against that written contract.

The Vendor API is the ONLY channel to the warehouse-systems fleet: its
network-fulfillment context is a Conformist behind an anti-corruption layer
that decodes these responses. Any change must therefore be strictly additive
and optional, so the fleet's existing decoder keeps working unchanged.

Alternatives weighed:

- **Resolve the owner from the listing** (a `ListingView` lookup): the
  listing does not carry the owner, and a second source of truth for one fact
  invites drift. Rejected.
- **A per-order owner instead of a per-line owner**: one release can mix
  operator-owned and seller-owned lines, so the owner belongs on the line.
  Rejected.
- **A required field**: breaks every existing consumer and every stored row.
  Rejected; absence means operator-owned.

## Decision

The contract, verbatim:

1. The consumer of FulfillmentReleasedToNode reads optional lines[].ownerRef.
2. The PurchaseOrder line value object persists `ownerRef` (empty = operator-owned): four-places rule + repocontract, plus a migration with the next free number (existing rows ''). Update the migration test ceilings.
3. Vendor API: GET /vendor/purchase-orders and GET /vendor/purchase-orders/{poNumber} (use the exact existing routes) gain optional `lines[].ownerRef` (string, described as 'The owner of the goods to pick (the seller's ownerRef); omitted for operator-owned stock'). It is never required, and existing fields are unchanged. Regenerate the REST docs and the MCP golden.
4. PurchaseOrderIssued.data lines (if it carries lines) gain the same optional ownerRef. Update apis/asyncapi.yaml and the CloudEvents ADR if it lists fields.
5. Fill-or-kill acknowledgement, cancellation, labels and shipment confirmation are unchanged.

How it is built:

- The consumer decodes `lines[].ownerRef` tolerantly
  (`internal/adapters/inbound/kafka/consumers.go`): an absent or empty value
  is operator-owned.
- `purchaseorder.Line` and `purchaseorder.NewLine` carry `OwnerRef`
  (`internal/domain/purchaseorder`); `Issue` copies it onto the numbered
  line, `Restore` and `Snapshot()` carry it through the lines slice, and
  `checkLines` accepts an empty value or a valid id.
- Postgres keeps lines as JSONB (`purchase_orders.lines`); migration `0004`
  writes `"ownerRef": ""` into every stored line, and its down migration
  removes the key.
- The Vendor API, the MCP tools that share the read views, and
  `PurchaseOrderIssued` omit `ownerRef` when it is empty.
- Tests: consumer decode with and without `ownerRef`; the repocontract
  round-trip; HTTP golden/schema tests for both routes; an encoder golden;
  a migration integration test (up through `0004`, down one step, up again).

## Consequences

- The fleet's network-fulfillment may read `lines[].ownerRef` to pick
  owner-segregated stock. Nothing on the fleet side changes until it does.
- Known residual: until network-fulfillment reads the field, behaviour is
  unchanged for operator-owned stock, and seller-owned lines are picked as
  before (from the node's stock without owner segregation).
- An order issued before this change, or a release from an order-sourcing
  version that does not send `ownerRef`, is operator-owned; that is the
  correct reading for every line issued before owner-aware sourcing existed.
- Acknowledgement (fill-or-kill), cancellation, labels and shipment
  confirmation are unchanged: the owner is information for picking, not an
  input of any vendor write.
- No new topic, consumer group or environment variable; the deployment is
  unchanged.
