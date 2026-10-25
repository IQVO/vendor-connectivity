// Package repocontract is the behaviour every implementation of the repository
// ports must show, run against the in-memory doubles and against Postgres
// (integration build tag) with the SAME suite. It is a normal (non _test)
// package so both adapters' tests can import it.
package repocontract

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/claudioed/vendor-connectivity/internal/application/ports"
	"github.com/claudioed/vendor-connectivity/internal/application/repository"
	"github.com/claudioed/vendor-connectivity/internal/domain/inbound"
	"github.com/claudioed/vendor-connectivity/internal/domain/network"
	"github.com/claudioed/vendor-connectivity/internal/domain/purchaseorder"
	"github.com/claudioed/vendor-connectivity/internal/domain/shared"
	"github.com/claudioed/vendor-connectivity/internal/domain/submission"
	"github.com/claudioed/vendor-connectivity/internal/domain/vendor"
)

// Env is one fresh, empty implementation under test.
type Env struct {
	UoW         ports.UnitOfWork
	Vendors     ports.VendorRepository
	Orders      ports.PurchaseOrderRepository
	Submissions ports.SubmissionRepository
	Nodes       ports.NodeViewRepository
	Listings    ports.ListingViewRepository
	Shipments   ports.InboundShipmentViewRepository
	Packaging   ports.CatalogPackagingViewRepository
	Inbox       ports.ProcessedEvents
	Outbox      ports.EventOutbox
	// OutboxCount returns how many outbox rows are committed.
	OutboxCount func(ctx context.Context) (int, error)
}

// T0 is the instant the builders start from.
var T0 = time.Date(2026, 10, 22, 13, 0, 0, 0, time.UTC)

