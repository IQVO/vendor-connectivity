package http_test

import (
	"context"
	"net/url"
	"testing"
	"time"

	"github.com/claudioed/vendor-connectivity/internal/domain/inbound"
)

const (
	ssccA = "006141410000000012"
	ssccB = "006141410000000029"
	ssccP = "006141410000000036"
	gtinE = "00614141000036"
)

// shipped makes SHP-<n> SHIPPED for the node through the consumers.
func (a *api) shipped(id, node string, at time.Time) {
	a.t.Helper()
	ctx := context.Background()
	err := a.svc.OnInboundShipmentLabelled(ctx, "lab-"+id, "InboundShipmentLabelled", inbound.Labelled{ShipmentID: id, PlanID: "P-1", SellerID: "S-1", NodeID: node, LabelledAt: t0,
		Cartons: []inbound.Carton{
			{SSCC: ssccA, ParentSSCC: ssccP, Kind: "HOMOGENEOUS_CASE", GrossWeightG: 900, Contents: []inbound.Item{{GTIN: gtinE, QtyEaches: 12}}},
			{SSCC: ssccB, Kind: "MIXED", Contents: []inbound.Item{{GTIN: gtinE, QtyEaches: 2}}},
		}})
	if err == nil {
		err = a.svc.OnInboundShipmentShipped(ctx, "shp-"+id, "InboundShipmentShipped", inbound.Shipped{ShipmentID: id, NodeID: node, ShippedAt: at})
	}
	if err != nil {
		a.t.Fatal(err)
	}
}

func TestPollAndGetInboundShipments(t *testing.T) {
	a := newAPI(t)
	a.shipped("SHP-1", "n-1", t0.Add(time.Hour))
	a.shipped("SHP-2", "n-1", t0.Add(2*time.Hour))

	r := a.do("GET", "/vendor/inbound-shipments?vendorId=V-1&limit=1", "")
	if r.code != 200 {
		t.Fatalf("poll = %d %s", r.code, r.body)
	}
	page := r.json(t)
	list := page["items"].([]any)
	if len(list) != 1 || page["nextCursor"] == nil {
		t.Fatalf("page = %s", r.body)
	}
	first := list[0].(map[string]any)
	if first["shipmentId"] != "SHP-1" || first["sellerId"] != "S-1" || first["status"] != "SHIPPED" || len(first["pallets"].([]any)) != 1 || len(first["cartons"].([]any)) != 1 {
		t.Fatalf("tree = %s", r.body)
	}
	next := a.do("GET", "/vendor/inbound-shipments?vendorId=V-1&cursor="+url.QueryEscape(page["nextCursor"].(string)), "")
	if got := next.json(t); next.code != 200 || len(got["items"].([]any)) != 1 || got["nextCursor"] != nil {
		t.Fatalf("second page = %d %s", next.code, next.body)
	}
}

func TestInboundPollFiltersAndGetByID(t *testing.T) {
	a := newAPI(t)
	a.shipped("SHP-1", "n-1", t0.Add(time.Hour))
	a.shipped("SHP-2", "n-1", t0.Add(2*time.Hour))
	if f := a.do("GET", "/vendor/inbound-shipments?vendorId=V-1&nodeId=n-1&createdAfter="+url.QueryEscape(t0.Add(time.Hour).Format(time.RFC3339)), ""); f.code != 200 ||
		len(f.json(t)["items"].([]any)) != 1 {
		t.Fatalf("filtered = %d %s", f.code, f.body)
	}
	one := a.do("GET", "/vendor/inbound-shipments/SHP-2?vendorId=V-1", "")
	if one.code != 200 || one.json(t)["shipmentId"] != "SHP-2" {
		t.Fatalf("get = %d %s", one.code, one.body)
	}
}

func TestInboundShipmentReadsRefuseWhatTheyCannotAnswer(t *testing.T) {
	a := newAPI(t)
	a.shipped("SHP-1", "n-1", t0)
	a.do("GET", "/vendor/inbound-shipments/SHP-404?vendorId=V-1", "").problem(t, 404, "not-found")
	a.do("GET", "/vendor/inbound-shipments/SHP-1", "").problem(t, 422, "validation-failed")
	a.do("GET", "/vendor/inbound-shipments?vendorId=V-1&createdAfter=yesterday", "").problem(t, 422, "validation-failed")
	a.do("GET", "/vendor/inbound-shipments?vendorId=V-1&limit=0", "").problem(t, 400, "invalid-limit")
	a.do("GET", "/vendor/inbound-shipments?vendorId=V-1&nodeId=n-404", "").problem(t, 404, "not-found")
}

func TestInboundReceiptIsAsynchronous(t *testing.T) {
	a := newAPI(t)
	a.shipped("SHP-1", "n-1", t0)
	body := `{"vendorId":"V-1","shipmentId":"SHP-1","nodeId":"n-1","receivedAt":"2026-10-25T09:30:00Z","cartons":[` +
		`{"sscc":"` + ssccA + `","status":"RECEIVED","items":[{"gtin":"` + gtinE + `","qtyEaches":12}]},` +
		`{"sscc":"` + ssccB + `","status":"MISSING"}]}`
	r := a.do("POST", "/vendor/inbound-receipts", body)
	if r.code != 202 || r.json(t)["transactionId"] == "" {
		t.Fatalf("receipt = %d %s", r.code, r.body)
	}
	id := r.json(t)["transactionId"].(string)
	tx := a.do("GET", "/vendor/transactions/"+id+"?vendorId=V-1", "")
	if got := tx.json(t); tx.code != 200 || got["kind"] != "INBOUND_RECEIPT" || got["status"] != "PROCESSING" {
		t.Fatalf("transaction = %d %s", tx.code, tx.body)
	}
	a.do("POST", "/vendor/inbound-receipts", `{"vendorId":"V-1","shipmentId":"SHP-404","nodeId":"n-1","receivedAt":"2026-10-25T09:30:00Z","cartons":[{"sscc":"`+ssccA+`","status":"MISSING"}]}`).
		problem(t, 404, "not-found")
	a.do("POST", "/vendor/inbound-receipts", `{"vendorId":"V-1","shipmentId":"SHP-1","nodeId":"n-1","receivedAt":"2026-10-25T09:30:00Z","cartons":[{"sscc":"`+ssccP+`","status":"RECEIVED"}]}`).
		problem(t, 422, "validation-failed")
	a.do("POST", "/vendor/inbound-receipts", `{"vendorId":"V-1","shipmentId":"SHP-1","extra":1}`).problem(t, 422, "validation-failed")
	a.do("POST", "/vendor/inbound-receipts", `not json`).problem(t, 422, "validation-failed")
}
