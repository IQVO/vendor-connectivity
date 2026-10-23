package network

import (
	"errors"
	"testing"

	"github.com/claudioed/vendor-connectivity/internal/domain/shared"
)

func vendorNode() Registered {
	return Registered{NodeID: "n-1", NodeType: TypeFulfillmentCenter, Subtype: "SORTABLE", Operator: OperatorVendor,
		VendorID: "V-WS", VendorNodeCode: "SITE-01", RegionID: "r-se", Timezone: "America/Sao_Paulo"}
}

func TestVendorMayRegister(t *testing.T) {
	for typ, want := range map[string]bool{TypeFulfillmentCenter: true, TypeInboundCrossDock: true, TypeReturnsCenter: true,
		"SORTATION_CENTER": false, "DELIVERY_STATION": false, "AIR_HUB": false, "": false} {
		if VendorMayRegister(typ) != want {
			t.Errorf("VendorMayRegister(%q) != %v", typ, want)
		}
	}
}

func TestOnRegisteredBuildsAndMerges(t *testing.T) {
	n, err := Node{}.OnRegistered(vendorNode())
	if err != nil {
		t.Fatal(err)
	}
	want := Node{ID: "n-1", Operator: OperatorVendor, VendorID: "V-WS", VendorNodeCode: "SITE-01", NodeType: TypeFulfillmentCenter,
		Subtype: "SORTABLE", RegionID: "r-se", Timezone: "America/Sao_Paulo", Status: StatusProposed}
	if n != want {
		t.Fatalf("node = %+v", n)
	}
	if !n.VendorOperated() {
		t.Fatal("vendor node must be vendor operated")
	}
	// A stub created by a later event keeps its status and profile when the registration arrives.
	stub, _ := Node{}.OnStatus("n-1", StatusActive)
	stub, _ = stub.OnProfileDeclared("n-1", 2, 90)
	merged, err := stub.OnRegistered(vendorNode())
	if err != nil || merged.Status != StatusActive || merged.ProfileRevision != 2 || merged.HandlingTimeMinutes != 90 || merged.VendorID != "V-WS" {
		t.Fatalf("merged = %+v %v", merged, err)
	}
	net, err := Node{}.OnRegistered(Registered{NodeID: "n-2", NodeType: "DELIVERY_STATION", Operator: OperatorNetwork})
	if err != nil || net.VendorOperated() || net.Status != StatusProposed {
		t.Fatalf("network node = %+v %v", net, err)
	}
}

func TestOnRegisteredRejectsBadFacts(t *testing.T) {
	tests := map[string]func(*Registered){
		"bad id":         func(r *Registered) { r.NodeID = "a b" },
		"unknown op":     func(r *Registered) { r.Operator = OperatorUnknown },
		"vendor no id":   func(r *Registered) { r.VendorID = "" },
		"vendor no code": func(r *Registered) { r.VendorNodeCode = "" },
		"odd operator":   func(r *Registered) { r.Operator = "OTHER" },
	}
	for name, mut := range tests {
		r := vendorNode()
		mut(&r)
		if _, err := (Node{}).OnRegistered(r); !errors.Is(err, shared.ErrValidation) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

func TestOnProfileDeclaredNeverLowersTheRevision(t *testing.T) {
	n, _ := Node{}.OnRegistered(vendorNode())
	tests := []struct {
		rev, handling, wantRev, wantHandling int
	}{{1, 60, 1, 60}, {3, 90, 3, 90}, {2, 15, 3, 90}, {3, 15, 3, 90}, {4, 0, 4, 0}}
	for _, tc := range tests {
		var err error
		n, err = n.OnProfileDeclared("n-1", tc.rev, tc.handling)
		if err != nil || n.ProfileRevision != tc.wantRev || n.HandlingTimeMinutes != tc.wantHandling {
			t.Fatalf("after rev %d: %+v %v", tc.rev, n, err)
		}
	}
	for name, args := range map[string][3]any{"id": {"a b", 1, 1}, "rev": {"n-1", 0, 1}, "handling": {"n-1", 1, -1}} {
		if _, err := n.OnProfileDeclared(args[0].(string), args[1].(int), args[2].(int)); !errors.Is(err, shared.ErrValidation) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

func TestOnStatusRetiredIsFinal(t *testing.T) {
	n, _ := Node{}.OnRegistered(vendorNode())
	for _, st := range []Status{StatusActive, StatusSuspended, StatusActive, StatusRetired, StatusActive, StatusSuspended} {
		var err error
		n, err = n.OnStatus("n-1", st)
		if err != nil {
			t.Fatal(err)
		}
		wantStatus := st
		if n.Status == StatusRetired {
			wantStatus = StatusRetired
		}
		if n.Status != wantStatus {
			t.Fatalf("status = %s after %s", n.Status, st)
		}
	}
	if n.Status != StatusRetired {
		t.Fatalf("final status = %s", n.Status)
	}
	for _, bad := range []Status{"", StatusProposed, "GONE"} {
		if _, err := n.OnStatus("n-1", bad); !errors.Is(err, shared.ErrValidation) {
			t.Errorf("status %q: err = %v", bad, err)
		}
	}
	if _, err := n.OnStatus("a b", StatusActive); !errors.Is(err, shared.ErrValidation) {
		t.Errorf("bad id: %v", err)
	}
}

func TestEnsureOwnedBy(t *testing.T) {
	n, _ := Node{}.OnRegistered(vendorNode())
	if err := n.EnsureOwnedBy("V-WS"); err != nil {
		t.Fatalf("owner: %v", err)
	}
	for _, v := range []string{"", "V-OTHER", "v-ws"} {
		if err := n.EnsureOwnedBy(v); !errors.Is(err, ErrNodeNotOwnedByVendor) {
			t.Errorf("vendor %q: err = %v", v, err)
		}
	}
	net, _ := Node{}.OnRegistered(Registered{NodeID: "n-2", Operator: OperatorNetwork})
	if err := net.EnsureOwnedBy("V-WS"); !errors.Is(err, ErrNodeNotOwnedByVendor) {
		t.Errorf("network node: %v", err)
	}
	var stub Node
	if err := stub.EnsureOwnedBy(""); !errors.Is(err, ErrNodeNotOwnedByVendor) {
		t.Errorf("zero node: %v", err)
	}
}

func TestNewListing(t *testing.T) {
	l, err := NewListing("lst-1", "n-1", "item-9")
	if err != nil || l != (Listing{ID: "lst-1", NodeID: "n-1", CatalogItemID: "item-9"}) {
		t.Fatalf("listing = %+v %v", l, err)
	}
	if _, err := NewListing("", "n-1", ""); !errors.Is(err, shared.ErrValidation) {
		t.Errorf("blank listing id: %v", err)
	}
	if _, err := NewListing("lst-1", "x y", ""); !errors.Is(err, shared.ErrValidation) {
		t.Errorf("bad node id: %v", err)
	}
}
