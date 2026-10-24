package usecases_test

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/claudioed/vendor-connectivity/internal/application/ports"
	"github.com/claudioed/vendor-connectivity/internal/application/requests"
	"github.com/claudioed/vendor-connectivity/internal/application/usecases"
	"github.com/claudioed/vendor-connectivity/internal/domain/inbound"
	"github.com/claudioed/vendor-connectivity/internal/domain/network"
	"github.com/claudioed/vendor-connectivity/internal/domain/shared"
	"github.com/claudioed/vendor-connectivity/internal/domain/submission"
)

const (
	ssccA = "006141410000000012"
	ssccB = "006141410000000029"
	ssccP = "006141410000000036"
	gtinE = "00614141000036"
	gtinF = "00614141000050"
	gtinC = "10614141000019"
)

func shipmentLabelled(id, node string, cartons ...inbound.Carton) inbound.Labelled {
	return inbound.Labelled{ShipmentID: id, PlanID: "PLAN-1", SellerID: "S-1", NodeID: node, Cartons: cartons, LabelledAt: t0}
}

func homogeneous(sscc, parent, gtin string, qty int) inbound.Carton {
	return inbound.Carton{SSCC: sscc, ParentSSCC: parent, Kind: "HOMOGENEOUS_CASE", GrossWeightG: 1000,
		Contents: []inbound.Item{{CatalogItemID: "item-1", GTIN: gtin, QtyEaches: qty}}}
}

// shipped makes a SHIPPED shipment visible, through the consumers.
func (h *harness) shipped(id, node string, at time.Time, cartons ...inbound.Carton) {
	h.t.Helper()
	h.must(h.svc.OnInboundShipmentLabelled(h.ctx, h.eventID(), "InboundShipmentLabelled", shipmentLabelled(id, node, cartons...)))
	h.must(h.svc.OnInboundShipmentShipped(h.ctx, h.eventID(), "InboundShipmentShipped", inbound.Shipped{ShipmentID: id, SellerID: "S-1", NodeID: node, ShippedAt: at}))
}

func poll(h *harness, q requests.InboundPollQuery) (ids []string, next string) {
	h.t.Helper()
	page, err := h.svc.PollInboundShipments(h.ctx, q)
	h.must(err)
	for _, s := range page.InboundShipments {
		ids = append(ids, s.ShipmentID)
	}
	return ids, page.NextToken
}

func TestPollInboundShipmentsScopesToTheVendorsShippedShipments(t *testing.T) {
	h := newHarness(t, usecases.Config{})
	h.onboard("V-1")
	h.onboard("V-2")
	h.vendorNode("n-1", "V-1", 0)
	h.vendorNode("n-2", "V-1", 0)
	h.vendorNode("n-9", "V-2", 0)
	h.shipped("SHP-1", "n-1", t0.Add(time.Hour), homogeneous(ssccA, "", gtinE, 12))
	h.shipped("SHP-2", "n-2", t0.Add(2*time.Hour), homogeneous(ssccB, "", gtinE, 6))
	h.shipped("SHP-3", "n-9", t0.Add(3*time.Hour), homogeneous(ssccB, "", gtinE, 6))
	// Labelled only: not offered.
	h.must(h.svc.OnInboundShipmentLabelled(h.ctx, h.eventID(), "InboundShipmentLabelled", shipmentLabelled("SHP-4", "n-1", homogeneous(ssccA, "", gtinE, 1))))

	if ids, next := poll(h, requests.InboundPollQuery{VendorID: "V-1"}); fmt.Sprint(ids) != "[SHP-1 SHP-2]" || next != "" {
		t.Errorf("V-1 sees %v next %q", ids, next)
	}
	if ids, _ := poll(h, requests.InboundPollQuery{VendorID: "V-2"}); fmt.Sprint(ids) != "[SHP-3]" {
		t.Errorf("V-2 sees %v", ids)
	}
	if ids, _ := poll(h, requests.InboundPollQuery{VendorID: "V-1", NodeID: "n-2"}); fmt.Sprint(ids) != "[SHP-2]" {
		t.Errorf("node filter sees %v", ids)
	}
	after := t0.Add(time.Hour)
	if ids, _ := poll(h, requests.InboundPollQuery{VendorID: "V-1", CreatedAfter: &after}); fmt.Sprint(ids) != "[SHP-2]" {
		t.Errorf("createdAfter is strict and compares with shippedAt: %v", ids)
	}
	// A vendor without nodes gets an empty page with an array, not null.
	h.onboard("V-3")
	page, err := h.svc.PollInboundShipments(h.ctx, requests.InboundPollQuery{VendorID: "V-3"})
	if err != nil || page.InboundShipments == nil || len(page.InboundShipments) != 0 {
		t.Errorf("empty poll = %+v %v", page, err)
	}
}

