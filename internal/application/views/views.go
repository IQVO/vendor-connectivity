// Package views holds the read DTOs of the application: exactly the Vendor
// API's response schemas (apis/openapi.yaml). They carry JSON tags; optional
// fields are omitted when absent and arrays are always present.
package views

import "time"

// VendorAccount is the vendor account.
type VendorAccount struct {
	VendorID         string `json:"vendorId"`
	Name             string `json:"name"`
	AckWindowMinutes int    `json:"ackWindowMinutes"`
	State            string `json:"state"`
}

// PoLine is one purchase order line.
type PoLine struct {
	LineNo    int    `json:"lineNo"`
	ListingID string `json:"listingId"`
	Quantity  int    `json:"quantity"`
	// Packaging is the active packaging of the listing's catalogue item, from
	// CatalogPackagingView (empty when none was declared).
	Packaging []Packaging `json:"packaging"`
}

// Label is a shipping label; it never carries the ship-to address.
type Label struct {
	PackageNo      int    `json:"packageNo,omitempty"`
	LabelRef       string `json:"labelRef"`
	TrackingNumber string `json:"trackingNumber"`
	Carrier        string `json:"carrier"`
}

// Acknowledgement is the vendor's decision, visible once its transaction succeeded.
type Acknowledgement struct {
	Decision      string `json:"decision"`
	ReasonCode    string `json:"reasonCode,omitempty"`
	TransactionID string `json:"transactionId"`
}

// PurchaseOrder is the Vendor API's purchase order. It carries no ship-to PII,
// no orderId and no delivery promise.
type PurchaseOrder struct {
	PONumber        string           `json:"poNumber"`
	VendorID        string           `json:"vendorId"`
	NodeID          string           `json:"nodeId"`
	State           string           `json:"state"`
	IssuedAt        time.Time        `json:"issuedAt"`
	RequiredShipBy  time.Time        `json:"requiredShipBy"`
	AckBy           time.Time        `json:"ackBy"`
	Lines           []PoLine         `json:"lines"`
	Acknowledgement *Acknowledgement `json:"acknowledgement,omitempty"`
	Labels          []Label          `json:"labels"`
	TrackingNumbers []string         `json:"trackingNumbers"`
	ShippedAt       *time.Time       `json:"shippedAt,omitempty"`
}

// PurchaseOrderPage is one page of the poll.
type PurchaseOrderPage struct {
	PurchaseOrders []PurchaseOrder `json:"purchaseOrders"`
	NextToken      string          `json:"nextToken,omitempty"`
}

// LabelResponse answers a label request.
type LabelResponse struct {
	PONumber string  `json:"poNumber"`
	Labels   []Label `json:"labels"`
}

// TransactionAccepted is the 202 body of an asynchronous write.
type TransactionAccepted struct {
	TransactionID string `json:"transactionId"`
}

// SubmissionError is one error of a failed transaction.
type SubmissionError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Path    string `json:"path,omitempty"`
}

// Transaction is the outcome of an asynchronous write.
type Transaction struct {
	TransactionID string            `json:"transactionId"`
	VendorID      string            `json:"vendorId"`
	Kind          string            `json:"kind"`
	Status        string            `json:"status"`
	ReceivedAt    time.Time         `json:"receivedAt"`
	ProcessedAt   *time.Time        `json:"processedAt,omitempty"`
	Errors        []SubmissionError `json:"errors"`
	NodeID        string            `json:"nodeId,omitempty"`
	PONumber      string            `json:"poNumber,omitempty"`
}

// VendorNode is a node as the network knows it (VendorNodeView).
type VendorNode struct {
	NodeID              string `json:"nodeId"`
	VendorID            string `json:"vendorId"`
	VendorNodeCode      string `json:"vendorNodeCode"`
	NodeType            string `json:"nodeType"`
	Subtype             string `json:"subtype,omitempty"`
	Status              string `json:"status"`
	RegionID            string `json:"regionId,omitempty"`
	Timezone            string `json:"timezone,omitempty"`
	HandlingTimeMinutes int    `json:"handlingTimeMinutes,omitempty"`
	ProfileRevision     int    `json:"profileRevision,omitempty"`
}

// VendorNodeList is the answer of GET /vendor/nodes.
type VendorNodeList struct {
	Nodes []VendorNode `json:"nodes"`
}

// ScorecardWindow is one rolling window of a scorecard.
type ScorecardWindow struct {
	Window                  string    `json:"window"`
	Grade                   string    `json:"grade"`
	OnTimeHandoverRate      *float64  `json:"onTimeHandoverRate,omitempty"`
	LateAckRate             *float64  `json:"lateAckRate,omitempty"`
	RejectRate              *float64  `json:"rejectRate,omitempty"`
	ExpiryRate              *float64  `json:"expiryRate,omitempty"`
	PromiseKeptRate         *float64  `json:"promiseKeptRate,omitempty"`
	FeedFreshnessP95Minutes *float64  `json:"feedFreshnessP95Minutes,omitempty"`
	Observations            int       `json:"observations"`
	ComputedAt              time.Time `json:"computedAt"`
}

// Scorecard is the node's scorecards relayed from node-performance.
type Scorecard struct {
	NodeID  string            `json:"nodeId"`
	Windows []ScorecardWindow `json:"windows"`
}

// Dims is the outer size of a carton in millimetres.
type Dims struct {
	L int `json:"l"`
	W int `json:"w"`
	H int `json:"h"`
}

// ShipmentItem is one SKU line of a carton, in eaches.
type ShipmentItem struct {
	GTIN      string `json:"gtin"`
	SKU       string `json:"sku,omitempty"`
	QtyEaches int    `json:"qtyEaches"`
	Lot       string `json:"lot,omitempty"`
	Expiry    string `json:"expiry,omitempty"`
}

// ShipmentCarton is one carton of the tree; its parentSscc is its pallet.
type ShipmentCarton struct {
	SSCC         string         `json:"sscc"`
	ParentSSCC   string         `json:"parentSscc,omitempty"`
	Kind         string         `json:"kind"`
	Contents     []ShipmentItem `json:"contents"`
	GrossWeightG int            `json:"grossWeightG,omitempty"`
	DimsMm       *Dims          `json:"dimsMm,omitempty"`
}

// ShipmentPallet is a pallet SSCC with the cartons stacked on it.
type ShipmentPallet struct {
	SSCC    string           `json:"sscc"`
	Cartons []ShipmentCarton `json:"cartons"`
}

// InboundShipment is the SOTPI-shaped tree of one shipment: shipment, pallets,
// cartons, items. Cartons that are not on a pallet are top-level.
type InboundShipment struct {
	ShipmentID string           `json:"shipmentId"`
	SellerID   string           `json:"sellerId,omitempty"`
	NodeID     string           `json:"nodeId"`
	ShippedAt  time.Time        `json:"shippedAt"`
	Pallets    []ShipmentPallet `json:"pallets"`
	Cartons    []ShipmentCarton `json:"cartons"`
}

// InboundShipmentPage is one page of the inbound shipment poll.
type InboundShipmentPage struct {
	InboundShipments []InboundShipment `json:"inboundShipments"`
	NextToken        string            `json:"nextToken,omitempty"`
}

// Packaging is one active packaging level of a listing's catalogue item.
type Packaging struct {
	Level        string `json:"level"`
	GTIN         string `json:"gtin"`
	ChildGTIN    string `json:"childGtin,omitempty"`
	UnitsPerPack int    `json:"unitsPerPack"`
}
