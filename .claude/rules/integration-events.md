---
paths:
  - "internal/adapters/**/kafka/**"
  - "internal/adapters/outbound/outbox/**"
  - "apis/asyncapi*"
---

# Cross-service integration events (Kafka)

vendor-connectivity PUBLISHES ten events and CONSUMES eight (ADR 0003, ADR 0004). Published:
`retailnetwork.vendor-connectivity.events`, plus `retailnetwork.vendor-connectivity.analytics`
for `PurchaseOrderCancelled` ONLY. Consumed: `retailnetwork.node-topology.events`,
`retailnetwork.offer-availability.events`, `retailnetwork.order-sourcing.events`. Never produce
or consume a `warehouse.*` topic.

## Events: CloudEvents 1.0 is MANDATORY

Every Kafka message this service produces or consumes is a CloudEvents 1.0 event in structured
content mode. This is a hard rule, not a preference:

- No flat envelope (`event_id`/`event_type`/`occurred_at`), no dual-write, no dual-read, no
  envelope toggle env var. `internal/architecture/fitness_test.go`'s
  `TestNoEventEnvelopeToggleOrFlatEnvelope` fails CI on any of them.
- Build and validate the envelope with `github.com/cloudevents/sdk-go/v2/event` through ONE helper
  package, `internal/adapters/kafka/cloudevents`. Transport stays `segmentio/kafka-go`; no
  hand-rolled CloudEvent structs. The only producing caller is the encoder
  `internal/adapters/outbound/kafka/encoder.go`.
- Every message carries the Kafka header `content-type: application/cloudevents+json; charset=UTF-8`.
  Message key = aggregate id (`vendorId`, `poNumber`, `transactionId`), `kafkago.Hash{}` balancer
  (`internal/adapters/outbound/kafka/writer.go`).
- Attributes: `specversion=1.0`; `id` (UUID v4 minted ONCE per event at outbox append and stored in
  `outbox_events.event_id`, so a redelivery carries the same id); `source=/retailnetwork/vendor-connectivity`;
  `type`; `subject` (the aggregate id, never empty); `time` (the domain occurred-at, UTC);
  `datacontenttype=application/json`;
  `dataschema=urn:retailnetwork:vendor-connectivity:<stream>:<EventName>:v1`. No custom extension
  attributes without an ADR.
- `type` = `com.retailnetwork.partner.vendor-connectivity.<entity>.<EventName>` with entity
  `vendoraccount`, `purchaseorder` or `submission` (lowercase, no separators). A breaking payload
  change is a new `:v2` dataschema, never a mutation of v1; evolution is additive only.
- Payloads follow the wire conventions of contracts-delta: ids are strings, instants RFC 3339 UTC,
  durations integers ending in `Minutes`, enums UPPER_SNAKE, optional fields omitted, arrays always
  present.
- Consumers decode with `cloudevents.Decode` (validates), dispatch on the FULL `type` string, ignore
  unknown types and unknown fields, dedupe on `id`, and skip (WARN, commit past) anything that is
  not a valid CloudEvent without claiming the inbox; they never fall back to a legacy shape
  (`TestSkippableMessagesReturnNil`).
- Tests: a golden exact-JSON test per published type
  (`internal/adapters/outbound/kafka/golden_test.go`), the catalogue-vs-contract sensor
  (`TestEventCatalogueMatchesContract`), `TestEncodedEventsMatchTheAsyncAPI`, consumer tests in
  `internal/adapters/inbound/kafka/consumers_test.go`, and the Kafka proof against a real broker
  (`internal/adapters/outbound/outbox/relay_integration_test.go`, `cmd/api/e2e_integration_test.go`),
  all via testcontainers.

### Published types

`subject` and Kafka key is the aggregate id.

