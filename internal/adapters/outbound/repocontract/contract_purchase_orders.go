package repocontract

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/claudioed/vendor-connectivity/internal/application/ports"
	"github.com/claudioed/vendor-connectivity/internal/application/repository"
	"github.com/claudioed/vendor-connectivity/internal/domain/purchaseorder"
)

func purchaseOrders(t *testing.T, e Env) {
	ctx := context.Background()
	_, err := e.Orders.Get(ctx, "PO-x")
	isErr(t, err, ports.ErrNotFound, "Get on empty")
	_, err = e.Orders.FindByRelease(ctx, "asg-1", 1)
	isErr(t, err, ports.ErrNotFound, "FindByRelease on empty")
	_, err = e.Orders.FindLatestByAssignment(ctx, "asg-1")
	isErr(t, err, ports.ErrNotFound, "FindLatestByAssignment on empty")

	p := NewOrder(t, "asg-1", "V-1", "n-1", T0)
	must(t, e.Orders.Save(ctx, p))
	got, err := e.Orders.Get(ctx, p.Number())
	must(t, err)
	if !reflect.DeepEqual(got.Snapshot(), p.Snapshot()) {
		t.Fatalf("round trip:\n got %+v\nwant %+v", got.Snapshot(), p.Snapshot())
	}
	byRelease, err := e.Orders.FindByRelease(ctx, "asg-1", 1)
	must(t, err)
	if byRelease.Number() != p.Number() {
		t.Fatal("FindByRelease returned another order")
	}
	_, err = e.Orders.FindByRelease(ctx, "asg-1", 2)
	isErr(t, err, ports.ErrNotFound, "other release")
	isErr(t, e.Orders.Save(ctx, NewOrder(t, "asg-1", "V-1", "n-1", T0)), ports.ErrConcurrentUpdate, "second order for one release")
	savedFullLifecycle(t, ctx, e)
	latestRelease(t, ctx, e)
}

// savedFullLifecycle stores an order through ACK_PROCESSING..SHIPPED, proving
// labels, the acknowledgement and the shipment round-trip.
func savedFullLifecycle(t *testing.T, ctx context.Context, e Env) {
	t.Helper()
	p := NewOrder(t, "asg-life", "V-1", "n-1", T0)
	must(t, e.Orders.Save(ctx, p))
	steps := []func(*purchaseorder.PurchaseOrder) error{
		func(p *purchaseorder.PurchaseOrder) error {
			_, err := p.SubmitAcknowledgement(purchaseorder.AckInput{Decision: purchaseorder.Accept, TransactionID: "tx-a",
				Lines: []purchaseorder.AckLine{{LineNo: 1, Quantity: 2}, {LineNo: 2, Quantity: 1}}}, T0)
			return err
		},
		func(p *purchaseorder.PurchaseOrder) error {
			_, err := p.ApplyAckOutcome(true, T0.Add(time.Minute))
			return err
		},
		func(p *purchaseorder.PurchaseOrder) error {
			return p.AddLabels([]purchaseorder.Label{{PackageNo: 2, LabelRef: "l2", TrackingNumber: "T2", Carrier: "c"}, {PackageNo: 1, LabelRef: "l1", TrackingNumber: "T1", Carrier: "c"}})
		},
		func(p *purchaseorder.PurchaseOrder) error {
			_, err := p.SubmitShipmentConfirmation(purchaseorder.ShipInput{TrackingNumbers: []string{"T1", "T2"}, ShippedAt: T0.Add(2 * time.Hour), TransactionID: "tx-s"})
			return err
		},
		func(p *purchaseorder.PurchaseOrder) error {
			_, err := p.ApplyShipmentOutcome(true, T0.Add(3*time.Hour))
			return err
		},
	}
	for i, step := range steps {
		cur, err := e.Orders.Get(ctx, p.Number())
		must(t, err)
		must(t, step(cur))
		must(t, e.Orders.Save(ctx, cur))
		again, err := e.Orders.Get(ctx, p.Number())
		must(t, err)
		if !reflect.DeepEqual(again.Snapshot(), cur.Snapshot()) {
			t.Fatalf("step %d round trip:\n got %+v\nwant %+v", i, again.Snapshot(), cur.Snapshot())
		}
	}
	cancelled := NewOrder(t, "asg-cancel", "V-1", "n-1", T0)
	must(t, e.Orders.Save(ctx, cancelled))
	_, err := cancelled.Cancel("customer cancelled", T0.Add(time.Minute))
	must(t, err)
	must(t, e.Orders.Save(ctx, cancelled))
	got, err := e.Orders.Get(ctx, cancelled.Number())
	must(t, err)
	if !reflect.DeepEqual(got.Snapshot(), cancelled.Snapshot()) {
		t.Fatalf("cancelled round trip: %+v", got.Snapshot())
	}
}

