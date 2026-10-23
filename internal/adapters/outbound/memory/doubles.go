package memory

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/claudioed/vendor-connectivity/internal/application/gateway"
	"github.com/claudioed/vendor-connectivity/internal/application/ports"
	"github.com/claudioed/vendor-connectivity/internal/application/requests"
	"github.com/claudioed/vendor-connectivity/internal/application/views"
)

// The doubles implement the out ports.
var (
	_ ports.VendorRepository        = (*VendorRepository)(nil)
	_ ports.PurchaseOrderRepository = (*PurchaseOrderRepository)(nil)
	_ ports.SubmissionRepository    = (*SubmissionRepository)(nil)
	_ ports.NodeViewRepository      = (*NodeViewRepository)(nil)
	_ ports.ListingViewRepository   = (*ListingViewRepository)(nil)
	_ ports.ProcessedEvents         = (*Inbox)(nil)
	_ ports.EventOutbox             = (*Outbox)(nil)
	_ ports.UnitOfWork              = (*UnitOfWork)(nil)
	_ ports.Clock                   = (*Clock)(nil)
	_ ports.IDGenerator             = (*SequentialIDs)(nil)
	_ ports.Random                  = (*ScriptedRandom)(nil)
	_ ports.NodeTopology            = (*NodeTopology)(nil)
	_ ports.OfferAvailability       = (*OfferAvailability)(nil)
	_ ports.ShippingTracking        = (*ShippingTracking)(nil)
	_ ports.NodePerformance         = (*NodePerformance)(nil)
)

// Clock is a settable ports.Clock.
type Clock struct {
	mu  sync.Mutex
	now time.Time
}

// NewClock returns a clock reading t.
func NewClock(t time.Time) *Clock { return &Clock{now: t} }

// Now returns the current reading.
func (c *Clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// Set moves the clock to t.
func (c *Clock) Set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = t
}

// Advance moves the clock forward by d.
func (c *Clock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// SequentialIDs is a ports.IDGenerator minting tx-1, tx-2, ...
type SequentialIDs struct {
	mu sync.Mutex
	n  int
}

// NewTransactionID returns the next transaction id.
func (g *SequentialIDs) NewTransactionID() string {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.n++
	return fmt.Sprintf("tx-%d", g.n)
}

// ScriptedRandom is a ports.Random that returns the scripted values in order
// and then Default (0.99: no knob fires by default).
type ScriptedRandom struct {
	mu      sync.Mutex
	Values  []float64
	Default float64
}

// NewScriptedRandom returns a Random answering values, then 0.99.
func NewScriptedRandom(values ...float64) *ScriptedRandom {
	return &ScriptedRandom{Values: values, Default: 0.99}
}

// Float64 returns the next scripted value.
func (r *ScriptedRandom) Float64() float64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.Values) == 0 {
		return r.Default
	}
	v := r.Values[0]
	r.Values = r.Values[1:]
	return v
}

// NodeTopology is a scripted ports.NodeTopology. Set Err to fail every call;
// Registered is the answer of RegisterNode.
type NodeTopology struct {
	mu         sync.Mutex
	Err        error
	Registered gateway.RegisteredNode
	Registers  []requests.NodeRegistration
	Profiles   []requests.NodeProfile
	Keys       []string
}

// RegisterNode records the call and answers Registered.
func (n *NodeTopology) RegisterNode(_ context.Context, key string, req requests.NodeRegistration) (gateway.RegisteredNode, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.Keys = append(n.Keys, key)
	n.Registers = append(n.Registers, req)
	return n.Registered, n.Err
}

// DeclareOperationalProfile records the call.
func (n *NodeTopology) DeclareOperationalProfile(_ context.Context, key, _ string, req requests.NodeProfile) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.Keys = append(n.Keys, key)
	n.Profiles = append(n.Profiles, req)
	return n.Err
}

// OfferAvailability is a scripted ports.OfferAvailability.
type OfferAvailability struct {
	mu      sync.Mutex
	Err     error
	Feeds   []requests.InventoryFeed
	FeedIDs []string
}

// ApplyInventoryFeed records the call.
func (o *OfferAvailability) ApplyInventoryFeed(_ context.Context, feedID string, feed requests.InventoryFeed) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.FeedIDs = append(o.FeedIDs, feedID)
	o.Feeds = append(o.Feeds, feed)
	return o.Err
}

// ShippingTracking is a scripted ports.ShippingTracking: it issues
// "lbl-<po>-<n>" / "TRK-<po>-<n>" labels, or fails with ErrAt for the package
// number FailPackage (0 = never).
type ShippingTracking struct {
	mu          sync.Mutex
	Err         error
	FailPackage int
	Calls       []gateway.LabelCall
	Keys        []string
}

// IssueLabel records the call and issues a label.
func (s *ShippingTracking) IssueLabel(_ context.Context, key string, call gateway.LabelCall) (gateway.IssuedLabel, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Keys = append(s.Keys, key)
	s.Calls = append(s.Calls, call)
	if s.Err != nil && (s.FailPackage == 0 || s.FailPackage == call.PackageNo) {
		return gateway.IssuedLabel{}, s.Err
	}
	return gateway.IssuedLabel{LabelRef: fmt.Sprintf("lbl-%s-%d", call.PONumber, call.PackageNo),
		TrackingNumber: fmt.Sprintf("TRK-%s-%d", call.PONumber, call.PackageNo), Carrier: "carrier-a"}, nil
}

// NodePerformance is a scripted ports.NodePerformance.
type NodePerformance struct {
	Err       error
	Scorecard views.Scorecard
}

// GetScorecard answers the scripted scorecard.
func (p *NodePerformance) GetScorecard(_ context.Context, nodeID string) (views.Scorecard, error) {
	if p.Err != nil {
		return views.Scorecard{}, p.Err
	}
	sc := p.Scorecard
	sc.NodeID = nodeID
	return sc, nil
}
