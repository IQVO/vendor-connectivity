package purchaseorder

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/claudioed/vendor-connectivity/internal/domain/shared"
)

func TestLabelPlanByState(t *testing.T) {
	for _, st := range allStates {
		p := inState(t, st)
		have, missing, err := p.LabelPlan([]int{1, 2})
		switch st {
		case Accepted:
			assertPlan(t, st, have, missing, err, nil, []int{1, 2})
		case Issued, AckProcessing:
			if !errors.Is(err, ErrLabelBeforeAcceptance) {
				t.Errorf("%s: err = %v", st, err)
			}
		case ShipmentProcessing, Shipped:
			assertPlan(t, st, have, missing, err, []Label{label1, label2}, nil)
			if _, _, err := p.LabelPlan([]int{3}); !errors.Is(err, shared.ErrIllegalTransition) {
				t.Errorf("%s new package: %v", st, err)
			}
		default:
			if !errors.Is(err, shared.ErrIllegalTransition) {
				t.Errorf("%s: err = %v", st, err)
			}
		}
	}
}

func assertPlan(t *testing.T, st State, have []Label, missing []int, err error, wantHave []Label, wantMissing []int) {
	t.Helper()
	if err != nil || !reflect.DeepEqual(have, wantHave) || !reflect.DeepEqual(missing, wantMissing) {
		t.Errorf("%s: plan = %v %v %v, want %v %v", st, have, missing, err, wantHave, wantMissing)
	}
}

func TestLabelPlanSplitsHaveAndMissing(t *testing.T) {
	p := inState(t, Accepted)
	if err := p.AddLabels([]Label{label2}); err != nil {
		t.Fatal(err)
	}
	have, missing, err := p.LabelPlan([]int{3, 2, 1})
	if err != nil || len(have) != 1 || have[0] != label2 || !reflect.DeepEqual(missing, []int{3, 1}) {
		t.Fatalf("plan = %v %v %v", have, missing, err)
	}
	have, missing, err = p.LabelPlan([]int{2})
	if err != nil || len(have) != 1 || len(missing) != 0 {
		t.Fatalf("replay in ACCEPTED = %v %v %v", have, missing, err)
	}
}

