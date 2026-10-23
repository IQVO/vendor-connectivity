package usecases_test

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/claudioed/vendor-connectivity/internal/application/ports"
	"github.com/claudioed/vendor-connectivity/internal/application/requests"
	"github.com/claudioed/vendor-connectivity/internal/application/usecases"
	"github.com/claudioed/vendor-connectivity/internal/domain/purchaseorder"
	"github.com/claudioed/vendor-connectivity/internal/domain/shared"
	"github.com/claudioed/vendor-connectivity/internal/domain/vendor"
)

func TestOnboardVendorPublishesOnceAndIsIdempotent(t *testing.T) {
	h := newHarness(t, usecases.Config{})
	in := requests.OnboardVendor{VendorID: "V-1", Name: "Warehouse Systems", AckWindowMinutes: 240}
	acct, created, err := h.svc.OnboardVendor(h.ctx, in)
	if err != nil || !created || acct.State != "ACTIVE" || acct.AckWindowMinutes != 240 || acct.Name != "Warehouse Systems" {
		t.Fatalf("onboard: %+v %v %v", acct, created, err)
	}
	again, created, err := h.svc.OnboardVendor(h.ctx, requests.OnboardVendor{VendorID: "V-1", Name: "Other", AckWindowMinutes: 120})
	if err != nil || created || again != acct {
		t.Fatalf("a second onboarding returns the account unchanged: %+v %v %v", again, created, err)
	}
	if got := h.eventNames(); !reflect.DeepEqual(got, []string{"VendorOnboarded"}) {
		t.Fatalf("events = %v", got)
	}
	got, err := h.svc.GetVendor(h.ctx, "V-1")
	if err != nil || got != acct {
		t.Fatalf("GetVendor = %+v %v", got, err)
	}
	if _, err := h.svc.GetVendor(h.ctx, "V-9"); !errors.Is(err, ports.ErrNotFound) {
		t.Fatalf("unknown vendor: %v", err)
	}
}

func TestOnboardVendorRefusals(t *testing.T) {
	tests := map[string]struct {
		in   requests.OnboardVendor
		want error
	}{
		"window low":  {requests.OnboardVendor{VendorID: "V-1", Name: "n", AckWindowMinutes: 59}, vendor.ErrAckWindowOutOfRange},
		"window high": {requests.OnboardVendor{VendorID: "V-1", Name: "n", AckWindowMinutes: 1441}, vendor.ErrAckWindowOutOfRange},
		"bad id":      {requests.OnboardVendor{VendorID: "a b", Name: "n", AckWindowMinutes: 60}, shared.ErrValidation},
		"blank name":  {requests.OnboardVendor{VendorID: "V-1", Name: " ", AckWindowMinutes: 60}, shared.ErrValidation},
	}
	for name, tc := range tests {
		h := newHarness(t, usecases.Config{})
		if _, _, err := h.svc.OnboardVendor(h.ctx, tc.in); !errors.Is(err, tc.want) {
			t.Errorf("%s: err = %v", name, err)
		}
		if c := h.store.Counts(); c.Vendors != 0 || c.OutboxEvents != 0 {
			t.Errorf("%s: a refused onboarding stores nothing: %+v", name, c)
		}
	}
}

func TestSuspendAndReinstateVendor(t *testing.T) {
	h := newHarness(t, usecases.Config{})
	h.onboard("V-1")
	acct, err := h.svc.SuspendVendor(h.ctx, "V-1", "contract review")
	if err != nil || acct.State != "SUSPENDED" {
		t.Fatalf("suspend: %+v %v", acct, err)
	}
	if _, err := h.svc.SuspendVendor(h.ctx, "V-1", "again"); err != nil {
		t.Fatalf("suspending a suspended vendor is a no-op: %v", err)
	}
	acct, err = h.svc.ReinstateVendor(h.ctx, "V-1")
	if err != nil || acct.State != "ACTIVE" {
		t.Fatalf("reinstate: %+v %v", acct, err)
	}
	if _, err := h.svc.ReinstateVendor(h.ctx, "V-1"); err != nil {
		t.Fatalf("reinstating an active vendor is a no-op: %v", err)
	}
	if got := h.eventNames(); !reflect.DeepEqual(got, []string{"VendorOnboarded", "VendorSuspended", "VendorReinstated"}) {
		t.Fatalf("events = %v", got)
	}
	for _, call := range []func() error{
		func() error { _, err := h.svc.SuspendVendor(h.ctx, "V-9", ""); return err },
		func() error { _, err := h.svc.ReinstateVendor(h.ctx, "V-9"); return err },
	} {
		if err := call(); !errors.Is(err, ports.ErrNotFound) {
			t.Errorf("unknown vendor: %v", err)
		}
	}
	if _, err := h.svc.SuspendVendor(h.ctx, "V-1", " bad"); !errors.Is(err, shared.ErrValidation) {
		t.Errorf("bad reason: %v", err)
	}
}

