package usecases_test

import (
	"errors"
	"testing"

	"github.com/claudioed/vendor-connectivity/internal/application/gateway"
	"github.com/claudioed/vendor-connectivity/internal/application/ports"
	"github.com/claudioed/vendor-connectivity/internal/application/requests"
	"github.com/claudioed/vendor-connectivity/internal/application/usecases"
	"github.com/claudioed/vendor-connectivity/internal/domain/submission"
)

// retryableRefusal is what the outbound adapter hands the use case for a 4xx the
// callee released under the Idempotency-Key (replay class RETRYABLE, ADR 0016).
func retryableRefusal(code string) error {
	return &gateway.RefusalError{Status: 409, Code: code, Slug: "slug-of-" + code, Message: "released by the callee: " + code, Path: "$.x", Retryable: true}
}

// pendingCase is one relayed kind with its neighbour double wired to a scripted error.
type pendingCase struct {
	name   string
	submit func(h *harness) string
	script func(h *harness, err error)
	keys   func(h *harness) []string
}

func pendingCases() []pendingCase {
	return []pendingCase{
		{"NODE_REGISTRATION (node-topology POST /nodes)",
			func(h *harness) string {
				acc, err := h.svc.RegisterVendorNode(h.ctx, registration())
				h.must(err)
				return acc.TransactionID
			},
			func(h *harness, err error) {
				h.nt.Err = err
				h.nt.Registered = gateway.RegisteredNode{NodeID: "n-9", NodeType: "FULFILLMENT_CENTER", VendorID: "V-1", VendorNodeCode: "SITE-9", RegionID: "r-1", Timezone: "America/Sao_Paulo"}
			},
			func(h *harness) []string { return h.nt.Keys }},
		{"NODE_PROFILE (node-topology PUT operational-profile)",
			func(h *harness) string {
				h.vendorNode("n-1", "V-1", 60)
				acc, err := h.svc.DeclareNodeProfile(h.ctx, "n-1", validProfile())
				h.must(err)
				return acc.TransactionID
			},
			func(h *harness, err error) { h.nt.Err = err },
			func(h *harness) []string { return h.nt.Keys }},
		{"INVENTORY (offer-availability POST /inventory-feeds)",
			func(h *harness) string {
				h.vendorNode("n-1", "V-1", 60)
				acc, err := h.svc.SubmitInventory(h.ctx, validFeed())
				h.must(err)
				return acc.TransactionID
			},
			func(h *harness, err error) { h.offers.Err = err },
			func(h *harness) []string { return h.offers.FeedIDs }},
	}
}

func lastRefusalOf(t *testing.T, h *harness, id string) (submission.Error, bool, int) {
	t.Helper()
	sub, err := h.store.Submissions().Get(h.ctx, submission.ID(id))
	h.must(err)
	e, ok := sub.LastRefusal()
	return e, ok, sub.Attempts()
}

// A refusal the callee released keeps the submission PROCESSING (not FAILURE), the
// worker retries it under the SAME key, and a later success completes it. The
// reason it is pending is recorded on the submission and the vendor-visible
// transaction is unchanged (no new field: the Transaction schema has none).
func TestAReleasedRefusalKeepsTheSubmissionPendingAndALaterSuccessCompletesIt(t *testing.T) {
	for _, tc := range pendingCases() {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, usecases.Config{})
			h.onboard("V-1")
			id := tc.submit(h)

			tc.script(h, retryableRefusal("ErrUnknownRegion"))
			assertPendingWith(t, h, id, "ErrUnknownRegion", 1)
			if names := h.eventNames(); contains(names, "SubmissionProcessed") {
				t.Fatalf("a pending submission publishes no outcome: %v", names)
			}
			// still refused: the same submission is retried and the NEW reason replaces the old one
			tc.script(h, retryableRefusal("ErrConcurrentUpdate"))
			assertPendingWith(t, h, id, "ErrConcurrentUpdate", 2)

			// the callee now accepts: the same submission completes
			tc.script(h, nil)
			if n := h.processAll(); n != 1 {
				t.Fatalf("the retry that succeeds ends the submission: %d", n)
			}
			if tx := h.tx("V-1", id); tx.Status != "SUCCESS" || len(tx.Errors) != 0 {
				t.Fatalf("transaction = %+v", tx)
			}
			if _, ok, _ := lastRefusalOf(t, h, id); ok {
				t.Fatal("a SUCCESS carries no pending refusal")
			}
			assertEveryCallUsedKey(t, tc.keys(h), id, 3)
			if n := countOf(h.eventNames(), "SubmissionProcessed"); n != 1 {
				t.Fatalf("SubmissionProcessed published %d times, want 1", n)
			}
		})
	}
}

