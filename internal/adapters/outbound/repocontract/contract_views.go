package repocontract

import (
	"context"
	"testing"

	"github.com/claudioed/vendor-connectivity/internal/application/ports"
	"github.com/claudioed/vendor-connectivity/internal/domain/network"
)

func nodes(t *testing.T, e Env) {
	ctx := context.Background()
	_, err := e.Nodes.Get(ctx, nodeOne)
	isErr(t, err, ports.ErrNotFound, "Get on empty")
	reg := func(id, vendorID string, op network.Operator) network.Node {
		n, err := network.Node{}.OnRegistered(network.Registered{NodeID: id, NodeType: "FULFILLMENT_CENTER", Subtype: "SORTABLE", Operator: op,
			VendorID: vendorID, VendorNodeCode: "code-" + id, RegionID: "r-1", Timezone: "America/Sao_Paulo"})
		must(t, err)
		return n
	}
	n1 := reg(nodeOne, vendorOne, network.OperatorVendor)
	must(t, e.Nodes.Save(ctx, n1))
	got, err := e.Nodes.Get(ctx, nodeOne)
	must(t, err)
	if got != n1 {
		t.Fatalf("round trip: %+v vs %+v", got, n1)
	}
	n1, err = n1.OnProfileDeclared(nodeOne, 2, 90)
	must(t, err)
	n1, err = n1.OnStatus(nodeOne, network.StatusActive)
	must(t, err)
	must(t, e.Nodes.Save(ctx, n1))
	got, _ = e.Nodes.Get(ctx, nodeOne)
	if got != n1 || got.HandlingTimeMinutes != 90 || got.Status != network.StatusActive {
		t.Fatalf("upsert replaced the row: %+v", got)
	}
	must(t, e.Nodes.Save(ctx, reg("n-0", vendorOne, network.OperatorVendor)))
	must(t, e.Nodes.Save(ctx, reg(nodeTwo, "V-2", network.OperatorVendor)))
	must(t, e.Nodes.Save(ctx, reg("n-3", "", network.OperatorNetwork)))
	mine, err := e.Nodes.ListByVendor(ctx, vendorOne)
	must(t, err)
	if len(mine) != 2 || mine[0].ID != "n-0" || mine[1].ID != nodeOne {
		t.Fatalf("ListByVendor = %+v", mine)
	}
	first, err := e.Nodes.ListByVendorPage(ctx, vendorOne, "", 1)
	must(t, err)
	rest, err := e.Nodes.ListByVendorPage(ctx, vendorOne, first[0].ID, 5)
	must(t, err)
	if len(first) != 1 || first[0].ID != "n-0" || len(rest) != 1 || rest[0].ID != nodeOne {
		t.Fatalf("ListByVendorPage = %+v then %+v", first, rest)
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
	l, err := network.NewListing("lst-1", nodeOne, "item-1")
	must(t, err)
	must(t, e.Listings.Save(ctx, l))
	got, err := e.Listings.Get(ctx, "lst-1")
	must(t, err)
	if got != l {
		t.Fatalf("round trip: %+v", got)
	}
	l.NodeID = nodeTwo
	must(t, e.Listings.Save(ctx, l))
	got, _ = e.Listings.Get(ctx, "lst-1")
	if got.NodeID != nodeTwo {
		t.Fatalf("upsert replaced the row: %+v", got)
	}
}