func TestSuspendedVendorWritesAreRefusedButReadsWork(t *testing.T) {
	h := newHarness(t, usecases.Config{})
	h.onboard("V-1")
	h.vendorNode("n-1", "V-1", 60)
	po := h.issue("asg-1", "n-1")
	_, err := h.svc.SuspendVendor(h.ctx, "V-1", "")
	h.must(err)
	before := h.store.Counts()
	writes := map[string]func() error{
		"acknowledgement": func() error {
			_, err := h.svc.SubmitAcknowledgement(h.ctx, ackAll("V-1", po, "ACCEPT", ""))
			return err
		},
		"label": func() error {
			_, err := h.svc.RequestLabel(h.ctx, requests.LabelRequest{VendorID: "V-1", PONumber: po, Packages: []requests.LabelPackage{{PackageNo: 1}}})
			return err
		},
		"shipment": func() error {
			_, err := h.svc.SubmitShipmentConfirmation(h.ctx, requests.ShipmentConfirmation{VendorID: "V-1", PONumber: po, ShippedAt: t0, TrackingNumbers: []string{"T"}})
			return err
		},
		"registration": func() error { _, err := h.svc.RegisterVendorNode(h.ctx, registration()); return err },
		"profile":      func() error { _, err := h.svc.DeclareNodeProfile(h.ctx, "n-1", validProfile()); return err },
		"inventory":    func() error { _, err := h.svc.SubmitInventory(h.ctx, validFeed()); return err },
	}
	for name, call := range writes {
		if err := call(); !errors.Is(err, vendor.ErrVendorSuspended) {
			t.Errorf("%s: err = %v, want ErrVendorSuspended", name, err)
		}
	}
	if after := h.store.Counts(); after != before {
		t.Fatalf("refused writes change nothing: %+v vs %+v", after, before)
	}
	if _, err := h.svc.GetPurchaseOrder(h.ctx, "V-1", po); err != nil {
		t.Fatalf("reads continue while suspended: %v", err)
	}
	if _, err := h.svc.ListVendorNodes(h.ctx, "V-1"); err != nil {
		t.Fatalf("node list continues while suspended: %v", err)
	}
}

func TestIssueOnReleaseComputesAckBy(t *testing.T) {
	tests := []struct {
		name     string
		handling int
		shipBy   time.Duration
		wantAck  time.Duration
	}{
		{"window bound", 60, 8 * time.Hour, 4 * time.Hour},
		{"ship-by bound", 120, 5 * time.Hour, 3 * time.Hour},
		{"no profile yet means handling 0", 0, 3 * time.Hour, 3 * time.Hour},
	}
	for _, tc := range tests {
		h := newHarness(t, usecases.Config{})
		h.onboard("V-1")
		h.vendorNode("n-1", "V-1", tc.handling)
		in := release("asg-1", "n-1")
		in.RequiredShipBy = t0.Add(tc.shipBy)
		h.must(h.svc.IssueOnRelease(h.ctx, "evt-r", "FulfillmentReleasedToNode", in))
		o := h.order(string(purchaseorder.NumberFor("asg-1", 1)))
		if !o.AckBy.Equal(t0.Add(tc.wantAck)) || o.State != "ISSUED" || o.VendorID != "V-1" || o.NodeID != "n-1" || len(o.Lines) != 2 {
			t.Errorf("%s: order = %+v", tc.name, o)
		}
		if got := h.eventNames(); got[len(got)-1] != "PurchaseOrderIssued" {
			t.Errorf("%s: events = %v", tc.name, got)
		}
	}
}

