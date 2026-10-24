package inbound

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/claudioed/vendor-connectivity/internal/domain/shared"
	"github.com/claudioed/vendor-connectivity/internal/domain/shared/gs1"
)

// Valid GS1 keys (check digits verified by gs1.Parse*): two cartons and a pallet,
// an each GTIN, a second each GTIN and a case GTIN.
const (
	ssccA   = "006141410000000012"
	ssccB   = "006141410000000029"
	ssccP   = "006141410000000036"
	gtinE   = "00614141000036"
	gtinE13 = "0614141000036"
	gtinF   = "00614141000050"
	gtinC   = "10614141000019"
	gtinVar = "90614141000015"
)

var t0 = time.Date(2026, 10, 24, 13, 0, 0, 0, time.UTC)

func wantInvalid(t *testing.T, err error) {
	t.Helper()
	if !errors.Is(err, shared.ErrValidation) {
		t.Fatalf("err = %v, want validation-failed", err)
	}
}

func sscc(t *testing.T, n int) string {
	t.Helper()
	s, err := gs1.NewSSCC(fmt.Sprintf("0%016d", n))
	if err != nil {
		t.Fatal(err)
	}
	return s.String()
}

func carton(s string, items ...Item) Carton {
	return Carton{SSCC: s, Kind: "HOMOGENEOUS_CASE", Contents: items, GrossWeightG: 1000}
}

func labelled(cartons ...Carton) Labelled {
	return Labelled{ShipmentID: "SHP-1", PlanID: "PLAN-1", SellerID: "S-1", NodeID: "n-1", Cartons: cartons, LabelledAt: t0}
}

func TestShipmentFollowsTheEventsForwardOnly(t *testing.T) {
	lab, err := Shipment{}.OnLabelled(labelled(carton(ssccA, Item{GTIN: gtinE13, QtyEaches: 12})))
	if err != nil {
		t.Fatal(err)
	}
	if lab.Status != StatusLabelled || lab.Pollable() || lab.Cartons[0].Contents[0].GTIN != gtinE {
		t.Fatalf("labelled = %+v (pollable %v)", lab, lab.Pollable())
	}
	shp, err := lab.OnShipped(Shipped{ShipmentID: "SHP-1", NodeID: "n-1", ShippedAt: t0.Add(time.Hour)})
	if err != nil || shp.Status != StatusShipped || !shp.Pollable() || !shp.ShippedAt.Equal(t0.Add(time.Hour)) {
		t.Fatalf("shipped = %+v %v", shp, err)
	}
	if shp.SellerID != "S-1" || shp.PlanID != "PLAN-1" {
		t.Errorf("identity lost: %+v", shp)
	}
	// A repeated or late label never moves the status back, and replaces the manifest.
	again, err := shp.OnLabelled(labelled(carton(ssccB, Item{GTIN: gtinF, QtyEaches: 1})))
	if err != nil || again.Status != StatusShipped || len(again.Cartons) != 1 || again.Cartons[0].SSCC != ssccB {
		t.Fatalf("relabel = %+v %v", again, err)
	}
	done, err := again.OnReceived(Received{ShipmentID: "SHP-1", NodeID: "n-1", Status: StatusClosedWithDiscrepancy, ReceivedAt: t0.Add(48 * time.Hour)})
	if err != nil || done.Status != StatusClosedWithDiscrepancy || done.Pollable() || done.ReceivedAt.IsZero() {
		t.Fatalf("received = %+v %v", done, err)
	}
	// The first close wins: a second Received with the other closed status does not change it.
	if again2, _ := done.OnReceived(Received{ShipmentID: "SHP-1", NodeID: "n-1", Status: StatusClosed}); again2.Status != StatusClosedWithDiscrepancy {
		t.Errorf("a repeated Received changed the status to %s", again2.Status)
	}
	// Nothing after the close reopens it.
	late, _ := done.OnShipped(Shipped{ShipmentID: "SHP-1", NodeID: "n-1", ShippedAt: t0})
	relabel, _ := done.OnLabelled(labelled(carton(ssccA, Item{GTIN: gtinE, QtyEaches: 1})))
	if late.Status != StatusClosedWithDiscrepancy || relabel.Status != StatusClosedWithDiscrepancy {
		t.Errorf("a late event reopened the shipment: %s %s", late.Status, relabel.Status)
	}
}

