package mcp_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	inboundmcp "github.com/claudioed/vendor-connectivity/internal/adapters/inbound/mcp"
	"github.com/claudioed/vendor-connectivity/internal/adapters/outbound/memory"
	"github.com/claudioed/vendor-connectivity/internal/application/requests"
	"github.com/claudioed/vendor-connectivity/internal/application/usecases"
	"github.com/claudioed/vendor-connectivity/internal/domain/inbound"
	"github.com/claudioed/vendor-connectivity/internal/domain/purchaseorder"
)

// now is the fixture's fixed "now".
var now = time.Date(2026, 10, 24, 9, 0, 0, 0, time.UTC)

// harness is a connected in-memory MCP client over the real server wired to
// the in-memory doubles, exactly as cmd/mcp wires the Service (minus
// Postgres). The write use cases exist ONLY here, to seed data the way the
// REST adapter, the consumers and the background loops would: the MCP surface
// itself has none.
type harness struct {
	session *sdk.ClientSession
	svc     *usecases.Service
	store   *memory.Store
	clock   *memory.Clock
	perf    *memory.NodePerformance
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	s := memory.NewStore()
	clock := memory.NewClock(now)
	perf := &memory.NodePerformance{}
	svc := usecases.New(usecases.Deps{
		UoW: s.UnitOfWork(), Vendors: s.Vendors(), Orders: s.Orders(), Submissions: s.Submissions(),
		Nodes: s.Nodes(), Listings: s.Listings(), Shipments: s.Shipments(), Packaging: s.Packaging(),
		Outbox: s.Outbox(), Inbox: s.Inbox(),
		Clock: clock, IDs: &memory.SequentialIDs{}, Random: memory.NewScriptedRandom(),
		NodeTopology: &memory.NodeTopology{}, Offers: &memory.OfferAvailability{},
		Shipping: &memory.ShippingTracking{}, Performance: perf,
	}, usecases.Config{})
	h := &harness{svc: svc, store: s, clock: clock, perf: perf}
	h.session = connectSession(t, inboundmcp.Deps{Reads: svc})
	h.onboard(t, "V-1")
	h.node(t, "n-1", "V-1")
	return h
}

