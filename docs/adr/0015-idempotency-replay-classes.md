# ADR 0015: every problem type has a replay class; a retryable refusal is not stored under the Idempotency-Key

## Status

Accepted (2026-11-18). Supersedes the sentence "A 4xx is stored" of ADR 0010 and the
"replays the first response of every state-changing `POST`/`PUT`" of ADR 0003, for refusals only
(a 2xx is still stored and replayed). It adopts the fleet standard of the owner decision of
2026-11-18 (warehouse-harness-template: `.claude/rules/fleet/idempotency-and-outbox.md`,
`docs/idempotency-replay-classes.md`).

## Context

`RequireIdempotencyKey` (`internal/adapters/inbound/http/idempotency.go`) stored every answer below
500: the key row, the handler's writes and the stored answer commit in ONE Postgres transaction (a
`SAVEPOINT handler_run` keeps a failed statement from poisoning it). A 4xx was therefore stored for 24
hours whatever it said. Several refusals of this service are true only now: a `409 concurrent-update`
(the loser of an optimistic lock), a `404 not-found` for a vendor, order, node or return advice that
another request or an event delivers a moment later, a `409 vendor-suspended` that a reinstatement
lifts. Replayed to a client that did the right thing and retried, each pins a false answer for a day.

## Decision

A request retried under the same `Idempotency-Key` is answered from the store only when its first answer
is final for that request. Every problem type has exactly one replay class, declared on its entry of
`problemCatalogue` (`errors.go`, field `replay`):

- **FINAL**: the answer is a function of the request and of data that never changes. Stored, replayed
  with `Idempotency-Replayed: true`.
- **OUTCOME**: the command committed state and then answered an error that must be remembered. Stored,
  and the writes commit with the stored answer. **No entry is an OUTCOME** (see Evidence).
- **RETRYABLE**: nothing was committed and the answer depends on state that can change. Not stored; the
  key row rolls back with the request, so the key is free and a retry executes again.

A 2xx is stored; a 5xx and a panic are never stored (every 5xx entry is RETRYABLE). The middleware holds
no list of slugs and no status range beyond 2xx and 5xx: it reads the class. A catalogue entry without a
class fails `TestEveryCatalogueEntryDeclaresAReplayClass`; there is no runtime default (a refusal that
did not go through `writeError` has no class and is not stored). When unsure between FINAL and RETRYABLE
the class is RETRYABLE: a released refusal costs one more execution, a wrongly FINAL one pins a stale
refusal for 24 h.

### Mechanism

`writeError` tells the capturing writer the class of the problem it writes (`MarkReplay`, `capture.go`);
`capture.stored()` is true for a 2xx and for a FINAL or OUTCOME refusal. `runFreshRequest` rolls the
whole transaction back, key row included, and flushes the answer for anything not stored: the same
branch the 5xx already took. A FINAL or OUTCOME refusal keeps the existing path (release the savepoint,
heal it with `ROLLBACK TO SAVEPOINT` when a statement failed on the server, store, commit); an OUTCOME
whose writes were lost to that healing is rolled back like a lost 2xx. 2xx, 5xx and FINAL answers are
byte-identical to before.

- **A concurrent duplicate** blocked on the first request's key row (`INSERT ... ON CONFLICT DO NOTHING`
  waits for the transaction) sees, when the first ends RETRYABLE, its own insert succeed: it EXECUTES
  the command itself and answers what that execution answers (stored if a 2xx or FINAL). It is not
  answered with the refusal it waited for. This is what a 5xx of the first already did.
  `TestIdempotency_AConcurrentDuplicateExecutesWhenTheFirstEndsRetryable` pins it.
- **Releasing a key**: a different body under a released key is a NEW request, not
  `409 idempotency-key-reused`.
- Problems the middlewares write themselves are outside the table: `idempotency-key-required`,
  `idempotency-key-invalid`, `rate-limited` and the NUL rejection answer before the key is claimed, and
  `idempotency-key-reused` is written on the replay path; none is ever stored.

## The class of every problem type

