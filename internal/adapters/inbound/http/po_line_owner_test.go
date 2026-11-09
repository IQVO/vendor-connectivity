package http_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"slices"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/claudioed/vendor-connectivity/internal/application/requests"
	"github.com/claudioed/vendor-connectivity/internal/domain/purchaseorder"
)

// ownerLinesGolden is the exact wire form of the lines of an order whose
// line 1 is operator-owned and line 2 a seller's stock (ADR 0012): ownerRef
// is omitted for the first and present for the second.
const ownerLinesGolden = `[{"lineNo":1,"listingId":"lst-1","quantity":2,"packaging":[]},` +
	`{"lineNo":2,"listingId":"lst-2","quantity":1,"ownerRef":"seller-7","packaging":[]}]`

const ownerRefDescription = "The owner of the goods to pick (the seller's ownerRef); omitted for operator-owned stock"

// issueWithOwner issues one order with an operator-owned and a seller-owned line.
func (a *api) issueWithOwner(assignment string) string {
	a.t.Helper()
	err := a.svc.IssueOnRelease(context.Background(), "rel-"+assignment, "FulfillmentReleasedToNode", requests.ReleasedToNode{AssignmentID: assignment, ReleaseNo: 1,
		OrderID: "ord-" + assignment, GroupNo: 1, NodeID: "n-1", RequiredShipBy: t0.Add(8 * time.Hour), Tier: "NEXT_DAY",
		Lines: []requests.ReleasedLine{{ListingID: "lst-1", Quantity: 2}, {ListingID: "lst-2", Quantity: 1, OwnerRef: "seller-7"}}})
	if err != nil {
		a.t.Fatal(err)
	}
	return string(purchaseorder.NumberFor(assignment, 1))
}

func compactLines(t *testing.T, raw json.RawMessage) string {
	t.Helper()
	var b bytes.Buffer
	if err := json.Compact(&b, raw); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

func TestGetPurchaseOrderLinesNameTheOwner(t *testing.T) {
	a := newAPI(t)
	po := a.issueWithOwner("asg-own")
	r := a.do("GET", "/vendor/purchase-orders/"+po+"?vendorId=V-1", "")
	var body struct{ Lines json.RawMessage }
	if err := json.Unmarshal([]byte(r.body), &body); r.code != 200 || err != nil {
		t.Fatalf("get = %d %s %v", r.code, r.body, err)
	}
	if got := compactLines(t, body.Lines); got != ownerLinesGolden {
		t.Fatalf("lines:\n got %s\nwant %s", got, ownerLinesGolden)
	}
	assertLinesMatchPoLineSchema(t, body.Lines)
}

func TestPollPurchaseOrdersLinesNameTheOwner(t *testing.T) {
	a := newAPI(t)
	a.issueWithOwner("asg-own")
	r := a.do("GET", "/vendor/purchase-orders?vendorId=V-1", "")
	var page struct {
		Items []struct{ Lines json.RawMessage }
	}
	if err := json.Unmarshal([]byte(r.body), &page); r.code != 200 || err != nil || len(page.Items) != 1 {
		t.Fatalf("poll = %d %s %v", r.code, r.body, err)
	}
	if got := compactLines(t, page.Items[0].Lines); got != ownerLinesGolden {
		t.Fatalf("lines:\n got %s\nwant %s", got, ownerLinesGolden)
	}
	assertLinesMatchPoLineSchema(t, page.Items[0].Lines)
}

// TestOperatorOwnedLinesOmitOwnerRef keeps the existing wire form byte for
// byte for an order without a named owner.
func TestOperatorOwnedLinesOmitOwnerRef(t *testing.T) {
	a := newAPI(t)
	po := a.issue("asg-op", "n-1")
	r := a.do("GET", "/vendor/purchase-orders/"+po+"?vendorId=V-1", "")
	var body struct{ Lines json.RawMessage }
	if err := json.Unmarshal([]byte(r.body), &body); r.code != 200 || err != nil {
		t.Fatalf("get = %d %s %v", r.code, r.body, err)
	}
	if got, want := compactLines(t, body.Lines), `[{"lineNo":1,"listingId":"lst-1","quantity":2,"packaging":[]}]`; got != want {
		t.Fatalf("lines:\n got %s\nwant %s", got, want)
	}
}

type poLineSchema struct {
	Required   []string
	Properties map[string]struct {
		Type        string
		Description string
	}
}

func loadPoLineSchema(t *testing.T) poLineSchema {
	t.Helper()
	raw, err := os.ReadFile("../../../../apis/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Paths      map[string]map[string]any
		Components struct{ Schemas map[string]poLineSchema }
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"/vendor/purchase-orders", "/vendor/purchase-orders/{poNumber}"} {
		if _, ok := doc.Paths[p]["get"]; !ok {
			t.Fatalf("apis/openapi.yaml has no GET %s", p)
		}
	}
	s, ok := doc.Components.Schemas["PoLine"]
	if !ok {
		t.Fatal("apis/openapi.yaml has no PoLine schema")
	}
	return s
}

// assertLinesMatchPoLineSchema checks every returned line against the
// OpenAPI PoLine: the required properties are present, none is undeclared,
// and ownerRef is an optional string with the contract's description.
func assertLinesMatchPoLineSchema(t *testing.T, raw json.RawMessage) {
	t.Helper()
	s := loadPoLineSchema(t)
	owner, ok := s.Properties["ownerRef"]
	if !ok || owner.Type != "string" || owner.Description != ownerRefDescription || slices.Contains(s.Required, "ownerRef") {
		t.Fatalf("PoLine.ownerRef = %+v (declared %v, required %v)", owner, ok, s.Required)
	}
	var lines []map[string]any
	if err := json.Unmarshal(raw, &lines); err != nil {
		t.Fatal(err)
	}
	for i, l := range lines {
		for _, req := range s.Required {
			if _, ok := l[req]; !ok {
				t.Errorf("lines[%d] lacks required %s", i, req)
			}
		}
		for k := range l {
			if _, ok := s.Properties[k]; !ok {
				t.Errorf("lines[%d] carries undeclared %s", i, k)
			}
		}
	}
}
