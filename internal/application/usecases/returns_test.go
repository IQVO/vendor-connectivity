package usecases_test

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/claudioed/vendor-connectivity/internal/application/ports"
	"github.com/claudioed/vendor-connectivity/internal/application/requests"
	"github.com/claudioed/vendor-connectivity/internal/application/usecases"
	"github.com/claudioed/vendor-connectivity/internal/domain/network"
	"github.com/claudioed/vendor-connectivity/internal/domain/returns"
	"github.com/claudioed/vendor-connectivity/internal/domain/shared"
	"github.com/claudioed/vendor-connectivity/internal/domain/submission"
	"github.com/claudioed/vendor-connectivity/internal/domain/vendor"
)

func authorized(id, node string, at time.Time) returns.Authorized {
	return returns.Authorized{AuthorizationID: id, OrderID: "ord-1", GroupNo: 1, NodeID: node,
		Disposition: "NEAREST_RETURNS_CENTER", Method: "DROP_OFF",
		Lines:     []returns.Line{{LineNo: 1, CatalogItemID: "ci-1", Quantity: 2, OwnerRef: "s-1"}},
		ExpiresAt: at.Add(30 * 24 * time.Hour), AuthorizedAt: at}
}

// advised makes one AUTHORIZED advice of node visible, through the consumer.
func (h *harness) advised(id, node string, at time.Time) {
	h.t.Helper()
	h.must(h.svc.OnReturnAuthorized(h.ctx, h.eventID(), "ReturnAuthorized", authorized(id, node, at)))
}

func adviceIDs(h *harness, q requests.ReturnPollQuery) ([]string, string) {
	h.t.Helper()
	page, err := h.svc.PollReturnAdvices(h.ctx, q)
	h.must(err)
	ids := make([]string, 0, len(page.ReturnAdvices))
	for _, a := range page.ReturnAdvices {
		ids = append(ids, a.AuthorizationID)
	}
	return ids, page.NextToken
}

func adviceHarness(t *testing.T) *harness {
	t.Helper()
	h := newHarness(t, usecases.Config{})
	h.onboard("V-1")
	h.vendorNode("n-1", "V-1", 0)
	h.vendorNode("n-2", "V-2", 0)
	return h
}

func TestPollReturnAdvicesScopesToTheVendorsAuthorizedAdvices(t *testing.T) {
	h := adviceHarness(t)
	h.advised("ret-1", "n-1", t0)
	h.advised("ret-2", "n-2", t0) // another vendor's node
	ids, next := adviceIDs(h, requests.ReturnPollQuery{VendorID: "V-1"})
	if fmt.Sprint(ids) != "[ret-1]" || next != "" {
		t.Fatalf("poll = %v %q", ids, next)
	}
	// A second advice of another vendor's node is not found either.
	if _, err := h.svc.GetReturnAdvice(h.ctx, "V-1", "ret-2"); !errors.Is(err, ports.ErrNotFound) {
		t.Fatalf("get of another vendor's advice = %v", err)
	}
	one, err := h.svc.GetReturnAdvice(h.ctx, "V-1", "ret-1")
	h.must(err)
	if one.AuthorizationID != "ret-1" || one.NodeID != "n-1" || one.State != "AUTHORIZED" || one.VendorID != "V-1" ||
		len(one.Lines) != 1 || one.Lines[0].OwnerRef != "s-1" || len(one.TrackingNumbers) != 0 || !one.ReadyAt.Equal(t0) || one.ExpiresAt == nil {
		t.Fatalf("advice = %+v", one)
	}
	// The nodeId filter of another vendor's node is node-not-owned-by-vendor.
	if _, err := h.svc.PollReturnAdvices(h.ctx, requests.ReturnPollQuery{VendorID: "V-1", NodeID: "n-2"}); !errors.Is(err, network.ErrNodeNotOwnedByVendor) {
		t.Fatalf("node filter of n-2 = %v", err)
	}
}

func TestPollReturnAdvicesPagesWithNextToken(t *testing.T) {
	h := adviceHarness(t)
	for i := 1; i <= 5; i++ {
		// Two advices share an instant: the id breaks the tie.
		h.advised(fmt.Sprintf("ret-%d", i), "n-1", t0.Add(time.Duration((i+1)/2)*time.Hour))
	}
	var seen []string
	token := ""
	pages := 0
	for {
		ids, next := adviceIDs(h, requests.ReturnPollQuery{VendorID: "V-1", Limit: 2, NextToken: token})
		seen = append(seen, ids...)
		pages++
		if next == "" {
			break
		}
		token = next
	}
	if fmt.Sprint(seen) != "[ret-1 ret-2 ret-3 ret-4 ret-5]" || pages != 3 {
		t.Fatalf("paged %v in %d pages", seen, pages)
	}
	// readyAfter is strict and compares with readyAt (ret-1..2 at +1h, ret-3..4 at +2h, ret-5 at +3h).
	after := t0.Add(2 * time.Hour)
	ids, _ := adviceIDs(h, requests.ReturnPollQuery{VendorID: "V-1", ReadyAfter: &after})
	if fmt.Sprint(ids) != "[ret-5]" {
		t.Fatalf("readyAfter = %v", ids)
	}
	// A cursor of another vendor is refused.
	if _, err := h.svc.PollReturnAdvices(h.ctx, requests.ReturnPollQuery{VendorID: "V-1", NextToken: "!"}); !errors.Is(err, ports.ErrInvalidCursor) {
		t.Fatalf("bad cursor = %v", err)
	}
	// An unknown vendor is not found; a malformed id is validation-failed.
	if _, err := h.svc.PollReturnAdvices(h.ctx, requests.ReturnPollQuery{VendorID: "V-9"}); !errors.Is(err, ports.ErrNotFound) {
		t.Fatalf("unknown vendor = %v", err)
	}
	if _, err := h.svc.PollReturnAdvices(h.ctx, requests.ReturnPollQuery{VendorID: "!"}); !errors.Is(err, shared.ErrValidation) {
		t.Fatalf("malformed vendor = %v", err)
	}
}

