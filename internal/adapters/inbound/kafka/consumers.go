package kafka

import (
	"context"
	"log/slog"
	"time"

	ce "github.com/cloudevents/sdk-go/v2/event"

	"github.com/claudioed/vendor-connectivity/internal/application/requests"
	"github.com/claudioed/vendor-connectivity/internal/application/usecases"
)

// Consumed topics: the producers' integration topics (their contracts are
// restated in apis/asyncapi.yaml, never imported).
const (
	NodeTopology      = "retailnetwork.node-topology.events"
	OfferAvailability = "retailnetwork.offer-availability.events"
	OrderSourcing     = "retailnetwork.order-sourcing.events"
)

// Consumed types, byte-identical to the producers' AsyncAPI.
const (
	TypeNodeRegistered   = "com.retailnetwork.logistics.node-topology.node.NodeRegistered"
	TypeProfileDeclared  = "com.retailnetwork.logistics.node-topology.node.NodeOperationalProfileDeclared"
	TypeNodeActivated    = "com.retailnetwork.logistics.node-topology.node.NodeActivated"
	TypeNodeSuspended    = "com.retailnetwork.logistics.node-topology.node.NodeSuspended"
	TypeNodeRetired      = "com.retailnetwork.logistics.node-topology.node.NodeRetired"
	TypeListingCreated   = "com.retailnetwork.commerce.offer-availability.listing.ListingCreated"
	TypeReleasedToNode   = "com.retailnetwork.commerce.order-sourcing.assignment.FulfillmentReleasedToNode"
	TypeFulfillmentCancl = "com.retailnetwork.commerce.order-sourcing.assignment.FulfillmentCancelled"
)

// Wire payloads (only the fields this context relies on).

type nodeRegisteredData struct {
	NodeID         string `json:"nodeId"`
	NodeType       string `json:"nodeType"`
	Subtype        string `json:"subtype"`
	Operator       string `json:"operator"`
	VendorID       string `json:"vendorId"`
	VendorNodeCode string `json:"vendorNodeCode"`
	RegionID       string `json:"regionId"`
	Timezone       string `json:"timezone"`
}

type profileDeclaredData struct {
	NodeID              string `json:"nodeId"`
	ProfileRevision     int    `json:"profileRevision"`
	HandlingTimeMinutes int    `json:"handlingTimeMinutes"`
}

type nodeStatusData struct {
	NodeID string `json:"nodeId"`
}

type listingCreatedData struct {
	ListingID     string `json:"listingId"`
	CatalogItemID string `json:"catalogItemId"`
	NodeID        string `json:"nodeId"`
}

type releasedData struct {
	AssignmentID   string    `json:"assignmentId"`
	ReleaseNo      int       `json:"releaseNo"`
	OrderID        string    `json:"orderId"`
	GroupNo        int       `json:"groupNo"`
	NodeID         string    `json:"nodeId"`
	RequiredShipBy time.Time `json:"requiredShipBy"`
	Tier           string    `json:"tier"`
	Lines          []struct {
		ListingID string `json:"listingId"`
		Quantity  int    `json:"quantity"`
	} `json:"lines"`
}

type cancelledData struct {
	AssignmentID string `json:"assignmentId"`
	Reason       string `json:"reason"`
}

