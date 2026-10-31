//go:build integration

package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2aclient"

	inbounda2a "github.com/claudioed/vendor-connectivity/internal/adapters/inbound/a2a"
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

// The binary migrates a database itself (idempotently: pgtest's databases are
// already migrated, a second run must be a no-op), then answers both skills
// over the real JSON-RPC wire from what the write use cases persisted (the
// scorecard relayed from a fake node-performance), and its reads add no
// outbox row: the agent is read-only, so it leaves NO unpublished fact behind.
func TestA2AAgainstPostgres_ReadsWhatTheApiWroteAndNeverWrites(t *testing.T) {
	url := pgtest.URL(t) // a fresh, already migrated database
	ctx := context.Background()
	urls := fakeNeighbourURLs()

	pool := pgtest.Open(t, url)
	countOutbox := func() int { return pgtest.Count(t, pool, "outbox_events") }

	po := seedThroughWriteUseCases(t, url, urls)
	before := countOutbox()
	if before < 2 {
		t.Fatalf("outbox rows after seeding = %d, want the vendor/node/publish events", before)
	}

	deps, closeFn, err := buildDeps(ctx, quietLogger(), url, url, urls)
	if err != nil {
		t.Fatalf("buildDeps: %v", err)
	}
	t.Cleanup(closeFn)

	srv := httptest.NewServer(newRouter(
		inbounda2a.Handler(inbounda2a.NewExecutor(deps)),
		inbounda2a.Card(inbounda2a.Config{AgentURL: testAgentURL, Version: agentVersion}),
		quietLogger(), "vendor-connectivity-a2a"))
	defer srv.Close()
	client, err := a2aclient.NewFromCard(ctx, inbounda2a.Card(inbounda2a.Config{AgentURL: srv.URL + "/a2a", Version: "test"}))
	if err != nil {
		t.Fatalf("NewFromCard: %v", err)
	}

	assertSeededReads(t, client, po)

	if after := countOutbox(); after != before {
		t.Fatalf("outbox rows %d -> %d: the A2A binary wrote an event", before, after)
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
		Shipments: postgres.NewInboundShipmentViewRepo(pool), Packaging: postgres.NewCatalogPackagingViewRepo(pool),
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

// skillAnswer sends one skill call and returns the decoded envelope.
func skillAnswer(t *testing.T, client *a2aclient.Client, skill string, args map[string]any) map[string]any {
	t.Helper()
	msg := a2a.NewMessage(a2a.MessageRoleUser, a2a.NewDataPart(map[string]any{"skill": skill, "arguments": args}))
	resp, err := client.SendMessage(context.Background(), &a2a.SendMessageRequest{Message: msg})
	if err != nil {
		t.Fatalf("%s: SendMessage: %v", skill, err)
	}
	reply, ok := resp.(*a2a.Message)
	if !ok || len(reply.Parts) == 0 {
		t.Fatalf("%s: reply = %#v", skill, resp)
	}
	data, ok := reply.Parts[0].Content.(a2a.Data)
	if !ok {
		t.Fatalf("%s: first reply part is %T, want data", skill, reply.Parts[0].Content)
	}
	raw, _ := json.Marshal(data.Value)
	var env map[string]any
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("%s: %v (%s)", skill, err, raw)
	}
	return env
}

// assertSeededReads reads back, through both skills, what
// seedThroughWriteUseCases persisted, plus an in-band 404 problem.
func assertSeededReads(t *testing.T, client *a2aclient.Client, po string) {
	t.Helper()

	env := skillAnswer(t, client, "get_purchase_order", map[string]any{"vendorId": "V-1", "poNumber": po})
	order := dataField(t, env, "purchaseOrder")
	ack, _ := order["acknowledgement"].(map[string]any)
	if order["poNumber"] != po || order["state"] != "ACCEPTED" || order["nodeId"] != "n-1" || ack["decision"] != "ACCEPT" {
		t.Fatalf("get_purchase_order = %v", env)
	}

	env = skillAnswer(t, client, "get_node_scorecard", map[string]any{"vendorId": "V-1", "nodeId": "n-1"})
	sc := dataField(t, env, "scorecard")
	if ws, _ := sc["windows"].([]any); len(ws) != 1 || ws[0].(map[string]any)["grade"] != "A" {
		t.Fatalf("get_node_scorecard = %v", env)
	}

	env = skillAnswer(t, client, "get_purchase_order", map[string]any{"vendorId": "V-1", "poNumber": "PO-nope"})
	problem, _ := env["problem"].(map[string]any)
	if problem["type"] != "not-found" || problem["status"] != float64(http.StatusNotFound) {
		t.Fatalf("unknown order = %v, want an in-band 404 not-found problem", env)
	}
	// A NUL identifier is a 404 here too, before it can reach Postgres (a
	// 500 on this real runtime, hidden by the in-memory doubles).
	env = skillAnswer(t, client, "get_purchase_order", map[string]any{"vendorId": "V-1", "poNumber": po + "\u0000"})
	if problem, _ = env["problem"].(map[string]any); problem["status"] != float64(http.StatusNotFound) {
		t.Fatalf("NUL poNumber = %v, want 404", env)
	}
}

func dataField(t *testing.T, env map[string]any, field string) map[string]any {
	t.Helper()
	data, _ := env["data"].(map[string]any)
	got, _ := data[field].(map[string]any)
	if got == nil {
		t.Fatalf("answer has no data.%s: %v", field, env)
	}
	return got
}