func TestSubmitReturnOutcomeAcceptsPublishesAndLeavesThePoll(t *testing.T) {
	h := adviceHarness(t)
	h.advised("ret-1", "n-1", t0)
	h.must(h.svc.OnReturnLabelIssued(h.ctx, h.eventID(), "ReturnLabelIssued", "ret-1", "TRACK-1"))
	body := requests.ReturnOutcome{VendorID: "V-1", AuthorizationID: "ret-1", NodeID: "n-1", ReceivedAt: t0.Add(time.Hour),
		Lines: []requests.ReturnOutcomeLine{{CatalogItemID: "ci-1", OwnerRef: "s-1", QtyRestockable: 1, QtyUnsellable: 1},
			{CatalogItemID: "ci-9", QtyUnsellable: 3}}} // ci-9 was never authorized: never refused
	acc, err := h.svc.SubmitReturnOutcome(h.ctx, body)
	h.must(err)
	if acc.TransactionID == "" {
		t.Fatal("no transactionId")
	}
	tx := h.tx("V-1", acc.TransactionID)
	if tx.Kind != "RETURN_OUTCOME" || tx.Status != "PROCESSING" {
		t.Fatalf("transaction = %+v", tx)
	}
	checkOutcomeEvent(h, acc.TransactionID)
	// The advice left the poll and answers with its terminal state.
	if ids, _ := adviceIDs(h, requests.ReturnPollQuery{VendorID: "V-1"}); len(ids) != 0 {
		t.Fatalf("the poll still offers the advice: %v", ids)
	}
	one, err := h.svc.GetReturnAdvice(h.ctx, "V-1", "ret-1")
	h.must(err)
	if one.State != "OUTCOME_RECORDED" || len(one.TrackingNumbers) != 1 {
		t.Fatalf("advice after the outcome = %+v", one)
	}
	sub, err := h.store.Submissions().Get(h.ctx, submission.ID(acc.TransactionID))
	h.must(err)
	if sub.NodeID() != "n-1" || sub.Kind() != submission.KindReturnOutcome {
		t.Errorf("submission = %s %s", sub.NodeID(), sub.Kind())
	}
}

// checkOutcomeEvent pins the outbox event: subject, lines and received time.
func checkOutcomeEvent(h *harness, txID string) {
	h.t.Helper()
	evs := h.store.Outbox().Events()
	ev, ok := evs[len(evs)-1].(returns.OutcomeSubmitted)
	if !ok || ev.TransactionID != txID || ev.AuthorizationID != "ret-1" || ev.SubjectID() != "ret-1" ||
		len(ev.Lines) != 2 || ev.Lines[1].QtyUnsellable != 3 || !ev.ReceivedAt.Equal(t0.Add(time.Hour)) {
		h.t.Fatalf("event = %+v", ev)
	}
}

func outcomeBody() requests.ReturnOutcome {
	return requests.ReturnOutcome{VendorID: "V-1", AuthorizationID: "ret-1", NodeID: "n-1", ReceivedAt: t0.Add(time.Hour),
		Lines: []requests.ReturnOutcomeLine{{CatalogItemID: "ci-1", QtyRestockable: 1}}}
}

