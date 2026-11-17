package returns

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/claudioed/vendor-connectivity/internal/domain/shared"
)

// The view only moves forward (ADR 0014): a state outside the vocabulary ranks
// below AUTHORIZED, and both terminal states rank above it, on the same step.
func TestRankOrdersTheStates(t *testing.T) {
	cases := []struct {
		state State
		want  int
	}{
		{"", 0},
		{"IN_TRANSIT", 0},
		{StateAuthorized, 1},
		{StateCancelled, 2},
		{StateOutcomeRecorded, 2},
	}
	for _, c := range cases {
		if got := rank(c.state); got != c.want {
			t.Errorf("rank(%q) = %d, want %d", c.state, got, c.want)
		}
	}
}

func TestFurthestKeepsTheLaterStateAndTheFirstOnATie(t *testing.T) {
	cases := []struct{ a, b, want State }{
		{"", StateAuthorized, StateAuthorized},
		{"", StateCancelled, StateCancelled},
		{StateAuthorized, "", StateAuthorized},
		{StateAuthorized, StateAuthorized, StateAuthorized},
		{StateAuthorized, StateCancelled, StateCancelled},
		{StateAuthorized, StateOutcomeRecorded, StateOutcomeRecorded},
		{StateCancelled, StateAuthorized, StateCancelled},
		{StateOutcomeRecorded, StateAuthorized, StateOutcomeRecorded},
		{StateCancelled, StateOutcomeRecorded, StateCancelled},
		{StateOutcomeRecorded, StateCancelled, StateOutcomeRecorded},
	}
	for _, c := range cases {
		if got := furthest(c.a, c.b); got != c.want {
			t.Errorf("furthest(%q, %q) = %q, want %q", c.a, c.b, got, c.want)
		}
	}
}

func TestOnCancelledMovesAnAuthorizedAdviceToCancelled(t *testing.T) {
	a, err := (Advice{}).OnAuthorized(auth())
	if err != nil {
		t.Fatal(err)
	}
	cancelled, err := a.OnCancelled()
	if err != nil {
		t.Fatal(err)
	}
	if cancelled.State != StateCancelled || cancelled.Pollable() {
		t.Fatalf("a cancelled authorization leaves the poll: state %s pollable %v", cancelled.State, cancelled.Pollable())
	}
	if cancelled.AuthorizationID != "ret-1" || cancelled.NodeID != "n-1" || cancelled.VendorID != "V-1" || len(cancelled.Lines) != 1 {
		t.Fatalf("cancelling must change the state only: %+v", cancelled)
	}
	if a.State != StateAuthorized {
		t.Fatalf("the receiver must not be changed: %s", a.State)
	}
	again, err := cancelled.OnCancelled()
	if err != nil || again.State != StateCancelled {
		t.Fatalf("a repeated ReturnCancelled keeps CANCELLED: %v %s", err, again.State)
	}
	if _, err := cancelled.RecordOutcome(); !errors.Is(err, shared.ErrIllegalTransition) {
		t.Fatalf("an outcome for a cancelled advice = %v, want illegal transition", err)
	}
	ok := Outcome{AuthorizationID: "ret-1", VendorID: "V-1", NodeID: "n-1", ReceivedAt: at(),
		Lines: []OutcomeLine{{CatalogItemID: "ci-1", QtyRestockable: 1}}}
	if err := cancelled.CheckOutcome(ok); !errors.Is(err, shared.ErrIllegalTransition) {
		t.Fatalf("checking an outcome against a cancelled advice = %v, want illegal transition", err)
	}
	if _, err := (Advice{}).OnCancelled(); !errors.Is(err, shared.ErrValidation) {
		t.Fatalf("a cancellation for an advice never authorized = %v, want a validation error", err)
	}
}

func adviceLines(n int) []Line {
	out := make([]Line, n)
	for i := range out {
		out[i] = Line{LineNo: i + 1, CatalogItemID: fmt.Sprintf("ci-%d", i+1), Quantity: 1}
	}
	return out
}

