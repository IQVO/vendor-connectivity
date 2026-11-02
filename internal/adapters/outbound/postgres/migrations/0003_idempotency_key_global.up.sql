-- REST guideline 2.3: the Idempotency-Key is global, not per route. Rows that
-- share a key across routes (24 h retention) keep only the oldest; the route
-- column stays and is compared with the incoming method + path.
DELETE FROM idempotency_keys a
    USING idempotency_keys b
    WHERE a.key = b.key
      AND (a.created_at, a.route) > (b.created_at, b.route);
ALTER TABLE idempotency_keys DROP CONSTRAINT idempotency_keys_pkey;
ALTER TABLE idempotency_keys ADD PRIMARY KEY (key);