func TestPollInboundShipmentsPagesWithNextToken(t *testing.T) {
	h := newHarness(t, usecases.Config{})
	h.onboard("V-1")
	h.vendorNode("n-1", "V-1", 0)
	for i := 1; i <= 5; i++ {
		// Two shipments share an instant: the id breaks the tie.
		at := t0.Add(time.Duration((i+1)/2) * time.Hour)
		h.shipped(fmt.Sprintf("SHP-%d", i), "n-1", at, homogeneous(ssccA, "", gtinE, i))
	}
	var seen []string
	token := ""
	pages := 0
	for {
		q := requests.InboundPollQuery{VendorID: "V-1", Limit: 2, NextToken: token}
		ids, next := poll(h, q)
		seen = append(seen, ids...)
		pages++
		if next == "" {
			break
		}
		token = next
	}
	if fmt.Sprint(seen) != "[SHP-1 SHP-2 SHP-3 SHP-4 SHP-5]" || pages != 3 {
		t.Errorf("paged %v in %d pages", seen, pages)
	}
	// Exactly one page of the page size has no continuation.
	if ids, next := poll(h, requests.InboundPollQuery{VendorID: "V-1", Limit: 5}); len(ids) != 5 || next != "" {
		t.Errorf("limit == rows: %v %q", ids, next)
	}
	// The default page size applies when no limit is asked.
	h2 := newHarness(t, usecases.Config{PageSize: 1})
	h2.onboard("V-1")
	h2.vendorNode("n-1", "V-1", 0)
	h2.shipped("SHP-1", "n-1", t0, homogeneous(ssccA, "", gtinE, 1))
	h2.shipped("SHP-2", "n-1", t0.Add(time.Hour), homogeneous(ssccA, "", gtinE, 1))
	if ids, next := poll(h2, requests.InboundPollQuery{VendorID: "V-1"}); len(ids) != 1 || next == "" {
		t.Errorf("default page size: %v %q", ids, next)
	}
}

func TestPollInboundShipmentsRefusals(t *testing.T) {
	h := newHarness(t, usecases.Config{})
	h.onboard("V-1")
	h.onboard("V-2")
	h.vendorNode("n-1", "V-1", 0)
	h.vendorNode("n-9", "V-2", 0)
	h.shipped("SHP-1", "n-1", t0, homogeneous(ssccA, "", gtinE, 1))
	_, token := poll(h, requests.InboundPollQuery{VendorID: "V-1", Limit: 1})
	h.shipped("SHP-2", "n-1", t0.Add(time.Hour), homogeneous(ssccA, "", gtinE, 1))
	_, token = poll(h, requests.InboundPollQuery{VendorID: "V-1", Limit: 1})
	if token == "" {
		t.Fatal("no token")
	}
	poTokenPage, _ := h.svc.PollPurchaseOrders(h.ctx, requests.PollQuery{VendorID: "V-1"})
	_ = poTokenPage
	cases := map[string]struct {
		q    requests.InboundPollQuery
		want error
	}{
		"no vendor":         {requests.InboundPollQuery{}, shared.ErrValidation},
		"unknown vendor":    {requests.InboundPollQuery{VendorID: "V-X"}, ports.ErrNotFound},
		"other vendor node": {requests.InboundPollQuery{VendorID: "V-1", NodeID: "n-9"}, network.ErrNodeNotOwnedByVendor},
		"unknown node":      {requests.InboundPollQuery{VendorID: "V-1", NodeID: "n-404"}, ports.ErrNotFound},
		"garbage token":     {requests.InboundPollQuery{VendorID: "V-1", NextToken: "%%%"}, shared.ErrValidation},
		"json token":        {requests.InboundPollQuery{VendorID: "V-1", NextToken: "e30"}, shared.ErrValidation},
		"foreign token":     {requests.InboundPollQuery{VendorID: "V-2", NextToken: token}, shared.ErrValidation},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := h.svc.PollInboundShipments(h.ctx, c.q); !errors.Is(err, c.want) {
				t.Fatalf("err = %v, want %v", err, c.want)
			}
		})
	}
}

