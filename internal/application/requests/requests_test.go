package requests_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/claudioed/vendor-connectivity/internal/application/requests"
	"github.com/claudioed/vendor-connectivity/internal/domain/shared"
)

func reg() requests.NodeRegistration {
	return requests.NodeRegistration{VendorID: "V-1", VendorNodeCode: "SITE-1", NodeType: "FULFILLMENT_CENTER", Subtype: "XL", RegionID: "r-1",
		Timezone: "America/Sao_Paulo", PostalCode: "07750-000", CountryCode: "BR"}
}

func profile() requests.NodeProfile {
	n := 5
	return requests.NodeProfile{VendorID: "V-1", ProfileRevision: 1, Timezone: "UTC", HandlingTimeMinutes: 30,
		OperatingHoursByDay: map[string]requests.DayHours{"monday": {Start: "06:00", End: "22:00"}}, Holidays: []string{"2026-12-25"},
		Cutoffs:       []requests.Cutoff{{LocalTime: "18:00", Days: []string{"monday"}, MaxOrders: &n}},
		ThroughputCap: requests.ThroughputCap{Value: 10, TimeUnit: "DAY", Unit: "ORDER"}}
}

func feed() requests.InventoryFeed {
	return requests.InventoryFeed{VendorID: "V-1", NodeID: "n-1", AsOf: time.Now(), Lines: []requests.InventoryLine{{ListingID: "l-1", AvailableQty: 1, Basis: "PHYSICAL"}}}
}

type validator interface{ Validate() error }

func check(t *testing.T, name string, v validator, wantOK bool) {
	t.Helper()
	err := v.Validate()
	if wantOK && err != nil {
		t.Errorf("%s: %v", name, err)
	}
	if !wantOK && !errors.Is(err, shared.ErrValidation) {
		t.Errorf("%s: err = %v, want ErrValidation", name, err)
	}
}

func TestOnboardVendorValidate(t *testing.T) {
	check(t, "good", requests.OnboardVendor{VendorID: "V-1", Name: "n"}, true)
	check(t, "bad id", requests.OnboardVendor{VendorID: "a b", Name: "n"}, false)
	check(t, "bad name", requests.OnboardVendor{VendorID: "V-1", Name: ""}, false)
	check(t, "long name", requests.OnboardVendor{VendorID: "V-1", Name: strings.Repeat("n", 121)}, false)
}

func TestNodeRegistrationValidate(t *testing.T) {
	check(t, "good", reg(), true)
	lat, lon, badLat := 10.0, -170.0, 91.0
	g := reg()
	g.Lat, g.Lon = &lat, &lon
	check(t, "with coordinates", g, true)
	g.Subtype = ""
	check(t, "no subtype", g, true)
	mutations := map[string]func(*requests.NodeRegistration){
		"vendor":   func(r *requests.NodeRegistration) { r.VendorID = "" },
		"code":     func(r *requests.NodeRegistration) { r.VendorNodeCode = " " },
		"region":   func(r *requests.NodeRegistration) { r.RegionID = "" },
		"zone":     func(r *requests.NodeRegistration) { r.Timezone = "Nowhere/Land" },
		"no zone":  func(r *requests.NodeRegistration) { r.Timezone = "" },
		"postal":   func(r *requests.NodeRegistration) { r.PostalCode = "" },
		"country":  func(r *requests.NodeRegistration) { r.CountryCode = "br" },
		"country3": func(r *requests.NodeRegistration) { r.CountryCode = "BRA" },
		"subtype":  func(r *requests.NodeRegistration) { r.Subtype = "HUGE" },
		"lat":      func(r *requests.NodeRegistration) { r.Lat = &badLat },
		"lon":      func(r *requests.NodeRegistration) { badLon := 181.0; r.Lon = &badLon },
	}
	for name, mut := range mutations {
		r := reg()
		mut(&r)
		check(t, name, r, false)
	}
}