func TestLabelPlanRejectsBadPackageNumbers(t *testing.T) {
	tooMany := make([]int, MaxPackages+1)
	for i := range tooMany {
		tooMany[i] = i + 1
	}
	tests := map[string][]int{"none": nil, "zero": {0}, "negative": {-1}, "huge": {MaxPackageNo + 1}, "twice": {1, 1}, "too many": tooMany}
	for name, nos := range tests {
		p := inState(t, Accepted)
		if _, _, err := p.LabelPlan(nos); !errors.Is(err, shared.ErrValidation) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	p := inState(t, Accepted)
	if _, _, err := p.LabelPlan(tooMany[:MaxPackages]); err != nil {
		t.Errorf("MaxPackages packages: %v", err)
	}
	if _, _, err := p.LabelPlan([]int{MaxPackageNo}); err != nil {
		t.Errorf("MaxPackageNo: %v", err)
	}
}

func TestAddLabelsByState(t *testing.T) {
	for _, st := range allStates {
		p := inState(t, st)
		before := len(p.Labels())
		err := p.AddLabels([]Label{{PackageNo: 9, LabelRef: "l", TrackingNumber: "T9", Carrier: "c"}})
		var wantErr error
		wantAdded := 0
		switch st {
		case Accepted:
			wantErr, wantAdded = nil, 1
		case Issued, AckProcessing:
			wantErr = ErrLabelBeforeAcceptance
		default:
			wantErr = shared.ErrIllegalTransition
		}
		if wantErr == nil && err != nil || wantErr != nil && !errors.Is(err, wantErr) || len(p.Labels()) != before+wantAdded {
			t.Errorf("%s: err = %v labels %d", st, err, len(p.Labels()))
		}
	}
}

func TestAddLabelsOrdersAndDeduplicates(t *testing.T) {
	p := inState(t, Accepted)
	v := p.Version()
	if err := p.AddLabels([]Label{label2, label1}); err != nil || p.Version() != v+1 {
		t.Fatalf("add: %v v%d", err, p.Version())
	}
	if got := p.Labels(); !reflect.DeepEqual(got, []Label{label1, label2}) {
		t.Fatalf("labels must be ordered by packageNo: %v", got)
	}
	for name, batch := range map[string][]Label{"identical label": {label1}, "no labels": nil} {
		if err := p.AddLabels(batch); err != nil || p.Version() != v+1 {
			t.Fatalf("%s is a no-op: %v v%d", name, err, p.Version())
		}
	}
}

func TestAddLabelsRejectsBadAndClashingLabels(t *testing.T) {
	long := strings.Repeat("x", MaxText+1)
	tests := map[string]Label{
		"package zero":  {PackageNo: 0, LabelRef: "l", TrackingNumber: "T", Carrier: "c"},
		"package huge":  {PackageNo: MaxPackageNo + 1, LabelRef: "l", TrackingNumber: "T", Carrier: "c"},
		"no ref":        {PackageNo: 5, TrackingNumber: "T", Carrier: "c"},
		"no tracking":   {PackageNo: 5, LabelRef: "l", Carrier: "c"},
		"no carrier":    {PackageNo: 5, LabelRef: "l", TrackingNumber: "T"},
		"long ref":      {PackageNo: 5, LabelRef: long, TrackingNumber: "T", Carrier: "c"},
		"same package":  {PackageNo: 1, LabelRef: "other", TrackingNumber: "T", Carrier: "c"},
		"same tracking": {PackageNo: 5, LabelRef: "l", TrackingNumber: "BR1", Carrier: "c"},
	}
	for name, l := range tests {
		p := inState(t, Accepted)
		_ = p.AddLabels([]Label{label1})
		v := p.Version()
		if err := p.AddLabels([]Label{l}); !errors.Is(err, shared.ErrValidation) || len(p.Labels()) != 1 || p.Version() != v {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	p := inState(t, Accepted)
	for i := 1; i <= MaxPackages; i++ {
		l := Label{PackageNo: i, LabelRef: "l", TrackingNumber: fmt.Sprintf("T%d", i), Carrier: "c"}
		if err := p.AddLabels([]Label{l}); err != nil {
			t.Fatalf("package %d: %v", i, err)
		}
	}
	over := Label{PackageNo: MaxPackages + 1, LabelRef: "l", TrackingNumber: "Tover", Carrier: "c"}
	if err := p.AddLabels([]Label{over}); !errors.Is(err, shared.ErrValidation) {
		t.Errorf("label %d: %v", MaxPackages+1, err)
	}
	atomic := inState(t, Accepted)
	if err := atomic.AddLabels([]Label{label1, {PackageNo: 0}}); err == nil || len(atomic.Labels()) != 0 {
		t.Errorf("a batch with a bad label adds nothing: %v %v", err, atomic.Labels())
	}
}

func shipInput(nums ...string) ShipInput {
	return ShipInput{TrackingNumbers: nums, ShippedAt: t0.Add(2 * time.Hour), TransactionID: "tx-s"}
}

func TestSubmitShipmentConfirmationByState(t *testing.T) {
	for _, st := range allStates {
		p := inState(t, st)
		if st == Accepted {
			_ = p.AddLabels([]Label{label1, label2})
		}
		evs, err := p.SubmitShipmentConfirmation(shipInput("BR1", "BR2"))
		switch st {
		case Accepted:
			assertEvents(t, evs)
			assertPendingShipment(t, p, err)
		case Issued, AckProcessing:
			if !errors.Is(err, ErrConfirmWithoutLabel) {
				t.Errorf("%s: %v", st, err)
			}
		default:
			if !errors.Is(err, shared.ErrIllegalTransition) || p.State() != st {
				t.Errorf("%s: %v", st, err)
			}
		}
	}
}

func assertPendingShipment(t *testing.T, p *PurchaseOrder, err error) {
	t.Helper()
	sh, ok := p.Shipment()
	want := Shipment{TransactionID: "tx-s", ShippedAt: t0.Add(2 * time.Hour), TrackingNumbers: []string{"BR1", "BR2"}}
	if err != nil || p.State() != ShipmentProcessing || !ok || !reflect.DeepEqual(sh, want) {
		t.Errorf("accepted: err %v state %s shipment %+v", err, p.State(), sh)
	}
}

func TestSubmitShipmentConfirmationNeedsEveryLabelledPackage(t *testing.T) {
	tests := []struct {
		name   string
		labels []Label
		nums   []string
		ok     bool
	}{
		{"all labelled", []Label{label1, label2}, []string{"BR2", "BR1"}, true},
		{"single package", []Label{label1}, []string{"BR1"}, true},
		{"no labels", nil, []string{"BR1"}, false},
		{"missing one", []Label{label1, label2}, []string{"BR1"}, false},
		{"unlabelled number", []Label{label1}, []string{"BR1", "BR9"}, false},
		{"only unlabelled", []Label{label1}, []string{"BR9"}, false},
		{"duplicate number", []Label{label1, label2}, []string{"BR1", "BR1"}, false},
		{"empty list", []Label{label1}, nil, false},
	}
	for _, tc := range tests {
		p := inState(t, Accepted)
		_ = p.AddLabels(tc.labels)
		_, err := p.SubmitShipmentConfirmation(shipInput(tc.nums...))
		if tc.ok && err != nil {
			t.Errorf("%s: %v", tc.name, err)
		}
		if !tc.ok && (!errors.Is(err, ErrConfirmWithoutLabel) || p.State() != Accepted) {
			t.Errorf("%s: err = %v state %s", tc.name, err, p.State())
		}
	}
	p := inState(t, Accepted)
	_ = p.AddLabels([]Label{label1})
	for name, in := range map[string]ShipInput{
		"no shippedAt":    {TrackingNumbers: []string{"BR1"}, TransactionID: "tx-s"},
		"bad transaction": {TrackingNumbers: []string{"BR1"}, ShippedAt: t0, TransactionID: "a b"},
	} {
		if _, err := p.SubmitShipmentConfirmation(in); !errors.Is(err, shared.ErrValidation) || p.State() != Accepted {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestApplyShipmentOutcome(t *testing.T) {
	at := t0.Add(4 * time.Hour)
	p := inState(t, ShipmentProcessing)
	evs, err := p.ApplyShipmentOutcome(true, at)
	if err != nil || p.State() != Shipped {
		t.Fatalf("success: %v %s", err, p.State())
	}
	assertEvents(t, evs, VendorShipmentConfirmed{Header: shared.Header{Version: p.Version(), At: at}, PONumber: p.Number(), AssignmentID: "asg-1",
		NodeID: "n-1", TrackingNumbers: []string{"BR2", "BR1"}, ShippedAt: t0.Add(2 * time.Hour), RequiredShipBy: p.RequiredShipBy()})
	assertEventMeta(t, evs[0], "VendorShipmentConfirmed", string(p.Number()))
}

func TestApplyShipmentOutcomeFailureReturnsToAccepted(t *testing.T) {
	f := inState(t, ShipmentProcessing)
	v := f.Version()
	evs, err := f.ApplyShipmentOutcome(false, t0.Add(4*time.Hour))
	if err != nil || f.State() != Accepted || f.Version() != v+1 {
		t.Fatalf("failure: %v %v %s", evs, err, f.State())
	}
	assertEvents(t, evs)
	if _, ok := f.Shipment(); ok {
		t.Fatal("a failed confirmation is forgotten")
	}
	if len(f.Labels()) != 2 {
		t.Fatal("labels survive a failed confirmation")
	}
	if _, err := f.SubmitShipmentConfirmation(shipInput("BR1", "BR2")); err != nil {
		t.Fatalf("the vendor can confirm again: %v", err)
	}
}

func TestApplyShipmentOutcomeOnlyWhileProcessing(t *testing.T) {
	for _, st := range allStates {
		if st == ShipmentProcessing {
			continue
		}
		o := inState(t, st)
		if _, err := o.ApplyShipmentOutcome(true, t0); !errors.Is(err, shared.ErrIllegalTransition) || o.State() != st {
			t.Errorf("%s: %v", st, err)
		}
	}
}

func TestSnapshotRoundTripInEveryState(t *testing.T) {
	for _, st := range allStates {
		p := inState(t, st)
		r, err := Restore(p.Snapshot())
		if err != nil {
			t.Fatalf("%s: %v", st, err)
		}
		if !reflect.DeepEqual(r.Snapshot(), p.Snapshot()) {
			t.Errorf("%s: round trip changed the order:\n%+v\n%+v", st, p.Snapshot(), r.Snapshot())
		}
	}
}

func TestSnapshotIsACopy(t *testing.T) {
	p := inState(t, ShipmentProcessing)
	s := p.Snapshot()
	s.Lines[0].Quantity = 99
	s.Labels[0].LabelRef = "changed"
	s.Ack.Decision = Reject
	s.Shipment.TrackingNumbers[0] = "changed"
	if p.Lines()[0].Quantity != 2 || p.Labels()[0].LabelRef == "changed" {
		t.Fatal("slices must be copied")
	}
	if a, _ := p.Ack(); a.Decision != Accept {
		t.Fatal("ack must be copied")
	}
	if sh, _ := p.Shipment(); sh.TrackingNumbers[0] == "changed" {
		t.Fatal("shipment must be copied")
	}
}

func TestRestoreRejectsCorruptSnapshots(t *testing.T) {
	good := func(st State) Snapshot { return inState(t, st).Snapshot() }
	tests := map[string]func() Snapshot{
		"number":           func() Snapshot { s := good(Issued); s.Number = "PO-x"; return s },
		"vendor":           func() Snapshot { s := good(Issued); s.VendorID = ""; return s },
		"tier":             func() Snapshot { s := good(Issued); s.Tier = "X"; return s },
		"group":            func() Snapshot { s := good(Issued); s.GroupNo = -1; return s },
		"release":          func() Snapshot { s := good(Issued); s.ReleaseNo = 0; return s },
		"times":            func() Snapshot { s := good(Issued); s.AckBy = time.Time{}; return s },
		"version":          func() Snapshot { s := good(Issued); s.Version = 0; return s },
		"no lines":         func() Snapshot { s := good(Issued); s.Lines = nil; return s },
		"line number":      func() Snapshot { s := good(Issued); s.Lines[1].LineNo = 5; return s },
		"line quantity":    func() Snapshot { s := good(Issued); s.Lines[0].Quantity = 0; return s },
		"line listing":     func() Snapshot { s := good(Issued); s.Lines[0].ListingID = ""; return s },
		"state":            func() Snapshot { s := good(Issued); s.State = "LOST"; return s },
		"issued with ack":  func() Snapshot { s := good(AckProcessing); s.State = Issued; return s },
		"ack missing":      func() Snapshot { s := good(AckProcessing); s.Ack = nil; return s },
		"ack tx":           func() Snapshot { s := good(AckProcessing); s.Ack.TransactionID = ""; return s },
		"ack decision":     func() Snapshot { s := good(AckProcessing); s.Ack.Decision = "X"; return s },
		"accept reason":    func() Snapshot { s := good(AckProcessing); s.Ack.ReasonCode = OutOfStock; return s },
		"reject no reason": func() Snapshot { s := good(Rejected); s.Ack.ReasonCode = ""; return s },
		"rejected accept":  func() Snapshot { s := good(Rejected); s.Ack = &Ack{Decision: Accept, TransactionID: "tx"}; return s },
		"accepted reject": func() Snapshot {
			s := good(Accepted)
			s.Ack = &Ack{Decision: Reject, ReasonCode: OutOfStock, TransactionID: "tx"}
			return s
		},
		"issued label":  func() Snapshot { s := good(Issued); s.Labels = []Label{label1}; return s },
		"label invalid": func() Snapshot { s := good(Accepted); s.Labels = []Label{{PackageNo: 0}}; return s },
		"label twice":   func() Snapshot { s := good(Accepted); s.Labels = []Label{label1, label1}; return s },
		"tracking twice": func() Snapshot {
			s := good(Accepted)
			s.Labels = []Label{label1, {PackageNo: 2, LabelRef: "x", TrackingNumber: "BR1", Carrier: "c"}}
			return s
		},
		"too many labels": func() Snapshot {
			s := good(Accepted)
			for i := 1; i <= MaxPackages+1; i++ {
				s.Labels = append(s.Labels, Label{PackageNo: i, LabelRef: "l", TrackingNumber: fmt.Sprintf("T%d", i), Carrier: "c"})
			}
			return s
		},
		"shipping no labels":  func() Snapshot { s := good(ShipmentProcessing); s.Labels = nil; return s },
		"shipment missing":    func() Snapshot { s := good(ShipmentProcessing); s.Shipment = nil; return s },
		"shipment tx":         func() Snapshot { s := good(Shipped); s.Shipment.TransactionID = ""; return s },
		"shipment time":       func() Snapshot { s := good(Shipped); s.Shipment.ShippedAt = time.Time{}; return s },
		"shipment tracking":   func() Snapshot { s := good(Shipped); s.Shipment.TrackingNumbers = []string{"BR1"}; return s },
		"accepted shipment":   func() Snapshot { s := good(ShipmentProcessing); s.State = Accepted; return s },
		"cancelled no reason": func() Snapshot { s := good(Cancelled); s.CancelReason = ""; return s },
		"cancelled no time":   func() Snapshot { s := good(Cancelled); s.CancelledAt = time.Time{}; return s },
		"issued cancel":       func() Snapshot { s := good(Issued); s.CancelReason = "x"; return s },
		"issued cancel time":  func() Snapshot { s := good(Issued); s.CancelledAt = t0; return s },
	}
	for name, mk := range tests {
		if _, err := Restore(mk()); !errors.Is(err, shared.ErrInvalidSnapshot) {
			t.Errorf("%s: err = %v, want ErrInvalidSnapshot", name, err)
		}
	}
	edge := good(Accepted)
	edge.Lines = []Line{{1, "lst-1", MaxQuantity}}
	edge.GroupNo, edge.ReleaseNo = 0, 1
	if _, err := Restore(edge); err != nil {
		t.Errorf("edge values: %v", err)
	}
	max := good(Accepted)
	for i := 1; i <= MaxPackages; i++ {
		max.Labels = append(max.Labels, Label{PackageNo: i, LabelRef: "l", TrackingNumber: fmt.Sprintf("T%d", i), Carrier: "c"})
	}
	if _, err := Restore(max); err != nil {
		t.Errorf("MaxPackages labels: %v", err)
	}
	lim := good(Issued)
	lim.GroupNo = MaxGroupNo
	if _, err := Restore(lim); err != nil {
		t.Errorf("MaxGroupNo: %v", err)
	}
}
