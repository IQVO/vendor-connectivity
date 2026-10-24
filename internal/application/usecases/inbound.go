package usecases

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"time"

	"github.com/claudioed/vendor-connectivity/internal/application/ports"
	"github.com/claudioed/vendor-connectivity/internal/application/repository"
	"github.com/claudioed/vendor-connectivity/internal/application/requests"
	"github.com/claudioed/vendor-connectivity/internal/application/views"
	"github.com/claudioed/vendor-connectivity/internal/domain/inbound"
	"github.com/claudioed/vendor-connectivity/internal/domain/network"
	"github.com/claudioed/vendor-connectivity/internal/domain/shared"
	"github.com/claudioed/vendor-connectivity/internal/domain/submission"
)

// shipmentToken is the opaque continuation of the inbound shipment poll: the
// keyset position and the node filter it was issued for.
type shipmentToken struct {
	ShippedAt  time.Time `json:"t"`
	ShipmentID string    `json:"s"`
	VendorID   string    `json:"v"`
	NodeID     string    `json:"n,omitempty"`
}

func encodeShipmentToken(t shipmentToken) string {
	b, _ := json.Marshal(t) //nolint:errchkjson // a struct of strings and a time always marshals
	return base64.RawURLEncoding.EncodeToString(b)
}

func decodeShipmentToken(s string) (shipmentToken, error) {
	b, err := base64.RawURLEncoding.DecodeString(s)
	var t shipmentToken
	if err != nil || json.Unmarshal(b, &t) != nil || t.ShipmentID == "" || t.ShippedAt.IsZero() {
		return shipmentToken{}, shared.Invalid("nextToken is not a token this API issued")
	}
	return t, nil
}

// PollInboundShipments is query PollInboundShipments (GET /vendor/inbound-shipments):
// the SHIPPED shipments bound for the vendor's nodes, oldest shipment first,
// paged with nextToken.
func (s *Service) PollInboundShipments(ctx context.Context, q requests.InboundPollQuery) (views.InboundShipmentPage, error) {
	page := views.InboundShipmentPage{InboundShipments: []views.InboundShipment{}}
	if !shared.ValidID(q.VendorID) {
		return page, shared.Invalid("vendorId is required")
	}
	if err := s.requireVendor(ctx, q.VendorID); err != nil {
		return page, err
	}
	tok := shipmentToken{VendorID: q.VendorID, NodeID: q.NodeID}
	var after *repository.ShipmentCursor
	if q.NextToken != "" {
		t, err := decodeShipmentToken(q.NextToken)
		if err != nil {
			return page, err
		}
		if t.VendorID != q.VendorID {
			return page, shared.Invalid("nextToken belongs to another vendor")
		}
		tok.NodeID = t.NodeID
		after = &repository.ShipmentCursor{ShippedAt: t.ShippedAt, ShipmentID: t.ShipmentID}
	}
	nodeIDs, err := s.shipmentNodes(ctx, q.VendorID, tok.NodeID)
	if err != nil || len(nodeIDs) == 0 {
		return page, err
	}
	limit := s.pageLimit(q.Limit)
	f := repository.InboundShipmentFilter{NodeIDs: nodeIDs, After: after, Limit: limit + 1}
	if after == nil {
		f.ShippedAfter = q.CreatedAfter
	}
	rows, err := s.d.Shipments.ListShipped(ctx, f)
	if err != nil {
		return page, err
	}
	if len(rows) > limit {
		rows = rows[:limit]
		last := rows[limit-1]
		page.NextToken = encodeShipmentToken(shipmentToken{ShippedAt: last.ShippedAt, ShipmentID: last.ID, VendorID: q.VendorID, NodeID: tok.NodeID})
	}
	for _, r := range rows {
		page.InboundShipments = append(page.InboundShipments, shipmentView(r))
	}
	return page, nil
}