func TestNodeProfileValidate(t *testing.T) {
	check(t, "good", profile(), true)
	neg, bigN := -1, 0
	mutations := map[string]func(*requests.NodeProfile){
		"vendor":   func(p *requests.NodeProfile) { p.VendorID = "" },
		"revision": func(p *requests.NodeProfile) { p.ProfileRevision = 0 },
		"zone":     func(p *requests.NodeProfile) { p.Timezone = "x/y" },
		"handling": func(p *requests.NodeProfile) { p.HandlingTimeMinutes = 0 },
		"dwell":    func(p *requests.NodeProfile) { p.SortDwellMinutes = &neg },
		"weekday": func(p *requests.NodeProfile) {
			p.OperatingHoursByDay = map[string]requests.DayHours{"funday": {Start: "06:00", End: "22:00"}}
		},
		"hours": func(p *requests.NodeProfile) {
			p.OperatingHoursByDay = map[string]requests.DayHours{"monday": {Start: "6:00", End: "22:00"}}
		},
		"hours end": func(p *requests.NodeProfile) {
			p.OperatingHoursByDay = map[string]requests.DayHours{"monday": {Start: "06:00", End: "24:00"}}
		},
		"holiday shape": func(p *requests.NodeProfile) { p.Holidays = []string{"25/12/2026"} },
		"holiday date":  func(p *requests.NodeProfile) { p.Holidays = []string{"2026-13-45"} },
		"holidays many": func(p *requests.NodeProfile) { p.Holidays = make([]string, requests.MaxHolidays+1) },
		"cutoff time":   func(p *requests.NodeProfile) { p.Cutoffs[0].LocalTime = "6pm" },
		"cutoff orders": func(p *requests.NodeProfile) { p.Cutoffs[0].MaxOrders = &bigN },
		"cutoff day":    func(p *requests.NodeProfile) { p.Cutoffs[0].Days = []string{"someday"} },
		"cutoffs many":  func(p *requests.NodeProfile) { p.Cutoffs = make([]requests.Cutoff, requests.MaxCutoffs+1) },
		"cap value":     func(p *requests.NodeProfile) { p.ThroughputCap.Value = 0 },
		"cap unit time": func(p *requests.NodeProfile) { p.ThroughputCap.TimeUnit = "WEEK" },
		"cap unit":      func(p *requests.NodeProfile) { p.ThroughputCap.Unit = "PALLET" },
	}
	for name, mut := range mutations {
		p := profile()
		p.Cutoffs = append([]requests.Cutoff(nil), p.Cutoffs...)
		mut(&p)
		check(t, name, p, false)
	}
	dwell := 15
	p := profile()
	p.SortDwellMinutes = &dwell
	p.Cutoffs = nil
	check(t, "dwell and no cutoffs are node-topology's call", p, true)
}

func TestInventoryFeedValidate(t *testing.T) {
	check(t, "good", feed(), true)
	empty := feed()
	empty.Lines = nil
	check(t, "empty partial feed", empty, true)
	mutations := map[string]func(*requests.InventoryFeed){
		"vendor":    func(f *requests.InventoryFeed) { f.VendorID = "" },
		"node":      func(f *requests.InventoryFeed) { f.NodeID = "" },
		"asOf":      func(f *requests.InventoryFeed) { f.AsOf = time.Time{} },
		"listing":   func(f *requests.InventoryFeed) { f.Lines[0].ListingID = "" },
		"negative":  func(f *requests.InventoryFeed) { f.Lines[0].AvailableQty = -1 },
		"basis":     func(f *requests.InventoryFeed) { f.Lines[0].Basis = "GUESS" },
		"duplicate": func(f *requests.InventoryFeed) { f.Lines = append(f.Lines, f.Lines[0]) },
		"too many": func(f *requests.InventoryFeed) {
			f.Lines = make([]requests.InventoryLine, requests.MaxInventoryLines+1)
		},
	}
	for name, mut := range mutations {
		f := feed()
		f.Lines = append([]requests.InventoryLine(nil), f.Lines...)
		mut(&f)
		check(t, name, f, false)
	}
}

func TestPoRequestsValidate(t *testing.T) {
	check(t, "ack", requests.Acknowledgement{VendorID: "V-1", PONumber: "PO-1"}, true)
	check(t, "ack vendor", requests.Acknowledgement{PONumber: "PO-1"}, false)
	check(t, "ack po", requests.Acknowledgement{VendorID: "V-1"}, false)
	check(t, "label", requests.LabelRequest{VendorID: "V-1", PONumber: "PO-1"}, true)
	check(t, "label vendor", requests.LabelRequest{PONumber: "PO-1"}, false)
	check(t, "label po", requests.LabelRequest{VendorID: "V-1"}, false)
	check(t, "ship", requests.ShipmentConfirmation{VendorID: "V-1", PONumber: "PO-1"}, true)
	check(t, "ship vendor", requests.ShipmentConfirmation{PONumber: "PO-1"}, false)
	check(t, "ship po", requests.ShipmentConfirmation{VendorID: "V-1"}, false)
}
