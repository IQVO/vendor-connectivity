package vendor

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/claudioed/vendor-connectivity/internal/domain/shared"
)

var t0 = time.Date(2026, 10, 22, 13, 0, 0, 0, time.UTC)

func onboarded(t *testing.T) *Account {
	t.Helper()
	a, _, err := Onboard(NewAccount{ID: "V-WS", Name: "Warehouse Systems", AckWindowMinutes: 240}, t0)
	if err != nil {
		t.Fatalf("Onboard: %v", err)
	}
	return a
}

func TestOnboardRaisesVendorOnboarded(t *testing.T) {
	a, evs, err := Onboard(NewAccount{ID: "V-WS", Name: "Warehouse Systems", AckWindowMinutes: 240}, t0.In(time.FixedZone("x", 3600)))
	if err != nil {
		t.Fatal(err)
	}
	want := Snapshot{ID: "V-WS", Name: "Warehouse Systems", AckWindowMinutes: 240, State: Active, OnboardedAt: t0, Version: 1}
	if !reflect.DeepEqual(a.Snapshot(), want) || a.AckWindow() != 4*time.Hour {
		t.Fatalf("account = %+v", a.Snapshot())
	}
	wantEv := VendorOnboarded{Header: shared.Header{Version: 1, At: t0}, VendorID: "V-WS", Name: "Warehouse Systems", AckWindowMinutes: 240}
	if !reflect.DeepEqual(evs, []shared.Event{wantEv}) {
		t.Fatalf("events = %+v", evs)
	}
	assertMeta(t, evs[0], "VendorOnboarded")
}

func assertMeta(t *testing.T, ev shared.Event, name string) {
	t.Helper()
	got := [3]string{ev.EventName(), ev.Entity(), ev.SubjectID()}
	if want := [3]string{name, "vendoraccount", "V-WS"}; got != want {
		t.Fatalf("event meta = %v, want %v", got, want)
	}
}

func TestOnboardAckWindowBounds(t *testing.T) {
	tests := []struct {
		mins int
		ok   bool
	}{{0, false}, {59, false}, {60, true}, {61, true}, {1439, true}, {1440, true}, {1441, false}, {-60, false}}
	for _, tc := range tests {
		_, _, err := Onboard(NewAccount{ID: "V-1", Name: "n", AckWindowMinutes: tc.mins}, t0)
		if tc.ok && err != nil {
			t.Errorf("%d minutes: %v", tc.mins, err)
		}
		if !tc.ok && !errors.Is(err, ErrAckWindowOutOfRange) {
			t.Errorf("%d minutes: err = %v, want ErrAckWindowOutOfRange", tc.mins, err)
		}
	}
}

func TestOnboardRejectsBadValues(t *testing.T) {
	tests := map[string]NewAccount{
		"blank id":     {ID: "", Name: "n", AckWindowMinutes: 60},
		"bad id":       {ID: "a b", Name: "n", AckWindowMinutes: 60},
		"blank name":   {ID: "V-1", Name: " ", AckWindowMinutes: 60},
		"padded name":  {ID: "V-1", Name: " n", AckWindowMinutes: 60},
		"long name":    {ID: "V-1", Name: strings.Repeat("n", MaxName+1), AckWindowMinutes: 60},
		"control name": {ID: "V-1", Name: "a\tb", AckWindowMinutes: 60},
	}
	for name, in := range tests {
		if _, _, err := Onboard(in, t0); !errors.Is(err, shared.ErrValidation) {
			t.Errorf("%s: err = %v, want ErrValidation", name, err)
		}
	}
	if _, _, err := Onboard(NewAccount{ID: "V-1", Name: strings.Repeat("n", MaxName), AckWindowMinutes: 60}, t0); err != nil {
		t.Errorf("name at the limit: %v", err)
	}
	if _, _, err := Onboard(NewAccount{ID: "V-1", Name: "n", AckWindowMinutes: 60}, time.Time{}); !errors.Is(err, shared.ErrValidation) {
		t.Errorf("zero clock: err = %v", err)
	}
}

