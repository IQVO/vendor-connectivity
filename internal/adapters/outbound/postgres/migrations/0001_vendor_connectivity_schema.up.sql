-- vendor-connectivity schema (ADR 0002, ADR 0003). Every context owns its database.

-- VendorAccount aggregate.
CREATE TABLE vendor_accounts (
    vendor_id          TEXT        PRIMARY KEY,
    name               TEXT        NOT NULL,
    ack_window_minutes INTEGER     NOT NULL CHECK (ack_window_minutes BETWEEN 60 AND 1440),
    state              TEXT        NOT NULL CHECK (state IN ('ACTIVE', 'SUSPENDED')),
    onboarded_at       TIMESTAMPTZ NOT NULL,
    suspended_at       TIMESTAMPTZ,
    suspend_reason     TEXT        NOT NULL DEFAULT '',
    version            BIGINT      NOT NULL CHECK (version >= 1)
);

-- PurchaseOrder aggregate. Lines, labels and the confirmed tracking numbers are
-- value objects of the aggregate and live in JSONB columns. order_id and tier
-- never leave the service except in the IssueLabel call (Gap G2).
CREATE TABLE purchase_orders (
    po_number                TEXT        PRIMARY KEY,
    vendor_id                TEXT        NOT NULL,
    node_id                  TEXT        NOT NULL,
    assignment_id            TEXT        NOT NULL,
    release_no               INTEGER     NOT NULL CHECK (release_no >= 1),
    order_id                 TEXT        NOT NULL,
    group_no                 INTEGER     NOT NULL CHECK (group_no >= 0),
    tier                     TEXT        NOT NULL CHECK (tier IN ('SAME_DAY', 'NEXT_DAY', 'TWO_DAY', 'STANDARD')),
    issued_at                TIMESTAMPTZ NOT NULL,
    required_ship_by         TIMESTAMPTZ NOT NULL,
    ack_by                   TIMESTAMPTZ NOT NULL,
    state                    TEXT        NOT NULL CHECK (state IN ('ISSUED', 'ACK_PROCESSING', 'ACCEPTED', 'REJECTED', 'EXPIRED', 'SHIPMENT_PROCESSING', 'SHIPPED', 'CANCELLED')),
    lines                    JSONB       NOT NULL,
    ack_decision             TEXT,
    ack_reason_code          TEXT,
    ack_transaction_id       TEXT,
    labels                   JSONB       NOT NULL DEFAULT '[]',
    shipment_transaction_id  TEXT,
    shipment_shipped_at      TIMESTAMPTZ,
    shipment_tracking        JSONB,
    cancel_reason            TEXT        NOT NULL DEFAULT '',
    cancelled_at             TIMESTAMPTZ,
    version                  BIGINT      NOT NULL CHECK (version >= 1),
    CONSTRAINT purchase_orders_release_key UNIQUE (assignment_id, release_no)
);

-- The poll walks one vendor's orders oldest first.
CREATE INDEX idx_purchase_orders_poll ON purchase_orders (vendor_id, issued_at, po_number);
CREATE INDEX idx_purchase_orders_node ON purchase_orders (node_id, issued_at, po_number);
-- The expiry sweep only ever scans ISSUED orders.
CREATE INDEX idx_purchase_orders_expirable ON purchase_orders (ack_by, po_number) WHERE state = 'ISSUED';

-- Submission aggregate: one asynchronous vendor write and its outcome.
CREATE TABLE submissions (
    transaction_id  TEXT        PRIMARY KEY,
    vendor_id       TEXT        NOT NULL,
    kind            TEXT        NOT NULL CHECK (kind IN ('ACKNOWLEDGEMENT', 'INVENTORY', 'NODE_REGISTRATION', 'NODE_PROFILE', 'SHIPMENT_CONFIRMATION')),
    status          TEXT        NOT NULL CHECK (status IN ('PROCESSING', 'SUCCESS', 'FAILURE')),
    payload         BYTEA       NOT NULL, -- the vendor body verbatim; JSONB would renormalise it
    po_number       TEXT        NOT NULL DEFAULT '',
    node_id         TEXT        NOT NULL DEFAULT '',
    result_node_id  TEXT        NOT NULL DEFAULT '',
    received_at     TIMESTAMPTZ NOT NULL,
    not_before      TIMESTAMPTZ NOT NULL,
    processed_at    TIMESTAMPTZ,
    errors          JSONB       NOT NULL DEFAULT '[]',
    attempts        INTEGER     NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    version         BIGINT      NOT NULL CHECK (version >= 1)
);