// shipmentNodes returns the node ids the poll may read: the named node after
// checking the vendor owns it, else every node of the vendor.
func (s *Service) shipmentNodes(ctx context.Context, vendorID, nodeID string) ([]string, error) {
	if nodeID != "" {
		if _, err := s.ownedNode(ctx, vendorID, nodeID); err != nil {
			return nil, err
		}
		return []string{nodeID}, nil
	}
	nodes, err := s.d.Nodes.ListByVendor(ctx, vendorID)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(nodes))
	for _, n := range nodes {
		ids = append(ids, n.ID)
	}
	return ids, nil
}

// GetInboundShipment is query GetInboundShipment (GET /vendor/inbound-shipments/{shipmentId}).
// A shipment that has not shipped yet, or is bound for another vendor's node,
// is not found.
func (s *Service) GetInboundShipment(ctx context.Context, vendorID, shipmentID string) (views.InboundShipment, error) {
	if !shared.ValidID(vendorID) {
		return views.InboundShipment{}, shared.Invalid("vendorId is required")
	}
	if err := s.requireVendor(ctx, vendorID); err != nil {
		return views.InboundShipment{}, err
	}
	sh, err := s.d.Shipments.Get(ctx, shipmentID)
	if err != nil {
		return views.InboundShipment{}, err
	}
	if sh.ShippedAt.IsZero() || len(sh.Cartons) == 0 {
		return views.InboundShipment{}, ports.ErrNotFound
	}
	if _, err := s.ownedNode(ctx, vendorID, sh.NodeID); err != nil {
		if errors.Is(err, network.ErrNodeNotOwnedByVendor) || errors.Is(err, ports.ErrNotFound) {
			return views.InboundShipment{}, ports.ErrNotFound
		}
		return views.InboundShipment{}, err
	}
	return shipmentView(sh), nil
}

// shipmentView builds the SOTPI-shaped tree: cartons whose parentSscc names a
// pallet are grouped under it (pallets in order of first appearance); the rest
// stay top-level.
func shipmentView(sh inbound.Shipment) views.InboundShipment {
	v := views.InboundShipment{ShipmentID: sh.ID, SellerID: sh.SellerID, NodeID: sh.NodeID, ShippedAt: sh.ShippedAt, Status: string(sh.Status),
		Pallets: []views.ShipmentPallet{}, Cartons: []views.ShipmentCarton{}}
	at := map[string]int{}
	for _, c := range sh.Cartons {
		cv := cartonView(c)
		if c.ParentSSCC == "" {
			v.Cartons = append(v.Cartons, cv)
			continue
		}
		i, ok := at[c.ParentSSCC]
		if !ok {
			i = len(v.Pallets)
			at[c.ParentSSCC] = i
			v.Pallets = append(v.Pallets, views.ShipmentPallet{SSCC: c.ParentSSCC, Cartons: []views.ShipmentCarton{}})
		}
		v.Pallets[i].Cartons = append(v.Pallets[i].Cartons, cv)
	}
	return v
}

func cartonView(c inbound.Carton) views.ShipmentCarton {
	cv := views.ShipmentCarton{SSCC: c.SSCC, ParentSSCC: c.ParentSSCC, Kind: c.Kind, GrossWeightG: c.GrossWeightG,
		Contents: make([]views.ShipmentItem, 0, len(c.Contents))}
	if c.Dims != nil {
		cv.DimsMm = &views.Dims{L: c.Dims.L, W: c.Dims.W, H: c.Dims.H}
	}
	for _, it := range c.Contents {
		cv.Contents = append(cv.Contents, views.ShipmentItem{GTIN: it.GTIN, QtyEaches: it.QtyEaches, Lot: it.Lot, Expiry: it.Expiry})
	}
	return cv
}

