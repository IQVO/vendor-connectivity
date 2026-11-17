// Package returns holds the return-advice side of the Vendor API: the
// ReturnAdviceView read model fed by customer-returns' events, the rules a
// vendor's return outcome must satisfy against it, and the
// VendorReturnOutcomeSubmitted event (ADR 0014).
//
// The view is a projection, not an aggregate: it is rebuilt from events that may
// arrive twice or out of order, so every transition only moves forward
// (AUTHORIZED, then terminal CANCELLED or OUTCOME_RECORDED).
package returns

import (
	"time"

	"github.com/claudioed/vendor-connectivity/internal/domain/shared"
)

// State is the lifecycle position of a return advice as this context knows it.
// The customer-returns states (IN_TRANSIT, RECEIVED, ...) are not mirrored:
// the vendor polls until an outcome is recorded or the authorization is
// cancelled, nothing else (assumption A34).
type State string

// The advice states.
const (
	StateAuthorized      State = "AUTHORIZED"
	StateCancelled       State = "CANCELLED"
	StateOutcomeRecorded State = "OUTCOME_RECORDED"
)

const (
	// MaxAdviceLines is the most lines one advice carries (the authorization's
	// own limit, mirrored here for the validation of what arrives).
	MaxAdviceLines = 50
	// MaxTrackingNumbers is the most reverse labels one return may carry.
	MaxTrackingNumbers = 5
	maxQuantity        = shared.MaxInt32
)

// rank orders the states so a late or repeated event never moves an advice back.
func rank(s State) int {
	if s == StateAuthorized {
		return 1
	}
	if s == StateCancelled || s == StateOutcomeRecorded {
		return 2
	}
	return 0
}

// furthest returns the later of two states; on a tie it keeps a.
func furthest(a, b State) State {
	if rank(b) > rank(a) {
		return b
	}
	return a
}

// Line is one authorization line of an advice.
type Line struct {
	LineNo        int
	CatalogItemID string
	Quantity      int
	OwnerRef      string
}

// Advice is one row of ReturnAdviceView.
type Advice struct {
	AuthorizationID string
	OrderID         string
	GroupNo         int
	VendorID        string
	NodeID          string
	Disposition     string
	Method          string
	State           State
	Lines           []Line
	TrackingNumbers []string
	ReadyAt         time.Time
	ExpiresAt       time.Time
	AuthorizedAt    time.Time
}

// Authorized carries ReturnAuthorized with the destination node and operator
// vendor already resolved by the consumer from VendorNodeView (ADR 0014).
type Authorized struct {
	AuthorizationID string
	OrderID         string
	GroupNo         int
	VendorID        string
	NodeID          string
	Disposition     string
	Method          string
	Lines           []Line
	ExpiresAt       time.Time
	AuthorizedAt    time.Time
}

func normaliseLines(in []Line) ([]Line, error) {
	if len(in) == 0 || len(in) > MaxAdviceLines {
		return nil, shared.Invalid("an advice carries between 1 and %d lines", MaxAdviceLines)
	}
	out := make([]Line, 0, len(in))
	for i, l := range in {
		if !shared.ValidID(l.CatalogItemID) {
			return nil, shared.Invalid("lines[%d]: catalogItemId must be a valid id", i)
		}
		if l.Quantity < 1 || l.Quantity > maxQuantity {
			return nil, shared.Invalid("lines[%d]: quantity must be between 1 and %d", i, maxQuantity)
		}
		out = append(out, l)
	}
	return out, nil
}

// OnAuthorized applies ReturnAuthorized: the advice appears in AUTHORIZED with
// the resolved node and vendor. A late re-delivery after a terminal state
// changes nothing (the first terminal fact wins; the state never moves back).
func (a Advice) OnAuthorized(e Authorized) (Advice, error) {
	if !shared.ValidID(e.AuthorizationID) || !shared.ValidID(e.NodeID) || !shared.ValidID(e.VendorID) {
		return Advice{}, shared.Invalid("authorizationId, nodeId and vendorId must be valid ids")
	}
	if e.AuthorizedAt.IsZero() {
		return Advice{}, shared.Invalid("authorizedAt is required")
	}
	lines, err := normaliseLines(e.Lines)
	if err != nil {
		return Advice{}, err
	}
	out := a
	out.AuthorizationID, out.NodeID, out.VendorID = e.AuthorizationID, e.NodeID, e.VendorID
	out.OrderID, out.GroupNo = e.OrderID, e.GroupNo
	out.Disposition, out.Method = e.Disposition, e.Method
	out.Lines, out.AuthorizedAt = lines, shared.UTC(e.AuthorizedAt)
	out.ReadyAt = shared.UTC(e.AuthorizedAt) // A33
	out.ExpiresAt = shared.UTC(e.ExpiresAt)
	out.TrackingNumbers = nonNil(a.TrackingNumbers)
	if a.AuthorizationID == "" {
		out.State = StateAuthorized
	}
	return out, nil
}

func nonNil(in []string) []string {
	if in == nil {
		return []string{}
	}
	return in
}

// OnLabelIssued applies ReturnLabelIssued: the tracking number joins the list
// (idempotent, at most MaxTrackingNumbers). It never changes the state: a
// DROP_OFF advice is the vendor's business from authorization (A33).
func (a Advice) OnLabelIssued(trackingNumber string) (Advice, error) {
	if a.AuthorizationID == "" {
		return Advice{}, shared.Invalid("label issued for an advice never authorized")
	}
	if !shared.ValidTextN(trackingNumber, 64) || trackingNumber == "" {
		return Advice{}, shared.Invalid("trackingNumber is required text of at most 64 characters")
	}
	out := a
	for _, t := range a.TrackingNumbers {
		if t == trackingNumber {
			return out, nil
		}
	}
	if len(a.TrackingNumbers) >= MaxTrackingNumbers {
		return Advice{}, shared.Invalid("an advice carries at most %d tracking numbers", MaxTrackingNumbers)
	}
	out.TrackingNumbers = append(nonNil(a.TrackingNumbers), trackingNumber)
	return out, nil
}

