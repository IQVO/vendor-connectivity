---
id: overview
title: API overview
sidebar_label: Overview
slug: /api-reference
sidebar_position: 1
---

# API overview

:::info Implemented by cmd/api
`cmd/api` serves every route below (`internal/adapters/inbound/http`), on `HTTP_ADDR` (default
`:8080`). The contracts are `apis/openapi.yaml` (the Vendor API) and `apis/asyncapi.yaml`,
pinned by [ADR 0002](/docs/adr/0002-aggregates-and-invariants),
[ADR 0004](/docs/adr/0004-cloudevents-envelope-and-type-catalogue) and
[ADR 0005](/docs/adr/0005-inbound-shipments-receipts-and-catalog-packaging). The router
registers the 19 operations of the spec plus `/healthz` and `/readyz`. Nothing here is
authenticated.
:::

| Interface | Source of truth | Notes |
| --- | --- | --- |
| REST (Vendor API) | `apis/openapi.yaml` (OpenAPI 3.0.3, version 1.0.0) | The pages under *REST* are generated from the spec with `npm run gen-api-docs`; do not edit them by hand. Server named in the spec: `http://localhost:8000/api/retail-network/vendor-connectivity` (kind cluster through Kong); direct to the pod it is `http://localhost:8080`. |
| Events | `apis/asyncapi.yaml` (AsyncAPI 3.0.0, version 1.0.0) | Eleven published and thirteen consumed types, summarised on the [event catalogue](/docs/api-reference/events); every payload field on [Domain events](/docs/ddd/domain-events). |
| MCP | [ADR 0006](/docs/adr/0006-mcp-server-adoption), `internal/adapters/inbound/mcp` | `cmd/mcp` serves Streamable HTTP on `/mcp` and `/` (port 8090; through Kong `/api/mcp/vendor-connectivity`). Nine read-only tools, no write tool; see [MCP tools](/docs/mcp/tools). |
| A2A | [ADR 0009](/docs/adr/0009-a2a-inbound-adapter), `internal/adapters/inbound/a2a` | `cmd/a2a` serves JSON-RPC 2.0 on `/` and `/a2a` (port 8093; through Kong `/api/a2a/vendor-connectivity`) and the Agent Card at `/.well-known/agent-card.json`. A message-only agent with two read-only skills, no write skill and no streaming: `get_purchase_order` (`vendorId`, `poNumber`) and `get_node_scorecard` (`vendorId`, `nodeId`), named like the MCP tools. A skill request is the first message part, a data object `{"skill": "...", "arguments": {...}}`; unknown fields are refused and a problem is answered in the reply envelope, not as a JSON-RPC error. |

## Endpoints

All `/vendor/*` routes sit behind the `RATE_LIMIT_RPS` limiter; every `POST` and `PUT` takes an
`Idempotency-Key`. Reads take `vendorId` as a required query parameter; writes carry it in the
body.

| Tag | Endpoint | Operation id | Success |
| --- | --- | --- | --- |
| Vendors | `POST /vendors` | `onboardVendor` | `201` with the account; `200` with the unchanged account when the `vendorId` exists |
| Vendors | `GET /vendors/{vendorId}` | `getVendor` | `200` |
| Vendors | `POST /vendors/{vendorId}:suspend`, `POST /vendors/{vendorId}:reinstate` | `suspendVendor`, `reinstateVendor` | `200` with the account |
| Purchase Orders | `GET /vendor/purchase-orders` | `pollPurchaseOrders` | `200`, a page with `nextToken` |
| Purchase Orders | `GET /vendor/purchase-orders/{poNumber}` | `getPurchaseOrder` | `200` |
| Acknowledgements | `POST /vendor/acknowledgements` | `submitAcknowledgement` | `202 {transactionId}` |
| Inventory | `POST /vendor/inventory` | `submitInventory` | `202 {transactionId}` |
| Nodes | `GET /vendor/nodes`, `GET /vendor/nodes/{nodeId}` | `listVendorNodes`, `getNode` | `200` |
| Nodes | `POST /vendor/nodes` | `registerVendorNode` | `202 {transactionId}`; the new `nodeId` appears on the processed transaction |
| Nodes | `PUT /vendor/nodes/{nodeId}/profile` | `declareNodeProfile` | `202 {transactionId}` |
| Nodes | `GET /vendor/nodes/{nodeId}/scorecard` | `getScorecard` | `200`; `503` when node-performance does not answer |
| Shipping Labels | `POST /vendor/shipping-labels` | `requestLabel` | `200` with the labels (the one synchronous write); `503` or `502` on a neighbour failure |
| Shipment | `POST /vendor/shipment-confirmations` | `submitShipmentConfirmation` | `202 {transactionId}` |
| Inbound | `GET /vendor/inbound-shipments`, `GET /vendor/inbound-shipments/{shipmentId}` | `pollInboundShipments`, `getInboundShipment` | `200` |
| Inbound | `POST /vendor/inbound-receipts` | `submitInboundReceipt` | `202 {transactionId}` |
| Transactions | `GET /vendor/transactions/{transactionId}` | `getTransaction` | `200` with status `PROCESSING`, `SUCCESS` or `FAILURE` |
| Health | `GET /healthz`, `GET /readyz` | not in the spec | `200`; `/readyz` is `503` while draining or when Postgres or Kafka is down (see the [Runbook](/docs/operations/runbook)) |

