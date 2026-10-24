package kafka

import (
	"context"
	"log/slog"
	"time"

	ce "github.com/cloudevents/sdk-go/v2/event"

	"github.com/claudioed/vendor-connectivity/internal/application/usecases"
	"github.com/claudioed/vendor-connectivity/internal/domain/inbound"
)

// SellerInbound is the topic of seller-inbound's integration events. The
// contract is restated here and in apis/asyncapi.yaml, never imported.
const SellerInbound = "retailnetwork.seller-inbound.events"

// Consumed types of seller-inbound and of offer-availability's packaging
// events. Both partner-tier and commerce-tier spellings of the packaging types
// are accepted: the producer's tier segment is not pinned yet (ADR 0005).
const (
	TypeShipmentLabelled = "com.retailnetwork.partner.seller-inbound.inboundshipment.InboundShipmentLabelled"
	TypeShipmentShipped  = "com.retailnetwork.partner.seller-inbound.inboundshipment.InboundShipmentShipped"
	TypeShipmentReceived = "com.retailnetwork.partner.seller-inbound.inboundshipment.InboundShipmentReceived"

	TypePackagingDeclared = "com.retailnetwork.partner.offer-availability.catalogitem.CatalogItemPackagingDeclared"
	TypePackLevelRetired  = "com.retailnetwork.partner.offer-availability.catalogitem.CatalogItemPackLevelRetired"

	typePackagingDeclaredCommerce = "com.retailnetwork.commerce.offer-availability.catalogitem.CatalogItemPackagingDeclared"
	typePackLevelRetiredCommerce  = "com.retailnetwork.commerce.offer-availability.catalogitem.CatalogItemPackLevelRetired"
)

// Wire payloads: a TOLERANT local copy of only the fields this context uses.
// Unknown fields are ignored (decodeData uses encoding/json), optional ones
// default to their zero value.

type wireItem struct {
	CatalogItemID string `json:"catalogItemId"`
	GTIN          string `json:"gtin"`
	QtyEaches     int    `json:"qtyEaches"`
	Lot           string `json:"lot"`
	Expiry        string `json:"expiry"`
}

type wireDims struct {
	L int `json:"l"`
	W int `json:"w"`
	H int `json:"h"`
}

type wireCarton struct {
	SSCC         string     `json:"sscc"`
	ParentSSCC   string     `json:"parentSscc"`
	Kind         string     `json:"kind"`
	Contents     []wireItem `json:"contents"`
	GrossWeightG int        `json:"grossWeightG"`
	DimsMm       *wireDims  `json:"dimsMm"`
}

type shipmentLabelledData struct {
	ShipmentID string       `json:"shipmentId"`
	PlanID     string       `json:"planId"`
	SellerID   string       `json:"sellerId"`
	NodeID     string       `json:"nodeId"`
	Cartons    []wireCarton `json:"cartons"`
	LabelledAt time.Time    `json:"labelledAt"`
}

type shipmentShippedData struct {
	ShipmentID string    `json:"shipmentId"`
	PlanID     string    `json:"planId"`
	SellerID   string    `json:"sellerId"`
	NodeID     string    `json:"nodeId"`
	ShippedAt  time.Time `json:"shippedAt"`
}

type shipmentReceivedData struct {
	ShipmentID string    `json:"shipmentId"`
	SellerID   string    `json:"sellerId"`
	NodeID     string    `json:"nodeId"`
	Status     string    `json:"status"`
	ReceivedAt time.Time `json:"receivedAt"`
}

type packagingDeclaredData struct {
	CatalogItemID string    `json:"catalogItemId"`
	Level         string    `json:"level"`
	GTIN          string    `json:"gtin"`
	ChildGTIN     string    `json:"childGtin"`
	UnitsPerPack  int       `json:"unitsPerPack"`
	DeclaredAt    time.Time `json:"declaredAt"`
}

type packLevelRetiredData struct {
	CatalogItemID string    `json:"catalogItemId"`
	Level         string    `json:"level"`
	GTIN          string    `json:"gtin"`
	RetiredAt     time.Time `json:"retiredAt"`
}

