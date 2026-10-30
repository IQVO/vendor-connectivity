package a2a

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2aclient"
	"github.com/a2aproject/a2a-go/v2/a2asrv"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/claudioed/vendor-connectivity/internal/application/gateway"
	"github.com/claudioed/vendor-connectivity/internal/application/views"
)

func TestWireGetPurchaseOrderReturnsTheVendorsView(t *testing.T) {
	f := newFixture(t)
	w := newWire(t, f.deps())

	env, text := w.skill(t, skillGetPurchaseOrder, map[string]any{"vendorId": "V-1", "poNumber": f.po})
	if env.Problem != nil || env.Data == nil || env.Data.PurchaseOrder == nil || env.Skill != skillGetPurchaseOrder {
		t.Fatalf("envelope = %+v", env)
	}
	assertOrderView(t, f.po, env.Data.PurchaseOrder)
	if want := f.po + ": issued, node n-1, 1 line(s), ack by "; !strings.HasPrefix(text, want) {
		t.Fatalf("summary = %q, want prefix %q", text, want)
	}
	// ADR 0006's privacy promise holds on this surface too: never an orderId,
	// a ship-to address or a delivery promise.
	for _, private := range []string{"orderId", "shipTo", "address", "promise"} {
		if strings.Contains(strings.ToLower(env.raw), strings.ToLower(private)) {
			t.Fatalf("the answer mentions %q: %s", private, env.raw)
		}
	}
}

// assertOrderView pins the seeded order as the vendor sees it.
func assertOrderView(t *testing.T, poNumber string, po *wireOrder) {
	t.Helper()
	identity := po.PONumber == poNumber && po.VendorID == "V-1" && po.NodeID == "n-1" && po.State == "ISSUED"
	lines := len(po.Lines) == 1 && po.Lines[0].ListingID == "lst-1" && po.Lines[0].Quantity == 2
	if !identity || !lines || po.Labels == nil || po.TrackingNumbers == nil {
		t.Fatalf("purchase order = %+v", po)
	}
}

func TestWireGetNodeScorecardRelaysTheWindows(t *testing.T) {
	f := newFixture(t)
	onTime := 0.97
	f.perf.Scorecard = views.Scorecard{Windows: []views.ScorecardWindow{
		{Window: "7d", Grade: "A", OnTimeHandoverRate: &onTime, Observations: 120, ComputedAt: now},
		{Window: "30d", Grade: "B", Observations: 400, ComputedAt: now},
	}}
	w := newWire(t, f.deps())

	env, text := w.skill(t, skillGetNodeScorecard, map[string]any{"vendorId": "V-1", "nodeId": "n-1"})
	if env.Problem != nil || env.Data == nil || env.Data.Scorecard == nil {
		t.Fatalf("envelope = %+v", env)
	}
	sc := env.Data.Scorecard
	if sc.NodeID != "n-1" || len(sc.Windows) != 2 || sc.Windows[0].Window != "7d" || sc.Windows[0].Grade != "A" ||
		sc.Windows[0].OnTimeRate == nil || *sc.Windows[0].OnTimeRate != onTime || sc.Windows[0].Observations != 120 {
		t.Fatalf("scorecard = %+v", sc)
	}
	if text != "node n-1: 7d A, 30d B" {
		t.Fatalf("summary = %q", text)
	}
}

// A domain rejection is answered in-band with the type slug and the status
// the REST adapter gives the same outcome (problemCatalogue), never a
// JSON-RPC error.
func TestWireDomainRejectionsAreProblemsWithTheRESTClassification(t *testing.T) {
	f := newFixture(t)
	w := newWire(t, f.deps())
	cases := map[string]struct {
		skill  string
		args   map[string]any
		status int
		slug   string
	}{
		"order: unknown order":      {skillGetPurchaseOrder, map[string]any{"vendorId": "V-1", "poNumber": "PO-nope"}, 404, "not-found"},
		"order: unknown vendor":     {skillGetPurchaseOrder, map[string]any{"vendorId": "V-404", "poNumber": f.po}, 404, "not-found"},
		"order: another vendor's":   {skillGetPurchaseOrder, map[string]any{"vendorId": "V-2", "poNumber": f.po}, 404, "node-not-owned-by-vendor"},
		"order: malformed vendorId": {skillGetPurchaseOrder, map[string]any{"vendorId": "a b", "poNumber": f.po}, 422, "validation-failed"},
		"node: unknown node":        {skillGetNodeScorecard, map[string]any{"vendorId": "V-1", "nodeId": "n-404"}, 404, "not-found"},
		"node: another vendor's":    {skillGetNodeScorecard, map[string]any{"vendorId": "V-2", "nodeId": "n-1"}, 404, "node-not-owned-by-vendor"},
		"node: malformed vendorId":  {skillGetNodeScorecard, map[string]any{"vendorId": "a b", "nodeId": "n-1"}, 422, "validation-failed"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			env, text := w.skill(t, tc.skill, tc.args)
			wantProblem(t, env, tc.status, tc.slug)
			if env.Skill != tc.skill || !strings.Contains(text, env.Problem.Detail) {
				t.Fatalf("skill = %q, text = %q, problem = %+v", env.Skill, text, env.Problem)
			}
		})
	}
}