`/healthz` and `/readyz` are hand-written routes: they are in `internal/adapters/inbound/http/server.go`
but not in `apis/openapi.yaml`. The `:suspend` and `:reinstate` actions share one chi pattern
(`/vendors/{vendorAction}`) and are dispatched on the suffix; any other suffix is `404 not-found`.

## Conventions in the contract

- **Identity without authentication.** `vendorId` names the caller in every request. Ownership of
  the addressed node, order or shipment is checked against it (`node-not-owned-by-vendor`, `404`),
  so one vendor cannot probe another. This is a consistency check, not security.
- **Asynchronous writes.** `202` with a `transactionId`. The write takes effect only when
  `GET /vendor/transactions/{transactionId}` reports `SUCCESS`. Processing waits at least
  `TX_PROCESSING_DELAY`, and a share `TX_FAILURE_RATE` of transactions fails on purpose with
  `ErrSimulatedFailure`; a neighbour refusal fails a transaction with the neighbour's `Err...` code.
  A failed acknowledgement returns the order to `ISSUED` (so the vendor may try again inside the
  window) and a failed shipment confirmation returns it to `ACCEPTED`.
- **Idempotency.** `Idempotency-Key` is 1 to 128 characters and scoped to method and path. Every
  response below 500 (4xx included) is stored for 24 h and replayed for the same key and body
  (for an asynchronous write, the same `transactionId`); the same key with a different body is
  `422 idempotency-key-reused`; a missing or oversized key is `422 idempotency-key-required`. A
  5xx rolls back and is never stored.
- **Paging.** Polls take `nodeId`, `createdAfter` (RFC 3339), `limit` (default 50, maximum 200)
  and the opaque `nextToken` of the previous page; no `nextToken` marks the last page. A token is
  bound to its vendor and filters. Polling is oldest-first, and with `PO_REDELIVERY_RATE` a page may
  repeat its first order, so consumers must be idempotent on `poNumber`.
- **Rate limit.** `429 rate-limited` with `Retry-After` (whole seconds, at least 1) when the
  per-process token bucket is empty.
- **No PII.** A label is `labelRef`, `trackingNumber` and `carrier`; no response carries a ship-to
  address, name or phone.
- **Errors** are RFC 9457 `application/problem+json`, `type` ending in the slug. Shared slugs:
  `not-found` (404), `illegal-transition`, `concurrent-update` (409), `validation-failed` (422).
  One slug per domain error: `partial-acknowledgement`, `ack-window-out-of-range` (422);
  `node-not-owned-by-vendor` (404); `vendor-suspended`, `already-acknowledged`,
  `ack-window-closed`, `label-before-acceptance`, `confirm-without-label`,
  `cancel-after-acknowledgement`, `submission-already-processed`, `submission-not-due` (409);
  `rate-limited` (429); `idempotency-key-required`, `idempotency-key-reused` (422);
  `upstream-unavailable` (503); `upstream-refused` (502); and `internal-error` (500). The map is
  `internal/adapters/inbound/http/errors.go`; remedies are on
  [Troubleshooting](/docs/operations/troubleshooting).
- **Ordering of checks on an acknowledgement:** order state, then the window (`now` equal to
  `ackBy` is still inside), then completeness (every line once, at its ordered quantity), then the
  decision (`ACCEPT` carries no `reasonCode`; `REJECT` needs `OUT_OF_STOCK`, `UNKNOWN_LISTING`,
  `CANNOT_MEET_SHIP_BY` or `DISCONTINUED`).
- **Auth**: none. The fleet reverted REST, MCP and A2A auth on 2026-09-11, and
  `TestNoAuthMiddlewareReintroduced` fails CI if auth middleware returns under
  `internal/adapters/inbound`.

## Discrepancies between the contract and the code

- `CLAUDE.md` says "12 `/vendor/*` routes"; the router registers 15 under `/vendor/*`
  (`internal/adapters/inbound/http/server.go`) since the inbound-shipment and inbound-receipt
  routes (ADR 0005) were added; with the four `/vendors` routes that is the 19 operations of the
  spec.
- `CLAUDE.md` lists three consumers and ten events; the code runs five consumers and publishes
  eleven types (ten on `.events`, `PurchaseOrderCancelled` only on `.analytics`;
  `InboundReceiptSubmitted` came with ADR 0005).
