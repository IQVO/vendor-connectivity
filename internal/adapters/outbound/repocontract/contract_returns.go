package repocontract

import (
	"context"
	"testing"
	"time"

	"github.com/claudioed/vendor-connectivity/internal/application/ports"
	"github.com/claudioed/vendor-connectivity/internal/application/repository"
	"github.com/claudioed/vendor-connectivity/internal/domain/returns"
)

func adviceRow(id, node string, readyAt time.Time, state returns.State) returns.Advice {
	return returns.Advice{AuthorizationID: id, VendorID: vendorOne, NodeID: node, Disposition: "NEAREST_RETURNS_CENTER",
		Method: "DROP_OFF", State: state, ReadyAt: readyAt, ExpiresAt: readyAt.Add(30 * 24 * time.Hour),
		Lines: []returns.Line{{LineNo: 1, CatalogItemID: "ci-1", Quantity: 2, OwnerRef: sellerOne}}}
}

// returnAdvices covers the ReturnAdviceView repository contract: round trip,
// upsert and the poll's filter semantics (state, node set, strict readyAfter,
// keyset tie on the id, limit).
func returnAdvices(t *testing.T, e Env) {
	ctx := context.Background()
	_, err := e.Advices.Get(ctx, "ret-1")
	isErr(t, err, ports.ErrNotFound, "Get of an unknown advice")

	a, err := adviceRow("ret-1", nodeOne, T0, returns.StateAuthorized).OnAuthorized(returns.Authorized{
		AuthorizationID: "ret-1", VendorID: vendorOne, NodeID: nodeOne, Disposition: "NEAREST_RETURNS_CENTER", Method: "DROP_OFF",
		Lines:     []returns.Line{{LineNo: 1, CatalogItemID: "ci-1", Quantity: 2, OwnerRef: sellerOne}},
		ExpiresAt: T0.Add(30 * 24 * time.Hour), AuthorizedAt: T0})
	must(t, err)
	labeled, err := a.OnLabelIssued("TRACK-1")
	must(t, err)
	must(t, e.Advices.Save(ctx, labeled))

	got, err := e.Advices.Get(ctx, "ret-1")
	must(t, err)
	if got.VendorID != vendorOne || got.NodeID != nodeOne || got.State != returns.StateAuthorized || !got.ReadyAt.Equal(T0) ||
		!got.ExpiresAt.Equal(T0.Add(30*24*time.Hour)) || len(got.Lines) != 1 || got.Lines[0].OwnerRef != sellerOne ||
		len(got.TrackingNumbers) != 1 || got.TrackingNumbers[0] != "TRACK-1" {
		t.Fatalf("round trip = %+v", got)
	}
	done, err := got.RecordOutcome()
	must(t, err)
	must(t, e.Advices.Save(ctx, done))
	again, _ := e.Advices.Get(ctx, "ret-1")
	if again.State != returns.StateOutcomeRecorded {
		t.Fatalf("an upsert replaces the row: %+v", again)
	}
	listReady(t, e)
}

func listReady(t *testing.T, e Env) {
	ctx := context.Background()
	rows := []returns.Advice{
		adviceRow("ret-2", nodeOne, T0.Add(2*time.Hour), returns.StateAuthorized),
		adviceRow("ret-3", nodeOne, T0.Add(2*time.Hour), returns.StateAuthorized),
		adviceRow("ret-4", nodeTwo, T0.Add(3*time.Hour), returns.StateAuthorized),
		adviceRow("ret-5", nodeOne, T0.Add(4*time.Hour), returns.StateCancelled),
		adviceRow("ret-6", "n-3", T0.Add(5*time.Hour), returns.StateAuthorized),
	}
	for _, a := range rows {
		must(t, e.Advices.Save(ctx, a))
	}
	ids := func(f repository.ReturnAdviceFilter) string {
		t.Helper()
		got, err := e.Advices.ListReady(ctx, f)
		must(t, err)
		out := ""
		for _, r := range got {
			out += r.AuthorizationID + " "
		}
		return out
	}
	// ret-1 is OUTCOME_RECORDED, ret-5 CANCELLED and ret-6 another node: none is listed.
	if got := ids(repository.ReturnAdviceFilter{NodeIDs: []string{nodeOne, nodeTwo}}); got != "ret-2 ret-3 ret-4 " {
		t.Errorf("both nodes = %q", got)
	}
	if got := ids(repository.ReturnAdviceFilter{NodeIDs: []string{nodeTwo}}); got != "ret-4 " {
		t.Errorf("n-2 = %q", got)
	}
	after := T0.Add(2 * time.Hour)
	if got := ids(repository.ReturnAdviceFilter{NodeIDs: []string{nodeOne, nodeTwo}, ReadyAfter: &after}); got != "ret-4 " {
		t.Errorf("readyAfter is strict = %q", got)
	}
	cur := repository.AdviceCursor{ReadyAt: T0.Add(2 * time.Hour), AuthorizationID: "ret-2"}
	if got := ids(repository.ReturnAdviceFilter{NodeIDs: []string{nodeOne, nodeTwo}, After: &cur}); got != "ret-3 ret-4 " {
		t.Errorf("keyset breaks the tie on the id = %q", got)
	}
	if got := ids(repository.ReturnAdviceFilter{NodeIDs: []string{nodeOne, nodeTwo}, Limit: 2}); got != "ret-2 ret-3 " {
		t.Errorf("limit = %q", got)
	}
	if got := ids(repository.ReturnAdviceFilter{NodeIDs: []string{nodeNever}}); got != "" {
		t.Errorf("unknown node = %q", got)
	}
}