func TestSuspendAndReinstateTransitions(t *testing.T) {
	a := onboarded(t)
	if err := a.EnsureActive(); err != nil {
		t.Fatalf("active vendor: %v", err)
	}
	evs, err := a.Suspend("contract review", t0.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	wantSus := VendorSuspended{Header: shared.Header{Version: 2, At: t0.Add(time.Hour)}, VendorID: "V-WS", Reason: "contract review"}
	if !reflect.DeepEqual(evs, []shared.Event{wantSus}) {
		t.Fatalf("events = %+v", evs)
	}
	assertMeta(t, evs[0], "VendorSuspended")
	if a.State() != Suspended || a.Version() != 2 || !errors.Is(a.EnsureActive(), ErrVendorSuspended) {
		t.Fatalf("after suspend: %+v", a.Snapshot())
	}
	evs, err = a.Reinstate(t0.Add(2 * time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	wantRe := VendorReinstated{Header: shared.Header{Version: 3, At: t0.Add(2 * time.Hour)}, VendorID: "V-WS"}
	if !reflect.DeepEqual(evs, []shared.Event{wantRe}) {
		t.Fatalf("events = %+v", evs)
	}
	assertMeta(t, evs[0], "VendorReinstated")
	want := Snapshot{ID: "V-WS", Name: "Warehouse Systems", AckWindowMinutes: 240, State: Active, OnboardedAt: t0, Version: 3}
	if !reflect.DeepEqual(a.Snapshot(), want) || a.EnsureActive() != nil {
		t.Fatalf("after reinstate: %+v", a.Snapshot())
	}
}

func TestRepeatedCommandsAreNoOps(t *testing.T) {
	a := onboarded(t)
	if evs, err := a.Reinstate(t0); err != nil || evs != nil || a.Version() != 1 {
		t.Fatalf("reinstate active: %v %v v%d", evs, err, a.Version())
	}
	if _, err := a.Suspend("", t0); err != nil {
		t.Fatal(err)
	}
	if evs, err := a.Suspend("again", t0); err != nil || evs != nil || a.Version() != 2 || a.Snapshot().SuspendReason != "" {
		t.Fatalf("suspend suspended: %v %v v%d %+v", evs, err, a.Version(), a.Snapshot())
	}
}

func TestSuspendReasonBounds(t *testing.T) {
	for _, r := range []string{" x", "x ", "a\tb", strings.Repeat("r", MaxReason+1)} {
		a := onboarded(t)
		if _, err := a.Suspend(r, t0); !errors.Is(err, shared.ErrValidation) || a.State() != Active {
			t.Errorf("reason %q: err = %v state %s", r, err, a.State())
		}
	}
	a := onboarded(t)
	if _, err := a.Suspend(strings.Repeat("r", MaxReason), t0); err != nil {
		t.Errorf("reason at the limit: %v", err)
	}
}

func TestSnapshotRoundTripAndRestoreChecks(t *testing.T) {
	a := onboarded(t)
	_, _ = a.Suspend("audit", t0.Add(time.Minute))
	r, err := Restore(a.Snapshot())
	if err != nil || r.Snapshot() != a.Snapshot() {
		t.Fatalf("round trip: %v %+v", err, r)
	}
	good := Snapshot{ID: "V-1", Name: "n", AckWindowMinutes: 60, State: Active, OnboardedAt: t0, Version: 1}
	if _, err := Restore(good); err != nil {
		t.Fatalf("good: %v", err)
	}
	sus := good
	sus.State, sus.SuspendedAt = Suspended, t0
	if _, err := Restore(sus); err != nil {
		t.Fatalf("suspended without reason: %v", err)
	}
	bad := map[string]func(*Snapshot){
		"id":                func(s *Snapshot) { s.ID = "" },
		"name":              func(s *Snapshot) { s.Name = "" },
		"window low":        func(s *Snapshot) { s.AckWindowMinutes = 59 },
		"window high":       func(s *Snapshot) { s.AckWindowMinutes = 1441 },
		"onboarded":         func(s *Snapshot) { s.OnboardedAt = time.Time{} },
		"version":           func(s *Snapshot) { s.Version = 0 },
		"state":             func(s *Snapshot) { s.State = "GONE" },
		"active suspended":  func(s *Snapshot) { s.SuspendedAt = t0 },
		"active reason":     func(s *Snapshot) { s.SuspendReason = "x" },
		"suspended no time": func(s *Snapshot) { s.State = Suspended },
		"suspended reason":  func(s *Snapshot) { s.State, s.SuspendedAt, s.SuspendReason = Suspended, t0, " x" },
	}
	for name, mut := range bad {
		s := good
		mut(&s)
		if _, err := Restore(s); !errors.Is(err, shared.ErrInvalidSnapshot) {
			t.Errorf("%s: err = %v, want ErrInvalidSnapshot", name, err)
		}
	}
	lim := good
	lim.AckWindowMinutes = 1440
	if _, err := Restore(lim); err != nil {
		t.Errorf("window at the upper limit: %v", err)
	}
	ver := good
	ver.Version = 1
	if _, err := Restore(ver); err != nil {
		t.Errorf("version 1: %v", err)
	}
}

func TestAccessors(t *testing.T) {
	a := onboarded(t)
	got := [4]any{a.ID(), a.Name(), a.AckWindowMinutes(), a.AckWindow()}
	if want := [4]any{ID("V-WS"), "Warehouse Systems", 240, 4 * time.Hour}; got != want {
		t.Fatalf("accessors = %v, want %v", got, want)
	}
}
