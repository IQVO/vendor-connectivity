// Package requests holds the application's input vocabulary: the bodies of
// the Vendor API writes (also stored verbatim on the Submission they create)
// and the facts of the consumed events. They carry JSON tags because the same
// value is decoded from a request body, marshalled into the submission payload
// and decoded again by ProcessSubmission. Validate methods return errors that
// wrap shared.ErrValidation.
package requests

import (
	"regexp"
	"strings"
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

// NodeRegistration is the body of POST /vendor/nodes.
type NodeRegistration struct {
	VendorID       string   `json:"vendorId"`
	VendorNodeCode string   `json:"vendorNodeCode"`
	NodeType       string   `json:"nodeType"`
	Subtype        string   `json:"subtype,omitempty"`
	RegionID       string   `json:"regionId"`
	Timezone       string   `json:"timezone"`
	PostalCode     string   `json:"postalCode"`
	CountryCode    string   `json:"countryCode"`
	Lat            *float64 `json:"lat,omitempty"`
	Lon            *float64 `json:"lon,omitempty"`
}

// DayHours is the opening interval of one weekday.
type DayHours struct {
	Start string `json:"start"`
	End   string `json:"end"`
}

// Cutoff is one handover cutoff.
type Cutoff struct {
	LocalTime string   `json:"localTime"`
	Days      []string `json:"days,omitempty"`
	MaxOrders *int     `json:"maxOrders,omitempty"`
}

// ThroughputCap bounds the orders per time unit.
type ThroughputCap struct {
	Value    int    `json:"value"`
	TimeUnit string `json:"timeUnit"`
	Unit     string `json:"unit"`
}

// NodeProfile is the body of PUT /vendor/nodes/{nodeId}/profile.
type NodeProfile struct {
	VendorID            string              `json:"vendorId"`
	ProfileRevision     int                 `json:"profileRevision"`
	Timezone            string              `json:"timezone"`
	OperatingHoursByDay map[string]DayHours `json:"operatingHoursByDay"`
	Holidays            []string            `json:"holidays,omitempty"`
	HandlingTimeMinutes int                 `json:"handlingTimeMinutes"`
	Cutoffs             []Cutoff            `json:"cutoffs"`
	ThroughputCap       ThroughputCap       `json:"throughputCap"`
	SortDwellMinutes    *int                `json:"sortDwellMinutes,omitempty"`
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

// Validate checks the shape of a node registration.
func (r NodeRegistration) Validate() error {
	switch {
	case !shared.ValidID(r.VendorID):
		return shared.Invalid("vendorId is not a valid id")
	case !shared.ValidText(r.VendorNodeCode):
		return shared.Invalid("vendorNodeCode must be non-blank, trimmed and at most 100 characters")
	case !shared.ValidID(r.RegionID):
		return shared.Invalid("regionId is not a valid id")
	case !validZone(r.Timezone):
		return shared.Invalid("timezone must be an IANA time zone")
	case !shared.ValidText(r.PostalCode):
		return shared.Invalid("postalCode must be non-blank, trimmed and at most 100 characters")
	case !countryISO.MatchString(r.CountryCode):
		return shared.Invalid("countryCode must be an ISO 3166-1 alpha-2 code")
	case r.Subtype != "" && !validSubtype(r.Subtype):
		return shared.Invalid("subtype is not a node subtype")
	case !validCoord(r.Lat, 90) || !validCoord(r.Lon, 180):
		return shared.Invalid("lat and lon are out of range")
	}
	return nil
}

func validCoord(v *float64, limit float64) bool { return v == nil || (*v >= -limit && *v <= limit) }

func validSubtype(s string) bool {
	switch s {
	case "SORTABLE", "NON_SORTABLE", "XL", "SOFT_LINES", "SUB_SAME_DAY", "FRESH", "MICRO":
		return true
	}
	return false
}

func validZone(z string) bool {
	if z == "" || strings.ContainsAny(z, "\x00") {
		return false
	}
	_, err := time.LoadLocation(z)
	return err == nil
}

// Validate checks the shape of a profile declaration (semantics are node-topology's).
func (r NodeProfile) Validate() error {
	switch {
	case !shared.ValidID(r.VendorID):
		return shared.Invalid("vendorId is not a valid id")
	case r.ProfileRevision < 1 || r.ProfileRevision > shared.MaxInt32:
		return shared.Invalid("profileRevision must be at least 1")
	case !validZone(r.Timezone):
		return shared.Invalid("timezone must be an IANA time zone")
	case r.HandlingTimeMinutes < 1 || r.HandlingTimeMinutes > shared.MaxInt32:
		return shared.Invalid("handlingTimeMinutes must be at least 1")
	case r.SortDwellMinutes != nil && (*r.SortDwellMinutes < 0 || *r.SortDwellMinutes > shared.MaxInt32):
		return shared.Invalid("sortDwellMinutes must not be negative")
	}
	if err := r.validateHours(); err != nil {
		return err
	}
	return r.validateCutoffs()
}

func (r NodeProfile) validateHours() error {
	for day, h := range r.OperatingHoursByDay {
		if !weekdays[day] || !hhmm.MatchString(h.Start) || !hhmm.MatchString(h.End) {
			return shared.Invalid("operatingHoursByDay.%s needs a weekday key and HH:mm start and end", day)
		}
	}
	if len(r.Holidays) > MaxHolidays {
		return shared.Invalid("at most %d holidays", MaxHolidays)
	}
	for _, d := range r.Holidays {
		if !isoDate.MatchString(d) {
			return shared.Invalid("holidays are YYYY-MM-DD dates")
		}
		if _, err := time.Parse("2006-01-02", d); err != nil {
			return shared.Invalid("holiday %q is not a date", d)
		}
	}
	return nil
}

func (r NodeProfile) validateCutoffs() error {
	if len(r.Cutoffs) > MaxCutoffs {
		return shared.Invalid("at most %d cutoffs", MaxCutoffs)
	}
	for i, c := range r.Cutoffs {
		if !hhmm.MatchString(c.LocalTime) || (c.MaxOrders != nil && (*c.MaxOrders < 1 || *c.MaxOrders > shared.MaxInt32)) {
			return shared.Invalid("cutoffs[%d] needs an HH:mm localTime and a positive maxOrders", i)
		}
		for _, d := range c.Days {
			if !weekdays[d] {
				return shared.Invalid("cutoffs[%d].days has %q, not a weekday", i, d)
			}
		}
	}
	c := r.ThroughputCap
	if c.Value < 1 || c.Value > shared.MaxInt32 || (c.TimeUnit != "HOUR" && c.TimeUnit != "DAY") || c.Unit != "ORDER" {
		return shared.Invalid("throughputCap needs a positive value, timeUnit HOUR or DAY and unit ORDER")
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
