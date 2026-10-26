# Architecture decision records

The decisions of `vendor-connectivity`, in the order they were taken. The ADR bodies
are immutable; a changed decision gets a new ADR that supersedes the old one.
The *Status* column is copied from each ADR's own `## Status` section.

| ADR | Title | Status |
| --- | --- | --- |
| [0001](./0001-vendor-connectivity-bounded-context.md) | Vendor Connectivity is a bounded context of the retail network | Accepted (bootstrap) |
| [0002](./0002-aggregates-and-invariants.md) | the VendorAccount, PurchaseOrder and Submission aggregates, their invariants and the assumptions the model left open | Accepted (2026-10-23) |
| [0003](./0003-consumed-contracts-and-local-read-models.md) | consumed contracts, local read models, synchronous calls and relationships | Accepted (2026-10-23) |
| [0004](./0004-cloudevents-envelope-and-type-catalogue.md) | CloudEvents envelope and type catalogue | Accepted (2026-10-23) |
| [0005](./0005-inbound-shipments-receipts-and-catalog-packaging.md) | inbound shipments, inbound receipts and catalogue packaging | Accepted (2026-10-24) |
| [0006](./0006-mcp-server-adoption.md) | a read-only MCP server | Accepted (2026-10-24) |
| [0007](./0007-console-remote.md) | a warehouse-console remote (web MFE) | Accepted (2026-10-24) |
| [0008](./0008-fleet-observability-standard.md) | Adopt the fleet observability standard (logs, metrics, traces) | Accepted (2026-10-26) |
