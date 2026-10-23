package usecases_test

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/claudioed/vendor-connectivity/internal/application/gateway"
	"github.com/claudioed/vendor-connectivity/internal/application/ports"
	"github.com/claudioed/vendor-connectivity/internal/application/requests"
	"github.com/claudioed/vendor-connectivity/internal/application/usecases"
	"github.com/claudioed/vendor-connectivity/internal/application/views"
	"github.com/claudioed/vendor-connectivity/internal/domain/network"
	"github.com/claudioed/vendor-connectivity/internal/domain/shared"
)

func validProfile() requests.NodeProfile {
	two := 100
	return requests.NodeProfile{
		VendorID: "V-1", ProfileRevision: 2, Timezone: "America/Sao_Paulo", HandlingTimeMinutes: 90,
		OperatingHoursByDay: map[string]requests.DayHours{"monday": {Start: "06:00", End: "22:00"}},
		Holidays:            []string{"2026-12-25"},
		Cutoffs:             []requests.Cutoff{{LocalTime: "18:00", Days: []string{"monday"}, MaxOrders: &two}},
		ThroughputCap:       requests.ThroughputCap{Value: 120, TimeUnit: "HOUR", Unit: "ORDER"},
	}
}

func validFeed() requests.InventoryFeed {
	return requests.InventoryFeed{VendorID: "V-1", NodeID: "n-1", FullUpdate: true, AsOf: t0,
		Lines: []requests.InventoryLine{{ListingID: "lst-1", AvailableQty: 5, Basis: "PHYSICAL"}, {ListingID: "lst-2", AvailableQty: 0, Basis: "UNKNOWN"}}}
}

func TestRegisterVendorNodeRelaysToNodeTopologyAndReturnsTheNodeId(t *testing.T) {
	h := newHarness(t, usecases.Config{})
	h.onboard("V-1")
	h.nt.Registered = gateway.RegisteredNode{NodeID: "n-77", NodeType: "FULFILLMENT_CENTER", Subtype: "SORTABLE", Status: "PROPOSED",
		VendorID: "V-1", VendorNodeCode: "SITE-9", RegionID: "r-1", Timezone: "America/Sao_Paulo"}
	acc, err := h.svc.RegisterVendorNode(h.ctx, registration())
	h.must(err)
	if tx := h.tx("V-1", acc.TransactionID); tx.Status != "PROCESSING" || tx.NodeID != "" || tx.Kind != "NODE_REGISTRATION" {
		t.Fatalf("received: %+v", tx)
	}
	h.processAll()
	tx := h.tx("V-1", acc.TransactionID)
	if tx.Status != "SUCCESS" || tx.NodeID != "n-77" {
		t.Fatalf("processed: %+v", tx)
	}
	assertRegisterCall(t, h, acc.TransactionID)
	assertRegisteredNodeUsable(t, h)
}

func assertRegisterCall(t *testing.T, h *harness, txID string) {
	t.Helper()
	got := []string{h.nt.Keys[0], h.nt.Registers[0].VendorNodeCode}
	if want := []string{txID, "SITE-9"}; !reflect.DeepEqual(got, want) || len(h.nt.Registers) != 1 {
		t.Fatalf("RegisterNode calls = %+v keys %v", h.nt.Registers, h.nt.Keys)
	}
	if names := h.eventNames(); names[len(names)-1] != "SubmissionProcessed" {
		t.Fatalf("events = %v", names)
	}
}

func assertRegisteredNodeUsable(t *testing.T, h *harness) {
	t.Helper()
	node, err := h.svc.GetNode(h.ctx, "V-1", "n-77")
	if err != nil || node.VendorNodeCode != "SITE-9" || node.Status != "PROPOSED" {
		t.Fatalf("the registered node is usable at once (A11): %+v %v", node, err)
	}
	list, err := h.svc.ListVendorNodes(h.ctx, "V-1")
	if err != nil || len(list.Nodes) != 1 || list.Nodes[0].NodeID != "n-77" {
		t.Fatalf("ListVendorNodes = %+v %v", list, err)
	}
}

