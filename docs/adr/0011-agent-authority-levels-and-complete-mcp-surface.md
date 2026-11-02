# ADR 0011: Agent authority levels and complete MCP surface

## Status

Accepted (2026-11-02). Follows [ADR 0006](./0006-mcp-server-adoption.md) (the
read-only MCP charter this keeps) and [ADR 0009](./0009-a2a-inbound-adapter.md)
(the A2A skills that carry the same levels). Applies the fleet's Agentic
Standard v2 (warehouse-docs `docs/architecture/agentic-standard.md`, rule
`agents/fleet/agent-authority.md`) to this context; mirrors the reference
rollout in [workforce-management
ADR-0041](https://iqvo.github.io/workforce-management/docs/adr/0041-agent-authority-levels-and-attributable-assignments).

## Context

The fleet is standardising what an agent may do on each bounded context: every
MCP tool and every A2A skill must declare exactly one **authority level** so a
caller knows what gating to apply before it calls — `L1` reads facts, `L2`
computes a recommendation that changes nothing, `L3` mutates state, `L4`
mutates inside an envelope an ADR grants (there is no `L5`). The declaration
is part of the wire contract: MCP tools carry it in `tools/list` under the
`com.warehouse/authority` `_meta` key; A2A skills carry it as an
`authority:<L>` tag on the Agent Card.

vendor-connectivity's agent surface is already entirely read-only (ADR 0006:
the MCP adapter holds a `Reads` interface that names exactly the nine read
methods behind the REST `GET` routes; ADR 0009's agent is read-only for the
same reason). Second, the fleet standard asks an MCP server to be a complete
agent surface — tools **plus** at least one resource and at least one prompt —
and the Streamable HTTP handler to be stateless, answering the 2026-07-28
protocol revision's per-request handshake while legacy `initialize` clients
keep working.

## Decision

1. **Authority levels (AS-1).** Every one of the nine MCP tools is declared
   `L1` — each is a pure read of this context's own state — via
   `Meta: authorityMeta(authorityL1)` under the `com.warehouse/authority` key
   (the consts `authorityL1..L4` and the `authorityMeta` helper are pinned by
   the fleet sensor). Both A2A skills carry the same level as the tool they
   front, as the tag `authority:L1`. No tool or skill here is `L2` (nothing
   computes a recommendation), `L3` or `L4` (the charter has no writes), and
   the golden registry pins the level of every tool.

2. **Complete surface (AS-3), still read-only.** The server gains exactly one
   resource and one prompt, both `L1` by nature:
   - `warehouse://vendor-connectivity/vendors/{vendorId}/nodes` — the vendor
     node roster, a scoped read model built from the existing
     `ListVendorNodes` read use case (the local node-topology copy of
     ADR 0003 behind `GET /vendor/nodes`), JSON text, bounded to one vendor's
     nodes; never a dump of a table.
   - the `triage_vendor_orders` prompt — the operator SOP for triaging one
     vendor's purchase orders (account state, node roster, open orders
     against their acknowledgement windows, in-flight transactions, what to
     escalate). It takes one typed, required argument (`vendorId`), names the
     read tools but never calls them, and encodes no hidden write: every
     vendor write stays a REST operation a human decides on.

3. **Stateless transport (AS-4).** The Streamable HTTP handler is built with
   `&mcp.StreamableHTTPOptions{Stateless: true}`: no `Mcp-Session-Id` is
   issued, nothing relies on cross-call session state (every tool call is
   scoped by its own `vendorId`), and the server answers the 2026-07-28
   revision's per-request handshake (`server/discover`) while a legacy
   `initialize` still negotiates a legacy revision.

4. **Mechanical gates.** The fleet's static sensor
   (`internal/architecture/mcp_authority_fitness_test.go`, copied verbatim
   from the harness template) fails the build when a tool or skill loses its
   declaration, when a read tool is declared `L3+`, when an undeclared `L4`
   appears, or when the surface regresses to tools only. Raw-POST
   `stateless_test.go` pins the wire facts, and the A2A package keeps its own
   card-level authority test.

## Consequences

- `tools/list` answers every tool with an `_meta` authority entry; the golden
  registry (`testdata/tool_registry.golden.json`) carries it per tool, so any
  level change is a reviewed golden diff.
- Agent clients can gate on the level without knowing this context: everything
  here is `L1`, and the one live relay (`get_node_scorecard`, from
  node-performance) stays `L1` — it returns data, it does not compute a
  proposal for the caller.
- The annotations of every tool are now spelled inline
  (`&mcp.ToolAnnotations{ReadOnlyHint: true, ...}`) instead of through a
  shared variable: same values, byte-identical wire output, but provable to
  the static sensor.
- Legacy session-based MCP clients (the ops-agent's SDK generation) are
  unaffected: `initialize` keeps answering `200`.
- A future write tool would arrive as `L3` through a new ADR amending ADR
  0006's read-only charter — nothing in this ADR pre-approves it.
