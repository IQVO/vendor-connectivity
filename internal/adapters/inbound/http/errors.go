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
var problemCatalogue = []catalogueEntry{
	{errMalformedRequest, problem{http.StatusUnprocessableEntity, "validation-failed", "Validation failed"}, replayUnset},
	{page.ErrInvalidLimit, problem{http.StatusBadRequest, "invalid-limit", "Invalid limit"}, replayUnset},
	{page.ErrInvalidCursor, problem{http.StatusBadRequest, "invalid-cursor", "Invalid cursor"}, replayUnset},
	{ports.ErrInvalidCursor, problem{http.StatusBadRequest, "invalid-cursor", "Invalid cursor"}, replayUnset},
	{shared.ErrValidation, problem{http.StatusUnprocessableEntity, "validation-failed", "Validation failed"}, replayUnset},
	{purchaseorder.ErrPartialAcknowledgement, problem{http.StatusUnprocessableEntity, "partial-acknowledgement", "An acknowledgement must cover every line with one decision"}, replayUnset},
	{vendor.ErrAckWindowOutOfRange, problem{http.StatusUnprocessableEntity, "ack-window-out-of-range", "The acknowledgement window must be between 1h and 24h"}, replayUnset},
	{network.ErrNodeNotOwnedByVendor, problem{http.StatusNotFound, "node-not-owned-by-vendor", "Node not found for this vendor"}, replayUnset},
	{ports.ErrNotFound, problem{http.StatusNotFound, "not-found", "Not found"}, replayUnset},
	{vendor.ErrVendorSuspended, problem{http.StatusConflict, "vendor-suspended", "The vendor is suspended"}, replayUnset},
	{purchaseorder.ErrAlreadyAcknowledged, problem{http.StatusConflict, "already-acknowledged", "The purchase order was already acknowledged"}, replayUnset},
	{purchaseorder.ErrAckWindowClosed, problem{http.StatusConflict, "ack-window-closed", "The acknowledgement window is closed"}, replayUnset},
	{purchaseorder.ErrLabelBeforeAcceptance, problem{http.StatusConflict, "label-before-acceptance", "A label can be requested only after acceptance"}, replayUnset},
	{purchaseorder.ErrConfirmWithoutLabel, problem{http.StatusConflict, "confirm-without-label", "A shipment confirmation needs a label for every package"}, replayUnset},
	{purchaseorder.ErrCancelAfterAcknowledgement, problem{http.StatusConflict, "cancel-after-acknowledgement", "The purchase order was already acknowledged"}, replayUnset},
	{submission.ErrSubmissionAlreadyProcessed, problem{http.StatusConflict, "submission-already-processed", "The submission was already processed"}, replayUnset},
	{submission.ErrSubmissionNotDue, problem{http.StatusConflict, "submission-not-due", "The submission is not due yet"}, replayUnset},
	{shared.ErrIllegalTransition, problem{http.StatusConflict, "illegal-transition", "Illegal transition"}, replayUnset},
	{ports.ErrConcurrentUpdate, problem{http.StatusConflict, "concurrent-update", "The resource was modified by another request; re-fetch it and retry"}, replayUnset},
	{ports.ErrUpstreamUnavailable, problem{http.StatusServiceUnavailable, "upstream-unavailable", "A neighbouring context did not answer; retry"}, replayUnset},
	{ports.ErrUpstreamRefused, problem{http.StatusBadGateway, "upstream-refused", "A neighbouring context refused the request"}, replayUnset},
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
