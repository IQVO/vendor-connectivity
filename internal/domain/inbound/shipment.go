// Package inbound holds the inbound-shipment side of the Vendor API: the
// InboundShipmentView read model fed by seller-inbound's events, the rules a
// vendor's inbound receipt must satisfy against it, and the
// InboundReceiptSubmitted event (ADR 0005).
//
// The view is a projection, not an aggregate: it is rebuilt from events that
// may arrive twice or out of order, so every transition only moves forward
// (LABELLED, then SHIPPED, then CLOSED or CLOSED_WITH_DISCREPANCY).
package inbound

import (
	"fmt"
	"time"

	"github.com/claudioed/vendor-connectivity/internal/domain/shared"
	"github.com/claudioed/vendor-connectivity/internal/domain/shared/gs1"
)

// Status is the lifecycle position of an inbound shipment as seller-inbound
// reports it. The zero value is "no event seen yet".
type Status string

// The statuses the view tracks.
const (
	StatusLabelled              Status = "LABELLED"
	StatusShipped               Status = "SHIPPED"
	StatusClosed                Status = "CLOSED"
	StatusClosedWithDiscrepancy Status = "CLOSED_WITH_DISCREPANCY"
	maxCartons                         = 5000
	maxItemsPerCarton                  = 500
	maxLot                             = 20
	maxKind                            = 32
	maxDimMillimetres                  = shared.MaxInt32
	maxGrossWeightGrams                = shared.MaxInt32
	maxQtyEaches                       = shared.MaxInt32
	isoDateLayout                      = "2006-01-02"
)

// rank orders the statuses so a late or repeated event never moves a shipment back.
func rank(s Status) int {
	if s == StatusLabelled {
		return 1
	}
	if s == StatusShipped {
		return 2
	}
	if s == StatusClosed || s == StatusClosedWithDiscrepancy {
		return 3
	}
	return 0
}

// Dims is the outer size of a carton in millimetres.
type Dims struct{ L, W, H int }

// Item is one SKU line inside a carton, in eaches.
type Item struct {
	CatalogItemID string
	GTIN          string
	QtyEaches     int
	Lot           string
	// Expiry is an ISO date (YYYY-MM-DD) or empty.
	Expiry string
}

// Carton is one logistic unit: a case, or a mixed carton. A pallet is a Carton
// whose SSCC is the ParentSSCC of others and which has no contents of its own.
type Carton struct {
	SSCC         string
	ParentSSCC   string
	Kind         string
	Contents     []Item
	GrossWeightG int
	Dims         *Dims
}

// Shipment is one row of InboundShipmentView.
type Shipment struct {
	ID         string
	PlanID     string
	SellerID   string
	NodeID     string
	Status     Status
	Cartons    []Carton
	LabelledAt time.Time
	ShippedAt  time.Time
	ReceivedAt time.Time
}

// Labelled carries InboundShipmentLabelled.
type Labelled struct {
	ShipmentID string
	PlanID     string
	SellerID   string
	NodeID     string
	Cartons    []Carton
	LabelledAt time.Time
}

// Shipped carries InboundShipmentShipped.
type Shipped struct {
	ShipmentID string
	PlanID     string
	SellerID   string
	NodeID     string
	ShippedAt  time.Time
}

// Received carries InboundShipmentReceived (only what the view needs).
type Received struct {
	ShipmentID string
	SellerID   string
	NodeID     string
	Status     Status
	ReceivedAt time.Time
}

func checkIDs(shipmentID, nodeID, sellerID string) error {
	if !shared.ValidID(shipmentID) || !shared.ValidID(nodeID) {
		return shared.Invalid("shipmentId and nodeId must be valid ids")
	}
	if sellerID != "" && !shared.ValidID(sellerID) {
		return shared.Invalid("sellerId must be a valid id")
	}
	return nil
}

// merge copies the identity facts of an event onto the row, keeping what the
// row already knows when the event does not say.
func (s Shipment) merge(shipmentID, planID, sellerID, nodeID string) Shipment {
	out := s
	out.ID, out.NodeID = shipmentID, nodeID
	if planID != "" {
		out.PlanID = planID
	}
	if sellerID != "" {
		out.SellerID = sellerID
	}
	return out
}

func (s Shipment) advance(to Status) Shipment {
	out := s
	if rank(to) > rank(s.Status) {
		out.Status = to
	}
	return out
}

// OnLabelled applies InboundShipmentLabelled: the manifest is replaced (the
// latest label wins) and the status never moves back.
func (s Shipment) OnLabelled(e Labelled) (Shipment, error) {
	if err := checkIDs(e.ShipmentID, e.NodeID, e.SellerID); err != nil {
		return Shipment{}, err
	}
	if e.PlanID != "" && !shared.ValidID(e.PlanID) {
		return Shipment{}, shared.Invalid("planId must be a valid id")
	}
	cartons, err := normaliseCartons(e.Cartons)
	if err != nil {
		return Shipment{}, err
	}
	out := s.merge(e.ShipmentID, e.PlanID, e.SellerID, e.NodeID).advance(StatusLabelled)
	out.Cartons, out.LabelledAt = cartons, shared.UTC(e.LabelledAt)
	return out, nil
}