// assertPendingWith runs one pass and checks the submission is still PROCESSING with
// the refusal recorded as the reason, after the given number of attempts.
func assertPendingWith(t *testing.T, h *harness, id, code string, attempts int) {
	t.Helper()
	if n := h.processAll(); n != 0 {
		t.Fatalf("a released refusal reaches no outcome: %d", n)
	}
	tx := h.tx("V-1", id)
	if tx.Status != "PROCESSING" || len(tx.Errors) != 0 || tx.ProcessedAt != nil {
		t.Fatalf("transaction = %+v, want PROCESSING with no errors", tx)
	}
	got, ok, n := lastRefusalOf(t, h, id)
	if !ok || n != attempts || got.Code != code || got.Message != "released by the callee: "+code || got.Path != "$.x" {
		t.Fatalf("recorded refusal = %+v %v attempts %d, want %s after %d", got, ok, n, code, attempts)
	}
}

func assertEveryCallUsedKey(t *testing.T, keys []string, id string, calls int) {
	t.Helper()
	if len(keys) != calls {
		t.Fatalf("callee calls = %v, want %d", keys, calls)
	}
	for _, k := range keys {
		if k != id {
			t.Fatalf("every retry must carry the SAME Idempotency-Key %q, got %v", id, keys)
		}
	}
}

// A FINAL or OUTCOME refusal, and a refusal nobody classified, are not marked
// retryable by the adapter: the submission fails on the first pass exactly as before.
func TestAFinalOrUnclassifiedRefusalStillFailsTheSubmissionAtOnce(t *testing.T) {
	for _, tc := range pendingCases() {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, usecases.Config{})
			h.onboard("V-1")
			id := tc.submit(h)
			tc.script(h, &gateway.RefusalError{Status: 409, Code: "ErrNegativeAvailability", Slug: "negative-availability", Message: "negative", Path: "$.lines[0]"})

			if n := h.processAll(); n != 1 {
				t.Fatalf("a terminal refusal ends the submission: %d", n)
			}
			tx := h.tx("V-1", id)
			if tx.Status != "FAILURE" || len(tx.Errors) != 1 || tx.Errors[0].Code != "ErrNegativeAvailability" || tx.Errors[0].Path != "$.lines[0]" {
				t.Fatalf("transaction = %+v", tx)
			}
			if _, ok, attempts := lastRefusalOf(t, h, id); ok || attempts != 0 {
				t.Fatalf("no refusal is recorded for a terminal one: %v %d", ok, attempts)
			}
			if h.processAll() != 0 || len(tc.keys(h)) != 1 {
				t.Fatal("the callee is asked once")
			}
		})
	}
}

// A refusal that stays released until the attempt cap ends in the SAME terminal state
// as before this change: FAILURE carrying the callee's own code, not ErrUpstreamUnavailable.
func TestAForeverReleasedRefusalFailsAtTheAttemptCapWithTheCalleesCode(t *testing.T) {
	for _, tc := range pendingCases() {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, usecases.Config{SubmissionMaxAttempts: 3})
			h.onboard("V-1")
			id := tc.submit(h)
			tc.script(h, retryableRefusal("ErrListingNotOnNode"))

			for pass := 1; pass <= 2; pass++ {
				assertPendingWith(t, h, id, "ErrListingNotOnNode", pass)
			}
			if n := h.processAll(); n != 1 {
				t.Fatalf("the third pass ends it: %d", n)
			}
			assertFailedWithRefusal(t, h, id, "ErrListingNotOnNode")
			if h.processAll() != 0 || len(tc.keys(h)) != 3 {
				t.Fatalf("calls = %v, want exactly 3 and no more after the failure", tc.keys(h))
			}
			if n := countOf(h.eventNames(), "SubmissionProcessed"); n != 1 {
				t.Fatalf("SubmissionProcessed published %d times, want 1", n)
			}
		})
	}
}