func TestGetInboundShipmentBuildsThePalletAndCartonTree(t *testing.T) {
	h := newHarness(t, usecases.Config{})
	h.onboard("V-1")
	h.onboard("V-2")
	h.vendorNode("n-1", "V-1", 0)
	h.vendorNode("n-9", "V-2", 0)
	mixed := inbound.Carton{SSCC: ssccB, Kind: "MIXED", Contents: []inbound.Item{{GTIN: gtinE, QtyEaches: 2, Lot: "L1", Expiry: "2027-03-01"}, {GTIN: gtinF, QtyEaches: 3}},
		Dims: &inbound.Dims{L: 4, W: 5, H: 6}}
	h.shipped("SHP-1", "n-1", t0, homogeneous(ssccA, ssccP, gtinE, 12), mixed)
	got, err := h.svc.GetInboundShipment(h.ctx, "V-1", "SHP-1")
	h.must(err)
	if got.ShipmentID != "SHP-1" || got.SellerID != "S-1" || got.NodeID != "n-1" || got.Status != "SHIPPED" || !got.ShippedAt.Equal(t0) {
		t.Fatalf("header = %+v", got)
	}
	if len(got.Pallets) != 1 || got.Pallets[0].SSCC != ssccP || len(got.Pallets[0].Cartons) != 1 || got.Pallets[0].Cartons[0].SSCC != ssccA || got.Pallets[0].Cartons[0].ParentSSCC != ssccP {
		t.Fatalf("pallets = %+v", got.Pallets)
	}
	if len(got.Cartons) != 1 || got.Cartons[0].SSCC != ssccB || got.Cartons[0].DimsMm == nil || got.Cartons[0].DimsMm.H != 6 || len(got.Cartons[0].Contents) != 2 ||
		got.Cartons[0].Contents[0].Lot != "L1" || got.Cartons[0].Contents[0].Expiry != "2027-03-01" || got.Cartons[0].ParentSSCC != "" {
		t.Fatalf("cartons = %+v", got.Cartons)
	}
	// Another vendor, an unknown id and a shipment that has not shipped are all not found.
	if _, err := h.svc.GetInboundShipment(h.ctx, "V-2", "SHP-1"); !errors.Is(err, ports.ErrNotFound) {
		t.Errorf("other vendor: %v", err)
	}
	if _, err := h.svc.GetInboundShipment(h.ctx, "V-1", "SHP-404"); !errors.Is(err, ports.ErrNotFound) {
		t.Errorf("unknown: %v", err)
	}
	h.must(h.svc.OnInboundShipmentLabelled(h.ctx, h.eventID(), "InboundShipmentLabelled", shipmentLabelled("SHP-2", "n-1", homogeneous(ssccA, "", gtinE, 1))))
	if _, err := h.svc.GetInboundShipment(h.ctx, "V-1", "SHP-2"); !errors.Is(err, ports.ErrNotFound) {
		t.Errorf("not shipped: %v", err)
	}
	h.must(h.svc.OnInboundShipmentShipped(h.ctx, h.eventID(), "InboundShipmentShipped", inbound.Shipped{ShipmentID: "SHP-3", NodeID: "n-1", ShippedAt: t0}))
	if _, err := h.svc.GetInboundShipment(h.ctx, "V-1", "SHP-3"); !errors.Is(err, ports.ErrNotFound) {
		t.Errorf("shipped without a manifest: %v", err)
	}
	if _, err := h.svc.GetInboundShipment(h.ctx, "", "SHP-1"); !errors.Is(err, shared.ErrValidation) {
		t.Errorf("no vendor: %v", err)
	}
	if _, err := h.svc.GetInboundShipment(h.ctx, "V-X", "SHP-1"); !errors.Is(err, ports.ErrNotFound) {
		t.Errorf("unknown vendor: %v", err)
	}
	// Once received the poll drops it but a direct read still answers.
	h.must(h.svc.OnInboundShipmentReceived(h.ctx, h.eventID(), "InboundShipmentReceived", inbound.Received{ShipmentID: "SHP-1", NodeID: "n-1", Status: inbound.StatusClosed, ReceivedAt: t0.Add(time.Hour)}))
	if ids, _ := poll(h, requests.InboundPollQuery{VendorID: "V-1"}); len(ids) != 0 {
		t.Errorf("a received shipment is still polled: %v", ids)
	}
	if got, err := h.svc.GetInboundShipment(h.ctx, "V-1", "SHP-1"); err != nil || got.Status != "CLOSED" {
		t.Errorf("received read = %+v %v", got, err)
	}
}