// SubmitInboundReceipt receives an INBOUND_RECEIPT (POST /vendor/inbound-receipts):
// the receipt is judged against InboundShipmentView, a PROCESSING submission is
// received and InboundReceiptSubmitted is appended to the outbox, all in one unit.
func (s *Service) SubmitInboundReceipt(ctx context.Context, in requests.InboundReceipt) (views.TransactionAccepted, error) {
	if err := in.Validate(); err != nil {
		return views.TransactionAccepted{}, err
	}
	rc, err := receiptOf(in).Normalise()
	if err != nil {
		return views.TransactionAccepted{}, err
	}
	var txID string
	err = s.atomically(ctx, func(ctx context.Context) error {
		if err := s.requireActive(ctx, in.VendorID); err != nil {
			return err
		}
		if _, err := s.ownedNode(ctx, in.VendorID, rc.NodeID); err != nil {
			return err
		}
		sh, err := s.d.Shipments.Get(ctx, rc.ShipmentID)
		if err != nil {
			return err
		}
		if err := sh.CheckReceipt(rc); err != nil {
			return err
		}
		txID = s.d.IDs.NewTransactionID()
		sub, err := s.newSubmission(txID, in.VendorID, submission.KindInboundReceipt, receiptBody(in.VendorID, rc), "", rc.NodeID)
		if err != nil {
			return err
		}
		if err := s.d.Submissions.Save(ctx, sub); err != nil {
			return err
		}
		return s.d.Outbox.Append(ctx, inbound.NewReceiptSubmitted(txID, in.VendorID, rc, s.d.Clock.Now()))
	})
	return views.TransactionAccepted{TransactionID: txID}, err
}

func receiptOf(in requests.InboundReceipt) inbound.Receipt {
	rc := inbound.Receipt{ShipmentID: in.ShipmentID, NodeID: in.NodeID, ReceivedAt: in.ReceivedAt, Cartons: make([]inbound.ReceiptCarton, 0, len(in.Cartons))}
	for _, c := range in.Cartons {
		rcc := inbound.ReceiptCarton{SSCC: c.SSCC, Status: inbound.CartonStatus(c.Status), ComplianceCodes: c.ComplianceCodes,
			Items: make([]inbound.ReceiptItem, 0, len(c.Items))}
		for _, it := range c.Items {
			rcc.Items = append(rcc.Items, inbound.ReceiptItem{GTIN: it.GTIN, QtyEaches: it.QtyEaches})
		}
		rc.Cartons = append(rc.Cartons, rcc)
	}
	return rc
}

// receiptBody is the normalised receipt kept on the submission.
func receiptBody(vendorID string, rc inbound.Receipt) requests.InboundReceipt {
	out := requests.InboundReceipt{VendorID: vendorID, ShipmentID: rc.ShipmentID, NodeID: rc.NodeID, ReceivedAt: rc.ReceivedAt,
		Cartons: make([]requests.InboundReceiptCarton, 0, len(rc.Cartons))}
	for _, c := range rc.Cartons {
		oc := requests.InboundReceiptCarton{SSCC: c.SSCC, Status: string(c.Status), ComplianceCodes: c.ComplianceCodes,
			Items: make([]requests.InboundReceiptItem, 0, len(c.Items))}
		for _, it := range c.Items {
			oc.Items = append(oc.Items, requests.InboundReceiptItem{GTIN: it.GTIN, QtyEaches: it.QtyEaches})
		}
		out.Cartons = append(out.Cartons, oc)
	}
	return out
}

// --- consumers of seller-inbound and offer-availability events -------------

// OnInboundShipmentLabelled feeds InboundShipmentView from InboundShipmentLabelled.
func (s *Service) OnInboundShipmentLabelled(ctx context.Context, eventID, eventType string, in inbound.Labelled) error {
	return s.updateShipment(ctx, eventID, eventType, in.ShipmentID, func(cur inbound.Shipment) (inbound.Shipment, error) { return cur.OnLabelled(in) })
}

// OnInboundShipmentShipped feeds InboundShipmentView from InboundShipmentShipped.
func (s *Service) OnInboundShipmentShipped(ctx context.Context, eventID, eventType string, in inbound.Shipped) error {
	return s.updateShipment(ctx, eventID, eventType, in.ShipmentID, func(cur inbound.Shipment) (inbound.Shipment, error) { return cur.OnShipped(in) })
}

// OnInboundShipmentReceived feeds InboundShipmentView from InboundShipmentReceived.
func (s *Service) OnInboundShipmentReceived(ctx context.Context, eventID, eventType string, in inbound.Received) error {
	return s.updateShipment(ctx, eventID, eventType, in.ShipmentID, func(cur inbound.Shipment) (inbound.Shipment, error) { return cur.OnReceived(in) })
}

