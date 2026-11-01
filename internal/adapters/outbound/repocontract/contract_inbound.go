package repocontract

import (
	"context"
	"testing"
	"time"

	"github.com/claudioed/vendor-connectivity/internal/application/ports"
	"github.com/claudioed/vendor-connectivity/internal/application/repository"
	"github.com/claudioed/vendor-connectivity/internal/domain/inbound"
)

func shipmentRow(id, node string, shippedAt time.Time, status inbound.Status, cartons ...inbound.Carton) inbound.Shipment {
	return inbound.Shipment{ID: id, PlanID: "plan-1", SellerID: "S-1", NodeID: node, Status: status, Cartons: cartons, ShippedAt: shippedAt}
}

func oneCarton(sscc string) inbound.Carton {
	return inbound.Carton{SSCC: sscc, ParentSSCC: "006141410000000036", Kind: "HOMOGENEOUS_CASE", GrossWeightG: 1200,
		Dims:     &inbound.Dims{L: 400, W: 300, H: 200},
		Contents: []inbound.Item{{CatalogItemID: "item-1", GTIN: "00614141000036", QtyEaches: 12, Lot: "L-1", Expiry: "2027-03-01"}}}
}

func inboundShipments(t *testing.T, e Env) {
	ctx := context.Background()
	_, err := e.Shipments.Get(ctx, "SHP-1")
	isErr(t, err, ports.ErrNotFound, "Get of an unknown shipment")

	a := shipmentRow("SHP-1", "n-1", T0, inbound.StatusShipped, oneCarton("006141410000000012"))
	a.LabelledAt = T0.Add(-time.Hour)
	must(t, e.Shipments.Save(ctx, a))
	got, err := e.Shipments.Get(ctx, "SHP-1")
	must(t, err)
	if got.NodeID != "n-1" || got.Status != inbound.StatusShipped || !got.ShippedAt.Equal(T0) || !got.LabelledAt.Equal(a.LabelledAt) || !got.ReceivedAt.IsZero() ||
		len(got.Cartons) != 1 || got.Cartons[0].Dims == nil || got.Cartons[0].Dims.W != 300 || got.Cartons[0].Contents[0].Lot != "L-1" || got.Cartons[0].ParentSSCC != "006141410000000036" {
		t.Fatalf("round trip = %+v", got)
	}
	got.Status = inbound.StatusClosed
	got.ReceivedAt = T0.Add(time.Hour)
	must(t, e.Shipments.Save(ctx, got))
	again, _ := e.Shipments.Get(ctx, "SHP-1")
	if again.Status != inbound.StatusClosed || !again.ReceivedAt.Equal(T0.Add(time.Hour)) {
		t.Fatalf("an upsert replaces the row: %+v", again)
	}
	listShipped(t, e)
}

