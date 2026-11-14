package kafka

import (
	"context"
	"log/slog"
	"time"

	ce "github.com/cloudevents/sdk-go/v2/event"

	"github.com/claudioed/vendor-connectivity/internal/application/usecases"
	"github.com/claudioed/vendor-connectivity/internal/domain/returns"
)

// CustomerReturns is the topic of the customer-returns context's integration
// events. The contract is restated here and in apis/asyncapi.yaml, never
// imported. The producer repo is created in parallel (ADR 0014): the reader
// construction does not dial and a consumer group is handed an EMPTY assignment
// while the topic does not exist, so boot and readiness are unaffected.
const CustomerReturns = "retailnetwork.customer-returns.events"

// Consumed types of customer-returns (ADR 0014).
const (
	TypeReturnAuthorized = "com.retailnetwork.commerce.customer-returns.returnauthorization.ReturnAuthorized"
	TypeReturnLabelled   = "com.retailnetwork.commerce.customer-returns.returnauthorization.ReturnLabelIssued"
	TypeReturnCancelled  = "com.retailnetwork.commerce.customer-returns.returnauthorization.ReturnCancelled"
)

// Wire payloads: a TOLERANT local copy of only the fields this context uses.
// Unknown fields are ignored (decodeData uses encoding/json), optional ones
// default to their zero value (destinationNodeId and originNodeId are omitted
// for a KEEP disposition and an unknown origin).

type wireReturnLine struct {
	LineNo        int    `json:"lineNo"`
	CatalogItemID string `json:"catalogItemId"`
	Quantity      int    `json:"quantity"`
	OwnerRef      string `json:"ownerRef"`
}

type returnAuthorizedData struct {
	AuthorizationID   string           `json:"authorizationId"`
	OrderID           string           `json:"orderId"`
	GroupNo           int              `json:"groupNo"`
	Reason            string           `json:"reason"`
	Disposition       string           `json:"disposition"`
	Method            string           `json:"method"`
	Lines             []wireReturnLine `json:"lines"`
	DestinationNodeID string           `json:"destinationNodeId"`
	OriginNodeID      string           `json:"originNodeId"`
	AuthorizedAt      time.Time        `json:"authorizedAt"`
	ExpiresAt         time.Time        `json:"expiresAt"`
}

type returnLabelIssuedData struct {
	AuthorizationID   string    `json:"authorizationId"`
	TrackingNumber    string    `json:"trackingNumber"`
	DestinationNodeID string    `json:"destinationNodeId"`
	IssuedAt          time.Time `json:"issuedAt"`
}

type returnCancelledData struct {
	AuthorizationID string    `json:"authorizationId"`
	Reason          string    `json:"reason"`
	CancelledAt     time.Time `json:"cancelledAt"`
}

// destination resolves the advice's node: the event's destination, else its
// origin node (the ORIGIN_NODE disposition ships there); "" for a KEEP.
func (d returnAuthorizedData) destination() string {
	if d.DestinationNodeID != "" {
		return d.DestinationNodeID
	}
	return d.OriginNodeID
}

func (d returnAuthorizedData) facts() returns.Authorized {
	lines := make([]returns.Line, 0, len(d.Lines))
	for _, l := range d.Lines {
		lines = append(lines, returns.Line{LineNo: l.LineNo, CatalogItemID: l.CatalogItemID, Quantity: l.Quantity, OwnerRef: l.OwnerRef})
	}
	return returns.Authorized{AuthorizationID: d.AuthorizationID, OrderID: d.OrderID, GroupNo: d.GroupNo, NodeID: d.destination(),
		Disposition: d.Disposition, Method: d.Method, Lines: lines, ExpiresAt: d.ExpiresAt, AuthorizedAt: d.AuthorizedAt}
}

// CustomerReturnsHandlers maps customer-returns' events to ReturnAdviceView.
func CustomerReturnsHandlers(svc *usecases.Service) map[string]HandlerFunc {
	return map[string]HandlerFunc{
		TypeReturnAuthorized: func(ctx context.Context, e ce.Event) error {
			d, err := decodeData[returnAuthorizedData](e)
			if err != nil {
				return err
			}
			return svc.OnReturnAuthorized(ctx, e.ID(), e.Type(), d.facts())
		},
		TypeReturnLabelled: func(ctx context.Context, e ce.Event) error {
			d, err := decodeData[returnLabelIssuedData](e)
			if err != nil {
				return err
			}
			return svc.OnReturnLabelIssued(ctx, e.ID(), e.Type(), d.AuthorizationID, d.TrackingNumber)
		},
		TypeReturnCancelled: func(ctx context.Context, e ce.Event) error {
			d, err := decodeData[returnCancelledData](e)
			if err != nil {
				return err
			}
			return svc.OnReturnCancelled(ctx, e.ID(), e.Type(), d.AuthorizationID)
		},
	}
}

// NewCustomerReturnsConsumer reads customer-returns' topic under groupID
// (CUSTOMER_RETURNS_CONSUMER_GROUP).
func NewCustomerReturnsConsumer(brokers []string, groupID string, svc *usecases.Service, logger *slog.Logger) *Consumer {
	return newConsumer("customer-returns consumer", brokers, CustomerReturns, groupID, CustomerReturnsHandlers(svc), logger)
}
