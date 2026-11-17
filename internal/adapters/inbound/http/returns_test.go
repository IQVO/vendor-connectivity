package http_test

import (
	"context"
	"net/url"
	"testing"
	"time"

	"github.com/claudioed/vendor-connectivity/internal/domain/returns"
)

// advised makes one AUTHORIZED advice of the node visible, through the consumer.
func (a *api) advised(id, node string, at time.Time) {
	a.t.Helper()
	err := a.svc.OnReturnAuthorized(context.Background(), "auth-"+id, "ReturnAuthorized", returns.Authorized{
		AuthorizationID: id, OrderID: "ord-1", GroupNo: 1, NodeID: node, Disposition: "NEAREST_RETURNS_CENTER", Method: "DROP_OFF",
		Lines:     []returns.Line{{LineNo: 1, CatalogItemID: "ci-1", Quantity: 2, OwnerRef: "s-1"}},
		ExpiresAt: at.Add(30 * 24 * time.Hour), AuthorizedAt: at})
	if err != nil {
		a.t.Fatal(err)
	}
}

func TestPollReturnAdvicesPagesAndFilters(t *testing.T) {
	a := newAPI(t)
	a.advised("ret-1", "n-1", t0)
	a.advised("ret-2", "n-1", t0.Add(time.Hour))

	r := a.do("GET", "/vendor/return-advices?vendorId=V-1&limit=1", "")
	if r.code != 200 {
		t.Fatalf("poll = %d %s", r.code, r.body)
	}
	page := r.json(t)
	list := page["items"].([]any)
	if len(list) != 1 || page["nextCursor"] == nil {
		t.Fatalf("page = %s", r.body)
	}
	first := list[0].(map[string]any)
	if first["authorizationId"] != "ret-1" || first["state"] != "AUTHORIZED" || first["vendorId"] != "V-1" ||
		first["disposition"] != "NEAREST_RETURNS_CENTER" || len(first["lines"].([]any)) != 1 || len(first["trackingNumbers"].([]any)) != 0 {
		t.Fatalf("advice = %s", r.body)
	}
	next := a.do("GET", "/vendor/return-advices?vendorId=V-1&cursor="+url.QueryEscape(page["nextCursor"].(string)), "")
	if got := next.json(t); next.code != 200 || len(got["items"].([]any)) != 1 || got["nextCursor"] != nil {
		t.Fatalf("second page = %d %s", next.code, next.body)
	}
	if f := a.do("GET", "/vendor/return-advices?vendorId=V-1&nodeId=n-1&readyAfter="+url.QueryEscape(t0.Format(time.RFC3339)), ""); f.code != 200 ||
		len(f.json(t)["items"].([]any)) != 1 {
		t.Fatalf("filtered = %d %s", f.code, f.body)
	}
}

func TestGetReturnAdviceAnswersById(t *testing.T) {
	a := newAPI(t)
	a.advised("ret-2", "n-1", t0.Add(time.Hour))
	one := a.do("GET", "/vendor/return-advices/ret-2?vendorId=V-1", "")
	if one.code != 200 || one.json(t)["authorizationId"] != "ret-2" {
		t.Fatalf("get = %d %s", one.code, one.body)
	}
}

func TestReturnAdviceReadsRefuseWhatTheyCannotAnswer(t *testing.T) {
	a := newAPI(t)
	a.advised("ret-1", "n-1", t0)
	a.do("GET", "/vendor/return-advices/ret-404?vendorId=V-1", "").problem(t, 404, "not-found")
	a.do("GET", "/vendor/return-advices/ret-1", "").problem(t, 422, "validation-failed")
	a.do("GET", "/vendor/return-advices?vendorId=V-1&readyAfter=yesterday", "").problem(t, 422, "validation-failed")
	a.do("GET", "/vendor/return-advices?vendorId=V-1&limit=0", "").problem(t, 400, "invalid-limit")
	a.do("GET", "/vendor/return-advices?vendorId=V-1&nodeId=n-404", "").problem(t, 404, "not-found")
	a.do("GET", "/vendor/return-advices?vendorId=V-1&shippedAfter=x", "").problem(t, 400, "unknown-query-parameter")
}

func TestReturnOutcomeIsAsynchronous(t *testing.T) {
	a := newAPI(t)
	a.advised("ret-1", "n-1", t0)
	body := `{"vendorId":"V-1","authorizationId":"ret-1","nodeId":"n-1","receivedAt":"2026-11-14T09:30:00Z","lines":[` +
		`{"catalogItemId":"ci-1","ownerRef":"s-1","qtyRestockable":1,"qtyUnsellable":1},{"catalogItemId":"ci-9","qtyUnsellable":3}]}`
	r := a.do("POST", "/vendor/return-outcomes", body)
	if r.code != 202 || r.json(t)["transactionId"] == "" {
		t.Fatalf("outcome = %d %s", r.code, r.body)
	}
	id := r.json(t)["transactionId"].(string)
	tx := a.do("GET", "/vendor/transactions/"+id+"?vendorId=V-1", "")
	if got := tx.json(t); tx.code != 200 || got["kind"] != "RETURN_OUTCOME" || got["status"] != "PROCESSING" {
		t.Fatalf("transaction = %d %s", tx.code, tx.body)
	}
	// The advice left the poll but still answers by id with its terminal state.
	if p := a.do("GET", "/vendor/return-advices?vendorId=V-1", ""); p.code != 200 || len(p.json(t)["items"].([]any)) != 0 {
		t.Fatalf("poll after the outcome = %d %s", p.code, p.body)
	}
	if one := a.do("GET", "/vendor/return-advices/ret-1?vendorId=V-1", ""); one.code != 200 || one.json(t)["state"] != "OUTCOME_RECORDED" {
		t.Fatalf("advice after the outcome = %d %s", one.code, one.body)
	}
	a.do("POST", "/vendor/return-outcomes", body).problem(t, 409, "illegal-transition")
	a.do("POST", "/vendor/return-outcomes", `{"vendorId":"V-1","authorizationId":"ret-404","nodeId":"n-1","receivedAt":"2026-11-14T09:30:00Z","lines":[{"catalogItemId":"ci-1","qtyRestockable":1}]}`).problem(t, 404, "not-found")
	a.do("POST", "/vendor/return-outcomes", `{"vendorId":"V-1","authorizationId":"ret-1","nodeId":"n-2","receivedAt":"2026-11-14T09:30:00Z","lines":[{"catalogItemId":"ci-1","qtyRestockable":1}]}`).problem(t, 422, "validation-failed")
	a.do("POST", "/vendor/return-outcomes", `{"vendorId":"V-1","authorizationId":"ret-1","nodeId":"n-1","receivedAt":"2026-11-14T09:30:00Z","extra":1}`).problem(t, 422, "validation-failed")
	a.do("POST", "/vendor/return-outcomes", `not json`).problem(t, 422, "validation-failed")
}