func latestRelease(t *testing.T, ctx context.Context, e Env) {
	t.Helper()
	for rel := 1; rel <= 3; rel++ {
		p, _, err := purchaseorder.Issue(purchaseorder.NewOrder{
			AssignmentID: "asg-rel", ReleaseNo: rel, VendorID: "V-1", NodeID: "n-1", OrderID: "ord-r", Tier: purchaseorder.Standard,
			RequiredShipBy: T0.Add(8 * time.Hour), AckWindow: time.Hour, Lines: []purchaseorder.NewLine{{ListingID: "lst-1", Quantity: 1}},
		}, T0.Add(time.Duration(rel)*time.Minute))
		must(t, err)
		must(t, e.Orders.Save(ctx, p))
	}
	got, err := e.Orders.FindLatestByAssignment(ctx, "asg-rel")
	must(t, err)
	if got.ReleaseNo() != 3 {
		t.Fatalf("latest release = %d, want 3", got.ReleaseNo())
	}
}

func orderQueries(t *testing.T, e Env) {
	ctx := context.Background()
	// five orders: V-1/n-1 at +0,+1,+2 min (the last two share the instant), V-1/n-2 at +3, V-2/n-9 at +4
	specs := []struct {
		asg, vendorID, node string
		at                  time.Duration
	}{{"a1", "V-1", "n-1", 0}, {"a2", "V-1", "n-1", time.Minute}, {"a3", "V-1", "n-1", 2 * time.Minute}, {"a4", "V-1", "n-2", 2 * time.Minute}, {"a5", "V-2", "n-9", 4 * time.Minute}}
	byAsg := map[string]*purchaseorder.PurchaseOrder{}
	for _, sp := range specs {
		p := NewOrder(t, sp.asg, sp.vendorID, sp.node, T0.Add(sp.at))
		must(t, e.Orders.Save(ctx, p))
		byAsg[sp.asg] = p
	}
	all, err := e.Orders.List(ctx, repository.PurchaseOrderFilter{})
	must(t, err)
	if len(all) != 5 {
		t.Fatalf("List() = %d orders, want 5", len(all))
	}
	for i := 1; i < len(all); i++ {
		a, b := all[i-1], all[i]
		if b.IssuedAt().Before(a.IssuedAt()) || (b.IssuedAt().Equal(a.IssuedAt()) && b.Number() <= a.Number()) {
			t.Fatalf("List order broken at %d: %s then %s", i, a.Number(), b.Number())
		}
	}
	v1, _ := e.Orders.List(ctx, repository.PurchaseOrderFilter{VendorID: "V-1"})
	n2, _ := e.Orders.List(ctx, repository.PurchaseOrderFilter{VendorID: "V-1", NodeID: "n-2"})
	if len(v1) != 4 || len(n2) != 1 || n2[0].NodeID() != "n-2" {
		t.Fatalf("filters: v1=%d n2=%d", len(v1), len(n2))
	}
	cut := T0.Add(time.Minute)
	later, _ := e.Orders.List(ctx, repository.PurchaseOrderFilter{VendorID: "V-1", IssuedAfter: &cut})
	if len(later) != 2 {
		t.Fatalf("IssuedAfter is strict: got %d, want 2", len(later))
	}
	// paging by cursor walks every V-1 order exactly once, in order
	var seen []purchaseorder.Number
	var cur *repository.Cursor
	for page := 0; page < 10; page++ {
		got, err := e.Orders.List(ctx, repository.PurchaseOrderFilter{VendorID: "V-1", After: cur, Limit: 3})
		must(t, err)
		if len(got) == 0 {
			break
		}
		for _, p := range got {
			seen = append(seen, p.Number())
		}
		last := got[len(got)-1]
		cur = &repository.Cursor{IssuedAt: last.IssuedAt(), PONumber: string(last.Number())}
	}
	if len(seen) != 4 {
		t.Fatalf("paged %d orders, want 4: %v", len(seen), seen)
	}
	expirable(t, ctx, e, byAsg)
}

func expirable(t *testing.T, ctx context.Context, e Env, byAsg map[string]*purchaseorder.PurchaseOrder) {
	t.Helper()
	// ackBy of every order is issuedAt + min(4h, 8h-1h) = +4h
	got, err := e.Orders.ListExpirable(ctx, T0.Add(4*time.Hour), 10)
	must(t, err)
	if len(got) != 0 {
		t.Fatalf("nothing is past ackBy at exactly +4h of the first order, got %d", len(got))
	}
	got, err = e.Orders.ListExpirable(ctx, T0.Add(4*time.Hour+90*time.Second), 10)
	must(t, err)
	if len(got) != 2 || got[0].AckBy().After(got[1].AckBy()) {
		t.Fatalf("two orders are past ackBy, oldest ackBy first: %d", len(got))
	}
	limited, _ := e.Orders.ListExpirable(ctx, T0.Add(24*time.Hour), 3)
	if len(limited) != 3 {
		t.Fatalf("limit: %d", len(limited))
	}
	acked := byAsg["a1"]
	_, err = acked.SubmitAcknowledgement(purchaseorder.AckInput{Decision: purchaseorder.Accept, TransactionID: "tx-1",
		Lines: []purchaseorder.AckLine{{LineNo: 1, Quantity: 2}, {LineNo: 2, Quantity: 1}}}, T0)
	must(t, err)
	must(t, e.Orders.Save(ctx, acked))
	rest, _ := e.Orders.ListExpirable(ctx, T0.Add(24*time.Hour), 10)
	if len(rest) != 4 {
		t.Fatalf("only ISSUED orders are expirable: %d", len(rest))
	}
}
