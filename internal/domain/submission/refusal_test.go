package submission

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/claudioed/vendor-connectivity/internal/domain/shared"
)

// A neighbour refusal the neighbour released (replay class RETRYABLE) leaves
// the submission PROCESSING: it counts as one attempt and the submission keeps
// the LAST refusal so the reason it is still pending can be read (ADR 0016).
func TestRecordRefusalCountsTheAttemptAndKeepsTheLastRefusal(t *testing.T) {
	s := received(t, KindInventory, 0)
	if _, ok := s.LastRefusal(); ok {
		t.Fatal("a new submission has no refusal")
	}
	first := Error{Code: "ErrListingNotOnNode", Message: "listing l-1 is not on node n-1", Path: "$.lines[0]"}
	n, err := s.RecordRefusal(first)
	if err != nil || n != 1 || s.Attempts() != 1 || s.Version() != 2 || s.Status() != Processing {
		t.Fatalf("first refusal: %d %v %+v", n, err, s.Snapshot())
	}
	if got, ok := s.LastRefusal(); !ok || got != first {
		t.Fatalf("last refusal = %+v %v, want %+v", got, ok, first)
	}
	second := Error{Code: "ErrConcurrentUpdate", Message: "another writer won"}
	if n, err := s.RecordRefusal(second); err != nil || n != 2 {
		t.Fatalf("second refusal: %d %v", n, err)
	}
	if got, _ := s.LastRefusal(); got != second {
		t.Fatalf("the LAST refusal wins, got %+v", got)
	}
	if got := s.Snapshot().LastRefusal; got != second {
		t.Fatalf("snapshot carries it: %+v", got)
	}
	if len(s.Errors()) != 0 {
		t.Fatalf("a pending submission has no outcome errors: %+v", s.Errors())
	}
}

// An outage after a refusal keeps the refusal: it is the last REFUSAL, not the
// last attempt.
func TestAnOutageAttemptKeepsTheLastRefusal(t *testing.T) {
	s := received(t, KindNodeRegistration, 0)
	refusal := Error{Code: "ErrUnknownRegion", Message: "region r-1 is not known"}
	_, _ = s.RecordRefusal(refusal)
	if _, err := s.RecordAttempt(); err != nil {
		t.Fatal(err)
	}
	if got, ok := s.LastRefusal(); !ok || got != refusal {
		t.Fatalf("the outage must not erase the refusal: %+v %v", got, ok)
	}
}

// The outcome supersedes the refusal: a SUCCESS or a FAILURE carries none.
func TestCompleteClearsTheLastRefusal(t *testing.T) {
	for name, outcome := range map[string]Outcome{
		"success": {Status: Success},
		"failure": {Status: Failure, Errors: []Error{{Code: "ErrUnknownRegion", Message: "region r-1 is not known"}}},
	} {
		s := received(t, KindNodeRegistration, 0)
		_, _ = s.RecordRefusal(Error{Code: "ErrUnknownRegion", Message: "region r-1 is not known"})
		if _, err := s.Complete(outcome, t0.Add(time.Second)); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if _, ok := s.LastRefusal(); ok || s.Snapshot().LastRefusal != (Error{}) {
			t.Errorf("%s: a processed submission keeps no pending refusal", name)
		}
	}
}

func TestRecordRefusalIsRefusedOnceProcessedAndAtTheCap(t *testing.T) {
	s := received(t, KindInventory, 0)
	if _, err := s.Complete(Outcome{Status: Success}, t0); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RecordRefusal(Error{Code: "ErrX", Message: "m"}); !errors.Is(err, ErrSubmissionAlreadyProcessed) {
		t.Fatalf("after the outcome: %v", err)
	}
	snap := received(t, KindInventory, 0).Snapshot()
	snap.Attempts = MaxAttempts
	capped, err := Restore(snap)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := capped.RecordRefusal(Error{Code: "ErrX", Message: "m"}); !errors.Is(err, shared.ErrValidation) || capped.Attempts() != MaxAttempts {
		t.Fatalf("cap: %v", err)
	}
	if _, ok := capped.LastRefusal(); ok {
		t.Fatal("a refused record leaves no refusal behind")
	}
}

// The neighbour's text is not ours: it is cleaned and clipped to the bounds of a
// SubmissionError instead of failing the pass (a control character or an over-long
// detail must never wedge a pending submission).
func TestRecordRefusalBoundsTheNeighboursText(t *testing.T) {
	s := received(t, KindInventory, 0)
	long := strings.Repeat("é", MaxMessage+50)
	_, err := s.RecordRefusal(Error{Code: strings.Repeat("E", MaxCode+5), Message: "line one\nline two\t " + long, Path: strings.Repeat("p", MaxPath+1)})
	if err != nil {
		t.Fatal(err)
	}
	got, _ := s.LastRefusal()
	if len([]rune(got.Code)) != MaxCode || len([]rune(got.Message)) != MaxMessage || len([]rune(got.Path)) != MaxPath {
		t.Fatalf("lengths = %d %d %d", len([]rune(got.Code)), len([]rune(got.Message)), len([]rune(got.Path)))
	}
	if !strings.HasPrefix(got.Message, "line one line two") {
		t.Fatalf("control characters become spaces: %q", got.Message)
	}
	if _, err := Restore(s.Snapshot()); err != nil {
		t.Fatalf("what is recorded must restore: %v", err)
	}
	empty := received(t, KindInventory, 0)
	if _, err := empty.RecordRefusal(Error{Code: "  ", Message: ""}); err != nil {
		t.Fatal(err)
	}
	got, _ = empty.LastRefusal()
	if got.Code != "ErrRefused" || got.Message != "the neighbour refused the request" {
		t.Fatalf("an empty refusal keeps the generic words: %+v", got)
	}
}

func TestLastRefusalSurvivesTheSnapshotAndIsCheckedOnRestore(t *testing.T) {
	s := received(t, KindInventory, 0)
	_, _ = s.RecordRefusal(Error{Code: "ErrNotFound", Message: "node n-1 is not known", Path: "$.nodeId"})
	r, err := Restore(s.Snapshot())
	if err != nil || !reflect.DeepEqual(r.Snapshot(), s.Snapshot()) {
		t.Fatalf("round trip: %v %+v", err, r)
	}
	pending := s.Snapshot()
	processed := received(t, KindInventory, 0)
	_, _ = processed.Complete(Outcome{Status: Success}, t0)
	bad := map[string]Snapshot{}
	withOutcome := processed.Snapshot()
	withOutcome.LastRefusal = Error{Code: "ErrX", Message: "m"}
	bad["a processed submission has no pending refusal"] = withOutcome
	unbounded := pending
	unbounded.LastRefusal = Error{Code: "ErrX", Message: strings.Repeat("m", MaxMessage+1)}
	bad["unbounded text"] = unbounded
	half := pending
	half.LastRefusal = Error{Code: "ErrX"}
	bad["a refusal needs a code and a message"] = half
	for name, snap := range bad {
		if _, err := Restore(snap); !errors.Is(err, shared.ErrInvalidSnapshot) {
			t.Errorf("%s: Restore = %v, want corrupt", name, err)
		}
	}
}
