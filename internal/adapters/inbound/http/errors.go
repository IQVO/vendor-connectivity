package http

import (
	"errors"
	"net/http"

	"github.com/claudioed/vendor-connectivity/internal/adapters/inbound/http/page"
	"github.com/claudioed/vendor-connectivity/internal/application/ports"
	"github.com/claudioed/vendor-connectivity/internal/domain/network"
	"github.com/claudioed/vendor-connectivity/internal/domain/purchaseorder"
	"github.com/claudioed/vendor-connectivity/internal/domain/shared"
	"github.com/claudioed/vendor-connectivity/internal/domain/submission"
	"github.com/claudioed/vendor-connectivity/internal/domain/vendor"
)

// errMalformedRequest marks a body or parameter the adapter itself rejects
// (bad JSON, unknown field, wrong type, a malformed query value).
var errMalformedRequest = errors.New("request is not valid for this operation")

// problem is the fixed (status, slug, title) of one error category; the
// detail comes from the error at write time.
type problem struct {
	status int
	slug   string
	title  string
}

var internalProblem = problem{http.StatusInternalServerError, "internal-error", "Internal server error"}

// Problems the middlewares write themselves, before any use case runs.
var (
	idempotencyKeyRequired = problem{http.StatusBadRequest, "idempotency-key-required", "Idempotency-Key header is required"}
	idempotencyKeyInvalid  = problem{http.StatusBadRequest, "idempotency-key-invalid", "Idempotency-Key header is not valid"}
	idempotencyKeyReused   = problem{http.StatusConflict, "idempotency-key-reused", "Idempotency-Key was already used with a different request"}
	unknownQueryParameter  = problem{http.StatusBadRequest, "unknown-query-parameter", "Unknown query parameter"}
	rateLimited            = problem{http.StatusTooManyRequests, "rate-limited", "Rate limit exceeded"}
)

// catalogueEntry is one typed error, its problem and the replay class of that
// problem (what the Idempotency-Key middleware does with the answer, ADR 0015).
type catalogueEntry struct {
	err    error
	p      problem
	replay replayClass
}

