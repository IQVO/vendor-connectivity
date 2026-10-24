# ADR 0005: inbound shipments, inbound receipts and catalogue packaging

## Status

Accepted (2026-10-24). Extends ADR 0002 (a sixth `SubmissionKind`), ADR 0003 (two more
local read models and two more consumers) and ADR 0004 (an eleventh published type).

## Context

The mastercase / GTIN rollout (contracts section 10, P3 and P4) lets sellers ship SSCC-labelled
cartons to nodes. seller-inbound (a new network context, built in parallel) owns the shipment.
Vendors that operate fulfillment nodes learn about those shipments, and report what their
dock received, only through this Vendor API (Published Language). The producers
are not merged yet, so this context builds against the written contract and keeps a
tolerant local copy of the payloads it consumes.

## Decision

### Vendor API

- `GET /vendor/inbound-shipments?vendorId=&nodeId=&createdAfter=&nextToken=&limit=`: the SHIPPED
  shipments with a manifest bound for the vendor's nodes, oldest first, keyset-paged by
  `(shippedAt, shipmentId)` exactly like the purchase-order poll. `createdAfter` compares with
  `shippedAt`. `nodeId` of another vendor is `node-not-owned-by-vendor`. A shipment leaves the poll
  when seller-inbound reports it received.
- `GET /vendor/inbound-shipments/{shipmentId}?vendorId=`: the same tree for one shipment. It also
  answers for an already received shipment (`status` CLOSED or CLOSED_WITH_DISCREPANCY); a shipment
  that has not shipped, is unknown, or is bound for another vendor's node is `not-found`.
- The tree is `{shipmentId, sellerId, nodeId, shippedAt, status, pallets[{sscc, cartons[]}], cartons[]}`;
  a carton is `{sscc, parentSscc?, kind, contents[{gtin, qtyEaches, lot?, expiry?}], grossWeightG?, dimsMm?}`.
  The pallet SSCC is the `parentSscc` of its cartons; a carton with no `parentSscc` is top-level.
  Quantities are eaches, GTINs are 14 digits, SSCCs are 18 digits with a valid check digit.
- `POST /vendor/inbound-receipts` (Idempotency-Key): body `{vendorId, shipmentId, nodeId, receivedAt,
  cartons[{sscc, status, items[{gtin, qtyEaches}], complianceCodes[]}]}`. Answer 202 + `transactionId`,
  `SubmissionKind` `INBOUND_RECEIPT`, reconciled with `GET /vendor/transactions/{id}`. `vendorId` is in the
  body like in every other write of this API (G32); the contract's body did not list it.

### Receipt rules (domain `internal/domain/inbound`, error mapping by the existing problem table)

| Rule | Outcome |
| --- | --- |
| vendor suspended | 409 `vendor-suspended` |
| unknown vendor / unknown shipment / unknown node | 404 `not-found` |
| node not the vendor's | 404 `node-not-owned-by-vendor` |
| `nodeId` is not the shipment's destination | 422 `validation-failed` |
| shipment not SHIPPED with a manifest (labelled only, already received) | 409 `illegal-transition` |
| SSCC or GTIN fails the GS1 check digit (shared `gs1` value objects), GTIN indicator 9 | 422 |
| a carton not on the manifest and not `UNEXPECTED` | 422 |
| a GTIN counted in a manifest carton that is not a GTIN of that carton | 422 |
| `MISSING` carton with items; duplicate SSCC; duplicate GTIN in a carton; qty outside 0..2147483647 | 422 |
| `complianceCodes` not UPPER_SNAKE, more than 20 per carton | 422 |

Counting more or fewer eaches than the manifest says is a discrepancy to report, never a refusal (goods
on the dock are not turned away); the quantities are not compared here, seller-inbound does that.
GTINs are stored and published in the 14-digit form (fleet rule G2).

### Event published

`com.retailnetwork.partner.vendor-connectivity.inboundreceipt.InboundReceiptSubmitted`, entity
`inboundreceipt`, subject and Kafka key = `shipmentId`, stream `events`, data
`{transactionId, vendorId, nodeId, shipmentId, receivedAt, cartons[{sscc, status, items[{gtin, qtyEaches}],
complianceCodes[]}]}`. It is appended to the outbox in the SAME unit of work that saves the submission, so
a 202 always has its event. It is raised when the receipt is accepted, not when the transaction is processed
(`SubmissionProcessed` still reports that). Consumers: seller-inbound (emits `InboundShipmentReceived` and
`InboundDiscrepancyRecorded`), node-performance (later).

