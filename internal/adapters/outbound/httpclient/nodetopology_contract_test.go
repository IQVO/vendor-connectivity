// Consumer contract tests for the node-topology edge (NODE_TOPOLOGY_URL).
// testdata/node-topology.openapi.yaml is a byte copy of IQVO/node-topology
// develop's apis/openapi.yaml. node.example.json is derived from the Node
// schema (the producer spec carries no full Node example; backfill pending);
// profile-declaration.example.json is the spec's own withoutInboundLimits
// request example. R1 the routes the gateway calls are declared, R2 the
// fixtures satisfy the producer's schemas, R3 the real gateway's requests
// satisfy the request schemas and it decodes the fixture into the domain (a
// documented problem answer maps to its sentinel), R4 every field the DTOs
// carry is specified with a compatible type.
package httpclient

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/claudioed/vendor-connectivity/internal/application/gateway"
	"github.com/claudioed/vendor-connectivity/internal/application/requests"
)

const nodeTopologySpec = "node-topology.openapi.yaml"

// R1: the pinned spec declares every (method, path) the gateway calls.
func TestNodeTopologySpecCoversEveryCallSite(t *testing.T) {
	doc := loadPinnedSpec(t, nodeTopologySpec)
	for _, c := range []struct{ method, path string }{
		{http.MethodPost, "/nodes"},
		{http.MethodPut, "/nodes/{nodeId}/operational-profile"},
	} {
		item := doc.Paths.Find(c.path)
		if item == nil {
			t.Errorf("pinned node-topology spec has no path %s, the gateway calls it", c.path)
			continue
		}
		if item.Operations()[c.method] == nil {
			t.Errorf("pinned node-topology spec path %s has no %s operation", c.path, c.method)
		}
	}
}

// R2: the fixtures satisfy the pinned schemas.
func TestNodeTopologyExamplesSatisfyPinnedSpec(t *testing.T) {
	doc := loadPinnedSpec(t, nodeTopologySpec)
	checkExampleSatisfies(t, doc, "Node", "node.example.json")
	checkExampleSatisfies(t, doc, "OperationalProfileDeclaration", "profile-declaration.example.json")
}

// R3: the fixtures flow through the REAL gateway calls into the domain, the
// requests the gateway sends satisfy the pinned request schemas, and a
// documented problem answer becomes the gateway's sentinel error.
func TestNodeTopologyGatewayDecodesSpecExamples(t *testing.T) {
	doc := loadPinnedSpec(t, nodeTopologySpec)
	nodeFixture := string(readExampleFixture(t, "node.example.json"))
	srv, got := serveFixture(t, http.StatusCreated, "application/json", nodeFixture)
	node, err := NewNodeTopology(srv.URL, nil).RegisterNode(context.Background(), "vc-node-V-1-SITE-1", nodeRegistration())
	if err != nil {
		t.Fatal(err)
	}
	assertRegisteredNode(t, node)
	if got.method != "POST" || got.path != "/nodes" || got.key != "vc-node-V-1-SITE-1" {
		t.Fatalf("request = %+v", got)
	}
	checkBodySatisfiesSchema(t, doc, "RegisterNodeRequest", got.body)

	profileSrv, profileGot := serveFixture(t, http.StatusOK, "application/json", nodeFixture)
	if err := NewNodeTopology(profileSrv.URL, nil).DeclareOperationalProfile(context.Background(), "vc-profile-n-9-5", "n-9", nodeProfile()); err != nil {
		t.Fatal(err)
	}
	if profileGot.method != "PUT" || profileGot.path != "/nodes/n-9/operational-profile" || profileGot.key != "vc-profile-n-9-5" {
		t.Fatalf("request = %+v", profileGot)
	}
	checkBodySatisfiesSchema(t, doc, "OperationalProfileDeclaration", profileGot.body)
}

// TestNodeTopologyProblemMapsToItsSentinel: the 409 stale-profile-revision
// answer the spec documents for PUT .../operational-profile (and whose slug
// the Problem schema itself uses as example) decodes into a RefusalError
// carrying the Err name.
func TestNodeTopologyProblemMapsToItsSentinel(t *testing.T) {
	body := `{"type":"stale-profile-revision","title":"Stale profile revision","status":409,"detail":"revision 3 is not newer than 4"}`
	srv, _ := serveFixture(t, http.StatusConflict, "application/problem+json", body)
	err := NewNodeTopology(srv.URL, nil).DeclareOperationalProfile(context.Background(), "k", "n-9", nodeProfile())
	var ref *gateway.RefusalError
	if !errors.As(err, &ref) || ref.Status != 409 || ref.Code != "ErrStaleProfileRevision" || ref.Message != "revision 3 is not newer than 4" {
		t.Fatalf("err = %v", err)
	}
}

// R4: every wire field the response DTO reads is declared by the pinned Node
// schema, and every field the request DTOs write is declared by their schemas.
func TestNodeTopologyFieldsWeReadAreSpecified(t *testing.T) {
	doc := loadPinnedSpec(t, nodeTopologySpec)
	checkDTOFields(t, typeOf[nodeWire](), schemaOf(t, doc, "Node"), "Node", true)
	checkDTOFields(t, typeOf[registerNodeWire](), schemaOf(t, doc, "RegisterNodeRequest"), "RegisterNodeRequest", false)
	checkDTOFields(t, typeOf[profileWire](), schemaOf(t, doc, "OperationalProfileDeclaration"), "OperationalProfileDeclaration", false)
}

// assertRegisteredNode checks the fixture decoded through the REAL gateway
// call against the domain values the node.example.json fixture carries.
func assertRegisteredNode(t *testing.T, node gateway.RegisteredNode) {
	t.Helper()
	want := gateway.RegisteredNode{NodeID: "n-9", NodeType: "FULFILLMENT_CENTER", Subtype: "XL", Status: "PROPOSED",
		VendorID: "V-1", VendorNodeCode: "SITE-1", RegionID: "r-1", Timezone: "America/Sao_Paulo"}
	if node != want {
		t.Fatalf("node = %+v, want %+v", node, want)
	}
}

func nodeRegistration() requests.NodeRegistration {
	lat, lon := -23.5, -46.6
	return requests.NodeRegistration{VendorID: "V-1", VendorNodeCode: "SITE-1", NodeType: "FULFILLMENT_CENTER", Subtype: "XL",
		RegionID: "r-1", Timezone: "America/Sao_Paulo", PostalCode: "07750-000", CountryCode: "BR", Lat: &lat, Lon: &lon}
}

func nodeProfile() requests.NodeProfile {
	max := 5
	return requests.NodeProfile{VendorID: "V-1", ProfileRevision: 5, Timezone: "America/Sao_Paulo",
		OperatingHoursByDay: map[string]requests.DayHours{"monday": {Start: "06:00", End: "22:00"}},
		Holidays:            []string{"2026-12-25"}, HandlingTimeMinutes: 240,
		Cutoffs:       []requests.Cutoff{{LocalTime: "11:00", MaxOrders: &max}},
		ThroughputCap: requests.ThroughputCap{Value: 400, TimeUnit: "HOUR", Unit: "ORDER"}}
}