func listShipped(t *testing.T, e Env) {
	ctx := context.Background()
	for _, sh := range []inbound.Shipment{
		shipmentRow("SHP-2", "n-1", T0.Add(2*time.Hour), inbound.StatusShipped, oneCarton("006141410000000029")),
		shipmentRow("SHP-3", "n-1", T0.Add(2*time.Hour), inbound.StatusShipped, oneCarton("006141410000000029")),
		shipmentRow("SHP-4", "n-2", T0.Add(3*time.Hour), inbound.StatusShipped, oneCarton("006141410000000029")),
		shipmentRow("SHP-5", "n-1", T0.Add(4*time.Hour), inbound.StatusLabelled, oneCarton("006141410000000029")),
		shipmentRow("SHP-6", "n-1", T0.Add(5*time.Hour), inbound.StatusShipped),
		shipmentRow("SHP-7", "n-3", T0.Add(6*time.Hour), inbound.StatusShipped, oneCarton("006141410000000029")),
	} {
		must(t, e.Shipments.Save(ctx, sh))
	}
	ids := func(f repository.InboundShipmentFilter) string {
		t.Helper()
		rows, err := e.Shipments.ListShipped(ctx, f)
		must(t, err)
		out := ""
		for _, r := range rows {
			out += r.ID + " "
		}
		return out
	}
	// SHP-1 is CLOSED, SHP-5 LABELLED, SHP-6 has no manifest and SHP-7 is another node: none is listed.
	if got := ids(repository.InboundShipmentFilter{NodeIDs: []string{"n-1", "n-2"}}); got != "SHP-2 SHP-3 SHP-4 " {
		t.Errorf("both nodes = %q", got)
	}
	if got := ids(repository.InboundShipmentFilter{NodeIDs: []string{"n-2"}}); got != "SHP-4 " {
		t.Errorf("n-2 = %q", got)
	}
	after := T0.Add(2 * time.Hour)
	if got := ids(repository.InboundShipmentFilter{NodeIDs: []string{"n-1", "n-2"}, ShippedAfter: &after}); got != "SHP-4 " {
		t.Errorf("shippedAfter is strict = %q", got)
	}
	cur := repository.ShipmentCursor{ShippedAt: T0.Add(2 * time.Hour), ShipmentID: "SHP-2"}
	if got := ids(repository.InboundShipmentFilter{NodeIDs: []string{"n-1", "n-2"}, After: &cur}); got != "SHP-3 SHP-4 " {
		t.Errorf("keyset breaks the tie on the id = %q", got)
	}
	if got := ids(repository.InboundShipmentFilter{NodeIDs: []string{"n-1", "n-2"}, Limit: 2}); got != "SHP-2 SHP-3 " {
		t.Errorf("limit = %q", got)
	}
	if got := ids(repository.InboundShipmentFilter{NodeIDs: []string{"n-9"}}); got != "" {
		t.Errorf("unknown node = %q", got)
	}
}

func packRow(item string, level inbound.Level, gtin string, active bool) inbound.PackLevel {
	p := inbound.PackLevel{CatalogItemID: item, Level: level, GTIN: gtin, UnitsPerPack: 12, Active: active, DeclaredAt: T0}
	if level == inbound.LevelEach {
		p.UnitsPerPack = 1
	}
	if !active {
		p.RetiredAt = T0.Add(time.Hour)
	}
	return p
}

func catalogPackaging(t *testing.T, e Env) {
	ctx := context.Background()
	_, err := e.Packaging.Get(ctx, "item-1", inbound.LevelCase, "10614141000019")
	isErr(t, err, ports.ErrNotFound, "Get of an unknown level")
	rows, err := e.Packaging.ListByItem(ctx, "item-1")
	must(t, err)
	if len(rows) != 0 {
		t.Fatalf("empty item lists %d rows", len(rows))
	}
	c := packRow("item-1", inbound.LevelCase, "10614141000019", true)
	c.ChildGTIN = "00614141000036"
	must(t, e.Packaging.Save(ctx, c))
	must(t, e.Packaging.Save(ctx, packRow("item-1", inbound.LevelEach, "00614141000036", true)))
	must(t, e.Packaging.Save(ctx, packRow("item-2", inbound.LevelEach, "00614141000050", true)))
	got, err := e.Packaging.Get(ctx, "item-1", inbound.LevelCase, "10614141000019")
	must(t, err)
	if got.ChildGTIN != "00614141000036" || got.UnitsPerPack != 12 || !got.Active || !got.DeclaredAt.Equal(T0) || !got.RetiredAt.IsZero() {
		t.Fatalf("round trip = %+v", got)
	}
	must(t, e.Packaging.Save(ctx, packRow("item-1", inbound.LevelCase, "10614141000019", false)))
	got, _ = e.Packaging.Get(ctx, "item-1", inbound.LevelCase, "10614141000019")
	if got.Active || !got.RetiredAt.Equal(T0.Add(time.Hour)) {
		t.Fatalf("an upsert replaces the row: %+v", got)
	}
	rows, err = e.Packaging.ListByItem(ctx, "item-1")
	must(t, err)
	if len(rows) != 2 {
		t.Fatalf("item-1 lists %d rows, want 2 (retired rows stay as history)", len(rows))
	}
}
