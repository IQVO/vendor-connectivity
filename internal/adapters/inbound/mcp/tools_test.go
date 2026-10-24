package mcp_test

import (
	"testing"
	"time"

	"github.com/claudioed/vendor-connectivity/internal/application/ports"
	"github.com/claudioed/vendor-connectivity/internal/application/requests"
	"github.com/claudioed/vendor-connectivity/internal/application/views"
)

func TestPollPurchaseOrders(t *testing.T) {
	h := newHarness(t)
	first := h.issue(t, "a-9f31", "n-1")
	// The second order is issued an hour later, so createdAfter can tell
	// them apart.
	h.clock.Set(now.Add(time.Hour))
	h.issue(t, "a-77c2", "n-1")

	got := h.ok(t, "poll_purchase_orders", map[string]any{"vendorId": "V-1"})
	pos := got["purchaseOrders"].([]any)
	if len(pos) != 2 {
		t.Fatalf("poll_purchase_orders = %d orders, want 2: %v", len(pos), got)
	}
	// Oldest first, the head carrying the read shape.
	head := pos[0].(map[string]any)
	for key, want := range map[string]any{
		"poNumber": first, "vendorId": "V-1", "nodeId": "n-1", "state": "ISSUED",
		"requiredShipBy": now.Add(8 * time.Hour).Format(time.RFC3339Nano),
	} {
		if head[key] != want {
			t.Errorf("polled order %q = %#v, want %#v", key, head[key], want)
		}
	}
	if issued, _ := head["issuedAt"].(string); issued == "" {
		t.Error("issuedAt is absent")
	}
	if ackBy, _ := head["ackBy"].(string); ackBy == "" {
		t.Error("ackBy is absent: the acknowledgement window is part of the read")
	}
	lines := head["lines"].([]any)
	if len(lines) != 1 || lines[0].(map[string]any)["listingId"] != "lst-1" {
		t.Errorf("lines = %#v, want the released line", head["lines"])
	}
	// Never a ship-to address, an orderId or a delivery promise.
	for _, absent := range []string{"orderId", "shipTo", "deliveryPromise", "address"} {
		if _, ok := head[absent]; ok {
			t.Errorf("%s must be absent from a polled order: %v", absent, head)
		}
	}
	// Two of two inside the default page: no nextToken.
	if token, _ := got["nextToken"].(string); token != "" {
		t.Errorf("a page holding every order still carries nextToken %q", token)
	}

	// The nodeId filter narrows to that node's orders.
	byNode := h.ok(t, "poll_purchase_orders", map[string]any{"vendorId": "V-1", "nodeId": "n-1"})
	if len(byNode["purchaseOrders"].([]any)) != 2 {
		t.Errorf("nodeId=n-1 = %v", byNode)
	}
}

// TestPollPurchaseOrdersPages covers limit/nextToken paging and the
// createdAfter filter; kept apart from the listing test so each stays under
// the cyclop budget the fleet applies to tests too.
func TestPollPurchaseOrdersPages(t *testing.T) {
	h := newHarness(t)
	first := h.issue(t, "a-9f31", "n-1")
	// The second order is issued an hour later, so createdAfter can tell
	// them apart.
	h.clock.Set(now.Add(time.Hour))
	h.issue(t, "a-77c2", "n-1")

	// limit pages with the opaque nextToken.
	all := h.ok(t, "poll_purchase_orders", map[string]any{"vendorId": "V-1"})
	head := all["purchaseOrders"].([]any)[0].(map[string]any)
	one := h.ok(t, "poll_purchase_orders", map[string]any{"vendorId": "V-1", "limit": 1})
	if len(one["purchaseOrders"].([]any)) != 1 || one["nextToken"] == nil || one["nextToken"] == "" {
		t.Fatalf("limit=1 page = %#v, want 1 order and a nextToken", one)
	}
	token, _ := one["nextToken"].(string)
	second := h.ok(t, "poll_purchase_orders", map[string]any{"vendorId": "V-1", "limit": 1, "nextToken": token})
	if len(second["purchaseOrders"].([]any)) != 1 || second["nextToken"] != nil {
		t.Fatalf("second page = %#v, want the last order and no nextToken", second)
	}
	if one["purchaseOrders"].([]any)[0].(map[string]any)["poNumber"] == second["purchaseOrders"].([]any)[0].(map[string]any)["poNumber"] {
		t.Fatal("the pages overlap")
	}

	// createdAfter keeps only orders issued strictly after the moment: with
	// the second order issued an hour later, the first order's issuedAt
	// keeps exactly the second.
	issuedAt := head["issuedAt"].(string)
	after := h.ok(t, "poll_purchase_orders", map[string]any{"vendorId": "V-1", "createdAfter": issuedAt})
	kept := strs(after, "purchaseOrders", "poNumber")
	if len(kept) != 1 || kept[0] == first {
		t.Fatalf("createdAfter=%s kept %v, want only the later order", issuedAt, kept)
	}
}

