---
name: how-to-add-an-integration-event
description: Publish or consume a cross-service Kafka event in this service: CloudEvents 1.0 type naming, AsyncAPI, the type catalogue ADR, the encoder, the transactional outbox, the inbox consumers. Use when touching internal/adapters kafka or outbox code, a domain event, or apis/asyncapi*.yaml.
---

# How to add an integration event

vendor-connectivity publishes ten events and consumes eight (ADR 0003, ADR 0004). The envelope is
CloudEvents 1.0 only; read `.claude/rules/integration-events.md` and
`.claude/rules/fleet/cloudevents.md` first. The worked example is `VendorShipmentConfirmed`.

## Publishing a new event

1. **Domain event.** A typed struct in `internal/domain/<pkg>/events.go`
   (`purchaseorder.VendorShipmentConfirmed`): embeds `shared.Header`, fields exactly the model's
   `data` list, methods `EventName()`, `Entity()` (`vendoraccount`, `purchaseorder`, `submission`;
   lowercase, no separators) and `SubjectID()` (the aggregate id). The aggregate command returns it.
2. **Contract, one commit.** Add the message and its `data` schema to `apis/asyncapi.yaml`, AND one
   `type: com.retailnetwork.partner.vendor-connectivity.<entity>.<Event> stream: events` line to
   `docs/adr/0004-cloudevents-envelope-and-type-catalogue.md`. `TestEventCatalogueMatchesContract`
   fails when the two disagree, so the push fails when they are not in the same commit. Lint:
   `npx --yes @stoplight/spectral-cli lint apis/asyncapi.yaml --ruleset .spectral.asyncapi.yaml`.
   Evolution is additive only; a breaking change is a new `:v2` dataschema.
3. **Encoder.** In `internal/adapters/outbound/kafka/encoder.go` add a `case` in `payloadFor` (or
   `purchaseOrderPayload`) with the payload struct (camelCase json tags, instants through `ts`,
   arrays never null). The envelope itself is built by `internal/adapters/kafka/cloudevents` only;
   never hand-build one. Add the event to `published()` in `encoder_test.go` and a row to the table
   in `golden_test.go` (an exact-JSON test of every attribute and the payload).
4. **Publish from the use case** inside the same `ports.UnitOfWork` as the aggregate `Save`: the
   aggregate row and the outbox row commit or roll back together. Never publish after the commit and
   never call Kafka from a use case.
5. **Stream.** `.events` for integration events; an analytics-only event (today only
   `PurchaseOrderCancelled`) goes ONLY to `.analytics` (the type switch in `Encoder.Encode`) with
   `dataschema` `...:analytics:<Event>:v1`. The relay (`internal/adapters/outbound/outbox/relay.go`)
   already drains every row.
6. **Prove it.** `go test -tags=integration ./internal/adapters/outbound/outbox/ ./cmd/... -race -count=1`
   reads the events back from a real broker; extend `cmd/api/e2e_integration_test.go` when the event
   is part of a REST flow.

## Consuming a new event

Re-read the producer's contract first:
`git -C ~/warehouse-systems/<producer> show origin/feature/service:apis/asyncapi.yaml`. Then:

1. Supersede ADR 0003 with a new ADR (consumed contracts and the local read model), and add a
   `receive` operation with only the fields you rely on to `apis/asyncapi.yaml`.
2. Add the `Type...` constant (byte-identical to the producer's `type`) and a tolerant payload struct
   to `internal/adapters/inbound/kafka/consumers.go`, a handler in the matching `...Handlers` map, and
   a use case that records the inbox row (`ports.ProcessedEvents`) in the same transaction as its
   writes. Dispatch is on the FULL type; unknown fields are ignored.
3. A new topic needs its own `*_CONSUMER_GROUP` env var (boot error naming it, started in
   `cmd/api/main.go`), never a literal group id.
4. Test: `internal/adapters/inbound/kafka/consumers_test.go` (handler, redelivery, skippable and
   malformed messages), a scenario in `features/`, and the real-broker path in
   `cmd/api/e2e_integration_test.go`.

## Verify before opening the PR

`make check`, `go test ./internal/architecture/ -count=1` (catalogue, CloudEvents-only, relay cap),
the spectral lint of the AsyncAPI, and the integration command above.
