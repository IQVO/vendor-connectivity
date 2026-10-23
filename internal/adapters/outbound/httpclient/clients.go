package httpclient

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/claudioed/vendor-connectivity/internal/application/gateway"
	"github.com/claudioed/vendor-connectivity/internal/application/ports"
	"github.com/claudioed/vendor-connectivity/internal/application/requests"
	"github.com/claudioed/vendor-connectivity/internal/application/views"
)

// NodeTopology calls node-topology (NODE_TOPOLOGY_URL).
type NodeTopology struct{ b base }

// NewNodeTopology builds the client; c may be nil (DefaultTimeout).
func NewNodeTopology(baseURL string, c *http.Client) *NodeTopology {
	return &NodeTopology{b: newBase(baseURL, c)}
}

var _ ports.NodeTopology = (*NodeTopology)(nil)

type vendorRefWire struct {
	VendorID       string `json:"vendorId"`
	VendorNodeCode string `json:"vendorNodeCode"`
}

type locationWire struct {
	PostalCode  string   `json:"postalCode"`
	CountryCode string   `json:"countryCode"`
	Lat         *float64 `json:"lat,omitempty"`
	Lon         *float64 `json:"lon,omitempty"`
}

type registerNodeWire struct {
	NodeType string        `json:"nodeType"`
	Subtype  string        `json:"subtype,omitempty"`
	Operator string        `json:"operator"`
	Vendor   vendorRefWire `json:"vendorRef"`
	RegionID string        `json:"regionId"`
	Timezone string        `json:"timezone"`
	Location locationWire  `json:"location"`
}

type nodeWire struct {
	NodeID    string         `json:"nodeId"`
	NodeType  string         `json:"nodeType"`
	Subtype   string         `json:"subtype"`
	Status    string         `json:"status"`
	VendorRef *vendorRefWire `json:"vendorRef"`
	RegionID  string         `json:"regionId"`
	Timezone  string         `json:"timezone"`
}

// RegisterNode is POST /nodes with operator VENDOR.
func (c *NodeTopology) RegisterNode(ctx context.Context, key string, req requests.NodeRegistration) (gateway.RegisteredNode, error) {
	var out nodeWire
	err := c.b.do(ctx, http.MethodPost, "/nodes", key, registerNodeWire{
		NodeType: req.NodeType, Subtype: req.Subtype, Operator: "VENDOR",
		Vendor:   vendorRefWire{VendorID: req.VendorID, VendorNodeCode: req.VendorNodeCode},
		RegionID: req.RegionID, Timezone: req.Timezone,
		Location: locationWire{PostalCode: req.PostalCode, CountryCode: req.CountryCode, Lat: req.Lat, Lon: req.Lon},
	}, &out)
	if err != nil {
		return gateway.RegisteredNode{}, err
	}
	if out.NodeID == "" {
		return gateway.RegisteredNode{}, fmt.Errorf("%w: POST /nodes: %w", ports.ErrUpstreamUnavailable, errEmpty)
	}
	node := gateway.RegisteredNode{NodeID: out.NodeID, NodeType: out.NodeType, Subtype: out.Subtype, Status: out.Status, RegionID: out.RegionID, Timezone: out.Timezone}
	if out.VendorRef != nil {
		node.VendorID, node.VendorNodeCode = out.VendorRef.VendorID, out.VendorRef.VendorNodeCode
	}
	return node, nil
}

type profileWire struct {
	ProfileRevision     int                          `json:"profileRevision"`
	Timezone            string                       `json:"timezone"`
	OperatingHoursByDay map[string]requests.DayHours `json:"operatingHoursByDay"`
	Holidays            []string                     `json:"holidays,omitempty"`
	HandlingTimeMinutes int                          `json:"handlingTimeMinutes"`
	Cutoffs             []requests.Cutoff            `json:"cutoffs"`
	ThroughputCap       requests.ThroughputCap       `json:"throughputCap"`
	SortDwellMinutes    *int                         `json:"sortDwellMinutes,omitempty"`
	DeclaredBy          string                       `json:"declaredBy"`
}

// DeclareOperationalProfile is PUT /nodes/{nodeId}/operational-profile.
func (c *NodeTopology) DeclareOperationalProfile(ctx context.Context, key, nodeID string, req requests.NodeProfile) error {
	cutoffs := req.Cutoffs
	if cutoffs == nil {
		cutoffs = []requests.Cutoff{}
	}
	return c.b.do(ctx, http.MethodPut, "/nodes/"+url.PathEscape(nodeID)+"/operational-profile", key, profileWire{
		ProfileRevision: req.ProfileRevision, Timezone: req.Timezone, OperatingHoursByDay: req.OperatingHoursByDay, Holidays: req.Holidays,
		HandlingTimeMinutes: req.HandlingTimeMinutes, Cutoffs: cutoffs, ThroughputCap: req.ThroughputCap, SortDwellMinutes: req.SortDwellMinutes,
		DeclaredBy: "VENDOR",
	}, nil)
}

// OfferAvailability calls offer-availability (OFFER_AVAILABILITY_URL).
type OfferAvailability struct{ b base }

// NewOfferAvailability builds the client; c may be nil (DefaultTimeout).
func NewOfferAvailability(baseURL string, c *http.Client) *OfferAvailability {
	return &OfferAvailability{b: newBase(baseURL, c)}
}

