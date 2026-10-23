package submission

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/claudioed/vendor-connectivity/internal/domain/shared"
)

var t0 = time.Date(2026, 10, 22, 13, 0, 0, 0, time.UTC)

func received(t *testing.T, kind Kind, delay time.Duration) *Submission {
	t.Helper()
	s, err := Receive(NewSubmission{ID: "tx-1", VendorID: "V-WS", Kind: kind, Payload: []byte(`{"vendorId":"V-WS"}`), PONumber: "PO-1", NodeID: "n-1"}, t0, delay)
	if err != nil {
		t.Fatalf("Receive: %v", err)
	}
	return s
}

func TestReceiveStartsProcessingWithNotBefore(t *testing.T) {
	s := received(t, KindAcknowledgement, 3*time.Second)
	want := Snapshot{ID: "tx-1", VendorID: "V-WS", Kind: KindAcknowledgement, Status: Processing, Payload: []byte(`{"vendorId":"V-WS"}`),
		PONumber: "PO-1", NodeID: "n-1", ReceivedAt: t0, NotBefore: t0.Add(3 * time.Second), Version: 1}
	if got := s.Snapshot(); !reflect.DeepEqual(got, want) {
		t.Fatalf("snapshot:\n got %+v\nwant %+v", got, want)
	}
	p := s.Payload()
	p[0] = 'X'
	if s.Payload()[0] != '{' {
		t.Fatal("Payload must return a copy")
	}
	if s.ProcessedAt() != (time.Time{}) || len(s.Errors()) != 0 || s.ResultNodeID() != "" || s.Attempts() != 0 {
		t.Fatal("a new submission has no outcome")
	}
}

