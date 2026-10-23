package purchaseorder

import (
	"cmp"
	"errors"
	"fmt"
	"slices"
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

// AckInput is the input of SubmitAcknowledgement.
type AckInput struct {
	Decision      Decision
	ReasonCode    RejectReason
	Lines         []AckLine
	TransactionID string
}

// AckLine is one acknowledged line: the line number and its quantity.
type AckLine struct {
	LineNo   int
	Quantity int
}

// SubmitAcknowledgement records the vendor's single, whole-order decision and
// moves ISSUED to ACK_PROCESSING. Checks, first failure wins (A6): state, window
// (judged on now; now == ackBy is still inside), completeness, decision.
func (p *PurchaseOrder) SubmitAcknowledgement(in AckInput, now time.Time) ([]shared.Event, error) {
	if err := p.ackable(now); err != nil {
		return nil, err
	}
	if err := p.coversEveryLine(in.Lines); err != nil {
		return nil, err
	}
	if err := checkDecision(in); err != nil {
		return nil, err
	}
	p.state, p.ack = AckProcessing, &Ack{Decision: in.Decision, ReasonCode: in.ReasonCode, TransactionID: in.TransactionID}
	p.version++
	return nil, nil
}

func (p *PurchaseOrder) ackable(now time.Time) error {
	switch p.state {
	case Issued:
		if now.After(p.ackBy) {
			return ErrAckWindowClosed
		}
		return nil
	case Expired:
		return ErrAckWindowClosed
	case Cancelled:
		return fmt.Errorf("%w: the order was cancelled", shared.ErrIllegalTransition)
	default:
		return ErrAlreadyAcknowledged
	}
}

func (p *PurchaseOrder) coversEveryLine(lines []AckLine) error {
	if len(lines) != len(p.lines) {
		return ErrPartialAcknowledgement
	}
	seen := map[int]bool{}
	for _, l := range lines {
		if l.LineNo < 1 || l.LineNo > len(p.lines) || seen[l.LineNo] || p.lines[l.LineNo-1].Quantity != l.Quantity {
			return ErrPartialAcknowledgement
		}
		seen[l.LineNo] = true
	}
	return nil
}

func checkDecision(in AckInput) error {
	if !shared.ValidID(in.TransactionID) {
		return shared.Invalid("transactionId is not a valid id")
	}
	switch in.Decision {
	case Accept:
		if in.ReasonCode != "" {
			return shared.Invalid("an ACCEPT carries no reasonCode")
		}
	case Reject:
		if !ValidReason(in.ReasonCode) {
			return shared.Invalid("a REJECT needs a reasonCode: OUT_OF_STOCK, UNKNOWN_LISTING, CANNOT_MEET_SHIP_BY or DISCONTINUED")
		}
	default:
		return shared.Invalid("decision must be ACCEPT or REJECT")
	}
	return nil
}

// ApplyAckOutcome applies the outcome of the acknowledgement transaction
// (ProcessSubmission): success moves ACK_PROCESSING to ACCEPTED or REJECTED by the
// decision and raises PurchaseOrderAccepted or PurchaseOrderRejected; a failed
// transaction returns the order to ISSUED and raises nothing.
func (p *PurchaseOrder) ApplyAckOutcome(success bool, now time.Time) ([]shared.Event, error) {
	if p.state != AckProcessing {
		return nil, fmt.Errorf("%w: no acknowledgement is processing (state %s)", shared.ErrIllegalTransition, p.state)
	}
	if !success {
		p.state, p.ack = Issued, nil
		p.version++
		return nil, nil
	}
	h := func() shared.Header { return shared.Header{Version: p.version, At: now.UTC()} }
	if p.ack.Decision == Accept {
		p.state = Accepted
		p.version++
		return []shared.Event{PurchaseOrderAccepted{Header: h(), PONumber: p.number, AssignmentID: p.assignmentID, ReleaseNo: p.releaseNo,
			NodeID: p.nodeID, AcceptedAt: now.UTC(), TransactionID: p.ack.TransactionID}}, nil
	}
	p.state = Rejected
	p.version++
	return []shared.Event{PurchaseOrderRejected{Header: h(), PONumber: p.number, AssignmentID: p.assignmentID, ReleaseNo: p.releaseNo,
		NodeID: p.nodeID, ReasonCode: p.ack.ReasonCode, RejectedAt: now.UTC()}}, nil
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

// labelState says whether the order can hold labels now: nil in ACCEPTED,
// ErrLabelBeforeAcceptance before acceptance, an illegal transition after.
func (p *PurchaseOrder) labelState() error {
	switch p.state {
	case Accepted:
		return nil
	case Issued, AckProcessing:
		return ErrLabelBeforeAcceptance
	default:
		return fmt.Errorf("%w: no label for an order in state %s", shared.ErrIllegalTransition, p.state)
	}
}

// LabelPlan splits the requested packages into those already labelled and
// those still to issue (RequestLabel, A7). Re-requesting only labelled
// packages is allowed in ACCEPTED, SHIPMENT_PROCESSING and SHIPPED.
func (p *PurchaseOrder) LabelPlan(packageNos []int) ([]Label, []int, error) {
	if err := checkPackageNos(packageNos); err != nil {
		return nil, nil, err
	}
	byPkg := map[int]Label{}
	for _, l := range p.labels {
		byPkg[l.PackageNo] = l
	}
	var have []Label
	var missing []int
	for _, n := range packageNos {
		if l, ok := byPkg[n]; ok {
			have = append(have, l)
		} else {
			missing = append(missing, n)
		}
	}
	if len(missing) == 0 && (p.state == ShipmentProcessing || p.state == Shipped) {
		return sortedLabels(have), nil, nil
	}
	if err := p.labelState(); err != nil {
		return nil, nil, err
	}
	return sortedLabels(have), missing, nil
}

func sortedLabels(ls []Label) []Label {
	slices.SortFunc(ls, func(a, b Label) int { return cmp.Compare(a.PackageNo, b.PackageNo) })
	return ls
}

func checkPackageNos(nos []int) error {
	if len(nos) == 0 || len(nos) > MaxPackages {
		return shared.Invalid("request 1 to %d packages", MaxPackages)
	}
	seen := map[int]bool{}
	for _, n := range nos {
		if n < 1 || n > MaxPackageNo {
			return shared.Invalid("packageNo must be from 1 to %d", MaxPackageNo)
		}
		if seen[n] {
			return shared.Invalid("packageNo %d is requested twice", n)
		}
		seen[n] = true
	}
	return nil
}

// AddLabels records issued labels on an ACCEPTED order. A label for a package
// that already holds the identical label is ignored; a different one, or a
// tracking number used twice, is a validation error.
func (p *PurchaseOrder) AddLabels(labels []Label) error {
	if err := p.labelState(); err != nil {
		return err
	}
	next := append([]Label(nil), p.labels...)
	changed := false
	for _, l := range labels {
		if err := checkLabel(l); err != nil {
			return shared.Invalid("%v", err)
		}
		dup, err := findLabel(next, l)
		if err != nil {
			return err
		}
		if !dup {
			next = append(next, l)
			changed = true
		}
	}
	if len(next) > MaxPackages {
		return shared.Invalid("at most %d packages", MaxPackages)
	}
	if changed {
		slices.SortFunc(next, func(a, b Label) int { return cmp.Compare(a.PackageNo, b.PackageNo) })
		p.labels = next
		p.version++
	}
	return nil
}

// findLabel reports whether l is already in labels; a clash is an error.
func findLabel(labels []Label, l Label) (bool, error) {
	for _, e := range labels {
		if e == l {
			return true, nil
		}
		if e.PackageNo == l.PackageNo || e.TrackingNumber == l.TrackingNumber {
			return false, shared.Invalid("package %d clashes with the label of package %d", l.PackageNo, e.PackageNo)
		}
	}
	return false, nil
}

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
