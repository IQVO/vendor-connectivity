package purchaseorder

import (
	"fmt"
	"time"

	"github.com/claudioed/vendor-connectivity/internal/domain/shared"
)

// ShipInput is the input of SubmitShipmentConfirmation.
type ShipInput struct {
	TrackingNumbers []string
	ShippedAt       time.Time
	TransactionID   string
}

// SubmitShipmentConfirmation records the vendor's confirmation and moves
// ACCEPTED to SHIPMENT_PROCESSING. The tracking numbers must be exactly the
// order's labelled ones (A20), else ErrConfirmWithoutLabel.
func (p *PurchaseOrder) SubmitShipmentConfirmation(in ShipInput) ([]shared.Event, error) {
	switch p.state {
	case Accepted:
	case Issued, AckProcessing:
		return nil, ErrConfirmWithoutLabel
	default:
		return nil, fmt.Errorf("%w: no shipment confirmation for an order in state %s", shared.ErrIllegalTransition, p.state)
	}
	if !shared.ValidID(in.TransactionID) || in.ShippedAt.IsZero() {
		return nil, shared.Invalid("transactionId and shippedAt are required")
	}
	if !sameTracking(in.TrackingNumbers, p.labels) {
		return nil, ErrConfirmWithoutLabel
	}
	p.state = ShipmentProcessing
	p.shipment = &Shipment{TransactionID: in.TransactionID, ShippedAt: in.ShippedAt.UTC(), TrackingNumbers: append([]string(nil), in.TrackingNumbers...)}
	p.version++
	return nil, nil
}

// ApplyShipmentOutcome applies the outcome of the confirmation transaction:
// success moves SHIPMENT_PROCESSING to SHIPPED and raises VendorShipmentConfirmed; a
// failed transaction returns the order to ACCEPTED.
func (p *PurchaseOrder) ApplyShipmentOutcome(success bool, now time.Time) ([]shared.Event, error) {
	if p.state != ShipmentProcessing {
		return nil, fmt.Errorf("%w: no shipment confirmation is processing (state %s)", shared.ErrIllegalTransition, p.state)
	}
	if !success {
		p.state, p.shipment = Accepted, nil
		p.version++
		return nil, nil
	}
	p.state = Shipped
	p.version++
	sh, _ := p.Shipment()
	return []shared.Event{VendorShipmentConfirmed{Header: shared.Header{Version: p.version, At: now.UTC()}, PONumber: p.number,
		AssignmentID: p.assignmentID, NodeID: p.nodeID, TrackingNumbers: sh.TrackingNumbers, ShippedAt: sh.ShippedAt, RequiredShipBy: p.requiredShipBy}}, nil
}
