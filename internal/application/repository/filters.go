// Package repository holds the non-interface vocabulary of the repository
// ports: the list filters and the keyset cursor. It has no behaviour; the
// interfaces are in internal/application/ports.
package repository

import "time"

// Cursor is the keyset position after which a purchase order page continues:
// orders are ordered by (issuedAt, poNumber) ascending.
type Cursor struct {
	IssuedAt time.Time
	PONumber string
}

// PurchaseOrderFilter narrows a purchase order list. Zero fields do not
// filter; set fields combine with AND. Limit is the page size (0 = no limit).
type PurchaseOrderFilter struct {
	VendorID    string
	NodeID      string
	IssuedAfter *time.Time
	After       *Cursor
	Limit       int
}

// ShipmentCursor is the keyset position after which an inbound shipment page
// continues: shipments are ordered by (shippedAt, shipmentId) ascending.
type ShipmentCursor struct {
	ShippedAt  time.Time
	ShipmentID string
}

// InboundShipmentFilter narrows the poll of SHIPPED inbound shipments. NodeIDs
// is the set of nodes the caller may see (never empty); the other fields do not
// filter when zero. Limit is the page size (0 = no limit).
type InboundShipmentFilter struct {
	NodeIDs      []string
	ShippedAfter *time.Time
	After        *ShipmentCursor
	Limit        int
}