func receiptBody(vendor string, cartons ...requests.InboundReceiptCarton) requests.InboundReceipt {
	return requests.InboundReceipt{VendorID: vendor, ShipmentID: "SHP-1", NodeID: "n-1", ReceivedAt: t0.Add(24 * time.Hour), Cartons: cartons}
}

func received(sscc, status string, items ...requests.InboundReceiptItem) requests.InboundReceiptCarton {
	return requests.InboundReceiptCarton{SSCC: sscc, Status: status, Items: items}
}

func receiptHarness(t *testing.T) *harness {
	t.Helper()
	h := newHarness(t, usecases.Config{})
	h.onboard("V-1")
	h.onboard("V-2")
	h.vendorNode("n-1", "V-1", 0)
	h.vendorNode("n-9", "V-2", 0)
	h.shipped("SHP-1", "n-1", t0, homogeneous(ssccA, "", gtinE, 12), homogeneous(ssccB, "", gtinF, 6))
	return h
}

func TestSubmitInboundReceiptAcceptsAndPublishes(t *testing.T) {
	h := receiptHarness(t)
	body := receiptBody("V-1",
		received(ssccA, "RECEIVED", requests.InboundReceiptItem{GTIN: "0614141000036", QtyEaches: 12}),
		requests.InboundReceiptCarton{SSCC: ssccB, Status: "DAMAGED", Items: []requests.InboundReceiptItem{{GTIN: gtinF, QtyEaches: 5}}, ComplianceCodes: []string{"CARTON_CRUSHED"}},
		received(ssccP, "UNEXPECTED"))
	acc, err := h.svc.SubmitInboundReceipt(h.ctx, body)
	h.must(err)
	if acc.TransactionID == "" {
		t.Fatal("no transactionId")
	}
	tx := h.tx("V-1", acc.TransactionID)
	if tx.Kind != "INBOUND_RECEIPT" || tx.Status != "PROCESSING" || tx.PONumber != "" {
		t.Fatalf("transaction = %+v", tx)
	}
	evs := h.store.Outbox().Events()
	if got := h.eventNames(); got[len(got)-1] != "InboundReceiptSubmitted" {
		t.Fatalf("events = %v", got)
	}
	ev, ok := evs[len(evs)-1].(inbound.ReceiptSubmitted)
	if !ok || ev.TransactionID != acc.TransactionID || ev.VendorID != "V-1" || ev.ShipmentID != "SHP-1" || ev.SubjectID() != "SHP-1" ||
		ev.Cartons[0].Items[0].GTIN != gtinE || !ev.OccurredAt().Equal(t0) {
		t.Fatalf("event = %+v", ev)
	}
	// The stored payload is the normalised receipt (GTIN in 14 digits).
	sub, err := h.store.Submissions().Get(h.ctx, submission.ID(acc.TransactionID))
	h.must(err)
	if sub.NodeID() != "n-1" || sub.Kind() != submission.KindInboundReceipt || !contains(string(sub.Payload()), `"gtin":"`+gtinE+`"`) {
		t.Errorf("submission = %s %s %s", sub.NodeID(), sub.Kind(), sub.Payload())
	}
	h.clock.Set(t0.Add(time.Minute))
	if n := h.processAll(); n != 1 {
		t.Fatalf("processed %d", n)
	}
	if tx := h.tx("V-1", acc.TransactionID); tx.Status != "SUCCESS" {
		t.Errorf("transaction after processing = %+v", tx)
	}
	if got := h.eventNames(); fmt.Sprint(got[len(got)-2:]) != "[InboundReceiptSubmitted SubmissionProcessed]" {
		t.Errorf("events = %v", got)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func TestSubmitInboundReceiptRefusals(t *testing.T) {
	ok := func() requests.InboundReceipt {
		return receiptBody("V-1", received(ssccA, "RECEIVED", requests.InboundReceiptItem{GTIN: gtinE, QtyEaches: 12}))
	}
	cases := map[string]struct {
		mutate func(*requests.InboundReceipt)
		want   error
	}{
		"no vendor":        {func(r *requests.InboundReceipt) { r.VendorID = "" }, shared.ErrValidation},
		"unknown vendor":   {func(r *requests.InboundReceipt) { r.VendorID = "V-X" }, ports.ErrNotFound},
		"shape":            {func(r *requests.InboundReceipt) { r.Cartons = nil }, shared.ErrValidation},
		"node of another":  {func(r *requests.InboundReceipt) { r.NodeID = "n-9" }, network.ErrNodeNotOwnedByVendor},
		"unknown node":     {func(r *requests.InboundReceipt) { r.NodeID = "n-404" }, ports.ErrNotFound},
		"unknown shipment": {func(r *requests.InboundReceipt) { r.ShipmentID = "SHP-404" }, ports.ErrNotFound},
		"carton off manifest": {func(r *requests.InboundReceipt) {
			r.Cartons = []requests.InboundReceiptCarton{received(ssccP, "RECEIVED")}
		}, shared.ErrValidation},
		"foreign gtin": {func(r *requests.InboundReceipt) {
			r.Cartons = []requests.InboundReceiptCarton{received(ssccA, "RECEIVED", requests.InboundReceiptItem{GTIN: gtinF, QtyEaches: 1})}
		}, shared.ErrValidation},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			h := receiptHarness(t)
			before := h.store.Counts()
			body := ok()
			c.mutate(&body)
			if _, err := h.svc.SubmitInboundReceipt(h.ctx, body); !errors.Is(err, c.want) {
				t.Fatalf("err = %v, want %v", err, c.want)
			}
			if after := h.store.Counts(); after != before {
				t.Errorf("a refused receipt stored something: %+v -> %+v", before, after)
			}
		})
	}
}