// problemCatalogue maps every typed error to its problem, in match order: the
// single REST error table of ADR 0002. The slug is the kebab-case of the Err
// name (the shared types are not-found, illegal-transition, concurrent-update
// and validation-failed).
//
// Every entry declares its replay class (ADR 0015 holds the reason of each):
// FINAL when the answer is a function of the request and of data that never
// changes (stored and replayed under the Idempotency-Key), OUTCOME when the
// command committed state and then refused (none today), RETRYABLE when
// nothing was committed and the answer depends on state that can change (not
// stored, the key is released). A 5xx is RETRYABLE and never stored. When
// unsure the class is RETRYABLE: a released refusal costs one more execution,
// a wrongly FINAL one pins a stale refusal for 24 h. A new entry without a
// class fails TestEveryCatalogueEntryDeclaresAReplayClass.
var problemCatalogue = []catalogueEntry{
	// validation-failed is RETRYABLE: the one slug also answers state-dependent checks (an inventory line whose listing the event-fed view puts on another node, ADR 0002 A12).
	{errMalformedRequest, problem{http.StatusUnprocessableEntity, "validation-failed", "Validation failed"}, replayRetryable},
	// Read routes only (a GET holds no key), FINAL by construction.
	{page.ErrInvalidLimit, problem{http.StatusBadRequest, "invalid-limit", "Invalid limit"}, replayFinal},
	{page.ErrInvalidCursor, problem{http.StatusBadRequest, "invalid-cursor", "Invalid cursor"}, replayFinal},
	{ports.ErrInvalidCursor, problem{http.StatusBadRequest, "invalid-cursor", "Invalid cursor"}, replayFinal},
	{shared.ErrValidation, problem{http.StatusUnprocessableEntity, "validation-failed", "Validation failed"}, replayRetryable},
	// FINAL: a purchase order's lines never change after it is issued.
	{purchaseorder.ErrPartialAcknowledgement, problem{http.StatusUnprocessableEntity, "partial-acknowledgement", "An acknowledgement must cover every line with one decision"}, replayFinal},
	// RETRYABLE: onboarding an existing vendor answers 200 before the window is checked, so the same request can pass later.
	{vendor.ErrAckWindowOutOfRange, problem{http.StatusUnprocessableEntity, "ack-window-out-of-range", "The acknowledgement window must be between 1h and 24h"}, replayRetryable},
	// RETRYABLE: the node view is event-fed (a stub with no operator becomes the vendor's once NodeRegistered is consumed).
	{network.ErrNodeNotOwnedByVendor, problem{http.StatusNotFound, "node-not-owned-by-vendor", "Node not found for this vendor"}, replayRetryable},
	// RETRYABLE: vendors are named by the caller and orders, nodes, advices and shipments arrive by event; none is ever deleted but all can appear later.
	{ports.ErrNotFound, problem{http.StatusNotFound, "not-found", "Not found"}, replayRetryable},
	// RETRYABLE: a reinstatement lets the same request pass.
	{vendor.ErrVendorSuspended, problem{http.StatusConflict, "vendor-suspended", "The vendor is suspended"}, replayRetryable},
	// RETRYABLE: ACK_PROCESSING also answers this, and a failed acknowledgement returns the order to ISSUED.
	{purchaseorder.ErrAlreadyAcknowledged, problem{http.StatusConflict, "already-acknowledged", "The purchase order was already acknowledged"}, replayRetryable},
	// FINAL: ackBy is fixed at issue, the clock only moves forward and EXPIRED is terminal.
	{purchaseorder.ErrAckWindowClosed, problem{http.StatusConflict, "ack-window-closed", "The acknowledgement window is closed"}, replayFinal},
	// RETRYABLE: the order moves on (ACK_PROCESSING to ACCEPTED), and labels are added by other requests.
	{purchaseorder.ErrLabelBeforeAcceptance, problem{http.StatusConflict, "label-before-acceptance", "A label can be requested only after acceptance"}, replayRetryable},
	{purchaseorder.ErrConfirmWithoutLabel, problem{http.StatusConflict, "confirm-without-label", "A shipment confirmation needs a label for every package"}, replayRetryable},
	// Not reachable from a route (the cancel is the consumer's); RETRYABLE like the other state refusals.
	{purchaseorder.ErrCancelAfterAcknowledgement, problem{http.StatusConflict, "cancel-after-acknowledgement", "The purchase order was already acknowledged"}, replayRetryable},
	// Not reachable from a route (the processor's); an outcome is set once, so FINAL.
	{submission.ErrSubmissionAlreadyProcessed, problem{http.StatusConflict, "submission-already-processed", "The submission was already processed"}, replayFinal},
	// Not reachable from a route (the processor's); time moves, so RETRYABLE.
	{submission.ErrSubmissionNotDue, problem{http.StatusConflict, "submission-not-due", "The submission is not due yet"}, replayRetryable},
	// RETRYABLE: an aggregate (order, return advice) that has not reached the required state yet.
	{shared.ErrIllegalTransition, problem{http.StatusConflict, "illegal-transition", "Illegal transition"}, replayRetryable},
	// RETRYABLE: another writer won the optimistic lock; the retry re-reads and may win.
	{ports.ErrConcurrentUpdate, problem{http.StatusConflict, "concurrent-update", "The resource was modified by another request; re-fetch it and retry"}, replayRetryable},
	// 5xx: never stored, so RETRYABLE (a dependency that is not ready).
	{ports.ErrUpstreamUnavailable, problem{http.StatusServiceUnavailable, "upstream-unavailable", "A neighbouring context did not answer; retry"}, replayRetryable},
	{ports.ErrUpstreamRefused, problem{http.StatusBadGateway, "upstream-refused", "A neighbouring context refused the request"}, replayRetryable},
}

// entryFor returns the first catalogue entry err matches.
func entryFor(err error) (catalogueEntry, bool) {
	for _, entry := range problemCatalogue {
		if errors.Is(err, entry.err) {
			return entry, true
		}
	}
	return catalogueEntry{}, false
}

// problemFor returns the problem of the first catalogue entry err matches, or
// the internal-error problem.
func problemFor(err error) problem {
	if entry, ok := entryFor(err); ok {
		return entry.p
	}
	return internalProblem
}

// replayOf returns the replay class of the first catalogue entry err matches;
// replayUnset for an error the catalogue does not know (a 500, never stored).
func replayOf(err error) replayClass {
	entry, _ := entryFor(err)
	return entry.replay
}