// The scorecard is relayed from node-performance: its outage is 503 and its
// refusal 502 (REST: upstream-unavailable / upstream-refused), and the
// neighbour's own text (a URL, a message) is not echoed to the agent.
func TestWireNeighbourFailuresAreProblemsAndDoNotLeak(t *testing.T) {
	f := newFixture(t)
	w := newWire(t, f.deps())
	args := map[string]any{"vendorId": "V-1", "nodeId": "n-1"}

	f.perf.Err = errors.New("dial tcp 10.0.0.7:443: connection refused")
	env, text := w.skill(t, skillGetNodeScorecard, args)
	wantProblem(t, env, 503, "upstream-unavailable")
	assertNoLeak(t, env, text, "10.0.0.7")

	f.perf.Err = &gateway.Refusal{Status: 403, Code: "forbidden", Message: "token abc123 rejected", Path: "/scorecards/n-1"}
	env, text = w.skill(t, skillGetNodeScorecard, args)
	wantProblem(t, env, 502, "upstream-refused")
	assertNoLeak(t, env, text, "abc123")

	// A neighbour 404 is the node having no scorecard yet: not-found.
	f.perf.Err = &gateway.Refusal{Status: 404, Code: "not-found", Message: "no scorecard"}
	env, _ = w.skill(t, skillGetNodeScorecard, args)
	wantProblem(t, env, 404, "not-found")
}

func assertNoLeak(t *testing.T, env wireEnvelope, text string, secrets ...string) {
	t.Helper()
	for _, s := range []string{env.Problem.Detail, env.Problem.Title, text} {
		for _, secret := range secrets {
			if strings.Contains(s, secret) {
				t.Fatalf("an answer leaks %q: %q", secret, s)
			}
		}
	}
}

// failingReads makes every read fail with an infrastructure error.
type failingReads struct{}

var errBoom = errors.New("pgx: connection refused to 10.0.0.7:5432 password=hunter2")

func (failingReads) GetPurchaseOrder(context.Context, string, string) (views.PurchaseOrder, error) {
	return views.PurchaseOrder{}, errBoom
}

func (failingReads) GetScorecard(context.Context, string, string) (views.Scorecard, error) {
	return views.Scorecard{}, errBoom
}

// An unexpected failure is answered as a 500 problem, never a JSON-RPC error,
// and its raw text (which may carry infrastructure detail) is not echoed.
func TestWireUnexpectedFailureIs500AndDoesNotLeakTheCause(t *testing.T) {
	w := newWire(t, Deps{Reads: failingReads{}})
	cases := map[string]struct {
		skill string
		args  map[string]any
	}{
		"order": {skillGetPurchaseOrder, map[string]any{"vendorId": "V-1", "poNumber": "PO-1"}},
		"node":  {skillGetNodeScorecard, map[string]any{"vendorId": "V-1", "nodeId": "n-1"}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			env, text := w.skill(t, tc.skill, tc.args)
			wantProblem(t, env, 500, "internal-error")
			assertNoLeak(t, env, text, "hunter2", "10.0.0.7")
		})
	}
}

// countingReads counts the reads that reach the use cases.
type countingReads struct {
	Reads
	calls atomic.Int32
}

func (c *countingReads) GetPurchaseOrder(ctx context.Context, v, po string) (views.PurchaseOrder, error) {
	c.calls.Add(1)
	return c.Reads.GetPurchaseOrder(ctx, v, po)
}

func (c *countingReads) GetScorecard(ctx context.Context, v, n string) (views.Scorecard, error) {
	c.calls.Add(1)
	return c.Reads.GetScorecard(ctx, v, n)
}