func assertFailedWithRefusal(t *testing.T, h *harness, id, code string) {
	t.Helper()
	tx := h.tx("V-1", id)
	if tx.Status != "FAILURE" || len(tx.Errors) != 1 || tx.Errors[0].Code != code || tx.Errors[0].Message != "released by the callee: "+code {
		t.Fatalf("transaction = %+v, want FAILURE with the callee's refusal (not %s)", tx, usecases.CodeUpstreamUnavail)
	}
	if _, ok, _ := lastRefusalOf(t, h, id); ok {
		t.Fatal("the failure carries the refusal as its error, not as a pending one")
	}
}

// Refusals and outages share the one attempt budget; an outage that ends the budget
// is still ErrUpstreamUnavailable and a refusal that does is the refusal.
func TestRefusalsAndOutagesShareTheAttemptBudget(t *testing.T) {
	h := newHarness(t, usecases.Config{SubmissionMaxAttempts: 3})
	h.onboard("V-1")
	acc, err := h.svc.RegisterVendorNode(h.ctx, registration())
	h.must(err)
	h.nt.Err = retryableRefusal("ErrUnknownRegion")
	h.processAll()
	h.nt.Err = errors.New("connection refused")
	h.processAll()
	if got, ok, attempts := lastRefusalOf(t, h, acc.TransactionID); !ok || got.Code != "ErrUnknownRegion" || attempts != 2 {
		t.Fatalf("an outage keeps the last refusal: %+v %v %d", got, ok, attempts)
	}
	if n := h.processAll(); n != 1 {
		t.Fatalf("the third pass ends it: %d", n)
	}
	if tx := h.tx("V-1", acc.TransactionID); tx.Status != "FAILURE" || tx.Errors[0].Code != usecases.CodeUpstreamUnavail {
		t.Fatalf("an outage on the last attempt is ErrUpstreamUnavailable: %+v", tx)
	}

	h2 := newHarness(t, usecases.Config{SubmissionMaxAttempts: 3})
	h2.onboard("V-1")
	acc2, _ := h2.svc.RegisterVendorNode(h2.ctx, registration())
	h2.nt.Err = errors.New("connection refused")
	h2.processAll()
	h2.processAll()
	h2.nt.Err = retryableRefusal("ErrUnknownRegion")
	h2.processAll()
	if tx := h2.tx("V-1", acc2.TransactionID); tx.Status != "FAILURE" || tx.Errors[0].Code != "ErrUnknownRegion" {
		t.Fatalf("a refusal on the last attempt is the refusal: %+v", tx)
	}
}

// A refused label call must leave nothing terminal: the labels already issued are
// kept, the order stays ACCEPTED, and a retry issues only the missing package under
// the SAME deterministic key (shipping-and-tracking released the refusal, so it
// executes again). RequestLabel has no stored step, so this needs no mapping.
func TestAReleasedLabelRefusalLeavesTheStepRetryableUnderTheSameKey(t *testing.T) {
	h, po := poFixture(t, usecases.Config{})
	h.accept("V-1", po)
	req := requests.LabelRequest{VendorID: "V-1", PONumber: po, Packages: []requests.LabelPackage{{PackageNo: 1}, {PackageNo: 2}}}
	h.ship.Err, h.ship.FailPackage = retryableRefusal("ErrAddressSuspect"), 2
	if _, err := h.svc.RequestLabel(h.ctx, req); !errors.Is(err, ports.ErrUpstreamRefused) {
		t.Fatalf("the vendor sees the refusal: %v", err)
	}
	if o := h.order(po); o.State != "ACCEPTED" || len(o.Labels) != 1 || o.Labels[0].PackageNo != 1 {
		t.Fatalf("order = %+v, want ACCEPTED holding label 1", o)
	}
	h.ship.Err = nil // the shopper corrected the address
	resp, err := h.svc.RequestLabel(h.ctx, req)
	if err != nil || len(resp.Labels) != 2 {
		t.Fatalf("the retry completes the request: %+v %v", resp, err)
	}
	if got := h.ship.Keys; len(got) != 3 || got[1] != got[2] || got[1] != "vc-label-"+po+"-2" {
		t.Fatalf("keys = %v, want package 2 asked twice under vc-label-%s-2", got, po)
	}
}

func contains(names []string, want string) bool { return countOf(names, want) > 0 }

func countOf(names []string, want string) int {
	n := 0
	for _, name := range names {
		if name == want {
			n++
		}
	}
	return n
}