func TestIssueOnReleaseIsIdempotent(t *testing.T) {
	h := newHarness(t, usecases.Config{})
	h.onboard("V-1")
	h.vendorNode("n-1", "V-1", 60)
	in := release("asg-1", "n-1")
	h.must(h.svc.IssueOnRelease(h.ctx, "rel-1", "FulfillmentReleasedToNode", in))
	after := h.store.Counts()
	h.must(h.svc.IssueOnRelease(h.ctx, "rel-1", "FulfillmentReleasedToNode", in)) // same CloudEvents id
	h.must(h.svc.IssueOnRelease(h.ctx, "rel-2", "FulfillmentReleasedToNode", in)) // same release, new id
	c := h.store.Counts()
	if c.Orders != 1 || c.OutboxEvents != after.OutboxEvents || c.VersionSum != after.VersionSum {
		t.Fatalf("redelivery must change nothing: %+v vs %+v", c, after)
	}
	next := in
	next.ReleaseNo = 2
	h.must(h.svc.IssueOnRelease(h.ctx, "rel-3", "FulfillmentReleasedToNode", next))
	if h.store.Counts().Orders != 2 {
		t.Fatal("a new release is a new purchase order")
	}
}

func TestIssueOnReleaseUnknownNodeIsRetryable(t *testing.T) {
	h := newHarness(t, usecases.Config{})
	h.onboard("V-1")
	err := h.svc.IssueOnRelease(h.ctx, "rel-1", "FulfillmentReleasedToNode", release("asg-1", "n-1"))
	if !errors.Is(err, ports.ErrNodeNotKnown) {
		t.Fatalf("err = %v, want ErrNodeNotKnown", err)
	}
	if c := h.store.Counts(); c.Inbox != 0 || c.Orders != 0 {
		t.Fatalf("nothing is stored and the event id is not claimed: %+v", c)
	}
	h.vendorNode("n-1", "V-1", 60)
	h.must(h.svc.IssueOnRelease(h.ctx, "rel-1", "FulfillmentReleasedToNode", release("asg-1", "n-1")))
	if h.store.Counts().Orders != 1 {
		t.Fatal("the retry of the same event issues the order once the node is known")
	}
}

func TestIssueOnReleaseIgnoresWhatIsNotAVendorOrder(t *testing.T) {
	h := newHarness(t, usecases.Config{})
	h.onboard("V-1")
	h.must(h.svc.OnNodeRegistered(h.ctx, "evt-n", "NodeRegistered", requests.NodeRegisteredFacts{NodeID: "n-net", NodeType: "DELIVERY_STATION", Operator: "NETWORK"}))
	h.vendorNode("n-ghost", "V-ghost", 0) // vendor without an account
	bad := release("asg-bad", "n-ghost")
	noLines := release("asg-nolines", "n-ghost")
	noLines.Lines = nil
	h.vendorNode("n-1", "V-1", 0)
	noLines.NodeID = "n-1"
	for i, in := range []requests.ReleasedToNode{release("asg-net", "n-net"), bad, noLines} {
		if err := h.svc.IssueOnRelease(h.ctx, "evt-ign-"+string(rune('a'+i)), "FulfillmentReleasedToNode", in); err != nil {
			t.Errorf("release %d: %v", i, err)
		}
	}
	if c := h.store.Counts(); c.Orders != 0 || c.OutboxEvents != 1 {
		t.Fatalf("no order, no event beyond VendorOnboarded: %+v", c)
	}
}

func TestCancelOnSourcingCancel(t *testing.T) {
	h := newHarness(t, usecases.Config{})
	h.onboard("V-1")
	h.vendorNode("n-1", "V-1", 60)
	po := h.issue("asg-1", "n-1")
	h.must(h.svc.CancelOnSourcingCancel(h.ctx, "evt-c1", "FulfillmentCancelled", requests.FulfillmentCancelled{AssignmentID: "asg-1", Reason: "customer cancelled"}))
	if o := h.order(po); o.State != "CANCELLED" {
		t.Fatalf("state = %s", o.State)
	}
	if got := h.eventNames(); got[len(got)-1] != "PurchaseOrderCancelled" {
		t.Fatalf("events = %v", got)
	}
	n := h.store.Counts().OutboxEvents
	h.must(h.svc.CancelOnSourcingCancel(h.ctx, "evt-c2", "FulfillmentCancelled", requests.FulfillmentCancelled{AssignmentID: "asg-1"}))
	h.must(h.svc.CancelOnSourcingCancel(h.ctx, "evt-c3", "FulfillmentCancelled", requests.FulfillmentCancelled{AssignmentID: "asg-unknown"}))
	if h.store.Counts().OutboxEvents != n {
		t.Fatal("a cancelled or unknown order publishes nothing more")
	}
}

