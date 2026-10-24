// Package submission is the Submission aggregate: one asynchronous vendor
// write (acknowledgement, inventory feed, node registration, profile
// declaration, shipment confirmation, inbound receipt) and its processing outcome (ADR 0002).
//
// States: PROCESSING (initial), SUCCESS, FAILURE. The outcome is set once
// (ErrSubmissionAlreadyProcessed) and never before notBefore = receivedAt +
// the simulated processing delay (ErrSubmissionNotDue).
package submission

import (
	"encoding/json"
	"errors"
	"time"

	"github.com/claudioed/vendor-connectivity/internal/domain/shared"
)

var (
	// ErrSubmissionAlreadyProcessed is raised when the outcome is set twice
	// (problem type submission-already-processed, 409; internal).
	ErrSubmissionAlreadyProcessed = errors.New("submission already processed")
	// ErrSubmissionNotDue is raised when processing starts before notBefore
	// (problem type submission-not-due, 409; internal).
	ErrSubmissionNotDue = errors.New("submission not due")
)

const (
	// MaxPayload is the largest accepted request body kept on a submission, in bytes.
	MaxPayload = 4194304
	// MaxErrors is the most SubmissionErrors one submission keeps.
	MaxErrors = 50
	// MaxAttempts is the most processing attempts one submission records.
	MaxAttempts = 1000
	// MaxCode, MaxMessage and MaxPath bound a SubmissionError, in characters.
	MaxCode    = 100
	MaxMessage = 1000
	MaxPath    = 200
)

// ID identifies a submission (transactionId).
type ID string

// Kind is the SubmissionKind.
type Kind string

// The six kinds of vendor write.
const (
	KindAcknowledgement      Kind = "ACKNOWLEDGEMENT"
	KindInventory            Kind = "INVENTORY"
	KindNodeRegistration     Kind = "NODE_REGISTRATION"
	KindNodeProfile          Kind = "NODE_PROFILE"
	KindShipmentConfirmation Kind = "SHIPMENT_CONFIRMATION"
	KindInboundReceipt       Kind = "INBOUND_RECEIPT"
)

// Status is the processing state.
type Status string

// The three states of a submission.
const (
	Processing Status = "PROCESSING"
	Success    Status = "SUCCESS"
	Failure    Status = "FAILURE"
)

// Error is one SubmissionError: code is the owning context's Err identifier.
type Error struct {
	Code    string
	Message string
	Path    string
}

// Submission is the aggregate root.
type Submission struct {
	id           ID
	vendorID     string
	kind         Kind
	status       Status
	payload      []byte
	poNumber     string
	nodeID       string
	resultNodeID string
	receivedAt   time.Time
	notBefore    time.Time
	processedAt  time.Time
	errs         []Error
	attempts     int
	version      int64
}

// Snapshot is the persisted form of a submission. Restore validates it.
type Snapshot struct {
	ID           ID
	VendorID     string
	Kind         Kind
	Status       Status
	Payload      []byte
	PONumber     string
	NodeID       string
	ResultNodeID string
	ReceivedAt   time.Time
	NotBefore    time.Time
	ProcessedAt  time.Time
	Errors       []Error
	Attempts     int
	Version      int64
}

// NewSubmission is the input of Receive.
type NewSubmission struct {
	ID       ID
	VendorID string
	Kind     Kind
	// Payload is the vendor's request body as received (valid JSON).
	Payload []byte
	// PONumber names the purchase order of an ACKNOWLEDGEMENT or SHIPMENT_CONFIRMATION.
	PONumber string
	// NodeID names the node of an INVENTORY or NODE_PROFILE write.
	NodeID string
}

// Receive creates a PROCESSING submission at version 1 with notBefore =
// now + delay (ReceiveSubmission). It raises no event.
func Receive(in NewSubmission, now time.Time, delay time.Duration) (*Submission, error) {
	if delay < 0 {
		return nil, shared.Invalid("processing delay must not be negative")
	}
	s, err := build(Snapshot{ID: in.ID, VendorID: in.VendorID, Kind: in.Kind, Status: Processing, Payload: in.Payload,
		PONumber: in.PONumber, NodeID: in.NodeID, ReceivedAt: now, NotBefore: now.Add(delay), Version: 1})
	if err != nil {
		return nil, shared.Invalid("%v", err)
	}
	return s, nil
}

