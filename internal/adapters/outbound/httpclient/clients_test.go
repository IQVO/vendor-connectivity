package httpclient_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/claudioed/vendor-connectivity/internal/adapters/outbound/httpclient"
	"github.com/claudioed/vendor-connectivity/internal/application/gateway"
	"github.com/claudioed/vendor-connectivity/internal/application/ports"
	"github.com/claudioed/vendor-connectivity/internal/application/requests"
	"github.com/claudioed/vendor-connectivity/internal/application/views"
)

// seen is what the fake neighbour received.
type seen struct {
	method, path, key, contentType string
	body                           map[string]any
}

// neighbour answers every request with status and body, recording the last one.
func neighbour(t *testing.T, status int, body string) (*httptest.Server, *seen) {
	t.Helper()
	got := &seen{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		got.method, got.path, got.key, got.contentType = r.Method, r.URL.EscapedPath(), r.Header.Get("Idempotency-Key"), r.Header.Get("Content-Type")
		got.body = nil
		_ = json.Unmarshal(raw, &got.body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv, got
}

func registration() requests.NodeRegistration {
	lat := 10.5
	return requests.NodeRegistration{VendorID: "V-1", VendorNodeCode: "SITE-1", NodeType: "FULFILLMENT_CENTER", Subtype: "XL", RegionID: "r-1",
		Timezone: "America/Sao_Paulo", PostalCode: "07750-000", CountryCode: "BR", Lat: &lat}
}

func TestRegisterNodeSpeaksNodeTopologyOpenAPI(t *testing.T) {
	srv, got := neighbour(t, 201, `{"nodeId":"n-9","nodeType":"FULFILLMENT_CENTER","subtype":"XL","operator":"VENDOR","vendorRef":{"vendorId":"V-1","vendorNodeCode":"SITE-1"},"regionId":"r-1","timezone":"America/Sao_Paulo","status":"PROPOSED","extra":1}`)
	node, err := httpclient.NewNodeTopology(srv.URL+"/", nil).RegisterNode(context.Background(), "tx-1", registration())
	if err != nil {
		t.Fatal(err)
	}
	want := gateway.RegisteredNode{NodeID: "n-9", NodeType: "FULFILLMENT_CENTER", Subtype: "XL", Status: "PROPOSED", VendorID: "V-1", VendorNodeCode: "SITE-1", RegionID: "r-1", Timezone: "America/Sao_Paulo"}
	if node != want {
		t.Fatalf("node = %+v", node)
	}
	if got.method != "POST" || got.path != "/nodes" || got.key != "tx-1" || got.contentType != "application/json" {
		t.Fatalf("request = %+v", got)
	}
	vr, _ := got.body["vendorRef"].(map[string]any)
	loc, _ := got.body["location"].(map[string]any)
	if got.body["operator"] != "VENDOR" || vr["vendorId"] != "V-1" || vr["vendorNodeCode"] != "SITE-1" || loc["postalCode"] != "07750-000" || loc["lat"] != 10.5 || got.body["nodeType"] != "FULFILLMENT_CENTER" {
		t.Fatalf("body = %v", got.body)
	}
	if _, has := loc["lon"]; has {
		t.Fatal("an absent lon is omitted")
	}
}

func TestRegisterNodeWithoutAVendorRefOrNodeId(t *testing.T) {
	srv, _ := neighbour(t, 201, `{"nodeId":"n-9"}`)
	node, err := httpclient.NewNodeTopology(srv.URL, nil).RegisterNode(context.Background(), "k", registration())
	if err != nil || node.NodeID != "n-9" || node.VendorID != "" {
		t.Fatalf("node = %+v %v", node, err)
	}
	empty, _ := neighbour(t, 201, `{}`)
	if _, err := httpclient.NewNodeTopology(empty.URL, nil).RegisterNode(context.Background(), "k", registration()); !errors.Is(err, ports.ErrUpstreamUnavailable) {
		t.Fatalf("an answer without nodeId is not the contract: %v", err)
	}
}

func TestDeclareOperationalProfile(t *testing.T) {
	srv, got := neighbour(t, 200, `{}`)
	max := 5
	err := httpclient.NewNodeTopology(srv.URL, nil).DeclareOperationalProfile(context.Background(), "tx-2", "n 1", requests.NodeProfile{
		VendorID: "V-1", ProfileRevision: 3, Timezone: "UTC", HandlingTimeMinutes: 30,
		OperatingHoursByDay: map[string]requests.DayHours{"monday": {Start: "06:00", End: "22:00"}}, Holidays: []string{"2026-12-25"},
		ThroughputCap: requests.ThroughputCap{Value: 10, TimeUnit: "DAY", Unit: "ORDER"}, Cutoffs: []requests.Cutoff{{LocalTime: "18:00", MaxOrders: &max}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.method != "PUT" || got.path != "/nodes/n%201/operational-profile" || got.key != "tx-2" || got.body["declaredBy"] != "VENDOR" || got.body["profileRevision"] != float64(3) {
		t.Fatalf("request = %+v", got)
	}
	if _, has := got.body["vendorId"]; has {
		t.Fatal("the vendor id stays inside this context")
	}
	err = httpclient.NewNodeTopology(srv.URL, nil).DeclareOperationalProfile(context.Background(), "tx-3", "n-1", requests.NodeProfile{})
	if cut, ok := got.body["cutoffs"].([]any); err != nil || !ok || len(cut) != 0 {
		t.Fatalf("nil cutoffs are sent as an empty list: %v %v", got.body["cutoffs"], err)
	}
}

func TestApplyInventoryFeed(t *testing.T) {
	srv, got := neighbour(t, 201, `{"feedId":"tx-1","state":"APPLIED"}`)
	asOf := time.Date(2026, 10, 22, 13, 0, 0, 0, time.UTC)
	err := httpclient.NewOfferAvailability(srv.URL, nil).ApplyInventoryFeed(context.Background(), "tx-1", requests.InventoryFeed{VendorID: "V-1", NodeID: "n-1", FullUpdate: true, AsOf: asOf,
		Lines: []requests.InventoryLine{{ListingID: "lst-1", AvailableQty: 4, Basis: "PHYSICAL"}}})
	if err != nil {
		t.Fatal(err)
	}
	lines, _ := got.body["lines"].([]any)
	if got.method != "POST" || got.path != "/inventory-feeds" || got.key != "tx-1" || got.body["feedId"] != "tx-1" || got.body["nodeId"] != "n-1" ||
		got.body["fullUpdate"] != true || got.body["asOf"] != "2026-10-22T13:00:00Z" || len(lines) != 1 {
		t.Fatalf("request = %+v", got)
	}
	if err := httpclient.NewOfferAvailability(srv.URL, nil).ApplyInventoryFeed(context.Background(), "tx-2", requests.InventoryFeed{NodeID: "n-1", AsOf: asOf}); err != nil {
		t.Fatal(err)
	}
	if l, ok := got.body["lines"].([]any); !ok || len(l) != 0 {
		t.Fatalf("a feed with no lines sends an empty list: %v", got.body["lines"])
	}
}

func TestIssueLabel(t *testing.T) {
	srv, got := neighbour(t, 201, `{"labelRef":"lbl-1","trackingNumber":"TRK-1","carrier":"carrier-a","poNumber":"PO-1","packageNo":2,"status":"ISSUED"}`)
	label, err := httpclient.NewShippingTracking(srv.URL, nil).IssueLabel(context.Background(), "vc-label-PO-1-2",
		gateway.LabelCall{PONumber: "PO-1", PackageNo: 2, AssignmentID: "asg-1", OrderID: "ord-1", Tier: "NEXT_DAY"})
	if err != nil || label != (gateway.IssuedLabel{LabelRef: "lbl-1", TrackingNumber: "TRK-1", Carrier: "carrier-a"}) {
		t.Fatalf("label = %+v %v", label, err)
	}
	if got.path != "/labels" || got.key != "vc-label-PO-1-2" || got.body["orderId"] != "ord-1" || got.body["tier"] != "NEXT_DAY" || got.body["packageNo"] != float64(2) || got.body["assignmentId"] != "asg-1" {
		t.Fatalf("request = %+v", got)
	}
	bad, _ := neighbour(t, 201, `{"labelRef":"lbl-1"}`)
	if _, err := httpclient.NewShippingTracking(bad.URL, nil).IssueLabel(context.Background(), "k", gateway.LabelCall{}); !errors.Is(err, ports.ErrUpstreamUnavailable) {
		t.Fatalf("an incomplete label is not the contract: %v", err)
	}
}

func TestGetScorecardFlattensTheScorecardsIntoWindows(t *testing.T) {
	srv, got := neighbour(t, 200, `{"nodeId":"n-1","scorecards":[
		{"scorecardId":"s1","nodeId":"n-1","window":"7d","state":"GRADED","grade":"B","computedAt":"2026-10-22T12:00:00Z",
		 "metrics":{"onTimeHandoverRate":0.9,"lateAckRate":0.1,"rejectRate":0.05,"expiryRate":0.01,"promiseKeptRate":0.95,"feedFreshnessP95Minutes":12.5,"observations":40}},
		{"scorecardId":"s2","nodeId":"n-1","window":"30d","state":"PROVISIONAL","grade":"PROVISIONAL","metrics":{"observations":3}}]}`)
	sc, err := httpclient.NewNodePerformance(srv.URL, nil).GetScorecard(context.Background(), "n-1")
	if err != nil || got.method != "GET" || got.path != "/scorecards/n-1" || got.key != "" || got.contentType != "" {
		t.Fatalf("request = %+v %v", got, err)
	}
	if sc.NodeID != "n-1" || len(sc.Windows) != 2 {
		t.Fatalf("scorecard = %+v", sc)
	}
	assertGradedWindow(t, sc.Windows[0])
	if p := sc.Windows[1]; p.Grade != "PROVISIONAL" || p.OnTimeHandoverRate != nil || p.Observations != 3 {
		t.Fatalf("window 1 = %+v", p)
	}
}

func assertGradedWindow(t *testing.T, w views.ScorecardWindow) {
	t.Helper()
	if w.Window != "7d" || w.Grade != "B" || *w.OnTimeHandoverRate != 0.9 || *w.FeedFreshnessP95Minutes != 12.5 || w.Observations != 40 || w.ComputedAt.IsZero() {
		t.Fatalf("window 0 = %+v", w)
	}
}

func TestRefusalsCarryTheNeighboursErrName(t *testing.T) {
	srv, _ := neighbour(t, 409, `{"type":"stale-profile-revision","title":"Stale","status":409,"detail":"revision 2 is not newer than 3"}`)
	_, err := httpclient.NewNodeTopology(srv.URL, nil).RegisterNode(context.Background(), "k", registration())
	var ref *gateway.Refusal
	if !errors.As(err, &ref) || ref.Status != 409 || ref.Code != "ErrStaleProfileRevision" || ref.Message != "revision 2 is not newer than 3" {
		t.Fatalf("err = %v", err)
	}
	titleOnly, _ := neighbour(t, 422, `{"type":"https://x.example/problems/validation-failed","title":"Validation failed","status":422}`)
	err = httpclient.NewOfferAvailability(titleOnly.URL, nil).ApplyInventoryFeed(context.Background(), "k", requests.InventoryFeed{})
	if !errors.As(err, &ref) || ref.Code != "ErrValidationFailed" || ref.Message != "Validation failed" {
		t.Fatalf("err = %v", err)
	}
	html, _ := neighbour(t, 404, `<html>nope</html>`)
	_, err = httpclient.NewNodePerformance(html.URL, nil).GetScorecard(context.Background(), "n-1")
	if !errors.As(err, &ref) || ref.Status != 404 || ref.Code != "ErrRefused" {
		t.Fatalf("a 4xx without a problem body is still a refusal: %v", err)
	}
}

func TestOutagesAreUpstreamUnavailable(t *testing.T) {
	down, _ := neighbour(t, 503, `{}`)
	_, err := httpclient.NewShippingTracking(down.URL, nil).IssueLabel(context.Background(), "k", gateway.LabelCall{})
	if !errors.Is(err, ports.ErrUpstreamUnavailable) {
		t.Fatalf("5xx: %v", err)
	}
	gone := httptest.NewServer(http.NotFoundHandler())
	url := gone.URL
	gone.Close()
	if _, err := httpclient.NewNodePerformance(url, nil).GetScorecard(context.Background(), "n-1"); !errors.Is(err, ports.ErrUpstreamUnavailable) {
		t.Fatalf("connection refused: %v", err)
	}
	garbled, _ := neighbour(t, 200, `not json`)
	if _, err := httpclient.NewNodePerformance(garbled.URL, nil).GetScorecard(context.Background(), "n-1"); !errors.Is(err, ports.ErrUpstreamUnavailable) {
		t.Fatalf("a 2xx that is not the contract: %v", err)
	}
	slow := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { time.Sleep(300 * time.Millisecond) }))
	t.Cleanup(slow.Close)
	short := &http.Client{Timeout: 50 * time.Millisecond}
	if _, err := httpclient.NewNodePerformance(slow.URL, short).GetScorecard(context.Background(), "n-1"); !errors.Is(err, ports.ErrUpstreamUnavailable) {
		t.Fatalf("timeout: %v", err)
	}
}
