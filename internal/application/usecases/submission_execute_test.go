package usecases_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/claudioed/vendor-connectivity/internal/application/ports"
	"github.com/claudioed/vendor-connectivity/internal/application/usecases"
	"github.com/claudioed/vendor-connectivity/internal/domain/submission"
)

// A RETURN_OUTCOME is processed locally (ADR 0014, "Read model and consumer (no
// sync calls)"): the fact already left in the unit that accepted it
// (VendorReturnOutcomeSubmitted), so processing reaches SUCCESS without calling
// any neighbour. Every neighbour is down here on purpose: an outcome that
// depended on one would stay PROCESSING.
func TestReturnOutcomeIsProcessedLocallyWithNoNeighbourCall(t *testing.T) {
	h := adviceHarness(t)
	h.advised("ret-1", "n-1", t0)
	acc, err := h.svc.SubmitReturnOutcome(h.ctx, outcomeBody())
	h.must(err)
	h.nt.Err, h.offers.Err, h.ship.Err, h.perf.Err = ports.ErrUpstreamUnavailable, ports.ErrUpstreamUnavailable, ports.ErrUpstreamUnavailable, ports.ErrUpstreamUnavailable

	if n := h.processAll(); n != 1 {
		t.Errorf("submissions that reached an outcome = %d, want 1", n)
	}
	if got := h.neighbourCalls(); got != (neighbourCalls{}) {
		t.Errorf("neighbour calls = %+v (offer-availability feed ids %v), want none", got, h.offers.FeedIDs)
	}
	tx := h.tx("V-1", acc.TransactionID)
	if tx.Kind != "RETURN_OUTCOME" || tx.Status != "SUCCESS" || len(tx.Errors) != 0 || tx.ProcessedAt == nil || tx.PONumber != "" || tx.NodeID != "" {
		t.Errorf("transaction = %+v, want a SUCCESS with no errors, order or result node", tx)
	}
	sub, err := h.store.Submissions().Get(h.ctx, submission.ID(acc.TransactionID))
	h.must(err)
	if sub.Attempts() != 0 {
		t.Errorf("attempts = %d: a local outcome never waits for a neighbour", sub.Attempts())
	}
	assertOutcomeEventsOnce(t, h, acc.TransactionID)
}

// assertOutcomeEventsOnce pins the outbox after processing: the outcome fact
// was published once, when the outcome was accepted, and processing appended
// only SubmissionProcessed for the same transaction.
func assertOutcomeEventsOnce(t *testing.T, h *harness, txID string) {
	t.Helper()
	submitted := 0
	for _, name := range h.eventNames() {
		if name == "VendorReturnOutcomeSubmitted" {
			submitted++
		}
	}
	evs := h.store.Outbox().Events()
	last, ok := evs[len(evs)-1].(submission.SubmissionProcessed)
	if submitted != 1 || !ok || string(last.TransactionID) != txID || last.Kind != submission.KindReturnOutcome ||
		last.Status != submission.Success || last.ErrorCount != 0 {
		t.Errorf("events = %v, last = %+v; want one VendorReturnOutcomeSubmitted and a final SubmissionProcessed of the outcome", h.eventNames(), evs[len(evs)-1])
	}
}

// intendedPaths is the path every SubmissionKind is meant to take when it is
// executed, written as the neighbour calls it makes. It is the second half of
// a deliberate double entry: a kind added to the domain fails
// TestEveryDomainKindHasAnIntendedExecutionPath until its path is decided
// here AND it has its own case in the kind switch of submissions.go.
var intendedPaths = map[submission.Kind]neighbourCalls{
	submission.KindAcknowledgement:      {},             // local: finish applies the outcome to the purchase order
	submission.KindShipmentConfirmation: {},             // local: finish applies the outcome to the purchase order
	submission.KindInboundReceipt:       {},             // local: ADR 0005, no sync calls
	submission.KindReturnOutcome:        {},             // local: ADR 0014, no sync calls
	submission.KindNodeRegistration:     {registers: 1}, // node-topology RegisterNode
	submission.KindNodeProfile:          {profiles: 1},  // node-topology DeclareOperationalProfile
	submission.KindInventory:            {feeds: 1},     // offer-availability ApplyInventoryFeed
}

