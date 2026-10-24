DROP TABLE IF EXISTS catalog_packaging;
DROP TABLE IF EXISTS inbound_shipments;
DELETE FROM submissions WHERE kind = 'INBOUND_RECEIPT';
ALTER TABLE submissions DROP CONSTRAINT submissions_kind_check;
ALTER TABLE submissions ADD CONSTRAINT submissions_kind_check
    CHECK (kind IN ('ACKNOWLEDGEMENT', 'INVENTORY', 'NODE_REGISTRATION', 'NODE_PROFILE', 'SHIPMENT_CONFIRMATION'));
