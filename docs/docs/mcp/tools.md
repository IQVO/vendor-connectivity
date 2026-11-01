---
id: tools
title: MCP tools
sidebar_label: MCP tools
sidebar_position: 2
---

# MCP tools

`cmd/mcp` serves this context's reads to AI agents over **Streamable HTTP only** (no stdio, no
SSE), on `MCP_ADDR` (default `:8090`) at `/mcp` and at `/`. The server identifies itself as
`vendor-connectivity-mcp` version `1.0.0` in the `initialize` handshake. It is **read-only by
construction** ([ADR 0006](/docs/adr/0006-mcp-server-adoption)): the adapter holds a `Reads`
interface that names exactly the nine read methods behind the REST `GET` routes, so no write use
case can even be named from it, and `TestToolSurface` / `governance_test.go` fail the build on a
write-verb tool name. There is **no authentication**; access control is the in-cluster boundary.

:::note Provenance
This page is written from the tool registry and the input structs in
`internal/adapters/inbound/mcp/tools.go`, not from a captured `tools/list` response: no server
was started for this page. The input schemas are the ones the Go SDK infers from those structs;
a field without `omitempty` is required.
:::

Every tool carries the same annotations: `readOnlyHint: true`, `idempotentHint: true`,
`openWorldHint: false`. A failing read comes back as a tool result with `isError: true` and the
text `<slug>: <detail>`, where the slug is the REST problem slug (`validation-failed`,
`not-found`, `node-not-owned-by-vendor`, `upstream-unavailable`, `upstream-refused`); any other
failure is `internal-error: an unexpected internal error occurred` and is logged server-side
(`internal/adapters/inbound/mcp/errors.go`).

Every call is scoped by `vendorId`. That is an identity claim, not authentication: ownership is
checked against it and a node or order of another vendor is not visible.

## The nine tools

| Tool | Access | Input fields | Returns |
| --- | --- | --- | --- |
| `poll_purchase_orders` | read-only | `vendorId` (required); `nodeId`, `createdAfter` (RFC 3339), `nextToken`, `limit` (1 to 200, default 50), all optional | A page of the vendor's purchase orders, oldest first: `poNumber`, `nodeId`, `state`, `issuedAt`, `requiredShipBy`, `ackBy`, lines, and once they exist the acknowledgement, labels and tracking numbers; `nextToken` is absent on the last page. Backs `GET /vendor/purchase-orders`. |
| `get_purchase_order` | read-only | `vendorId`, `poNumber` (both required) | One order with state and windows, lines, the acknowledgement once its transaction succeeded, labels, tracking numbers and `shippedAt`. Backs `GET /vendor/purchase-orders/{poNumber}`. |
| `poll_inbound_shipments` | read-only | `vendorId` (required); `nodeId`, `createdAfter` (shipped after, RFC 3339), `nextToken`, `limit` (1 to 200, default 50), all optional | A page of `SHIPPED` inbound shipments bound for the vendor's nodes, oldest shipment first, with the pallet-and-carton tree. Backs `GET /vendor/inbound-shipments`. |
| `get_inbound_shipment` | read-only | `vendorId`, `shipmentId` (both required) | One shipment: pallets with their cartons, top-level cartons and every item line in eaches. A shipment that has not shipped, or is bound for another vendor's node, is `not-found`. Backs `GET /vendor/inbound-shipments/{shipmentId}`. |
| `list_vendor_nodes` | read-only | `vendorId` (required) | The vendor's nodes sorted by `nodeId`: type, subtype, status, region, timezone, handling time and profile revision. The local copy of node-topology events. Backs `GET /vendor/nodes`. |
| `get_vendor_node` | read-only | `vendorId`, `nodeId` (both required) | One node. A node of another vendor fails with `node-not-owned-by-vendor`. Backs `GET /vendor/nodes/{nodeId}`. |
| `get_node_scorecard` | read-only | `vendorId`, `nodeId` (both required) | The node's rolling scorecards (7d, 30d, 90d: grade, on-time handover, late acknowledgement, reject, expiry and promise-kept rates, feed freshness p95, observation count), **relayed live** from node-performance; the only tool that calls a neighbour. Backs `GET /vendor/nodes/{nodeId}/scorecard`. |
| `get_transaction` | read-only | `vendorId`, `transactionId` (both required) | One asynchronous write by the `transactionId` its `202` returned: kind, status (`PROCESSING`, `SUCCESS`, `FAILURE`), `receivedAt`, `processedAt`, the node and `poNumber` it concerns and, if it failed, every error (code, message, path). A transaction of another vendor is `not-found`. Backs `GET /vendor/transactions/{transactionId}`. |
| `get_vendor` | read-only | `vendorId` (required) | The vendor account: name, `ackWindowMinutes` and state (`ACTIVE` or `SUSPENDED`). Reads stay allowed while suspended. Backs `GET /vendors/{vendorId}`. |

There are no write tools. Acknowledgements, inventory feeds, shipment confirmations, label
requests, inbound receipts, node registration and the vendor lifecycle are REST operations
(see the [API overview](/docs/api-reference)).

## Calling it

```bash
curl -s -X POST http://localhost:8090/mcp \
  -H 'Content-Type: application/json' \
  -H 'Accept: application/json, text/event-stream' \
  -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"probe","version":"0"}}}'
```

This request shape is the Streamable HTTP convention of the MCP specification; it was not run
for this page. Use an MCP client (or the Go SDK) for the session handshake, then
`tools/list` and `tools/call`. Behind Kong the route is `/api/mcp/vendor-connectivity`
(chart `mcp.route.enabled`), rewritten to `/mcp`; `GET /healthz` answers `200 {"status":"ok"}`.
The A2A agent exposes two of the same reads as skills (`get_purchase_order` and
`get_node_scorecard`) with the same names.

## Known discrepancy

The `get_transaction` tool description lists five transaction kinds
(`ACKNOWLEDGEMENT`, `INVENTORY`, `NODE_REGISTRATION`, `NODE_PROFILE`, `SHIPMENT_CONFIRMATION`);
the submission aggregate has a sixth, `INBOUND_RECEIPT` (ADR 0005), which the tool returns like
any other (`internal/adapters/inbound/mcp/tools.go`, `registerAccountTools` against
`internal/domain/submission/submission.go`). Likewise `poll_inbound_shipments` describes
statuses `SHIPPED` or `RECEIVED` while the view's statuses are `LABELLED`, `SHIPPED`, `CLOSED`
and `CLOSED_WITH_DISCREPANCY`; the poll itself only returns `SHIPPED` rows.
