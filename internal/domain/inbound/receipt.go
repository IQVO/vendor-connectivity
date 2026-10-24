package inbound

import (
	"fmt"
	"regexp"
	"time"

	"github.com/claudioed/vendor-connectivity/internal/domain/shared"
	"github.com/claudioed/vendor-connectivity/internal/domain/shared/gs1"
)

// Entity is the CloudEvents entity segment of InboundReceiptSubmitted.
const Entity = "inboundreceipt"

// CartonStatus is what the vendor's dock found for one carton.
type CartonStatus string

// The four carton statuses of a receipt.
const (
	CartonReceived   CartonStatus = "RECEIVED"
	CartonMissing    CartonStatus = "MISSING"
	CartonUnexpected CartonStatus = "UNEXPECTED"
	CartonDamaged    CartonStatus = "DAMAGED"
)

const (
	maxComplianceCodes = 20
	maxReceiptCartons  = maxCartons
)

var complianceCode = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,63}$`)

// ReceiptItem is a counted quantity of one GTIN, in eaches.
type ReceiptItem struct {
	GTIN      string
	QtyEaches int
}

// ReceiptCarton is the finding for one carton.
type ReceiptCarton struct {
	SSCC            string
	Status          CartonStatus
	Items           []ReceiptItem
	ComplianceCodes []string
}

// Receipt is a vendor's inbound receipt as the domain judges it.
type Receipt struct {
	ShipmentID string
	NodeID     string
	ReceivedAt time.Time
	Cartons    []ReceiptCarton
}

func validStatus(s CartonStatus) bool {
	return s == CartonReceived || s == CartonMissing || s == CartonUnexpected || s == CartonDamaged
}

// Normalise checks the shape of a receipt on its own (ids, SSCC and GTIN check
// digits, statuses, quantities, codes) and returns it with every GTIN in the
// 14-digit form. It does not look at any shipment.
func (r Receipt) Normalise() (Receipt, error) {
	if !shared.ValidID(r.ShipmentID) || !shared.ValidID(r.NodeID) {
		return Receipt{}, shared.Invalid("shipmentId and nodeId must be valid ids")
	}
	if r.ReceivedAt.IsZero() {
		return Receipt{}, shared.Invalid("receivedAt is required")
	}
	if len(r.Cartons) == 0 || len(r.Cartons) > maxReceiptCartons {
		return Receipt{}, shared.Invalid("a receipt lists between 1 and %d cartons", maxReceiptCartons)
	}
	out := r
	out.ReceivedAt = r.ReceivedAt.UTC()
	out.Cartons = make([]ReceiptCarton, 0, len(r.Cartons))
	seen := make(map[string]bool, len(r.Cartons))
	for i, c := range r.Cartons {
		n, err := normaliseReceiptCarton(c)
		if err != nil {
			return Receipt{}, shared.Invalid("cartons[%d]: %v", i, err)
		}
		if seen[n.SSCC] {
			return Receipt{}, shared.Invalid("cartons[%d]: sscc %s appears twice", i, n.SSCC)
		}
		seen[n.SSCC] = true
		out.Cartons = append(out.Cartons, n)
	}
	return out, nil
}

func normaliseReceiptCarton(c ReceiptCarton) (ReceiptCarton, error) {
	if _, err := gs1.ParseSSCC(c.SSCC); err != nil {
		return ReceiptCarton{}, err
	}
	if !validStatus(c.Status) {
		return ReceiptCarton{}, fmt.Errorf("status must be RECEIVED, MISSING, UNEXPECTED or DAMAGED")
	}
	if c.Status == CartonMissing && len(c.Items) > 0 {
		return ReceiptCarton{}, fmt.Errorf("a MISSING carton has no items")
	}
	if len(c.Items) > maxItemsPerCarton {
		return ReceiptCarton{}, fmt.Errorf("a carton lists at most %d items", maxItemsPerCarton)
	}
	if len(c.ComplianceCodes) > maxComplianceCodes {
		return ReceiptCarton{}, fmt.Errorf("a carton carries at most %d complianceCodes", maxComplianceCodes)
	}
	for _, code := range c.ComplianceCodes {
		if !complianceCode.MatchString(code) {
			return ReceiptCarton{}, fmt.Errorf("complianceCode %q must be UPPER_SNAKE", code)
		}
	}
	out := c
	out.Items = make([]ReceiptItem, 0, len(c.Items))
	seen := make(map[string]bool, len(c.Items))
	for j, it := range c.Items {
		g, err := ParseGTIN(it.GTIN)
		if err != nil {
			return ReceiptCarton{}, fmt.Errorf("items[%d]: %w", j, err)
		}
		if it.QtyEaches < 0 || it.QtyEaches > maxQtyEaches {
			return ReceiptCarton{}, fmt.Errorf("items[%d]: qtyEaches must be between 0 and %d", j, maxQtyEaches)
		}
		if seen[g] {
			return ReceiptCarton{}, fmt.Errorf("items[%d]: gtin %s appears twice", j, g)
		}
		seen[g] = true
		out.Items = append(out.Items, ReceiptItem{GTIN: g, QtyEaches: it.QtyEaches})
	}
	if out.ComplianceCodes == nil {
		out.ComplianceCodes = []string{}
	}
	return out, nil
}

// CheckReceipt judges a normalised receipt against the shipment the vendor
// received: it must be a SHIPPED shipment of the receipt's node, every carton
// but an UNEXPECTED one must be on its manifest, and the GTINs counted in a
// manifest carton must be GTINs of that carton. Counting MORE than the manifest
// says is a discrepancy to report, never a refusal (goods on the dock are never
// turned away); the quantities are not compared here.
func (s Shipment) CheckReceipt(r Receipt) error {
	if s.NodeID != r.NodeID {
		return shared.Invalid("nodeId %s is not the destination of shipment %s", r.NodeID, s.ID)
	}
	if !s.Pollable() {
		return fmt.Errorf("%w: shipment %s is %s, only a SHIPPED shipment with a manifest can be received", shared.ErrIllegalTransition, s.ID, s.Status)
	}
	for i, c := range r.Cartons {
		m, onManifest := s.Carton(c.SSCC)
		if !onManifest {
			if c.Status != CartonUnexpected {
				return shared.Invalid("cartons[%d]: sscc %s is not on shipment %s (use UNEXPECTED)", i, c.SSCC, s.ID)
			}
			continue
		}
		if err := checkCounted(m, c); err != nil {
			return shared.Invalid("cartons[%d]: %v", i, err)
		}
	}
	return nil
}

func checkCounted(m Carton, c ReceiptCarton) error {
	for j, it := range c.Items {
		found := false
		for _, mi := range m.Contents {
			if mi.GTIN == it.GTIN {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("items[%d]: gtin %s is not in carton %s", j, it.GTIN, c.SSCC)
		}
	}
	return nil
}

// ReceiptSubmitted is raised when the network accepts a vendor's receipt for
// processing. Its fields are the contract's data list; the subject and the
// Kafka key are the shipmentId.
type ReceiptSubmitted struct {
	shared.Header
	TransactionID string
	VendorID      string
	NodeID        string
	ShipmentID    string
	ReceivedAt    time.Time
	Cartons       []ReceiptCarton
}

// NewReceiptSubmitted builds the event for a normalised receipt.
func NewReceiptSubmitted(transactionID, vendorID string, r Receipt, now time.Time) ReceiptSubmitted {
	return ReceiptSubmitted{Header: shared.Header{Version: 1, At: now.UTC()}, TransactionID: transactionID, VendorID: vendorID,
		NodeID: r.NodeID, ShipmentID: r.ShipmentID, ReceivedAt: r.ReceivedAt, Cartons: r.Cartons}
}

// EventName returns "InboundReceiptSubmitted".
func (ReceiptSubmitted) EventName() string { return "InboundReceiptSubmitted" }

// Entity returns "inboundreceipt".
func (ReceiptSubmitted) Entity() string { return Entity }

// SubjectID returns the shipmentId.
func (e ReceiptSubmitted) SubjectID() string { return e.ShipmentID }

var _ shared.Event = ReceiptSubmitted{}
