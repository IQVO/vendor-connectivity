// Package ports declares the application's OUT ports: interfaces only
// (enforced by internal/architecture). Their non-interface vocabulary is the
// sentinels of errors.go, the filters of internal/application/repository and the
// neighbour value types of internal/application/gateway.
//
// Optimistic concurrency: every Save of an aggregate expects the stored version
// to be aggregate.Version()-1 (ADR 0002); a create (Version() 1) expects that
// the id is not stored. Any mismatch, including an update of a row that no
// longer exists, is ErrConcurrentUpdate. Inside a UnitOfWork every repository
// joins the transaction carried by the ctx the unit handed to its function.
package ports

import (
	"context"
	"time"

	"github.com/claudioed/vendor-connectivity/internal/application/gateway"
	"github.com/claudioed/vendor-connectivity/internal/application/repository"
	"github.com/claudioed/vendor-connectivity/internal/application/requests"
	"github.com/claudioed/vendor-connectivity/internal/application/views"
	"github.com/claudioed/vendor-connectivity/internal/domain/inbound"
	"github.com/claudioed/vendor-connectivity/internal/domain/network"
	"github.com/claudioed/vendor-connectivity/internal/domain/purchaseorder"
	"github.com/claudioed/vendor-connectivity/internal/domain/shared"
	"github.com/claudioed/vendor-connectivity/internal/domain/submission"
	"github.com/claudioed/vendor-connectivity/internal/domain/vendor"
)

// VendorRepository persists VendorAccount aggregates.
type VendorRepository interface {
	// Get returns the account, or ErrNotFound.
	Get(ctx context.Context, id vendor.ID) (*vendor.Account, error)
	// Save persists the account under the concurrency rule of the package comment.
	Save(ctx context.Context, a *vendor.Account) error
}

// PurchaseOrderRepository persists PurchaseOrder aggregates.
type PurchaseOrderRepository interface {
	// Get returns the order, or ErrNotFound.
	Get(ctx context.Context, n purchaseorder.Number) (*purchaseorder.PurchaseOrder, error)
	// FindByRelease returns the order issued for (assignmentId, releaseNo), or ErrNotFound.
	FindByRelease(ctx context.Context, assignmentID string, releaseNo int) (*purchaseorder.PurchaseOrder, error)
	// FindLatestByAssignment returns the order with the highest releaseNo of the
	// assignment (assumption A16), or ErrNotFound.
	FindLatestByAssignment(ctx context.Context, assignmentID string) (*purchaseorder.PurchaseOrder, error)
	// List returns the orders matching filter ordered by (issuedAt, poNumber)
	// ascending, in every state; no match is an empty list, never an error.
	List(ctx context.Context, filter repository.PurchaseOrderFilter) ([]*purchaseorder.PurchaseOrder, error)
	// ListExpirable returns up to limit ISSUED orders whose ackBy is before now,
	// oldest ackBy first.
	ListExpirable(ctx context.Context, now time.Time, limit int) ([]*purchaseorder.PurchaseOrder, error)
	// Save persists the order under the concurrency rule of the package comment.
	// A second order for the same (assignmentId, releaseNo) is ErrConcurrentUpdate.
	Save(ctx context.Context, p *purchaseorder.PurchaseOrder) error
}

// SubmissionRepository persists Submission aggregates.
type SubmissionRepository interface {
	// Get returns the submission, or ErrNotFound.
	Get(ctx context.Context, id submission.ID) (*submission.Submission, error)
	// ListDue returns up to limit PROCESSING submissions whose notBefore is not
	// after now, oldest notBefore first.
	ListDue(ctx context.Context, now time.Time, limit int) ([]*submission.Submission, error)
	// Save persists the submission under the concurrency rule of the package comment.
	Save(ctx context.Context, s *submission.Submission) error
	// DeleteProcessedBefore removes processed submissions whose processedAt is
	// before t and returns how many (assumption A22).
	DeleteProcessedBefore(ctx context.Context, t time.Time) (int, error)
}

// NodeViewRepository stores the VendorNodeView read model (upsert, no version).
type NodeViewRepository interface {
	// Get returns the node, or ErrNotFound.
	Get(ctx context.Context, nodeID string) (network.Node, error)
	// ListByVendor returns the nodes owned by the vendor ordered by id ascending.
	ListByVendor(ctx context.Context, vendorID string) ([]network.Node, error)
	// Save inserts or replaces the node row.
	Save(ctx context.Context, n network.Node) error
}

