package a2a

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2aclient"

	"github.com/claudioed/vendor-connectivity/internal/adapters/outbound/memory"
	"github.com/claudioed/vendor-connectivity/internal/application/requests"
	"github.com/claudioed/vendor-connectivity/internal/application/usecases"
	"github.com/claudioed/vendor-connectivity/internal/domain/purchaseorder"
)

// now is the fixture's fixed "now".
var now = time.Date(2026, 10, 24, 9, 0, 0, 0, time.UTC)

// fixture is the real application Service over the in-memory doubles, exactly
// as cmd/a2a wires it (minus Postgres). The write use cases exist ONLY here,
// to seed data the way the REST adapter, the consumers and the background
// loops would: the A2A adapter itself holds none (Deps.Reads).
type fixture struct {
	svc  *usecases.Service
	perf *memory.NodePerformance
	po   string
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// newFixture seeds vendor V-1 with node n-1 and one ISSUED purchase order,
// and vendor V-2 with node n-2.
func newFixture(t *testing.T) *fixture {
	t.Helper()
	ctx := context.Background()
	s := memory.NewStore()
	perf := &memory.NodePerformance{}
	svc := usecases.New(usecases.Deps{
		UoW: s.UnitOfWork(), Vendors: s.Vendors(), Orders: s.Orders(), Submissions: s.Submissions(),
		Nodes: s.Nodes(), Listings: s.Listings(), Shipments: s.Shipments(), Packaging: s.Packaging(),
		Outbox: s.Outbox(), Inbox: s.Inbox(),
		Clock: memory.NewClock(now), IDs: &memory.SequentialIDs{}, Random: memory.NewScriptedRandom(),
		NodeTopology: &memory.NodeTopology{}, Offers: &memory.OfferAvailability{},
		Shipping: &memory.ShippingTracking{}, Performance: perf,
	}, usecases.Config{})
	for vendor, node := range map[string]string{"V-1": "n-1", "V-2": "n-2"} {
		_, _, err := svc.OnboardVendor(ctx, requests.OnboardVendor{VendorID: vendor, Name: "North Fulfilment", AckWindowMinutes: 240})
		must(t, err)
		must(t, svc.OnNodeRegistered(ctx, "ev-"+node, "NodeRegistered", requests.NodeRegisteredFacts{
			NodeID: node, NodeType: "FULFILLMENT_CENTER", Subtype: "SORTABLE", Operator: "VENDOR", VendorID: vendor,
			VendorNodeCode: "code-" + node, RegionID: "r-1", Timezone: "America/Sao_Paulo",
		}))
	}
	must(t, svc.IssueOnRelease(ctx, "rel-a-9f31", "FulfillmentReleasedToNode", requests.ReleasedToNode{
		AssignmentID: "a-9f31", ReleaseNo: 1, OrderID: "ord-a-9f31", GroupNo: 1, NodeID: "n-1",
		RequiredShipBy: now.Add(8 * time.Hour), Tier: "NEXT_DAY",
		Lines: []requests.ReleasedLine{{ListingID: "lst-1", Quantity: 2}},
	}))
	return &fixture{svc: svc, perf: perf, po: string(purchaseorder.NumberFor("a-9f31", 1))}
}

// The narrow Reads interface is satisfied by the real application Service.
var _ Reads = (*usecases.Service)(nil)

func (f *fixture) deps() Deps { return Deps{Reads: f.svc} }

// ---- the wire: JSON-RPC over HTTP, driven by the SDK's own client ----------

// wireEnvelope is a TEST-LOCAL copy of the answer contract. If the adapter's
// envelope drifts, this suite fails instead of silently following it.
type wireEnvelope struct {
	Skill string `json:"skill"`
	Data  *struct {
		PurchaseOrder *wireOrder     `json:"purchaseOrder"`
		Scorecard     *wireScorecard `json:"scorecard"`
	} `json:"data"`
	Problem *wireProblem `json:"problem"`
	raw     string
}

type wireProblem struct {
	Type   string `json:"type"`
	Title  string `json:"title"`
	Detail string `json:"detail"`
	Status int    `json:"status"`
}

type wireOrder struct {
	PONumber string `json:"poNumber"`
	VendorID string `json:"vendorId"`
	NodeID   string `json:"nodeId"`
	State    string `json:"state"`
	AckBy    string `json:"ackBy"`
	Lines    []struct {
		LineNo    int    `json:"lineNo"`
		ListingID string `json:"listingId"`
		Quantity  int    `json:"quantity"`
	} `json:"lines"`
	Labels          []json.RawMessage `json:"labels"`
	TrackingNumbers []string          `json:"trackingNumbers"`
}

type wireScorecard struct {
	NodeID  string `json:"nodeId"`
	Windows []struct {
		Window       string   `json:"window"`
		Grade        string   `json:"grade"`
		OnTimeRate   *float64 `json:"onTimeHandoverRate"`
		Observations int      `json:"observations"`
	} `json:"windows"`
}

type wire struct {
	client *a2aclient.Client
}

func newWire(t *testing.T, deps Deps) *wire {
	t.Helper()
	srv := httptest.NewServer(Handler(NewExecutor(deps)))
	t.Cleanup(srv.Close)
	card := Card(Config{AgentURL: srv.URL + "/", Version: "test"})
	client, err := a2aclient.NewFromCard(context.Background(), card)
	if err != nil {
		t.Fatalf("NewFromCard: %v", err)
	}
	return &wire{client: client}
}

// call sends one message part and returns the decoded envelope and the
// one-line text summary of the agent's reply.
func (w *wire) call(t *testing.T, part *a2a.Part) (wireEnvelope, string) {
	t.Helper()
	resp, err := w.client.SendMessage(context.Background(), &a2a.SendMessageRequest{Message: a2a.NewMessage(a2a.MessageRoleUser, part)})
	if err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	msg, ok := resp.(*a2a.Message)
	if !ok || len(msg.Parts) < 2 {
		t.Fatalf("reply = %T, want a message with a data and a text part", resp)
	}
	data, ok := msg.Parts[0].Content.(a2a.Data)
	if !ok {
		t.Fatalf("first reply part is %T, want data", msg.Parts[0].Content)
	}
	raw, _ := json.Marshal(data.Value)
	var env wireEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("reply is not the skill envelope: %v (%s)", err, raw)
	}
	env.raw = string(raw)
	text, _ := msg.Parts[1].Content.(a2a.Text)
	return env, string(text)
}

func (w *wire) skill(t *testing.T, skill string, args map[string]any) (wireEnvelope, string) {
	t.Helper()
	return w.call(t, a2a.NewDataPart(map[string]any{"skill": skill, "arguments": args}))
}

func wantProblem(t *testing.T, env wireEnvelope, status int, slug string) {
	t.Helper()
	if env.Problem == nil {
		t.Fatalf("expected a %d %s problem, got data: %+v", status, slug, env.Data)
	}
	if env.Problem.Status != status || env.Problem.Type != slug {
		t.Fatalf("problem = %+v, want %d %s", env.Problem, status, slug)
	}
}