func TestShipmentEventsMayArriveOutOfOrder(t *testing.T) {
	shp, err := Shipment{}.OnShipped(Shipped{ShipmentID: "SHP-1", NodeID: "n-1", SellerID: "S-1", ShippedAt: t0})
	if err != nil || shp.Status != StatusShipped || shp.Pollable() {
		t.Fatalf("shipped first = %+v %v (no manifest yet, not pollable)", shp, err)
	}
	both, err := shp.OnLabelled(labelled(carton(ssccA, Item{GTIN: gtinE, QtyEaches: 1})))
	if err != nil || both.Status != StatusShipped || !both.Pollable() {
		t.Fatalf("label after ship = %+v %v", both, err)
	}
	closed, err := Shipment{}.OnReceived(Received{ShipmentID: "SHP-1", NodeID: "n-1", Status: StatusClosed})
	if err != nil || closed.Status != StatusClosed {
		t.Fatalf("received first = %+v %v", closed, err)
	}
	if shipped, _ := closed.OnShipped(Shipped{ShipmentID: "SHP-1", NodeID: "n-1", ShippedAt: t0}); shipped.Status != StatusClosed {
		t.Errorf("ship after close = %s", shipped.Status)
	}
}

func TestStatusRanksAreStrictlyIncreasing(t *testing.T) {
	order := []Status{"", StatusLabelled, StatusShipped, StatusClosed}
	for i := 1; i < len(order); i++ {
		if rank(order[i]) <= rank(order[i-1]) {
			t.Errorf("rank(%q)=%d is not above rank(%q)=%d", order[i], rank(order[i]), order[i-1], rank(order[i-1]))
		}
	}
	if rank(StatusClosed) != rank(StatusClosedWithDiscrepancy) || rank("BOGUS") != 0 {
		t.Error("both closed statuses share a rank and an unknown status ranks 0")
	}
}

func TestEventIdentityIsValidated(t *testing.T) {
	good := carton(ssccA, Item{GTIN: gtinE, QtyEaches: 1})
	cases := map[string]func() error{
		"label shipment id": func() error {
			_, e := Shipment{}.OnLabelled(Labelled{ShipmentID: " ", NodeID: "n-1", Cartons: []Carton{good}})
			return e
		},
		"label node id": func() error {
			_, e := Shipment{}.OnLabelled(Labelled{ShipmentID: "S", NodeID: "", Cartons: []Carton{good}})
			return e
		},
		"label seller id": func() error {
			_, e := Shipment{}.OnLabelled(Labelled{ShipmentID: "S", NodeID: "n", SellerID: "bad id", Cartons: []Carton{good}})
			return e
		},
		"label plan id": func() error {
			_, e := Shipment{}.OnLabelled(Labelled{ShipmentID: "S", NodeID: "n", PlanID: "bad id", Cartons: []Carton{good}})
			return e
		},
		"ship time": func() error { _, e := Shipment{}.OnShipped(Shipped{ShipmentID: "S", NodeID: "n"}); return e },
		"ship node": func() error {
			_, e := Shipment{}.OnShipped(Shipped{ShipmentID: "S", NodeID: "x y", ShippedAt: t0})
			return e
		},
		"receive status": func() error {
			_, e := Shipment{}.OnReceived(Received{ShipmentID: "S", NodeID: "n", Status: StatusShipped})
			return e
		},
		"receive shipment": func() error {
			_, e := Shipment{}.OnReceived(Received{ShipmentID: "", NodeID: "n", Status: StatusClosed})
			return e
		},
	}
	for name, fn := range cases {
		t.Run(name, func(t *testing.T) { wantInvalid(t, fn()) })
	}
	if _, err := (Shipment{}).OnLabelled(Labelled{ShipmentID: "S", NodeID: "n", Cartons: []Carton{good}}); err != nil {
		t.Errorf("an event without sellerId and planId is valid: %v", err)
	}
}

