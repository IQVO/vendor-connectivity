-- Inbound shipments and catalogue packaging (ADR 0005).

-- A vendor's inbound receipt is a sixth kind of asynchronous write.
ALTER TABLE submissions DROP CONSTRAINT submissions_kind_check;
ALTER TABLE submissions ADD CONSTRAINT submissions_kind_check
    CHECK (kind IN ('ACKNOWLEDGEMENT', 'INVENTORY', 'NODE_REGISTRATION', 'NODE_PROFILE', 'SHIPMENT_CONFIRMATION', 'INBOUND_RECEIPT'));

-- InboundShipmentView: event-fed read model of seller-inbound's shipments. The
-- manifest (cartons and their contents) is a JSONB value, replaced whole by each
-- InboundShipmentLabelled; status only moves forward.
CREATE TABLE inbound_shipments (
    shipment_id TEXT        PRIMARY KEY,
    plan_id     TEXT        NOT NULL DEFAULT '',
    seller_id   TEXT        NOT NULL DEFAULT '',
    node_id     TEXT        NOT NULL,
    status      TEXT        NOT NULL CHECK (status IN ('LABELLED', 'SHIPPED', 'CLOSED', 'CLOSED_WITH_DISCREPANCY')),
    cartons     JSONB       NOT NULL DEFAULT '[]',
    labelled_at TIMESTAMPTZ,
    shipped_at  TIMESTAMPTZ,
    received_at TIMESTAMPTZ
);

-- The poll walks the SHIPPED shipments of a node set oldest first.
CREATE INDEX idx_inbound_shipments_poll ON inbound_shipments (shipped_at, shipment_id) WHERE status = 'SHIPPED';
CREATE INDEX idx_inbound_shipments_node ON inbound_shipments (node_id, shipped_at, shipment_id) WHERE status = 'SHIPPED';

-- CatalogPackagingView: event-fed read model of offer-availability's packaging
-- hierarchy, one row per (item, level, gtin); a retired GTIN stays as history.
CREATE TABLE catalog_packaging (
    catalog_item_id TEXT        NOT NULL,
    level           TEXT        NOT NULL CHECK (level IN ('EACH', 'INNER', 'CASE', 'PALLET')),
    gtin            TEXT        NOT NULL CHECK (gtin ~ '^[0-9]{14}$'),
    child_gtin      TEXT        NOT NULL DEFAULT '',
    units_per_pack  INTEGER     NOT NULL CHECK (units_per_pack >= 1),
    active          BOOLEAN     NOT NULL,
    declared_at     TIMESTAMPTZ,
    retired_at      TIMESTAMPTZ,
    PRIMARY KEY (catalog_item_id, level, gtin)
);
