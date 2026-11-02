package http_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	vchttp "github.com/claudioed/vendor-connectivity/internal/adapters/inbound/http"
	"github.com/claudioed/vendor-connectivity/internal/adapters/outbound/memory"
	"github.com/claudioed/vendor-connectivity/internal/application/gateway"
	"github.com/claudioed/vendor-connectivity/internal/application/requests"
	"github.com/claudioed/vendor-connectivity/internal/application/usecases"
	"github.com/claudioed/vendor-connectivity/internal/domain/purchaseorder"
)

var t0 = time.Date(2026, 10, 22, 13, 0, 0, 0, time.UTC)

// api is the router over the in-memory doubles, with vendor V-1 onboarded and
// its node n-1 known.
type api struct {
	t      *testing.T
	h      http.Handler
	store  *memory.Store
	svc    *usecases.Service
	clock  *memory.Clock
	nt     *memory.NodeTopology
	ship   *memory.ShippingTracking
	perf   *memory.NodePerformance
	events int
}

func newAPI(t *testing.T) *api {
	t.Helper()
	s := memory.NewStore()
	a := &api{t: t, store: s, clock: memory.NewClock(t0), nt: &memory.NodeTopology{}, ship: &memory.ShippingTracking{}, perf: &memory.NodePerformance{}}
	a.svc = usecases.New(usecases.Deps{
		UoW: s.UnitOfWork(), Vendors: s.Vendors(), Orders: s.Orders(), Submissions: s.Submissions(), Nodes: s.Nodes(), Listings: s.Listings(), Shipments: s.Shipments(), Packaging: s.Packaging(),
		Outbox: s.Outbox(), Inbox: s.Inbox(), Clock: a.clock, IDs: &memory.SequentialIDs{}, Random: memory.NewScriptedRandom(),
		NodeTopology: a.nt, Offers: &memory.OfferAvailability{}, Shipping: a.ship, Performance: a.perf,
	}, usecases.Config{})
	a.h = vchttp.NewRouter(&vchttp.Server{Service: a.svc}, slog.New(slog.DiscardHandler), "")
	ctx := context.Background()
	if _, _, err := a.svc.OnboardVendor(ctx, requests.OnboardVendor{VendorID: "V-1", Name: "Carrier A", AckWindowMinutes: 240}); err != nil {
		t.Fatal(err)
	}
	a.node("n-1", "V-1")
	return a
}

func (a *api) node(id, vendor string) {
	a.t.Helper()
	a.events++
	err := a.svc.OnNodeRegistered(context.Background(), "ev-"+id, "NodeRegistered", requests.NodeRegisteredFacts{NodeID: id, NodeType: "FULFILLMENT_CENTER", Subtype: "SORTABLE",
		Operator: "VENDOR", VendorID: vendor, VendorNodeCode: "code-" + id, RegionID: "r-1", Timezone: "America/Sao_Paulo"})
	if err != nil {
		a.t.Fatal(err)
	}
}

// issue creates an ISSUED purchase order for the node through the consumer.
func (a *api) issue(assignment, node string) string {
	a.t.Helper()
	err := a.svc.IssueOnRelease(context.Background(), "rel-"+assignment, "FulfillmentReleasedToNode", requests.ReleasedToNode{AssignmentID: assignment, ReleaseNo: 1,
		OrderID: "ord-" + assignment, GroupNo: 1, NodeID: node, RequiredShipBy: t0.Add(8 * time.Hour), Tier: "NEXT_DAY",
		Lines: []requests.ReleasedLine{{ListingID: "lst-1", Quantity: 2}}})
	if err != nil {
		a.t.Fatal(err)
	}
	return string(purchaseorder.NumberFor(assignment, 1))
}

type result struct {
	code int
	body string
	hdr  http.Header
}

func (a *api) do(method, target, body string, hdr ...string) result {
	a.t.Helper()
	var rd io.Reader = http.NoBody
	if body != "" {
		rd = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, target, rd)
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	rec := httptest.NewRecorder()
	a.h.ServeHTTP(rec, req)
	return result{rec.Code, rec.Body.String(), rec.Header()}
}

func (r result) json(t *testing.T) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(r.body), &m); err != nil {
		t.Fatalf("body %q: %v", r.body, err)
	}
	return m
}

func (r result) problem(t *testing.T, status int, slug string) {
	t.Helper()
	m := r.json(t)
	if r.code != status || m["type"] != slug || r.hdr.Get("Content-Type") != "application/problem+json" || m["status"] != float64(status) {
		t.Fatalf("got %d %s, want %d %s", r.code, r.body, status, slug)
	}
}

