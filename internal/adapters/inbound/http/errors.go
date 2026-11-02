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

// problemCatalogue maps every typed error to its problem, in match order: the
// single REST error table of ADR 0002. The slug is the kebab-case of the Err
// name (the shared types are not-found, illegal-transition, concurrent-update
// and validation-failed).
var problemCatalogue = []struct {
	err error
	p   problem
}{
	{errMalformedRequest, problem{http.StatusUnprocessableEntity, "validation-failed", "Validation failed"}},
	{page.ErrInvalidLimit, problem{http.StatusBadRequest, "invalid-limit", "Invalid limit"}},
	{page.ErrInvalidCursor, problem{http.StatusBadRequest, "invalid-cursor", "Invalid cursor"}},
	{ports.ErrInvalidCursor, problem{http.StatusBadRequest, "invalid-cursor", "Invalid cursor"}},
	{shared.ErrValidation, problem{http.StatusUnprocessableEntity, "validation-failed", "Validation failed"}},
	{purchaseorder.ErrPartialAcknowledgement, problem{http.StatusUnprocessableEntity, "partial-acknowledgement", "An acknowledgement must cover every line with one decision"}},
	{vendor.ErrAckWindowOutOfRange, problem{http.StatusUnprocessableEntity, "ack-window-out-of-range", "The acknowledgement window must be between 1h and 24h"}},
	{network.ErrNodeNotOwnedByVendor, problem{http.StatusNotFound, "node-not-owned-by-vendor", "Node not found for this vendor"}},
	{ports.ErrNotFound, problem{http.StatusNotFound, "not-found", "Not found"}},
	{vendor.ErrVendorSuspended, problem{http.StatusConflict, "vendor-suspended", "The vendor is suspended"}},
	{purchaseorder.ErrAlreadyAcknowledged, problem{http.StatusConflict, "already-acknowledged", "The purchase order was already acknowledged"}},
	{purchaseorder.ErrAckWindowClosed, problem{http.StatusConflict, "ack-window-closed", "The acknowledgement window is closed"}},
	{purchaseorder.ErrLabelBeforeAcceptance, problem{http.StatusConflict, "label-before-acceptance", "A label can be requested only after acceptance"}},
	{purchaseorder.ErrConfirmWithoutLabel, problem{http.StatusConflict, "confirm-without-label", "A shipment confirmation needs a label for every package"}},
	{purchaseorder.ErrCancelAfterAcknowledgement, problem{http.StatusConflict, "cancel-after-acknowledgement", "The purchase order was already acknowledged"}},
	{submission.ErrSubmissionAlreadyProcessed, problem{http.StatusConflict, "submission-already-processed", "The submission was already processed"}},
	{submission.ErrSubmissionNotDue, problem{http.StatusConflict, "submission-not-due", "The submission is not due yet"}},
	{shared.ErrIllegalTransition, problem{http.StatusConflict, "illegal-transition", "Illegal transition"}},
	{ports.ErrConcurrentUpdate, problem{http.StatusConflict, "concurrent-update", "The resource was modified by another request; re-fetch it and retry"}},
	{ports.ErrUpstreamUnavailable, problem{http.StatusServiceUnavailable, "upstream-unavailable", "A neighbouring context did not answer; retry"}},
	{ports.ErrUpstreamRefused, problem{http.StatusBadGateway, "upstream-refused", "A neighbouring context refused the request"}},
}

// problemFor returns the problem of the first catalogue entry err matches, or
// the internal-error problem.
func problemFor(err error) problem {
	for _, entry := range problemCatalogue {
		if errors.Is(err, entry.err) {
			return entry.p
		}
	}
	return internalProblem
}