func TestSubmitReturnOutcomeRefusals(t *testing.T) {
	h := adviceHarness(t)
	h.advised("ret-1", "n-1", t0)
	ok := outcomeBody
	cases := map[string]func(requests.ReturnOutcome) requests.ReturnOutcome{
		"vendorId":        func(b requests.ReturnOutcome) requests.ReturnOutcome { b.VendorID = "!!"; return b },
		"authorizationId": func(b requests.ReturnOutcome) requests.ReturnOutcome { b.AuthorizationID = "?"; return b },
		"nodeId":          func(b requests.ReturnOutcome) requests.ReturnOutcome { b.NodeID = "?"; return b },
		"receivedAt":      func(b requests.ReturnOutcome) requests.ReturnOutcome { b.ReceivedAt = time.Time{}; return b },
		"lines":           func(b requests.ReturnOutcome) requests.ReturnOutcome { b.Lines = nil; return b },
		"catalogItemId": func(b requests.ReturnOutcome) requests.ReturnOutcome {
			b.Lines = []requests.ReturnOutcomeLine{{CatalogItemID: "?", QtyRestockable: 1}}
			return b
		},
		"quantities": func(b requests.ReturnOutcome) requests.ReturnOutcome {
			b.Lines = []requests.ReturnOutcomeLine{{CatalogItemID: "ci-1", QtyUnsellable: -1}}
			return b
		},
	}
	for name, mutate := range cases {
		if _, err := h.svc.SubmitReturnOutcome(h.ctx, mutate(ok())); err == nil || !errors.Is(err, shared.ErrValidation) {
			t.Errorf("%s: err = %v, want validation-failed", name, err)
		}
	}
	// Unknown authorization or vendor: not-found. A node that is not the advice's: validation.
	unknown := ok()
	unknown.AuthorizationID = "ret-9"
	if _, err := h.svc.SubmitReturnOutcome(h.ctx, unknown); !errors.Is(err, ports.ErrNotFound) {
		t.Fatalf("unknown authorization = %v", err)
	}
	wrongVendor := ok()
	wrongVendor.VendorID = "V-2"
	if _, err := h.svc.SubmitReturnOutcome(h.ctx, wrongVendor); !errors.Is(err, ports.ErrNotFound) {
		t.Fatalf("a vendor with no advice = %v", err)
	}
	wrongNode := ok()
	wrongNode.NodeID = "n-2"
	if _, err := h.svc.SubmitReturnOutcome(h.ctx, wrongNode); !errors.Is(err, shared.ErrValidation) {
		t.Fatalf("a node that is not the advice's = %v", err)
	}
	// A second outcome for the same advice is an illegal transition.
	if _, err := h.svc.SubmitReturnOutcome(h.ctx, ok()); err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.SubmitReturnOutcome(h.ctx, ok()); !errors.Is(err, shared.ErrIllegalTransition) {
		t.Fatalf("second outcome = %v", err)
	}
	// A suspended vendor's outcome is vendor-suspended.
	h.advised("ret-2", "n-1", t0.Add(time.Minute))
	_, err := h.svc.SuspendVendor(h.ctx, "V-1", "test")
	h.must(err)
	suspended := ok()
	suspended.AuthorizationID = "ret-2"
	if _, err := h.svc.SubmitReturnOutcome(h.ctx, suspended); !errors.Is(err, vendor.ErrVendorSuspended) {
		t.Fatalf("suspended vendor = %v", err)
	}
}

func TestReturnEventsAreIdempotentAndSkipUnknownNodes(t *testing.T) {
	h := adviceHarness(t)
	h.advised("ret-1", "n-1", t0)
	before := h.store.Counts()
	// The same event id twice claims the inbox once.
	h.must(h.svc.OnReturnAuthorized(h.ctx, "evt-dup", "ReturnAuthorized", authorized("ret-1", "n-1", t0)))
	h.must(h.svc.OnReturnAuthorized(h.ctx, "evt-dup", "ReturnAuthorized", authorized("ret-1", "n-1", t0)))
	if h.store.Counts().Inbox != before.Inbox+1 {
		t.Fatalf("inbox = %d, want %d", h.store.Counts().Inbox, before.Inbox+1)
	}
	// A node the view does not know, and a node with no operator vendor, are skipped.
	h.must(h.svc.OnReturnAuthorized(h.ctx, h.eventID(), "ReturnAuthorized", authorized("ret-x", "n-x", t0)))
	if _, err := h.store.Advices().Get(h.ctx, "ret-x"); !errors.Is(err, ports.ErrNotFound) {
		t.Fatalf("an unknown node must not create an advice: %v", err)
	}
	// A label for an advice never authorized is logged and acknowledged.
	h.must(h.svc.OnReturnLabelIssued(h.ctx, h.eventID(), "ReturnLabelIssued", "ret-none", "T-1"))
	if _, err := h.store.Advices().Get(h.ctx, "ret-none"); !errors.Is(err, ports.ErrNotFound) {
		t.Fatal("a label alone must not create an advice")
	}
	// Cancellation closes the advice out of the poll; a late authorization does not reopen it.
	h.must(h.svc.OnReturnCancelled(h.ctx, h.eventID(), "ReturnCancelled", "ret-1"))
	if ids, _ := adviceIDs(h, requests.ReturnPollQuery{VendorID: "V-1"}); len(ids) != 0 {
		t.Fatalf("a cancelled advice must leave the poll: %v", ids)
	}
	h.must(h.svc.OnReturnAuthorized(h.ctx, h.eventID(), "ReturnAuthorized", authorized("ret-1", "n-1", t0)))
	one, err := h.svc.GetReturnAdvice(h.ctx, "V-1", "ret-1")
	h.must(err)
	if one.State != "CANCELLED" {
		t.Fatalf("a late authorization must not reopen a cancelled advice: %s", one.State)
	}
}