func TestHealthz(t *testing.T) {
	a := newAPI(t)
	if r := a.do("GET", "/healthz", ""); r.code != 200 || r.json(t)["status"] != "ok" {
		t.Fatalf("healthz = %d %s", r.code, r.body)
	}
	if r := a.do("GET", "/readyz", ""); r.code != 200 || r.json(t)["status"] != "ready" {
		t.Fatalf("readyz = %d %s", r.code, r.body)
	}
}

func TestVendorLifecycle(t *testing.T) {
	a := newAPI(t)
	created := a.do("POST", "/vendors", `{"vendorId":"V-2","name":"Carrier B","ackWindowMinutes":120}`)
	if created.code != 201 || created.json(t)["state"] != "ACTIVE" {
		t.Fatalf("onboard = %d %s", created.code, created.body)
	}
	if again := a.do("POST", "/vendors", `{"vendorId":"V-2","name":"Carrier B","ackWindowMinutes":120}`); again.code != 200 {
		t.Fatalf("a repeat onboarding is 200: %d %s", again.code, again.body)
	}
	a.do("POST", "/vendors", `{"vendorId":"V-3","name":"c","ackWindowMinutes":5}`).problem(t, 422, "ack-window-out-of-range")
	if g := a.do("GET", "/vendors/V-2", ""); g.code != 200 || g.json(t)["name"] != "Carrier B" {
		t.Fatalf("get = %d %s", g.code, g.body)
	}
	a.do("GET", "/vendors/V-9", "").problem(t, 404, "not-found")
	if s := a.do("POST", "/vendors/V-2/suspensions", `{"reason":"late"}`); s.code != 200 || s.json(t)["state"] != "SUSPENDED" {
		t.Fatalf("suspend = %d %s", s.code, s.body)
	}
	if again := a.do("POST", "/vendors/V-2/suspensions", ""); again.code != 200 || again.json(t)["state"] != "SUSPENDED" {
		t.Fatalf("suspending twice is a no-op: %d %s", again.code, again.body)
	}
	if s := a.do("POST", "/vendors/V-2/reinstatements", ""); s.code != 200 || s.json(t)["state"] != "ACTIVE" {
		t.Fatalf("reinstate = %d %s", s.code, s.body)
	}
	a.do("POST", "/vendors/V-9/suspensions", "").problem(t, 404, "not-found")
	a.do("POST", "/vendors/V-2/suspensions", `{"nope":1}`).problem(t, 422, "validation-failed")
}

func TestPollAndGetPurchaseOrders(t *testing.T) {
	a := newAPI(t)
	po1, po2 := a.issue("asg-1", "n-1"), a.issue("asg-2", "n-1")
	r := a.do("GET", "/vendor/purchase-orders?vendorId=V-1&limit=1", "")
	page := r.json(t)
	list, _ := page["items"].([]any)
	if r.code != 200 || len(list) != 1 || page["nextCursor"] == "" || page["nextToken"] != nil || list[0].(map[string]any)["poNumber"] != po1 {
		t.Fatalf("page 1 = %d %s", r.code, r.body)
	}
	next := a.do("GET", "/vendor/purchase-orders?vendorId=V-1&limit=1&cursor="+page["nextCursor"].(string), "").json(t)
	if got := next["items"].([]any)[0].(map[string]any)["poNumber"]; got != po2 {
		t.Fatalf("page 2 = %v", next)
	}
	if r := a.do("GET", "/vendor/purchase-orders?vendorId=V-1&nodeId=n-1&createdAfter=2026-10-22T12:00:00Z", ""); r.code != 200 {
		t.Fatalf("filters: %d %s", r.code, r.body)
	}
	a.do("GET", "/vendor/purchase-orders?vendorId=V-1&createdAfter=yesterday", "").problem(t, 422, "validation-failed")
	for _, bad := range []string{"limit=0", "limit=x", "limit=", "limit=501", "limit=1&limit=2"} {
		a.do("GET", "/vendor/purchase-orders?vendorId=V-1&"+bad, "").problem(t, 400, "invalid-limit")
	}
	for _, bad := range []string{"cursor=%25%25%25", "cursor=e30", "cursor=a&cursor=b"} {
		a.do("GET", "/vendor/purchase-orders?vendorId=V-1&"+bad, "").problem(t, 400, "invalid-cursor")
	}
	// a cursor issued to another vendor is not valid for this one
	a.do("POST", "/vendors", `{"vendorId":"V-7","name":"c","ackWindowMinutes":120}`)
	a.do("GET", "/vendor/purchase-orders?vendorId=V-7&cursor="+page["nextCursor"].(string), "").problem(t, 400, "invalid-cursor")
	// retired paging parameters fail loudly
	for _, retired := range []string{"nextToken=x", "after=x", "offset=1", "from=x"} {
		a.do("GET", "/vendor/purchase-orders?vendorId=V-1&"+retired, "").problem(t, 400, "unknown-query-parameter")
		a.do("GET", "/vendor/inbound-shipments?vendorId=V-1&"+retired, "").problem(t, 400, "unknown-query-parameter")
		a.do("GET", "/vendor/nodes?vendorId=V-1&"+retired, "").problem(t, 400, "unknown-query-parameter")
	}
	a.do("GET", "/vendor/purchase-orders", "").problem(t, 422, "validation-failed")
	a.do("GET", "/vendor/purchase-orders?vendorId=V-9", "").problem(t, 404, "not-found")
	if g := a.do("GET", "/vendor/purchase-orders/"+po1+"?vendorId=V-1", ""); g.code != 200 || g.json(t)["state"] != "ISSUED" {
		t.Fatalf("get = %d %s", g.code, g.body)
	}
	a.do("GET", "/vendor/purchase-orders/PO-nope?vendorId=V-1", "").problem(t, 404, "not-found")
	a.do("POST", "/vendors", `{"vendorId":"V-2","name":"c","ackWindowMinutes":120}`)
	a.do("GET", "/vendor/purchase-orders/"+po1+"?vendorId=V-2", "").problem(t, 404, "node-not-owned-by-vendor")
}