func manyItems(n int) []Item {
	out := make([]Item, n)
	for i := range out {
		out[i] = Item{GTIN: gtinE, QtyEaches: 1}
	}
	return out
}

func TestManifestValidationBoundaries(t *testing.T) {
	item := Item{GTIN: gtinE, QtyEaches: 1}
	mod := func(f func(c *Carton)) Carton {
		c := carton(ssccA, item)
		c.Contents = []Item{item}
		f(&c)
		return c
	}
	maxI := shared.MaxInt32
	ok := map[string]Carton{
		"min qty":        mod(func(c *Carton) { c.Contents[0].QtyEaches = 1 }),
		"max qty":        mod(func(c *Carton) { c.Contents[0].QtyEaches = maxI }),
		"zero weight":    mod(func(c *Carton) { c.GrossWeightG = 0 }),
		"max weight":     mod(func(c *Carton) { c.GrossWeightG = maxI }),
		"zero dims":      mod(func(c *Carton) { c.Dims = &Dims{} }),
		"max dims":       mod(func(c *Carton) { c.Dims = &Dims{L: maxI, W: maxI, H: maxI} }),
		"lot 20":         mod(func(c *Carton) { c.Contents[0].Lot = strings.Repeat("L", 20) }),
		"expiry":         mod(func(c *Carton) { c.Contents[0].Expiry = "2027-03-01" }),
		"parent":         mod(func(c *Carton) { c.ParentSSCC = ssccP }),
		"kind 32":        mod(func(c *Carton) { c.Kind = strings.Repeat("K", 32) }),
		"no contents":    mod(func(c *Carton) { c.Contents = nil }),
		"item lines max": mod(func(c *Carton) { c.Contents = manyItems(500) }),
	}
	for name, c := range ok {
		t.Run("ok "+name, func(t *testing.T) {
			if _, err := (Shipment{}).OnLabelled(labelled(c)); err != nil {
				t.Fatalf("err = %v", err)
			}
		})
	}
	bad := map[string]Carton{
		"qty 0":          mod(func(c *Carton) { c.Contents[0].QtyEaches = 0 }),
		"qty over":       mod(func(c *Carton) { c.Contents[0].QtyEaches = maxI + 1 }),
		"weight neg":     mod(func(c *Carton) { c.GrossWeightG = -1 }),
		"weight over":    mod(func(c *Carton) { c.GrossWeightG = maxI + 1 }),
		"dim l neg":      mod(func(c *Carton) { c.Dims = &Dims{L: -1} }),
		"dim w neg":      mod(func(c *Carton) { c.Dims = &Dims{W: -1} }),
		"dim h neg":      mod(func(c *Carton) { c.Dims = &Dims{H: -1} }),
		"dim l over":     mod(func(c *Carton) { c.Dims = &Dims{L: maxI + 1} }),
		"dim w over":     mod(func(c *Carton) { c.Dims = &Dims{W: maxI + 1} }),
		"dim h over":     mod(func(c *Carton) { c.Dims = &Dims{H: maxI + 1} }),
		"lot 21":         mod(func(c *Carton) { c.Contents[0].Lot = strings.Repeat("L", 21) }),
		"lot blank":      mod(func(c *Carton) { c.Contents[0].Lot = " " }),
		"expiry text":    mod(func(c *Carton) { c.Contents[0].Expiry = "tomorrow" }),
		"kind 33":        mod(func(c *Carton) { c.Kind = strings.Repeat("K", 33) }),
		"kind empty":     mod(func(c *Carton) { c.Kind = "" }),
		"bad sscc":       mod(func(c *Carton) { c.SSCC = "006141410000000013" }),
		"bad parent":     mod(func(c *Carton) { c.ParentSSCC = "006141410000000037" }),
		"bad gtin":       mod(func(c *Carton) { c.Contents[0].GTIN = "00614141000037" }),
		"variable gtin":  mod(func(c *Carton) { c.Contents[0].GTIN = gtinVar }),
		"item lines 501": mod(func(c *Carton) { c.Contents = manyItems(501) }),
	}
	for name, c := range bad {
		t.Run("bad "+name, func(t *testing.T) {
			_, err := (Shipment{}).OnLabelled(labelled(c))
			wantInvalid(t, err)
		})
	}
}