// NodeHandlers maps node-topology's events to the VendorNodeView updates.
func NodeHandlers(svc *usecases.Service) map[string]HandlerFunc {
	status := func(typ, status string) HandlerFunc {
		return func(ctx context.Context, e ce.Event) error {
			d, err := decodeData[nodeStatusData](e)
			if err != nil {
				return err
			}
			return svc.OnNodeStatus(ctx, e.ID(), typ, requests.NodeStatusFacts{NodeID: d.NodeID, Status: status})
		}
	}
	return map[string]HandlerFunc{
		TypeNodeRegistered: func(ctx context.Context, e ce.Event) error {
			d, err := decodeData[nodeRegisteredData](e)
			if err != nil {
				return err
			}
			return svc.OnNodeRegistered(ctx, e.ID(), e.Type(), requests.NodeRegisteredFacts(d))
		},
		TypeProfileDeclared: func(ctx context.Context, e ce.Event) error {
			d, err := decodeData[profileDeclaredData](e)
			if err != nil {
				return err
			}
			return svc.OnNodeProfileDeclared(ctx, e.ID(), e.Type(), requests.NodeProfileFacts(d))
		},
		TypeNodeActivated: status(TypeNodeActivated, "ACTIVE"),
		TypeNodeSuspended: status(TypeNodeSuspended, "SUSPENDED"),
		TypeNodeRetired:   status(TypeNodeRetired, "RETIRED"),
	}
}

// ListingHandlers maps offer-availability's ListingCreated to ListingView.
func ListingHandlers(svc *usecases.Service) map[string]HandlerFunc {
	return map[string]HandlerFunc{
		TypeListingCreated: func(ctx context.Context, e ce.Event) error {
			d, err := decodeData[listingCreatedData](e)
			if err != nil {
				return err
			}
			return svc.OnListingCreated(ctx, e.ID(), e.Type(), requests.ListingFacts{ListingID: d.ListingID, NodeID: d.NodeID, CatalogItemID: d.CatalogItemID})
		},
	}
}

// SourcingHandlers maps order-sourcing's release and cancellation to the
// PurchaseOrder policies.
func SourcingHandlers(svc *usecases.Service, logger *slog.Logger) map[string]HandlerFunc {
	logger = defaultLogger(logger)
	return map[string]HandlerFunc{
		TypeReleasedToNode: func(ctx context.Context, e ce.Event) error {
			d, err := decodeData[releasedData](e)
			if err != nil {
				return err
			}
			in := requests.ReleasedToNode{AssignmentID: d.AssignmentID, ReleaseNo: d.ReleaseNo, OrderID: d.OrderID, GroupNo: d.GroupNo,
				NodeID: d.NodeID, RequiredShipBy: d.RequiredShipBy, Tier: d.Tier}
			for _, l := range d.Lines {
				in.Lines = append(in.Lines, requests.ReleasedLine{ListingID: l.ListingID, Quantity: l.Quantity})
			}
			err = svc.IssueOnRelease(ctx, e.ID(), e.Type(), in)
			if err == nil {
				logger.InfoContext(ctx, "event processed", "type", e.Type(), "event_id", e.ID(), "subject", e.Subject())
			}
			return err
		},
		TypeFulfillmentCancl: func(ctx context.Context, e ce.Event) error {
			d, err := decodeData[cancelledData](e)
			if err != nil {
				return err
			}
			return svc.CancelOnSourcingCancel(ctx, e.ID(), e.Type(), requests.FulfillmentCancelled{AssignmentID: d.AssignmentID, Reason: d.Reason})
		},
	}
}

// NewNodeConsumer reads node-topology's topic under groupID (NODE_CONSUMER_GROUP).
func NewNodeConsumer(brokers []string, groupID string, svc *usecases.Service, logger *slog.Logger) *Consumer {
	return newConsumer("node consumer", brokers, NodeTopology, groupID, NodeHandlers(svc), logger)
}

// NewListingConsumer reads offer-availability's topic under groupID (LISTING_CONSUMER_GROUP).
func NewListingConsumer(brokers []string, groupID string, svc *usecases.Service, logger *slog.Logger) *Consumer {
	return newConsumer("listing consumer", brokers, OfferAvailability, groupID, ListingHandlers(svc), logger)
}

// NewSourcingConsumer reads order-sourcing's topic under groupID (SOURCING_CONSUMER_GROUP).
func NewSourcingConsumer(brokers []string, groupID string, svc *usecases.Service, logger *slog.Logger) *Consumer {
	return newConsumer("sourcing consumer", brokers, OrderSourcing, groupID, SourcingHandlers(svc, logger), logger)
}