// OnCancelled applies ReturnCancelled: the advice goes CANCELLED, unless an
// outcome was already recorded (a graded receipt is never un-recorded).
func (a Advice) OnCancelled() (Advice, error) {
	if a.AuthorizationID == "" {
		return Advice{}, shared.Invalid("cancellation for an advice never authorized")
	}
	out := a
	out.State = furthest(a.State, StateCancelled)
	return out, nil
}

// Pollable reports whether the vendor sees the advice in the poll.
func (a Advice) Pollable() bool { return a.State == StateAuthorized }

// OutcomeLine is one graded quantity of a vendor's return outcome.
type OutcomeLine struct {
	CatalogItemID  string
	OwnerRef       string
	QtyRestockable int
	QtyUnsellable  int
}

// Outcome is a vendor's return outcome as the domain judges it.
type Outcome struct {
	AuthorizationID string
	VendorID        string
	NodeID          string
	ReceivedAt      time.Time
	Lines           []OutcomeLine
}

// Normalise checks the shape of an outcome on its own (ids, quantities,
// duplicates) and returns it with every time in UTC. It does not look at any
// advice.
func (o Outcome) Normalise() (Outcome, error) {
	if !shared.ValidID(o.AuthorizationID) || !shared.ValidID(o.NodeID) || !shared.ValidID(o.VendorID) {
		return Outcome{}, shared.Invalid("authorizationId, nodeId and vendorId must be valid ids")
	}
	if o.ReceivedAt.IsZero() {
		return Outcome{}, shared.Invalid("receivedAt is required")
	}
	if len(o.Lines) == 0 || len(o.Lines) > MaxAdviceLines {
		return Outcome{}, shared.Invalid("an outcome lists between 1 and %d lines", MaxAdviceLines)
	}
	out := o
	out.ReceivedAt = o.ReceivedAt.UTC()
	out.Lines = make([]OutcomeLine, 0, len(o.Lines))
	seen := make(map[string]bool, len(o.Lines))
	for i, l := range o.Lines {
		if !shared.ValidID(l.CatalogItemID) {
			return Outcome{}, shared.Invalid("lines[%d]: catalogItemId must be a valid id", i)
		}
		if l.QtyRestockable < 0 || l.QtyUnsellable < 0 || l.QtyRestockable > maxQuantity || l.QtyUnsellable > maxQuantity {
			return Outcome{}, shared.Invalid("lines[%d]: quantities must be between 0 and %d", i, maxQuantity)
		}
		key := l.CatalogItemID + "\x00" + l.OwnerRef
		if seen[key] {
			return Outcome{}, shared.Invalid("lines[%d]: catalogItemId %s with ownerRef %q appears twice", i, l.CatalogItemID, l.OwnerRef)
		}
		seen[key] = true
		out.Lines = append(out.Lines, l)
	}
	return out, nil
}

// CheckOutcome judges a normalised outcome against the advice: it must be the
// advice's own node and vendor, and still AUTHORIZED. Lines the advice never
// authorized are NOT refused: goods on the dock are never turned away (ADR 0005
// rule); customer-returns records them as unexpected and does not restock them.
func (a Advice) CheckOutcome(o Outcome) error {
	if a.NodeID != o.NodeID {
		return shared.Invalid("nodeId %s is not the node of advice %s", o.NodeID, a.AuthorizationID)
	}
	if a.VendorID != o.VendorID {
		return shared.Invalid("vendorId %s is not the operator of advice %s", o.VendorID, a.AuthorizationID)
	}
	if !a.Pollable() {
		return shared.ErrIllegalTransition
	}
	return nil
}

// RecordOutcome moves the advice to OUTCOME_RECORDED: it leaves the poll.
func (a Advice) RecordOutcome() (Advice, error) {
	if !a.Pollable() {
		return Advice{}, shared.ErrIllegalTransition
	}
	out := a
	out.State = StateOutcomeRecorded
	return out, nil
}

// Entity is the CloudEvents entity segment of VendorReturnOutcomeSubmitted.
const Entity = "returnoutcome"

// OutcomeSubmitted is raised when the network accepts a vendor's return
// outcome for processing. Its fields are the contract's data list; the subject
// and the Kafka key are the authorizationId.
type OutcomeSubmitted struct {
	shared.Header
	TransactionID   string
	VendorID        string
	NodeID          string
	AuthorizationID string
	ReceivedAt      time.Time
	Lines           []OutcomeLine
}

// NewOutcomeSubmitted builds the event for a normalised outcome.
func NewOutcomeSubmitted(transactionID, vendorID string, o Outcome, now time.Time) OutcomeSubmitted {
	return OutcomeSubmitted{Header: shared.Header{Version: 1, At: now.UTC()}, TransactionID: transactionID, VendorID: vendorID,
		NodeID: o.NodeID, AuthorizationID: o.AuthorizationID, ReceivedAt: o.ReceivedAt, Lines: o.Lines}
}

// EventName returns "VendorReturnOutcomeSubmitted".
func (OutcomeSubmitted) EventName() string { return "VendorReturnOutcomeSubmitted" }

// Entity returns "returnoutcome".
func (OutcomeSubmitted) Entity() string { return Entity }

// SubjectID returns the authorizationId.
func (e OutcomeSubmitted) SubjectID() string { return e.AuthorizationID }

var _ shared.Event = OutcomeSubmitted{}
