package httpclient_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/claudioed/vendor-connectivity/internal/adapters/outbound/httpclient"
	"github.com/claudioed/vendor-connectivity/internal/application/gateway"
	"github.com/claudioed/vendor-connectivity/internal/application/requests"
)

// callee is one outbound client driven through its real HTTP code against a
// fake neighbour that answers a 4xx problem.
type callee struct {
	name string
	call func(url string) error
}

func callees() []callee {
	ctx := context.Background()
	return []callee{
		{"node-topology POST /nodes", func(url string) error {
			_, err := httpclient.NewNodeTopology(url, nil).RegisterNode(ctx, "tx-1", registration())
			return err
		}},
		{"node-topology PUT /nodes/{id}/operational-profile", func(url string) error {
			return httpclient.NewNodeTopology(url, nil).DeclareOperationalProfile(ctx, "tx-1", "n-1", requests.NodeProfile{})
		}},
		{"offer-availability POST /inventory-feeds", func(url string) error {
			return httpclient.NewOfferAvailability(url, nil).ApplyInventoryFeed(ctx, "tx-1", requests.InventoryFeed{})
		}},
		{"shipping-and-tracking POST /labels", func(url string) error {
			_, err := httpclient.NewShippingTracking(url, nil).IssueLabel(ctx, "vc-label-PO-1-1", gateway.LabelCall{})
			return err
		}},
	}
}

func refusalOf(t *testing.T, c callee, status int, problemType string) *gateway.RefusalError {
	t.Helper()
	srv, _ := neighbour(t, status, fmt.Sprintf(`{"type":%q,"title":"Refused","status":%d,"detail":"the detail of %s"}`, problemType, status, problemType))
	var ref *gateway.RefusalError
	if err := c.call(srv.URL); !errors.As(err, &ref) {
		t.Fatalf("%s answered %d %s: err = %v, want a *gateway.RefusalError", c.name, status, problemType, err)
	}
	return ref
}

// The mapping of each callee is its own table: the slug alone does not decide
// (validation-failed is FINAL in node-topology and RETRYABLE in offer-availability
// and shipping-and-tracking), and a slug of another callee means nothing here.
func TestRefusalsAreRetryableOnlyWhereTheCalleeReleasesThemUnderItsKey(t *testing.T) {
	nodeTopology := []callee{callees()[0], callees()[1]}
	offers := []callee{callees()[2]}
	shipping := []callee{callees()[3]}
	cases := []struct {
		callees []callee
		retry   []string
		final   []string
	}{
		{nodeTopology,
			[]string{"not-found", "illegal-transition", "concurrent-update", "incomplete-operational-profile", "serving-station-not-allowed",
				"lane-endpoint-not-active", "duplicate-lane", "not-a-delivery-station", "handoff-node-not-usable", "unknown-region"},
			[]string{"validation-failed", "vendor-node-code-taken", "stale-profile-revision", "node-retired", "operator-not-allowed-for-node-type",
				"subtype-not-allowed", "cutoff-outside-operating-hours", "malformed-request-body", "idempotency-key-reused",
				"address-suspect", "listing-not-on-node"}}, // the last two belong to other callees
		{offers,
			[]string{"listing-not-on-node", "concurrent-update", "pack-level-not-found", "node-not-sellable", "not-found", "illegal-transition",
				"pack-level-order", "invalid-pack-quantity", "invalid-pack-dimensions", "validation-failed"},
			[]string{"negative-availability", "feed-already-applied", "duplicate-listing", "gtin-already-assigned", "stale-availability",
				"malformed-request-body", "idempotency-key-reused", "unknown-region", "address-suspect"}},
		{shipping,
			[]string{"validation-failed", "address-suspect", "pickup-point-unavailable", "destination-prefix-unavailable", "tracking-number-taken", "concurrent-update"},
			[]string{"label-already-issued", "not-found", "void-after-handover", "journey-closed", "duplicate-scan", "scan-out-of-order",
				"illegal-transition", "idempotency-key-reused", "listing-not-on-node", "unknown-region"}},
	}
	for _, tc := range cases {
		for _, c := range tc.callees {
			for _, slug := range tc.retry {
				if ref := refusalOf(t, c, 409, slug); !ref.Retryable || ref.Slug != slug {
					t.Errorf("%s: %s = %+v, want retryable", c.name, slug, ref)
				}
			}
			for _, slug := range tc.final {
				if ref := refusalOf(t, c, 409, slug); ref.Retryable || ref.Slug != slug {
					t.Errorf("%s: %s = %+v, want terminal", c.name, slug, ref)
				}
			}
		}
	}
}

// A slug the table does not know is TERMINAL: an unknown refusal must never loop.
func TestAnUnknownRefusalIsTerminalWhateverTheStatus(t *testing.T) {
	for _, c := range callees() {
		for _, status := range []int{400, 404, 409, 422, 429} {
			ref := refusalOf(t, c, status, "a-slug-nobody-classified")
			if ref.Retryable || ref.Code != "ErrASlugNobodyClassified" || ref.Slug != "a-slug-nobody-classified" || ref.Status != status {
				t.Errorf("%s %d: %+v, want a terminal refusal", c.name, status, ref)
			}
		}
		html, _ := neighbour(t, 409, `<html>nope</html>`)
		var ref *gateway.RefusalError
		if err := c.call(html.URL); !errors.As(err, &ref) || ref.Retryable || ref.Slug != "" || ref.Code != "ErrRefused" {
			t.Errorf("%s: a 4xx without a problem body is terminal: %v", c.name, err)
		}
	}
}

// The slug may arrive as a URI whose last segment is the slug (the shape the
// neighbours' `type` takes in the spec examples); the table is keyed by the slug.
func TestARetryableSlugInURIFormIsStillRetryable(t *testing.T) {
	ref := refusalOf(t, callees()[0], 422, "https://retailnetwork.example/problems/unknown-region")
	if !ref.Retryable || ref.Slug != "unknown-region" || ref.Code != "ErrUnknownRegion" {
		t.Fatalf("ref = %+v", ref)
	}
}

// node-performance is read-only and takes no key: nothing it refuses is released
// for a retry, so its 4xx stays terminal.
func TestANodePerformanceRefusalIsNeverRetryable(t *testing.T) {
	srv, _ := neighbour(t, 409, `{"type":"concurrent-update","title":"x","status":409}`)
	_, err := httpclient.NewNodePerformance(srv.URL, nil).GetScorecard(context.Background(), "n-1")
	var ref *gateway.RefusalError
	if !errors.As(err, &ref) || ref.Retryable {
		t.Fatalf("err = %v", err)
	}
}
