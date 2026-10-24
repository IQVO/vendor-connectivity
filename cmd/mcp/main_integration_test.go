//go:build integration

package main

import (
	"context"
	"os"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	inboundmcp "github.com/claudioed/vendor-connectivity/internal/adapters/inbound/mcp"
	"github.com/claudioed/vendor-connectivity/internal/adapters/outbound/clock"
	"github.com/claudioed/vendor-connectivity/internal/adapters/outbound/httpclient"
	"github.com/claudioed/vendor-connectivity/internal/adapters/outbound/ids"
	outboundkafka "github.com/claudioed/vendor-connectivity/internal/adapters/outbound/kafka"
	"github.com/claudioed/vendor-connectivity/internal/adapters/outbound/postgres"
	"github.com/claudioed/vendor-connectivity/internal/application/requests"
	"github.com/claudioed/vendor-connectivity/internal/application/usecases"
	"github.com/claudioed/vendor-connectivity/internal/domain/purchaseorder"
	"github.com/claudioed/vendor-connectivity/internal/testing/pgtest"
)

func TestMain(m *testing.M) {
	code := m.Run()
	pgtest.Shutdown()
	os.Exit(code)
}

// The binary migrates a database itself (idempotently: pgtest's databases
// are already migrated, a second run must be a no-op), then reads through
// every MCP tool exactly what the write use cases persisted, and its reads
// add no outbox row.
func TestMCPAgainstPostgres_ReadsWhatTheApiWroteAndNeverWrites(t *testing.T) {
	url := pgtest.URL(t) // a fresh, already migrated database
	ctx := context.Background()
	urls := fakeNeighbourURLs()

	pool := pgtest.Open(t, url)
	countOutbox := func() int { return pgtest.Count(t, pool, "outbox_events") }

	seedThroughWriteUseCases(t, url, urls)
	before := countOutbox()
	if before < 2 {
		t.Fatalf("outbox rows after seeding = %d, want the vendor/node/publish events", before)
	}

	deps, closeFn, err := buildDeps(ctx, quietLogger(), url, url, urls)
	if err != nil {
		t.Fatalf("buildDeps: %v", err)
	}
	t.Cleanup(closeFn)

	ct, st := sdk.NewInMemoryTransports()
	ss, err := inboundmcp.NewServer(deps).Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ss.Close()
	session, err := sdk.NewClient(&sdk.Implementation{Name: "t", Version: "0"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	assertSeededReads(t, session)

	if after := countOutbox(); after != before {
		t.Fatalf("outbox rows %d -> %d: the MCP binary wrote an event", before, after)
	}
}

// seedThroughWriteUseCases onboards V-1, registers n-1 through the consumer
// intake, issues one purchase order through the sourcing intake and
// acknowledges it, the way cmd/api does (same Service, Postgres adapters,
// transactional outbox). Returns the poNumber it seeded.
func seedThroughWriteUseCases(t *testing.T, url string, urls map[string]string) string {
	t.Helper()
	ctx := context.Background()
	pool := pgtest.Open(t, url)
	ob := postgres.NewOutboxRepo(pool)
	svc := usecases.New(usecases.Deps{
		UoW: postgres.NewUnitOfWork(pool), Vendors: postgres.NewVendorRepo(pool),
		Orders: postgres.NewPurchaseOrderRepo(pool), Submissions: postgres.NewSubmissionRepo(pool),
		Nodes: postgres.NewNodeViewRepo(pool), Listings: postgres.NewListingViewRepo(pool),
		Outbox: postgres.NewEventOutbox(ob, outboundkafka.NewEncoder()), Inbox: postgres.NewProcessedEventRepo(pool),
		Clock: clock.System{}, IDs: ids.UUID{}, Random: ids.Random{},
		NodeTopology: httpclient.NewNodeTopology(urls["NODE_TOPOLOGY_URL"], nil),
		Offers:       httpclient.NewOfferAvailability(urls["OFFER_AVAILABILITY_URL"], nil),
		Shipping:     httpclient.NewShippingTracking(urls["SHIPPING_AND_TRACKING_URL"], nil),
		Performance:  httpclient.NewNodePerformance(urls["NODE_PERFORMANCE_URL"], nil),
	}, usecases.Config{})
	if _, _, err := svc.OnboardVendor(ctx, requests.OnboardVendor{VendorID: "V-1", Name: "North Fulfilment", AckWindowMinutes: 240}); err != nil {
		t.Fatalf("onboard vendor: %v", err)
	}
	if err := svc.OnNodeRegistered(ctx, "ev-n-1", "NodeRegistered", requests.NodeRegisteredFacts{
		NodeID: "n-1", NodeType: "FULFILLMENT_CENTER", Subtype: "SORTABLE", Operator: "VENDOR",
		VendorID: "V-1", VendorNodeCode: "code-n-1", RegionID: "r-1", Timezone: "America/Sao_Paulo",
	}); err != nil {
		t.Fatalf("register node: %v", err)
	}
	if err := svc.IssueOnRelease(ctx, "rel-a-9f31", "FulfillmentReleasedToNode", requests.ReleasedToNode{
		AssignmentID: "a-9f31", ReleaseNo: 1, OrderID: "ord-a-9f31", GroupNo: 1, NodeID: "n-1",
		RequiredShipBy: time.Now().Add(8 * time.Hour), Tier: "NEXT_DAY",
		Lines: []requests.ReleasedLine{{ListingID: "lst-1", Quantity: 2}},
	}); err != nil {
		t.Fatalf("issue order: %v", err)
	}
	po := string(purchaseorder.NumberFor("a-9f31", 1))
	if _, err := svc.SubmitAcknowledgement(ctx, requests.Acknowledgement{
		VendorID: "V-1", PONumber: po, Decision: "ACCEPT", Lines: []requests.AckLine{{LineNo: 1, Quantity: 2}},
	}); err != nil {
		t.Fatalf("acknowledge: %v", err)
	}
	if _, err := svc.ProcessDueSubmissions(ctx); err != nil {
		t.Fatalf("process submissions: %v", err)
	}
	return po
}

// assertSeededReads reads back, through every MCP tool, what
// seedThroughWriteUseCases persisted.
func assertSeededReads(t *testing.T, session *sdk.ClientSession) {
	t.Helper()
	ctx := context.Background()
	call := func(name string, args map[string]any) map[string]any {
		t.Helper()
		res, err := session.CallTool(ctx, &sdk.CallToolParams{Name: name, Arguments: args})
		if err != nil || res.IsError {
			t.Fatalf("%s: err=%v res=%+v", name, err, res)
		}
		return res.StructuredContent.(map[string]any)
	}

	po := string(purchaseorder.NumberFor("a-9f31", 1))

	page := call("poll_purchase_orders", map[string]any{"vendorId": "V-1"})
	pos := page["purchaseOrders"].([]any)
	if len(pos) != 1 {
		t.Fatalf("poll_purchase_orders = %v, want the seeded order", page)
	}
	if head := pos[0].(map[string]any); head["poNumber"] != po || head["state"] != "ACCEPTED" || head["nodeId"] != "n-1" {
		t.Fatalf("polled order = %v", head)
	}

	one := call("get_purchase_order", map[string]any{"vendorId": "V-1", "poNumber": po})
	if one["state"] != "ACCEPTED" || one["acknowledgement"].(map[string]any)["decision"] != "ACCEPT" {
		t.Fatalf("get_purchase_order = %v", one)
	}

	nodes := call("list_vendor_nodes", map[string]any{"vendorId": "V-1"})
	if ns := nodes["nodes"].([]any); len(ns) != 1 || ns[0].(map[string]any)["nodeId"] != "n-1" {
		t.Fatalf("list_vendor_nodes = %v", nodes)
	}
	node := call("get_vendor_node", map[string]any{"vendorId": "V-1", "nodeId": "n-1"})
	if node["nodeType"] != "FULFILLMENT_CENTER" || node["timezone"] != "America/Sao_Paulo" {
		t.Fatalf("get_vendor_node = %v", node)
	}

	sc := call("get_node_scorecard", map[string]any{"vendorId": "V-1", "nodeId": "n-1"})
	if ws := sc["windows"].([]any); len(ws) != 1 || ws[0].(map[string]any)["grade"] != "A" {
		t.Fatalf("get_node_scorecard = %v", sc)
	}

	vendor := call("get_vendor", map[string]any{"vendorId": "V-1"})
	if vendor["name"] != "North Fulfilment" || vendor["state"] != "ACTIVE" || vendor["ackWindowMinutes"] != float64(240) {
		t.Fatalf("get_vendor = %v", vendor)
	}

	// The acknowledgement's transaction, read by the id its 202 returned:
	// seeded through the submission repo's tx id, discovered through the
	// purchase order view.
	txID := one["acknowledgement"].(map[string]any)["transactionId"].(string)
	tx := call("get_transaction", map[string]any{"vendorId": "V-1", "transactionId": txID})
	if tx["kind"] != "ACKNOWLEDGEMENT" || tx["status"] != "SUCCESS" || tx["poNumber"] != po {
		t.Fatalf("get_transaction = %v", tx)
	}
}
