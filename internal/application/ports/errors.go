package ports

import "errors"

var (
	// ErrNotFound is what every repository returns for a Get or Find that
	// matches nothing (problem type not-found, 404). An adapter wraps its own
	// miss into this sentinel, so callers test errors.Is(err, ports.ErrNotFound).
	ErrNotFound = errors.New("not found")
	// ErrConcurrentUpdate is what Save returns when the stored version is not
	// the one the aggregate was loaded at (problem type concurrent-update, 409).
	ErrConcurrentUpdate = errors.New("concurrent update")
	// ErrUpstreamUnavailable is what a sync-call adapter returns when the
	// neighbour did not answer or answered 5xx (problem type
	// upstream-unavailable, 503). A processing submission stays PROCESSING.
	ErrUpstreamUnavailable = errors.New("upstream unavailable")
	// ErrUpstreamRefused is a neighbour 4xx the caller cannot turn into a
	// vendor-meaningful answer (problem type upstream-refused, 502).
	ErrUpstreamRefused = errors.New("upstream refused")
	// ErrInvalidCursor is a continuation token this API did not issue for this
	// request (problem type invalid-cursor, 400).
	ErrInvalidCursor = errors.New("cursor is not a valid page token")
	// ErrNodeNotKnown marks a release for a node VendorNodeView has not seen
	// yet: a retryable consumer error (assumption A4), never a client error.
	ErrNodeNotKnown = errors.New("node not known yet")
)
