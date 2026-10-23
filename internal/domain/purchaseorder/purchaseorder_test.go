package purchaseorder

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/claudioed/vendor-connectivity/internal/domain/shared"
)

var t0 = time.Date(2026, 10, 22, 13, 0, 0, 0, time.UTC)

func newOrderInput() NewOrder {
	return NewOrder{
		AssignmentID: "asg-1", ReleaseNo: 1, VendorID: "V-WS", NodeID: "n-1", OrderID: "ord-1", GroupNo: 2, Tier: NextDay,
		RequiredShipBy: t0.Add(8 * time.Hour), AckWindow: 4 * time.Hour, HandlingTime: time.Hour,
		Lines: []NewLine{{ListingID: "lst-1", Quantity: 2}, {ListingID: "lst-2", Quantity: 5}},
	}
}

func issue(t *testing.T) *PurchaseOrder {
	t.Helper()
	p, _, err := Issue(newOrderInput(), t0)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	return p
}

func fullAck(d Decision, reason RejectReason, tx string) AckInput {
	return AckInput{Decision: d, ReasonCode: reason, TransactionID: tx, Lines: []AckLine{{LineNo: 1, Quantity: 2}, {LineNo: 2, Quantity: 5}}}
}

var label1 = Label{PackageNo: 1, LabelRef: "lbl-1", TrackingNumber: "BR1", Carrier: "carrier-a"}
var label2 = Label{PackageNo: 2, LabelRef: "lbl-2", TrackingNumber: "BR2", Carrier: "carrier-a"}

// inState drives a fresh order to the named state through the public API only.
func inState(t *testing.T, st State) *PurchaseOrder {
	t.Helper()
	p := issue(t)
	must := func(_ []shared.Event, err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("driving to %s: %v", st, err)
		}
	}
	switch st {
	case Issued:
	case AckProcessing:
		must(p.SubmitAcknowledgement(fullAck(Accept, "", "tx-a"), t0))
	case Accepted, ShipmentProcessing, Shipped:
		must(p.SubmitAcknowledgement(fullAck(Accept, "", "tx-a"), t0))
		must(p.ApplyAckOutcome(true, t0.Add(time.Minute)))
		if st != Accepted {
			if err := p.AddLabels([]Label{label2, label1}); err != nil {
				t.Fatal(err)
			}
			must(p.SubmitShipmentConfirmation(ShipInput{TrackingNumbers: []string{"BR2", "BR1"}, ShippedAt: t0.Add(2 * time.Hour), TransactionID: "tx-s"}))
		}
		if st == Shipped {
			must(p.ApplyShipmentOutcome(true, t0.Add(3*time.Hour)))
		}
	case Rejected:
		must(p.SubmitAcknowledgement(fullAck(Reject, OutOfStock, "tx-a"), t0))
		must(p.ApplyAckOutcome(true, t0.Add(time.Minute)))
	case Expired:
		must(p.Expire(p.AckBy().Add(time.Nanosecond)))
	case Cancelled:
		must(p.Cancel("customer cancelled", t0))
	}
	if p.State() != st {
		t.Fatalf("inState(%s) produced %s", st, p.State())
	}
	return p
}

var allStates = []State{Issued, AckProcessing, Accepted, Rejected, Expired, ShipmentProcessing, Shipped, Cancelled}