func TestSubmitInboundReceiptStateRules(t *testing.T) {
	h := receiptHarness(t)
	body := receiptBody("V-1", received(ssccA, "RECEIVED", requests.InboundReceiptItem{GTIN: gtinE, QtyEaches: 12}))
	// Not shipped yet: a conflict.
	h.must(h.svc.OnInboundShipmentLabelled(h.ctx, h.eventID(), "InboundShipmentLabelled", shipmentLabelled("SHP-7", "n-1", homogeneous(ssccA, "", gtinE, 1))))
	pending := body
	pending.ShipmentID = "SHP-7"
	if _, err := h.svc.SubmitInboundReceipt(h.ctx, pending); !errors.Is(err, shared.ErrIllegalTransition) {
		t.Errorf("unshipped: %v", err)
	}
	// Already closed: a conflict.
	h.must(h.svc.OnInboundShipmentReceived(h.ctx, h.eventID(), "InboundShipmentReceived", inbound.Received{ShipmentID: "SHP-1", NodeID: "n-1", Status: inbound.StatusClosed}))
	if _, err := h.svc.SubmitInboundReceipt(h.ctx, body); !errors.Is(err, shared.ErrIllegalTransition) {
		t.Errorf("closed: %v", err)
	}
	// A suspended vendor's writes are refused.
	h2 := receiptHarness(t)
	_, err := h2.svc.SuspendVendor(h2.ctx, "V-1", "review")
	h2.must(err)
	if _, err := h2.svc.SubmitInboundReceipt(h2.ctx, body); err == nil {
		t.Error("a suspended vendor submitted a receipt")
	}
}