### Read models and consumers (no sync calls)

| View | Fed by | Consumer group env |
| --- | --- | --- |
| `InboundShipmentView` (`inbound_shipments`) | seller-inbound `InboundShipmentLabelled`, `InboundShipmentShipped`, `InboundShipmentReceived` on `retailnetwork.seller-inbound.events` | `INBOUND_CONSUMER_GROUP` |
| `CatalogPackagingView` (`catalog_packaging`) | offer-availability `CatalogItemPackagingDeclared`, `CatalogItemPackLevelRetired` on `retailnetwork.offer-availability.events` | `PACKAGING_CONSUMER_GROUP` |

- The packaging consumer reads the same topic as the listing consumer under its OWN group, so a fault in one
  never stalls the other. Both groups are mandatory (boot error and chart render error naming the variable).
- Types: `com.retailnetwork.partner.seller-inbound.inboundshipment.<Event>` and
  `com.retailnetwork.partner.offer-availability.catalogitem.<Event>`. The offer-availability tier segment is
  not pinned by a merged producer (its listing events say `commerce`), so the packaging types are accepted
  with either `partner` or `commerce`. Open assumption A28 below.
- Payloads are tolerant local copies: unknown fields are ignored, only the fields listed in
  `apis/asyncapi.yaml` are relied on. Facts that break a rule (bad SSCC, GTIN, status) are logged and
  acknowledged, never retried.
- The view only moves forward: LABELLED < SHIPPED < CLOSED/CLOSED_WITH_DISCREPANCY, whatever the arrival
  order. A shipment shipped before its label arrived is not offered until the manifest is there. The
  latest label replaces the manifest. The first close wins.
- A packaging row is `(catalogItemId, level, gtin)`; a GTIN retired at or after its declaration stops being
  active (the tie goes to the retirement, in either arrival order); a new GTIN at a level is a new row.
- Every handler runs in the inbox unit of work (`processed_events`), like the existing consumers.

### Packaging on the listing payload

The Vendor API has no listing resource: a listing reaches the vendor as `listingId` on a purchase order
line. Each `lines[]` item therefore gains `packaging[]` (always present, empty when nothing was declared):
the ACTIVE levels of the line's catalogue item, ordered EACH, INNER, CASE, PALLET, each
`{level, gtin, childGtin?, unitsPerPack}`. The lookup is listing -> `ListingView.catalogItemId` ->
`CatalogPackagingView`; a listing the view does not know, or without a catalogue item, has an empty list.

### Data

Migration `0002`: `submissions.kind` accepts `INBOUND_RECEIPT`; `inbound_shipments` (manifest as JSONB,
partial indexes for the poll) and `catalog_packaging`. The down migration deletes receipt submissions
before restoring the old check.

### Runtime

`INBOUND_CONSUMER_GROUP` and `PACKAGING_CONSUMER_GROUP` are mandatory like the other groups; the chart renders
them (`config.inboundConsumerGroup`, `config.packagingConsumerGroup`) and refuses to render without them. Five
consumers now stop in turn on shutdown, so every reader sets `MaxWait` to 500 ms (a quiet topic otherwise
blocks `Reader.Close` for up to 10 s each).

## Assumptions

- A28. The `partner` tier segment of the offer-availability packaging types is the contract; `commerce` is
  accepted too until the producer is merged. Drop the second spelling then.
- A29. `createdAfter` of the shipment poll is `shippedAt`: the instant the shipment becomes visible.
- A30. A receipt is judged against the view at submission time and not re-judged when processed; two receipts
  for the same shipment submitted before seller-inbound closes it are both accepted, seller-inbound
  deduplicates by shipment.
- A31. order-sourcing seller-awareness (a seller-owned listing per `ownerRef`) is out of this phase; only
  `sellerId` travels on the shipment tree.
- A32. `sku` is optional on a tree item and is not filled yet: the network knows catalogue items, not the
  vendor's SKUs; the consumer maps GTIN to SKU on its side.

## Consequences

- Vendors see seller cartons by SSCC and GTIN and answer in the same keys.
- The service stays free of synchronous calls to seller-inbound and offer-availability.
- Until seller-inbound is merged the consumed shapes are a contract on paper; a producer that renames a
  field changes what this view contains, not whether the service runs.
