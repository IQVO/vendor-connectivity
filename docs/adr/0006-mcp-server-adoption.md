# ADR 0006: a read-only MCP server

## Status

Accepted (2026-10-24).

## Context

The AI ecosystem of the fleet (warehouse-ops-agent first) reaches a bounded
context through MCP, not through bespoke REST clients: every sibling context
that exposes an HTTP API also ships an MCP server over the same reads.
vendor-connectivity had none. The service already answers seven GETs through
its REST adapter (`pollPurchaseOrders`, `getPurchaseOrder`, `listVendorNodes`,
`getNode`, `getScorecard`, `getTransaction`, `getVendor`), so an MCP adapter
is another driving adapter over the same application queries, not new domain.

## Decision

- **A second driving adapter, a separate binary.** `internal/adapters/inbound/mcp`
  is built on the official MCP Go SDK (`github.com/modelcontextprotocol/go-sdk`
  v1.8.0) and served over Streamable HTTP only (no stdio, no SSE) by `cmd/mcp`,
  mounted at `/mcp` and `/` with an open `/healthz`. It is a second deployable
  beside `cmd/api`; the Helm chart gains an `mcp` Deployment, Service and
  (off-by-default) HTTPRoute.
- **Seven read tools, one per GET of the Vendor API, nothing else.**
  `poll_purchase_orders(vendorId, nodeId?, createdAfter?, nextToken?, limit?)`,
  `get_purchase_order(vendorId, poNumber)`, `list_vendor_nodes(vendorId)`,
  `get_vendor_node(vendorId, nodeId)`, `get_node_scorecard(vendorId, nodeId)`,
  `get_transaction(vendorId, transactionId)` and `get_vendor(vendorId)`.
  Arguments and results are lowerCamelCase like the REST contract; typed
  errors carry the REST problem slug (`not-found`, `validation-failed`,
  `node-not-owned-by-vendor`, `upstream-unavailable`, `upstream-refused`) as
  the prefix of the tool error, so a client sees one vocabulary over both
  surfaces. `poll` is a read here, exactly as the Vendor API's own operationId
  is. The submissions, the vendor lifecycle and the node registrations stay
  on REST.
- **Read-only is structural.** Reads and writes share the application
  `Service`, so the adapter holds a `Reads` interface naming EXACTLY the
  seven read methods the REST adapter's GET routes call — through it no write
  use case can even be named. `TestReadsSurfacePinsTheReadMethods` pins the
  interface, `TestNoWriteUseCaseIsNamed` pins that no write use case appears
  in the adapter's sources, `TestToolSurface` pins the exact tool set (budget
  7), the read-only annotations and the absence of write verbs in tool names,
  and `TestToolRegistryGolden` pins the whole advertised registry byte for
  byte. The integration test proves the binary reads, on a real Postgres,
  what the write use cases persisted and adds no outbox row.
- **No Kafka, no loops.** `cmd/mcp` never starts the outbox relay, the
  submission loop, the order-expiry sweep, the retention sweep or the
  local-copy consumers, and never dials Kafka (the chart renders no
  `KAFKA_BROKERS` or consumer group on the mcp pod). Those stay in `cmd/api`.
  The one outbound call a read can make is the scorecard relay to
  node-performance, the same synchronous call the REST GET makes.
- **No auth.** No keys, no bearer checks (the fleet-wide revert of
  2026-09-11; `TestNoAuthMiddlewareReintroduced`). Access control is the
  in-cluster ClusterIP boundary; Kong rewrites `/api/mcp/vendor-connectivity`
  to the SDK's canonical `/mcp`.
- **Boot.** `DATABASE_URL` is mandatory (no in-memory mode), plus the four
  neighbour URLs the shared Service reads; migrations and the first ping are
  wrapped in the boot retry, and `MIGRATIONS_DATABASE_URL` carries the direct
  DSN for the advisory lock behind PgBouncer, exactly as `cmd/api` does.

## Consequences

- Agents can answer "which orders are open for this vendor, what does node X's
  scorecard look like, did this acknowledgement's transaction succeed" without
  REST glue.
- Adding a tool is a reviewed act: the tool budget, the curated list and the
  golden file all change together (an amendment of this ADR).
- Acknowledging an order, feeding inventory and confirming shipments stay
  REST operations with idempotency keys; the MCP surface cannot write.
