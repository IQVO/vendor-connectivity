// Consumer contract tests for the offer-availability edge
// (OFFER_AVAILABILITY_URL). testdata/offer-availability.openapi.yaml is a byte
// copy of IQVO/offer-availability develop's apis/openapi.yaml. The producer
// spec carries no full feed examples: both fixtures are derived from its
// schemas (producer example backfill pending). R1 the route the gateway calls
// is declared, R2 the fixtures satisfy the producer's schemas, R3 the real
// gateway's request satisfies the request schema (the gateway does not decode
// the 201 answer today; the fixture pins the shape it would read) and a
// documented problem answer maps to its sentinel, R4 every field the request
// DTO writes is specified with a compatible type.
package httpclient

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/claudioed/vendor-connectivity/internal/application/gateway"
	"github.com/claudioed/vendor-connectivity/internal/application/requests"
)

const offerAvailabilitySpec = "offer-availability.openapi.yaml"

// R1: the pinned spec declares every (method, path) the gateway calls.
func TestOfferAvailabilitySpecCoversEveryCallSite(t *testing.T) {
	doc := loadPinnedSpec(t, offerAvailabilitySpec)
	for _, c := range []struct{ method, path string }{
		{http.MethodPost, "/inventory-feeds"},
	} {
		item := doc.Paths.Find(c.path)
		if item == nil {
			t.Errorf("pinned offer-availability spec has no path %s, the gateway calls it", c.path)
			continue
		}
		if item.Operations()[c.method] == nil {
			t.Errorf("pinned offer-availability spec path %s has no %s operation", c.path, c.method)
		}
	}
}

// R2: the fixtures satisfy the pinned schemas.
func TestOfferAvailabilityExamplesSatisfyPinnedSpec(t *testing.T) {
	doc := loadPinnedSpec(t, offerAvailabilitySpec)
	checkExampleSatisfies(t, doc, "ApplyInventoryFeedRequest", "inventory-feed-request.example.json")
	checkExampleSatisfies(t, doc, "InventoryFeed", "inventory-feed.example.json")
}

// R3: the request the REAL gateway sends satisfies the pinned
// ApplyInventoryFeedRequest schema; the 201 fixture is the answer shape the
// spec promises for it.
func TestOfferAvailabilityGatewayDecodesSpecExamples(t *testing.T) {
	doc := loadPinnedSpec(t, offerAvailabilitySpec)
	feedFixture := string(readExampleFixture(t, "inventory-feed.example.json"))
	srv, got := serveFixture(t, http.StatusCreated, "application/json", feedFixture)
	err := NewOfferAvailability(srv.URL, nil).ApplyInventoryFeed(context.Background(), "feed-042", inventoryFeed())
	if err != nil {
		t.Fatal(err)
	}
	if got.method != "POST" || got.path != "/inventory-feeds" || got.key != "feed-042" {
		t.Fatalf("request = %+v", got)
	}
	checkBodySatisfiesSchema(t, doc, "ApplyInventoryFeedRequest", got.body)
}

// TestOfferAvailabilityProblemMapsToItsSentinel: the 409 feed-already-applied
// answer the spec documents for POST /inventory-feeds decodes into a
// RefusalError carrying the Err name.
func TestOfferAvailabilityProblemMapsToItsSentinel(t *testing.T) {
	body := `{"type":"feed-already-applied","title":"Feed already applied","status":409,"detail":"feed feed-042 is already APPLIED"}`
	srv, _ := serveFixture(t, http.StatusConflict, "application/problem+json", body)
	err := NewOfferAvailability(srv.URL, nil).ApplyInventoryFeed(context.Background(), "feed-042", inventoryFeed())
	var ref *gateway.RefusalError
	if !errors.As(err, &ref) || ref.Status != 409 || ref.Code != "ErrFeedAlreadyApplied" {
		t.Fatalf("err = %v", err)
	}
}

// R4: every wire field the request DTO writes is declared by the pinned
// ApplyInventoryFeedRequest schema (FeedLine included, through lines).
func TestOfferAvailabilityFieldsWeReadAreSpecified(t *testing.T) {
	doc := loadPinnedSpec(t, offerAvailabilitySpec)
	checkDTOFields(t, typeOf[feedWire](), schemaOf(t, doc, "ApplyInventoryFeedRequest"), "ApplyInventoryFeedRequest", false)
}

func inventoryFeed() requests.InventoryFeed {
	return requests.InventoryFeed{VendorID: "V-1", NodeID: "n-9", FullUpdate: false, AsOf: time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC),
		Lines: []requests.InventoryLine{
			{ListingID: "lst-1", AvailableQty: 4, Basis: "PHYSICAL"},
			{ListingID: "lst-2", AvailableQty: 0, Basis: "THROUGHPUT_CONSTRAINED"},
		}}
}