func TestOnAuthorizedLineCountBoundaries(t *testing.T) {
	cases := []struct {
		count   int
		refused bool
	}{{0, true}, {1, false}, {49, false}, {50, false}, {51, true}}
	for _, c := range cases {
		e := auth()
		e.Lines = adviceLines(c.count)
		a, err := (Advice{}).OnAuthorized(e)
		if c.refused {
			if !errors.Is(err, shared.ErrValidation) || !strings.Contains(err.Error(), "between 1 and 50 lines") {
				t.Errorf("%d lines: err = %v, want the 1..50 lines validation error", c.count, err)
			}
			continue
		}
		if err != nil || len(a.Lines) != c.count {
			t.Errorf("%d lines: err = %v, advice lines = %d, want accepted with every line", c.count, err, len(a.Lines))
		}
	}
}

func TestOnAuthorizedLineQuantityBoundaries(t *testing.T) {
	cases := []struct {
		quantity int
		refused  bool
	}{{-1, true}, {0, true}, {1, false}, {2147483646, false}, {2147483647, false}, {2147483648, true}}
	for _, c := range cases {
		e := auth()
		e.Lines = []Line{{LineNo: 1, CatalogItemID: "ci-1", Quantity: c.quantity}}
		a, err := (Advice{}).OnAuthorized(e)
		if c.refused {
			if !errors.Is(err, shared.ErrValidation) || !strings.Contains(err.Error(), "lines[0]: quantity must be between 1 and 2147483647") {
				t.Errorf("quantity %d: err = %v, want the 1..2147483647 validation error", c.quantity, err)
			}
			continue
		}
		if err != nil || len(a.Lines) != 1 || a.Lines[0].Quantity != c.quantity {
			t.Errorf("quantity %d: err = %v, lines = %+v, want accepted unchanged", c.quantity, err, a.Lines)
		}
	}
}

func outcomeWith(lines []OutcomeLine) Outcome {
	return Outcome{AuthorizationID: "ret-1", VendorID: "V-1", NodeID: "n-1", ReceivedAt: at(), Lines: lines}
}

func TestOutcomeLineCountBoundaries(t *testing.T) {
	cases := []struct {
		count   int
		refused bool
	}{{0, true}, {1, false}, {49, false}, {50, false}, {51, true}}
	for _, c := range cases {
		lines := make([]OutcomeLine, c.count)
		for i := range lines {
			lines[i] = OutcomeLine{CatalogItemID: fmt.Sprintf("ci-%d", i+1), QtyRestockable: 1}
		}
		got, err := outcomeWith(lines).Normalise()
		if c.refused {
			if !errors.Is(err, shared.ErrValidation) || !strings.Contains(err.Error(), "between 1 and 50 lines") {
				t.Errorf("%d lines: err = %v, want the 1..50 lines validation error", c.count, err)
			}
			continue
		}
		if err != nil || len(got.Lines) != c.count {
			t.Errorf("%d lines: err = %v, outcome lines = %d, want accepted with every line", c.count, err, len(got.Lines))
		}
	}
}

func TestOutcomeQuantityBoundaries(t *testing.T) {
	cases := []struct {
		restockable, unsellable int
		refused                 bool
	}{
		{0, 0, false},
		{2147483646, 0, false},
		{2147483647, 0, false},
		{0, 2147483646, false},
		{0, 2147483647, false},
		{2147483647, 2147483647, false},
		{2147483648, 0, true},
		{0, 2147483648, true},
		{-1, 0, true},
		{0, -1, true},
	}
	for _, c := range cases {
		got, err := outcomeWith([]OutcomeLine{{CatalogItemID: "ci-1", QtyRestockable: c.restockable, QtyUnsellable: c.unsellable}}).Normalise()
		if c.refused {
			if !errors.Is(err, shared.ErrValidation) || !strings.Contains(err.Error(), "lines[0]: quantities must be between 0 and 2147483647") {
				t.Errorf("%d/%d: err = %v, want the 0..2147483647 validation error", c.restockable, c.unsellable, err)
			}
			continue
		}
		if err != nil || len(got.Lines) != 1 || got.Lines[0].QtyRestockable != c.restockable || got.Lines[0].QtyUnsellable != c.unsellable {
			t.Errorf("%d/%d: err = %v, lines = %+v, want accepted unchanged", c.restockable, c.unsellable, err, got.Lines)
		}
	}
}
