package purchaseorder

import (
	"strings"
	"testing"
	"time"
)

// Each transition moves the aggregate version forward by exactly one.
func TestEveryTransitionBumpsTheVersionByOne(t *testing.T) {
	steps := map[string]func(t *testing.T) (*PurchaseOrder, func() error){
		"accept": func(t *testing.T) (*PurchaseOrder, func() error) {
			p := inState(t, AckProcessing)
			return p, func() error { _, err := p.ApplyAckOutcome(true, t0.Add(time.Minute)); return err }
		},
		"reject": func(t *testing.T) (*PurchaseOrder, func() error) {
			p := issue(t)
			if _, err := p.SubmitAcknowledgement(fullAck(Reject, OutOfStock, "tx-r"), t0); err != nil {
				t.Fatal(err)
			}
			return p, func() error { _, err := p.ApplyAckOutcome(true, t0.Add(time.Minute)); return err }
		},
		"confirm": func(t *testing.T) (*PurchaseOrder, func() error) {
			p := inState(t, Accepted)
			if err := p.AddLabels([]Label{label1, label2}); err != nil {
				t.Fatal(err)
			}
			return p, func() error {
				_, err := p.SubmitShipmentConfirmation(ShipInput{TrackingNumbers: []string{"BR1", "BR2"}, ShippedAt: t0.Add(time.Hour), TransactionID: "tx-s"})
				return err
			}
		},
		"ship": func(t *testing.T) (*PurchaseOrder, func() error) {
			p := inState(t, ShipmentProcessing)
			return p, func() error { _, err := p.ApplyShipmentOutcome(true, t0.Add(time.Hour)); return err }
		},
	}
	for name, mk := range steps {
		p, do := mk(t)
		before := p.Version()
		if err := do(); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if p.Version() != before+1 {
			t.Errorf("%s: version %d -> %d", name, before, p.Version())
		}
	}
}

func TestPackageNumberBoundaryIsInclusive(t *testing.T) {
	p := inState(t, Accepted)
	if err := p.AddLabels([]Label{{PackageNo: MaxPackageNo, LabelRef: "l", TrackingNumber: "T", Carrier: "c"}}); err != nil {
		t.Fatalf("MaxPackageNo is allowed: %v", err)
	}
	if err := p.AddLabels([]Label{{PackageNo: 1, LabelRef: "l", TrackingNumber: "T1", Carrier: "c"}}); err != nil {
		t.Fatalf("package 1 is allowed: %v", err)
	}
}

func TestZeroHandlingTimeIsAllowed(t *testing.T) {
	in := newOrderInput()
	in.HandlingTime = 0
	if _, _, err := Issue(in, t0); err != nil {
		t.Fatalf("a node with no handling time is valid: %v", err)
	}
	in.AckWindow = 0
	if _, _, err := Issue(in, t0); err == nil {
		t.Fatal("a zero ack window is not")
	}
}

func TestRestoreNamesTheMisnumberedLine(t *testing.T) {
	s := inState(t, Issued).Snapshot()
	s.Lines[1].LineNo = 5
	_, err := Restore(s)
	if err == nil || !strings.Contains(err.Error(), "line 2 is numbered 5") {
		t.Fatalf("err = %v", err)
	}
}