func TestManifestCartonCountBoundaryAndDuplicates(t *testing.T) {
	build := func(n int) []Carton {
		out := make([]Carton, n)
		for i := range out {
			out[i] = Carton{SSCC: sscc(t, i+1), Kind: "MIXED"}
		}
		return out
	}
	if _, err := (Shipment{}).OnLabelled(labelled(build(5000)...)); err != nil {
		t.Fatalf("5000 cartons: %v", err)
	}
	_, err := (Shipment{}).OnLabelled(labelled(build(5001)...))
	wantInvalid(t, err)
	_, err = (Shipment{}).OnLabelled(labelled(carton(ssccA), carton(ssccA)))
	wantInvalid(t, err)
}

func TestParseGTINNormalisesAndRefusesVariableMeasure(t *testing.T) {
	got, err := ParseGTIN(gtinE13)
	if err != nil || got != gtinE {
		t.Fatalf("ParseGTIN(13) = %q %v", got, err)
	}
	if got, err := ParseGTIN("06141417"); err != nil || got != "00000006141417" {
		t.Fatalf("ParseGTIN(8) = %q %v", got, err)
	}
	for _, bad := range []string{"", "0614141000037", gtinVar, "abc"} {
		if _, err := ParseGTIN(bad); err == nil {
			t.Errorf("ParseGTIN(%q) accepted", bad)
		}
	}
}

func TestCartonLookup(t *testing.T) {
	sh, _ := Shipment{}.OnLabelled(labelled(carton(ssccA, Item{GTIN: gtinE, QtyEaches: 1}), carton(ssccB)))
	if c, ok := sh.Carton(ssccB); !ok || c.SSCC != ssccB {
		t.Errorf("Carton(B) = %+v %v", c, ok)
	}
	if _, ok := sh.Carton(ssccP); ok {
		t.Error("Carton(P) found a carton that is not on the manifest")
	}
}

// --- receipts -------------------------------------------------------------

func shippedShipment(t *testing.T) Shipment {
	t.Helper()
	sh, err := Shipment{}.OnLabelled(labelled(
		carton(ssccA, Item{GTIN: gtinE, QtyEaches: 12}),
		carton(ssccB, Item{GTIN: gtinE, QtyEaches: 6}, Item{GTIN: gtinF, QtyEaches: 2}),
	))
	if err != nil {
		t.Fatal(err)
	}
	sh, err = sh.OnShipped(Shipped{ShipmentID: "SHP-1", NodeID: "n-1", ShippedAt: t0})
	if err != nil {
		t.Fatal(err)
	}
	return sh
}

func receipt(cartons ...ReceiptCarton) Receipt {
	return Receipt{ShipmentID: "SHP-1", NodeID: "n-1", ReceivedAt: t0.Add(time.Hour), Cartons: cartons}
}

func rc(s string, st CartonStatus, items ...ReceiptItem) ReceiptCarton {
	return ReceiptCarton{SSCC: s, Status: st, Items: items}
}

