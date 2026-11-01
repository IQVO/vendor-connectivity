package requests

import (
	"time"

	"github.com/claudioed/vendor-connectivity/internal/domain/shared"
)

// MaxReceiptCartons is the most cartons one inbound receipt may list.
const MaxReceiptCartons = 5000

// InboundReceiptItem is a counted quantity of one GTIN, in eaches.
type InboundReceiptItem struct {
	GTIN      string `json:"gtin"`
	QtyEaches int    `json:"qtyEaches"`
}

// InboundReceiptCarton is the dock's finding for one carton.
type InboundReceiptCarton struct {
	SSCC            string               `json:"sscc"`
	Status          string               `json:"status"`
	Items           []InboundReceiptItem `json:"items,omitempty"`
	ComplianceCodes []string             `json:"complianceCodes,omitempty"`
}

// InboundReceipt is the body of POST /vendor/inbound-receipts.
type InboundReceipt struct {
	VendorID   string                 `json:"vendorId"`
	ShipmentID string                 `json:"shipmentId"`
	NodeID     string                 `json:"nodeId"`
	ReceivedAt time.Time              `json:"receivedAt"`
	Cartons    []InboundReceiptCarton `json:"cartons"`
}

// Validate checks what can be checked without the shipment.
func (r InboundReceipt) Validate() error {
	if !shared.ValidID(r.VendorID) {
		return shared.Invalid("vendorId must be a valid id")
	}
	return nil
}

// InboundPollQuery is the query of GET /vendor/inbound-shipments.
type InboundPollQuery struct {
	VendorID     string
	NodeID       string
	CreatedAfter *time.Time
	NextToken    string
	Limit        int
}