func TestRegisterVendorNodeRefusals(t *testing.T) {
	h := newHarness(t, usecases.Config{})
	h.onboard("V-1")
	parcel := registration()
	parcel.NodeType = "SORTATION_CENTER"
	badZone := registration()
	badZone.Timezone = "Mars/Olympus"
	otherVendor := registration()
	otherVendor.VendorID = "V-9"
	for name, tc := range map[string]struct {
		in   requests.NodeRegistration
		want error
	}{
		"parcel node":    {parcel, shared.ErrValidation},
		"bad time zone":  {badZone, shared.ErrValidation},
		"unknown vendor": {otherVendor, ports.ErrNotFound},
	} {
		if _, err := h.svc.RegisterVendorNode(h.ctx, tc.in); !errors.Is(err, tc.want) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	if h.store.Counts().Submissions != 0 {
		t.Fatal("a refused write creates no transaction")
	}
}

func TestNeighbourRefusalFailsTheTransactionWithItsErrCode(t *testing.T) {
	h := newHarness(t, usecases.Config{})
	h.onboard("V-1")
	h.nt.Err = &gateway.Refusal{Status: 409, Code: "ErrVendorNodeCodeTaken", Message: "code taken", Path: "$.vendorNodeCode"}
	acc, err := h.svc.RegisterVendorNode(h.ctx, registration())
	h.must(err)
	h.processAll()
	tx := h.tx("V-1", acc.TransactionID)
	if tx.Status != "FAILURE" || len(tx.Errors) != 1 || tx.Errors[0].Code != "ErrVendorNodeCodeTaken" || tx.Errors[0].Path != "$.vendorNodeCode" || tx.NodeID != "" {
		t.Fatalf("transaction = %+v", tx)
	}
	if h.store.Counts().Nodes != 0 {
		t.Fatal("a refused registration adds no node")
	}
}

func TestOutageKeepsTheTransactionProcessingUntilTheAttemptCap(t *testing.T) {
	h := newHarness(t, usecases.Config{SubmissionMaxAttempts: 3})
	h.onboard("V-1")
	h.nt.Err = errors.New("connection refused")
	acc, err := h.svc.RegisterVendorNode(h.ctx, registration())
	h.must(err)
	for pass := 1; pass <= 2; pass++ {
		if n := h.processAll(); n != 0 {
			t.Fatalf("pass %d: an outage reaches no outcome: %d", pass, n)
		}
		if tx := h.tx("V-1", acc.TransactionID); tx.Status != "PROCESSING" {
			t.Fatalf("pass %d: %+v", pass, tx)
		}
	}
	if n := h.processAll(); n != 1 {
		t.Fatalf("the third attempt ends the transaction: %d", n)
	}
	tx := h.tx("V-1", acc.TransactionID)
	if tx.Status != "FAILURE" || tx.Errors[0].Code != usecases.CodeUpstreamUnavail {
		t.Fatalf("transaction = %+v", tx)
	}
	h.nt.Err = nil
	h.nt.Registered = gateway.RegisteredNode{NodeID: "n-1", VendorID: "V-1", VendorNodeCode: "c"}
	acc2, _ := h.svc.RegisterVendorNode(h.ctx, registration())
	h.processAll()
	if h.tx("V-1", acc2.TransactionID).Status != "SUCCESS" {
		t.Fatal("recovery")
	}
}

func TestDeclareNodeProfileRelaysTheDeclaration(t *testing.T) {
	h := newHarness(t, usecases.Config{})
	h.onboard("V-1")
	h.onboard("V-2")
	h.vendorNode("n-1", "V-1", 60)
	acc, err := h.svc.DeclareNodeProfile(h.ctx, "n-1", validProfile())
	h.must(err)
	h.processAll()
	if tx := h.tx("V-1", acc.TransactionID); tx.Status != "SUCCESS" || tx.Kind != "NODE_PROFILE" {
		t.Fatalf("transaction = %+v", tx)
	}
	if len(h.nt.Profiles) != 1 || h.nt.Profiles[0].ProfileRevision != 2 || h.nt.Keys[0] != acc.TransactionID {
		t.Fatalf("DeclareOperationalProfile calls = %+v", h.nt.Profiles)
	}
	bad := validProfile()
	bad.VendorID = "V-2"
	if _, err := h.svc.DeclareNodeProfile(h.ctx, "n-1", bad); !errors.Is(err, network.ErrNodeNotOwnedByVendor) {
		t.Fatalf("another vendor's node: %v", err)
	}
	if _, err := h.svc.DeclareNodeProfile(h.ctx, "n-404", validProfile()); !errors.Is(err, ports.ErrNotFound) {
		t.Fatalf("unknown node: %v", err)
	}
	invalid := validProfile()
	invalid.HandlingTimeMinutes = 0
	if _, err := h.svc.DeclareNodeProfile(h.ctx, "n-1", invalid); !errors.Is(err, shared.ErrValidation) {
		t.Fatalf("invalid profile: %v", err)
	}
}

func TestSubmitInventoryRelaysTheFeedWithTheTransactionIdAsFeedId(t *testing.T) {
	h := newHarness(t, usecases.Config{})
	h.onboard("V-1")
	h.vendorNode("n-1", "V-1", 60)
	acc, err := h.svc.SubmitInventory(h.ctx, validFeed())
	h.must(err)
	h.processAll()
	if tx := h.tx("V-1", acc.TransactionID); tx.Status != "SUCCESS" || tx.Kind != "INVENTORY" {
		t.Fatalf("transaction = %+v", tx)
	}
	if len(h.offers.Feeds) != 1 || h.offers.FeedIDs[0] != acc.TransactionID || !h.offers.Feeds[0].FullUpdate || len(h.offers.Feeds[0].Lines) != 2 {
		t.Fatalf("ApplyInventoryFeed calls = %+v", h.offers.Feeds)
	}
}

func TestInventoryRefusalsAndFeedAlreadyApplied(t *testing.T) {
	h := newHarness(t, usecases.Config{})
	h.onboard("V-1")
	h.vendorNode("n-1", "V-1", 60)
	h.must(h.svc.OnListingCreated(h.ctx, "evt-l1", "ListingCreated", requests.ListingFacts{ListingID: "lst-1", NodeID: "n-1"}))
	h.must(h.svc.OnListingCreated(h.ctx, "evt-l2", "ListingCreated", requests.ListingFacts{ListingID: "lst-2", NodeID: "n-other"}))
	if _, err := h.svc.SubmitInventory(h.ctx, validFeed()); !errors.Is(err, shared.ErrValidation) {
		t.Fatalf("a listing known on another node is refused (A12): %v", err)
	}
	feed := validFeed()
	feed.Lines = feed.Lines[:1]
	h.offers.Err = refusal("ErrFeedAlreadyApplied")
	acc, err := h.svc.SubmitInventory(h.ctx, feed)
	h.must(err)
	h.processAll()
	if tx := h.tx("V-1", acc.TransactionID); tx.Status != "SUCCESS" {
		t.Fatalf("an already applied feed is a success: %+v", tx)
	}
	h.offers.Err = refusal("ErrListingNotOnNode")
	acc, _ = h.svc.SubmitInventory(h.ctx, feed)
	h.processAll()
	if tx := h.tx("V-1", acc.TransactionID); tx.Status != "FAILURE" || tx.Errors[0].Code != "ErrListingNotOnNode" {
		t.Fatalf("a refused feed fails with the neighbour's code: %+v", tx)
	}
	other := validFeed()
	other.NodeID = "n-9"
	if _, err := h.svc.SubmitInventory(h.ctx, other); !errors.Is(err, ports.ErrNotFound) {
		t.Fatalf("unknown node: %v", err)
	}
	empty := validFeed()
	empty.AsOf = time.Time{}
	if _, err := h.svc.SubmitInventory(h.ctx, empty); !errors.Is(err, shared.ErrValidation) {
		t.Fatalf("invalid feed: %v", err)
	}
}

func TestProcessSubmissionWithMalformedPayloadFails(t *testing.T) {
	h := newHarness(t, usecases.Config{})
	h.onboard("V-1")
	h.must(h.store.Submissions().Save(h.ctx, mustReceive(t, "tx-bad", "NODE_REGISTRATION", `[1]`)))
	h.must(h.store.Submissions().Save(h.ctx, mustReceive(t, "tx-bad2", "NODE_PROFILE", `[1]`)))
	h.must(h.store.Submissions().Save(h.ctx, mustReceive(t, "tx-bad3", "INVENTORY", `[1]`)))
	h.processAll()
	for _, id := range []string{"tx-bad", "tx-bad2", "tx-bad3"} {
		if tx := h.tx("V-1", id); tx.Status != "FAILURE" || tx.Errors[0].Code != usecases.CodeMalformedPayload {
			t.Errorf("%s: %+v", id, tx)
		}
	}
}

func TestNodeEventsFeedTheView(t *testing.T) {
	h := newHarness(t, usecases.Config{})
	h.onboard("V-1")
	h.vendorNode("n-1", "V-1", 90)
	h.must(h.svc.OnNodeStatus(h.ctx, "evt-s1", "NodeActivated", requests.NodeStatusFacts{NodeID: "n-1", Status: "ACTIVE"}))
	h.must(h.svc.OnNodeProfileDeclared(h.ctx, "evt-p2", "NodeOperationalProfileDeclared", requests.NodeProfileFacts{NodeID: "n-1", ProfileRevision: 3, HandlingTimeMinutes: 45}))
	h.must(h.svc.OnNodeProfileDeclared(h.ctx, "evt-p3", "NodeOperationalProfileDeclared", requests.NodeProfileFacts{NodeID: "n-1", ProfileRevision: 2, HandlingTimeMinutes: 10}))
	n, err := h.svc.GetNode(h.ctx, "V-1", "n-1")
	want := "ACTIVE"
	if err != nil || n.Status != want || n.HandlingTimeMinutes != 45 || n.ProfileRevision != 3 || n.NodeType != "FULFILLMENT_CENTER" {
		t.Fatalf("node = %+v %v", n, err)
	}
	before := h.store.Counts()
	h.must(h.svc.OnNodeStatus(h.ctx, "evt-s1", "NodeActivated", requests.NodeStatusFacts{NodeID: "n-1", Status: "RETIRED"})) // duplicate id
	if h.store.Counts() != before {
		t.Fatal("a duplicate CloudEvents id is skipped")
	}
	h.must(h.svc.OnNodeStatus(h.ctx, "evt-s9", "NodeRetired", requests.NodeStatusFacts{NodeID: "n-1", Status: "RETIRED"}))
	if n, _ := h.svc.GetNode(h.ctx, "V-1", "n-1"); n.Status != "RETIRED" {
		t.Fatalf("retired: %+v", n)
	}
	for i, call := range []func() error{
		func() error {
			return h.svc.OnNodeStatus(h.ctx, "evt-bad1", "NodeSuspended", requests.NodeStatusFacts{NodeID: "n-1", Status: "NOPE"})
		},
		func() error {
			return h.svc.OnNodeRegistered(h.ctx, "evt-bad2", "NodeRegistered", requests.NodeRegisteredFacts{NodeID: "n-x", Operator: "VENDOR"})
		},
		func() error {
			return h.svc.OnNodeProfileDeclared(h.ctx, "evt-bad3", "NodeOperationalProfileDeclared", requests.NodeProfileFacts{NodeID: "n-1", ProfileRevision: 0})
		},
		func() error {
			return h.svc.OnListingCreated(h.ctx, "evt-bad4", "ListingCreated", requests.ListingFacts{ListingID: "", NodeID: "n-1"})
		},
	} {
		if err := call(); err != nil {
			t.Errorf("invalid facts are ignored, not retried (%d): %v", i, err)
		}
	}
	if _, err := h.store.Nodes().Get(h.ctx, "n-x"); !errors.Is(err, ports.ErrNotFound) {
		t.Fatal("an invalid NodeRegistered stores nothing")
	}
}

func TestStatusEventBeforeRegistrationCreatesAStub(t *testing.T) {
	h := newHarness(t, usecases.Config{})
	h.onboard("V-1")
	h.must(h.svc.OnNodeStatus(h.ctx, "evt-stub", "NodeActivated", requests.NodeStatusFacts{NodeID: "n-1", Status: "ACTIVE"}))
	if _, err := h.svc.GetNode(h.ctx, "V-1", "n-1"); !errors.Is(err, network.ErrNodeNotOwnedByVendor) {
		t.Fatalf("a stub belongs to nobody: %v", err)
	}
	h.vendorNode("n-1", "V-1", 0)
	if n, err := h.svc.GetNode(h.ctx, "V-1", "n-1"); err != nil || n.Status != "ACTIVE" {
		t.Fatalf("the registration fills the stub and keeps its status: %+v %v", n, err)
	}
}

func TestPollPurchaseOrdersPagesOldestFirst(t *testing.T) {
	h := newHarness(t, usecases.Config{PageSize: 2})
	h.onboard("V-1")
	h.vendorNode("n-1", "V-1", 60)
	var want []string
	for _, a := range []string{"a1", "a2", "a3", "a4", "a5"} {
		want = append(want, h.issue(a, "n-1"))
		h.clock.Advance(time.Minute)
	}
	var got []string
	token := ""
	for pages := 0; pages < 5; pages++ {
		page, err := h.svc.PollPurchaseOrders(h.ctx, requests.PollQuery{VendorID: "V-1", NextToken: token})
		h.must(err)
		for _, o := range page.PurchaseOrders {
			got = append(got, o.PONumber)
		}
		if token = page.NextToken; token == "" {
			break
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("polled %v, want %v", got, want)
	}
}

func TestPollFiltersAndRefusals(t *testing.T) {
	h := newHarness(t, usecases.Config{})
	h.onboard("V-1")
	h.onboard("V-2")
	h.vendorNode("n-1", "V-1", 60)
	h.vendorNode("n-2", "V-1", 60)
	h.vendorNode("n-3", "V-2", 60)
	h.issue("a1", "n-1")
	h.clock.Advance(time.Hour)
	h.issue("a2", "n-2")
	h.issue("a3", "n-3")
	page, _ := h.svc.PollPurchaseOrders(h.ctx, requests.PollQuery{VendorID: "V-1"})
	if len(page.PurchaseOrders) != 2 {
		t.Fatalf("only the vendor's orders: %d", len(page.PurchaseOrders))
	}
	byNode, _ := h.svc.PollPurchaseOrders(h.ctx, requests.PollQuery{VendorID: "V-1", NodeID: "n-2"})
	cut := t0
	after, _ := h.svc.PollPurchaseOrders(h.ctx, requests.PollQuery{VendorID: "V-1", CreatedAfter: &cut})
	if len(byNode.PurchaseOrders) != 1 || len(after.PurchaseOrders) != 1 {
		t.Fatalf("filters: node %d createdAfter %d", len(byNode.PurchaseOrders), len(after.PurchaseOrders))
	}
	small, _ := h.svc.PollPurchaseOrders(h.ctx, requests.PollQuery{VendorID: "V-1", Limit: 1})
	if len(small.PurchaseOrders) != 1 || small.NextToken == "" {
		t.Fatalf("limit 1: %+v", small)
	}
	for name, tc := range map[string]struct {
		q    requests.PollQuery
		want error
	}{
		"no vendor":       {requests.PollQuery{}, shared.ErrValidation},
		"unknown vendor":  {requests.PollQuery{VendorID: "V-9"}, ports.ErrNotFound},
		"foreign node":    {requests.PollQuery{VendorID: "V-1", NodeID: "n-3"}, network.ErrNodeNotOwnedByVendor},
		"unknown node":    {requests.PollQuery{VendorID: "V-1", NodeID: "n-9"}, ports.ErrNotFound},
		"garbage token":   {requests.PollQuery{VendorID: "V-1", NextToken: "%%%"}, shared.ErrValidation},
		"foreign token":   {requests.PollQuery{VendorID: "V-2", NextToken: small.NextToken}, shared.ErrValidation},
		"token no fields": {requests.PollQuery{VendorID: "V-1", NextToken: "e30"}, shared.ErrValidation},
	} {
		if _, err := h.svc.PollPurchaseOrders(h.ctx, tc.q); !errors.Is(err, tc.want) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	huge, err := h.svc.PollPurchaseOrders(h.ctx, requests.PollQuery{VendorID: "V-1", Limit: 100000})
	if err != nil || len(huge.PurchaseOrders) != 2 {
		t.Fatalf("limit is capped, not refused: %v", err)
	}
}

func TestPollRedeliversTheFirstOrderWithTheKnob(t *testing.T) {
	h := newHarness(t, usecases.Config{Knobs: usecases.Knobs{PORedeliveryRate: 0.3}})
	h.onboard("V-1")
	h.vendorNode("n-1", "V-1", 60)
	h.issue("a1", "n-1")
	h.issue("a2", "n-1")
	h.rand.Values = []float64{0.1}
	page, _ := h.svc.PollPurchaseOrders(h.ctx, requests.PollQuery{VendorID: "V-1"})
	if len(page.PurchaseOrders) != 3 || page.PurchaseOrders[0].PONumber != page.PurchaseOrders[2].PONumber {
		t.Fatalf("redelivery repeats the first order: %d", len(page.PurchaseOrders))
	}
	again, _ := h.svc.PollPurchaseOrders(h.ctx, requests.PollQuery{VendorID: "V-1"})
	if len(again.PurchaseOrders) != 2 {
		t.Fatalf("no redelivery above the rate: %d", len(again.PurchaseOrders))
	}
}

func TestReadsCheckOwnership(t *testing.T) {
	h, po := poFixture(t, usecases.Config{})
	h.onboard("V-2")
	if _, err := h.svc.GetPurchaseOrder(h.ctx, "V-2", po); !errors.Is(err, network.ErrNodeNotOwnedByVendor) {
		t.Fatalf("foreign order: %v", err)
	}
	if _, err := h.svc.GetPurchaseOrder(h.ctx, "", po); !errors.Is(err, shared.ErrValidation) {
		t.Fatalf("missing vendorId: %v", err)
	}
	if _, err := h.svc.GetPurchaseOrder(h.ctx, "V-1", "PO-x"); !errors.Is(err, ports.ErrNotFound) {
		t.Fatalf("unknown order: %v", err)
	}
	acc, _ := h.svc.SubmitAcknowledgement(h.ctx, ackAll("V-1", po, "ACCEPT", ""))
	if _, err := h.svc.GetTransaction(h.ctx, "V-2", acc.TransactionID); !errors.Is(err, ports.ErrNotFound) {
		t.Fatalf("foreign transaction: %v", err)
	}
	if _, err := h.svc.GetTransaction(h.ctx, "", acc.TransactionID); !errors.Is(err, shared.ErrValidation) {
		t.Fatalf("missing vendorId: %v", err)
	}
	if _, err := h.svc.GetNode(h.ctx, "V-2", "n-1"); !errors.Is(err, network.ErrNodeNotOwnedByVendor) {
		t.Fatalf("foreign node: %v", err)
	}
	if _, err := h.svc.GetNode(h.ctx, "", "n-1"); !errors.Is(err, shared.ErrValidation) {
		t.Fatalf("missing vendorId: %v", err)
	}
	if _, err := h.svc.ListVendorNodes(h.ctx, ""); !errors.Is(err, shared.ErrValidation) {
		t.Fatalf("missing vendorId: %v", err)
	}
	if l, err := h.svc.ListVendorNodes(h.ctx, "V-2"); err != nil || len(l.Nodes) != 0 {
		t.Fatalf("a vendor with no nodes: %+v %v", l, err)
	}
	if _, err := h.svc.ListVendorNodes(h.ctx, "V-9"); !errors.Is(err, ports.ErrNotFound) {
		t.Fatalf("unknown vendor: %v", err)
	}
}

func TestGetScorecardRelaysNodePerformance(t *testing.T) {
	h, _ := poFixture(t, usecases.Config{})
	rate := 0.93
	h.perf.Scorecard.Windows = []views.ScorecardWindow{{Window: "30d", Grade: "B", OnTimeHandoverRate: &rate, Observations: 120, ComputedAt: t0}}
	sc, err := h.svc.GetScorecard(h.ctx, "V-1", "n-1")
	if err != nil || sc.NodeID != "n-1" || len(sc.Windows) != 1 || sc.Windows[0].Grade != "B" {
		t.Fatalf("scorecard = %+v %v", sc, err)
	}
	h.perf.Err = &gateway.Refusal{Status: 404, Code: "ErrScorecardNotFound", Message: "none"}
	if _, err := h.svc.GetScorecard(h.ctx, "V-1", "n-1"); !errors.Is(err, ports.ErrNotFound) {
		t.Fatalf("no scorecard yet: %v", err)
	}
	h.perf.Err = errors.New("timeout")
	if _, err := h.svc.GetScorecard(h.ctx, "V-1", "n-1"); !errors.Is(err, ports.ErrUpstreamUnavailable) {
		t.Fatalf("outage: %v", err)
	}
	h.perf.Err = &gateway.Refusal{Status: 422, Code: "ErrX", Message: "m"}
	if _, err := h.svc.GetScorecard(h.ctx, "V-1", "n-1"); !errors.Is(err, ports.ErrUpstreamRefused) {
		t.Fatalf("refusal: %v", err)
	}
	h.onboard("V-2")
	if _, err := h.svc.GetScorecard(h.ctx, "V-2", "n-1"); !errors.Is(err, network.ErrNodeNotOwnedByVendor) {
		t.Fatalf("foreign node: %v", err)
	}
}