// ListingViewRepository stores the ListingView read model (upsert).
type ListingViewRepository interface {
	// Get returns the listing, or ErrNotFound.
	Get(ctx context.Context, listingID string) (network.Listing, error)
	// Save inserts or replaces the listing row.
	Save(ctx context.Context, l network.Listing) error
}

// InboundShipmentViewRepository stores the InboundShipmentView read model
// (upsert, no version): seller-inbound's shipments as this context saw them.
type InboundShipmentViewRepository interface {
	// Get returns the shipment, or ErrNotFound.
	Get(ctx context.Context, shipmentID string) (inbound.Shipment, error)
	// Save inserts or replaces the shipment row.
	Save(ctx context.Context, s inbound.Shipment) error
	// ListShipped returns the SHIPPED shipments with a manifest that match the
	// filter, ordered by (shippedAt, shipmentId) ascending.
	ListShipped(ctx context.Context, filter repository.InboundShipmentFilter) ([]inbound.Shipment, error)
}

// CatalogPackagingViewRepository stores the CatalogPackagingView read model
// (upsert): the packaging levels offer-availability declared per catalogue item.
type CatalogPackagingViewRepository interface {
	// Get returns the (item, level, gtin) row, or ErrNotFound.
	Get(ctx context.Context, catalogItemID string, level inbound.Level, gtin string) (inbound.PackLevel, error)
	// Save inserts or replaces the row.
	Save(ctx context.Context, p inbound.PackLevel) error
	// ListByItem returns every row of the catalogue item, active or not.
	ListByItem(ctx context.Context, catalogItemID string) ([]inbound.PackLevel, error)
}

// EventOutbox is the write side of the transactional outbox. Called with the
// ctx handed to UnitOfWork.Do it joins the SAME transaction as the aggregate
// Save calls, so the aggregates and their raised events commit or roll back
// together. The adapter encodes each event into its CloudEvents envelope (the
// id is minted once, at append) and stores one row per event in order.
// Appending no events is a no-op.
type EventOutbox interface {
	Append(ctx context.Context, events ...shared.Event) error
}

// ProcessedEvents is the consumers' inbox (table processed_events).
type ProcessedEvents interface {
	// Claim records the CloudEvents id and reports whether it was new. A false
	// answer means the event was processed before and must be skipped. Inside a
	// UnitOfWork the claim commits or rolls back with the effect.
	Claim(ctx context.Context, eventID, eventType string, at time.Time) (bool, error)
}

// UnitOfWork runs fn as ONE atomic unit: every repository and outbox call fn
// makes with the ctx it is handed commits together when fn returns nil, or
// rolls back together when fn returns an error (or panics).
//
//   - fn MUST use the ctx passed to it for every call that should participate.
//   - Do is re-entrant: when ctx already carries an open unit, Do joins it and
//     the outermost Do owns commit and rollback.
//   - The error fn returns is returned from Do unchanged (errors.Is keeps
//     working); a commit failure is returned wrapped.
type UnitOfWork interface {
	Do(ctx context.Context, fn func(ctx context.Context) error) error
}

// Clock is the service's notion of "now".
type Clock interface {
	Now() time.Time
}

// IDGenerator mints transaction ids.
type IDGenerator interface {
	NewTransactionID() string
}

// Random draws the simulation knobs' dice (TX_FAILURE_RATE, PO_REDELIVERY_RATE).
type Random interface {
	// Float64 returns a value in [0, 1).
	Float64() float64
}

// NodeTopology is the model's sync_call to node-topology. Both methods return
// *gateway.Refusal for a 4xx answer and ErrUpstreamUnavailable for an outage.
// key is the Idempotency-Key.
type NodeTopology interface {
	RegisterNode(ctx context.Context, key string, req requests.NodeRegistration) (gateway.RegisteredNode, error)
	DeclareOperationalProfile(ctx context.Context, key, nodeID string, req requests.NodeProfile) error
}

// OfferAvailability is the model's sync_call to offer-availability. feedID is
// the caller-chosen id; a feed is applied at most once.
type OfferAvailability interface {
	ApplyInventoryFeed(ctx context.Context, feedID string, feed requests.InventoryFeed) error
}

// ShippingTracking is the model's sync_call to shipping-and-tracking.
type ShippingTracking interface {
	IssueLabel(ctx context.Context, key string, call gateway.LabelCall) (gateway.IssuedLabel, error)
}

// NodePerformance is the model's sync_call to node-performance.
type NodePerformance interface {
	GetScorecard(ctx context.Context, nodeID string) (views.Scorecard, error)
}
