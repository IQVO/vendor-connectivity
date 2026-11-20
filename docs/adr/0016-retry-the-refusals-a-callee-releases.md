# ADR 0016: retry the refusals a callee releases under its Idempotency-Key (the consumer half of the replay-class rule)

## Status

Accepted (2026-11-19). Narrows the "a `4xx` is `FAILURE`" half of ADR 0002 A10 to the refusals a callee
stores, and completes the sentence "This service is also a caller (`vc-label-<po>-<pkg>` to
shipping-and-tracking); not changed here" of ADR 0015. It adopts the consumer side of the fleet
idempotency rule decided on 2026-11-18 (warehouse-harness-template:
`.claude/rules/fleet/idempotency-and-outbox.md`, `docs/idempotency-replay-classes.md`). The providers' side
is merged: node-topology ADR 0020 (PR #86), offer-availability ADR 0016 (PR #76) and
shipping-and-tracking ADR 0020 (PR #89). No route, status code, problem slug, field or event of this
service changes.

## Context

Three callees of this service answer an idempotent route under an `Idempotency-Key` this service chooses,
and always the same one for the same piece of work:

| Call | Caller | Key |
| --- | --- | --- |
| node-topology `POST /nodes`, `PUT /nodes/{nodeId}/operational-profile` | the submission worker | the submission id |
| offer-availability `POST /inventory-feeds` | the submission worker | `feedId` = the submission id |
| shipping-and-tracking `POST /labels` | `RequestLabel` (synchronous) | `vc-label-<poNumber>-<packageNo>` |

Before the rule a callee stored every refusal below 500 for 24 hours, so a retry under the same key replayed
it: `classify` (`usecases/submissions.go`) and `client.go` turning every 4xx into a terminal failed submission
lost nothing. After the rule a callee does NOT store a refusal that depends on state another request can
change (replay class RETRYABLE): nothing was committed, the key is released and the same key executes again
and may pass. A refusal such as node-topology `unknown-region`, offer-availability `listing-not-on-node` or
shipping-and-tracking `address-suspect` can therefore succeed a moment later, but this service still ended the
submission with `FAILURE` on the first one.

## Decision

1. **The outbound adapter reads the callee's class.** `internal/adapters/outbound/httpclient/refusal_classes.go`
   holds one small table per callee, from the callee's merged class table, that maps a problem slug to
   `retryable` (RETRYABLE) or `terminal` (FINAL or OUTCOME). The client sets
   `gateway.RefusalError.Retryable` from it (and `Slug`); the use cases and the domain only see the flag.
   The class is the callee's knowledge and lives in the anti-corruption layer, never in the domain. The class is
   per callee, not per slug: `validation-failed` is FINAL in node-topology and RETRYABLE in offer-availability
   and shipping-and-tracking.
2. **Unknown is terminal.** A slug that is not in the callee's table, a 4xx without a problem body, a refusal of
   a callee that has no table (node-performance, a read with no key) and a refusal of another callee's slug are
   all `Retryable = false`: an unknown refusal never loops.
3. **What is retried.** A retryable refusal keeps the submission `PROCESSING`, exactly like an outage: the
   existing worker (`ProcessDueSubmissions`, every `SUBMISSION_POLL_INTERVAL`) executes the SAME submission
   again with the SAME key (the submission id), so the callee executes it again. A later success completes it
   normally (`SUCCESS`, one `SubmissionProcessed`); a later terminal refusal fails it as before. Each pass is
   one attempt.
4. **Bounds: the existing ones, unchanged.** A submission has no expiry of its own (`EXPIRY_SWEEP_INTERVAL`
   expires unacknowledged purchase orders, not submissions); what bounds it is the attempt budget
   `DefaultSubmissionMaxAttempts` (5 passes, not configurable by env), which refusals and outages share.
   At the budget a submission kept pending by a refusal ends `FAILURE` with **that refusal's own `Err` code and
   detail**, which is the terminal state it always had; one kept pending by an outage still ends with
   `ErrUpstreamUnavailable`. Nothing is added to the schedule: there is no new loop, timer or backoff.
5. **The reason is recorded.** `Submission.RecordRefusal` counts the attempt and keeps the last refusal (`Err`
   code, detail, JSON path; the neighbour's text is cleaned and clipped to the bounds of a `SubmissionError`)
   in columns `submissions.last_refusal_code|message|path` (migration 0006). The next refusal replaces it, an
   outage keeps it, and the outcome clears it (`Complete`). It is read from the table or the
   `neighbour released a refusal` log line of the worker. The vendor-visible `Transaction` is unchanged: its
   schema has no field for a pending reason, and adding one is a contract change reported below.
6. **Labels.** `RequestLabel` has no stored step: it asks for the missing packages, keeps the labels issued
   before a failure and answers `502 upstream-refused` (an outage `503`). Neither is stored under the vendor's
   key (ADR 0015) and the order stays `ACCEPTED`, so a released refusal already leaves the step retryable: the
   vendor repeats the request, the issued packages are answered from storage and the missing one is asked again
   under the SAME `vc-label-<po>-<pkg>` key, which the callee now executes instead of replaying the refusal.
   No code changes for it; `TestAReleasedLabelRefusalLeavesTheStepRetryableUnderTheSameKey` and the
   `label_shipment_rules` scenario pin it, and the shipping-and-tracking table is what the label path would
   consult if the caller ever retries by itself.

### The tables

RETRYABLE (retried); every other documented slug is explicitly TERMINAL in `refusal_classes.go`.

| Callee | Retryable slugs (callee ADR) |
| --- | --- |
| node-topology (ADR 0020) | `not-found`, `illegal-transition`, `concurrent-update`, `incomplete-operational-profile`, `serving-station-not-allowed`, `lane-endpoint-not-active`, `duplicate-lane`, `not-a-delivery-station`, `handoff-node-not-usable`, `unknown-region` |
| offer-availability (ADR 0016) | `listing-not-on-node` (RETRYABLE with a record: the FAILED feed stays visible and a retry under the same key turns it APPLIED once the listing exists), `concurrent-update`, `pack-level-not-found`, `node-not-sellable`, `not-found`, `illegal-transition`, `pack-level-order`, `invalid-pack-quantity`, `invalid-pack-dimensions`, `validation-failed` |
| shipping-and-tracking (ADR 0020) | `validation-failed`, `address-suspect`, `pickup-point-unavailable`, `destination-prefix-unavailable`, `tracking-number-taken`, `concurrent-update` |

Terminal, explicit: node-topology `validation-failed`, `vendor-node-code-taken`, `stale-profile-revision`,
`node-retired`, `operator-not-allowed-for-node-type`, `subtype-not-allowed`, `cutoff-outside-operating-hours`,
`sort-dwell-not-allowed`, `inbound-limits-not-allowed`, `pickup-point-postal-code-not-labellable` and the
request-shape slugs; offer-availability `negative-availability` (OUTCOME), `feed-already-applied` (read as
success before the table is consulted), `duplicate-listing`, `stale-availability`, `gtin-already-assigned`,
`pack-quantity-immutable`, `invalid-gtin`, `invalid-item-dimensions`, `invalid-pack-level` and the
request-shape slugs; shipping-and-tracking `not-found`, `label-already-issued`, `void-after-handover`,
`journey-closed`, `duplicate-scan`, `scan-out-of-order` and `illegal-transition` (the two scan refusals are
OUTCOMEs and do not apply to this caller); and, for the three, `idempotency-key-required|invalid|reused`
(this service sends a valid key and the same body every time: repeating it cannot pass).

### Keeping the tables honest

`httpclient/refusal_spec_test.go` reads each callee's byte-pinned OpenAPI (`testdata/*.openapi.yaml`, refreshed
in this change to the replay-class text) and fails when (a) a problem type the spec lists under "not stored,
the key is released" is not mapped retryable, or (b) a problem type named by the Idempotency-Key text or by a
4xx response of the operations this service calls is in neither class. It also fails when the callee rewords
the sentence the extraction anchors on, so a new pin cannot pass unread. **Limit:** the OpenAPI does not
enumerate the callee's whole catalogue (the class table lives in the callee's ADR), so a refusal no response
text names (for example offer-availability `pack-level-order`) is held only by `TestTheTablesCarryTheMergedADRClasses`,
which copies the three ADR class tables. A callee that adds a retryable slug and does not name it in the
spec is not caught until its ADR is read: the pin refresh (`pinned_contract_gate.py`) is the trigger to re-read it.

## Judgement calls

1. **A mixed slug is retried, because the callee released it.** `validation-failed` of offer-availability and
   shipping-and-tracking is RETRYABLE since one slug mixes request-only and state-dependent causes. A feed or
   a label that is truly malformed now uses the five passes (about five poll intervals) before it fails with
   the same `Err` code; the vendor sees `FAILURE` a few seconds later than before, never a different answer.
2. **`idempotency-key-reused` is terminal even where the callee calls it RETRYABLE** (offer-availability: it
   depends on a stored row that expires). This service repeats the same body under the same key, so the
   refusal cannot arise from a retry and a repeat could not pass.
3. **Refusals and outages share one attempt budget** rather than getting a second one: it keeps the bound the
   operators already know, and "unknown never loops" holds trivially.
4. **The label path is not given a retry loop.** Retrying a synchronous request inside the vendor's call
   would hold it open; the vendor's repeat already works under the same key.

## Consequences

- A submission can now sit `PROCESSING` after a 4xx. Its `attempts` and `last_refusal_*` say why; the
  vendor-visible `Transaction` shows `PROCESSING` with `errors: []`, as for an outage.
- The window is short: the budget is 5 passes at `SUBMISSION_POLL_INTERVAL` (default 1 s) with no backoff, so a
  refusal that clears within a few seconds (`concurrent-update`, a node or listing a moment away) is retried
  to success, and one that clears in minutes (`unknown-region`, `listing-not-on-node` waiting for an event
  consumer that is behind) still ends `FAILURE` with the callee's code, exactly as before. Widening the window
  (a backoff, a separate budget for refusals, `SUBMISSION_MAX_ATTEMPTS` wired to the environment) is a
  separate decision and is not part of this change.
- A retried refusal costs the callee one more rolled-back transaction per pass, bounded by the budget.
- A callee that releases a refusal this table maps terminal costs nothing but a stale refusal for that
  submission; a callee that stores a refusal this table maps retryable costs five replays of the same stored
  answer (the callee never executes it again) before the submission fails with it. Both are corrected by a
  line in the table and the callee's ADR.
- Rolled back with the change: migration 0006 has a down file; the column set is additive and defaulted.
