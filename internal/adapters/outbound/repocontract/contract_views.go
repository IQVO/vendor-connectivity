package repocontract

import (
	"context"
	"testing"

	"github.com/claudioed/vendor-connectivity/internal/application/ports"
	"github.com/claudioed/vendor-connectivity/internal/domain/network"
)

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
