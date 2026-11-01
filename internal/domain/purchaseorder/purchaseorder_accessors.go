package purchaseorder

import "time"

// ID returns the poNumber.
func (p *PurchaseOrder) ID() Number { return p.number }

// Number returns the poNumber.
func (p *PurchaseOrder) Number() Number { return p.number }

// VendorID returns the vendor that operates the node.
func (p *PurchaseOrder) VendorID() string { return p.vendorID }

// NodeID returns the vendor node.
func (p *PurchaseOrder) NodeID() string { return p.nodeID }

// AssignmentID returns order-sourcing's assignment.
func (p *PurchaseOrder) AssignmentID() string { return p.assignmentID }

// OrderID returns the customer order; it never leaves the service except in the IssueLabel call.
func (p *PurchaseOrder) OrderID() string { return p.orderID }

// GroupNo returns the shipment group number.
func (p *PurchaseOrder) GroupNo() int { return p.groupNo }

// Tier returns the service tier.
func (p *PurchaseOrder) Tier() Tier { return p.tier }

// ReleaseNo returns the release of the assignment.
func (p *PurchaseOrder) ReleaseNo() int { return p.releaseNo }

// IssuedAt returns when the order was issued.
func (p *PurchaseOrder) IssuedAt() time.Time { return p.issuedAt }

// RequiredShipBy returns the pickup cutoff the order must be ready for.
func (p *PurchaseOrder) RequiredShipBy() time.Time { return p.requiredShipBy }

// AckBy returns the acknowledgement deadline.
func (p *PurchaseOrder) AckBy() time.Time { return p.ackBy }

// State returns the lifecycle state.
func (p *PurchaseOrder) State() State { return p.state }

// Version returns the optimistic-concurrency version.
func (p *PurchaseOrder) Version() int64 { return p.version }

// Lines returns a copy of the lines.
func (p *PurchaseOrder) Lines() []Line { return append([]Line(nil), p.lines...) }

// Ack returns a copy of the pending or final acknowledgement, if any.
func (p *PurchaseOrder) Ack() (Ack, bool) {
	if p.ack == nil {
		return Ack{}, false
	}
	return *p.ack, true
}

// Labels returns a copy of the labels.
func (p *PurchaseOrder) Labels() []Label { return append([]Label(nil), p.labels...) }

// Shipment returns a copy of the pending or final shipment confirmation, if any.
func (p *PurchaseOrder) Shipment() (Shipment, bool) {
	if p.shipment == nil {
		return Shipment{}, false
	}
	sh := *p.shipment
	sh.TrackingNumbers = append([]string(nil), sh.TrackingNumbers...)
	return sh, true
}

// CancelReason returns why the order was cancelled.
func (p *PurchaseOrder) CancelReason() string { return p.cancelReason }

// Snapshot returns the persisted form.
func (p *PurchaseOrder) Snapshot() Snapshot {
	s := Snapshot{
		Number: p.number, VendorID: p.vendorID, NodeID: p.nodeID, AssignmentID: p.assignmentID, OrderID: p.orderID, GroupNo: p.groupNo,
		Tier: p.tier, ReleaseNo: p.releaseNo, IssuedAt: p.issuedAt, RequiredShipBy: p.requiredShipBy, AckBy: p.ackBy, Lines: p.Lines(),
		State: p.state, Labels: p.Labels(), CancelReason: p.cancelReason, CancelledAt: p.cancelledAt, Version: p.version,
	}
	if a, ok := p.Ack(); ok {
		s.Ack = &a
	}
	if sh, ok := p.Shipment(); ok {
		s.Shipment = &sh
	}
	return s
}