func TestPollPurchaseOrdersErrors(t *testing.T) {
	h, _ := withOrder(t)
	failWith(t, h.session, "poll_purchase_orders", map[string]any{"vendorId": "V-404"}, "not-found")
	failWith(t, h.session, "poll_purchase_orders", map[string]any{"vendorId": "a/b"}, "validation-failed")
	failWith(t, h.session, "poll_purchase_orders", map[string]any{"vendorId": "V-1", "createdAfter": "not-a-date"}, "validation-failed")
	failWith(t, h.session, "poll_purchase_orders", map[string]any{"vendorId": "V-1", "limit": -3}, "validation-failed")
	failWith(t, h.session, "poll_purchase_orders", map[string]any{"vendorId": "V-1", "nodeId": "n-other"}, "not-found")
	failWith(t, h.session, "poll_purchase_orders", map[string]any{"vendorId": "V-1", "nextToken": "!!bogus"}, "validation-failed")
}

func TestGetPurchaseOrder(t *testing.T) {
	h, po := withOrder(t)
	tx := h.acknowledge(t, "V-1", po, "ACCEPT")

	got := h.ok(t, "get_purchase_order", map[string]any{"vendorId": "V-1", "poNumber": po})
	if got["state"] != "ACCEPTED" {
		t.Fatalf("state = %v, want ACCEPTED once the acknowledgement's transaction succeeded", got["state"])
	}
	ack, isMap := got["acknowledgement"].(map[string]any)
	if !isMap || ack["decision"] != "ACCEPT" || ack["transactionId"] != tx {
		t.Fatalf("acknowledgement = %#v, want the ACCEPT decision and its transaction", got["acknowledgement"])
	}
	if _, ok := got["labels"]; !ok {
		t.Error("labels must be present (possibly empty)")
	}
	if _, ok := got["trackingNumbers"]; !ok {
		t.Error("trackingNumbers must be present (possibly empty)")
	}
}

func TestGetPurchaseOrderErrors(t *testing.T) {
	h, po := withOrder(t)
	failWith(t, h.session, "get_purchase_order", map[string]any{"vendorId": "V-1", "poNumber": "PO-nope"}, "not-found")
	failWith(t, h.session, "get_purchase_order", map[string]any{"vendorId": "a/b", "poNumber": po}, "validation-failed")
	h.onboard(t, "V-2")
	failWith(t, h.session, "get_purchase_order", map[string]any{"vendorId": "V-2", "poNumber": po}, "node-not-owned-by-vendor")
}

func TestListAndGetVendorNodes(t *testing.T) {
	h := newHarness(t)
	h.node(t, "n-2", "V-1")

	list := h.ok(t, "list_vendor_nodes", map[string]any{"vendorId": "V-1"})
	if got := strs(list, "nodes", "nodeId"); len(got) != 2 || got[0] != "n-1" || got[1] != "n-2" {
		t.Fatalf("list_vendor_nodes = %v, want n-1 and n-2 sorted by nodeId", got)
	}
	head := list["nodes"].([]any)[1].(map[string]any)
	for key, want := range map[string]any{
		"vendorId": "V-1", "vendorNodeCode": "code-n-2", "nodeType": "FULFILLMENT_CENTER",
		"subtype": "SORTABLE", "status": "PROPOSED", "regionId": "r-1", "timezone": "America/Sao_Paulo",
	} {
		if head[key] != want {
			t.Errorf("node %q = %#v, want %#v", key, head[key], want)
		}
	}

	one := h.ok(t, "get_vendor_node", map[string]any{"vendorId": "V-1", "nodeId": "n-2"})
	if one["nodeId"] != "n-2" || one["nodeType"] != "FULFILLMENT_CENTER" {
		t.Fatalf("get_vendor_node = %#v", one)
	}
}

func TestVendorNodeErrors(t *testing.T) {
	h := newHarness(t)
	failWith(t, h.session, "list_vendor_nodes", map[string]any{"vendorId": "V-404"}, "not-found")
	failWith(t, h.session, "list_vendor_nodes", map[string]any{"vendorId": "a b"}, "validation-failed")
	failWith(t, h.session, "get_vendor_node", map[string]any{"vendorId": "V-1", "nodeId": "n-404"}, "not-found")
	h.onboard(t, "V-2")
	failWith(t, h.session, "get_vendor_node", map[string]any{"vendorId": "V-2", "nodeId": "n-1"}, "node-not-owned-by-vendor")
}

func TestGetNodeScorecard(t *testing.T) {
	h := newHarness(t)
	onTime := 0.97
	h.perf.Scorecard = views.Scorecard{Windows: []views.ScorecardWindow{{
		Window: "7d", Grade: "A", OnTimeHandoverRate: &onTime, Observations: 120, ComputedAt: now,
	}}}

	got := h.ok(t, "get_node_scorecard", map[string]any{"vendorId": "V-1", "nodeId": "n-1"})
	if got["nodeId"] != "n-1" {
		t.Fatalf("get_node_scorecard = %#v", got)
	}
	windows, _ := got["windows"].([]any)
	if len(windows) != 1 {
		t.Fatalf("windows = %#v, want the relayed 7d window", got["windows"])
	}
	w := windows[0].(map[string]any)
	if w["window"] != "7d" || w["grade"] != "A" || w["onTimeHandoverRate"] != onTime || w["observations"] != float64(120) {
		t.Fatalf("7d window = %#v", w)
	}
}