var _ ports.OfferAvailability = (*OfferAvailability)(nil)

type feedWire struct {
	FeedID     string                   `json:"feedId"`
	NodeID     string                   `json:"nodeId"`
	FullUpdate bool                     `json:"fullUpdate"`
	AsOf       time.Time                `json:"asOf"`
	Lines      []requests.InventoryLine `json:"lines"`
}

// ApplyInventoryFeed is POST /inventory-feeds; the caller's feedId is also the
// Idempotency-Key, so a retry never applies a feed twice.
func (c *OfferAvailability) ApplyInventoryFeed(ctx context.Context, feedID string, feed requests.InventoryFeed) error {
	lines := feed.Lines
	if lines == nil {
		lines = []requests.InventoryLine{}
	}
	return c.b.do(ctx, http.MethodPost, "/inventory-feeds", feedID, feedWire{FeedID: feedID, NodeID: feed.NodeID, FullUpdate: feed.FullUpdate, AsOf: feed.AsOf, Lines: lines}, nil)
}

// ShippingTracking calls shipping-and-tracking (SHIPPING_AND_TRACKING_URL).
type ShippingTracking struct{ b base }

// NewShippingTracking builds the client; c may be nil (DefaultTimeout).
func NewShippingTracking(baseURL string, c *http.Client) *ShippingTracking {
	return &ShippingTracking{b: newBase(baseURL, c)}
}

var _ ports.ShippingTracking = (*ShippingTracking)(nil)

type issueLabelWire struct {
	PONumber     string `json:"poNumber"`
	PackageNo    int    `json:"packageNo"`
	AssignmentID string `json:"assignmentId"`
	OrderID      string `json:"orderId"`
	Tier         string `json:"tier"`
}

type labelWire struct {
	LabelRef       string `json:"labelRef"`
	TrackingNumber string `json:"trackingNumber"`
	Carrier        string `json:"carrier"`
}

// IssueLabel is POST /labels (Gap G2: orderId and tier come from the release).
func (c *ShippingTracking) IssueLabel(ctx context.Context, key string, call gateway.LabelCall) (gateway.IssuedLabel, error) {
	var out labelWire
	err := c.b.do(ctx, http.MethodPost, "/labels", key, issueLabelWire{PONumber: call.PONumber, PackageNo: call.PackageNo, AssignmentID: call.AssignmentID, OrderID: call.OrderID, Tier: call.Tier}, &out)
	if err != nil {
		return gateway.IssuedLabel{}, err
	}
	if out.LabelRef == "" || out.TrackingNumber == "" {
		return gateway.IssuedLabel{}, fmt.Errorf("%w: POST /labels: %w", ports.ErrUpstreamUnavailable, errEmpty)
	}
	return gateway.IssuedLabel{LabelRef: out.LabelRef, TrackingNumber: out.TrackingNumber, Carrier: out.Carrier}, nil
}

// NodePerformance calls node-performance (NODE_PERFORMANCE_URL).
type NodePerformance struct{ b base }

// NewNodePerformance builds the client; c may be nil (DefaultTimeout).
func NewNodePerformance(baseURL string, c *http.Client) *NodePerformance {
	return &NodePerformance{b: newBase(baseURL, c)}
}

var _ ports.NodePerformance = (*NodePerformance)(nil)

type scorecardWire struct {
	Window     string    `json:"window"`
	Grade      string    `json:"grade"`
	ComputedAt time.Time `json:"computedAt"`
	Metrics    struct {
		OnTimeHandoverRate      *float64 `json:"onTimeHandoverRate"`
		LateAckRate             *float64 `json:"lateAckRate"`
		RejectRate              *float64 `json:"rejectRate"`
		ExpiryRate              *float64 `json:"expiryRate"`
		PromiseKeptRate         *float64 `json:"promiseKeptRate"`
		FeedFreshnessP95Minutes *float64 `json:"feedFreshnessP95Minutes"`
		Observations            int      `json:"observations"`
	} `json:"metrics"`
}

// GetScorecard is GET /scorecards/{nodeId}: one window per scorecard.
func (c *NodePerformance) GetScorecard(ctx context.Context, nodeID string) (views.Scorecard, error) {
	var out struct {
		NodeID     string          `json:"nodeId"`
		Scorecards []scorecardWire `json:"scorecards"`
	}
	if err := c.b.do(ctx, http.MethodGet, "/scorecards/"+url.PathEscape(nodeID), "", nil, &out); err != nil {
		return views.Scorecard{}, err
	}
	sc := views.Scorecard{NodeID: nodeID, Windows: make([]views.ScorecardWindow, 0, len(out.Scorecards))}
	for _, w := range out.Scorecards {
		m := w.Metrics
		sc.Windows = append(sc.Windows, views.ScorecardWindow{
			Window: w.Window, Grade: w.Grade, OnTimeHandoverRate: m.OnTimeHandoverRate, LateAckRate: m.LateAckRate, RejectRate: m.RejectRate,
			ExpiryRate: m.ExpiryRate, PromiseKeptRate: m.PromiseKeptRate, FeedFreshnessP95Minutes: m.FeedFreshnessP95Minutes,
			Observations: m.Observations, ComputedAt: w.ComputedAt,
		})
	}
	return sc, nil
}
