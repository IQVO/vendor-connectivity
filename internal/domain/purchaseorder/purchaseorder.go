// Package purchaseorder is the PurchaseOrder aggregate: the network's
// instruction to one vendor node to ship one shipment group in full by
// requiredShipBy (ADR 0002).
//
// States and transitions (command):
//
//	ISSUED --SubmitAcknowledgement--> ACK_PROCESSING
//	ACK_PROCESSING --ProcessSubmission--> ACCEPTED | REJECTED | ISSUED
//	ISSUED --ExpireUnacknowledgedOrders--> EXPIRED
//	ISSUED --CancelPurchaseOrder--> CANCELLED
//	ACCEPTED --SubmitShipmentConfirmation--> SHIPMENT_PROCESSING
//	SHIPMENT_PROCESSING --ProcessSubmission--> SHIPPED | ACCEPTED
//
// RequestLabel changes no state: it adds labels to an ACCEPTED order.
package purchaseorder

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strconv"
	"time"

	"github.com/claudioed/vendor-connectivity/internal/domain/shared"
)

var (
	// ErrPartialAcknowledgement: the acknowledgement does not cover every line once at its ordered quantity (422).
	ErrPartialAcknowledgement = errors.New("partial acknowledgement")
	// ErrAlreadyAcknowledged: the order was acknowledged already, or an acknowledgement is processing (409).
	ErrAlreadyAcknowledged = errors.New("already acknowledged")
	// ErrAckWindowClosed: the acknowledgement arrived after ackBy, or the order expired (409).
	ErrAckWindowClosed = errors.New("acknowledgement window closed")
	// ErrLabelBeforeAcceptance: a label was requested before the order was ACCEPTED (409).
	ErrLabelBeforeAcceptance = errors.New("label before acceptance")
	// ErrConfirmWithoutLabel: a shipment confirmation does not match a label for every package (409).
	ErrConfirmWithoutLabel = errors.New("shipment confirmation without a label for every package")
	// ErrCancelAfterAcknowledgement: only an ISSUED order can be cancelled (409; never reaches HTTP).
	ErrCancelAfterAcknowledgement = errors.New("cancel after acknowledgement")
)

// Bounds, as plain literals so they hold in Postgres integer columns.
const (
	MaxLines     = 500
	MaxQuantity  = 2147483647
	MaxReleaseNo = 2147483647
	MaxGroupNo   = 2147483647
	MaxPackages  = 100
	MaxPackageNo = 999999
	MaxReason    = 200
	MaxText      = 100
)

// Number is the purchase order number (poNumber).
type Number string

// State is the lifecycle state of a purchase order.
type State string

// The eight states of a purchase order.
const (
	Issued             State = "ISSUED"
	AckProcessing      State = "ACK_PROCESSING"
	Accepted           State = "ACCEPTED"
	Rejected           State = "REJECTED"
	Expired            State = "EXPIRED"
	ShipmentProcessing State = "SHIPMENT_PROCESSING"
	Shipped            State = "SHIPPED"
	Cancelled          State = "CANCELLED"
)

// Tier is the service tier of the order, kept for the IssueLabel call.
type Tier string

// Service tiers (order-sourcing).
const (
	SameDay  Tier = "SAME_DAY"
	NextDay  Tier = "NEXT_DAY"
	TwoDay   Tier = "TWO_DAY"
	Standard Tier = "STANDARD"
)

// ValidTier reports whether t is a service tier.
func ValidTier(t Tier) bool {
	switch t {
	case SameDay, NextDay, TwoDay, Standard:
		return true
	}
	return false
}

// Line is one PoLine.
type Line struct {
	LineNo    int
	ListingID string
	Quantity  int
}

// Decision is the AckDecision.
type Decision string

// The two acknowledgement decisions.
const (
	Accept Decision = "ACCEPT"
	Reject Decision = "REJECT"
)

// RejectReason is the model's RejectReason value object.
type RejectReason string

// The four reject reasons.
const (
	OutOfStock       RejectReason = "OUT_OF_STOCK"
	UnknownListing   RejectReason = "UNKNOWN_LISTING"
	CannotMeetShipBy RejectReason = "CANNOT_MEET_SHIP_BY"
	Discontinued     RejectReason = "DISCONTINUED"
)

// ValidReason reports whether r is a RejectReason.
func ValidReason(r RejectReason) bool {
	switch r {
	case OutOfStock, UnknownListing, CannotMeetShipBy, Discontinued:
		return true
	}
	return false
}

// Ack is the vendor's decision with the acknowledgement transaction.
type Ack struct {
	Decision      Decision
	ReasonCode    RejectReason
	TransactionID string
}

// Label is the model's Label value object; it never carries an address.
type Label struct {
	PackageNo      int
	LabelRef       string
	TrackingNumber string
	Carrier        string
}

// Shipment is the vendor's shipment confirmation with its transaction.
type Shipment struct {
	TransactionID   string
	ShippedAt       time.Time
	TrackingNumbers []string
}

// PurchaseOrder is the aggregate root.
type PurchaseOrder struct {
	number         Number
	vendorID       string
	nodeID         string
	assignmentID   string
	orderID        string
	groupNo        int
	tier           Tier
	releaseNo      int
	issuedAt       time.Time
	requiredShipBy time.Time
	ackBy          time.Time
	lines          []Line
	state          State
	ack            *Ack
	labels         []Label
	shipment       *Shipment
	cancelReason   string
	cancelledAt    time.Time
	version        int64
}