func TestGetNodeScorecardErrors(t *testing.T) {
	h := newHarness(t)
	failWith(t, h.session, "get_node_scorecard", map[string]any{"vendorId": "V-1", "nodeId": "n-404"}, "not-found")
	h.onboard(t, "V-2")
	failWith(t, h.session, "get_node_scorecard", map[string]any{"vendorId": "V-2", "nodeId": "n-1"}, "node-not-owned-by-vendor")
	h.perf.Err = ports.ErrUpstreamUnavailable
	failWith(t, h.session, "get_node_scorecard", map[string]any{"vendorId": "V-1", "nodeId": "n-1"}, "upstream-unavailable")
}

func TestGetTransaction(t *testing.T) {
	h, po := withOrder(t)
	tx := h.acknowledge(t, "V-1", po, "REJECT")

	got := h.ok(t, "get_transaction", map[string]any{"vendorId": "V-1", "transactionId": tx})
	for key, want := range map[string]any{
		"transactionId": tx, "vendorId": "V-1", "kind": "ACKNOWLEDGEMENT", "status": "SUCCESS", "poNumber": po,
	} {
		if got[key] != want {
			t.Errorf("transaction %q = %#v, want %#v", key, got[key], want)
		}
	}
	if processed, _ := got["processedAt"].(string); processed == "" {
		t.Error("processedAt is absent on a finished transaction")
	}

	// A fresh 202 is PROCESSING until the submission loop runs.
	h2, po2 := withOrder(t)
	accepted, err := h2.svc.SubmitAcknowledgement(t.Context(), requests.Acknowledgement{
		VendorID: "V-1", PONumber: po2, Decision: "ACCEPT", Lines: []requests.AckLine{{LineNo: 1, Quantity: 2}},
	})
	if err != nil {
		t.Fatal(err)
	}
	processing := h2.ok(t, "get_transaction", map[string]any{"vendorId": "V-1", "transactionId": accepted.TransactionID})
	if processing["status"] != "PROCESSING" || processing["processedAt"] != nil {
		t.Fatalf("a fresh transaction = %#v, want PROCESSING and no processedAt", processing)
	}
}

func TestGetTransactionErrors(t *testing.T) {
	h, _ := withOrder(t)
	failWith(t, h.session, "get_transaction", map[string]any{"vendorId": "V-1", "transactionId": "tx-nope"}, "not-found")
	failWith(t, h.session, "get_transaction", map[string]any{"vendorId": "a/b", "transactionId": "tx-1"}, "validation-failed")
	tx := h.acknowledge(t, "V-1", h.issue(t, "a-5e21", "n-1"), "ACCEPT")
	h.onboard(t, "V-2")
	failWith(t, h.session, "get_transaction", map[string]any{"vendorId": "V-2", "transactionId": tx}, "not-found")
}

func TestGetVendor(t *testing.T) {
	h := newHarness(t)
	got := h.ok(t, "get_vendor", map[string]any{"vendorId": "V-1"})
	for key, want := range map[string]any{
		"vendorId": "V-1", "name": "North Fulfilment", "ackWindowMinutes": float64(240), "state": "ACTIVE",
	} {
		if got[key] != want {
			t.Errorf("vendor %q = %#v, want %#v", key, got[key], want)
		}
	}
}

func TestGetVendorErrors(t *testing.T) {
	h := newHarness(t)
	failWith(t, h.session, "get_vendor", map[string]any{"vendorId": "V-404"}, "not-found")
	// A malformed id is as absent as an unknown one: no vendor row matches.
	failWith(t, h.session, "get_vendor", map[string]any{"vendorId": "a/b"}, "not-found")
}

// Reads store and change nothing: the same store the service writes through
// is snapshotted before and after every tool of the surface.
func TestReadsEmitNothing(t *testing.T) {
	h, po := withOrder(t)
	tx := h.acknowledge(t, "V-1", po, "ACCEPT")
	before := h.store.Counts()
	h.ok(t, "poll_purchase_orders", map[string]any{"vendorId": "V-1"})
	h.ok(t, "get_purchase_order", map[string]any{"vendorId": "V-1", "poNumber": po})
	h.ok(t, "list_vendor_nodes", map[string]any{"vendorId": "V-1"})
	h.ok(t, "get_vendor_node", map[string]any{"vendorId": "V-1", "nodeId": "n-1"})
	h.ok(t, "get_node_scorecard", map[string]any{"vendorId": "V-1", "nodeId": "n-1"})
	h.ok(t, "get_transaction", map[string]any{"vendorId": "V-1", "transactionId": tx})
	h.ok(t, "get_vendor", map[string]any{"vendorId": "V-1"})
	if after := h.store.Counts(); after != before {
		t.Fatalf("store counts %v -> %v: a read tool wrote something", before, after)
	}
}
