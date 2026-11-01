---
id: entity-relationship
title: Entity Relationship
sidebar_label: Entity Relationship
sidebar_position: 8
description: "The Postgres schema of Vendor Connectivity as migrated: one table per aggregate root with lines, labels and errors held as JSONB, four event-fed read-model tables, and the outbox, inbox and idempotency infrastructure."
---

# Entity Relationship

The schema of this context's own database, as created by the two embedded migrations
(`internal/adapters/outbound/postgres/migrations/0001_vendor_connectivity_schema.up.sql` and
`0002_inbound_shipments_and_packaging.up.sql`). No other context reads it ("every context owns its
database; no shared tables"). There are **no foreign keys at all**: each aggregate is one row whose
value objects (order lines, labels, tracking numbers, submission errors, shipment cartons) are
JSONB columns, so an aggregate is loaded and saved by one primary key and one `version`. Links
between aggregates and to other contexts are by value.

## Aggregates

### VendorAccount

```mermaid
erDiagram
    vendor_accounts {
        TEXT vendor_id PK "for example V-WS"
        TEXT name
        INTEGER ack_window_minutes "CHECK 60 to 1440"
        TEXT state "CHECK ACTIVE, SUSPENDED"
        TIMESTAMPTZ onboarded_at
        TIMESTAMPTZ suspended_at "nullable"
        TEXT suspend_reason "default empty"
        BIGINT version "optimistic concurrency"
    }
```

Source: `internal/adapters/outbound/postgres/migrations/0001_vendor_connectivity_schema.up.sql` (`vendor_accounts`).

### PurchaseOrder

```mermaid
erDiagram
    purchase_orders {
        TEXT po_number PK "PO- plus 16 hex of sha256 of assignment_id:release_no"
        TEXT vendor_id "by value"
        TEXT node_id "by value"
        TEXT assignment_id UK "UK with release_no"
        INTEGER release_no UK "UK with assignment_id, CHECK >= 1"
        TEXT order_id "never leaves except in IssueLabel"
        INTEGER group_no
        TEXT tier "CHECK SAME_DAY, NEXT_DAY, TWO_DAY, STANDARD"
        TIMESTAMPTZ issued_at
        TIMESTAMPTZ required_ship_by
        TIMESTAMPTZ ack_by "fixed at issue"
        TEXT state "CHECK the 8 PO states"
        JSONB lines "lineNo, listingId, quantity"
        TEXT ack_decision "nullable, ACCEPT or REJECT"
        TEXT ack_reason_code "nullable"
        TEXT ack_transaction_id "nullable, by value"
        JSONB labels "packageNo, labelRef, trackingNumber, carrier"
        TEXT shipment_transaction_id "nullable, by value"
        TIMESTAMPTZ shipment_shipped_at "nullable"
        JSONB shipment_tracking "nullable"
        TEXT cancel_reason "default empty"
        TIMESTAMPTZ cancelled_at "nullable"
        BIGINT version "optimistic concurrency"
    }
```

Source: `internal/adapters/outbound/postgres/migrations/0001_vendor_connectivity_schema.up.sql` (`purchase_orders`) and `internal/adapters/outbound/postgres/repositories_purchase_order.go`.

### Submission

```mermaid
erDiagram
    submissions {
        TEXT transaction_id PK
        TEXT vendor_id "by value"
        TEXT kind "CHECK the 6 kinds"
        TEXT status "CHECK PROCESSING, SUCCESS, FAILURE"
        BYTEA payload "the vendor body verbatim"
        TEXT po_number "default empty, by value"
        TEXT node_id "default empty, by value"
        TEXT result_node_id "default empty, the registered nodeId"
        TIMESTAMPTZ received_at
        TIMESTAMPTZ not_before "received_at + TX_PROCESSING_DELAY"
        TIMESTAMPTZ processed_at "nullable"
        JSONB errors "code, message, path"
        INTEGER attempts "CHECK >= 0"
        BIGINT version "optimistic concurrency"
    }
```

Source: `internal/adapters/outbound/postgres/migrations/0001_vendor_connectivity_schema.up.sql` (`submissions`), widened by `0002_inbound_shipments_and_packaging.up.sql` to allow the `INBOUND_RECEIPT` kind.

Indexes (all partial or keyset-friendly): `purchase_orders (vendor_id, issued_at, po_number)` for the
poll, `(node_id, issued_at, po_number)` per node, `(ack_by, po_number) WHERE state = 'ISSUED'` for
the expiry sweep; `submissions (not_before, transaction_id) WHERE status = 'PROCESSING'` for the
processor and `(processed_at) WHERE status <> 'PROCESSING'` for retention.

## Read models and infrastructure

### Read models

```mermaid
erDiagram
    vendor_nodes {
        TEXT node_id PK
        TEXT operator "VENDOR or NETWORK, default empty"
        TEXT vendor_id "default empty"
        TEXT vendor_node_code
        TEXT node_type
        TEXT subtype
        TEXT region_id
        TEXT timezone
        TEXT status "PROPOSED, ACTIVE, SUSPENDED, RETIRED"
        INTEGER handling_time_minutes "from the operational profile"
        INTEGER profile_revision
    }
    listings {
        TEXT listing_id PK
        TEXT node_id
        TEXT catalog_item_id
    }
    inbound_shipments {
        TEXT shipment_id PK
        TEXT plan_id
        TEXT seller_id
        TEXT node_id
        TEXT status "CHECK LABELLED, SHIPPED, CLOSED, CLOSED_WITH_DISCREPANCY"
        JSONB cartons "the manifest, replaced whole"
        TIMESTAMPTZ labelled_at "nullable"
        TIMESTAMPTZ shipped_at "nullable"
        TIMESTAMPTZ received_at "nullable"
    }
    catalog_packaging {
        TEXT catalog_item_id PK
        TEXT level PK "EACH, INNER, CASE, PALLET"
        TEXT gtin PK "CHECK 14 digits"
        TEXT child_gtin
        INTEGER units_per_pack "CHECK >= 1"
        BOOLEAN active
        TIMESTAMPTZ declared_at "nullable"
        TIMESTAMPTZ retired_at "nullable"
    }
```

Source: `internal/adapters/outbound/postgres/migrations/0001_vendor_connectivity_schema.up.sql` (`vendor_nodes`, `listings`) and `0002_inbound_shipments_and_packaging.up.sql` (`inbound_shipments`, `catalog_packaging`); the repositories are in `inbound_repositories.go`.

`vendor_nodes` has a partial index on `(vendor_id, node_id) WHERE operator = 'VENDOR'`;
`inbound_shipments` has two partial indexes over `SHIPPED` rows for the poll.

### Infrastructure

```mermaid
erDiagram
    outbox_events {
        BIGSERIAL id PK
        TEXT event_id UK "UK with topic, the CloudEvents id"
        TEXT topic "events or analytics"
        TEXT event_type "full CloudEvents type"
        TEXT subject
        BYTEA key "Kafka key"
        TEXT dataschema
        BYTEA value "CloudEvents JSON"
        JSONB headers "content-type"
        TIMESTAMPTZ created_at
        TIMESTAMPTZ published_at "nullable"
        INTEGER attempts
        TEXT last_error "nullable"
    }
    processed_events {
        TEXT event_id PK "CloudEvents id"
        TEXT event_type
        TIMESTAMPTZ processed_at
    }
    idempotency_keys {
        TEXT route PK "method plus path"
        TEXT key PK
        TEXT request_hash "sha256 of the body"
        INTEGER status_code "nullable until completed"
        BYTEA response_body
        JSONB response_headers
        TIMESTAMPTZ created_at
        TIMESTAMPTZ completed_at "nullable"
    }
```

Source: `internal/adapters/outbound/postgres/migrations/0001_vendor_connectivity_schema.up.sql` (`outbox_events`, `processed_events`, `idempotency_keys`), `outbox_repository.go` and `internal/adapters/inbound/http/idempotency.go`.

`outbox_events` has a partial index on `(id) WHERE published_at IS NULL` (the relay scans only the
unpublished tail); `idempotency_keys` is indexed on `created_at` for the retention sweep.
golang-migrate also keeps its own `schema_migrations (version, dirty)` table, which is not part of
the domain.

## Why there are no foreign keys

- `purchase_orders.vendor_id` and `.node_id`, `submissions.po_number`, `.node_id` and `.vendor_id`, and
  the transaction ids on `purchase_orders` are **by-value** links. Each aggregate is loaded and saved on
  its own, with its own version; a foreign key would couple their lifecycles and their locks.
- `node_id`, `listing_id`, `assignment_id` and the read-model tables point at facts **owned by other
  contexts**. The local copies arrive by events and can lag; a foreign key would turn an event-ordering
  race into a failed write.
- Labels hold `labelRef` and `trackingNumber` issued by shipping-and-tracking, by value. The ship-to
  address is not stored anywhere in this database.

## Table is not aggregate

| Table | Aggregate or role | Notes |
| --- | --- | --- |
| `vendor_accounts` | **VendorAccount** (root) | One row per vendor; never deleted. |
| `purchase_orders` | **PurchaseOrder** (root) | One row per release to a vendor node; `UNIQUE (assignment_id, release_no)` is the idempotency of `IssueOnRelease`. Lines, labels and shipment are value objects inside the row. |
| `submissions` | **Submission** (root) | Purged 30 days after processing by the retention sweeper. Errors are a JSONB value. |
| `vendor_nodes` | none (read model `VendorNodeView`) | Fed by node-topology events; also holds the handling time that feeds `ackBy`. |
| `listings` | none (read model `ListingView`) | Fed by offer-availability `ListingCreated`. |
| `inbound_shipments` | none (read model `InboundShipmentView`) | Fed by seller-inbound events; status only moves forward. |
| `catalog_packaging` | none (read model `CatalogPackagingView`) | Fed by offer-availability packaging events; a retired GTIN stays as history. |
| `outbox_events` | none (infrastructure) | Written in the same transaction as the aggregate change. |
| `processed_events` | none (infrastructure) | Written in the same transaction as the consumer's effect. |
| `idempotency_keys` | none (infrastructure) | Written in the same transaction as the use case it guards; purged after 24 h. |
