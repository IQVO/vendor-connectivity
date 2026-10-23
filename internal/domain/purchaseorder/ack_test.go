package purchaseorder

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/claudioed/vendor-connectivity/internal/domain/shared"
)

func TestSubmitAcknowledgementByState(t *testing.T) {
	want := map[State]error{
		Issued: nil, AckProcessing: ErrAlreadyAcknowledged, Accepted: ErrAlreadyAcknowledged, Rejected: ErrAlreadyAcknowledged,
		Expired: ErrAckWindowClosed, ShipmentProcessing: ErrAlreadyAcknowledged, Shipped: ErrAlreadyAcknowledged, Cancelled: shared.ErrIllegalTransition,
	}
	for _, st := range allStates {
		p := inState(t, st)
		before := p.Version()
		evs, err := p.SubmitAcknowledgement(fullAck(Accept, "", "tx-new"), t0.Add(time.Minute))
		if want[st] == nil {
			if err != nil || evs != nil || p.State() != AckProcessing || p.Version() != before+1 {
				t.Errorf("%s: %v %v %s", st, evs, err, p.State())
			}
			continue
		}
		if !errors.Is(err, want[st]) || p.State() != st || p.Version() != before {
			t.Errorf("%s: err = %v state %s", st, err, p.State())
		}
	}
}

func TestSubmitAcknowledgementWindowBoundary(t *testing.T) {
	for _, tc := range []struct {
		at  time.Duration
		err error
	}{{-time.Hour, nil}, {-time.Nanosecond, nil}, {0, nil}, {time.Nanosecond, ErrAckWindowClosed}, {time.Hour, ErrAckWindowClosed}} {
		p := issue(t)
		_, err := p.SubmitAcknowledgement(fullAck(Accept, "", "tx-1"), p.AckBy().Add(tc.at))
		if !errors.Is(err, tc.err) || (tc.err == nil && err != nil) {
			t.Errorf("at ackBy%+v: err = %v, want %v", tc.at, err, tc.err)
		}
	}
}

func TestSubmitAcknowledgementIsFillOrKill(t *testing.T) {
	tests := map[string][]AckLine{
		"no lines":        nil,
		"one of two":      {{1, 2}},
		"extra line":      {{1, 2}, {2, 5}, {3, 1}},
		"wrong quantity":  {{1, 1}, {2, 5}},
		"quantity high":   {{1, 2}, {2, 6}},
		"duplicate line":  {{1, 2}, {1, 2}},
		"unknown line":    {{1, 2}, {3, 5}},
		"line zero":       {{0, 2}, {2, 5}},
		"negative line":   {{-1, 2}, {2, 5}},
		"swapped numbers": {{1, 5}, {2, 2}},
	}
	for name, lines := range tests {
		for _, d := range []AckInput{fullAck(Accept, "", "tx-1"), fullAck(Reject, OutOfStock, "tx-1")} {
			p := issue(t)
			d.Lines = lines
			if _, err := p.SubmitAcknowledgement(d, t0); !errors.Is(err, ErrPartialAcknowledgement) || p.State() != Issued || p.Version() != 1 {
				t.Errorf("%s/%s: err = %v state %s", name, d.Decision, err, p.State())
			}
		}
	}
	p := issue(t)
	in := fullAck(Accept, "", "tx-1")
	in.Lines = []AckLine{{2, 5}, {1, 2}}
	if _, err := p.SubmitAcknowledgement(in, t0); err != nil {
		t.Errorf("lines in any order must cover the order: %v", err)
	}
}

