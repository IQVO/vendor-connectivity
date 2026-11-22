package http

// The replay classes (ADR 0015) without a database: the catalogue declares
// one per problem type, writeError tells the capturing writer, and the
// capture decides from it whether an answer is stored under its key.

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/claudioed/vendor-connectivity/internal/application/ports"
	"github.com/claudioed/vendor-connectivity/internal/domain/purchaseorder"
)

// wantClasses is the class table of ADR 0015, one row per problem type (slug).
// Changing a class means editing this table and the ADR together.
var wantClasses = map[string]replayClass{
	// A function of the request and of data this service never changes.
	"invalid-limit":                replayFinal,
	"invalid-cursor":               replayFinal,
	"partial-acknowledgement":      replayFinal,
	"ack-window-closed":            replayFinal,
	"submission-already-processed": replayFinal,
	// Nothing committed and the answer depends on state that can change.
	"validation-failed":            replayRetryable,
	"ack-window-out-of-range":      replayRetryable,
	"node-not-owned-by-vendor":     replayRetryable,
	"not-found":                    replayRetryable,
	"vendor-suspended":             replayRetryable,
	"already-acknowledged":         replayRetryable,
	"label-before-acceptance":      replayRetryable,
	"confirm-without-label":        replayRetryable,
	"cancel-after-acknowledgement": replayRetryable,
	"submission-not-due":           replayRetryable,
	"illegal-transition":           replayRetryable,
	"concurrent-update":            replayRetryable,
	"upstream-unavailable":         replayRetryable,
	"upstream-refused":             replayRetryable,
}

// Every problem type declares exactly one class and a 5xx is RETRYABLE (it is
// never stored). A new problem type is classified by the PR that adds it.
func TestEveryCatalogueEntryDeclaresAReplayClass(t *testing.T) {
	if len(problemCatalogue) == 0 {
		t.Fatal("the catalogue is empty: the test would pass on nothing")
	}
	for _, e := range problemCatalogue {
		t.Run(e.p.slug, func(t *testing.T) {
			if !e.replay.declared() {
				t.Errorf("%s (%d) declares no replay class (%s): set replayFinal, replayOutcome or replayRetryable on its catalogue entry", e.p.slug, e.p.status, e.replay)
			}
			if e.p.status >= http.StatusInternalServerError && e.replay != replayRetryable {
				t.Errorf("%s answers %d: a 5xx is never stored, so its class is RETRYABLE, got %s", e.p.slug, e.p.status, e.replay)
			}
		})
	}
}

// The check above must bite: the zero value and an unknown value are not classes.
func TestTheUnclassifiedEntryCheckBites(t *testing.T) {
	for _, c := range []replayClass{replayUnset, replayClass(4), replayClass(255)} {
		if c.declared() {
			t.Errorf("class %d is accepted; only FINAL, OUTCOME and RETRYABLE are classes", c)
		}
	}
	for _, c := range []replayClass{replayFinal, replayOutcome, replayRetryable} {
		if !c.declared() {
			t.Errorf("class %s is refused", c)
		}
	}
}

// One class per problem type: two catalogue entries of the same slug agree, so
// the table of the ADR (and what a client reads) is per slug.
func TestEverySlugHasOneClassAndItIsTheOneOfTheADR(t *testing.T) {
	seen := map[string]replayClass{}
	for _, e := range problemCatalogue {
		if prev, ok := seen[e.p.slug]; ok && prev != e.replay {
			t.Errorf("%s is declared %s and %s by two entries", e.p.slug, prev, e.replay)
		}
		seen[e.p.slug] = e.replay
	}
	for slug, want := range wantClasses {
		got, ok := seen[slug]
		if !ok {
			t.Errorf("%s is in the ADR table but not in the catalogue", slug)
			continue
		}
		if got != want {
			t.Errorf("%s is %s, the ADR table says %s", slug, got, want)
		}
	}
	for slug := range seen {
		if _, ok := wantClasses[slug]; !ok {
			t.Errorf("%s is in the catalogue but not in the ADR table: classify it there", slug)
		}
	}
}

// writeError tells the capturing writer the class of the problem it writes.
func TestWriteErrorMarksTheCaptureWithTheClassOfItsProblem(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want replayClass
	}{
		{"a retryable refusal", ports.ErrConcurrentUpdate, replayRetryable},
		{"a final refusal", purchaseorder.ErrAckWindowClosed, replayFinal},
		{"an unmapped error", errors.New("boom"), replayUnset},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := &capture{header: http.Header{}}
			writeError(c, httptest.NewRequest(http.MethodPost, "/x", nil), tc.err)
			if c.class != tc.want {
				t.Fatalf("class = %s, want %s", c.class, tc.want)
			}
		})
	}
	// A plain writer (a GET, the middlewares) is not a marker and still works.
	rec := httptest.NewRecorder()
	writeError(rec, httptest.NewRequest(http.MethodGet, "/x", nil), ports.ErrConcurrentUpdate)
	if rec.Code != http.StatusConflict {
		t.Fatalf("plain writer status = %d, want 409", rec.Code)
	}
}

// What the middleware stores: a 2xx, and a refusal whose class says so. A 5xx
// and a RETRYABLE or unclassified refusal are not; the key is released.
func TestOnlyASuccessAndAFinalOrOutcomeRefusalIsStored(t *testing.T) {
	cases := []struct {
		code  int
		class replayClass
		want  bool
	}{
		{http.StatusOK, replayUnset, true},
		{http.StatusAccepted, replayUnset, true},
		{http.StatusConflict, replayFinal, true},
		{http.StatusUnprocessableEntity, replayOutcome, true},
		{http.StatusConflict, replayRetryable, false},
		{http.StatusNotFound, replayRetryable, false},
		{http.StatusConflict, replayUnset, false},
		{http.StatusBadGateway, replayFinal, false},
		{http.StatusServiceUnavailable, replayRetryable, false},
		{http.StatusInternalServerError, replayUnset, false},
	}
	for _, tc := range cases {
		c := &capture{header: http.Header{}, code: tc.code}
		c.MarkReplay(tc.class)
		if got := c.stored(); got != tc.want {
			t.Errorf("%d %s: stored = %v, want %v", tc.code, tc.class, got, tc.want)
		}
	}
}