// OnShipped applies InboundShipmentShipped.
func (s Shipment) OnShipped(e Shipped) (Shipment, error) {
	if err := checkIDs(e.ShipmentID, e.NodeID, e.SellerID); err != nil {
		return Shipment{}, err
	}
	if e.ShippedAt.IsZero() {
		return Shipment{}, shared.Invalid("shippedAt is required")
	}
	out := s.merge(e.ShipmentID, e.PlanID, e.SellerID, e.NodeID).advance(StatusShipped)
	out.ShippedAt = shared.UTC(e.ShippedAt)
	return out, nil
}

// OnReceived applies InboundShipmentReceived.
func (s Shipment) OnReceived(e Received) (Shipment, error) {
	if err := checkIDs(e.ShipmentID, e.NodeID, e.SellerID); err != nil {
		return Shipment{}, err
	}
	if e.Status != StatusClosed && e.Status != StatusClosedWithDiscrepancy {
		return Shipment{}, shared.Invalid("status must be CLOSED or CLOSED_WITH_DISCREPANCY")
	}
	out := s.merge(e.ShipmentID, "", e.SellerID, e.NodeID)
	if rank(s.Status) < rank(e.Status) {
		out.Status = e.Status
	}
	out.ReceivedAt = shared.UTC(e.ReceivedAt)
	return out, nil
}

// Pollable reports whether the vendor sees the shipment in the poll: shipped
// and labelled (a SHIPPED row whose label event has not arrived yet has no manifest).
func (s Shipment) Pollable() bool {
	return s.Status == StatusShipped && len(s.Cartons) > 0
}

// Carton returns the manifest carton with the SSCC.
func (s Shipment) Carton(sscc string) (Carton, bool) {
	for _, c := range s.Cartons {
		if c.SSCC == sscc {
			return c, true
		}
	}
	return Carton{}, false
}

// normaliseCartons validates a manifest and rewrites every GTIN to its
// 14-digit form (fleet rule G2).
func normaliseCartons(in []Carton) ([]Carton, error) {
	if len(in) > maxCartons {
		return nil, shared.Invalid("a shipment carries at most %d cartons", maxCartons)
	}
	seen := make(map[string]bool, len(in))
	out := make([]Carton, 0, len(in))
	for i, c := range in {
		n, err := normaliseCarton(c)
		if err != nil {
			return nil, shared.Invalid("cartons[%d]: %v", i, err)
		}
		if seen[n.SSCC] {
			return nil, shared.Invalid("cartons[%d]: sscc %s appears twice", i, n.SSCC)
		}
		seen[n.SSCC] = true
		out = append(out, n)
	}
	return out, nil
}

func normaliseCarton(c Carton) (Carton, error) {
	if _, err := gs1.ParseSSCC(c.SSCC); err != nil {
		return Carton{}, err
	}
	if c.ParentSSCC != "" {
		if _, err := gs1.ParseSSCC(c.ParentSSCC); err != nil {
			return Carton{}, err
		}
	}
	if !shared.ValidTextN(c.Kind, maxKind) {
		return Carton{}, fmt.Errorf("kind is required")
	}
	if c.GrossWeightG < 0 || c.GrossWeightG > maxGrossWeightGrams {
		return Carton{}, fmt.Errorf("grossWeightG is out of range")
	}
	if len(c.Contents) > maxItemsPerCarton {
		return Carton{}, fmt.Errorf("a carton holds at most %d item lines", maxItemsPerCarton)
	}
	if d := c.Dims; d != nil && (d.L < 0 || d.W < 0 || d.H < 0 || d.L > maxDimMillimetres || d.W > maxDimMillimetres || d.H > maxDimMillimetres) {
		return Carton{}, fmt.Errorf("dimsMm is out of range")
	}
	out := c
	out.Contents = make([]Item, 0, len(c.Contents))
	for j, it := range c.Contents {
		n, err := normaliseItem(it)
		if err != nil {
			return Carton{}, fmt.Errorf("contents[%d]: %v", j, err)
		}
		out.Contents = append(out.Contents, n)
	}
	return out, nil
}

func normaliseItem(it Item) (Item, error) {
	g, err := ParseGTIN(it.GTIN)
	if err != nil {
		return Item{}, err
	}
	if it.QtyEaches < 1 || it.QtyEaches > maxQtyEaches {
		return Item{}, fmt.Errorf("qtyEaches must be between 1 and %d", maxQtyEaches)
	}
	if it.Lot != "" && !shared.ValidTextN(it.Lot, maxLot) {
		return Item{}, fmt.Errorf("lot is not valid text of at most %d characters", maxLot)
	}
	if it.Expiry != "" {
		if _, err := time.Parse(isoDateLayout, it.Expiry); err != nil {
			return Item{}, fmt.Errorf("expiry must be an ISO date")
		}
	}
	out := it
	out.GTIN = g
	return out, nil
}

// ParseGTIN validates a GTIN with the shared GS1 value object, refuses the
// variable-measure indicator (fleet rule G7) and returns the 14-digit form.
func ParseGTIN(s string) (string, error) {
	g, err := gs1.ParseGTIN(s)
	if err != nil {
		return "", fmt.Errorf("gtin %q: %v", s, err)
	}
	if err := gs1.RejectVariableMeasure(g); err != nil {
		return "", fmt.Errorf("gtin %q: %v", s, err)
	}
	return g.String(), nil
}
