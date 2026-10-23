package purchaseorder

import (
	"time"

	"github.com/claudioed/vendor-connectivity/internal/domain/shared"
)

// Entity is the CloudEvents entity segment of the PurchaseOrder's events.
const Entity = "purchaseorder"

// PurchaseOrderIssued is raised by Issue.
type PurchaseOrderIssued struct {
	shared.Header
	PONumber       Number
	VendorID       string
	NodeID         string
	AssignmentID   string
	ReleaseNo      int
	RequiredShipBy time.Time
	AckBy          time.Time
	Lines          []Line
}

// PurchaseOrderAccepted is raised when an ACCEPT acknowledgement transaction succeeds.
type PurchaseOrderAccepted struct {
	shared.Header
	PONumber      Number
	AssignmentID  string
	ReleaseNo     int
	NodeID        string
	AcceptedAt    time.Time
	TransactionID string
}

// PurchaseOrderRejected is raised when a REJECT acknowledgement transaction succeeds.
type PurchaseOrderRejected struct {
	shared.Header
	PONumber     Number
	AssignmentID string
	ReleaseNo    int
	NodeID       string
	ReasonCode   RejectReason
	RejectedAt   time.Time
}

// PurchaseOrderExpired is raised by Expire.
type PurchaseOrderExpired struct {
	shared.Header
	PONumber     Number
	AssignmentID string
	ReleaseNo    int
	NodeID       string
	AckBy        time.Time
}

// PurchaseOrderCancelled is raised by Cancel (analytics only).
type PurchaseOrderCancelled struct {
	shared.Header
	PONumber     Number
	AssignmentID string
	Reason       string
}

// VendorShipmentConfirmed is raised when the confirmation transaction succeeds.
type VendorShipmentConfirmed struct {
	shared.Header
	PONumber        Number
	AssignmentID    string
	NodeID          string
	TrackingNumbers []string
	ShippedAt       time.Time
	RequiredShipBy  time.Time
}

// EventName returns "PurchaseOrderIssued".
func (PurchaseOrderIssued) EventName() string { return "PurchaseOrderIssued" }

// EventName returns "PurchaseOrderAccepted".
func (PurchaseOrderAccepted) EventName() string { return "PurchaseOrderAccepted" }

// EventName returns "PurchaseOrderRejected".
func (PurchaseOrderRejected) EventName() string { return "PurchaseOrderRejected" }

// EventName returns "PurchaseOrderExpired".
func (PurchaseOrderExpired) EventName() string { return "PurchaseOrderExpired" }

// EventName returns "PurchaseOrderCancelled".
func (PurchaseOrderCancelled) EventName() string { return "PurchaseOrderCancelled" }

// EventName returns "VendorShipmentConfirmed".
func (VendorShipmentConfirmed) EventName() string { return "VendorShipmentConfirmed" }

// Entity returns "purchaseorder".
func (PurchaseOrderIssued) Entity() string { return Entity }

// Entity returns "purchaseorder".
func (PurchaseOrderAccepted) Entity() string { return Entity }

// Entity returns "purchaseorder".
func (PurchaseOrderRejected) Entity() string { return Entity }

// Entity returns "purchaseorder".
func (PurchaseOrderExpired) Entity() string { return Entity }

// Entity returns "purchaseorder".
func (PurchaseOrderCancelled) Entity() string { return Entity }

// Entity returns "purchaseorder".
func (VendorShipmentConfirmed) Entity() string { return Entity }

// SubjectID returns the poNumber.
func (e PurchaseOrderIssued) SubjectID() string { return string(e.PONumber) }

// SubjectID returns the poNumber.
func (e PurchaseOrderAccepted) SubjectID() string { return string(e.PONumber) }

// SubjectID returns the poNumber.
func (e PurchaseOrderRejected) SubjectID() string { return string(e.PONumber) }

// SubjectID returns the poNumber.
func (e PurchaseOrderExpired) SubjectID() string { return string(e.PONumber) }

// SubjectID returns the poNumber.
func (e PurchaseOrderCancelled) SubjectID() string { return string(e.PONumber) }

// SubjectID returns the poNumber.
func (e VendorShipmentConfirmed) SubjectID() string { return string(e.PONumber) }

var (
	_ shared.Event = PurchaseOrderIssued{}
	_ shared.Event = PurchaseOrderAccepted{}
	_ shared.Event = PurchaseOrderRejected{}
	_ shared.Event = PurchaseOrderExpired{}
	_ shared.Event = PurchaseOrderCancelled{}
	_ shared.Event = VendorShipmentConfirmed{}
)