// Run executes the whole contract; newEnv must return an empty Env per call.
func Run(t *testing.T, newEnv func(t *testing.T) Env) {
	t.Helper()
	groups := map[string]func(*testing.T, Env){
		"Vendors": vendors, "PurchaseOrders": purchaseOrders, "OrderQueries": orderQueries, "Submissions": submissions,
		"Nodes": nodes, "Listings": listings, "InboundShipments": inboundShipments, "CatalogPackaging": catalogPackaging, "Inbox": inbox, "UnitOfWork": unitOfWork,
	}
	for name, fn := range groups {
		t.Run(name, func(t *testing.T) { fn(t, newEnv(t)) })
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func isErr(t *testing.T, err, want error, what string) {
	t.Helper()
	if !errors.Is(err, want) {
		t.Fatalf("%s: err = %v, want %v", what, err, want)
	}
}

// NewVendor builds an ACTIVE vendor.
func NewVendor(t *testing.T, id string) *vendor.Account {
	t.Helper()
	a, _, err := vendor.Onboard(vendor.NewAccount{ID: vendor.ID(id), Name: "Vendor " + id, AckWindowMinutes: 240}, T0)
	must(t, err)
	return a
}

// NewOrder issues an ISSUED order for (assignment, release 1) at issuedAt.
func NewOrder(t *testing.T, assignment, vendorID, nodeID string, issuedAt time.Time) *purchaseorder.PurchaseOrder {
	t.Helper()
	p, _, err := purchaseorder.Issue(purchaseorder.NewOrder{
		AssignmentID: assignment, ReleaseNo: 1, VendorID: vendorID, NodeID: nodeID, OrderID: "ord-" + assignment, GroupNo: 1,
		Tier: purchaseorder.NextDay, RequiredShipBy: issuedAt.Add(8 * time.Hour), AckWindow: 4 * time.Hour, HandlingTime: time.Hour,
		Lines: []purchaseorder.NewLine{{ListingID: "lst-1", Quantity: 2}, {ListingID: "lst-2", Quantity: 1}},
	}, issuedAt)
	must(t, err)
	return p
}

// NewSubmission receives a submission at at with the given delay.
func NewSubmission(t *testing.T, id string, kind submission.Kind, at time.Time, delay time.Duration) *submission.Submission {
	t.Helper()
	s, err := submission.Receive(submission.NewSubmission{ID: submission.ID(id), VendorID: "V-1", Kind: kind, Payload: []byte(`{"vendorId":"V-1"}`)}, at, delay)
	must(t, err)
	return s
}

func vendors(t *testing.T, e Env) {
	ctx := context.Background()
	_, err := e.Vendors.Get(ctx, "V-1")
	isErr(t, err, ports.ErrNotFound, "Get on empty")
	a := NewVendor(t, "V-1")
	must(t, e.Vendors.Save(ctx, a))
	got, err := e.Vendors.Get(ctx, "V-1")
	must(t, err)
	if !reflect.DeepEqual(got.Snapshot(), a.Snapshot()) {
		t.Fatalf("round trip:\n got %+v\nwant %+v", got.Snapshot(), a.Snapshot())
	}
	isErr(t, e.Vendors.Save(ctx, NewVendor(t, "V-1")), ports.ErrConcurrentUpdate, "second create")
	_, err = got.Suspend("audit", T0.Add(time.Hour))
	must(t, err)
	must(t, e.Vendors.Save(ctx, got))
	stale, _ := e.Vendors.Get(ctx, "V-1")
	fresh, _ := e.Vendors.Get(ctx, "V-1")
	_, _ = stale.Reinstate(T0.Add(2 * time.Hour))
	_, _ = fresh.Reinstate(T0.Add(2 * time.Hour))
	must(t, e.Vendors.Save(ctx, fresh))
	isErr(t, e.Vendors.Save(ctx, stale), ports.ErrConcurrentUpdate, "stale update")
	again, _ := e.Vendors.Get(ctx, "V-1")
	if again.Version() != 3 || again.State() != vendor.Active {
		t.Fatalf("stored = %+v", again.Snapshot())
	}
	ghost := NewVendor(t, "V-9")
	_, _ = ghost.Suspend("", T0)
	isErr(t, e.Vendors.Save(ctx, ghost), ports.ErrConcurrentUpdate, "update of an unknown vendor")
}

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

func submissions(t *testing.T, e Env) {
	ctx := context.Background()
	_, err := e.Submissions.Get(ctx, "tx-0")
	isErr(t, err, ports.ErrNotFound, "Get on empty")
	s := NewSubmission(t, "tx-1", submission.KindInventory, T0, time.Minute)
	must(t, e.Submissions.Save(ctx, s))
	got, err := e.Submissions.Get(ctx, "tx-1")
	must(t, err)
	if !reflect.DeepEqual(got.Snapshot(), s.Snapshot()) {
		t.Fatalf("round trip:\n got %+v\nwant %+v", got.Snapshot(), s.Snapshot())
	}
	isErr(t, e.Submissions.Save(ctx, NewSubmission(t, "tx-1", submission.KindInventory, T0, 0)), ports.ErrConcurrentUpdate, "second create")
	for i, delay := range []time.Duration{0, 2 * time.Minute, 30 * time.Second} {
		must(t, e.Submissions.Save(ctx, NewSubmission(t, fmt.Sprintf("tx-d%d", i), submission.KindAcknowledgement, T0, delay)))
	}
	due, err := e.Submissions.ListDue(ctx, T0.Add(time.Minute), 10)
	must(t, err)
	ids := make([]string, len(due))
	for i, d := range due {
		ids[i] = string(d.ID())
	}
	if !reflect.DeepEqual(ids, []string{"tx-d0", "tx-d2", "tx-1"}) {
		t.Fatalf("due (notBefore <= now, oldest first) = %v", ids)
	}
	one, _ := e.Submissions.ListDue(ctx, T0.Add(time.Minute), 1)
	if len(one) != 1 {
		t.Fatalf("limit: %d", len(one))
	}
	processedAndPurged(t, ctx, e)
}

func processedAndPurged(t *testing.T, ctx context.Context, e Env) {
	t.Helper()
	p, err := e.Submissions.Get(ctx, "tx-d0")
	must(t, err)
	_, err = p.Complete(submission.Outcome{Status: submission.Failure, Errors: []submission.Error{{Code: "ErrX", Message: "m", Path: "$.a"}}}, T0.Add(time.Second))
	must(t, err)
	must(t, e.Submissions.Save(ctx, p))
	again, _ := e.Submissions.Get(ctx, "tx-d0")
	if !reflect.DeepEqual(again.Snapshot(), p.Snapshot()) {
		t.Fatalf("outcome round trip:\n got %+v\nwant %+v", again.Snapshot(), p.Snapshot())
	}
	due, _ := e.Submissions.ListDue(ctx, T0.Add(time.Hour), 10)
	if len(due) != 3 {
		t.Fatalf("a processed submission is not due: %d", len(due))
	}
	n, err := e.Submissions.DeleteProcessedBefore(ctx, T0)
	must(t, err)
	if n != 0 {
		t.Fatalf("nothing was processed before T0: %d", n)
	}
	n, err = e.Submissions.DeleteProcessedBefore(ctx, T0.Add(time.Minute))
	must(t, err)
	if n != 1 {
		t.Fatalf("one processed submission is old: %d", n)
	}
	_, err = e.Submissions.Get(ctx, "tx-d0")
	isErr(t, err, ports.ErrNotFound, "purged")
	if _, err := e.Submissions.Get(ctx, "tx-1"); err != nil {
		t.Fatalf("PROCESSING submissions are never purged: %v", err)
	}
}

func nodes(t *testing.T, e Env) {
	ctx := context.Background()
	_, err := e.Nodes.Get(ctx, "n-1")
	isErr(t, err, ports.ErrNotFound, "Get on empty")
	reg := func(id, vendorID string, op network.Operator) network.Node {
		n, err := network.Node{}.OnRegistered(network.Registered{NodeID: id, NodeType: "FULFILLMENT_CENTER", Subtype: "SORTABLE", Operator: op,
			VendorID: vendorID, VendorNodeCode: "code-" + id, RegionID: "r-1", Timezone: "America/Sao_Paulo"})
		must(t, err)
		return n
	}
	n1 := reg("n-1", "V-1", network.OperatorVendor)
	must(t, e.Nodes.Save(ctx, n1))
	got, err := e.Nodes.Get(ctx, "n-1")
	must(t, err)
	if got != n1 {
		t.Fatalf("round trip: %+v vs %+v", got, n1)
	}
	n1, err = n1.OnProfileDeclared("n-1", 2, 90)
	must(t, err)
	n1, err = n1.OnStatus("n-1", network.StatusActive)
	must(t, err)
	must(t, e.Nodes.Save(ctx, n1))
	got, _ = e.Nodes.Get(ctx, "n-1")
	if got != n1 || got.HandlingTimeMinutes != 90 || got.Status != network.StatusActive {
		t.Fatalf("upsert replaced the row: %+v", got)
	}
	must(t, e.Nodes.Save(ctx, reg("n-0", "V-1", network.OperatorVendor)))
	must(t, e.Nodes.Save(ctx, reg("n-2", "V-2", network.OperatorVendor)))
	must(t, e.Nodes.Save(ctx, reg("n-3", "", network.OperatorNetwork)))
	mine, err := e.Nodes.ListByVendor(ctx, "V-1")
	must(t, err)
	if len(mine) != 2 || mine[0].ID != "n-0" || mine[1].ID != "n-1" {
		t.Fatalf("ListByVendor = %+v", mine)
	}
	none, err := e.Nodes.ListByVendor(ctx, "V-9")
	if err != nil || len(none) != 0 {
		t.Fatalf("no nodes is an empty list: %v %v", none, err)
	}
}

func listings(t *testing.T, e Env) {
	ctx := context.Background()
	_, err := e.Listings.Get(ctx, "lst-1")
	isErr(t, err, ports.ErrNotFound, "Get on empty")
	l, err := network.NewListing("lst-1", "n-1", "item-1")
	must(t, err)
	must(t, e.Listings.Save(ctx, l))
	got, err := e.Listings.Get(ctx, "lst-1")
	must(t, err)
	if got != l {
		t.Fatalf("round trip: %+v", got)
	}
	l.NodeID = "n-2"
	must(t, e.Listings.Save(ctx, l))
	got, _ = e.Listings.Get(ctx, "lst-1")
	if got.NodeID != "n-2" {
		t.Fatalf("upsert replaced the row: %+v", got)
	}
}

func inbox(t *testing.T, e Env) {
	ctx := context.Background()
	first, err := e.Inbox.Claim(ctx, "evt-1", "com.example.T", T0)
	must(t, err)
	again, err := e.Inbox.Claim(ctx, "evt-1", "com.example.T", T0.Add(time.Second))
	must(t, err)
	other, err := e.Inbox.Claim(ctx, "evt-2", "com.example.T", T0)
	must(t, err)
	if !first || again || !other {
		t.Fatalf("claims = %v %v %v, want true false true", first, again, other)
	}
}

type boom struct{}

func (boom) Error() string { return "boom" }

func unitOfWork(t *testing.T, e Env) {
	ctx := context.Background()
	count := func() int {
		n, err := e.OutboxCount(ctx)
		must(t, err)
		return n
	}
	ev := []shared.Event{vendor.VendorOnboarded{Header: shared.Header{Version: 1, At: T0}, VendorID: "V-1", Name: "n", AckWindowMinutes: 60}}
	// commit
	err := e.UoW.Do(ctx, func(ctx context.Context) error {
		must(t, e.Vendors.Save(ctx, NewVendor(t, "V-1")))
		must(t, e.Outbox.Append(ctx, ev...))
		_, err := e.Inbox.Claim(ctx, "evt-commit", "t", T0)
		return err
	})
	must(t, err)
	if _, err := e.Vendors.Get(ctx, "V-1"); err != nil || count() != 1 {
		t.Fatalf("commit must persist the vendor and its outbox row: %v %d", err, count())
	}
	// rollback
	err = e.UoW.Do(ctx, func(ctx context.Context) error {
		must(t, e.Vendors.Save(ctx, NewVendor(t, "V-2")))
		must(t, e.Outbox.Append(ctx, ev...))
		must(t, e.Orders.Save(ctx, NewOrder(t, "asg-rb", "V-2", "n-1", T0)))
		if _, err := e.Inbox.Claim(ctx, "evt-rollback", "t", T0); err != nil {
			return err
		}
		return boom{}
	})
	isErr(t, err, boom{}, "the unit returns fn's error unchanged")
	_, gerr := e.Vendors.Get(ctx, "V-2")
	_, oerr := e.Orders.FindByRelease(ctx, "asg-rb", 1)
	isErr(t, gerr, ports.ErrNotFound, "rolled-back vendor")
	isErr(t, oerr, ports.ErrNotFound, "rolled-back order")
	if count() != 1 {
		t.Fatalf("rolled-back outbox rows must vanish: %d", count())
	}
	claimed, err := e.Inbox.Claim(ctx, "evt-rollback", "t", T0)
	must(t, err)
	if !claimed {
		t.Fatal("a rolled-back inbox claim must vanish")
	}
	// re-entrant: an inner failure inside a joined unit rolls the OUTER unit back
	err = e.UoW.Do(ctx, func(ctx context.Context) error {
		must(t, e.Vendors.Save(ctx, NewVendor(t, "V-3")))
		return e.UoW.Do(ctx, func(ctx context.Context) error {
			must(t, e.Vendors.Save(ctx, NewVendor(t, "V-4")))
			return boom{}
		})
	})
	isErr(t, err, boom{}, "inner error")
	for _, id := range []vendor.ID{"V-3", "V-4"} {
		_, err := e.Vendors.Get(ctx, id)
		isErr(t, err, ports.ErrNotFound, "joined unit "+string(id))
	}
	err = e.UoW.Do(ctx, func(ctx context.Context) error {
		return e.UoW.Do(ctx, func(ctx context.Context) error { return e.Vendors.Save(ctx, NewVendor(t, "V-5")) })
	})
	must(t, err)
	if _, err := e.Vendors.Get(ctx, "V-5"); err != nil {
		t.Fatalf("joined unit commit: %v", err)
	}
}

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
