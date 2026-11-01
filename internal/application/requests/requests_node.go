package requests

import (
	"strings"
	"time"

	"github.com/claudioed/vendor-connectivity/internal/domain/shared"
)

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
