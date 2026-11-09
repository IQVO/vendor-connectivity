-- ADR 0012: every purchase-order line names the stock owner. Lines live in
-- purchase_orders.lines (JSONB array); every stored line gains
-- "ownerRef": "" (operator-owned), keeping the line order. A line that
-- already carries ownerRef is left as it is.
UPDATE purchase_orders
   SET lines = (
       SELECT COALESCE(jsonb_agg(
                  CASE WHEN l ? 'ownerRef' THEN l ELSE l || '{"ownerRef": ""}'::jsonb END
                  ORDER BY ord), '[]'::jsonb)
         FROM jsonb_array_elements(lines) WITH ORDINALITY AS t(l, ord)
   );
