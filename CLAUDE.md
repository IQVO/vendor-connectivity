# vendor-connectivity

harness-template: v4

Retail network, bounded context Vendor Connectivity (Supporting, partner tier). The Open Host Service of the network for vendors that operate fulfillment nodes: owns VendorAccount, PurchaseOrder and Submission, publishes the Vendor API, consumes node-topology, offer-availability and order-sourcing events and makes four synchronous in-org calls.
Design source of truth: IQVO/retail-network-docs `model/model.yaml` (context vendor-connectivity) plus the orchestrator's contracts-delta. This service belongs to the retail network, not the warehouse-systems fleet.
This project is a domain-driven design study, not associated with any company: never name a real company, brand or product in code, comments, ADRs, specs, tests or fixtures (README `## Disclaimer`).

## Commands
`make check` (fmt, vet, build, lint, unit + handler + godog scenarios; the pre-push gate), `make coverage` (>= 90% over internal/domain + internal/application), `make mutation` (gremlins per internal/domain/<pkg>, thresholds 99 below the measured 100%),
`make arch-test`, `make integration` (testcontainers Postgres + Kafka, needs Docker; build tag `integration`), `make vuln`. Lint: `golangci-lint run --allow-parallel-runners ./...`.
Guides: `python3 scripts/harness/repo_lint.py`, `python3 scripts/harness/guide_lint.py` must stay clean. Never gate a commit on a piped command.

## Layout (hexagonal)
`internal/domain/{shared,vendor,purchaseorder,submission,network}` pure Go; `internal/application/{ports (interfaces only),gateway (neighbour call shapes),repository (filters),requests,views,usecases,outbox}`;
`internal/adapters/inbound/{http (chi),kafka (consumers, inbox, DLQ)}`; `internal/adapters/outbound/{postgres,kafka,outbox,httpclient,clock,ids,memory,repocontract}`; `internal/adapters/kafka/cloudevents` (the ONLY envelope builder);
`internal/testing/{pgtest,kafkatest}` (testcontainers helpers); `cmd/api` (composition root, `run(ctx)`); `features/` (godog scenarios) run by `internal/bdd` (never a .go file at the repo root). Migrations are embedded in `internal/adapters/outbound/postgres/migrations`.

## Runtime rule
Real PostgreSQL and Kafka only. `DATABASE_URL`, `KAFKA_BROKERS`, `NODE_CONSUMER_GROUP`, `LISTING_CONSUMER_GROUP`, `SOURCING_CONSUMER_GROUP`, `NODE_TOPOLOGY_URL`, `OFFER_AVAILABILITY_URL`, `SHIPPING_AND_TRACKING_URL` and `NODE_PERFORMANCE_URL` are mandatory (boot error naming the variable, raised before the listener binds; `MIGRATIONS_DATABASE_URL` is an optional direct DSN).
The outbox relay, the three consumers and three loops (submission processor, expiry sweeper, retention) are always on; the process exits non-zero when one ends on its own. Simulation knobs: `TX_PROCESSING_DELAY`, `TX_FAILURE_RATE`, `PO_REDELIVERY_RATE`, `RATE_LIMIT_RPS`.
`/healthz` = liveness; `/readyz` = drain gate + Postgres ping + Kafka metadata request with an EMPTY non-nil topic list (fresh connection per check).
Shutdown: /readyz 503, `SHUTDOWN_DRAIN_DELAY` (5s), drain HTTP, stop consumers and loops, stop relay, close pool. cmd/** never imports internal/adapters/outbound/memory or repocontract. No authentication on REST. See docs/runtime.md.

## REST
chi router implementing every operation of apis/openapi.yaml (12 `/vendor/*` routes plus the `/vendors` account routes) plus /healthz, /readyz. Every write takes `Idempotency-Key` (table `idempotency_keys`, key = (route, key), 24h, replays the first response, same key + other request = 422 `idempotency-key-reused`).
Every write returns 202 + `transactionId` except a label (200, documented exception). `vendorId` is in every write body and a required query parameter on reads (consistency check, not security). A suspended vendor's writes are 409 `vendor-suspended`.
NUL: 404 in the path, 400 `malformed-request` elsewhere. Errors are RFC 9457 problem+json, `type` = slug: not-found, illegal-transition, concurrent-update, validation-failed + one kebab-case slug per model Err (internal/adapters/inbound/http/errors.go).

## Events: CloudEvents 1.0 is MANDATORY
type `com.retailnetwork.partner.vendor-connectivity.<entity>.<Event>` (entity: vendoraccount, purchaseorder, submission); source `/retailnetwork/vendor-connectivity`; dataschema `urn:retailnetwork:vendor-connectivity:<events|analytics>:<Event>:v1`;
ten events on `retailnetwork.vendor-connectivity.events` except `PurchaseOrderCancelled` (ONLY `.analytics`); subject and Kafka key = aggregate id; header `content-type: application/cloudevents+json; charset=UTF-8`.
Consumes (inbox `processed_events`, full-type dispatch, DLQ `<topic>.dlq`): node-topology NodeRegistered/NodeOperationalProfileDeclared/NodeActivated/NodeSuspended/NodeRetired, offer-availability ListingCreated, order-sourcing FulfillmentReleasedToNode/FulfillmentCancelled.
Use internal/adapters/kafka/cloudevents only. Transactional outbox: aggregates + outbox rows in ONE Postgres transaction, id minted once at append, at-least-once delivery. No `warehouse.*` topic, ever.
A new event: domain struct + apis/asyncapi.yaml + ADR 0004 `type:` line in the SAME commit + encoder payload + golden test (.claude/skills/how-to-add-an-integration-event).

## Domain
Aggregates, invariants and assumptions A1-A27 are in docs/adr/0002; consumed contracts and read models in 0003; catalogue in 0004. poNumber is deterministic from (assignmentId, releaseNo); ackBy = min(issuedAt + ackWindow, requiredShipBy - handlingTime); an acknowledgement covers every line (fill-or-kill); cancel only while ISSUED.

## Workflow
GitFlow: feature/* -> PR into develop, CI green before merge, never push develop or main. lefthook: pre-commit fmt/vet/lint, pre-push `make check`. Commit and push after each step.
Open follow-up: the Helm chart (docs/before-first-release.md rows 5-9).