func TestReceiptNormaliseAcceptsAndNormalises(t *testing.T) {
	r, err := receipt(rc(ssccA, CartonReceived, ReceiptItem{GTIN: gtinE13, QtyEaches: 0}), rc(ssccB, CartonMissing)).Normalise()
	if err != nil {
		t.Fatal(err)
	}
	if r.Cartons[0].Items[0].GTIN != gtinE || r.Cartons[0].Items[0].QtyEaches != 0 {
		t.Errorf("item = %+v (gtin 14 digits, an explicit 0 count is valid)", r.Cartons[0].Items[0])
	}
	if r.Cartons[0].ComplianceCodes == nil || r.Cartons[1].Items == nil {
		t.Error("arrays are always present, never nil")
	}
	r2, _ := Receipt{ShipmentID: "S", NodeID: "n", ReceivedAt: t0.In(time.FixedZone("x", 3600)), Cartons: []ReceiptCarton{rc(ssccA, CartonReceived)}}.Normalise()
	if r2.ReceivedAt.Location() != time.UTC {
		t.Error("receivedAt is kept in UTC")
	}
}

// sku returns the i-th valid 14-digit GTIN (distinct per i).
func sku(t *testing.T, i int) string {
	t.Helper()
	g, err := gs1.NewGTIN(fmt.Sprintf("%013d", 1000+i))
	if err != nil {
		t.Fatal(err)
	}
	return g.String()
}