| Problem type | Status | Class | Reason (code) |
| --- | --- | --- | --- |
| `invalid-limit`, `invalid-cursor` | 400 | FINAL | Read routes only (a GET holds no key); a function of the query. |
| `partial-acknowledgement` | 422 | FINAL | `commands_ack.go` `coversEveryLine`: compares the request with the order's lines, which never change after issue. |
| `ack-window-closed` | 409 | FINAL | `commands_ack.go:44-50`: `now` after `ackBy` (fixed at issue, never updated) or `EXPIRED` (terminal); the clock only moves forward. |
| `submission-already-processed` | 409 | FINAL | `submission.go`: an outcome is set once. Not reachable from a route (the submission processor's). |
| `validation-failed` | 422 | RETRYABLE | judgement 1 |
| `ack-window-out-of-range` | 422 | RETRYABLE | judgement 2 |
| `not-found` | 404 | RETRYABLE | `vendors.go:75,96` (a vendor named by the caller), `orders.go:191` (orders issued by an event), `returns.go:175`, `inbound.go:123` and the node view (event-fed): another request or an event can deliver the fact; nothing is ever deleted but all can appear. |
| `node-not-owned-by-vendor` | 404 | RETRYABLE | `network.go:153` `EnsureOwnedBy`: a node stub (`OperatorUnknown`) becomes the vendor's once `NodeRegistered` is consumed (`repositories.go:154` upserts the operator). |
| `vendor-suspended` | 409 | RETRYABLE | `vendor.go` `EnsureActive`; a reinstatement lets the same request pass. judgement 3 |
| `already-acknowledged` | 409 | RETRYABLE | `commands_ack.go:54`: also answers `ACK_PROCESSING`, and a failed acknowledgement returns the order to `ISSUED` (`ApplyAckOutcome(false)`). judgement 4 |
| `label-before-acceptance` | 409 | RETRYABLE | `commands_labels.go:18`: the order moves `ISSUED` -> `ACK_PROCESSING` -> `ACCEPTED` by another transaction. |
| `confirm-without-label` | 409 | RETRYABLE | `commands_shipment.go:21-32`: labels are added by other requests (`AddLabels`). |
| `illegal-transition` | 409 | RETRYABLE | an aggregate (order, return advice `Pollable()`) that has not reached the state yet, e.g. `domain/returns/returns.go:250`. |
| `concurrent-update` | 409 | RETRYABLE | `repositories.go:80` (`updated`: 0 rows) and `saveErr` (23505): another writer won; the retry re-reads. |
| `cancel-after-acknowledgement` | 409 | RETRYABLE | state refusal like `already-acknowledged`; not reachable from a route (the consumer's cancel). |
| `submission-not-due` | 409 | RETRYABLE | time moves; not reachable from a route (the processor's). |
| `upstream-unavailable` | 503 | RETRYABLE | 5xx, never stored. |
| `upstream-refused` | 502 | RETRYABLE | 5xx, never stored (a neighbour's 4xx is relayed as this 502, `orders.go` `upstreamError`). |

`TestEverySlugHasOneClassAndItIsTheOneOfTheADR` pins this table: a changed class is an edit of the test
and of this ADR together.

### Judgement calls

1. **`validation-failed` is RETRYABLE, not FINAL.** One slug answers both a malformed request (a function
   of the request) and `checkListings` (`submissions.go:104-115`): an inventory line whose listing the
   `ListingView` knows to belong to another node (A12). The view is event-fed and
   `ListingViewRepo.Save` upserts `node_id` (`repositories.go:181`), so the same request can pass later.
   A class is per problem type; the cost of RETRYABLE for a malformed body is one more validation.
2. **`ack-window-out-of-range` is RETRYABLE.** `OnboardVendor` answers 200 for an existing vendor before
   the window is checked (`vendors.go:29-34` then `vendor.go:81`), so a request refused for a new vendor
   can succeed once another request onboards it.
3. **`vendor-suspended` is RETRYABLE** though a suspension is a deliberate state: the troubleshooting path
   is "reinstate, then retry", and a pinned 409 would make the same key unusable for 24 h after it.
4. **`already-acknowledged` is RETRYABLE** because of `ACK_PROCESSING`; the terminal states alone would be
   FINAL.
5. **`cancel-after-acknowledgement`, `submission-already-processed`, `submission-not-due`** are classified
   for completeness: no idempotent route can answer them today.
6. **`not-found` and `node-not-owned-by-vendor`** are one class for every route. The one refusal that is
   final (an order that exists and belongs to another vendor, `orders.go:197`) shares the slug
   `node-not-owned-by-vendor` with the event-fed node case; the cost is one more execution.

## Evidence that no problem type is an OUTCOME

A refusal is an OUTCOME only if its use case commits state and then refuses. Every route refuses before
its first write: acknowledgements and shipment confirmations mutate the aggregate in memory and save last
(`orders.go:258-265`), the inventory, node and profile submissions check then save (`submissions.go:45-53`),
the inbound receipt and the return outcome check then save (`inbound.go:202-209`, `returns.go:185-205`),
vendor commands save last (`vendors.go:36-45`, `:75-85`). `RequestLabel` writes issued labels (`orders.go:294`)
and then returns the neighbour's failure, but that is a 502/503, which is never stored and rolls back (the
labels come back under the deterministic key `vc-label-<po>-<pkg>`). The one write followed by a possible
refusal (`returns.go:194` then `:198`, `validation-failed` from `submission.Receive`) is RETRYABLE, so it
rolls back. This store commits a stored 4xx's writes with it; no FINAL refusal follows a write, so nothing
relied on that.

## Consequences

- A client that retries after `concurrent-update`, `not-found`, `vendor-suspended` or any other RETRYABLE
  refusal under the same key now gets its retry executed, with no `Idempotency-Replayed` header, instead
  of the refusal for 24 hours. The OpenAPI `Idempotency-Key` description and the REST reference say which
  refusals are replayed and which release the key.
- A caller that reuses a key after a refusal on purpose and changes the body gets a new request, not
  `409 idempotency-key-reused`.
- Deterministic-key callers: returns-processing posts `POST /vendor/return-outcomes` with the key
  `return-outcome-<authorizationId>` and treats any 400/404/409/422 as terminal. After this change a `404
  not-found` (the advice not consumed yet) or a `409 illegal-transition` is not stored, so the same key can
  succeed on a retry; that client should retry those instead of treating them as terminal (a change in
  returns-processing, not in this repo).
- This service is also a caller (`vc-label-<po>-<pkg>` to shipping-and-tracking); not changed here.
- The REST middleware is the only write surface with a store: the MCP server and the A2A agent are
  read-only.
- Rolling back a RETRYABLE refusal is the existing 5xx rollback, so no new failure mode: the key row's
  24-hour window, the retention sweep and the expiry of an old row on claim are unchanged.
