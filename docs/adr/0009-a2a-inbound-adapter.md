# ADR 0009: A2A inbound adapter (a read-only third driving adapter)

## Status

Accepted (2026-10-30). Follows [ADR 0006](./0006-mcp-server-adoption.md) (the
read-only MCP adapter this one is modelled on), [ADR 0004](./0004-cloudevents-envelope-and-type-catalogue.md)
(CloudEvents on every Kafka message) and [ADR 0008](./0008-fleet-observability-standard.md)
(the telemetry the new binary shares). Mirrors the fleet's reference rollout in
[wes-work-planning ADR-0046](https://iqvo.github.io/wes-work-planning/docs/adr/0046-a2a-inbound-adapter).

## Context

The fleet is adopting the Agent2Agent (A2A) protocol so that one bounded
context can **delegate** a goal to another context's agent. vendor-connectivity
already exposes its reads over REST and MCP. Three transports now carry three
different things and must not overlap:

| Transport | Carries |
| --- | --- |
| Kafka + CloudEvents | **facts** between contexts (ADR 0004) |
| MCP | an agent's **tools** (ADR 0006) |
| A2A | **delegation** of a goal to another context's agent |

The MCP charter of this context is entirely read-only (ADR 0006: the adapter
holds a `Reads` interface and `TestNoWriteUseCaseIsNamed` pins that no write
use case is named). The fleet's write gate says an A2A skill may change state
only if the context's MCP surface already offers that write. It does not, so
**this agent is read-only** and never widens what the context exposes.

## Decision

Add `internal/adapters/inbound/a2a`, a third driving adapter over the **same**
application `Service` as the REST and MCP adapters, built on the official
`a2aproject/a2a-go/v2` SDK (v2.7.0) and served by a new `cmd/a2a` binary
(JSON-RPC 2.0 over HTTP, port **8093**, the Agent Card at
`/.well-known/agent-card.json`, the endpoint mounted at `/` and `/a2a`, an open
`/healthz`).

### Two curated read skills, not one per endpoint

| Skill | Kind | What it does |
| --- | --- | --- |
| `get_purchase_order` | read | where one purchase order released to a vendor stands: state, windows (`issuedAt`, `requiredShipBy`, `ackBy`), lines, and once they exist the acknowledgement, labels, tracking numbers and `shippedAt`. Never a ship-to address. |
| `get_node_scorecard` | read | how one of the vendor's nodes is performing: rolling 7d/30d/90d scorecards relayed live from node-performance (the node must belong to the vendor). |

Both are derived from the MCP tools of the same name (`get_purchase_order`,
`get_node_scorecard`) and call the same two application queries the REST GETs
call. The other seven MCP reads (polling, inbound shipments, nodes,
transactions, the vendor account) stay MCP/REST: they are paging or lookup
tools, not goals a delegating agent states.

### Read-only is structural

The adapter holds `Reads`, an interface naming exactly `GetPurchaseOrder` and
`GetScorecard`, so no write use case can even be named through it. The
architecture fitness tests pin it (`internal/architecture/a2a_fitness_test.go`):
the adapter depends only on the application and domain layers and nothing
depends on it; `Reads` has exactly those two methods; the adapter names no
`usecases.*` identifier and `cmd/a2a` only builds the shared `Service`
(`New`, `Deps`, `Config`); and no non-test source imports the outbound A2A
client (this context hosts an agent, it does not delegate outward). Submissions,
the vendor lifecycle, node registrations and inbound receipts stay on REST, on
the consumers and on the loops of `cmd/api`.

### Message-only, answered in-band

Both skills are transactional request/response reads, so the agent is
message-only (no Task lifecycle). The input is a data part
`{"skill":"<id>","arguments":{...}}` (or a text part holding the same JSON).
Domain rejections and malformed requests are **answered** as RFC 7807-shaped
`problem`s inside the message envelope, never raised as JSON-RPC transport
errors, with the **same slug and status as the REST adapter's problem
catalogue**: `404 not-found` and `404 node-not-owned-by-vendor`, `422
validation-failed`, `503 upstream-unavailable`, `502 upstream-refused`, `500
internal-error`. Request-shape failures (unknown skill, missing or unknown
argument, malformed JSON) are `400`. A NUL in an identifier is `404 not-found`,
as on REST. The details of the infrastructure-facing classes are adapter-owned
text, so an upstream URL or a SQL error never reaches the calling agent. The
409 classes of the REST catalogue belong to writes this agent does not offer.

### Strict input

Arguments are decoded with `DisallowUnknownFields` and trailing data is
rejected, so a misspelled argument cannot be silently dropped. Go's
`encoding/json` matches names case-insensitively, so only genuinely unknown
names are refused.

### Facts still travel on Kafka

A2A never becomes a data-sync channel. `cmd/a2a` writes nothing, so it has no
outbox, no relay, no consumers, no loops and never dials Kafka: the chart renders
no `KAFKA_BROKERS`, `EVENT_PUBLISHER` or consumer group on the a2a pod. Those
stay in `cmd/api`.

### Unauthenticated, like REST and MCP

The card declares no security schemes: the surface is unauthenticated by the
fleet-wide decision of 2026-09-11 (`TestNoAuthMiddlewareReintroduced`). Schemes
are added to the card, not a redesign, if that decision is ever reversed.

### Boot and deploy

`cmd/a2a` clones `cmd/mcp`'s composition (real Postgres, the four neighbour
URLs, the shared telemetry). `A2A_AGENT_URL` is mandatory: the absolute http(s)
URL the card advertises, refused at boot otherwise, before the listener binds.
`A2A_ADDR` defaults to `:8093`. The Helm chart gains an `a2a` component
(Deployment, Service, off-by-default HTTPRoute), **default off**, with
`A2A_AGENT_URL` defaulting to the release's in-cluster Service DNS
(`http://<release>-a2a.<namespace>.svc.cluster.local:8093/`) and overridable by
`a2a.agentUrl`. Kong exposes `/api/a2a/vendor-connectivity`, rewritten to `/`,
so the card stays at `/api/a2a/vendor-connectivity/.well-known/agent-card.json`.

## Consequences

- A fourth deployable (`/app/a2a`) in the same image; the Dockerfile already
  builds every `cmd/*`.
- The surface cannot write. Adding a write skill is a reviewed act: it first
  needs the same write on the MCP surface (an amendment of ADR 0006), then an
  amendment of this ADR, the outbox integration test and a change of the
  fitness tests.
- `get_node_scorecard` makes the same synchronous call to node-performance as
  the REST GET and the MCP tool; when node-performance is down the answer is a
  `503 upstream-unavailable` problem.
- Port 8093 is the fleet's A2A port (8080 api, 8090 mcp).
