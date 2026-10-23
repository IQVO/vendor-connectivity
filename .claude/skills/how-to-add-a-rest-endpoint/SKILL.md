---
name: how-to-add-a-rest-endpoint
description: Add or change a Vendor API endpoint in this service in the hexagonal order (domain invariant, use case, handler, apis/openapi.yaml, godog scenario). Use when touching internal/adapters/inbound/http, apis/openapi.yaml, or exposing a use case over HTTP.
---

# How to add a REST endpoint

Follow this order: domain first, adapter last. A handler written before the invariant it relies on
validates nothing and gets bypassed.

The worked example is the asynchronous write `POST /vendor/acknowledgements`:
`internal/domain/purchaseorder/purchaseorder.go` `SubmitAcknowledgement` ->
`internal/application/usecases/orders.go` `SubmitAcknowledgement` ->
`internal/adapters/inbound/http/handlers.go` `handleAcknowledge`. The read-side example is
`GET /vendor/purchase-orders/{poNumber}` (`usecases/queries.go` `GetPurchaseOrder` -> `handleGetPurchaseOrder`).

## 1. Domain first

Does the rule already exist in `internal/domain/<vendor|purchaseorder|submission|network>/`? A handler
holds no business logic. A new rule goes into the aggregate with a table-driven test in the same
package and a named `Err...` variable. Pure Go: no I/O, `now` and ids passed in. Then re-run the
mutation gate for that package (`gremlins unleash ./internal/domain/<pkg>`).

## 2. Application: the use case

Add a method to `Service` in `internal/application/usecases/` (`vendors.go`, `orders.go`,
`submissions.go` for commands; `queries.go` for reads). A write that the vendor reconciles later
creates a `Submission` and returns `views.TransactionAccepted` (202); a label is the one synchronous
write (200, G31). Checks the service can make at receipt are refused synchronously and create no
transaction (ADR 0002 A2); a vendor-scoped write starts with `requireActive`, and ownership of a node
goes through `ownedNode`. The body type goes in `internal/application/requests/requests.go` with a
`Validate` that wraps `shared.ErrValidation`; the answer is a view from `internal/application/views`.
If a new repository method is needed, add it to `internal/application/ports/ports.go` (interfaces
only), implement it in `internal/adapters/outbound/postgres/` AND `internal/adapters/outbound/memory/`,
and extend the shared suite in `internal/adapters/outbound/repocontract/` so both run it. Unit-test the
use case in `internal/application/usecases/*_test.go`.

## 3. Adapter: the handler

In `internal/adapters/inbound/http/`:

- add the handler to `handlers.go` (decode with `decodeJSON`, call the use case, answer with
  `accepted(...)` or `respond(...)`; a read takes `vendorId` from the query, G32);
- register the route in `server.go` `NewRouter`: a write goes through `write(...)` so it sits behind
  the Idempotency-Key middleware, a read uses `r.Get`;
- map a new domain `Err` in `errors.go` `problemCatalogue` (kebab-case slug, the status the OpenAPI
  documents) and add the case to `handlers_test.go`.

## 4. Contract: apis/openapi.yaml

Describe the operation, its `Idempotency-Key` parameter (writes), the success body and every problem it
can return, then lint:
`npx --yes @stoplight/spectral-cli lint apis/openapi.yaml --ruleset .spectral.yaml`
(the `api-lint` CI job runs the same). Keep `.claude/rules/rest-api.md` and the README in step.

## 5. Behaviour

Add the happy path and each refusal to `features/*.feature` (runner `internal/bdd/bdd_test.go`, steps
`internal/bdd/steps_test.go`; the scenarios drive the real router and consumers over the memory
doubles and name refusals by problem slug). The real middleware and Postgres are proven by
`cmd/api/e2e_integration_test.go`.

## 6. Verify before opening the PR

`make check`, `make coverage`, `go test -tags=integration ./cmd/... -race -count=1`, and the spectral
lint above.
