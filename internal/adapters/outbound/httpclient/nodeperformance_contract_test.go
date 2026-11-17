// Consumer contract tests for the node-performance edge
// (NODE_PERFORMANCE_URL). testdata/node-performance.openapi.yaml is a byte
// copy of IQVO/node-performance develop's apis/openapi.yaml;
// scorecards.example.json is the spec's own NodeScorecards example. R1 the
// route the gateway calls is declared, R2 the fixture satisfies the
// producer's schema, R3 the real gateway decodes the fixture into the domain
// (a documented problem answer maps to its sentinel), R4 every field the
// response DTO reads is specified with a compatible type.
package httpclient

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/claudioed/vendor-connectivity/internal/application/gateway"
	"github.com/claudioed/vendor-connectivity/internal/application/views"
)

const nodePerformanceSpec = "node-performance.openapi.yaml"

// R1: the pinned spec declares every (method, path) the gateway calls.
func TestNodePerformanceSpecCoversEveryCallSite(t *testing.T) {
	doc := loadPinnedSpec(t, nodePerformanceSpec)
	for _, c := range []struct{ method, path string }{
		{http.MethodGet, "/scorecards/{nodeId}"},
	} {
		item := doc.Paths.Find(c.path)
		if item == nil {
			t.Errorf("pinned node-performance spec has no path %s, the gateway calls it", c.path)
			continue
		}
		if item.Operations()[c.method] == nil {
			t.Errorf("pinned node-performance spec path %s has no %s operation", c.path, c.method)
		}
	}
}

// R2: the spec's own NodeScorecards example, as a fixture, satisfies the
// pinned NodeScorecards schema.
func TestNodePerformanceExamplesSatisfyPinnedSpec(t *testing.T) {
	doc := loadPinnedSpec(t, nodePerformanceSpec)
	checkExampleSatisfies(t, doc, "NodeScorecards", "scorecards.example.json")
}

// R3: the fixture flows through the REAL gateway call into the domain
// scorecard windows.
func TestNodePerformanceGatewayDecodesSpecExamples(t *testing.T) {
	scorecardsFixture := string(readExampleFixture(t, "scorecards.example.json"))
	srv, got := serveFixture(t, http.StatusOK, "application/json", scorecardsFixture)
	sc, err := NewNodePerformance(srv.URL, nil).GetScorecard(context.Background(), "N-0042")
	if err != nil {
		t.Fatal(err)
	}
	if got.method != "GET" || got.path != "/scorecards/N-0042" || got.key != "" {
		t.Fatalf("request = %+v", got)
	}
	if sc.NodeID != "N-0042" || len(sc.Windows) != 2 {
		t.Fatalf("scorecard = %+v", sc)
	}
	assertGradedCWindow(t, sc.Windows[0])
	assertGradedBWindow(t, sc.Windows[1])
}

func assertGradedCWindow(t *testing.T, w views.ScorecardWindow) {
	t.Helper()
	if w.Window != "7d" || w.Grade != "C" || w.Observations != 412 || w.OnTimeHandoverRate == nil || *w.OnTimeHandoverRate != 0.93 ||
		w.FeedFreshnessP95Minutes == nil || *w.FeedFreshnessP95Minutes != 55 {
		t.Fatalf("window 0 = %+v", w)
	}
	if want := time.Date(2026, 10, 22, 14, 3, 0, 0, time.UTC); !w.ComputedAt.Equal(want) {
		t.Fatalf("computedAt = %v, want %v", w.ComputedAt, want)
	}
}

func assertGradedBWindow(t *testing.T, p views.ScorecardWindow) {
	t.Helper()
	if p.Window != "30d" || p.Grade != "B" || p.Observations != 1874 || p.OnTimeHandoverRate == nil || *p.OnTimeHandoverRate != 0.962 || p.ComputedAt.IsZero() {
		t.Fatalf("window 1 = %+v", p)
	}
}

// TestNodePerformanceProblemMapsToItsSentinel: the 404 not-found example body
// the spec publishes for GET /scorecards/{nodeId} decodes into a RefusalError
// carrying the Err name.
func TestNodePerformanceProblemMapsToItsSentinel(t *testing.T) {
	body := `{"type":"not-found","title":"Not found","status":404,"detail":"not found: no scorecard for node N-0042"}`
	srv, _ := serveFixture(t, http.StatusNotFound, "application/problem+json", body)
	_, err := NewNodePerformance(srv.URL, nil).GetScorecard(context.Background(), "N-0042")
	var ref *gateway.RefusalError
	if !errors.As(err, &ref) || ref.Status != 404 || ref.Code != "ErrNotFound" {
		t.Fatalf("err = %v", err)
	}
}

// R4: every wire field the response DTO reads is declared by the pinned
// Scorecard schema with a compatible type. The GET envelope is an inline
// struct in the gateway, so its two wire fields are checked by name against
// NodeScorecards (both required there).
func TestNodePerformanceFieldsWeReadAreSpecified(t *testing.T) {
	doc := loadPinnedSpec(t, nodePerformanceSpec)
	checkDTOFields(t, typeOf[scorecardWire](), schemaOf(t, doc, "Scorecard"), "Scorecard", true)
	env := schemaOf(t, doc, "NodeScorecards")
	_, required := flattenSchema(env)
	for _, name := range []string{"nodeId", "scorecards"} {
		if env.Properties[name] == nil {
			t.Errorf("NodeScorecards does not declare %s, the gateway decodes it", name)
		}
		if !required[name] {
			t.Errorf("NodeScorecards no longer requires %s; absence handling needs a look", name)
		}
	}
}