func TestReceiptNormaliseRefusals(t *testing.T) {
	ok := rc(ssccA, CartonReceived, ReceiptItem{GTIN: gtinE, QtyEaches: 1})
	withCodes := func(codes ...string) ReceiptCarton { c := ok; c.ComplianceCodes = codes; return c }
	many := func(n int) []string {
		out := make([]string, n)
		for i := range out {
			out[i] = "CODE"
		}
		return out
	}
	items := func(n int) []ReceiptItem {
		out := make([]ReceiptItem, n)
		for i := range out {
			out[i] = ReceiptItem{GTIN: sku(t, i), QtyEaches: 1}
		}
		return out
	}
	cases := map[string]Receipt{
		"shipment id":     {ShipmentID: "", NodeID: "n", ReceivedAt: t0, Cartons: []ReceiptCarton{ok}},
		"node id":         {ShipmentID: "S", NodeID: "", ReceivedAt: t0, Cartons: []ReceiptCarton{ok}},
		"receivedAt":      {ShipmentID: "S", NodeID: "n", Cartons: []ReceiptCarton{ok}},
		"no cartons":      {ShipmentID: "S", NodeID: "n", ReceivedAt: t0},
		"duplicate sscc":  receipt(ok, ok),
		"bad sscc":        receipt(rc("006141410000000013", CartonReceived)),
		"bad status":      receipt(rc(ssccA, "LOST")),
		"missing w items": receipt(rc(ssccA, CartonMissing, ReceiptItem{GTIN: gtinE, QtyEaches: 1})),
		"bad gtin":        receipt(rc(ssccA, CartonReceived, ReceiptItem{GTIN: "1", QtyEaches: 1})),
		"qty negative":    receipt(rc(ssccA, CartonReceived, ReceiptItem{GTIN: gtinE, QtyEaches: -1})),
		"qty over":        receipt(rc(ssccA, CartonReceived, ReceiptItem{GTIN: gtinE, QtyEaches: shared.MaxInt32 + 1})),
		"duplicate gtin":  receipt(rc(ssccA, CartonReceived, ReceiptItem{GTIN: gtinE, QtyEaches: 1}, ReceiptItem{GTIN: gtinE13, QtyEaches: 1})),
		"code lower":      receipt(withCodes("crushed")),
		"code digit":      receipt(withCodes("1CRUSHED")),
		"code 65":         receipt(withCodes("C" + strings.Repeat("A", 64))),
		"codes 21":        receipt(withCodes(many(21)...)),
		"items 501":       receipt(rc(ssccA, CartonReceived, items(501)...)),
	}
	for name, r := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := r.Normalise()
			wantInvalid(t, err)
		})
	}
	good := map[string]Receipt{
		"qty max":   receipt(rc(ssccA, CartonReceived, ReceiptItem{GTIN: gtinE, QtyEaches: shared.MaxInt32})),
		"code 64":   receipt(withCodes("C" + strings.Repeat("A", 63))),
		"codes 20":  receipt(withCodes(many(20)...)),
		"items 500": receipt(rc(ssccA, CartonReceived, items(500)...)),
		"missing":   receipt(rc(ssccA, CartonMissing)),
		"code mix":  receipt(withCodes("LABEL_UNREADABLE_2")),
	}
	for name, r := range good {
		t.Run("ok "+name, func(t *testing.T) {
			if _, err := r.Normalise(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestReceiptCartonCountBoundary(t *testing.T) {
	build := func(n int) Receipt {
		cs := make([]ReceiptCarton, n)
		for i := range cs {
			cs[i] = rc(sscc(t, i+1), CartonUnexpected)
		}
		return receipt(cs...)
	}
	if _, err := build(5000).Normalise(); err != nil {
		t.Fatalf("5000 cartons: %v", err)
	}
	_, err := build(5001).Normalise()
	wantInvalid(t, err)
}

func TestCheckReceipt(t *testing.T) {
	sh := shippedShipment(t)
	norm := func(r Receipt) Receipt {
		t.Helper()
		n, err := r.Normalise()
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	good := norm(receipt(
		rc(ssccA, CartonReceived, ReceiptItem{GTIN: gtinE, QtyEaches: 99}), // over-count is a discrepancy, not a refusal
		rc(ssccB, CartonDamaged, ReceiptItem{GTIN: gtinF, QtyEaches: 0}),
		rc(ssccP, CartonUnexpected, ReceiptItem{GTIN: gtinC, QtyEaches: 1}), // not on the manifest: allowed when UNEXPECTED
	))
	if err := sh.CheckReceipt(good); err != nil {
		t.Fatalf("CheckReceipt = %v", err)
	}
	invalid := map[string]Receipt{
		"other node":      {ShipmentID: "SHP-1", NodeID: "n-2", ReceivedAt: t0, Cartons: good.Cartons},
		"unknown carton":  norm(receipt(rc(ssccP, CartonReceived))),
		"missing unknown": norm(receipt(rc(ssccP, CartonMissing))),
		"foreign gtin":    norm(receipt(rc(ssccA, CartonReceived, ReceiptItem{GTIN: gtinF, QtyEaches: 1}))),
	}
	for name, r := range invalid {
		t.Run(name, func(t *testing.T) { wantInvalid(t, sh.CheckReceipt(r)) })
	}
	for name, mutate := range map[string]func(Shipment) Shipment{
		"labelled only": func(s Shipment) Shipment { s.Status = StatusLabelled; return s },
		"closed":        func(s Shipment) Shipment { s.Status = StatusClosed; return s },
		"no manifest":   func(s Shipment) Shipment { s.Cartons = nil; return s },
	} {
		t.Run(name, func(t *testing.T) {
			err := mutate(sh).CheckReceipt(good)
			if !errors.Is(err, shared.ErrIllegalTransition) {
				t.Fatalf("err = %v, want illegal transition", err)
			}
		})
	}
}

func TestReceiptSubmittedEvent(t *testing.T) {
	r, _ := receipt(rc(ssccA, CartonReceived)).Normalise()
	e := NewReceiptSubmitted("tx-1", "V-1", r, t0.In(time.FixedZone("x", 7200)))
	if e.EventName() != "InboundReceiptSubmitted" || e.Entity() != "inboundreceipt" || e.SubjectID() != "SHP-1" {
		t.Errorf("identity = %s %s %s", e.EventName(), e.Entity(), e.SubjectID())
	}
	if e.AggregateVersion() != 1 || !e.OccurredAt().Equal(t0) || e.OccurredAt().Location() != time.UTC {
		t.Errorf("header = %d %v", e.AggregateVersion(), e.OccurredAt())
	}
	if e.TransactionID != "tx-1" || e.VendorID != "V-1" || e.NodeID != "n-1" || len(e.Cartons) != 1 || !e.ReceivedAt.Equal(r.ReceivedAt) {
		t.Errorf("event = %+v", e)
	}
}

// --- packaging ------------------------------------------------------------

func declared(level Level, gtin string, units int, at time.Time) Declared {
	return Declared{CatalogItemID: "item-12", Level: level, GTIN: gtin, UnitsPerPack: units, DeclaredAt: at}
}

func TestOnDeclared(t *testing.T) {
	p, err := PackLevel{}.OnDeclared(Declared{CatalogItemID: "item-12", Level: LevelCase, GTIN: gtinC, ChildGTIN: gtinE13, UnitsPerPack: 12, DeclaredAt: t0})
	if err != nil || !p.Active || p.GTIN != gtinC || p.ChildGTIN != gtinE || p.UnitsPerPack != 12 || p.Level != LevelCase {
		t.Fatalf("declared = %+v %v", p, err)
	}
	if e, err := (PackLevel{}).OnDeclared(declared(LevelEach, gtinE, 1, t0)); err != nil || e.ChildGTIN != "" {
		t.Fatalf("each = %+v %v", e, err)
	}
	if _, err := (PackLevel{}).OnDeclared(declared(LevelCase, gtinC, 2, t0)); err != nil {
		t.Errorf("unitsPerPack 2 is the lowest above EACH: %v", err)
	}
	if _, err := (PackLevel{}).OnDeclared(declared(LevelPallet, gtinC, shared.MaxInt32, t0)); err != nil {
		t.Errorf("max units: %v", err)
	}
	bad := map[string]Declared{
		"item":      {CatalogItemID: "", Level: LevelCase, GTIN: gtinC, UnitsPerPack: 12, DeclaredAt: t0},
		"level":     declared("BOX", gtinC, 12, t0),
		"gtin":      declared(LevelCase, "1", 12, t0),
		"child":     {CatalogItemID: "i", Level: LevelCase, GTIN: gtinC, ChildGTIN: "1", UnitsPerPack: 12, DeclaredAt: t0},
		"each 2":    declared(LevelEach, gtinE, 2, t0),
		"case 1":    declared(LevelCase, gtinC, 1, t0),
		"case 0":    declared(LevelCase, gtinC, 0, t0),
		"over":      declared(LevelCase, gtinC, shared.MaxInt32+1, t0),
		"declared":  declared(LevelCase, gtinC, 12, time.Time{}),
		"variable":  declared(LevelCase, gtinVar, 12, t0),
		"each zero": declared(LevelEach, gtinE, 0, t0),
	}
	for name, d := range bad {
		t.Run(name, func(t *testing.T) {
			_, err := PackLevel{}.OnDeclared(d)
			wantInvalid(t, err)
		})
	}
}

func TestRetirementAndDeclarationOrdering(t *testing.T) {
	d, _ := PackLevel{}.OnDeclared(declared(LevelCase, gtinC, 12, t0))
	r, err := d.OnRetired(Retired{CatalogItemID: "item-12", Level: LevelCase, GTIN: gtinC, RetiredAt: t0.Add(time.Hour)})
	if err != nil || r.Active || !r.RetiredAt.Equal(t0.Add(time.Hour)) || r.UnitsPerPack != 12 {
		t.Fatalf("retired = %+v %v", r, err)
	}
	// A retirement that predates the declaration is stale; one at the same instant wins the tie.
	stale, _ := d.OnRetired(Retired{CatalogItemID: "item-12", Level: LevelCase, GTIN: gtinC, RetiredAt: t0.Add(-time.Hour)})
	if !stale.Active {
		t.Error("a stale retirement deactivated the level")
	}
	same, _ := d.OnRetired(Retired{CatalogItemID: "item-12", Level: LevelCase, GTIN: gtinC, RetiredAt: t0})
	if same.Active {
		t.Error("a retirement at the declaration instant wins the tie, in either arrival order")
	}
	// Retired first, declared later: stays retired when the declaration is not newer.
	stub, err := PackLevel{}.OnRetired(Retired{CatalogItemID: "item-12", Level: LevelCase, GTIN: gtinC, RetiredAt: t0.Add(time.Hour)})
	if err != nil || stub.Active {
		t.Fatalf("stub = %+v %v", stub, err)
	}
	old, _ := stub.OnDeclared(declared(LevelCase, gtinC, 12, t0))
	atSame, _ := stub.OnDeclared(declared(LevelCase, gtinC, 12, t0.Add(time.Hour)))
	newer, _ := stub.OnDeclared(declared(LevelCase, gtinC, 12, t0.Add(2*time.Hour)))
	if old.Active || atSame.Active || !newer.Active {
		t.Errorf("redeclare: old=%v same=%v newer=%v", old.Active, atSame.Active, newer.Active)
	}
	// The latest retirement time wins.
	later, _ := r.OnRetired(Retired{CatalogItemID: "item-12", Level: LevelCase, GTIN: gtinC, RetiredAt: t0.Add(3 * time.Hour)})
	earlier, _ := r.OnRetired(Retired{CatalogItemID: "item-12", Level: LevelCase, GTIN: gtinC, RetiredAt: t0.Add(30 * time.Minute)})
	if !later.RetiredAt.Equal(t0.Add(3*time.Hour)) || !earlier.RetiredAt.Equal(t0.Add(time.Hour)) {
		t.Errorf("retiredAt = %v / %v", later.RetiredAt, earlier.RetiredAt)
	}
	for name, e := range map[string]Retired{
		"item":  {CatalogItemID: "", Level: LevelCase, GTIN: gtinC, RetiredAt: t0},
		"level": {CatalogItemID: "i", Level: "", GTIN: gtinC, RetiredAt: t0},
		"gtin":  {CatalogItemID: "i", Level: LevelCase, GTIN: "x", RetiredAt: t0},
		"time":  {CatalogItemID: "i", Level: LevelCase, GTIN: gtinC},
	} {
		t.Run("bad "+name, func(t *testing.T) {
			_, err := PackLevel{}.OnRetired(e)
			wantInvalid(t, err)
		})
	}
}

func TestActiveLevelsAreOrderedAndFiltered(t *testing.T) {
	rows := []PackLevel{
		{Level: LevelPallet, GTIN: "20614141000023", Active: true},
		{Level: LevelCase, GTIN: gtinC, Active: false},
		{Level: LevelCase, GTIN: "10614141000026", Active: true},
		{Level: LevelEach, GTIN: gtinE, Active: true},
		{Level: LevelInner, GTIN: gtinF, Active: true},
		{Level: LevelCase, GTIN: "10614141000019", Active: true},
	}
	got := ActiveLevels(rows)
	want := []string{gtinE, gtinF, "10614141000019", "10614141000026", "20614141000023"}
	if len(got) != len(want) {
		t.Fatalf("got %d rows, want %d", len(got), len(want))
	}
	for i, g := range want {
		if got[i].GTIN != g {
			t.Errorf("row %d = %s, want %s", i, got[i].GTIN, g)
		}
	}
	if len(ActiveLevels(nil)) != 0 || ActiveLevels(nil) == nil {
		t.Error("no rows is an empty, non-nil list")
	}
}

func TestLevelOrderAndValidity(t *testing.T) {
	ordered := []Level{LevelEach, LevelInner, LevelCase, LevelPallet}
	for i := 1; i < len(ordered); i++ {
		if ordered[i].order() <= ordered[i-1].order() {
			t.Errorf("%s does not rank above %s", ordered[i], ordered[i-1])
		}
	}
	for _, l := range ordered {
		if !l.Valid() {
			t.Errorf("%s must be valid", l)
		}
	}
	if Level("").Valid() || Level("BOX").Valid() || Level("BOX").order() != 0 {
		t.Error("an unknown level is invalid")
	}
}
