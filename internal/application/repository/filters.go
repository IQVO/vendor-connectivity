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
