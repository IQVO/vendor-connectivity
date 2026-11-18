package returns

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/claudioed/vendor-connectivity/internal/domain/shared"
)

func at() time.Time { return time.Date(2026, 11, 14, 10, 0, 0, 0, time.UTC) }

func auth() Authorized {
	return Authorized{AuthorizationID: "ret-1", OrderID: "ord-1", GroupNo: 2, VendorID: "V-1", NodeID: "n-1",
		Disposition: "NEAREST_RETURNS_CENTER", Method: "DROP_OFF",
		Lines:     []Line{{LineNo: 1, CatalogItemID: "ci-1", Quantity: 2, OwnerRef: "s-1"}},
		ExpiresAt: at().Add(30 * 24 * time.Hour), AuthorizedAt: at()}
}

func TestOnAuthorizedCreatesPollableAdvice(t *testing.T) {
	a, err := (Advice{}).OnAuthorized(auth())
	if err != nil {
		t.Fatal(err)
	}
	if a.State != StateAuthorized || !a.Pollable() || a.VendorID != "V-1" || a.NodeID != "n-1" {
		t.Fatalf("advice = %+v", a)
	}
	if !a.ReadyAt.Equal(at()) || !a.ExpiresAt.Equal(at().Add(30*24*time.Hour)) {
		t.Fatalf("readyAt/expiry = %v/%v", a.ReadyAt, a.ExpiresAt)
	}
	if a.TrackingNumbers == nil || len(a.TrackingNumbers) != 0 {
		t.Fatalf("trackingNumbers must start empty and non-nil, got %v", a.TrackingNumbers)
	}
}

func TestOnAuthorizedValidation(t *testing.T) {
	cases := map[string]struct {
		mutate func(Authorized) Authorized
		named  string
	}{
		"authorizationId": {func(e Authorized) Authorized { e.AuthorizationID = "??"; return e }, "authorizationId"},
		"nodeId":          {func(e Authorized) Authorized { e.NodeID = ""; return e }, "nodeId"},
		"vendorId":        {func(e Authorized) Authorized { e.VendorID = "n/1"; return e }, "vendorId"},
		"authorizedAt":    {func(e Authorized) Authorized { e.AuthorizedAt = time.Time{}; return e }, "authorizedAt"},
		"no lines":        {func(e Authorized) Authorized { e.Lines = nil; return e }, "lines"},
		"line quantity":   {func(e Authorized) Authorized { e.Lines = []Line{{CatalogItemID: "ci-1", Quantity: 0}}; return e }, "quantity"},
		"line item id":    {func(e Authorized) Authorized { e.Lines = []Line{{CatalogItemID: "!!", Quantity: 1}}; return e }, "catalogItemId"},
	}
	for name, c := range cases {
		if _, err := (Advice{}).OnAuthorized(c.mutate(auth())); err == nil || !strings.Contains(err.Error(), c.named) {
			t.Errorf("%s: err = %v, want it to name %q", name, err, c.named)
		}
	}
	if _, err := (Advice{}).OnAuthorized(auth()); err != nil {
		t.Fatalf("baseline must pass: %v", err)
	}
}

func TestOnAuthorizedTooManyLines(t *testing.T) {
	e := auth()
	e.Lines = make([]Line, MaxAdviceLines+1)
	for i := range e.Lines {
		e.Lines[i] = Line{LineNo: i + 1, CatalogItemID: "ci-1", Quantity: 1}
	}
	if _, err := (Advice{}).OnAuthorized(e); err == nil {
		t.Fatal("want a validation error for 51 lines")
	}
}

func TestStateNeverMovesBack(t *testing.T) {
	a, err := (Advice{}).OnAuthorized(auth())
	if err != nil {
		t.Fatal(err)
	}
	done, err := a.RecordOutcome()
	if err != nil {
		t.Fatal(err)
	}
	late, err := done.OnCancelled()
	if err != nil {
		t.Fatal(err)
	}
	if late.State != StateOutcomeRecorded {
		t.Fatalf("cancel after outcome must not reopen: %s", late.State)
	}
	if again, err := (Advice{}).OnAuthorized(auth()); err != nil || again.State != StateAuthorized {
		t.Fatalf("a fresh authorization is AUTHORIZED: %v %s", err, again.State)
	}
	re, err := late.OnAuthorized(auth())
	if err != nil {
		t.Fatal(err)
	}
	if re.State != StateOutcomeRecorded {
		t.Fatalf("a replayed ReturnAuthorized must keep the terminal state: %s", re.State)
	}
	if re.TrackingNumbers == nil {
		t.Fatal("a replay must keep the tracking numbers")
	}
}

func TestOnLabelIssued(t *testing.T) {
	a, _ := (Advice{}).OnAuthorized(auth())
	b, err := a.OnLabelIssued("TRACK-1")
	if err != nil || len(b.TrackingNumbers) != 1 || b.State != StateAuthorized {
		t.Fatalf("label = %v err = %v", b.TrackingNumbers, err)
	}
	c, err := b.OnLabelIssued("TRACK-1")
	if err != nil || len(c.TrackingNumbers) != 1 {
		t.Fatalf("a repeated tracking number must be idempotent: %v %v", c.TrackingNumbers, err)
	}
	if _, err := a.OnLabelIssued(""); err == nil {
		t.Fatal("an empty tracking number is refused")
	}
	full := a
	full.TrackingNumbers = []string{"T1", "T2", "T3", "T4", "T5"}
	if _, err := full.OnLabelIssued("T6"); err == nil {
		t.Fatal("the sixth tracking number is refused")
	}
	if _, err := (Advice{}).OnLabelIssued("T1"); err == nil {
		t.Fatal("a label for an advice never authorized is refused")
	}
}