func (a *api) ack(po, decision string) string {
	return `{"vendorId":"V-1","poNumber":"` + po + `","decision":"` + decision + `","lines":[{"lineNo":1,"quantity":2}]}`
}

func TestAcknowledgementIsAsynchronous(t *testing.T) {
	a := newAPI(t)
	po := a.issue("asg-1", "n-1")
	r := a.do("POST", "/vendor/acknowledgements", a.ack(po, "ACCEPT"))
	tx, _ := r.json(t)["transactionId"].(string)
	if r.code != 202 || tx == "" {
		t.Fatalf("ack = %d %s", r.code, r.body)
	}
	if g := a.do("GET", "/vendor/transactions/"+tx+"?vendorId=V-1", ""); g.code != 200 || g.json(t)["status"] != "PROCESSING" {
		t.Fatalf("transaction = %d %s", g.code, g.body)
	}
	if n, err := a.svc.ProcessDueSubmissions(context.Background()); err != nil || n != 1 {
		t.Fatalf("process: %d %v", n, err)
	}
	if g := a.do("GET", "/vendor/transactions/"+tx+"?vendorId=V-1", ""); g.json(t)["status"] != "SUCCESS" {
		t.Fatalf("transaction = %s", g.body)
	}
	a.do("GET", "/vendor/transactions/"+tx+"?vendorId=V-9", "").problem(t, 404, "not-found")
	a.do("POST", "/vendor/acknowledgements", a.ack(po, "ACCEPT")).problem(t, 409, "already-acknowledged")
	fresh := a.issue("asg-2", "n-1")
	a.do("POST", "/vendor/acknowledgements", `{"vendorId":"V-1","poNumber":"`+fresh+`","decision":"ACCEPT","lines":[]}`).problem(t, 422, "partial-acknowledgement")
	a.do("POST", "/vendor/acknowledgements", `{"vendorId":"V-1"`).problem(t, 422, "validation-failed")
	a.do("POST", "/vendor/acknowledgements", `{"vendorId":"V-1","extra":1}`).problem(t, 422, "validation-failed")
	a.do("POST", "/vendor/acknowledgements", a.ack(po, "ACCEPT")+" {}").problem(t, 422, "validation-failed")
	a.do("POST", "/vendor/acknowledgements", "").problem(t, 422, "validation-failed")
	late := a.issue("asg-late", "n-1")
	a.clock.Advance(5 * time.Hour)
	a.do("POST", "/vendor/acknowledgements", a.ack(late, "ACCEPT")).problem(t, 409, "ack-window-closed")
}

func TestSuspendedVendorWritesAreRefused(t *testing.T) {
	a := newAPI(t)
	po := a.issue("asg-1", "n-1")
	a.do("POST", "/vendors/V-1/suspensions", "")
	a.do("POST", "/vendor/acknowledgements", a.ack(po, "ACCEPT")).problem(t, 409, "vendor-suspended")
	if r := a.do("GET", "/vendor/purchase-orders?vendorId=V-1", ""); r.code != 200 {
		t.Fatalf("reads still work: %d", r.code)
	}
}

