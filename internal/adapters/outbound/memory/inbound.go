package memory

import (
	"context"
	"sort"

	"github.com/claudioed/vendor-connectivity/internal/application/ports"
	"github.com/claudioed/vendor-connectivity/internal/application/repository"
	"github.com/claudioed/vendor-connectivity/internal/domain/inbound"
)

func cloneShipment(s inbound.Shipment) inbound.Shipment {
	out := s
	out.Cartons = make([]inbound.Carton, len(s.Cartons))
	for i, c := range s.Cartons {
		c.Contents = append([]inbound.Item(nil), c.Contents...)
		if c.Dims != nil {
			d := *c.Dims
			c.Dims = &d
		}
		out.Cartons[i] = c
	}
	return out
}

// InboundShipmentViewRepository is the in-memory ports.InboundShipmentViewRepository.
type InboundShipmentViewRepository struct{ s *Store }

// Get returns the shipment or ports.ErrNotFound.
func (r *InboundShipmentViewRepository) Get(ctx context.Context, shipmentID string) (inbound.Shipment, error) {
	defer r.s.enter(ctx)()
	sh, ok := r.s.shipments[shipmentID]
	if !ok {
		return inbound.Shipment{}, ports.ErrNotFound
	}
	return cloneShipment(sh), nil
}

// Save inserts or replaces the shipment.
func (r *InboundShipmentViewRepository) Save(ctx context.Context, sh inbound.Shipment) error {
	defer r.s.enter(ctx)()
	r.s.shipments[sh.ID] = cloneShipment(sh)
	return nil
}

func shipmentAfter(sh inbound.Shipment, c repository.ShipmentCursor) bool {
	if sh.ShippedAt.Equal(c.ShippedAt) {
		return sh.ID > c.ShipmentID
	}
	return sh.ShippedAt.After(c.ShippedAt)
}

func shipmentMatches(sh inbound.Shipment, f repository.InboundShipmentFilter) bool {
	if !sh.Pollable() {
		return false
	}
	inNodes := false
	for _, n := range f.NodeIDs {
		if n == sh.NodeID {
			inNodes = true
		}
	}
	switch {
	case !inNodes:
		return false
	case f.ShippedAfter != nil && !sh.ShippedAt.After(*f.ShippedAfter):
		return false
	case f.After != nil && !shipmentAfter(sh, *f.After):
		return false
	}
	return true
}

// ListShipped returns the matching SHIPPED shipments ordered by (shippedAt, shipmentId).
func (r *InboundShipmentViewRepository) ListShipped(ctx context.Context, f repository.InboundShipmentFilter) ([]inbound.Shipment, error) {
	defer r.s.enter(ctx)()
	var out []inbound.Shipment
	for _, sh := range r.s.shipments {
		if shipmentMatches(sh, f) {
			out = append(out, cloneShipment(sh))
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].ShippedAt.Equal(out[j].ShippedAt) {
			return out[i].ID < out[j].ID
		}
		return out[i].ShippedAt.Before(out[j].ShippedAt)
	})
	if f.Limit > 0 && len(out) > f.Limit {
		out = out[:f.Limit]
	}
	return out, nil
}

// CatalogPackagingViewRepository is the in-memory ports.CatalogPackagingViewRepository.
type CatalogPackagingViewRepository struct{ s *Store }

func packKey(item string, level inbound.Level, gtin string) string {
	return item + "|" + string(level) + "|" + gtin
}

// Get returns the row or ports.ErrNotFound.
func (r *CatalogPackagingViewRepository) Get(ctx context.Context, item string, level inbound.Level, gtin string) (inbound.PackLevel, error) {
	defer r.s.enter(ctx)()
	p, ok := r.s.packaging[packKey(item, level, gtin)]
	if !ok {
		return inbound.PackLevel{}, ports.ErrNotFound
	}
	return p, nil
}

// Save inserts or replaces the row.
func (r *CatalogPackagingViewRepository) Save(ctx context.Context, p inbound.PackLevel) error {
	defer r.s.enter(ctx)()
	r.s.packaging[packKey(p.CatalogItemID, p.Level, p.GTIN)] = p
	return nil
}

// ListByItem returns every row of the catalogue item.
func (r *CatalogPackagingViewRepository) ListByItem(ctx context.Context, item string) ([]inbound.PackLevel, error) {
	defer r.s.enter(ctx)()
	var out []inbound.PackLevel
	for _, p := range r.s.packaging {
		if p.CatalogItemID == item {
			out = append(out, p)
		}
	}
	return out, nil
}