// Snapshot is the persisted form of a purchase order. Restore validates it.
type Snapshot struct {
	Number         Number
	VendorID       string
	NodeID         string
	AssignmentID   string
	OrderID        string
	GroupNo        int
	Tier           Tier
	ReleaseNo      int
	IssuedAt       time.Time
	RequiredShipBy time.Time
	AckBy          time.Time
	Lines          []Line
	State          State
	Ack            *Ack
	Labels         []Label
	Shipment       *Shipment
	CancelReason   string
	CancelledAt    time.Time
	Version        int64
}

// NumberFor derives the poNumber of a release (assumption A1): deterministic
// in (assignmentId, releaseNo), so a redelivered release finds its order. It
// is mapped, never parsed.
func NumberFor(assignmentID string, releaseNo int) Number {
	sum := sha256.Sum256([]byte(assignmentID + ":" + strconv.Itoa(releaseNo)))
	return Number("PO-" + hex.EncodeToString(sum[:])[:16])
}

// ComputeAckBy is the model's ackBy = min(issuedAt + ackWindow, requiredShipBy
// - handlingTime).
func ComputeAckBy(issuedAt time.Time, ackWindow time.Duration, requiredShipBy time.Time, handlingTime time.Duration) time.Time {
	byWindow := issuedAt.Add(ackWindow)
	byShip := requiredShipBy.Add(-handlingTime)
	if byShip.Before(byWindow) {
		return byShip.UTC()
	}
	return byWindow.UTC()
}

// NewLine is one line of a release.
type NewLine struct {
	ListingID string
	Quantity  int
}

// NewOrder is the input of Issue.
type NewOrder struct {
	AssignmentID   string
	ReleaseNo      int
	VendorID       string
	NodeID         string
	OrderID        string
	GroupNo        int
	Tier           Tier
	RequiredShipBy time.Time
	Lines          []NewLine
	AckWindow      time.Duration
	HandlingTime   time.Duration
}

// Issue creates an ISSUED purchase order at version 1 and raises
// PurchaseOrderIssued (IssuePurchaseOrder). Lines are numbered 1..n.
func Issue(in NewOrder, now time.Time) (*PurchaseOrder, []shared.Event, error) {
	if in.AckWindow <= 0 || in.HandlingTime < 0 {
		return nil, nil, shared.Invalid("ack window must be positive and handling time not negative")
	}
	lines := make([]Line, len(in.Lines))
	for i, l := range in.Lines {
		lines[i] = Line{LineNo: i + 1, ListingID: l.ListingID, Quantity: l.Quantity}
	}
	p, err := build(Snapshot{
		Number: NumberFor(in.AssignmentID, in.ReleaseNo), VendorID: in.VendorID, NodeID: in.NodeID, AssignmentID: in.AssignmentID,
		OrderID: in.OrderID, GroupNo: in.GroupNo, Tier: in.Tier, ReleaseNo: in.ReleaseNo, IssuedAt: now, RequiredShipBy: in.RequiredShipBy,
		AckBy: ComputeAckBy(now, in.AckWindow, in.RequiredShipBy, in.HandlingTime), Lines: lines, State: Issued, Version: 1,
	})
	if err != nil {
		return nil, nil, shared.Invalid("%v", err)
	}
	ev := PurchaseOrderIssued{
		Header: shared.Header{Version: p.version, At: p.issuedAt}, PONumber: p.number, VendorID: p.vendorID, NodeID: p.nodeID,
		AssignmentID: p.assignmentID, ReleaseNo: p.releaseNo, RequiredShipBy: p.requiredShipBy, AckBy: p.ackBy, Lines: p.Lines(),
	}
	return p, []shared.Event{ev}, nil
}

// Restore rebuilds a persisted purchase order, validating every invariant.
func Restore(s Snapshot) (*PurchaseOrder, error) {
	p, err := build(s)
	if err != nil {
		return nil, shared.Corrupt("purchase order %q: %v", s.Number, err)
	}
	return p, nil
}

func build(s Snapshot) (*PurchaseOrder, error) {
	if err := checkIdentity(s); err != nil {
		return nil, err
	}
	if err := checkLines(s.Lines); err != nil {
		return nil, err
	}
	if err := checkState(s); err != nil {
		return nil, err
	}
	p := &PurchaseOrder{
		number: s.Number, vendorID: s.VendorID, nodeID: s.NodeID, assignmentID: s.AssignmentID, orderID: s.OrderID, groupNo: s.GroupNo,
		tier: s.Tier, releaseNo: s.ReleaseNo, issuedAt: s.IssuedAt.UTC(), requiredShipBy: s.RequiredShipBy.UTC(), ackBy: s.AckBy.UTC(),
		lines: append([]Line(nil), s.Lines...), state: s.State, labels: append([]Label(nil), s.Labels...),
		cancelReason: s.CancelReason, cancelledAt: shared.UTC(s.CancelledAt), version: s.Version,
	}
	if s.Ack != nil {
		a := *s.Ack
		p.ack = &a
	}
	if s.Shipment != nil {
		sh := *s.Shipment
		sh.ShippedAt = sh.ShippedAt.UTC()
		sh.TrackingNumbers = append([]string(nil), sh.TrackingNumbers...)
		p.shipment = &sh
	}
	return p, nil
}