func TestNodeRoutes(t *testing.T) {
	a := newAPI(t)
	a.nt.Registered = gateway.RegisteredNode{NodeID: "n-2", NodeType: "FULFILLMENT_CENTER", VendorID: "V-1", VendorNodeCode: "SITE-2", Status: "PROPOSED"}
	reg := a.do("POST", "/vendor/nodes", `{"vendorId":"V-1","vendorNodeCode":"SITE-2","nodeType":"FULFILLMENT_CENTER","regionId":"r-1","timezone":"America/Sao_Paulo","postalCode":"07750-000","countryCode":"BR"}`)
	if reg.code != 202 {
		t.Fatalf("register = %d %s", reg.code, reg.body)
	}
	a.do("POST", "/vendor/nodes", `{"vendorId":"V-1","vendorNodeCode":"X","nodeType":"SORTATION_CENTER","regionId":"r","timezone":"UTC","postalCode":"1","countryCode":"BR"}`).problem(t, 422, "validation-failed")
	if g := a.do("GET", "/vendor/nodes/n-1?vendorId=V-1", ""); g.code != 200 || g.json(t)["vendorNodeCode"] != "code-n-1" {
		t.Fatalf("get node = %d %s", g.code, g.body)
	}
	if l := a.do("GET", "/vendor/nodes?vendorId=V-1", ""); l.code != 200 || len(l.json(t)["items"].([]any)) != 1 {
		t.Fatalf("list = %d %s", l.code, l.body)
	}
	a.do("GET", "/vendor/nodes?vendorId=", "").problem(t, 422, "validation-failed")
	a.do("GET", "/vendor/nodes/n-404?vendorId=V-1", "").problem(t, 404, "not-found")
	profile := `{"vendorId":"V-1","profileRevision":1,"timezone":"UTC","operatingHoursByDay":{"monday":{"start":"06:00","end":"22:00"}},"handlingTimeMinutes":60,"cutoffs":[],"throughputCap":{"value":10,"timeUnit":"DAY","unit":"ORDER"}}`
	if p := a.do("PUT", "/vendor/nodes/n-1/profile", profile); p.code != 202 {
		t.Fatalf("profile = %d %s", p.code, p.body)
	}
	a.do("PUT", "/vendor/nodes/n-404/profile", profile).problem(t, 404, "not-found")
	a.do("PUT", "/vendor/nodes/n-1/profile", `[`).problem(t, 422, "validation-failed")
}

func TestScorecardAndUpstreamFailures(t *testing.T) {
	a := newAPI(t)
	if r := a.do("GET", "/vendor/nodes/n-1/scorecard?vendorId=V-1", ""); r.code != 200 {
		t.Fatalf("scorecard = %d %s", r.code, r.body)
	}
	a.perf.Err = io.ErrUnexpectedEOF
	a.do("GET", "/vendor/nodes/n-1/scorecard?vendorId=V-1", "").problem(t, 503, "upstream-unavailable")
	a.perf.Err = &gateway.Refusal{Status: 422, Code: "ErrX", Message: "m"}
	a.do("GET", "/vendor/nodes/n-1/scorecard?vendorId=V-1", "").problem(t, 502, "upstream-refused")
}

func TestInventoryLabelsAndShipment(t *testing.T) {
	a := newAPI(t)
	po := a.issue("asg-1", "n-1")
	feed := `{"vendorId":"V-1","nodeId":"n-1","fullUpdate":true,"asOf":"2026-10-22T13:00:00Z","lines":[{"listingId":"lst-1","availableQty":3,"basis":"PHYSICAL"}]}`
	if r := a.do("POST", "/vendor/inventory", feed); r.code != 202 {
		t.Fatalf("inventory = %d %s", r.code, r.body)
	}
	label := `{"vendorId":"V-1","poNumber":"` + po + `","packages":[{"packageNo":1}]}`
	a.do("POST", "/vendor/shipping-labels", label).problem(t, 409, "label-before-acceptance")
	a.do("POST", "/vendor/acknowledgements", a.ack(po, "ACCEPT"))
	if _, err := a.svc.ProcessDueSubmissions(context.Background()); err != nil {
		t.Fatal(err)
	}
	l := a.do("POST", "/vendor/shipping-labels", label)
	if l.code != 200 || len(l.json(t)["labels"].([]any)) != 1 {
		t.Fatalf("label = %d %s", l.code, l.body)
	}
	ship := `{"vendorId":"V-1","poNumber":"` + po + `","shippedAt":"2026-10-22T15:00:00Z","trackingNumbers":["TRK-` + po + `-1"]}`
	a.do("POST", "/vendor/shipment-confirmations", strings.Replace(ship, "TRK-", "BAD-", 1)).problem(t, 409, "confirm-without-label")
	if s := a.do("POST", "/vendor/shipment-confirmations", ship); s.code != 202 {
		t.Fatalf("shipment = %d %s", s.code, s.body)
	}
	a.ship.Err = io.ErrUnexpectedEOF
	a.do("POST", "/vendor/shipping-labels", `{"vendorId":"V-1","poNumber":"`+po+`","packages":[{"packageNo":2}]}`).problem(t, 409, "illegal-transition")
}

