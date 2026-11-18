-- Return advices and return outcomes (ADR 0014).

-- A vendor's return outcome is a seventh kind of asynchronous write.
ALTER TABLE submissions DROP CONSTRAINT submissions_kind_check;
ALTER TABLE submissions ADD CONSTRAINT submissions_kind_check
    CHECK (kind IN ('ACKNOWLEDGEMENT', 'INVENTORY', 'NODE_REGISTRATION', 'NODE_PROFILE', 'SHIPMENT_CONFIRMATION', 'INBOUND_RECEIPT', 'RETURN_OUTCOME'));

-- ReturnAdviceView: event-fed read model of customer-returns' return
-- authorizations, attributed to the operator vendor of the destination node.
-- The line list is a JSONB value; tracking numbers grow by ReturnLabelIssued;
-- state only moves forward (AUTHORIZED, then CANCELLED or OUTCOME_RECORDED).
CREATE TABLE vendor_return_advices (
    authorization_id TEXT        PRIMARY KEY,
    order_id         TEXT        NOT NULL DEFAULT '',
    group_no         INTEGER     NOT NULL DEFAULT 0,
    vendor_id        TEXT        NOT NULL,
    node_id          TEXT        NOT NULL,
    disposition      TEXT        NOT NULL DEFAULT '',
    method           TEXT        NOT NULL DEFAULT '',
    state            TEXT        NOT NULL CHECK (state IN ('AUTHORIZED', 'CANCELLED', 'OUTCOME_RECORDED')),
    lines            JSONB       NOT NULL DEFAULT '[]',
    tracking_numbers JSONB       NOT NULL DEFAULT '[]',
    ready_at         TIMESTAMPTZ NOT NULL,
    expires_at       TIMESTAMPTZ
);

-- The poll walks the AUTHORIZED advices of a node set oldest first.
CREATE INDEX idx_return_advices_poll ON vendor_return_advices (ready_at, authorization_id) WHERE state = 'AUTHORIZED';
CREATE INDEX idx_return_advices_node ON vendor_return_advices (node_id, ready_at, authorization_id) WHERE state = 'AUTHORIZED';