// A NUL character cannot be stored in a Postgres text parameter and aborts the
// surrounding transaction (a 500 on the real runtime, invisible on the
// in-memory doubles). Like the REST adapter, which answers a NUL in a request
// path with 404 not-found before any repository, the skills answer it as a
// 404 and never reach the use case.
func TestWireNULInAnIdentifierIsNotFoundAndNeverReachesTheUseCase(t *testing.T) {
	f := newFixture(t)
	spy := &countingReads{Reads: f.svc}
	w := newWire(t, Deps{Reads: spy})

	env, _ := w.skill(t, skillGetPurchaseOrder, map[string]any{"vendorId": "V-1", "poNumber": "PO-1\u0000x"})
	wantProblem(t, env, 404, "not-found")
	env, _ = w.skill(t, skillGetNodeScorecard, map[string]any{"vendorId": "V-1", "nodeId": "n-1\u0000"})
	wantProblem(t, env, 404, "not-found")
	if n := spy.calls.Load(); n != 0 {
		t.Fatalf("%d read(s) reached the use cases with a NUL identifier", n)
	}
}

func TestWireArgumentValidation(t *testing.T) {
	f := newFixture(t)
	spy := &countingReads{Reads: f.svc}
	w := newWire(t, Deps{Reads: spy})
	order := func(extra map[string]any) map[string]any {
		return merge(map[string]any{"vendorId": "V-1", "poNumber": f.po}, extra)
	}
	node := func(extra map[string]any) map[string]any {
		return merge(map[string]any{"vendorId": "V-1", "nodeId": "n-1"}, extra)
	}
	cases := map[string]struct {
		skill string
		args  map[string]any
	}{
		"order: missing vendorId":    {skillGetPurchaseOrder, map[string]any{"poNumber": f.po}},
		"order: missing poNumber":    {skillGetPurchaseOrder, map[string]any{"vendorId": "V-1"}},
		"order: no arguments":        {skillGetPurchaseOrder, map[string]any{}},
		"order: blank vendorId":      {skillGetPurchaseOrder, order(map[string]any{"vendorId": "  "})},
		"order: blank poNumber":      {skillGetPurchaseOrder, order(map[string]any{"poNumber": ""})},
		"order: poNumber wrong type": {skillGetPurchaseOrder, order(map[string]any{"poNumber": 7})},
		"order: unknown argument":    {skillGetPurchaseOrder, order(map[string]any{"force": true})},
		"order: typo'd argument":     {skillGetPurchaseOrder, map[string]any{"vendorId": "V-1", "poNumber": f.po, "poNumbr": f.po}},
		"order: wrong-skill arg":     {skillGetPurchaseOrder, order(map[string]any{"nodeId": "n-1"})},
		"node: missing vendorId":     {skillGetNodeScorecard, map[string]any{"nodeId": "n-1"}},
		"node: missing nodeId":       {skillGetNodeScorecard, map[string]any{"vendorId": "V-1"}},
		"node: blank nodeId":         {skillGetNodeScorecard, node(map[string]any{"nodeId": " "})},
		"node: vendorId wrong type":  {skillGetNodeScorecard, node(map[string]any{"vendorId": true})},
		"node: unknown argument":     {skillGetNodeScorecard, node(map[string]any{"window": "7d"})},
		"node: wrong-skill arg":      {skillGetNodeScorecard, node(map[string]any{"poNumber": f.po})},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			env, _ := w.skill(t, tc.skill, tc.args)
			wantProblem(t, env, 400, "invalid-argument")
		})
	}
	if n := spy.calls.Load(); n != 0 {
		t.Fatalf("%d read(s) reached the use cases with a refused argument set", n)
	}
}

func merge(base, extra map[string]any) map[string]any {
	for k, v := range extra {
		base[k] = v
	}
	return base
}

func TestWireRequestShapeProblems(t *testing.T) {
	f := newFixture(t)
	w := newWire(t, f.deps())

	env, _ := w.call(t, a2a.NewTextPart("not json at all"))
	wantProblem(t, env, 400, "malformed-skill-request")

	env, _ = w.call(t, a2a.NewDataPart(map[string]any{"arguments": map[string]any{}}))
	wantProblem(t, env, 400, "malformed-skill-request")

	env, _ = w.call(t, a2a.NewDataPart(map[string]any{"skill": skillGetPurchaseOrder, "arguments": map[string]any{}, "extra": 1}))
	wantProblem(t, env, 400, "malformed-skill-request")

	// Trailing data after the JSON document is refused too.
	env, _ = w.call(t, a2a.NewTextPart(`{"skill":"get_purchase_order","arguments":{}} {"skill":"get_node_scorecard"}`))
	wantProblem(t, env, 400, "malformed-skill-request")

	env, _ = w.skill(t, "launch_rocket", map[string]any{})
	wantProblem(t, env, 400, "unknown-skill")

	// A text part holding the same JSON is accepted (documented contract).
	text := `{"skill":"get_purchase_order","arguments":{"vendorId":"V-1","poNumber":"` + f.po + `"}}`
	env, _ = w.call(t, a2a.NewTextPart(text))
	if env.Problem != nil || env.Data == nil || env.Data.PurchaseOrder == nil || env.Data.PurchaseOrder.PONumber != f.po {
		t.Fatalf("a text part holding the skill JSON must be accepted, got %+v", env)
	}
}