func TestReceiveRejectsBadInput(t *testing.T) {
	base := NewSubmission{ID: "tx-1", VendorID: "V-WS", Kind: KindInventory, Payload: []byte(`{}`)}
	tests := map[string]func(*NewSubmission){
		"bad id":     func(n *NewSubmission) { n.ID = "" },
		"bad vendor": func(n *NewSubmission) { n.VendorID = "a b" },
		"bad kind":   func(n *NewSubmission) { n.Kind = "OTHER" },
		"no payload": func(n *NewSubmission) { n.Payload = nil },
		"not json":   func(n *NewSubmission) { n.Payload = []byte(`{`) },
		"huge":       func(n *NewSubmission) { n.Payload = []byte(`"` + strings.Repeat("a", MaxPayload) + `"`) },
		"bad po":     func(n *NewSubmission) { n.PONumber = "a b" },
		"bad node":   func(n *NewSubmission) { n.NodeID = "a b" },
	}
	for name, mut := range tests {
		in := base
		mut(&in)
		if _, err := Receive(in, t0, 0); !errors.Is(err, shared.ErrValidation) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	if _, err := Receive(base, t0, -time.Second); !errors.Is(err, shared.ErrValidation) {
		t.Errorf("negative delay: %v", err)
	}
	if _, err := Receive(base, time.Time{}, 0); !errors.Is(err, shared.ErrValidation) {
		t.Errorf("zero clock: %v", err)
	}
	for _, kind := range []Kind{KindAcknowledgement, KindInventory, KindNodeRegistration, KindNodeProfile, KindShipmentConfirmation} {
		in := base
		in.Kind = kind
		if _, err := Receive(in, t0, 0); err != nil {
			t.Errorf("kind %s: %v", kind, err)
		}
	}
}

func TestCompleteTransitions(t *testing.T) {
	tests := []struct {
		name    string
		kind    Kind
		outcome Outcome
		wantErr bool
		count   int
	}{
		{"success", KindAcknowledgement, Outcome{Status: Success}, false, 0},
		{"success registration with node", KindNodeRegistration, Outcome{Status: Success, NodeID: "n-9"}, false, 0},
		{"failure one error", KindInventory, Outcome{Status: Failure, Errors: []Error{{Code: "ErrX", Message: "m"}}}, false, 1},
		{"failure two errors", KindInventory, Outcome{Status: Failure, Errors: []Error{{Code: "ErrX", Message: "m", Path: "$.a"}, {Code: "ErrY", Message: "n"}}}, false, 2},
		{"failure without error", KindInventory, Outcome{Status: Failure}, true, 0},
		{"success with error", KindInventory, Outcome{Status: Success, Errors: []Error{{Code: "ErrX", Message: "m"}}}, true, 0},
		{"processing outcome", KindInventory, Outcome{Status: Processing}, true, 0},
		{"unknown status", KindInventory, Outcome{Status: "DONE"}, true, 0},
		{"node id on failure", KindNodeRegistration, Outcome{Status: Failure, NodeID: "n-9", Errors: []Error{{Code: "ErrX", Message: "m"}}}, true, 0},
		{"node id on wrong kind", KindInventory, Outcome{Status: Success, NodeID: "n-9"}, true, 0},
		{"blank code", KindInventory, Outcome{Status: Failure, Errors: []Error{{Code: "", Message: "m"}}}, true, 0},
		{"blank message", KindInventory, Outcome{Status: Failure, Errors: []Error{{Code: "ErrX"}}}, true, 0},
		{"bad path", KindInventory, Outcome{Status: Failure, Errors: []Error{{Code: "ErrX", Message: "m", Path: " $"}}}, true, 0},
	}
	for _, tc := range tests {
		s := received(t, tc.kind, 0)
		evs, err := s.Complete(tc.outcome, t0.Add(time.Second))
		if tc.wantErr {
			if !errors.Is(err, shared.ErrValidation) || s.Status() != Processing || s.Version() != 1 {
				t.Errorf("%s: err = %v status %s v%d", tc.name, err, s.Status(), s.Version())
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: %v", tc.name, err)
			continue
		}
		assertProcessed(t, s, evs, tc.outcome.Status, tc.count)
	}
}

func assertProcessed(t *testing.T, s *Submission, evs []shared.Event, want Status, count int) {
	t.Helper()
	if s.Status() != want || s.Version() != 2 || !s.ProcessedAt().Equal(t0.Add(time.Second)) || len(s.Errors()) != count {
		t.Fatalf("submission = %+v", s.Snapshot())
	}
	if len(evs) != 1 {
		t.Fatalf("events = %v", evs)
	}
	ev := evs[0].(SubmissionProcessed)
	if ev.EventName() != "SubmissionProcessed" || ev.Entity() != "submission" || ev.SubjectID() != "tx-1" || ev.AggregateVersion() != 2 ||
		!ev.OccurredAt().Equal(t0.Add(time.Second)) || ev.VendorID != "V-WS" || ev.Status != want || ev.ErrorCount != count || ev.Kind != s.Kind() {
		t.Fatalf("event = %+v", ev)
	}
}

func TestCompleteOnceAndNotBeforeNotDue(t *testing.T) {
	s := received(t, KindAcknowledgement, time.Minute)
	if s.Due(t0.Add(time.Minute-time.Nanosecond)) || !s.Due(t0.Add(time.Minute)) {
		t.Fatal("Due must be inclusive of notBefore")
	}
	if _, err := s.Complete(Outcome{Status: Success}, t0.Add(time.Minute-time.Nanosecond)); !errors.Is(err, ErrSubmissionNotDue) {
		t.Fatalf("before notBefore: %v", err)
	}
	if _, err := s.Complete(Outcome{Status: Success}, t0.Add(time.Minute)); err != nil {
		t.Fatalf("at notBefore: %v", err)
	}
	for _, o := range []Outcome{{Status: Success}, {Status: Failure, Errors: []Error{{Code: "E", Message: "m"}}}} {
		if _, err := s.Complete(o, t0.Add(time.Hour)); !errors.Is(err, ErrSubmissionAlreadyProcessed) {
			t.Fatalf("second outcome: %v", err)
		}
	}
	if s.Status() != Success || s.Version() != 2 {
		t.Fatalf("outcome must stay: %+v", s.Snapshot())
	}
	// A processed submission checks "already processed" before "not due".
	if _, err := s.Complete(Outcome{Status: Success}, t0); !errors.Is(err, ErrSubmissionAlreadyProcessed) {
		t.Fatalf("order of checks: %v", err)
	}
}

func TestRecordAttempt(t *testing.T) {
	s := received(t, KindInventory, 0)
	for want := 1; want <= 3; want++ {
		n, err := s.RecordAttempt()
		if err != nil || n != want || s.Attempts() != want || s.Version() != int64(1+want) {
			t.Fatalf("attempt %d: %d %v %+v", want, n, err, s.Snapshot())
		}
	}
	if _, err := s.Complete(Outcome{Status: Success}, t0); err != nil {
		t.Fatal(err)
	}
	if n, err := s.RecordAttempt(); !errors.Is(err, ErrSubmissionAlreadyProcessed) || n != 3 {
		t.Fatalf("after outcome: %d %v", n, err)
	}
	snap := received(t, KindInventory, 0).Snapshot()
	snap.Attempts = MaxAttempts
	capped, err := Restore(snap)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := capped.RecordAttempt(); !errors.Is(err, shared.ErrValidation) || capped.Attempts() != MaxAttempts {
		t.Fatalf("cap: %v", err)
	}
}

func TestSnapshotRoundTripAndRestoreChecks(t *testing.T) {
	s := received(t, KindNodeRegistration, 0)
	_, _ = s.RecordAttempt()
	if _, err := s.Complete(Outcome{Status: Success, NodeID: "n-9"}, t0.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	r, err := Restore(s.Snapshot())
	if err != nil || !reflect.DeepEqual(r.Snapshot(), s.Snapshot()) || r.ResultNodeID() != "n-9" {
		t.Fatalf("round trip: %v %+v", err, r)
	}
	failed := received(t, KindInventory, 0)
	_, _ = failed.Complete(Outcome{Status: Failure, Errors: []Error{{Code: "ErrX", Message: "m", Path: "$.lines[0]"}}}, t0)
	r, err = Restore(failed.Snapshot())
	if err != nil || len(r.Errors()) != 1 || r.Errors()[0] != failed.Errors()[0] {
		t.Fatalf("failure round trip: %v", err)
	}
	good := received(t, KindInventory, 0).Snapshot()
	bad := map[string]func(*Snapshot){
		"id":               func(s *Snapshot) { s.ID = "" },
		"vendor":           func(s *Snapshot) { s.VendorID = "" },
		"kind":             func(s *Snapshot) { s.Kind = "X" },
		"payload":          func(s *Snapshot) { s.Payload = nil },
		"received":         func(s *Snapshot) { s.ReceivedAt = time.Time{} },
		"notBefore":        func(s *Snapshot) { s.NotBefore = s.ReceivedAt.Add(-time.Second) },
		"attempts low":     func(s *Snapshot) { s.Attempts = -1 },
		"attempts high":    func(s *Snapshot) { s.Attempts = MaxAttempts + 1 },
		"version":          func(s *Snapshot) { s.Version = 0 },
		"status":           func(s *Snapshot) { s.Status = "X" },
		"processing done":  func(s *Snapshot) { s.ProcessedAt = t0 },
		"processing error": func(s *Snapshot) { s.Errors = []Error{{Code: "E", Message: "m"}} },
		"success no time":  func(s *Snapshot) { s.Status = Success },
		"success error":    func(s *Snapshot) { s.Status, s.ProcessedAt, s.Errors = Success, t0, []Error{{Code: "E", Message: "m"}} },
		"failure no error": func(s *Snapshot) { s.Status, s.ProcessedAt = Failure, t0 },
		"failure no time":  func(s *Snapshot) { s.Status, s.Errors = Failure, []Error{{Code: "E", Message: "m"}} },
		"too many errors": func(s *Snapshot) {
			s.Status, s.ProcessedAt = Failure, t0
			for i := 0; i <= MaxErrors; i++ {
				s.Errors = append(s.Errors, Error{Code: "E", Message: "m"})
			}
		},
		"node id wrong": func(s *Snapshot) { s.ResultNodeID = "n-1" },
	}
	for name, mut := range bad {
		sn := good
		mut(&sn)
		if _, err := Restore(sn); !errors.Is(err, shared.ErrInvalidSnapshot) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	atMax := good
	atMax.Status, atMax.ProcessedAt = Failure, t0
	for i := 0; i < MaxErrors; i++ {
		atMax.Errors = append(atMax.Errors, Error{Code: "E", Message: "m"})
	}
	if _, err := Restore(atMax); err != nil {
		t.Errorf("MaxErrors errors must be accepted: %v", err)
	}
	edge := good
	edge.Attempts, edge.NotBefore = 0, edge.ReceivedAt
	if _, err := Restore(edge); err != nil {
		t.Errorf("attempts 0 and notBefore == receivedAt: %v", err)
	}
	big := good
	big.Payload = []byte(`"` + strings.Repeat("a", MaxPayload-2) + `"`)
	if _, err := Restore(big); err != nil {
		t.Errorf("payload at the limit: %v", err)
	}
}

func TestAccessors(t *testing.T) {
	s := received(t, KindInventory, time.Minute)
	got := []any{s.ID(), s.VendorID(), s.Kind(), s.PONumber(), s.NodeID(), s.ReceivedAt(), s.NotBefore(), s.Status(), s.Version()}
	want := []any{ID("tx-1"), "V-WS", KindInventory, "PO-1", "n-1", t0, t0.Add(time.Minute), Processing, int64(1)}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("accessors = %v, want %v", got, want)
	}
}