func TestSubmitAcknowledgementDecisionRules(t *testing.T) {
	tests := []struct {
		name string
		in   AckInput
		ok   bool
	}{
		{"accept", fullAck(Accept, "", "tx-1"), true},
		{"reject each reason a", fullAck(Reject, OutOfStock, "tx-1"), true},
		{"reject each reason b", fullAck(Reject, UnknownListing, "tx-1"), true},
		{"reject each reason c", fullAck(Reject, CannotMeetShipBy, "tx-1"), true},
		{"reject each reason d", fullAck(Reject, Discontinued, "tx-1"), true},
		{"accept with reason", fullAck(Accept, OutOfStock, "tx-1"), false},
		{"reject without reason", fullAck(Reject, "", "tx-1"), false},
		{"reject unknown reason", fullAck(Reject, "BORED", "tx-1"), false},
		{"unknown decision", fullAck("MAYBE", "", "tx-1"), false},
		{"empty decision", fullAck("", "", "tx-1"), false},
		{"bad transaction", fullAck(Accept, "", "a b"), false},
	}
	for _, tc := range tests {
		p := issue(t)
		_, err := p.SubmitAcknowledgement(tc.in, t0)
		if tc.ok && err != nil {
			t.Errorf("%s: %v", tc.name, err)
		}
		if !tc.ok && (!errors.Is(err, shared.ErrValidation) || p.State() != Issued) {
			t.Errorf("%s: err = %v state %s", tc.name, err, p.State())
		}
	}
}

func TestSubmitAcknowledgementCheckOrder(t *testing.T) {
	// state beats window beats completeness beats decision (A6)
	p := inState(t, Accepted)
	if _, err := p.SubmitAcknowledgement(AckInput{Decision: "X"}, p.AckBy().Add(time.Hour)); !errors.Is(err, ErrAlreadyAcknowledged) {
		t.Errorf("state first: %v", err)
	}
	q := issue(t)
	if _, err := q.SubmitAcknowledgement(AckInput{Decision: "X"}, q.AckBy().Add(time.Hour)); !errors.Is(err, ErrAckWindowClosed) {
		t.Errorf("window second: %v", err)
	}
	if _, err := q.SubmitAcknowledgement(AckInput{Decision: "X"}, t0); !errors.Is(err, ErrPartialAcknowledgement) {
		t.Errorf("completeness third: %v", err)
	}
	if ack, ok := q.Ack(); ok {
		t.Errorf("a refused acknowledgement leaves no trace: %+v", ack)
	}
}

func TestApplyAckOutcome(t *testing.T) {
	at := t0.Add(5 * time.Minute)
	acc := inState(t, AckProcessing)
	evs, err := acc.ApplyAckOutcome(true, at)
	if err != nil || acc.State() != Accepted {
		t.Fatalf("accept: %v %s", err, acc.State())
	}
	assertEvents(t, evs, PurchaseOrderAccepted{Header: shared.Header{Version: acc.Version(), At: at}, PONumber: acc.Number(), AssignmentID: "asg-1",
		ReleaseNo: 1, NodeID: "n-1", AcceptedAt: at, TransactionID: "tx-a"})
	assertEventMeta(t, evs[0], "PurchaseOrderAccepted", string(acc.Number()))
	if a, ok := acc.Ack(); !ok || a != (Ack{Decision: Accept, TransactionID: "tx-a"}) {
		t.Fatalf("ack = %+v", a)
	}

	rej := issue(t)
	_, _ = rej.SubmitAcknowledgement(fullAck(Reject, CannotMeetShipBy, "tx-r"), t0)
	evs, err = rej.ApplyAckOutcome(true, at)
	if err != nil || rej.State() != Rejected {
		t.Fatalf("reject: %v %s", err, rej.State())
	}
	assertEvents(t, evs, PurchaseOrderRejected{Header: shared.Header{Version: rej.Version(), At: at}, PONumber: rej.Number(), AssignmentID: "asg-1",
		ReleaseNo: 1, NodeID: "n-1", ReasonCode: CannotMeetShipBy, RejectedAt: at})
	assertEventMeta(t, evs[0], "PurchaseOrderRejected", string(rej.Number()))
}

func TestApplyAckOutcomeFailureReturnsToIssued(t *testing.T) {
	f := inState(t, AckProcessing)
	v := f.Version()
	evs, err := f.ApplyAckOutcome(false, t0.Add(time.Minute))
	if err != nil || f.State() != Issued || f.Version() != v+1 {
		t.Fatalf("failure: %v %v %s", evs, err, f.State())
	}
	assertEvents(t, evs)
	if _, ok := f.Ack(); ok {
		t.Fatal("a failed acknowledgement is forgotten")
	}
	if _, err := f.SubmitAcknowledgement(fullAck(Accept, "", "tx-again"), t0); err != nil {
		t.Fatalf("after a failed transaction the vendor can acknowledge again: %v", err)
	}
}