func TestCancelAfterAcknowledgementLeavesTheOrder(t *testing.T) {
	h := newHarness(t, usecases.Config{})
	h.onboard("V-1")
	h.vendorNode("n-1", "V-1", 60)
	po := h.issue("asg-1", "n-1")
	h.accept("V-1", po)
	n := h.store.Counts().OutboxEvents
	h.must(h.svc.CancelOnSourcingCancel(h.ctx, "evt-c1", "FulfillmentCancelled", requests.FulfillmentCancelled{AssignmentID: "asg-1", Reason: "late"}))
	if o := h.order(po); o.State != "ACCEPTED" || h.store.Counts().OutboxEvents != n {
		t.Fatalf("an accepted order is not cancelled and nothing is published: %+v", o)
	}
}

func TestCancelReasonIsSanitised(t *testing.T) {
	long := strings.Repeat("é", purchaseorder.MaxReason+50)
	for name, tc := range map[string]struct{ in, want string }{
		"blank":    {"  ", "cancelled by order-sourcing"},
		"control":  {"a\tb\nc", "a b c"},
		"padded":   {"  gone  ", "gone"},
		"too long": {long, strings.Repeat("é", purchaseorder.MaxReason)},
	} {
		h := newHarness(t, usecases.Config{})
		h.onboard("V-1")
		h.vendorNode("n-1", "V-1", 60)
		po := h.issue("asg-1", "n-1")
		h.must(h.svc.CancelOnSourcingCancel(h.ctx, "evt-c", "FulfillmentCancelled", requests.FulfillmentCancelled{AssignmentID: "asg-1", Reason: tc.in}))
		o, err := h.store.Orders().Get(h.ctx, purchaseorder.Number(po))
		h.must(err)
		if o.CancelReason() != tc.want {
			t.Errorf("%s: reason = %q", name, o.CancelReason())
		}
	}
}

func TestExpireUnacknowledged(t *testing.T) {
	h := newHarness(t, usecases.Config{})
	h.onboard("V-1")
	h.vendorNode("n-1", "V-1", 60)
	late := h.issue("asg-late", "n-1")
	acked := h.issue("asg-acked", "n-1")
	h.accept("V-1", acked)
	if n, err := h.svc.ExpireUnacknowledged(h.ctx); err != nil || n != 0 {
		t.Fatalf("nothing is past ackBy yet: %d %v", n, err)
	}
	h.clock.Advance(4*time.Hour + time.Second)
	n, err := h.svc.ExpireUnacknowledged(h.ctx)
	if err != nil || n != 1 {
		t.Fatalf("one order expires: %d %v", n, err)
	}
	if h.order(late).State != "EXPIRED" || h.order(acked).State != "ACCEPTED" {
		t.Fatal("only the unacknowledged order expires")
	}
	if got := h.eventNames(); got[len(got)-1] != "PurchaseOrderExpired" {
		t.Fatalf("events = %v", got)
	}
	if n, _ := h.svc.ExpireUnacknowledged(h.ctx); n != 0 {
		t.Fatal("an expired order is not listed again")
	}
}

func TestPurgeSubmissionsKeepsRecentOnes(t *testing.T) {
	h := newHarness(t, usecases.Config{SubmissionRetention: 24 * time.Hour})
	h.onboard("V-1")
	h.vendorNode("n-1", "V-1", 60)
	po := h.issue("asg-1", "n-1")
	h.accept("V-1", po)
	if n, err := h.svc.PurgeSubmissions(h.ctx); err != nil || n != 0 {
		t.Fatalf("recent: %d %v", n, err)
	}
	h.clock.Advance(25 * time.Hour)
	if n, err := h.svc.PurgeSubmissions(h.ctx); err != nil || n != 1 {
		t.Fatalf("old: %d %v", n, err)
	}
}
