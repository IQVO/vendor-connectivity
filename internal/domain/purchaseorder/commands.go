package purchaseorder

import (
	"errors"
	"fmt"
	"time"

	"github.com/claudioed/vendor-connectivity/internal/domain/shared"
)

type ackRule int

const (
	ackNone ackRule = iota
	ackAny
	ackAccept
	ackReject
)

// stateRule says what a persisted order of a given state must (not) hold.
type stateRule struct {
	ack            ackRule
	labelsAllowed  bool
	labelsRequired bool
	shipment       bool
	cancelled      bool
}

var stateRules = map[State]stateRule{
	Issued:             {ack: ackNone},
	AckProcessing:      {ack: ackAny},
	Accepted:           {ack: ackAccept, labelsAllowed: true},
	Rejected:           {ack: ackReject},
	Expired:            {ack: ackNone},
	ShipmentProcessing: {ack: ackAccept, labelsAllowed: true, labelsRequired: true, shipment: true},
	Shipped:            {ack: ackAccept, labelsAllowed: true, labelsRequired: true, shipment: true},
	Cancelled:          {ack: ackNone, cancelled: true},
}

func checkState(s Snapshot) error {
	rule, ok := stateRules[s.State]
	if !ok {
		return errors.New("state is not a purchase order state")
	}
	if err := checkAck(s.Ack, rule.ack); err != nil {
		return err
	}
	if err := checkLabels(s.Labels, rule); err != nil {
		return err
	}
	if err := checkShipment(s, rule); err != nil {
		return err
	}
	return checkCancel(s, rule)
}

func checkAck(a *Ack, rule ackRule) error {
	if rule == ackNone {
		if a != nil {
			return errors.New("this state holds no acknowledgement")
		}
		return nil
	}
	if a == nil {
		return errors.New("this state needs the acknowledgement")
	}
	if !shared.ValidID(a.TransactionID) {
		return errors.New("acknowledgement transactionId is not a valid id")
	}
	switch a.Decision {
	case Accept:
		if a.ReasonCode != "" || rule == ackReject {
			return errors.New("an ACCEPT carries no reasonCode and this state needs a REJECT")
		}
	case Reject:
		if !ValidReason(a.ReasonCode) || rule == ackAccept {
			return errors.New("a REJECT carries a reasonCode and this state needs an ACCEPT")
		}
	default:
		return errors.New("decision must be ACCEPT or REJECT")
	}
	return nil
}

func checkLabels(labels []Label, rule stateRule) error {
	if len(labels) > 0 && !rule.labelsAllowed {
		return errors.New("this state holds no labels")
	}
	if len(labels) == 0 && rule.labelsRequired {
		return errors.New("this state needs at least one label")
	}
	if len(labels) > MaxPackages {
		return errors.New("too many labels")
	}
	pkgs, tracks := map[int]bool{}, map[string]bool{}
	for _, l := range labels {
		if err := checkLabel(l); err != nil {
			return err
		}
		if pkgs[l.PackageNo] || tracks[l.TrackingNumber] {
			return fmt.Errorf("package %d or its tracking number is labelled twice", l.PackageNo)
		}
		pkgs[l.PackageNo], tracks[l.TrackingNumber] = true, true
	}
	return nil
}

func checkLabel(l Label) error {
	if l.PackageNo < 1 || l.PackageNo > MaxPackageNo {
		return errors.New("packageNo out of range")
	}
	if !shared.ValidTextN(l.LabelRef, MaxText) || !shared.ValidTextN(l.TrackingNumber, MaxText) || !shared.ValidTextN(l.Carrier, MaxText) {
		return errors.New("a label needs labelRef, trackingNumber and carrier")
	}
	return nil
}

func checkShipment(s Snapshot, rule stateRule) error {
	if !rule.shipment {
		if s.Shipment != nil {
			return errors.New("this state holds no shipment confirmation")
		}
		return nil
	}
	sh := s.Shipment
	if sh == nil || !shared.ValidID(sh.TransactionID) || sh.ShippedAt.IsZero() {
		return errors.New("this state needs a shipment confirmation with transactionId and shippedAt")
	}
	if !sameTracking(sh.TrackingNumbers, s.Labels) {
		return errors.New("the confirmed tracking numbers are not exactly the labelled ones")
	}
	return nil
}

func checkCancel(s Snapshot, rule stateRule) error {
	if !rule.cancelled {
		if s.CancelReason != "" || !s.CancelledAt.IsZero() {
			return errors.New("this state holds no cancellation")
		}
		return nil
	}
	if !shared.ValidTextN(s.CancelReason, MaxReason) || s.CancelledAt.IsZero() {
		return errors.New("a cancelled order needs a reason and cancelledAt")
	}
	return nil
}

// sameTracking reports whether numbers (no duplicates) are exactly the
// tracking numbers of labels.
func sameTracking(numbers []string, labels []Label) bool {
	if len(numbers) != len(labels) {
		return false
	}
	want := map[string]bool{}
	for _, l := range labels {
		want[l.TrackingNumber] = true
	}
	seen := map[string]bool{}
	for _, n := range numbers {
		if !want[n] || seen[n] {
			return false
		}
		seen[n] = true
	}
	return true
}

// Expire moves an ISSUED order whose ackBy has passed to EXPIRED and raises
// PurchaseOrderExpired. An order still inside its window is not expired.
func (p *PurchaseOrder) Expire(now time.Time) ([]shared.Event, error) {
	if p.state != Issued {
		return nil, fmt.Errorf("%w: only an ISSUED order expires (state %s)", shared.ErrIllegalTransition, p.state)
	}
	if !now.After(p.ackBy) {
		return nil, fmt.Errorf("%w: ackBy has not passed", shared.ErrIllegalTransition)
	}
	p.state = Expired
	p.version++
	return []shared.Event{PurchaseOrderExpired{Header: shared.Header{Version: p.version, At: now.UTC()}, PONumber: p.number,
		AssignmentID: p.assignmentID, ReleaseNo: p.releaseNo, NodeID: p.nodeID, AckBy: p.ackBy}}, nil
}

// Cancel moves an ISSUED order to CANCELLED and raises PurchaseOrderCancelled.
// An order past acknowledgement is refused with ErrCancelAfterAcknowledgement;
// an EXPIRED or CANCELLED one was never acknowledged, so it is an illegal
// transition.
func (p *PurchaseOrder) Cancel(reason string, now time.Time) ([]shared.Event, error) {
	switch p.state {
	case Issued:
	case Expired, Cancelled:
		return nil, fmt.Errorf("%w: the order is %s", shared.ErrIllegalTransition, p.state)
	default:
		return nil, ErrCancelAfterAcknowledgement
	}
	if !shared.ValidTextN(reason, MaxReason) {
		return nil, shared.Invalid("reason must be non-blank, trimmed and at most %d characters", MaxReason)
	}
	p.state, p.cancelReason, p.cancelledAt = Cancelled, reason, now.UTC()
	p.version++
	return []shared.Event{PurchaseOrderCancelled{Header: shared.Header{Version: p.version, At: p.cancelledAt}, PONumber: p.number,
		AssignmentID: p.assignmentID, Reason: reason}}, nil
}