// Every Kind the domain declares has an explicit, intended path: it reaches
// SUCCESS on answering neighbours and makes exactly the calls of intendedPaths.
// A kind with no case of its own fails the submission (the next test), so it
// cannot pass here by falling through to another kind's processing.
func TestEveryDomainKindHasAnIntendedExecutionPath(t *testing.T) {
	kinds := domainKinds(t)
	if len(kinds) != len(intendedPaths) {
		t.Errorf("internal/domain/submission declares %d kinds %v, intendedPaths lists %d", len(kinds), kinds, len(intendedPaths))
	}
	for _, kind := range kinds {
		want, ok := intendedPaths[kind]
		if !ok {
			t.Errorf("kind %s is declared by the domain and has no intended path: decide how it is processed, give it its own case in the kind switch of submissions.go and add it to intendedPaths", kind)
			continue
		}
		t.Run(string(kind), func(t *testing.T) {
			h := newHarness(t, usecases.Config{})
			sub := mustReceive(t, "tx-kind", string(kind), `{}`)
			outcome, pending := h.svc.ExecuteKind(h.ctx, sub.Kind(), sub)
			if pending || outcome.Status != submission.Success || len(outcome.Errors) != 0 {
				t.Errorf("outcome = %+v pending = %v, want SUCCESS", outcome, pending)
			}
			if got := h.neighbourCalls(); got != want {
				t.Errorf("neighbour calls = %+v, want %+v", got, want)
			}
		})
	}
}

// The domain builds no submission of a kind it does not declare, so this is
// the guard for a kind that reached the domain's list and not the kind switch:
// it ends the transaction in FAILURE and calls no neighbour, where it used to
// be relayed to offer-availability as an inventory feed.
func TestAnUnknownKindFailsTheSubmissionAndCallsNoNeighbour(t *testing.T) {
	h := newHarness(t, usecases.Config{})
	sub := mustReceive(t, "tx-new", "INVENTORY", `{"vendorId":"V-1","nodeId":"n-1","lines":[{"listingId":"lst-1","availableQty":5,"basis":"PHYSICAL"}]}`)
	outcome, pending := h.svc.ExecuteKind(h.ctx, submission.Kind("SOMETHING_NEW"), sub)
	if pending || outcome.Status != submission.Failure || len(outcome.Errors) != 1 ||
		outcome.Errors[0].Code != usecases.CodeUnsupportedKind || !strings.Contains(outcome.Errors[0].Message, "SOMETHING_NEW") {
		t.Errorf("outcome = %+v pending = %v, want a FAILURE with %s naming the kind", outcome, pending, usecases.CodeUnsupportedKind)
	}
	if got := h.neighbourCalls(); got != (neighbourCalls{}) {
		t.Errorf("neighbour calls = %+v (offer-availability feed ids %v), want none", got, h.offers.FeedIDs)
	}
	// The domain accepts the outcome, so the transaction really ends in FAILURE.
	if _, err := sub.Complete(outcome, t0); err != nil || sub.Status() != submission.Failure {
		t.Errorf("Complete = %v, status %s", err, sub.Status())
	}
}

// domainKinds lists every Kind constant internal/domain/submission declares,
// read from its source: the domain keeps its list of valid kinds private.
func domainKinds(t *testing.T) []submission.Kind {
	t.Helper()
	const dir = "../../domain/submission"
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	var kinds []submission.Kind
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(dir, e.Name()), nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range f.Decls {
			if gd, ok := decl.(*ast.GenDecl); ok && gd.Tok == token.CONST {
				kinds = append(kinds, kindConstants(t, gd)...)
			}
		}
	}
	if len(kinds) == 0 {
		t.Fatalf("no Kind constant found in %s", dir)
	}
	return kinds
}

// kindConstants returns the values of the Kind constants of one const block.
// A constant named Kind... in any other form than `KindX Kind = "X"` stops the
// test: the enumeration must be extended before it silently skips a kind.
func kindConstants(t *testing.T, gd *ast.GenDecl) []submission.Kind {
	t.Helper()
	var kinds []submission.Kind
	for _, spec := range gd.Specs {
		vs, ok := spec.(*ast.ValueSpec)
		if !ok {
			continue
		}
		typ, _ := vs.Type.(*ast.Ident)
		isKind := typ != nil && typ.Name == "Kind"
		for i, name := range vs.Names {
			if !isKind && !strings.HasPrefix(name.Name, "Kind") {
				continue
			}
			var lit *ast.BasicLit
			if i < len(vs.Values) {
				lit, _ = vs.Values[i].(*ast.BasicLit)
			}
			if !isKind || lit == nil || lit.Kind != token.STRING {
				t.Fatalf("constant %s is not declared as `%s Kind = \"...\"`: teach kindConstants this form", name.Name, name.Name)
			}
			value, err := strconv.Unquote(lit.Value)
			if err != nil {
				t.Fatal(err)
			}
			kinds = append(kinds, submission.Kind(value))
		}
	}
	return kinds
}
