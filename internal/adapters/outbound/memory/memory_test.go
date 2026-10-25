package memory_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/claudioed/vendor-connectivity/internal/adapters/outbound/memory"
	"github.com/claudioed/vendor-connectivity/internal/adapters/outbound/repocontract"
	"github.com/claudioed/vendor-connectivity/internal/application/gateway"
	"github.com/claudioed/vendor-connectivity/internal/application/requests"
	"github.com/claudioed/vendor-connectivity/internal/domain/shared"
	"github.com/claudioed/vendor-connectivity/internal/domain/vendor"
)

func TestMemoryDoublesSatisfyTheRepositoryContract(t *testing.T) {
	repocontract.Run(t, func(t *testing.T) repocontract.Env {
		s := memory.NewStore()
		return repocontract.Env{
			UoW: s.UnitOfWork(), Vendors: s.Vendors(), Orders: s.Orders(), Submissions: s.Submissions(), Nodes: s.Nodes(),
			Listings: s.Listings(), Shipments: s.Shipments(), Packaging: s.Packaging(), Inbox: s.Inbox(), Outbox: s.Outbox(),
			OutboxCount: func(context.Context) (int, error) { return len(s.Outbox().Events()), nil },
		}
	})
}

func TestUnitOfWorkRestoresOnPanic(t *testing.T) {
	s := memory.NewStore()
	func() {
		defer func() { _ = recover() }()
		_ = s.UnitOfWork().Do(context.Background(), func(ctx context.Context) error {
			a, _, _ := vendor.Onboard(vendor.NewAccount{ID: "V-1", Name: "n", AckWindowMinutes: 60}, repocontract.T0)
			_ = s.Vendors().Save(ctx, a)
			_ = s.Outbox().Append(ctx, vendor.VendorOnboarded{Header: shared.Header{Version: 1}})
			panic("boom")
		})
	}()
	if c := s.Counts(); c.Vendors != 0 || c.OutboxEvents != 0 {
		t.Fatalf("a panicking unit must leave nothing behind: %+v", c)
	}
}

func TestCountsSumVersions(t *testing.T) {
	s := memory.NewStore()
	ctx := context.Background()
	a, _, _ := vendor.Onboard(vendor.NewAccount{ID: "V-1", Name: "n", AckWindowMinutes: 60}, repocontract.T0)
	_ = s.Vendors().Save(ctx, a)
	if c := s.Counts(); c.Vendors != 1 || c.VersionSum != 1 {
		t.Fatalf("counts = %+v", c)
	}
}

func TestClockIDsAndRandom(t *testing.T) {
	c := memory.NewClock(repocontract.T0)
	c.Advance(time.Hour)
	if !c.Now().Equal(repocontract.T0.Add(time.Hour)) {
		t.Fatal("Advance")
	}
	c.Set(repocontract.T0)
	if !c.Now().Equal(repocontract.T0) {
		t.Fatal("Set")
	}
	ids := &memory.SequentialIDs{}
	if ids.NewTransactionID() != "tx-1" || ids.NewTransactionID() != "tx-2" {
		t.Fatal("ids")
	}
	r := memory.NewScriptedRandom(0.1, 0.2)
	if r.Float64() != 0.1 || r.Float64() != 0.2 || r.Float64() != 0.99 {
		t.Fatal("scripted random")
	}
}

func TestNeighbourDoublesRecordAndFail(t *testing.T) {
	ctx := context.Background()
	boom := errors.New("down")
	nt := &memory.NodeTopology{Registered: gateway.RegisteredNode{NodeID: "n-1"}}
	if n, err := nt.RegisterNode(ctx, "k", requestsReg()); err != nil || n.NodeID != "n-1" || len(nt.Registers) != 1 {
		t.Fatal("RegisterNode")
	}
	nt.Err = boom
	if err := nt.DeclareOperationalProfile(ctx, "k", "n-1", requestsProf()); !errors.Is(err, boom) || len(nt.Profiles) != 1 {
		t.Fatal("DeclareOperationalProfile")
	}
	of := &memory.OfferAvailability{Err: boom}
	if err := of.ApplyInventoryFeed(ctx, "f", requestsFeed()); !errors.Is(err, boom) || of.FeedIDs[0] != "f" {
		t.Fatal("ApplyInventoryFeed")
	}
	sh := &memory.ShippingTracking{Err: boom, FailPackage: 2}
	if l, err := sh.IssueLabel(ctx, "k1", gateway.LabelCall{PONumber: "PO-1", PackageNo: 1}); err != nil || l.LabelRef != "lbl-PO-1-1" {
		t.Fatalf("label 1: %+v %v", l, err)
	}
	if _, err := sh.IssueLabel(ctx, "k2", gateway.LabelCall{PONumber: "PO-1", PackageNo: 2}); !errors.Is(err, boom) {
		t.Fatal("label 2 must fail")
	}
	np := &memory.NodePerformance{}
	if sc, err := np.GetScorecard(ctx, "n-1"); err != nil || sc.NodeID != "n-1" {
		t.Fatal("GetScorecard")
	}
	np.Err = boom
	if _, err := np.GetScorecard(ctx, "n-1"); !errors.Is(err, boom) {
		t.Fatal("GetScorecard error")
	}
}

func requestsReg() requests.NodeRegistration { return requests.NodeRegistration{VendorID: "V-1"} }
func requestsProf() requests.NodeProfile     { return requests.NodeProfile{VendorID: "V-1"} }
func requestsFeed() requests.InventoryFeed   { return requests.InventoryFeed{VendorID: "V-1"} }