func (d shipmentLabelledData) facts() inbound.Labelled {
	cartons := make([]inbound.Carton, 0, len(d.Cartons))
	for _, c := range d.Cartons {
		ic := inbound.Carton{SSCC: c.SSCC, ParentSSCC: c.ParentSSCC, Kind: c.Kind, GrossWeightG: c.GrossWeightG, Contents: make([]inbound.Item, 0, len(c.Contents))}
		if c.DimsMm != nil {
			ic.Dims = &inbound.Dims{L: c.DimsMm.L, W: c.DimsMm.W, H: c.DimsMm.H}
		}
		for _, it := range c.Contents {
			ic.Contents = append(ic.Contents, inbound.Item{CatalogItemID: it.CatalogItemID, GTIN: it.GTIN, QtyEaches: it.QtyEaches, Lot: it.Lot, Expiry: it.Expiry})
		}
		cartons = append(cartons, ic)
	}
	return inbound.Labelled{ShipmentID: d.ShipmentID, PlanID: d.PlanID, SellerID: d.SellerID, NodeID: d.NodeID, Cartons: cartons, LabelledAt: d.LabelledAt}
}

// InboundHandlers maps seller-inbound's shipment events to InboundShipmentView.
func InboundHandlers(svc *usecases.Service) map[string]HandlerFunc {
	return map[string]HandlerFunc{
		TypeShipmentLabelled: func(ctx context.Context, e ce.Event) error {
			d, err := decodeData[shipmentLabelledData](e)
			if err != nil {
				return err
			}
			return svc.OnInboundShipmentLabelled(ctx, e.ID(), e.Type(), d.facts())
		},
		TypeShipmentShipped: func(ctx context.Context, e ce.Event) error {
			d, err := decodeData[shipmentShippedData](e)
			if err != nil {
				return err
			}
			return svc.OnInboundShipmentShipped(ctx, e.ID(), e.Type(), inbound.Shipped(d))
		},
		TypeShipmentReceived: func(ctx context.Context, e ce.Event) error {
			d, err := decodeData[shipmentReceivedData](e)
			if err != nil {
				return err
			}
			return svc.OnInboundShipmentReceived(ctx, e.ID(), e.Type(), inbound.Received{ShipmentID: d.ShipmentID, SellerID: d.SellerID,
				NodeID: d.NodeID, Status: inbound.Status(d.Status), ReceivedAt: d.ReceivedAt})
		},
	}
}

// PackagingHandlers maps offer-availability's packaging events to CatalogPackagingView.
func PackagingHandlers(svc *usecases.Service) map[string]HandlerFunc {
	declared := func(ctx context.Context, e ce.Event) error {
		d, err := decodeData[packagingDeclaredData](e)
		if err != nil {
			return err
		}
		return svc.OnPackagingDeclared(ctx, e.ID(), e.Type(), inbound.Declared{CatalogItemID: d.CatalogItemID, Level: inbound.Level(d.Level),
			GTIN: d.GTIN, ChildGTIN: d.ChildGTIN, UnitsPerPack: d.UnitsPerPack, DeclaredAt: d.DeclaredAt})
	}
	retired := func(ctx context.Context, e ce.Event) error {
		d, err := decodeData[packLevelRetiredData](e)
		if err != nil {
			return err
		}
		return svc.OnPackLevelRetired(ctx, e.ID(), e.Type(), inbound.Retired{CatalogItemID: d.CatalogItemID, Level: inbound.Level(d.Level),
			GTIN: d.GTIN, RetiredAt: d.RetiredAt})
	}
	return map[string]HandlerFunc{
		TypePackagingDeclared: declared, typePackagingDeclaredCommerce: declared,
		TypePackLevelRetired: retired, typePackLevelRetiredCommerce: retired,
	}
}

// NewInboundConsumer reads seller-inbound's topic under groupID (INBOUND_CONSUMER_GROUP).
func NewInboundConsumer(brokers []string, groupID string, svc *usecases.Service, logger *slog.Logger) *Consumer {
	return newConsumer("inbound consumer", brokers, SellerInbound, groupID, InboundHandlers(svc), logger)
}

// NewPackagingConsumer reads offer-availability's topic under its own groupID
// (PACKAGING_CONSUMER_GROUP), apart from the listing consumer.
func NewPackagingConsumer(brokers []string, groupID string, svc *usecases.Service, logger *slog.Logger) *Consumer {
	return newConsumer("packaging consumer", brokers, OfferAvailability, groupID, PackagingHandlers(svc), logger)
}
