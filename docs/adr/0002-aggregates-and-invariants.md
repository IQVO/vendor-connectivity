# ADR 0002: the VendorAccount, PurchaseOrder and Submission aggregates, their invariants and the assumptions the model left open

## Status

Accepted (2026-10-23). Design source: retail-network-docs `model/model.yaml`, context
`vendor-connectivity` (aggregates, policies, `rest`, `publishes`) and the pack pages
`aggregate-design-canvas`, `use-cases`, `domain-events`; `docs/integration/vendor-api.md`;
`contracts-delta.md` (E2, E4, E17) and Gap Register items G2, G4, G12, G30, G31, G32.

## Context

The model gives three aggregate roots, each its own transaction boundary: `VendorAccount`
(`vendorId`), `PurchaseOrder` (`poNumber`) and `Submission` (`transactionId`). The
purchase-order protocol is a conversation: the vendor writes, the write becomes a
`Submission` that is processed later, and the outcome is applied back onto the order.
The model is silent on a number of details; every one the implementation had to decide is an
**assumption** below, flagged `A<n>`, and is reported to the orchestrator as a model gap.
The model is not edited here.

## Decision

### Layout and conventions

- `internal/domain/{shared,vendor,purchaseorder,submission,network}`. Pure Go: no I/O,
  `time.Now` and id generation are never called; `now` and ids are passed in. Dependency
  direction: every package may use `shared`; no aggregate imports another.
- `shared`: `ErrValidation` (problem `validation-failed`, 422), `ErrIllegalTransition`
  (`illegal-transition`, 409), `ErrInvalidSnapshot` (an internal error), the `Event` interface
  and id/text checks. `ErrNotFound` and `ErrConcurrentUpdate` are NOT domain errors: they are
  the port sentinels of `internal/application/ports` (`not-found` 404, `concurrent-update` 409).
- Each aggregate has a constructor or command method per model command, `Snapshot()`, a
  validating `Restore(Snapshot)` and an optimistic `Version()`. A command returns the raised
  events; a save expects `stored version == Version()-1` (`0` for a create). Events are typed
  structs whose fields are the model's `data` list (ADR 0004).
- `network` holds the two event-fed read models the decisions need: `Node` (the model's
  `VendorNodeView`) and `Listing` (`ListingView`). They are domain values (they carry the
  ownership rule) rebuilt from consumed events; see ADR 0003.
- Bounded integers that reach Postgres are plain literal constants (`const MaxInt32 = 2147483647`).

### VendorAccount

States `ACTIVE`, `SUSPENDED`; `SuspendVendor` `ACTIVE -> SUSPENDED`, `ReinstateVendor`
`SUSPENDED -> ACTIVE`. `OnboardVendor` creates an `ACTIVE` account and raises `VendorOnboarded`;
suspend and reinstate raise `VendorSuspended` and `VendorReinstated` (Gap G30).

