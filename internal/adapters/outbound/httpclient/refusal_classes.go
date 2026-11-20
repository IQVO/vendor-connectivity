package httpclient

// The anti-corruption layer's reading of a callee's refusals (ADR 0016).
//
// A callee that adopted the fleet idempotency rule declares a replay class for every
// problem type it answers on an idempotent route. A RETRYABLE one is released: nothing
// was committed, the key is free, and the same request under the SAME Idempotency-Key
// executes again and may pass. FINAL and OUTCOME refusals are stored and replayed, so
// retrying them only repeats the refusal. This context's submission worker retries
// the same submission with the same key, so it needs to know which refusals are worth
// a retry. That knowledge is the callee's and lives here, one visible table per
// callee, derived from the callee's merged class table; the use cases only see
// gateway.RefusalError.Retryable.
//
// A slug that is not in the table is TERMINAL: an unknown refusal must never loop.
// A slug is classified per callee because the class is per callee (validation-failed is
// FINAL in node-topology and RETRYABLE in offer-availability and shipping-and-tracking).
// refusal_spec_test.go holds each table against the byte-pinned OpenAPI of the callee.

// refusalClass is how this context treats one problem slug of one callee.
type refusalClass uint8

const (
	// classUnset is the zero value: the slug is not in the table, which reads as terminal.
	classUnset refusalClass = iota
	// classTerminal is a FINAL or OUTCOME refusal of the callee: the submission fails.
	classTerminal
	// classRetryable is a RETRYABLE refusal of the callee: the submission stays pending.
	classRetryable
)

// refusalTable maps a callee's problem slug to its class here.
type refusalTable map[string]refusalClass

// retryable reports whether the callee releases the slug under the key.
func (t refusalTable) retryable(slug string) bool { return t[slug] == classRetryable }

// Problem slugs several callees share.
const (
	slugNotFound          = "not-found"
	slugIllegalTransition = "illegal-transition"
	slugConcurrentUpdate  = "concurrent-update"
	slugValidationFailed  = "validation-failed"
	slugKeyRequired       = "idempotency-key-required"
	slugKeyInvalid        = "idempotency-key-invalid"
	slugKeyReused         = "idempotency-key-reused"
)

// nodeTopologyRefusals follows node-topology ADR 0020 (POST /nodes and
// PUT /nodes/{nodeId}/operational-profile, key = the submission id).
var nodeTopologyRefusals = refusalTable{
	// RETRYABLE: the answer depends on state another request can change.
	slugNotFound:                     classRetryable, // the node or the region/station it names is not known yet
	slugIllegalTransition:            classRetryable,
	slugConcurrentUpdate:             classRetryable,
	"incomplete-operational-profile": classRetryable,
	"serving-station-not-allowed":    classRetryable,
	"lane-endpoint-not-active":       classRetryable,
	"duplicate-lane":                 classRetryable,
	"not-a-delivery-station":         classRetryable,
	"handoff-node-not-usable":        classRetryable,
	"unknown-region":                 classRetryable, // the region is not defined here yet
	// FINAL: a function of the request, or of a fact that never reverts.
	slugValidationFailed:                      classTerminal,
	"malformed-request-body":                  classTerminal,
	"malformed-request-query":                 classTerminal,
	"request-body-too-large":                  classTerminal,
	"vendor-node-code-taken":                  classTerminal,
	"stale-profile-revision":                  classTerminal,
	"node-retired":                            classTerminal,
	"operator-not-allowed-for-node-type":      classTerminal,
	"pickup-point-postal-code-not-labellable": classTerminal,
	"subtype-not-allowed":                     classTerminal,
	"cutoff-outside-operating-hours":          classTerminal,
	"sort-dwell-not-allowed":                  classTerminal,
	"inbound-limits-not-allowed":              classTerminal,
	// the key problems: this context sends a valid key and the same body every time.
	slugKeyRequired: classTerminal,
	slugKeyInvalid:  classTerminal,
	slugKeyReused:   classTerminal,
}

// offerAvailabilityRefusals follows offer-availability ADR 0016 (POST /inventory-feeds,
// key = feedId = the submission id).
var offerAvailabilityRefusals = refusalTable{
	// RETRYABLE
	"listing-not-on-node":     classRetryable, // RETRYABLE with a record: the FAILED feed is kept and a retry under the same key turns it APPLIED once the listing exists
	slugConcurrentUpdate:      classRetryable,
	"pack-level-not-found":    classRetryable,
	"node-not-sellable":       classRetryable,
	slugNotFound:              classRetryable,
	slugIllegalTransition:     classRetryable,
	"pack-level-order":        classRetryable, // mixed slugs: a request-only cause and a state-dependent one share the slug
	"invalid-pack-quantity":   classRetryable,
	"invalid-pack-dimensions": classRetryable,
	slugValidationFailed:      classRetryable,
	// OUTCOME: the FAILED feed is committed and the 422 stored; a retry replays it.
	"negative-availability": classTerminal,
	// FINAL
	"feed-already-applied":    classTerminal, // read as success before it gets here (submissions.go)
	"duplicate-listing":       classTerminal,
	"stale-availability":      classTerminal,
	"gtin-already-assigned":   classTerminal,
	"pack-quantity-immutable": classTerminal,
	"invalid-gtin":            classTerminal,
	"invalid-item-dimensions": classTerminal,
	"invalid-pack-level":      classTerminal,
	"malformed-request-body":  classTerminal,
	"malformed-request-query": classTerminal,
	"request-body-too-large":  classTerminal,
	"invalid-limit":           classTerminal,
	"invalid-cursor":          classTerminal,
	"unknown-query-parameter": classTerminal,
	// the key problems; idempotency-key-reused is RETRYABLE in the callee's table (it depends on a
	// stored row that expires), but this context repeats the same body, so repeating it cannot pass.
	slugKeyRequired: classTerminal,
	slugKeyInvalid:  classTerminal,
	slugKeyReused:   classTerminal,
}

// shippingTrackingRefusals follows shipping-and-tracking ADR 0020 (POST /labels, key
// vc-label-<po>-<pkg>). The scan refusals do not apply to this caller; they are listed
// terminal so that the table names every refusal the callee documents.
var shippingTrackingRefusals = refusalTable{
	// RETRYABLE
	slugValidationFailed:             classRetryable, // mixed slug: orderId unknown to customer-ordering, the fetched ship-to
	"address-suspect":                classRetryable, // the address is fetched, the shopper can correct it
	"pickup-point-unavailable":       classRetryable,
	"destination-prefix-unavailable": classRetryable,
	"tracking-number-taken":          classRetryable,
	slugConcurrentUpdate:             classRetryable,
	// FINAL
	slugNotFound:           classTerminal,
	"label-already-issued": classTerminal,
	"void-after-handover":  classTerminal,
	"journey-closed":       classTerminal,
	"duplicate-scan":       classTerminal,
	// OUTCOME: the scan is stored, not applied; replayed.
	"scan-out-of-order":   classTerminal,
	slugIllegalTransition: classTerminal,
	// the key problems
	slugKeyRequired: classTerminal,
	slugKeyInvalid:  classTerminal,
	slugKeyReused:   classTerminal,
}