CREATE INDEX idx_submissions_due ON submissions (not_before, transaction_id) WHERE status = 'PROCESSING';
CREATE INDEX idx_submissions_processed ON submissions (processed_at) WHERE status <> 'PROCESSING';

-- VendorNodeView: event-fed read model of node-topology's nodes (ADR 0003).
CREATE TABLE vendor_nodes (
    node_id               TEXT    PRIMARY KEY,
    operator              TEXT    NOT NULL DEFAULT '',
    vendor_id             TEXT    NOT NULL DEFAULT '',
    vendor_node_code      TEXT    NOT NULL DEFAULT '',
    node_type             TEXT    NOT NULL DEFAULT '',
    subtype               TEXT    NOT NULL DEFAULT '',
    region_id             TEXT    NOT NULL DEFAULT '',
    timezone              TEXT    NOT NULL DEFAULT '',
    status                TEXT    NOT NULL DEFAULT '',
    handling_time_minutes INTEGER NOT NULL DEFAULT 0 CHECK (handling_time_minutes >= 0),
    profile_revision      INTEGER NOT NULL DEFAULT 0 CHECK (profile_revision >= 0)
);

CREATE INDEX idx_vendor_nodes_vendor ON vendor_nodes (vendor_id, node_id) WHERE operator = 'VENDOR';

-- ListingView: event-fed read model of offer-availability's listings.
CREATE TABLE listings (
    listing_id      TEXT PRIMARY KEY,
    node_id         TEXT NOT NULL,
    catalog_item_id TEXT NOT NULL DEFAULT ''
);

-- outbox_events: the transactional outbox. One row per already-encoded Kafka
-- message, inserted in the SAME transaction as the aggregates; the relay drains
-- unpublished rows to Kafka.
--   event_id    the CloudEvents `id`, minted once at encode time and persisted:
--               a relay retry republishes the same id.
--   event_type  the FULL CloudEvents `type`.
--   subject     the CloudEvents `subject` (the aggregate root id).
--   key         the Kafka message key (the aggregate root id).
--   dataschema  the CloudEvents `dataschema` URN.
--   value       the structured-mode CloudEvents JSON bytes.
--   headers     [{"key":..,"value":..}] Kafka headers (content-type).
CREATE TABLE outbox_events (
    id           BIGSERIAL   PRIMARY KEY,
    event_id     TEXT        NOT NULL,
    topic        TEXT        NOT NULL,
    event_type   TEXT        NOT NULL,
    subject      TEXT        NOT NULL,
    key          BYTEA,
    dataschema   TEXT        NOT NULL,
    value        BYTEA       NOT NULL,
    headers      JSONB       NOT NULL DEFAULT '[]',
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    published_at TIMESTAMPTZ,
    attempts     INTEGER     NOT NULL DEFAULT 0,
    last_error   TEXT,
    CONSTRAINT outbox_events_event_id_topic_key UNIQUE (event_id, topic)
);

-- The relay only ever scans the unpublished tail; keep that scan tiny.
CREATE INDEX idx_outbox_events_unpublished ON outbox_events (id) WHERE published_at IS NULL;

-- processed_events: the inbox of the consumers. The CloudEvents id is claimed
-- with INSERT ... ON CONFLICT DO NOTHING in the SAME transaction as the effect,
-- so a rollback un-claims it.
CREATE TABLE processed_events (
    event_id     TEXT        PRIMARY KEY,
    event_type   TEXT        NOT NULL,
    processed_at TIMESTAMPTZ NOT NULL
);

-- idempotency_keys: the fleet Idempotency-Key middleware, keyed by (route, key)
-- with a 24 hour TTL (model conventions). The middleware INSERTs a bare row
-- inside the transaction shared with the use case, then UPDATEs it with the
-- outcome before it commits, so a COMMITTED row always has a status_code.
CREATE TABLE idempotency_keys (
    route            TEXT        NOT NULL,
    key              TEXT        NOT NULL,
    request_hash     TEXT        NOT NULL,
    status_code      INTEGER,
    response_body    BYTEA,
    response_headers JSONB,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at     TIMESTAMPTZ,
    PRIMARY KEY (route, key)
);

CREATE INDEX idx_idempotency_keys_created_at ON idempotency_keys (created_at);
