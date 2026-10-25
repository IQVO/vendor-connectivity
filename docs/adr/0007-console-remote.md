# ADR 0006: a warehouse-console remote (web MFE)

## Status

Accepted (2026-10-24).

## Context

The operations front end of the fleet is the warehouse-console: a Module
Federation host that lazy-loads one remote per bounded context under its own
splat route, serves them through the Nginx web gateway at
`/mfes/<context>/`, and publishes the runtime API origin through
`window.__WAREHOUSE_CONFIG__` (from `/config.json`) so one image works in
every environment. vendor-connectivity had no remote: operators watched
purchase orders and vendor nodes through raw REST calls.

## Decision

- **`web/` is the `vendor_connectivity_mfe` remote.** A Vite +
  `@module-federation/vite` SPA (React 19, react-router 7), built as a static
  bundle served by its own unprivileged nginx pod. It exposes exactly
  `./App` (default export, no props) and shares `react`, `react-dom`,
  `react-router-dom` and `@warehouse/ui-kit` as singletons with the shell.
  The production asset base is `/mfes/vendor-connectivity/`; dev and preview
  run standalone on :4021 (strictPort; the fleet's 5181-5197, 4010 and 4011
  are taken). The Vite config stays a plain OBJECT, never the callback form —
  a callback cannot be merged by `mergeConfig`, which has killed test suites
  elsewhere in the fleet.
- **Three read-only screens over the Vendor API's GETs.** The purchase-order
  list (poll with a vendor id, an optional node filter and `nextToken`
  paging), the order detail (windows, lines, the acknowledgement and its
  transaction outcome, labels and tracking numbers — never a ship-to
  address) and the nodes screen (the vendor's fulfilment nodes plus the
  selected node's rolling scorecard relayed from node-performance). Every
  call is a GET with no custom header; the console changes nothing.
- **The API base is resolved at runtime.** `src/config.ts` turns
`window.__WAREHOUSE_CONFIG__.apiOrigin` into
`${apiOrigin}/api/vendor-connectivity` and refuses to fall back to a
  developer port in a production build: a silent fallback would be a deployed
  console quietly talking to nothing. The screens read only this context's
  own API — no sibling context, no MCP server.
- **Deployment.** A second image (`web/Dockerfile`, the fleet's two-stage
  uikit named-context build), its own `frontend` Deployment and Service in
  the chart (default OFF, like every remote), ClusterIP only: the Nginx web
  gateway is the single host-exposed frontend endpoint, Kong never routes to
  it, and the api Service never selects it (`component=frontend` vs
  `component=api`, proven by the chart's selector test). CI gains a `web`
  job (oxlint, `tsc -b`, vitest with Testing Library, `vite build`) that
  checks out warehouse-ui-kit beside the repo exactly as the Dockerfile's
  named context does.

## Consequences

- Operators see a vendor's order book, one order's full lifecycle and a
  node's scorecards without curl; the remote mounts in the console under
  `/vendor-connectivity/*` and runs standalone on :4021.
- The remote's API surface stays the REST GETs: a screen needing a new read
  first needs a REST endpoint (and, for agents, its MCP twin) — one read,
  three surfaces, by design.
- Writes stay out of the console on purpose: acknowledging an order is a
  vendor-system operation with idempotency semantics this remote does not
  own.