func (s *Service) updateShipment(ctx context.Context, eventID, eventType, shipmentID string, apply func(inbound.Shipment) (inbound.Shipment, error)) error {
	return s.consume(ctx, eventID, eventType, func(ctx context.Context) error {
		cur, err := s.d.Shipments.Get(ctx, shipmentID)
		if err != nil && !errors.Is(err, ports.ErrNotFound) {
			return err
		}
		next, err := apply(cur)
		if errors.Is(err, shared.ErrValidation) {
			s.cfg.Logger.WarnContext(ctx, "inbound shipment event carries invalid facts; ignored", "eventId", eventID, "type", eventType, "error", err)
			return nil
		}
		if err != nil {
			return err
		}
		return s.d.Shipments.Save(ctx, next)
	})
}

// OnPackagingDeclared feeds CatalogPackagingView from CatalogItemPackagingDeclared.
func (s *Service) OnPackagingDeclared(ctx context.Context, eventID, eventType string, in inbound.Declared) error {
	return s.updatePackaging(ctx, eventID, eventType, in.CatalogItemID, in.Level, in.GTIN, func(cur inbound.PackLevel) (inbound.PackLevel, error) { return cur.OnDeclared(in) })
}

// OnPackLevelRetired feeds CatalogPackagingView from CatalogItemPackLevelRetired.
func (s *Service) OnPackLevelRetired(ctx context.Context, eventID, eventType string, in inbound.Retired) error {
	return s.updatePackaging(ctx, eventID, eventType, in.CatalogItemID, in.Level, in.GTIN, func(cur inbound.PackLevel) (inbound.PackLevel, error) { return cur.OnRetired(in) })
}

func (s *Service) updatePackaging(ctx context.Context, eventID, eventType, itemID string, level inbound.Level, gtin string, apply func(inbound.PackLevel) (inbound.PackLevel, error)) error {
	return s.consume(ctx, eventID, eventType, func(ctx context.Context) error {
		key, err := inbound.ParseGTIN(gtin)
		if err != nil {
			s.cfg.Logger.WarnContext(ctx, "packaging event carries an invalid gtin; ignored", "eventId", eventID, "type", eventType, "error", err)
			return nil
		}
		cur, err := s.d.Packaging.Get(ctx, itemID, level, key)
		if err != nil && !errors.Is(err, ports.ErrNotFound) {
			return err
		}
		next, err := apply(cur)
		if errors.Is(err, shared.ErrValidation) {
			s.cfg.Logger.WarnContext(ctx, "packaging event carries invalid facts; ignored", "eventId", eventID, "type", eventType, "error", err)
			return nil
		}
		if err != nil {
			return err
		}
		return s.d.Packaging.Save(ctx, next)
	})
}

// withPackaging fills packaging[] of every purchase order line from
// CatalogPackagingView: listing -> catalogue item -> its active levels.
func (s *Service) withPackaging(ctx context.Context, orders []views.PurchaseOrder) error {
	cache := map[string][]views.Packaging{}
	for i := range orders {
		for j := range orders[i].Lines {
			id := orders[i].Lines[j].ListingID
			pk, ok := cache[id]
			if !ok {
				var err error
				if pk, err = s.listingPackaging(ctx, id); err != nil {
					return err
				}
				cache[id] = pk
			}
			orders[i].Lines[j].Packaging = pk
		}
	}
	return nil
}

func (s *Service) listingPackaging(ctx context.Context, listingID string) ([]views.Packaging, error) {
	out := []views.Packaging{}
	l, err := s.d.Listings.Get(ctx, listingID)
	if errors.Is(err, ports.ErrNotFound) || (err == nil && l.CatalogItemID == "") {
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	rows, err := s.d.Packaging.ListByItem(ctx, l.CatalogItemID)
	if err != nil {
		return nil, err
	}
	for _, r := range inbound.ActiveLevels(rows) {
		out = append(out, views.Packaging{Level: string(r.Level), GTIN: r.GTIN, ChildGTIN: r.ChildGTIN, UnitsPerPack: r.UnitsPerPack})
	}
	return out, nil
}
