---
paths:
  - "internal/adapters/inbound/http/**"
  - "apis/openapi*.yaml"
  - "apis/openapi/**"
---

# REST API (inbound adapter)

Route -> use case, kept in sync with `apis/openapi.yaml`; routes are registered in
`internal/adapters/inbound/http/server.go` (`NewRouter`), handlers are in `handlers.go`, and
`cmd/api/main.go` wires the `Server`. Every vendor read takes a required `vendorId` query
parameter and every vendor write carries `vendorId` in the body (G32, a consistency check, not
security):

- `GET /vendor/purchase-orders?vendorId=&createdAfter=&nodeId=&nextToken=&limit=` -> `PollPurchaseOrders`
- `GET /vendor/purchase-orders/{poNumber}` -> `GetPurchaseOrder`
- `POST /vendor/acknowledgements` -> `SubmitAcknowledgement` (202 + `transactionId`)
- `POST /vendor/inventory` -> `SubmitInventory` (202)
- `POST /vendor/nodes` -> `RegisterVendorNode` (202); `GET /vendor/nodes` -> `ListVendorNodes`
- `GET /vendor/nodes/{nodeId}` -> `GetNode`; `PUT /vendor/nodes/{nodeId}/profile` -> `DeclareNodeProfile` (202)
- `GET /vendor/nodes/{nodeId}/scorecard` -> `GetScorecard`
- `POST /vendor/shipping-labels` -> `RequestLabel` (the one synchronous write: 200 with labels, G31)
- `POST /vendor/shipment-confirmations` -> `SubmitShipmentConfirmation` (202)
- `GET /vendor/transactions/{transactionId}` -> `GetTransaction`
- `POST /vendors` -> `OnboardVendor`; `GET /vendors/{vendorId}` -> `GetVendor`;
  `POST /vendors/{vendorId}:suspend` -> `SuspendVendor`; `POST /vendors/{vendorId}:reinstate` -> `ReinstateVendor`
  (chi cannot route a `:verb` suffix, so both share `/vendors/{vendorAction}` and `handleVendorAction` splits it)
- `GET /healthz` (liveness), `GET /readyz` (drain gate, Postgres ping, Kafka metadata): not business operations.

## Conventions

- **Errors** are RFC 9457 `application/problem+json`: `type` is a kebab-case slug from
  `problemCatalogue` in `internal/adapters/inbound/http/errors.go` (shared: `not-found` 404,
  `illegal-transition` 409, `concurrent-update` 409, `validation-failed` 422; one slug per model
  `Err...`, e.g. `partial-acknowledgement`, `ack-window-closed`, `vendor-suspended`), plus `title`,
  `status`, `detail`, `instance`. An unmapped error is an opaque 500 `internal-error`. A new `Err`
  needs a catalogue row.
- **Idempotency-Key** is required on every write (`idempotency.go`): same key + same request
  replays the first response, same key + other request is 422 `idempotency-key-reused`, a missing
  key is 422 `idempotency-key-required`; keys live 24 h in `idempotency_keys`. A new write
  registers through the `write(...)` wrapper in `NewRouter`, never bare `r.Post`.
- **NUL** (U+0000) is rejected before anything else (`nul.go`): 404 in the path, 400
  `malformed-request` in the query or body.
- **Rate limit**: one token bucket over `/vendor/*` (`ratelimit.go`, `RATE_LIMIT_RPS`, 429
  `rate-limited` with `Retry-After`).
- Bodies are decoded strictly (`decodeJSON`, `response.go`): unknown fields are refused
  (`validation-failed`).
- **Handlers hold no business logic**: decode, call a use case, encode the view.
- **Auth**: none. REST is unauthenticated, as in the rest of the network
  (`internal/architecture/fitness_test.go` `TestNoAuthMiddlewareReintroduced` keeps it so).
- Tests: `handlers_test.go` runs the real router over the memory doubles; the real middleware and
  Postgres are proven by `cmd/api/e2e_integration_test.go`; behaviour by `features/*.feature`.
