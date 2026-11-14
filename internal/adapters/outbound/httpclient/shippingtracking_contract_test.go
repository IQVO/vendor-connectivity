// Consumer contract tests for the shipping-and-tracking edge
// (SHIPPING_AND_TRACKING_URL). testdata/shipping-and-tracking.openapi.yaml is
// a byte copy of IQVO/shipping-and-tracking develop's apis/openapi.yaml. That
// spec carries ZERO examples: label.example.json is derived from the Label
// schema (producer example backfill pending). R1 the route the gateway calls
// is declared, R2 the fixture satisfies the producer's schema, R3 the real
// gateway's request satisfies the request schema and it decodes the fixture
// into the domain (a documented problem answer maps to its sentinel), R4
// every field the DTOs carry is specified with a compatible type.
package httpclient

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/claudioed/vendor-connectivity/internal/application/gateway"
)

const shippingTrackingSpec = "shipping-and-tracking.openapi.yaml"

// R1: the pinned spec declares every (method, path) the gateway calls.
func TestShippingTrackingSpecCoversEveryCallSite(t *testing.T) {
	doc := loadPinnedSpec(t, shippingTrackingSpec)
	for _, c := range []struct{ method, path string }{
		{http.MethodPost, "/labels"},
	} {
		item := doc.Paths.Find(c.path)
		if item == nil {
			t.Errorf("pinned shipping-and-tracking spec has no path %s, the gateway calls it", c.path)
			continue
		}
		if item.Operations()[c.method] == nil {
			t.Errorf("pinned shipping-and-tracking spec path %s has no %s operation", c.path, c.method)
		}
	}
}

// R2: the fixture satisfies the pinned Label schema of the POST /labels
// answer.
func TestShippingTrackingExamplesSatisfyPinnedSpec(t *testing.T) {
	doc := loadPinnedSpec(t, shippingTrackingSpec)
	checkExampleSatisfies(t, doc, "Label", "label.example.json")
}

// R3: the fixture flows through the REAL gateway call into the domain, and
// the request the gateway sends satisfies the pinned IssueLabelRequest
// schema (the producer would accept it).
func TestShippingTrackingGatewayDecodesSpecExamples(t *testing.T) {
	doc := loadPinnedSpec(t, shippingTrackingSpec)
	labelFixture := string(readExampleFixture(t, "label.example.json"))
	srv, got := serveFixture(t, http.StatusCreated, "application/json", labelFixture)
	label, err := NewShippingTracking(srv.URL, nil).IssueLabel(context.Background(), "vc-label-PO-1-2",
		gateway.LabelCall{PONumber: "PO-1", PackageNo: 2, AssignmentID: "asg-1", OrderID: "ord-1", Tier: "NEXT_DAY"})
	if err != nil {
		t.Fatal(err)
	}
	if label.LabelRef != "lbl-1" || label.TrackingNumber != "TRK-1" || label.Carrier != "carrier-a" {
		t.Fatalf("label = %+v", label)
	}
	if got.method != "POST" || got.path != "/labels" || got.key != "vc-label-PO-1-2" {
		t.Fatalf("request = %+v", got)
	}
	checkBodySatisfiesSchema(t, doc, "IssueLabelRequest", got.body)
}

// TestShippingTrackingProblemMapsToItsSentinel: the 409 label-already-issued
// answer the spec documents for POST /labels (and whose slug the Problem
// schema itself uses as example) decodes into a RefusalError carrying the Err
// name.
func TestShippingTrackingProblemMapsToItsSentinel(t *testing.T) {
	body := `{"type":"label-already-issued","title":"Label already issued","status":409,"detail":"a label for PO-1 package 2 already exists"}`
	srv, _ := serveFixture(t, http.StatusConflict, "application/problem+json", body)
	_, err := NewShippingTracking(srv.URL, nil).IssueLabel(context.Background(), "k",
		gateway.LabelCall{PONumber: "PO-1", PackageNo: 2, AssignmentID: "asg-1", OrderID: "ord-1", Tier: "NEXT_DAY"})
	var ref *gateway.RefusalError
	if !errors.As(err, &ref) || ref.Status != 409 || ref.Code != "ErrLabelAlreadyIssued" {
		t.Fatalf("err = %v", err)
	}
}

// R4: every wire field the response DTO reads is declared by the pinned Label
// schema, and every field the request DTO writes is declared by
// IssueLabelRequest.
func TestShippingTrackingFieldsWeReadAreSpecified(t *testing.T) {
	doc := loadPinnedSpec(t, shippingTrackingSpec)
	checkDTOFields(t, typeOf[labelWire](), schemaOf(t, doc, "Label"), "Label", true)
	checkDTOFields(t, typeOf[issueLabelWire](), schemaOf(t, doc, "IssueLabelRequest"), "IssueLabelRequest", false)
}
