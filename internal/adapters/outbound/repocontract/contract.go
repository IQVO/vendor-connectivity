// Package repocontract is the behaviour every implementation of the repository
// ports must show, run against the in-memory doubles and against Postgres
// (integration build tag) with the SAME suite. It is a normal (non _test)
// package so both adapters' tests can import it.
package repocontract

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/claudioed/vendor-connectivity/internal/application/ports"
	"github.com/claudioed/vendor-connectivity/internal/domain/purchaseorder"
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