// Restore rebuilds a persisted submission, validating every invariant.
func Restore(sn Snapshot) (*Submission, error) {
	s, err := build(sn)
	if err != nil {
		return nil, shared.Corrupt("submission %q: %v", sn.ID, err)
	}
	return s, nil
}

func build(sn Snapshot) (*Submission, error) {
	if err := checkValues(sn); err != nil {
		return nil, err
	}
	if err := checkOutcome(sn); err != nil {
		return nil, err
	}
	return &Submission{
		id: sn.ID, vendorID: sn.VendorID, kind: sn.Kind, status: sn.Status, payload: append([]byte(nil), sn.Payload...),
		poNumber: sn.PONumber, nodeID: sn.NodeID, resultNodeID: sn.ResultNodeID,
		receivedAt: sn.ReceivedAt.UTC(), notBefore: sn.NotBefore.UTC(), processedAt: shared.UTC(sn.ProcessedAt),
		errs: append([]Error(nil), sn.Errors...), attempts: sn.Attempts, version: sn.Version,
	}, nil
}

func validKind(k Kind) bool {
	switch k {
	case KindAcknowledgement, KindInventory, KindNodeRegistration, KindNodeProfile, KindShipmentConfirmation, KindInboundReceipt:
		return true
	}
	return false
}

func checkValues(sn Snapshot) error {
	if err := checkIdentity(sn); err != nil {
		return err
	}
	if sn.ResultNodeID != "" && (!shared.ValidID(sn.ResultNodeID) || sn.Kind != KindNodeRegistration || sn.Status != Success) {
		return errors.New("a result nodeId belongs to a successful NODE_REGISTRATION")
	}
	if sn.ReceivedAt.IsZero() || sn.NotBefore.Before(sn.ReceivedAt) {
		return errors.New("receivedAt is required and notBefore is not before it")
	}
	if sn.Attempts < 0 || sn.Attempts > MaxAttempts {
		return errors.New("attempts out of range")
	}
	if sn.Version < 1 {
		return errors.New("version must be at least 1")
	}
	return nil
}

func checkIdentity(sn Snapshot) error {
	if !shared.ValidID(string(sn.ID)) {
		return errors.New("transactionId is not a valid id")
	}
	if !shared.ValidID(sn.VendorID) {
		return errors.New("vendorId is not a valid id")
	}
	if !validKind(sn.Kind) {
		return errors.New("kind is not a SubmissionKind")
	}
	if len(sn.Payload) == 0 || len(sn.Payload) > MaxPayload || !json.Valid(sn.Payload) {
		return errors.New("payload must be valid JSON of at most 4 MiB")
	}
	if sn.PONumber != "" && !shared.ValidID(sn.PONumber) {
		return errors.New("poNumber is not a valid id")
	}
	if sn.NodeID != "" && !shared.ValidID(sn.NodeID) {
		return errors.New("nodeId is not a valid id")
	}
	return nil
}

func checkOutcome(sn Snapshot) error {
	switch sn.Status {
	case Processing:
		if !sn.ProcessedAt.IsZero() || len(sn.Errors) != 0 {
			return errors.New("a PROCESSING submission has no outcome")
		}
	case Success:
		if sn.ProcessedAt.IsZero() || len(sn.Errors) != 0 {
			return errors.New("a SUCCESS submission has processedAt and no errors")
		}
	case Failure:
		if sn.ProcessedAt.IsZero() || len(sn.Errors) == 0 {
			return errors.New("a FAILURE submission has processedAt and at least one error")
		}
	default:
		return errors.New("status must be PROCESSING, SUCCESS or FAILURE")
	}
	if len(sn.Errors) > MaxErrors {
		return errors.New("too many errors")
	}
	for _, e := range sn.Errors {
		if err := checkError(e); err != nil {
			return err
		}
	}
	return nil
}

func checkError(e Error) error {
	if !shared.ValidTextN(e.Code, MaxCode) || !shared.ValidTextN(e.Message, MaxMessage) || (e.Path != "" && !shared.ValidTextN(e.Path, MaxPath)) {
		return errors.New("a submission error needs a code and a message, and bounded text")
	}
	return nil
}