func TestUnknownRouteAndMethod(t *testing.T) {
	a := newAPI(t)
	if r := a.do("GET", "/nope", ""); r.code != 404 {
		t.Fatalf("404 expected: %d", r.code)
	}
	if r := a.do("DELETE", "/vendors/V-1", ""); r.code != 405 {
		t.Fatalf("405 expected: %d", r.code)
	}
}

func TestIdempotencyAndRateLimitMiddlewareWiring(t *testing.T) {
	a := newAPI(t)
	calls := 0
	idem := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; next.ServeHTTP(w, r) })
	}
	lim := vchttp.NewLimiter(1)
	h := vchttp.NewRouter(&vchttp.Server{Service: a.svc, Idempotency: idem, Limiter: lim}, slog.New(slog.DiscardHandler), "")
	do := func(method, target, body string) int {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(method, target, strings.NewReader(body)))
		return rec.Code
	}
	if c := do("POST", "/vendors", `{"vendorId":"V-7","name":"c","ackWindowMinutes":120}`); c != 201 || calls != 1 {
		t.Fatalf("writes go through the idempotency middleware: %d %d", c, calls)
	}
	if c := do("GET", "/vendors/V-7", ""); c != 200 || calls != 1 {
		t.Fatalf("reads do not: %d %d", c, calls)
	}
	if c := do("GET", "/vendor/nodes?vendorId=V-1", ""); c != 200 {
		t.Fatalf("first /vendor call: %d", c)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/vendor/nodes?vendorId=V-1", http.NoBody))
	if rec.Code != 429 || rec.Header().Get("Retry-After") == "" || !strings.Contains(rec.Body.String(), `"type":"rate-limited"`) {
		t.Fatalf("over the limit: %d %v %s", rec.Code, rec.Header(), rec.Body)
	}
	if c := do("GET", "/healthz", ""); c != 200 {
		t.Fatal("probes are not rate limited")
	}
}

// TestRetiredRoutesAreGone pins the REST guideline cutover: the :verb routes
// no longer exist (no alias, no dual route).
func TestRetiredRoutesAreGone(t *testing.T) {
	a := newAPI(t)
	for _, route := range []string{"/vendors/V-1:suspend", "/vendors/V-1:reinstate"} {
		if r := a.do("POST", route, ""); r.code != 404 && r.code != 405 {
			t.Errorf("POST %s = %d, want 404 or 405", route, r.code)
		}
	}
}

// TestNodesPageWalksEveryNode pages /vendor/nodes with limit=2 over five nodes
// and asserts no duplicate and no skipped node.
func TestNodesPageWalksEveryNode(t *testing.T) {
	a := newAPI(t)
	for _, id := range []string{"n-2", "n-3", "n-4", "n-5"} {
		a.node(id, "V-1")
	}
	seen := map[string]bool{}
	url := "/vendor/nodes?vendorId=V-1&limit=2"
	for pages := 0; url != ""; pages++ {
		if pages > 5 {
			t.Fatal("paging does not terminate")
		}
		body := a.do("GET", url, "").json(t)
		for _, it := range body["items"].([]any) {
			id := it.(map[string]any)["nodeId"].(string)
			if seen[id] {
				t.Fatalf("duplicate %s", id)
			}
			seen[id] = true
		}
		url = ""
		if c, _ := body["nextCursor"].(string); c != "" {
			url = "/vendor/nodes?vendorId=V-1&limit=2&cursor=" + c
		}
	}
	if len(seen) != 5 {
		t.Fatalf("saw %d nodes: %v", len(seen), seen)
	}
}
