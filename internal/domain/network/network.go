// Package network holds the two event-fed read models the vendor-connectivity
// decisions need: Node (the model's VendorNodeView) and Listing (ListingView).
// They are rebuilt from node-topology and offer-availability events (ADR 0003)
// and carry the ownership rule (ErrNodeNotOwnedByVendor).
//
// Both are plain values: an event produces a new value from the stored one, so
// the merge rules (a profile revision never goes down, RETIRED is final) are
// pure and testable. Events about a node the view has not seen yet create a
// stub that the later NodeRegistered fills.
package network

import (
	"errors"

	"github.com/claudioed/vendor-connectivity/internal/domain/shared"
)

// ErrNodeNotOwnedByVendor is raised when a vendor addresses a node that is not
// its own (problem type node-not-owned-by-vendor, 404 so a vendor cannot probe
// another vendor's nodes).
var ErrNodeNotOwnedByVendor = errors.New("node not owned by vendor")

// Operator says who runs a node.
type Operator string

// Node operators (node-topology NodeOperator). Unknown is a stub created by an
// event that arrived before NodeRegistered.
const (
	OperatorUnknown Operator = ""
	OperatorNetwork Operator = "NETWORK"
	OperatorVendor  Operator = "VENDOR"
)

// Status is the node status node-topology last announced.
type Status string

// Node statuses (node-topology NodeStatus).
const (
	StatusProposed  Status = "PROPOSED"
	StatusActive    Status = "ACTIVE"
	StatusSuspended Status = "SUSPENDED"
	StatusRetired   Status = "RETIRED"
)

// Node types a vendor may register through the Vendor API; parcel nodes are
// network-operated.
const (
	TypeFulfillmentCenter = "FULFILLMENT_CENTER"
	TypeInboundCrossDock  = "INBOUND_CROSS_DOCK"
	TypeReturnsCenter     = "RETURNS_CENTER"
)

// VendorMayRegister reports whether a vendor may register a node of this type.
func VendorMayRegister(nodeType string) bool {
	switch nodeType {
	case TypeFulfillmentCenter, TypeInboundCrossDock, TypeReturnsCenter:
		return true
	}
	return false
}

// Node is the VendorNodeView row of one node.
type Node struct {
	ID                  string
	Operator            Operator
	VendorID            string
	VendorNodeCode      string
	NodeType            string
	Subtype             string
	RegionID            string
	Timezone            string
	Status              Status
	HandlingTimeMinutes int
	ProfileRevision     int
}

// Registered carries the NodeRegistered facts the view keeps.
type Registered struct {
	NodeID         string
	NodeType       string
	Subtype        string
	Operator       Operator
	VendorID       string
	VendorNodeCode string
	RegionID       string
	Timezone       string
}

// OnRegistered merges a NodeRegistered into the stored node (zero value when
// unseen). The status of a stub stays what later events set; a brand new node
// is PROPOSED. The profile fields are never touched here.
func (n Node) OnRegistered(r Registered) (Node, error) {
	if !shared.ValidID(r.NodeID) {
		return Node{}, shared.Invalid("nodeId is not a valid id")
	}
	if r.Operator != OperatorNetwork && r.Operator != OperatorVendor {
		return Node{}, shared.Invalid("operator must be NETWORK or VENDOR")
	}
	if r.Operator == OperatorVendor && (r.VendorID == "" || r.VendorNodeCode == "") {
		return Node{}, shared.Invalid("a VENDOR node needs vendorId and vendorNodeCode")
	}
	out := n
	out.ID, out.Operator, out.NodeType, out.Subtype = r.NodeID, r.Operator, r.NodeType, r.Subtype
	out.VendorID, out.VendorNodeCode, out.RegionID, out.Timezone = r.VendorID, r.VendorNodeCode, r.RegionID, r.Timezone
	if out.Status == "" {
		out.Status = StatusProposed
	}
	return out, nil
}

// OnProfileDeclared applies a NodeOperationalProfileDeclared. A revision that
// is not higher than the stored one is ignored (events may be redelivered or
// reordered), so the handling time never goes back.
func (n Node) OnProfileDeclared(nodeID string, revision, handlingTimeMinutes int) (Node, error) {
	if !shared.ValidID(nodeID) {
		return Node{}, shared.Invalid("nodeId is not a valid id")
	}
	if revision < 1 || handlingTimeMinutes < 0 {
		return Node{}, shared.Invalid("profile revision must be at least 1 and the handling time not negative")
	}
	out := n
	out.ID = nodeID
	if revision > n.ProfileRevision {
		out.ProfileRevision, out.HandlingTimeMinutes = revision, handlingTimeMinutes
	}
	return out, nil
}

// OnStatus applies NodeActivated, NodeSuspended or NodeRetired. RETIRED is
// final: no later event changes it.
func (n Node) OnStatus(nodeID string, status Status) (Node, error) {
	if !shared.ValidID(nodeID) {
		return Node{}, shared.Invalid("nodeId is not a valid id")
	}
	switch status {
	case StatusActive, StatusSuspended, StatusRetired:
	default:
		return Node{}, shared.Invalid("status must be ACTIVE, SUSPENDED or RETIRED")
	}
	out := n
	out.ID = nodeID
	if n.Status != StatusRetired {
		out.Status = status
	}
	return out, nil
}

// VendorOperated reports whether the node is run by a vendor.
func (n Node) VendorOperated() bool { return n.Operator == OperatorVendor }

// EnsureOwnedBy returns ErrNodeNotOwnedByVendor unless the node is a VENDOR node
// of vendorID.
func (n Node) EnsureOwnedBy(vendorID string) error {
	if n.Operator != OperatorVendor || vendorID == "" || n.VendorID != vendorID {
		return ErrNodeNotOwnedByVendor
	}
	return nil
}

// Listing is the ListingView row: one catalogue item offered from one node.
type Listing struct {
	ID            string
	NodeID        string
	CatalogItemID string
}

// NewListing validates a ListingCreated into a Listing.
func NewListing(listingID, nodeID, catalogItemID string) (Listing, error) {
	if !shared.ValidID(listingID) || !shared.ValidID(nodeID) {
		return Listing{}, shared.Invalid("listingId and nodeId must be valid ids")
	}
	return Listing{ID: listingID, NodeID: nodeID, CatalogItemID: catalogItemID}, nil
}
