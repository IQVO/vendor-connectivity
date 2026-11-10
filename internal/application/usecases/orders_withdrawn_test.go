package usecases_test

import (
	"strings"
	"testing"

	"github.com/claudioed/vendor-connectivity/internal/application/requests"
	"github.com/claudioed/vendor-connectivity/internal/application/usecases"
	"github.com/claudioed/vendor-connectivity/internal/domain/purchaseorder"
)

const withdrawnType = "FulfillmentReleaseWithdrawn"

func withdrawn(assignment string, releaseNo int, reason string) requests.ReleaseWithdrawn {
	return requests.ReleaseWithdrawn{AssignmentID: assignment, ReleaseNo: releaseNo, Reason: reason}
}

func (h *harness) cancelReason(po string) string {
	h.t.Helper()
	o, err := h.store.Orders().Get(h.ctx, purchaseorder.Number(po))
	h.must(err)
	return o.CancelReason()
}

func TestCancelOnReleaseWithdrawnCancelsTheIssuedOrder(t *testing.T) {
	h := newHarness(t, usecases.Config{})
	h.onboard("V-1")
	h.vendorNode("n-1", "V-1", 60)
	po := h.issue("asg-1", "n-1")
	h.must(h.svc.CancelOnReleaseWithdrawn(h.ctx, "evt-w1", withdrawnType, withdrawn("asg-1", 1, "PROMISE_REVISED")))
	if o := h.order(po); o.State != "CANCELLED" {
		t.Fatalf("state = %s", o.State)
	}
	if got := h.cancelReason(po); got != "release withdrawn: PROMISE_REVISED" {
		t.Fatalf("reason = %q", got)
	}
	if got := h.eventNames(); got[len(got)-1] != "PurchaseOrderCancelled" {
		t.Fatalf("events = %v", got)
	}
}

func TestCancelOnReleaseWithdrawnLeavesAnAcceptedOrder(t *testing.T) {
	h := newHarness(t, usecases.Config{})
	h.onboard("V-1")
	h.vendorNode("n-1", "V-1", 60)
	po := h.issue("asg-1", "n-1")
	h.accept("V-1", po)
	before := h.store.Counts()
	h.must(h.svc.CancelOnReleaseWithdrawn(h.ctx, "evt-w1", withdrawnType, withdrawn("asg-1", 1, "PROMISE_REVISED")))
	if o := h.order(po); o.State != "ACCEPTED" {
		t.Fatalf("an accepted order is not cancelled: %+v", o)
	}
	after := h.store.Counts()
	if after.OutboxEvents != before.OutboxEvents || after.VersionSum != before.VersionSum {
		t.Fatalf("nothing is saved or published for a refused withdraw: %+v vs %+v", after, before)
	}
}

func TestCancelOnReleaseWithdrawnLeavesAnOrderThatIsAlreadyCancelled(t *testing.T) {
	h := newHarness(t, usecases.Config{})
	h.onboard("V-1")
	h.vendorNode("n-1", "V-1", 60)
	po := h.issue("asg-1", "n-1")
	h.must(h.svc.CancelOnSourcingCancel(h.ctx, "evt-c1", "FulfillmentCancelled", requests.FulfillmentCancelled{AssignmentID: "asg-1", Reason: "customer cancelled"}))
	before := h.store.Counts()
	h.must(h.svc.CancelOnReleaseWithdrawn(h.ctx, "evt-w1", withdrawnType, withdrawn("asg-1", 1, "PROMISE_REVISED")))
	if h.store.Counts().OutboxEvents != before.OutboxEvents || h.cancelReason(po) != "customer cancelled" {
		t.Fatal("a cancelled order keeps its first reason and publishes nothing more")
	}
}