| Invariant | Err | Problem type | Enforced by |
| --- | --- | --- | --- |
| ackWindow between 1h and 24h (60..1440 minutes) | `ErrAckWindowOutOfRange` | `ack-window-out-of-range` 422 | `vendor.Onboard` |
| a vendor acts only on its own nodes | `ErrNodeNotOwnedByVendor` | `node-not-owned-by-vendor` 404 | `network.Node.EnsureOwnedBy` (the owner is on the node, the rule is the vendor's) |
| a suspended vendor's writes are refused (G30) | `ErrVendorSuspended` | `vendor-suspended` 409 | `vendor.Account.EnsureActive` |

### PurchaseOrder

States `ISSUED`, `ACK_PROCESSING`, `ACCEPTED`, `REJECTED`, `EXPIRED`, `SHIPMENT_PROCESSING`,
`SHIPPED`, `CANCELLED`. Transitions (command): `ISSUED -> ACK_PROCESSING` (SubmitAcknowledgement);
`ACK_PROCESSING -> ACCEPTED | REJECTED | ISSUED` (ProcessSubmission: the decision succeeded as
ACCEPT or REJECT, or the transaction failed); `ISSUED -> EXPIRED` (ExpireUnacknowledgedOrders);
`ISSUED -> CANCELLED` (CancelPurchaseOrder); `ACCEPTED -> SHIPMENT_PROCESSING`
(SubmitShipmentConfirmation); `SHIPMENT_PROCESSING -> SHIPPED | ACCEPTED` (ProcessSubmission).
`RequestLabel` changes no state; it adds labels to an `ACCEPTED` order. Every other
command/state pair is an illegal transition (A7).

| Invariant | Err | Problem type | Enforced by |
| --- | --- | --- | --- |
| an acknowledgement covers every line with one decision (fill-or-kill) | `ErrPartialAcknowledgement` | `partial-acknowledgement` 422 | `PurchaseOrder.SubmitAcknowledgement` |
| acknowledged at most once | `ErrAlreadyAcknowledged` | `already-acknowledged` 409 | same |
| no acknowledgement after `ackBy` | `ErrAckWindowClosed` | `ack-window-closed` 409 | same |
| no label before ACCEPTED | `ErrLabelBeforeAcceptance` | `label-before-acceptance` 409 | `PurchaseOrder.AddLabels` |
| no shipment confirmation without a label for every package | `ErrConfirmWithoutLabel` | `confirm-without-label` 409 | `PurchaseOrder.SubmitShipmentConfirmation` |
| cancel only while ISSUED | `ErrCancelAfterAcknowledgement` | `cancel-after-acknowledgement` 409 (never reaches HTTP) | `PurchaseOrder.Cancel` |

### Submission

States `PROCESSING`, `SUCCESS`, `FAILURE`; `ProcessSubmission` `PROCESSING -> SUCCESS | FAILURE`.
`SubmissionKind`: `ACKNOWLEDGEMENT`, `INVENTORY`, `NODE_REGISTRATION`, `NODE_PROFILE`,
`SHIPMENT_CONFIRMATION`.

| Invariant | Err | Problem type | Enforced by |
| --- | --- | --- | --- |
| outcome set once | `ErrSubmissionAlreadyProcessed` | `submission-already-processed` 409 (internal) | `Submission.Complete` |
| processing never starts before `notBefore = receivedAt + delay` | `ErrSubmissionNotDue` | `submission-not-due` 409 (internal) | `Submission.Complete` |

### Assumptions where the model is silent

- **A1** `poNumber` is `PO-` + the first 16 hex digits of `sha256(assignmentId + ":" + releaseNo)`:
  deterministic, so a redelivered `FulfillmentReleasedToNode` finds its order; a unique index on
  `(assignment_id, release_no)` is the second guard. No consumer parses a `poNumber`.
- **A2** Rules this context can check at receipt are refused synchronously and create no
  transaction (ownership, suspension, state, window, fill-or-kill, body shape). Refusals by a
  neighbour during processing end the transaction in `FAILURE` with the neighbour's `Err...` as
  `SubmissionError.code` (the neighbour's problem slug in PascalCase with an `Err` prefix).
- **A3** A node with no declared operational profile has handling time 0, so
  `ackBy = min(issuedAt + ackWindow, requiredShipBy)`.
- **A4** A release for a node that `VendorNodeView` does not know yet is a retryable error, not
  a dropped event (the topics are separate, so node-topology's event can lag). The consumer
  retries with backoff and dead-letters when the budget is exhausted. A release for a node the
  view knows to be `NETWORK`-operated is ignored (inbox row written).
- **A5** An `ackBy` already in the past at issue time is accepted as computed; the sweep expires
  the order on its next pass.
- **A6** `SubmitAcknowledgement` checks in this order: state, window, completeness (every line
  once, ordered quantity), decision (REJECT needs a `reasonCode`; ACCEPT must not carry one).
  The first failure wins. The window is judged on the receipt instant and `now == ackBy` is
  still inside it.
- **A7** States without a dedicated `Err...`: an acknowledgement of a `CANCELLED` order, a label for
  a `REJECTED`/`EXPIRED`/`CANCELLED` order, a shipment confirmation for a `SHIPMENT_PROCESSING`,
  `SHIPPED`, `REJECTED`, `EXPIRED` or `CANCELLED` order are `ErrIllegalTransition`
  (`illegal-transition`). A label before acceptance (`ISSUED`, `ACK_PROCESSING`) is
  `ErrLabelBeforeAcceptance`; a shipment confirmation of an `ISSUED` or `ACK_PROCESSING` order
  is `ErrConfirmWithoutLabel` (no label can exist yet). A repeated label request for packages
  that already hold labels returns them in any of `ACCEPTED`, `SHIPMENT_PROCESSING`, `SHIPPED`.
- **A8** Repeating a command that is already in effect is a no-op, not an error: onboarding an
  existing `vendorId` returns the account unchanged (200), suspending a suspended vendor and
  reinstating an active one return the account unchanged (200); no event is raised.
- **A9** A suspended vendor's writes (acknowledgement, inventory, node registration, profile,
  label, shipment confirmation) are refused with `vendor-suspended`. Reads, polling, the
  issuing of new purchase orders, the expiry sweep and the processing of submissions received
  before the suspension continue.
- **A10** Neighbour outcomes during `ProcessSubmission`: a `2xx` is `SUCCESS`; a `4xx` is
  `FAILURE` with the neighbour's `Err...`; a `5xx`, a timeout or a refused connection leaves the
  submission `PROCESSING` for the next pass (one attempt is counted per pass); after
  `SUBMISSION_MAX_ATTEMPTS` (default 5) it ends `FAILURE` with `ErrUpstreamUnavailable`. The
  simulated failure (`TX_FAILURE_RATE`) ends `FAILURE` with `ErrSimulatedFailure`. Two neighbour
  answers are success: node-topology replaying an idempotent registration, and offer-availability
  `feed-already-applied` (the feed id is the transaction id, so the feed was applied by an
  earlier attempt).
- **A11** A successful `NODE_REGISTRATION` also upserts the node into `VendorNodeView` from the
  node-topology response, in the same transaction that completes the submission, so the vendor
  can declare a profile the moment it reads `SUCCESS`. The node's own events later merge into the
  same row (they never lower a profile revision or a status).
- **A12** An inventory feed line for a listing `ListingView` knows to belong to a different node
  is refused at receipt (`validation-failed`). A listing the view does not know yet is passed
  through: offer-availability decides (`ErrListingNotOnNode`).
- **A13** `VendorNodeView` keeps every node node-topology announces, including network-operated
  ones, so a release for a parcel node is recognised and ignored instead of retried (A4).
- **A14** `VendorSuspended` and `VendorReinstated` are integration events on
  `retailnetwork.vendor-connectivity.events`; no context consumes them yet. Suspension takes an
  optional `reason`. The routes are `POST /vendors/{vendorId}:suspend` and `:reinstate`, plus
  `GET /vendors/{vendorId}`, for partner managers (the model names no route for either command).
- **A15** `ErrCancelAfterAcknowledgement` is logged at INFO and the inbox row is written; the
  event is not retried and nothing is published. order-sourcing decides the cancel (E11, Gap G12)
  and learns the order's fate from `PurchaseOrderAccepted`.
- **A16** `CancelOnSourcingCancel` addresses the order with the highest `releaseNo` of the
  `assignmentId` (the open release; order-sourcing allows one pending release). No order for the
  assignment is a no-op with an inbox row.
- **A17** Polling pages are keyset pages ordered oldest first by `(issuedAt, poNumber)`; `createdAfter`
  is strict on `issuedAt`; `nextToken` is an opaque base64url cursor carrying the last
  `(issuedAt, poNumber)` and the vendor and node filters; the page size is 50 by default (`limit` 1..200).
  The poll returns orders in every state so the vendor can reconcile.
- **A18** `PO_REDELIVERY_RATE` (0..1): each non-empty page repeats its first order with that
  probability. `TX_PROCESSING_DELAY` (duration), `TX_FAILURE_RATE` (0..1) and `RATE_LIMIT_RPS`
  (0 disables) complete the model's four simulation knobs; all default to well-behaved.
- **A19** `RequestLabel` calls shipping-and-tracking `POST /labels` once per requested package with
  the Idempotency-Key `vc-label-<poNumber>-<packageNo>`, and the body `{poNumber, packageNo,
  assignmentId, orderId, tier}` (Gap G2; `orderId` and `tier` stay inside this service). Labels
  already issued are kept when a later package fails; the answer is then `503 upstream-unavailable`
  and a retry returns the stored labels and issues only the missing ones.
- **A20** A shipment confirmation names every tracking number of the order's labels, and only
  those (set equality); anything else is `ErrConfirmWithoutLabel`. `shippedAt` is required.
- **A21** The rate limit is one token bucket per process over `/vendor/*` (burst = max(1, RPS)).
  Over the limit the answer is `429` `rate-limited` with `Retry-After` in whole seconds.
- **A22** Submissions are retained 30 days (the model's size estimate) and then purged by a daily sweep.
- **A23** Reads take `vendorId` as a required query parameter (Gap G32). A resource of another
  vendor is `node-not-owned-by-vendor` (404); a transaction of another vendor is `not-found`.
- **A24** The in-organization base URLs of the four sync calls (`NODE_TOPOLOGY_URL`,
  `OFFER_AVAILABILITY_URL`, `SHIPPING_AND_TRACKING_URL`, `NODE_PERFORMANCE_URL`) are mandatory
  settings like the database and broker: a missing one is a boot error naming it.
- **A25** While an acknowledgement is `ACK_PROCESSING` the order keeps the pending decision
  (`decision`, `reasonCode`, `transactionId`); the vendor-facing `acknowledgement` appears only when
  the transaction succeeded (the OpenAPI text). A failed transaction clears it and returns the order to `ISSUED`.
- **A26** The expiry sweep expires an `ISSUED` order with `ackBy < now` (strict); an order in
  `ACK_PROCESSING` is never expired. A transaction that fails after `ackBy` returns the order to
  `ISSUED` and the next pass expires it.
- **A27** The model's "one aggregate per transaction" is relaxed in exactly one place, as the pack
  proposes: `ProcessSubmission` saves the `Submission`, the `PurchaseOrder` it concerns and their
  outbox rows in one transaction, so a vendor that reads `SUCCESS` always finds the order in its
  new state. Receipt does the same for the acknowledgement and shipment confirmation.

## Consequences

- Every invariant above has a table-driven domain test; the mutation gate runs over each
  `internal/domain/*` package.
- The REST error mapping is a single table (`internal/adapters/inbound/http/errors.go`).
- The assumptions are reported as `model_gaps` for the orchestrator to fold into the model.