func TestOutcomeNormalise(t *testing.T) {
	base := Outcome{AuthorizationID: "ret-1", VendorID: "V-1", NodeID: "n-1", ReceivedAt: at(),
		Lines: []OutcomeLine{{CatalogItemID: "ci-1", OwnerRef: "s-1", QtyRestockable: 2, QtyUnsellable: 1}}}
	if _, err := base.Normalise(); err != nil {
		t.Fatal(err)
	}
	cases := map[string]struct {
		mutate func(Outcome) Outcome
		named  string
	}{
		"authorizationId": {func(o Outcome) Outcome { o.AuthorizationID = "x!"; return o }, "authorizationId"},
		"nodeId":          {func(o Outcome) Outcome { o.NodeID = ""; return o }, "nodeId"},
		"vendorId":        {func(o Outcome) Outcome { o.VendorID = " "; return o }, "vendorId"},
		"receivedAt":      {func(o Outcome) Outcome { o.ReceivedAt = time.Time{}; return o }, "receivedAt"},
		"no lines":        {func(o Outcome) Outcome { o.Lines = nil; return o }, "lines"},
		"catalogItemId":   {func(o Outcome) Outcome { o.Lines[0].CatalogItemID = "?"; return o }, "catalogItemId"},
		"quantities":      {func(o Outcome) Outcome { o.Lines[0].QtyRestockable = -1; return o }, "quantities"},
	}
	for name, c := range cases {
		fresh := base
		fresh.Lines = append([]OutcomeLine(nil), base.Lines...)
		if _, err := c.mutate(fresh).Normalise(); err == nil || !strings.Contains(err.Error(), c.named) {
			t.Errorf("%s: err = %v, want it to name %q", name, err, c.named)
		}
	}
	dup := base
	dup.Lines = []OutcomeLine{{CatalogItemID: "ci-1", OwnerRef: "s-1"}, {CatalogItemID: "ci-1", OwnerRef: "s-1"}}
	if _, err := dup.Normalise(); err == nil {
		t.Fatal("a duplicate line is refused")
	}
	twoOwners := base
	twoOwners.Lines = []OutcomeLine{{CatalogItemID: "ci-1", OwnerRef: "s-1"}, {CatalogItemID: "ci-1", OwnerRef: ""}}
	if _, err := twoOwners.Normalise(); err != nil {
		t.Fatalf("the same item under two owners is two lines: %v", err)
	}
}

func TestCheckOutcomeAndRecord(t *testing.T) {
	a, _ := (Advice{}).OnAuthorized(auth())
	ok := Outcome{AuthorizationID: "ret-1", VendorID: "V-1", NodeID: "n-1", ReceivedAt: at(),
		Lines: []OutcomeLine{{CatalogItemID: "ci-9", QtyRestockable: 7}}}
	if err := a.CheckOutcome(ok); err != nil {
		t.Fatalf("an unexpected line is never refused: %v", err)
	}
	wrongNode := ok
	wrongNode.NodeID = "n-2"
	if err := a.CheckOutcome(wrongNode); err == nil || !errors.Is(err, shared.ErrValidation) {
		t.Fatalf("wrong node = %v", err)
	}
	wrongVendor := ok
	wrongVendor.VendorID = "V-2"
	if err := a.CheckOutcome(wrongVendor); err == nil || !errors.Is(err, shared.ErrValidation) {
		t.Fatalf("wrong vendor = %v", err)
	}
	done, _ := a.RecordOutcome()
	if err := done.CheckOutcome(ok); !errors.Is(err, shared.ErrIllegalTransition) {
		t.Fatalf("an advice with a recorded outcome refuses a second: %v", err)
	}
	if _, err := done.RecordOutcome(); !errors.Is(err, shared.ErrIllegalTransition) {
		t.Fatalf("double record = %v", err)
	}
}

func TestOutcomeSubmittedEvent(t *testing.T) {
	o := Outcome{AuthorizationID: "ret-1", VendorID: "V-1", NodeID: "n-1", ReceivedAt: at(),
		Lines: []OutcomeLine{{CatalogItemID: "ci-1", OwnerRef: "s-1", QtyRestockable: 2, QtyUnsellable: 1}}}
	e := NewOutcomeSubmitted("tx-1", "V-1", o, at())
	if e.EventName() != "VendorReturnOutcomeSubmitted" || e.Entity() != "returnoutcome" || e.SubjectID() != "ret-1" {
		t.Fatalf("event = %s %s %s", e.EventName(), e.Entity(), e.SubjectID())
	}
	if e.Version != 1 || !e.At.Equal(at()) {
		t.Fatalf("header = %+v", e.Header)
	}
}