func connectSession(t *testing.T, deps inboundmcp.Deps) *sdk.ClientSession {
	t.Helper()
	ctx := context.Background()
	clientTransport, serverTransport := sdk.NewInMemoryTransports()
	serverSession, err := inboundmcp.NewServer(deps).Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	t.Cleanup(func() { _ = serverSession.Close() })
	client := sdk.NewClient(&sdk.Implementation{Name: "test-client", Version: "0"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// onboard creates a vendor the way POST /vendors would.
func (h *harness) onboard(t *testing.T, vendor string) {
	t.Helper()
	_, _, err := h.svc.OnboardVendor(context.Background(), requests.OnboardVendor{VendorID: vendor, Name: "North Fulfilment", AckWindowMinutes: 240})
	must(t, err)
}

// node registers a node the way the node-topology consumer would.
func (h *harness) node(t *testing.T, id, vendor string) {
	t.Helper()
	err := h.svc.OnNodeRegistered(context.Background(), "ev-"+id, "NodeRegistered", requests.NodeRegisteredFacts{
		NodeID: id, NodeType: "FULFILLMENT_CENTER", Subtype: "SORTABLE", Operator: "VENDOR", VendorID: vendor,
		VendorNodeCode: "code-" + id, RegionID: "r-1", Timezone: "America/Sao_Paulo",
	})
	must(t, err)
}

// issue creates an ISSUED purchase order for the node the way the
// order-sourcing consumer would, and returns its poNumber.
func (h *harness) issue(t *testing.T, assignment, node string) string {
	t.Helper()
	err := h.svc.IssueOnRelease(context.Background(), "rel-"+assignment, "FulfillmentReleasedToNode", requests.ReleasedToNode{
		AssignmentID: assignment, ReleaseNo: 1, OrderID: "ord-" + assignment, GroupNo: 1, NodeID: node,
		RequiredShipBy: now.Add(8 * time.Hour), Tier: "NEXT_DAY",
		Lines: []requests.ReleasedLine{{ListingID: "lst-1", Quantity: 2}},
	})
	must(t, err)
	return string(purchaseorder.NumberFor(assignment, 1))
}

// acknowledge submits the vendor's whole-order decision the way POST
// /vendor/acknowledgements would, then advances the clock past the
// processing delay and runs the submission loop, so the transaction reaches
// its outcome and (on ACCEPT) the order shows the acknowledgement.
func (h *harness) acknowledge(t *testing.T, vendor, po, decision string) string {
	t.Helper()
	accepted, err := h.svc.SubmitAcknowledgement(context.Background(), requests.Acknowledgement{
		VendorID: vendor, PONumber: po, Decision: decision, ReasonCode: ackReason(decision),
		Lines: []requests.AckLine{{LineNo: 1, Quantity: 2}},
	})
	must(t, err)
	h.clock.Set(now.Add(time.Minute))
	_, err = h.svc.ProcessDueSubmissions(context.Background())
	must(t, err)
	return accepted.TransactionID
}

// ackReason gives a REJECT its mandatory reason; an ACCEPT carries none.
func ackReason(decision string) string {
	if decision == "REJECT" {
		return "OUT_OF_STOCK"
	}
	return ""
}

// ship seeds one SHIPPED inbound shipment bound for the node the way the
// inbound-shipment consumers would (labelled, then shipped), and returns its
// id. The pallet is derived from the carton's parent SSCC; one carton sits
// on it, one is top-level.
func (h *harness) ship(t *testing.T, id, node string, shippedAt time.Time) string {
	t.Helper()
	// GS1 SSCCs are 18 digits; the pallet SSCC exists only as a parent.
	pallet, onPallet, loose := "006141410000000036", "006141410000000012", "006141410000000029"
	must(t, h.svc.OnInboundShipmentLabelled(context.Background(), "lab-"+id, "InboundShipmentLabelled", inbound.Labelled{
		ShipmentID: id, PlanID: "P-1", SellerID: "S-1", NodeID: node, LabelledAt: shippedAt,
		Cartons: []inbound.Carton{
			{SSCC: onPallet, ParentSSCC: pallet, Kind: "HOMOGENEOUS_CASE", GrossWeightG: 900,
				Contents: []inbound.Item{{GTIN: "00614141000036", QtyEaches: 12}}},
			{SSCC: loose, Kind: "MIXED", Contents: []inbound.Item{{GTIN: "00614141000043", QtyEaches: 2}}},
		},
	}))
	must(t, h.svc.OnInboundShipmentShipped(context.Background(), "shp-"+id, "InboundShipmentShipped", inbound.Shipped{
		ShipmentID: id, SellerID: "S-1", NodeID: node, ShippedAt: shippedAt,
	}))
	return id
}

// withShipment seeds V-1 with one SHIPPED shipment for n-1 and returns its id.
func withShipment(t *testing.T) (*harness, string) {
	t.Helper()
	h := newHarness(t)
	return h, h.ship(t, "ish-4b7e", "n-1", now)
}

// withOrder seeds V-1 with one ISSUED order on n-1 and returns its poNumber.
func withOrder(t *testing.T) (*harness, string) {
	t.Helper()
	h := newHarness(t)
	return h, h.issue(t, "a-9f31", "n-1")
}

// call invokes a tool and fails the test on a TRANSPORT/protocol error;
// tool-level failures come back as res.IsError.
func call(t *testing.T, session *sdk.ClientSession, name string, args map[string]any) *sdk.CallToolResult {
	t.Helper()
	res, err := session.CallTool(context.Background(), &sdk.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: transport/protocol error (want a tool result): %v", name, err)
	}
	return res
}

// ok calls a tool, requires success, and returns its structured content.
func (h *harness) ok(t *testing.T, name string, args map[string]any) map[string]any {
	t.Helper()
	res := call(t, h.session, name, args)
	if res.IsError {
		t.Fatalf("%s returned a tool error: %s", name, text(res))
	}
	sc, isMap := res.StructuredContent.(map[string]any)
	if !isMap {
		t.Fatalf("%s: structured content = %#v, want an object", name, res.StructuredContent)
	}
	return sc
}

// failWith calls a tool and requires an isError result whose text starts
// with the slug want.
func failWith(t *testing.T, session *sdk.ClientSession, name string, args map[string]any, want string) {
	t.Helper()
	res := call(t, session, name, args)
	if !res.IsError {
		t.Fatalf("%s: expected an isError tool result, got %#v", name, res.StructuredContent)
	}
	if got := text(res); !strings.HasPrefix(got, want+": ") {
		t.Fatalf("%s: error text %q does not start with %q", name, got, want+": ")
	}
}

func text(res *sdk.CallToolResult) string {
	var b strings.Builder
	for _, c := range res.Content {
		if tc, isText := c.(*sdk.TextContent); isText {
			b.WriteString(tc.Text)
		}
	}
	return b.String()
}

// strs reads one string field off every object under the array field key.
func strs(obj map[string]any, array, field string) []string {
	items, _ := obj[array].([]any)
	out := make([]string, 0, len(items))
	for _, item := range items {
		if m, isMap := item.(map[string]any); isMap {
			out = append(out, fmt.Sprint(m[field]))
		}
	}
	return out
}
