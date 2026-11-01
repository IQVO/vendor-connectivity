package purchaseorder

import (
	"fmt"
	"time"

	"github.com/claudioed/vendor-connectivity/internal/domain/shared"
)

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
