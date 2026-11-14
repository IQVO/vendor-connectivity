-- Return advices and return outcomes (ADR 0014), reversed.

DROP INDEX IF EXISTS idx_return_advices_node;
DROP INDEX IF EXISTS idx_return_advices_poll;
DROP TABLE IF EXISTS vendor_return_advices;

DELETE FROM submissions WHERE kind = 'RETURN_OUTCOME';
ALTER TABLE submissions DROP CONSTRAINT submissions_kind_check;
ALTER TABLE submissions ADD CONSTRAINT submissions_kind_check
    CHECK (kind IN ('ACKNOWLEDGEMENT', 'INVENTORY', 'NODE_REGISTRATION', 'NODE_PROFILE', 'SHIPMENT_CONFIRMATION', 'INBOUND_RECEIPT'));