func TestCancelOnReleaseWithdrawnUnknownReleaseIsANoOp(t *testing.T) {
	h := newHarness(t, usecases.Config{})
	h.onboard("V-1")
	h.vendorNode("n-1", "V-1", 60)
	po := h.issue("asg-1", "n-1")
	before := h.store.Counts()
	h.must(h.svc.CancelOnReleaseWithdrawn(h.ctx, "evt-w1", withdrawnType, withdrawn("asg-unknown", 1, "PROMISE_REVISED")))
	h.must(h.svc.CancelOnReleaseWithdrawn(h.ctx, "evt-w2", withdrawnType, withdrawn("asg-1", 7, "PROMISE_REVISED")))
	after := h.store.Counts()
	if after.OutboxEvents != before.OutboxEvents || after.VersionSum != before.VersionSum || h.order(po).State != "ISSUED" {
		t.Fatalf("an unknown release changes nothing: %+v vs %+v", after, before)
	}
}

// The carrier is released again under a newer number after a dissolve: a late
// or replayed withdraw of release 1 must not cancel the order of release 2.
func TestCancelOnReleaseWithdrawnTouchesOnlyThatRelease(t *testing.T) {
	h := newHarness(t, usecases.Config{})
	h.onboard("V-1")
	h.vendorNode("n-1", "V-1", 60)
	first := h.issue("asg-1", "n-1")
	h.must(h.svc.CancelOnReleaseWithdrawn(h.ctx, "evt-w1", withdrawnType, withdrawn("asg-1", 1, "PROMISE_REVISED")))
	next := release("asg-1", "n-1")
	next.ReleaseNo = 2
	h.must(h.svc.IssueOnRelease(h.ctx, "evt-r2", "FulfillmentReleasedToNode", next))
	second := string(purchaseorder.NumberFor("asg-1", 2))
	h.must(h.svc.CancelOnReleaseWithdrawn(h.ctx, "evt-w2", withdrawnType, withdrawn("asg-1", 1, "PROMISE_REVISED")))
	if h.order(first).State != "CANCELLED" || h.order(second).State != "ISSUED" {
		t.Fatalf("release 1 = %s, release 2 = %s", h.order(first).State, h.order(second).State)
	}
}

func TestCancelOnReleaseWithdrawnIsIdempotentOnTheEventID(t *testing.T) {
	h := newHarness(t, usecases.Config{})
	h.onboard("V-1")
	h.vendorNode("n-1", "V-1", 60)
	h.issue("asg-1", "n-1")
	h.must(h.svc.CancelOnReleaseWithdrawn(h.ctx, "evt-w1", withdrawnType, withdrawn("asg-1", 1, "PROMISE_REVISED")))
	after := h.store.Counts()
	h.must(h.svc.CancelOnReleaseWithdrawn(h.ctx, "evt-w1", withdrawnType, withdrawn("asg-1", 1, "PROMISE_REVISED")))
	if c := h.store.Counts(); c != after {
		t.Fatalf("a replayed CloudEvents id changes nothing: %+v vs %+v", c, after)
	}
}

func TestWithdrawnCancelReasonIsSanitised(t *testing.T) {
	long := strings.Repeat("é", purchaseorder.MaxReason+50)
	for name, tc := range map[string]struct{ in, want string }{
		"blank":    {"  ", "release withdrawn"},
		"empty":    {"", "release withdrawn"},
		"control":  {"a\tb\nc", "release withdrawn: a b c"},
		"padded":   {"  gone  ", "release withdrawn: gone"},
		"too long": {long, string([]rune("release withdrawn: " + long)[:purchaseorder.MaxReason])},
	} {
		h := newHarness(t, usecases.Config{})
		h.onboard("V-1")
		h.vendorNode("n-1", "V-1", 60)
		po := h.issue("asg-1", "n-1")
		h.must(h.svc.CancelOnReleaseWithdrawn(h.ctx, "evt-w", withdrawnType, withdrawn("asg-1", 1, tc.in)))
		if got := h.cancelReason(po); got != tc.want {
			t.Errorf("%s: reason = %q, want %q", name, got, tc.want)
		}
	}
}