func TestComputeAckByIsTheEarlierOfWindowAndShipBy(t *testing.T) {
	issued := t0
	tests := []struct {
		name     string
		window   time.Duration
		shipBy   time.Time
		handling time.Duration
		want     time.Time
	}{
		{"window earlier", 2 * time.Hour, t0.Add(10 * time.Hour), time.Hour, t0.Add(2 * time.Hour)},
		{"ship-by earlier", 6 * time.Hour, t0.Add(5 * time.Hour), time.Hour, t0.Add(4 * time.Hour)},
		{"equal", 3 * time.Hour, t0.Add(4 * time.Hour), time.Hour, t0.Add(3 * time.Hour)},
		{"zero handling", 4 * time.Hour, t0.Add(3 * time.Hour), 0, t0.Add(3 * time.Hour)},
		{"ship-by before issue", 4 * time.Hour, t0.Add(30 * time.Minute), time.Hour, t0.Add(-30 * time.Minute)},
	}
	for _, tc := range tests {
		if got := ComputeAckBy(issued, tc.window, tc.shipBy, tc.handling); !got.Equal(tc.want) {
			t.Errorf("%s: ackBy = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestNumberForIsDeterministicAndDistinct(t *testing.T) {
	a, b := NumberFor("asg-1", 1), NumberFor("asg-1", 1)
	if a != b || len(a) != len("PO-")+16 || a[:3] != "PO-" {
		t.Fatalf("number = %q", a)
	}
	if a == NumberFor("asg-1", 2) || a == NumberFor("asg-2", 1) || NumberFor("asg-1", 11) == NumberFor("asg-11", 1) {
		t.Fatal("distinct releases need distinct numbers")
	}
}

func TestIssueBuildsTheOrderAndRaisesTheEvent(t *testing.T) {
	p, evs, err := Issue(newOrderInput(), t0)
	if err != nil {
		t.Fatal(err)
	}
	wantLines := []Line{{1, "lst-1", 2}, {2, "lst-2", 5}}
	assertSnapshot(t, p, Snapshot{
		Number: NumberFor("asg-1", 1), VendorID: "V-WS", NodeID: "n-1", AssignmentID: "asg-1", OrderID: "ord-1", GroupNo: 2, Tier: NextDay,
		ReleaseNo: 1, IssuedAt: t0, RequiredShipBy: t0.Add(8 * time.Hour), AckBy: t0.Add(4 * time.Hour), Lines: wantLines, State: Issued, Version: 1,
	})
	assertEvents(t, evs, PurchaseOrderIssued{
		Header: shared.Header{Version: 1, At: t0}, PONumber: p.Number(), VendorID: "V-WS", NodeID: "n-1", AssignmentID: "asg-1", ReleaseNo: 1,
		RequiredShipBy: t0.Add(8 * time.Hour), AckBy: t0.Add(4 * time.Hour), Lines: wantLines,
	})
	assertEventMeta(t, evs[0], "PurchaseOrderIssued", string(p.Number()))
	if p.ID() != p.Number() {
		t.Fatal("ID must be the poNumber")
	}
}

func assertSnapshot(t *testing.T, p *PurchaseOrder, want Snapshot) {
	t.Helper()
	if got := p.Snapshot(); !reflect.DeepEqual(got, want) {
		t.Fatalf("snapshot:\n got %+v\nwant %+v", got, want)
	}
}

func assertEvents(t *testing.T, got []shared.Event, want ...shared.Event) {
	t.Helper()
	if len(got) == 0 && len(want) == 0 {
		return
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("events:\n got %+v\nwant %+v", got, want)
	}
}

func assertEventMeta(t *testing.T, ev shared.Event, name, subject string) {
	t.Helper()
	got := [3]string{ev.EventName(), ev.Entity(), ev.SubjectID()}
	if want := [3]string{name, "purchaseorder", subject}; got != want {
		t.Fatalf("event meta = %v, want %v", got, want)
	}
}

func TestIssueRejectsBadInput(t *testing.T) {
	tests := map[string]func(*NewOrder){
		"zero window":       func(n *NewOrder) { n.AckWindow = 0 },
		"negative handling": func(n *NewOrder) { n.HandlingTime = -time.Second },
		"bad vendor":        func(n *NewOrder) { n.VendorID = "" },
		"bad node":          func(n *NewOrder) { n.NodeID = "a b" },
		"bad assignment":    func(n *NewOrder) { n.AssignmentID = "" },
		"bad order":         func(n *NewOrder) { n.OrderID = "" },
		"negative group":    func(n *NewOrder) { n.GroupNo = -1 },
		"huge group":        func(n *NewOrder) { n.GroupNo = MaxGroupNo + 1 },
		"bad tier":          func(n *NewOrder) { n.Tier = "WEEKLY" },
		"zero release":      func(n *NewOrder) { n.ReleaseNo = 0 },
		"huge release":      func(n *NewOrder) { n.ReleaseNo = MaxReleaseNo + 1 },
		"zero ship-by":      func(n *NewOrder) { n.RequiredShipBy = time.Time{} },
		"no lines":          func(n *NewOrder) { n.Lines = nil },
		"zero quantity":     func(n *NewOrder) { n.Lines[0].Quantity = 0 },
		"huge quantity":     func(n *NewOrder) { n.Lines[0].Quantity = MaxQuantity + 1 },
		"bad listing":       func(n *NewOrder) { n.Lines[1].ListingID = "" },
	}
	for name, mut := range tests {
		in := newOrderInput()
		in.Lines = append([]NewLine(nil), in.Lines...)
		mut(&in)
		if _, _, err := Issue(in, t0); !errors.Is(err, shared.ErrValidation) {
			t.Errorf("%s: err = %v, want ErrValidation", name, err)
		}
	}
	edge := newOrderInput()
	edge.GroupNo, edge.ReleaseNo = MaxGroupNo, MaxReleaseNo
	edge.Lines = []NewLine{{ListingID: "lst-1", Quantity: MaxQuantity}}
	if _, _, err := Issue(edge, t0); err != nil {
		t.Errorf("limits must be accepted: %v", err)
	}
	many := newOrderInput()
	many.Lines = nil
	for i := 0; i < MaxLines+1; i++ {
		many.Lines = append(many.Lines, NewLine{ListingID: "lst-1", Quantity: 1})
	}
	if _, _, err := Issue(many, t0); !errors.Is(err, shared.ErrValidation) {
		t.Errorf("too many lines: %v", err)
	}
	many.Lines = many.Lines[:MaxLines]
	if _, _, err := Issue(many, t0); err != nil {
		t.Errorf("MaxLines lines: %v", err)
	}
	g0 := newOrderInput()
	g0.GroupNo = 0
	if _, _, err := Issue(g0, t0); err != nil {
		t.Errorf("groupNo 0 (unset) is accepted: %v", err)
	}
}

func TestValidTierAndReason(t *testing.T) {
	for _, tier := range []Tier{SameDay, NextDay, TwoDay, Standard} {
		if !ValidTier(tier) {
			t.Errorf("tier %s", tier)
		}
	}
	for _, r := range []RejectReason{OutOfStock, UnknownListing, CannotMeetShipBy, Discontinued} {
		if !ValidReason(r) {
			t.Errorf("reason %s", r)
		}
	}
	if ValidTier("") || ValidTier("NOPE") || ValidReason("") || ValidReason("NOPE") {
		t.Error("unknown values must be invalid")
	}
}

func TestAccessors(t *testing.T) {
	p := issue(t)
	got := []any{p.VendorID(), p.NodeID(), p.AssignmentID(), p.OrderID(), p.GroupNo(), p.Tier(), p.ReleaseNo(), p.IssuedAt(), p.RequiredShipBy(), p.AckBy(), p.Version()}
	want := []any{"V-WS", "n-1", "asg-1", "ord-1", 2, NextDay, 1, t0, t0.Add(8 * time.Hour), t0.Add(4 * time.Hour), int64(1)}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("accessors = %v, want %v", got, want)
	}
}