func TestApplyAckOutcomeOnlyWhileProcessing(t *testing.T) {
	for _, st := range allStates {
		if st == AckProcessing {
			continue
		}
		o := inState(t, st)
		if _, err := o.ApplyAckOutcome(true, t0); !errors.Is(err, shared.ErrIllegalTransition) || o.State() != st {
			t.Errorf("%s: err = %v", st, err)
		}
	}
}

func TestExpire(t *testing.T) {
	for _, st := range allStates {
		p := inState(t, st)
		_, err := p.Expire(p.AckBy().Add(time.Hour))
		if st == Issued {
			if err != nil || p.State() != Expired {
				t.Errorf("issued: %v %s", err, p.State())
			}
			continue
		}
		if !errors.Is(err, shared.ErrIllegalTransition) || p.State() != st {
			t.Errorf("%s: err = %v", st, err)
		}
	}
}

func TestExpireOnlyAfterAckBy(t *testing.T) {
	p := issue(t)
	if _, err := p.Expire(p.AckBy()); !errors.Is(err, shared.ErrIllegalTransition) {
		t.Errorf("at ackBy exactly the order is still inside its window: %v", err)
	}
	at := p.AckBy().Add(time.Nanosecond)
	evs, err := p.Expire(at)
	if err != nil || p.Version() != 2 {
		t.Fatalf("expire: %v v%d", err, p.Version())
	}
	assertEvents(t, evs, PurchaseOrderExpired{Header: shared.Header{Version: 2, At: at}, PONumber: p.Number(), AssignmentID: "asg-1",
		ReleaseNo: 1, NodeID: "n-1", AckBy: p.AckBy()})
	assertEventMeta(t, evs[0], "PurchaseOrderExpired", string(p.Number()))
}

func TestCancelByState(t *testing.T) {
	want := map[State]error{
		Issued: nil, AckProcessing: ErrCancelAfterAcknowledgement, Accepted: ErrCancelAfterAcknowledgement, Rejected: ErrCancelAfterAcknowledgement,
		Expired: shared.ErrIllegalTransition, ShipmentProcessing: ErrCancelAfterAcknowledgement, Shipped: ErrCancelAfterAcknowledgement,
		Cancelled: shared.ErrIllegalTransition,
	}
	for _, st := range allStates {
		p := inState(t, st)
		evs, err := p.Cancel("customer cancelled", t0.Add(time.Minute))
		if want[st] == nil {
			if err != nil || p.State() != Cancelled || p.CancelReason() != "customer cancelled" {
				t.Errorf("issued cancel: %v %s", err, p.State())
			}
			assertEvents(t, evs, PurchaseOrderCancelled{Header: shared.Header{Version: p.Version(), At: t0.Add(time.Minute)}, PONumber: p.Number(),
				AssignmentID: "asg-1", Reason: "customer cancelled"})
			assertEventMeta(t, evs[0], "PurchaseOrderCancelled", string(p.Number()))
			continue
		}
		if !errors.Is(err, want[st]) || p.State() != st {
			t.Errorf("%s: err = %v", st, err)
		}
	}
}

func TestCancelReasonRules(t *testing.T) {
	for _, reason := range []string{"", " ", " x", "x ", "a\tb"} {
		p := issue(t)
		if _, err := p.Cancel(reason, t0); !errors.Is(err, shared.ErrValidation) || p.State() != Issued {
			t.Errorf("reason %q: %v", reason, err)
		}
	}
	atLimit := strings.Repeat("r", MaxReason)
	p := issue(t)
	if _, err := p.Cancel(atLimit, t0); err != nil {
		t.Errorf("reason at the limit: %v", err)
	}
	q := issue(t)
	if _, err := q.Cancel(atLimit+"r", t0); !errors.Is(err, shared.ErrValidation) {
		t.Errorf("reason over the limit: %v", err)
	}
}