func TestReceiptFailsWhenTheSimulatedFailureRateSaysSo(t *testing.T) {
	h := newHarness(t, usecases.Config{Knobs: usecases.Knobs{TxFailureRate: 1}})
	h.onboard("V-1")
	h.vendorNode("n-1", "V-1", 0)
	h.shipped("SHP-1", "n-1", t0, homogeneous(ssccA, "", gtinE, 12))
	acc, err := h.svc.SubmitInboundReceipt(h.ctx, receiptBody("V-1", received(ssccA, "RECEIVED")))
	h.must(err)
	h.processAll()
	if tx := h.tx("V-1", acc.TransactionID); tx.Status != "FAILURE" || tx.Errors[0].Code != usecases.CodeSimulatedFailure {
		t.Errorf("transaction = %+v", tx)
	}
}

// --- consumers ------------------------------------------------------------

func TestShipmentEventsAreIdempotentAndIgnoreInvalidFacts(t *testing.T) {
	h := newHarness(t, usecases.Config{})
	lab := shipmentLabelled("SHP-1", "n-1", homogeneous(ssccA, "", gtinE, 1))
	h.must(h.svc.OnInboundShipmentLabelled(h.ctx, "evt-same", "InboundShipmentLabelled", lab))
	h.must(h.svc.OnInboundShipmentShipped(h.ctx, "evt-ship", "InboundShipmentShipped", inbound.Shipped{ShipmentID: "SHP-1", NodeID: "n-1", ShippedAt: t0}))
	// The same CloudEvents id is skipped, even with different facts.
	other := shipmentLabelled("SHP-1", "n-1", homogeneous(ssccB, "", gtinF, 9))
	h.must(h.svc.OnInboundShipmentLabelled(h.ctx, "evt-same", "InboundShipmentLabelled", other))
	got, err := h.store.Shipments().Get(h.ctx, "SHP-1")
	h.must(err)
	if got.Cartons[0].SSCC != ssccA {
		t.Errorf("a redelivered event was applied twice: %+v", got.Cartons)
	}
	before := h.store.Counts()
	bad := shipmentLabelled("SHP-2", "n-1", inbound.Carton{SSCC: "123", Kind: "MIXED"})
	h.must(h.svc.OnInboundShipmentLabelled(h.ctx, h.eventID(), "InboundShipmentLabelled", bad))
	h.must(h.svc.OnInboundShipmentShipped(h.ctx, h.eventID(), "InboundShipmentShipped", inbound.Shipped{ShipmentID: "", NodeID: "n-1", ShippedAt: t0}))
	h.must(h.svc.OnInboundShipmentReceived(h.ctx, h.eventID(), "InboundShipmentReceived", inbound.Received{ShipmentID: "SHP-1", NodeID: "n-1", Status: "NOPE"}))
	if after := h.store.Counts(); after.Shipments != before.Shipments {
		t.Errorf("invalid facts stored a shipment: %d -> %d", before.Shipments, after.Shipments)
	}
	if after := h.store.Counts(); after.Inbox != before.Inbox+3 {
		t.Errorf("invalid events are still claimed (acknowledged): inbox %d -> %d", before.Inbox, after.Inbox)
	}
}

func declare(item string, level inbound.Level, gtin, child string, units int, at time.Time) inbound.Declared {
	return inbound.Declared{CatalogItemID: item, Level: level, GTIN: gtin, ChildGTIN: child, UnitsPerPack: units, DeclaredAt: at}
}