// Outcome is the result ProcessSubmission records.
type Outcome struct {
	Status Status
	Errors []Error
	// NodeID is the node a successful NODE_REGISTRATION created (Gap G30).
	NodeID string
}

// Due reports whether processing may start at now.
func (s *Submission) Due(now time.Time) bool { return !now.Before(s.notBefore) }

// Complete sets the outcome once and raises SubmissionProcessed. It refuses a
// second outcome (ErrSubmissionAlreadyProcessed) and a call before notBefore
// (ErrSubmissionNotDue).
func (s *Submission) Complete(o Outcome, now time.Time) ([]shared.Event, error) {
	if s.status != Processing {
		return nil, ErrSubmissionAlreadyProcessed
	}
	if !s.Due(now) {
		return nil, ErrSubmissionNotDue
	}
	next := s.Snapshot()
	next.Status, next.Errors, next.ProcessedAt, next.ResultNodeID = o.Status, o.Errors, now, o.NodeID
	if err := checkOutcome(next); err != nil {
		return nil, shared.Invalid("%v", err)
	}
	if err := checkValues(next); err != nil {
		return nil, shared.Invalid("%v", err)
	}
	s.status, s.errs, s.processedAt, s.resultNodeID = o.Status, append([]Error(nil), o.Errors...), now.UTC(), o.NodeID
	s.version++
	ev := SubmissionProcessed{Header: shared.Header{Version: s.version, At: s.processedAt}, TransactionID: s.id,
		VendorID: s.vendorID, Kind: s.kind, Status: s.status, ErrorCount: len(s.errs)}
	return []shared.Event{ev}, nil
}

// RecordAttempt counts one processing pass that ended without an outcome and
// returns the attempts so far. It is refused once the submission is processed.
func (s *Submission) RecordAttempt() (int, error) {
	if s.status != Processing {
		return s.attempts, ErrSubmissionAlreadyProcessed
	}
	if s.attempts >= MaxAttempts {
		return s.attempts, shared.Invalid("attempts are capped at %d", MaxAttempts)
	}
	s.attempts++
	s.version++
	return s.attempts, nil
}

// Snapshot returns the persisted form.
func (s *Submission) Snapshot() Snapshot {
	return Snapshot{ID: s.id, VendorID: s.vendorID, Kind: s.kind, Status: s.status, Payload: append([]byte(nil), s.payload...),
		PONumber: s.poNumber, NodeID: s.nodeID, ResultNodeID: s.resultNodeID, ReceivedAt: s.receivedAt, NotBefore: s.notBefore,
		ProcessedAt: s.processedAt, Errors: append([]Error(nil), s.errs...), Attempts: s.attempts, Version: s.version}
}

// ID returns the transactionId.
func (s *Submission) ID() ID { return s.id }

// VendorID returns the vendor that wrote.
func (s *Submission) VendorID() string { return s.vendorID }

// Kind returns the SubmissionKind.
func (s *Submission) Kind() Kind { return s.kind }

// Status returns the processing state.
func (s *Submission) Status() Status { return s.status }

// Payload returns a copy of the stored request body.
func (s *Submission) Payload() []byte { return append([]byte(nil), s.payload...) }

// PONumber returns the purchase order the write concerns, if any.
func (s *Submission) PONumber() string { return s.poNumber }

// NodeID returns the node the write concerns, if any.
func (s *Submission) NodeID() string { return s.nodeID }

// ResultNodeID returns the node a successful registration created.
func (s *Submission) ResultNodeID() string { return s.resultNodeID }

// ReceivedAt returns when the write was received.
func (s *Submission) ReceivedAt() time.Time { return s.receivedAt }

// NotBefore returns the earliest processing instant.
func (s *Submission) NotBefore() time.Time { return s.notBefore }

// ProcessedAt returns when the outcome was set (zero while PROCESSING).
func (s *Submission) ProcessedAt() time.Time { return s.processedAt }

// Errors returns a copy of the SubmissionErrors.
func (s *Submission) Errors() []Error { return append([]Error(nil), s.errs...) }

// Attempts returns the processing passes that ended without an outcome.
func (s *Submission) Attempts() int { return s.attempts }

// Version returns the optimistic-concurrency version.
func (s *Submission) Version() int64 { return s.version }
