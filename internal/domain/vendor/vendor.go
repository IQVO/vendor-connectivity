// Package vendor is the VendorAccount aggregate: a vendor organisation, its
// acknowledgement window and whether it may write (ADR 0002).
//
// States: ACTIVE (initial) and SUSPENDED. SuspendVendor moves ACTIVE to
// SUSPENDED, ReinstateVendor moves SUSPENDED to ACTIVE. Repeating a command
// that is already in effect is a no-op without an event (assumption A8).
package vendor

import (
	"errors"
	"time"

	"github.com/claudioed/vendor-connectivity/internal/domain/shared"
)

var (
	// ErrAckWindowOutOfRange is raised when the ack window is not between 1h
	// and 24h (problem type ack-window-out-of-range, 422).
	ErrAckWindowOutOfRange = errors.New("ack window out of range")
	// ErrVendorSuspended is raised when a suspended vendor writes (problem
	// type vendor-suspended, 409; Gap G30).
	ErrVendorSuspended = errors.New("vendor suspended")
)

const (
	// MinAckWindowMinutes is the shortest acknowledgement window (1h).
	MinAckWindowMinutes = 60
	// MaxAckWindowMinutes is the longest acknowledgement window (24h).
	MaxAckWindowMinutes = 1440
	// MaxName is the longest vendor name, in characters.
	MaxName = 120
	// MaxReason is the longest suspension reason, in characters.
	MaxReason = 200
)

// ID identifies a vendor (vendorId).
type ID string

// State is the lifecycle state of a vendor account.
type State string

// The two states of a vendor account.
const (
	Active    State = "ACTIVE"
	Suspended State = "SUSPENDED"
)

// Account is the aggregate root.
type Account struct {
	id            ID
	name          string
	ackWindowMins int
	state         State
	onboardedAt   time.Time
	suspendedAt   time.Time
	reason        string
	version       int64
}

// Snapshot is the persisted form of an account. Restore validates it.
type Snapshot struct {
	ID               ID
	Name             string
	AckWindowMinutes int
	State            State
	OnboardedAt      time.Time
	SuspendedAt      time.Time
	SuspendReason    string
	Version          int64
}

// NewAccount is the input of Onboard.
type NewAccount struct {
	ID               ID
	Name             string
	AckWindowMinutes int
}

// Onboard creates an ACTIVE account at version 1 and raises VendorOnboarded.
func Onboard(in NewAccount, now time.Time) (*Account, []shared.Event, error) {
	if in.AckWindowMinutes < MinAckWindowMinutes || in.AckWindowMinutes > MaxAckWindowMinutes {
		return nil, nil, ErrAckWindowOutOfRange
	}
	a, err := build(Snapshot{ID: in.ID, Name: in.Name, AckWindowMinutes: in.AckWindowMinutes, State: Active, OnboardedAt: now, Version: 1})
	if err != nil {
		return nil, nil, shared.Invalid("%v", err)
	}
	ev := VendorOnboarded{Header: shared.Header{Version: a.version, At: a.onboardedAt}, VendorID: a.id, Name: a.name, AckWindowMinutes: a.ackWindowMins}
	return a, []shared.Event{ev}, nil
}

// Restore rebuilds a persisted account, validating every invariant.
func Restore(s Snapshot) (*Account, error) {
	a, err := build(s)
	if err != nil {
		return nil, shared.Corrupt("vendor %q: %v", s.ID, err)
	}
	return a, nil
}

func build(s Snapshot) (*Account, error) {
	if err := checkValues(s); err != nil {
		return nil, err
	}
	return &Account{
		id: s.ID, name: s.Name, ackWindowMins: s.AckWindowMinutes, state: s.State,
		onboardedAt: s.OnboardedAt.UTC(), suspendedAt: shared.UTC(s.SuspendedAt), reason: s.SuspendReason, version: s.Version,
	}, nil
}

func checkValues(s Snapshot) error {
	if !shared.ValidID(string(s.ID)) {
		return errors.New("vendorId is not a valid id")
	}
	if !shared.ValidTextN(s.Name, MaxName) {
		return errors.New("name must be non-blank, trimmed and at most 120 characters")
	}
	if s.AckWindowMinutes < MinAckWindowMinutes || s.AckWindowMinutes > MaxAckWindowMinutes {
		return ErrAckWindowOutOfRange
	}
	if s.OnboardedAt.IsZero() {
		return errors.New("onboardedAt is required")
	}
	if s.Version < 1 {
		return errors.New("version must be at least 1")
	}
	return checkState(s)
}

func checkState(s Snapshot) error {
	switch s.State {
	case Active:
		if !s.SuspendedAt.IsZero() || s.SuspendReason != "" {
			return errors.New("an active vendor has no suspension")
		}
		return nil
	case Suspended:
		if s.SuspendedAt.IsZero() {
			return errors.New("a suspended vendor needs suspendedAt")
		}
		if s.SuspendReason != "" && !shared.ValidTextN(s.SuspendReason, MaxReason) {
			return errors.New("suspend reason must be trimmed and at most 200 characters")
		}
		return nil
	default:
		return errors.New("state must be ACTIVE or SUSPENDED")
	}
}

// Suspend moves ACTIVE to SUSPENDED and raises VendorSuspended. reason is
// optional. Suspending a suspended vendor is a no-op (A8).
func (a *Account) Suspend(reason string, now time.Time) ([]shared.Event, error) {
	if a.state == Suspended {
		return nil, nil
	}
	if reason != "" && !shared.ValidTextN(reason, MaxReason) {
		return nil, shared.Invalid("reason must be trimmed and at most %d characters", MaxReason)
	}
	a.state, a.suspendedAt, a.reason = Suspended, now.UTC(), reason
	a.version++
	return []shared.Event{VendorSuspended{Header: shared.Header{Version: a.version, At: a.suspendedAt}, VendorID: a.id, Reason: reason}}, nil
}

// Reinstate moves SUSPENDED to ACTIVE and raises VendorReinstated.
// Reinstating an active vendor is a no-op (A8).
func (a *Account) Reinstate(now time.Time) ([]shared.Event, error) {
	if a.state == Active {
		return nil, nil
	}
	a.state, a.suspendedAt, a.reason = Active, time.Time{}, ""
	a.version++
	return []shared.Event{VendorReinstated{Header: shared.Header{Version: a.version, At: now.UTC()}, VendorID: a.id}}, nil
}

// EnsureActive returns ErrVendorSuspended when the vendor may not write.
func (a *Account) EnsureActive() error {
	if a.state == Suspended {
		return ErrVendorSuspended
	}
	return nil
}

// Snapshot returns the persisted form.
func (a *Account) Snapshot() Snapshot {
	return Snapshot{ID: a.id, Name: a.name, AckWindowMinutes: a.ackWindowMins, State: a.state, OnboardedAt: a.onboardedAt,
		SuspendedAt: a.suspendedAt, SuspendReason: a.reason, Version: a.version}
}

// ID returns the vendorId.
func (a *Account) ID() ID { return a.id }

// Name returns the vendor's name.
func (a *Account) Name() string { return a.name }

// AckWindow returns the acknowledgement window.
func (a *Account) AckWindow() time.Duration { return time.Duration(a.ackWindowMins) * time.Minute }

// AckWindowMinutes returns the acknowledgement window in minutes.
func (a *Account) AckWindowMinutes() int { return a.ackWindowMins }

// State returns the lifecycle state.
func (a *Account) State() State { return a.state }

// Version returns the optimistic-concurrency version.
func (a *Account) Version() int64 { return a.version }
