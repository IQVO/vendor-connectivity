---
id: ddd-artifacts
title: DDD artifacts (ddd-crew)
sidebar_label: DDD artifacts index
sidebar_position: 10
---

# DDD artifacts (ddd-crew)

The strategic and tactical design of `vendor-connectivity`, drawn with the
[ddd-crew](https://github.com/ddd-crew) tools plus UML and ER diagrams. The pages started from the
aggregator's pack for this context (`network/docs/contexts/vendor-connectivity/` in
[warehouse-docs](https://iqvo.github.io/warehouse-docs/)) and were then checked against this repository's code: every
identifier, route, event type and table was verified, the proposals the pack still carried were replaced
by what is built, and every diagram carries a `Source:` line naming the files it came from. Nothing here
was captured from a live run.

| Artifact | ddd-crew tool / notation | What it shows |
| --- | --- | --- |
| [Core domain chart](/docs/ddd/core-domain-chart) | [Core Domain Charts](https://github.com/ddd-crew/core-domain-charts) | Where this context sits on differentiation against complexity, and why it is Supporting. |
| [Bounded context canvas](/docs/ddd/bounded-context-canvas) | [Bounded Context Canvas v5](https://github.com/ddd-crew/bounded-context-canvas) | Purpose, roles, every inbound and outbound message, business decisions, and how the open questions were settled. |
| [Context map](/docs/ddd/context-map) | [Context Mapping](https://github.com/ddd-crew/context-mapping) | This context's slice of the retail network's map: the pattern of every edge. |
| [Aggregate design canvas](/docs/ddd/aggregate-design-canvas) | [Aggregate Design Canvas v1.1](https://github.com/ddd-crew/aggregate-design-canvas) | `VendorAccount`, `PurchaseOrder` and `Submission`: states, invariants, commands, events, concurrency. |
| [Domain message flow](/docs/ddd/domain-message-flow) | [Domain Message Flow Modelling](https://github.com/ddd-crew/domain-message-flow-modelling) | Accepted and shipped, refused and reassigned, onboarding, and a failed or rate-limited transaction. |
| [EventStorming](/docs/ddd/eventstorming) | [EventStorming glossary and cheat sheet](https://github.com/ddd-crew/eventstorming-glossary-cheat-sheet) | Process boards with the sticky-note colours, plus hotspots marked resolved or open. |
| [Ubiquitous language](/docs/ddd/ubiquitous-language) | Glossary | Every term with the Go identifier that implements it, and the fleet translation table. |
| [Class diagrams](/docs/ddd/class-diagram) | UML class diagram | Domain types, events, use cases and read models, and the hexagonal ports and adapters. |
| [Entity-relationship diagram](/docs/ddd/entity-relationship) | ER diagram | The Postgres schema created by the two migrations: aggregate tables, read models, outbox, inbox and idempotency keys. |
| [Sequence diagrams](/docs/ddd/sequence-diagrams) | UML sequence diagram | One sequence per command use case, with error, version-conflict and idempotency branches. |
| [Domain events](/docs/ddd/domain-events) | Event catalogue | Every event published and consumed: full CloudEvents type, topic, key, payload, producer, consumers. |

Related pages outside the pack: [Use cases](/docs/ddd/use-cases),
[Subdomain classification](/docs/ddd/subdomain-classification),
[Integration](/docs/ecosystem/integration) and the
[event catalogue](/docs/api-reference/events).

:::note[Sources of truth]
The code wins. Domain rules come from `internal/domain/vendor`, `purchaseorder`, `submission`, `network`
and `inbound`, behaviour from `internal/application/usecases`, the envelope from
`internal/adapters/outbound/kafka` (the encoder) and the consumers in `internal/adapters/inbound/kafka`,
the schema from `internal/adapters/outbound/postgres/migrations/`, the REST contract from
`apis/openapi.yaml`, the event contract from `apis/asyncapi.yaml`, and decisions from the ADRs in
`docs/adr/`. Where a page and the code disagree, the page is wrong.
:::

:::info Not in this pack
The read-only MCP server ([ADR 0006](/docs/adr/0006-mcp-server-adoption), [tools](/docs/mcp/tools)),
the A2A adapter ([ADR 0009](/docs/adr/0009-a2a-inbound-adapter)) and the web remote (`web/`,
[ADR 0007](/docs/adr/0007-console-remote)) are built, but they are read surfaces over the model and add
no aggregate, entity or event of their own, so the pack has no pages for them. The
aggregator's business-context page has no counterpart here; its content is folded into the
[Introduction](/docs/overview/introduction).
:::
