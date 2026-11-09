-- Reverts 0004: drops ownerRef from every stored purchase-order line,
-- keeping the line order.
UPDATE purchase_orders
   SET lines = (
       SELECT COALESCE(jsonb_agg(l - 'ownerRef' ORDER BY ord), '[]'::jsonb)
         FROM jsonb_array_elements(lines) WITH ORDINALITY AS t(l, ord)
   );