func TestPackagingEventsFeedThePurchaseOrderLines(t *testing.T) {
	h := newHarness(t, usecases.Config{})
	h.onboard("V-1")
	h.vendorNode("n-1", "V-1", 0)
	h.must(h.svc.OnListingCreated(h.ctx, h.eventID(), "ListingCreated", requests.ListingFacts{ListingID: "lst-1", NodeID: "n-1", CatalogItemID: "item-1"}))
	// lst-2 has no ListingView row, so its line has no packaging.
	po := h.issue("asg-1", "n-1")

	lines := func() [][]string {
		var out [][]string
		o := h.order(po)
		for _, l := range o.Lines {
			if l.Packaging == nil {
				t.Fatalf("line %d: packaging must be an array, never null", l.LineNo)
			}
			var row []string
			for _, p := range l.Packaging {
				row = append(row, fmt.Sprintf("%s/%s/%s/%d", p.Level, p.GTIN, p.ChildGTIN, p.UnitsPerPack))
			}
			out = append(out, row)
		}
		return out
	}
	if got := fmt.Sprint(lines()); got != "[[] []]" {
		t.Fatalf("before any declaration: %s", got)
	}
	h.must(h.svc.OnPackagingDeclared(h.ctx, h.eventID(), "CatalogItemPackagingDeclared", declare("item-1", inbound.LevelCase, gtinC, gtinE, 12, t0)))
	h.must(h.svc.OnPackagingDeclared(h.ctx, h.eventID(), "CatalogItemPackagingDeclared", declare("item-1", inbound.LevelEach, gtinE, "", 1, t0)))
	h.must(h.svc.OnPackagingDeclared(h.ctx, h.eventID(), "CatalogItemPackagingDeclared", declare("item-2", inbound.LevelEach, gtinF, "", 1, t0)))
	want := "[[EACH/" + gtinE + "//1 CASE/" + gtinC + "/" + gtinE + "/12] []]"
	if got := fmt.Sprint(lines()); got != want {
		t.Fatalf("after declarations: %s, want %s", got, want)
	}
	// The poll carries the same packaging.
	page, err := h.svc.PollPurchaseOrders(h.ctx, requests.PollQuery{VendorID: "V-1"})
	h.must(err)
	if len(page.PurchaseOrders[0].Lines[0].Packaging) != 2 {
		t.Errorf("poll packaging = %+v", page.PurchaseOrders[0].Lines[0].Packaging)
	}
	h.must(h.svc.OnPackLevelRetired(h.ctx, h.eventID(), "CatalogItemPackLevelRetired", inbound.Retired{CatalogItemID: "item-1", Level: inbound.LevelCase, GTIN: gtinC, RetiredAt: t0.Add(time.Hour)}))
	if got := fmt.Sprint(lines()); got != "[[EACH/"+gtinE+"//1] []]" {
		t.Errorf("after retirement: %s", got)
	}
	// Invalid packaging events are acknowledged and ignored.
	before := h.store.Counts().Packaging
	h.must(h.svc.OnPackagingDeclared(h.ctx, h.eventID(), "CatalogItemPackagingDeclared", declare("item-1", inbound.LevelCase, "1", "", 12, t0)))
	h.must(h.svc.OnPackagingDeclared(h.ctx, h.eventID(), "CatalogItemPackagingDeclared", declare("item-1", inbound.LevelCase, gtinC, "", 1, t0)))
	h.must(h.svc.OnPackLevelRetired(h.ctx, h.eventID(), "CatalogItemPackLevelRetired", inbound.Retired{CatalogItemID: "item-1", Level: inbound.LevelCase, GTIN: gtinC}))
	if h.store.Counts().Packaging != before {
		t.Error("invalid packaging events stored rows")
	}
	// A listing whose catalogue item id is empty has no packaging.
	h.must(h.svc.OnListingCreated(h.ctx, h.eventID(), "ListingCreated", requests.ListingFacts{ListingID: "lst-2", NodeID: "n-1"}))
	if got := fmt.Sprint(lines()); got != "[[EACH/"+gtinE+"//1] []]" {
		t.Errorf("empty catalogue item: %s", got)
	}
}