| `type` | stream | `subject` |
| --- | --- | --- |
| `com.retailnetwork.partner.vendor-connectivity.vendoraccount.VendorOnboarded` | events | vendorId |
| `com.retailnetwork.partner.vendor-connectivity.vendoraccount.VendorSuspended` | events | vendorId |
| `com.retailnetwork.partner.vendor-connectivity.vendoraccount.VendorReinstated` | events | vendorId |
| `com.retailnetwork.partner.vendor-connectivity.purchaseorder.PurchaseOrderIssued` | events | poNumber |
| `com.retailnetwork.partner.vendor-connectivity.purchaseorder.PurchaseOrderAccepted` | events | poNumber |
| `com.retailnetwork.partner.vendor-connectivity.purchaseorder.PurchaseOrderRejected` | events | poNumber |
| `com.retailnetwork.partner.vendor-connectivity.purchaseorder.PurchaseOrderExpired` | events | poNumber |
| `com.retailnetwork.partner.vendor-connectivity.purchaseorder.PurchaseOrderCancelled` | analytics | poNumber |
| `com.retailnetwork.partner.vendor-connectivity.purchaseorder.VendorShipmentConfirmed` | events | poNumber |
| `com.retailnetwork.partner.vendor-connectivity.submission.SubmissionProcessed` | events | transactionId |

### Consumed types

Constants and tolerant payload structs: `internal/adapters/inbound/kafka/consumers.go`.

| `type` | topic | handler (use case) |
| --- | --- | --- |
| `com.retailnetwork.logistics.node-topology.node.NodeRegistered` | node-topology.events | `OnNodeRegistered` |
| `com.retailnetwork.logistics.node-topology.node.NodeOperationalProfileDeclared` | node-topology.events | `OnNodeProfileDeclared` (G4: `handlingTimeMinutes`) |
| `com.retailnetwork.logistics.node-topology.node.NodeActivated`, `NodeSuspended`, `NodeRetired` | node-topology.events | `OnNodeStatus` |
| `com.retailnetwork.commerce.offer-availability.listing.ListingCreated` | offer-availability.events | `OnListingCreated` |
| `com.retailnetwork.commerce.order-sourcing.assignment.FulfillmentReleasedToNode` | order-sourcing.events | `IssueOnRelease` |
| `com.retailnetwork.commerce.order-sourcing.assignment.FulfillmentCancelled` | order-sourcing.events | `CancelOnSourcingCancel` |

## Transactional outbox

A use case saves the aggregates and appends the encoded events in ONE Postgres transaction
(`ports.EventOutbox`, `internal/adapters/outbound/postgres/outbox_repository.go`). The relay
(`internal/adapters/outbound/outbox/relay.go`, always on, started in `cmd/api/main.go`
`startOutboxRelay`) drains `outbox_events` in id order, at-least-once. The cap
`OUTBOX_MAX_ATTEMPTS` (default 20, 0 = unlimited) excludes a poison row; re-arm it with
`UPDATE outbox_events SET attempts = 0 WHERE id = ...`. The first write to a topic that does not
exist yet can fail once with an unknown-topic error: the relay retries it with the same id. The
caller of `NewOutboxRelay` must pass `WithMaxAttempts(...)` inline (`TestOutboxRelaysPassAttemptCap`).

## Consumer group ids

`NODE_CONSUMER_GROUP`, `LISTING_CONSUMER_GROUP` and `SOURCING_CONSUMER_GROUP` are mandatory (a boot
error naming the variable) and never a string literal in code
(`TestKafkaConsumerGroupNeverHardcodedInline`). Each consumer is idempotent on the CloudEvents id
through the `processed_events` inbox (claimed in the same transaction as the handler's writes); a
transient failure is retried, then dead-lettered to `<topic>.dlq`
(`internal/adapters/inbound/kafka/deadletter.go`); a payload that can never decode goes to the DLQ
at once. A release for a node the view does not know yet is retried (A4), not dropped. Rules:
`.claude/rules/fleet/kafka-testing-and-consumers.md`.
