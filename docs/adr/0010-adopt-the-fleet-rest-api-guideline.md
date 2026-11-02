# ADR 0010: Adopt the fleet REST API guideline

## Status

Accepted (2026-11-01). Follows [ADR 0002](./0002-aggregates-and-invariants.md) (A17 polling pages
and the `Idempotency-Key` store), and applies the fleet REST API guideline to the Vendor API.

## Context

The fleet agreed one REST guideline: noun routes (no `:verb` custom methods), an
`Idempotency-Key` on every POST and PATCH with one global key space, `409` for key reuse, an
`Idempotency-Replayed` response header, and `{items, nextCursor}` keyset pagination on every
collection GET. The Vendor API predated it.

## Decision

- **Routes.** `POST /vendors/{vendorId}:suspend` becomes `POST /vendors/{vendorId}/suspensions` and
  `POST /vendors/{vendorId}:reinstate` becomes `POST /vendors/{vendorId}/reinstatements`. The old
  routes are gone (hard cutover, no alias); `TestRetiredRoutesAreGone` pins it. `operationId`s are unchanged,
  so the MCP and A2A mappings do not move.
- **Idempotency.** Every POST is behind the middleware. The key is **global**: migration
  `0003_idempotency_key_global` makes `key` the primary key of `idempotency_keys` (existing rows stay
  valid; a duplicated key keeps its oldest row). A stored record is matched by key, then compared by
  fingerprint (method + path + SHA-256 of the body). Same fingerprint replays the first response with
  `Idempotency-Replayed: true` (the retired spelling `Idempotent-Replayed` is gone); any other
  fingerprint is `409 idempotency-key-reused` (was `422`); a missing key is `400 idempotency-key-required`;
  a key that is not 1 to 128 printable ASCII characters is `400 idempotency-key-invalid`. The key row
  commits in the same Postgres transaction as the aggregate change and the outbox rows. A 4xx is stored,
  a 5xx rolls back and is not.
- **Pagination.** `GET /vendor/purchase-orders`, `GET /vendor/inbound-shipments` and `GET /vendor/nodes`
  take `limit` (1 to 500, default 100) and `cursor` and answer `{items, nextCursor}`; the cursor is the
  base64url `(sortKey, id)` of the last row (`internal/adapters/inbound/http/page`) and the queries order
  by `(sortKey, id)` in Postgres and in memory. `nextToken`, the per-resource keys (`purchaseOrders`,
  `inboundShipments`, `nodes`) and the old REST size bounds (50/200) are retired. `invalid-limit`,
  `invalid-cursor` and `unknown-query-parameter` are `400` problems. The MCP tools keep their own schemas
  (one page and a tool-level `nextToken`, `limit` 1 to 500, default 50) and are not changed beyond the larger bound.
- **Exemption.** `GET /vendor/nodes/{nodeId}/scorecard` is `x-pagination-exempt: view`: it is one
  scorecard resource per node whose representation holds one entry per rolling window (a fixed, small set).
- **Spectral.** `.spectral.yaml` carries the fleet `rest-guideline` rule; `spectral lint apis/openapi.yaml
  --fail-severity=warn` must exit 0.

## Consequences

Breaking for every Vendor API client: it must deploy together with the REST cutover set. Callers known
outside this repository: network-fulfillment (suspension and reinstatement routes, purchase-order poll).
The domain, the events and the Kafka contracts do not change.
