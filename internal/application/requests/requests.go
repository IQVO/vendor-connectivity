// Package requests holds the application's input vocabulary: the bodies of
// the Vendor API writes (also stored verbatim on the Submission they create)
// and the facts of the consumed events. They carry JSON tags because the same
// value is decoded from a request body, marshalled into the submission payload
// and decoded again by ProcessSubmission. Validate methods return errors that
// wrap shared.ErrValidation.
package requests

import (
	"regexp"
	"time"

	"github.com/claudioed/vendor-connectivity/internal/domain/shared"
)

// MaxInventoryLines is the most lines one inventory feed may carry.
const MaxInventoryLines = 5000

// MaxHolidays is the most holidays one profile may declare.
const MaxHolidays = 400

// MaxCutoffs is the most cutoffs one profile may declare.
const MaxCutoffs = 50

var (
	hhmm       = regexp.MustCompile(`^([01][0-9]|2[0-3]):[0-5][0-9]$`)
	isoDate    = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}$`)
	countryISO = regexp.MustCompile(`^[A-Z]{2}$`)
	weekdays   = map[string]bool{"monday": true, "tuesday": true, "wednesday": true, "thursday": true, "friday": true, "saturday": true, "sunday": true}
)

// OnboardVendor is the body of POST /vendors.
type OnboardVendor struct {
	VendorID         string `json:"vendorId"`
	Name             string `json:"name"`
	AckWindowMinutes int    `json:"ackWindowMinutes"`
}

// SuspendVendor is the optional body of POST /vendors/{vendorId}:suspend.
type SuspendVendor struct {
	Reason string `json:"reason,omitempty"`
}

// InventoryLine is one availability line.
type InventoryLine struct {
	ListingID    string `json:"listingId"`
	AvailableQty int    `json:"availableQty"`
	Basis        string `json:"basis"`
}

// InventoryFeed is the body of POST /vendor/inventory.
type InventoryFeed struct {
	VendorID   string          `json:"vendorId"`
	NodeID     string          `json:"nodeId"`
	FullUpdate bool            `json:"fullUpdate"`
	AsOf       time.Time       `json:"asOf"`
	Lines      []InventoryLine `json:"lines"`
}

// AckLine is one acknowledged line.
type AckLine struct {
	LineNo   int `json:"lineNo"`
	Quantity int `json:"quantity"`
}

// Acknowledgement is the body of POST /vendor/acknowledgements.
type Acknowledgement struct {
	VendorID   string    `json:"vendorId"`
	PONumber   string    `json:"poNumber"`
	Decision   string    `json:"decision"`
	ReasonCode string    `json:"reasonCode,omitempty"`
	Lines      []AckLine `json:"lines"`
}

// LabelPackage names one package to label.
type LabelPackage struct {
	PackageNo int `json:"packageNo"`
}

// LabelRequest is the body of POST /vendor/shipping-labels.
type LabelRequest struct {
	VendorID string         `json:"vendorId"`
	PONumber string         `json:"poNumber"`
	Packages []LabelPackage `json:"packages"`
}

// ShipmentConfirmation is the body of POST /vendor/shipment-confirmations.
type ShipmentConfirmation struct {
	VendorID        string    `json:"vendorId"`
	PONumber        string    `json:"poNumber"`
	ShippedAt       time.Time `json:"shippedAt"`
	TrackingNumbers []string  `json:"trackingNumbers"`
}

// PollQuery is the query of GET /vendor/purchase-orders.
type PollQuery struct {
	VendorID     string
	CreatedAfter *time.Time
	NodeID       string
	NextToken    string
	Limit        int
}

// ReleasedToNode carries the FulfillmentReleasedToNode facts this context uses.
type ReleasedToNode struct {
	AssignmentID   string
	ReleaseNo      int
	OrderID        string
	GroupNo        int
	NodeID         string
	RequiredShipBy time.Time
	Tier           string
	Lines          []ReleasedLine
}

// ReleasedLine is one line of a release.
type ReleasedLine struct {
	ListingID string
	Quantity  int
}

// FulfillmentCancelled carries the FulfillmentCancelled facts this context uses.
type FulfillmentCancelled struct {
	AssignmentID string
	Reason       string
}

// NodeRegisteredFacts carries NodeRegistered.
type NodeRegisteredFacts struct {
	NodeID, NodeType, Subtype, Operator, VendorID, VendorNodeCode, RegionID, Timezone string
}

// NodeProfileFacts carries NodeOperationalProfileDeclared.
type NodeProfileFacts struct {
	NodeID              string
	ProfileRevision     int
	HandlingTimeMinutes int
}

// NodeStatusFacts carries NodeActivated, NodeSuspended or NodeRetired.
type NodeStatusFacts struct {
	NodeID string
	Status string
}

// ListingFacts carries ListingCreated.
type ListingFacts struct {
	ListingID, NodeID, CatalogItemID string
}

// Validate checks the body shape of an OnboardVendor.
func (r OnboardVendor) Validate() error {
	if !shared.ValidID(r.VendorID) {
		return shared.Invalid("vendorId is not a valid id")
	}
	if !shared.ValidTextN(r.Name, 120) {
		return shared.Invalid("name must be non-blank, trimmed and at most 120 characters")
	}
	return nil
}

// Validate checks the shape of an inventory feed.
func (r InventoryFeed) Validate() error {
	switch {
	case !shared.ValidID(r.VendorID) || !shared.ValidID(r.NodeID):
		return shared.Invalid("vendorId and nodeId must be valid ids")
	case r.AsOf.IsZero():
		return shared.Invalid("asOf is required")
	case len(r.Lines) > MaxInventoryLines:
		return shared.Invalid("at most %d lines", MaxInventoryLines)
	}
	seen := map[string]bool{}
	for i, l := range r.Lines {
		switch {
		case !shared.ValidID(l.ListingID):
			return shared.Invalid("lines[%d].listingId is not a valid id", i)
		case seen[l.ListingID]:
			return shared.Invalid("lines[%d] repeats listing %s", i, l.ListingID)
		case l.AvailableQty < 0 || l.AvailableQty > shared.MaxInt32:
			return shared.Invalid("lines[%d].availableQty must not be negative", i)
		case l.Basis != "PHYSICAL" && l.Basis != "THROUGHPUT_CONSTRAINED" && l.Basis != "UNKNOWN":
			return shared.Invalid("lines[%d].basis must be PHYSICAL, THROUGHPUT_CONSTRAINED or UNKNOWN", i)
		}
		seen[l.ListingID] = true
	}
	return nil
}

// Validate checks the shape of an acknowledgement (the domain judges the rest).
func (r Acknowledgement) Validate() error {
	if !shared.ValidID(r.VendorID) || !shared.ValidID(r.PONumber) {
		return shared.Invalid("vendorId and poNumber must be valid ids")
	}
	return nil
}

// Validate checks the shape of a label request.
func (r LabelRequest) Validate() error {
	if !shared.ValidID(r.VendorID) || !shared.ValidID(r.PONumber) {
		return shared.Invalid("vendorId and poNumber must be valid ids")
	}
	return nil
}

// Validate checks the shape of a shipment confirmation.
func (r ShipmentConfirmation) Validate() error {
	if !shared.ValidID(r.VendorID) || !shared.ValidID(r.PONumber) {
		return shared.Invalid("vendorId and poNumber must be valid ids")
	}
	return nil
}
