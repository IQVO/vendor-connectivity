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