// The other MCP tools and every write of the REST surface are not offered
// here: asking for one is an unknown skill, answered in-band.
func TestWireOffersOnlyTheTwoCuratedSkills(t *testing.T) {
	f := newFixture(t)
	w := newWire(t, f.deps())
	for _, skill := range []string{
		"poll_purchase_orders", "get_vendor", "get_transaction", "list_vendor_nodes",
		"submit_acknowledgement", "onboard_vendor", "suspend_vendor", "request_label", "submit_shipment_confirmation",
	} {
		env, _ := w.skill(t, skill, map[string]any{"vendorId": "V-1"})
		wantProblem(t, env, 400, "unknown-skill")
	}
}

// The reply carries the caller's contextId, so a delegating saga can group
// its interactions.
func TestWireReplyKeepsTheCallersContext(t *testing.T) {
	f := newFixture(t)
	w := newWire(t, f.deps())
	msg := a2a.NewMessage(a2a.MessageRoleUser, a2a.NewDataPart(map[string]any{
		"skill": skillGetPurchaseOrder, "arguments": map[string]any{"vendorId": "V-1", "poNumber": f.po},
	}))
	msg.ContextID = "ctx-saga-1"
	resp, err := w.client.SendMessage(context.Background(), &a2a.SendMessageRequest{Message: msg})
	if err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	reply, ok := resp.(*a2a.Message)
	if !ok || reply.ContextID != "ctx-saga-1" {
		t.Fatalf("reply = %#v, want a message in context ctx-saga-1", resp)
	}
}

// Every skill run is one OTel span named "a2a.skill <id>" carrying the
// a2a.skill attribute (the counterpart of the MCP adapter's mcp.tool span).
func TestSkillRunEmitsOneSpanWithTheSkillAttribute(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder)))
	t.Cleanup(func() { otel.SetTracerProvider(previous) })

	f := newFixture(t)
	w := newWire(t, f.deps())
	w.skill(t, skillGetPurchaseOrder, map[string]any{"vendorId": "V-1", "poNumber": f.po})
	w.skill(t, skillGetNodeScorecard, map[string]any{"vendorId": "V-404", "nodeId": "n-1"})

	got := map[string]string{}
	for _, span := range recorder.Ended() {
		for _, attr := range span.Attributes() {
			if string(attr.Key) == "a2a.skill" {
				got[span.Name()] = attr.Value.AsString()
			}
		}
	}
	want := map[string]string{
		"a2a.skill " + skillGetPurchaseOrder: skillGetPurchaseOrder,
		"a2a.skill " + skillGetNodeScorecard: skillGetNodeScorecard,
	}
	if len(got) != len(want) {
		t.Fatalf("skill spans = %v, want %v", got, want)
	}
	for name, skill := range want {
		if got[name] != skill {
			t.Fatalf("skill spans = %v, want %v", got, want)
		}
	}
}

// TestCardIsServedAndDialable proves discovery end to end: the card handler
// serves the card at the well-known path and a client built from the FETCHED
// card reaches the agent.
func TestCardIsServedAndDialable(t *testing.T) {
	f := newFixture(t)
	mux := http.NewServeMux()
	mux.Handle("/", Handler(NewExecutor(f.deps())))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	mux.Handle(a2asrv.WellKnownAgentCardPath, a2asrv.NewStaticAgentCardHandler(Card(Config{AgentURL: srv.URL + "/", Version: "test"})))

	res, err := http.Get(srv.URL + a2asrv.WellKnownAgentCardPath)
	if err != nil || res.StatusCode != http.StatusOK {
		t.Fatalf("card fetch = %v / %v", res, err)
	}
	defer func() { _ = res.Body.Close() }()
	var card a2a.AgentCard
	if err := json.NewDecoder(res.Body).Decode(&card); err != nil {
		t.Fatal(err)
	}
	client, err := a2aclient.NewFromCard(context.Background(), &card)
	if err != nil {
		t.Fatal(err)
	}
	w := &wire{client: client}
	env, _ := w.skill(t, skillGetPurchaseOrder, map[string]any{"vendorId": "V-1", "poNumber": f.po})
	if env.Data == nil || env.Data.PurchaseOrder == nil {
		t.Fatalf("a client built from the served card could not call the agent: %+v", env)
	}
}
