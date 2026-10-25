package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/claudioed/vendor-connectivity/internal/application/ports"
	"github.com/claudioed/vendor-connectivity/internal/application/repository"
	"github.com/claudioed/vendor-connectivity/internal/domain/inbound"
)

// cartonRow is the stored JSON form of one manifest carton.
type cartonRow struct {
	SSCC         string    `json:"sscc"`
	ParentSSCC   string    `json:"parentSscc,omitempty"`
	Kind         string    `json:"kind"`
	Contents     []itemRow `json:"contents"`
	GrossWeightG int       `json:"grossWeightG,omitempty"`
	Dims         *dimsRow  `json:"dimsMm,omitempty"`
}

type itemRow struct {
	CatalogItemID string `json:"catalogItemId,omitempty"`
	GTIN          string `json:"gtin"`
	QtyEaches     int    `json:"qtyEaches"`
	Lot           string `json:"lot,omitempty"`
	Expiry        string `json:"expiry,omitempty"`
}

type dimsRow struct {
	L int `json:"l"`
	W int `json:"w"`
	H int `json:"h"`
}

func cartonsJSON(cs []inbound.Carton) ([]byte, error) {
	rows := make([]cartonRow, 0, len(cs))
	for _, c := range cs {
		r := cartonRow{SSCC: c.SSCC, ParentSSCC: c.ParentSSCC, Kind: c.Kind, GrossWeightG: c.GrossWeightG, Contents: make([]itemRow, 0, len(c.Contents))}
		if c.Dims != nil {
			r.Dims = &dimsRow{L: c.Dims.L, W: c.Dims.W, H: c.Dims.H}
		}
		for _, it := range c.Contents {
			r.Contents = append(r.Contents, itemRow{CatalogItemID: it.CatalogItemID, GTIN: it.GTIN, QtyEaches: it.QtyEaches, Lot: it.Lot, Expiry: it.Expiry})
		}
		rows = append(rows, r)
	}
	return json.Marshal(rows)
}

func cartonsOf(raw []byte) ([]inbound.Carton, error) {
	var rows []cartonRow
	if err := json.Unmarshal(raw, &rows); err != nil {
		return nil, fmt.Errorf("decode inbound shipment cartons: %w", err)
	}
	out := make([]inbound.Carton, 0, len(rows))
	for _, r := range rows {
		c := inbound.Carton{SSCC: r.SSCC, ParentSSCC: r.ParentSSCC, Kind: r.Kind, GrossWeightG: r.GrossWeightG, Contents: make([]inbound.Item, 0, len(r.Contents))}
		if r.Dims != nil {
			c.Dims = &inbound.Dims{L: r.Dims.L, W: r.Dims.W, H: r.Dims.H}
		}
		for _, it := range r.Contents {
			c.Contents = append(c.Contents, inbound.Item{CatalogItemID: it.CatalogItemID, GTIN: it.GTIN, QtyEaches: it.QtyEaches, Lot: it.Lot, Expiry: it.Expiry})
		}
		out = append(out, c)
	}
	return out, nil
}

// InboundShipmentViewRepo is the Postgres ports.InboundShipmentViewRepository.
type InboundShipmentViewRepo struct{ pool *pgxpool.Pool }

// NewInboundShipmentViewRepo constructs an InboundShipmentViewRepo over pool.
func NewInboundShipmentViewRepo(pool *pgxpool.Pool) *InboundShipmentViewRepo {
	return &InboundShipmentViewRepo{pool: pool}
}

const shipmentColumns = `shipment_id, plan_id, seller_id, node_id, status, cartons, labelled_at, shipped_at, received_at`

func scanShipment(row pgx.Row) (inbound.Shipment, error) {
	var (
		s                           inbound.Shipment
		status                      string
		cartons                     []byte
		labelled, shipped, received *time.Time
	)
	err := row.Scan(&s.ID, &s.PlanID, &s.SellerID, &s.NodeID, &status, &cartons, &labelled, &shipped, &received)
	if errors.Is(err, pgx.ErrNoRows) {
		return inbound.Shipment{}, ports.ErrNotFound
	}
	if err != nil {
		return inbound.Shipment{}, err
	}
	s.Status = inbound.Status(status)
	if s.Cartons, err = cartonsOf(cartons); err != nil {
		return inbound.Shipment{}, err
	}
	s.LabelledAt, s.ShippedAt, s.ReceivedAt = zeroIfNil(labelled), zeroIfNil(shipped), zeroIfNil(received)
	return s, nil
}

func zeroIfNil(t *time.Time) time.Time {
	if t == nil {
		return time.Time{}
	}
	return t.UTC()
}

// Get returns the shipment or ports.ErrNotFound.
func (r *InboundShipmentViewRepo) Get(ctx context.Context, shipmentID string) (inbound.Shipment, error) {
	return scanShipment(queryFor(ctx, r.pool).QueryRow(ctx, `SELECT `+shipmentColumns+` FROM inbound_shipments WHERE shipment_id = $1`, shipmentID))
}

// Save inserts or replaces the shipment row.
func (r *InboundShipmentViewRepo) Save(ctx context.Context, s inbound.Shipment) error {
	cartons, err := cartonsJSON(s.Cartons)
	if err != nil {
		return err
	}
	_, err = queryFor(ctx, r.pool).Exec(ctx, `
		INSERT INTO inbound_shipments (`+shipmentColumns+`) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
		ON CONFLICT (shipment_id) DO UPDATE SET plan_id = EXCLUDED.plan_id, seller_id = EXCLUDED.seller_id, node_id = EXCLUDED.node_id,
			status = EXCLUDED.status, cartons = EXCLUDED.cartons, labelled_at = EXCLUDED.labelled_at, shipped_at = EXCLUDED.shipped_at,
			received_at = EXCLUDED.received_at`,
		s.ID, s.PlanID, s.SellerID, s.NodeID, string(s.Status), cartons, nullTime(s.LabelledAt), nullTime(s.ShippedAt), nullTime(s.ReceivedAt))
	return err
}

// ListShipped returns the SHIPPED shipments with a manifest in the filter's
// nodes, oldest shipment first (keyset on shipped_at, shipment_id).
func (r *InboundShipmentViewRepo) ListShipped(ctx context.Context, f repository.InboundShipmentFilter) ([]inbound.Shipment, error) {
	sql := `SELECT ` + shipmentColumns + ` FROM inbound_shipments
		WHERE status = 'SHIPPED' AND shipped_at IS NOT NULL AND jsonb_array_length(cartons) > 0 AND node_id = ANY($1)`
	args := []any{f.NodeIDs}
	if f.ShippedAfter != nil {
		args = append(args, *f.ShippedAfter)
		sql += fmt.Sprintf(" AND shipped_at > $%d", len(args))
	}
	if f.After != nil {
		args = append(args, f.After.ShippedAt, f.After.ShipmentID)
		sql += fmt.Sprintf(" AND (shipped_at, shipment_id) > ($%d, $%d)", len(args)-1, len(args))
	}
	sql += " ORDER BY shipped_at, shipment_id"
	if f.Limit > 0 {
		args = append(args, f.Limit)
		sql += fmt.Sprintf(" LIMIT $%d", len(args))
	}
	rows, err := queryFor(ctx, r.pool).Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []inbound.Shipment
	for rows.Next() {
		s, err := scanShipment(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// CatalogPackagingViewRepo is the Postgres ports.CatalogPackagingViewRepository.
type CatalogPackagingViewRepo struct{ pool *pgxpool.Pool }

// NewCatalogPackagingViewRepo constructs a CatalogPackagingViewRepo over pool.
func NewCatalogPackagingViewRepo(pool *pgxpool.Pool) *CatalogPackagingViewRepo {
	return &CatalogPackagingViewRepo{pool: pool}
}

const packagingColumns = `catalog_item_id, level, gtin, child_gtin, units_per_pack, active, declared_at, retired_at`

func scanPackLevel(row pgx.Row) (inbound.PackLevel, error) {
	var (
		p                 inbound.PackLevel
		level             string
		declared, retired *time.Time
	)
	err := row.Scan(&p.CatalogItemID, &level, &p.GTIN, &p.ChildGTIN, &p.UnitsPerPack, &p.Active, &declared, &retired)
	if errors.Is(err, pgx.ErrNoRows) {
		return inbound.PackLevel{}, ports.ErrNotFound
	}
	p.Level, p.DeclaredAt, p.RetiredAt = inbound.Level(level), zeroIfNil(declared), zeroIfNil(retired)
	return p, err
}

// Get returns the (item, level, gtin) row or ports.ErrNotFound.
func (r *CatalogPackagingViewRepo) Get(ctx context.Context, item string, level inbound.Level, gtin string) (inbound.PackLevel, error) {
	return scanPackLevel(queryFor(ctx, r.pool).QueryRow(ctx,
		`SELECT `+packagingColumns+` FROM catalog_packaging WHERE catalog_item_id = $1 AND level = $2 AND gtin = $3`, item, string(level), gtin))
}

// Save inserts or replaces the row.
func (r *CatalogPackagingViewRepo) Save(ctx context.Context, p inbound.PackLevel) error {
	_, err := queryFor(ctx, r.pool).Exec(ctx, `
		INSERT INTO catalog_packaging (`+packagingColumns+`) VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
		ON CONFLICT (catalog_item_id, level, gtin) DO UPDATE SET child_gtin = EXCLUDED.child_gtin, units_per_pack = EXCLUDED.units_per_pack,
			active = EXCLUDED.active, declared_at = EXCLUDED.declared_at, retired_at = EXCLUDED.retired_at`,
		p.CatalogItemID, string(p.Level), p.GTIN, p.ChildGTIN, p.UnitsPerPack, p.Active, nullTime(p.DeclaredAt), nullTime(p.RetiredAt))
	return err
}

// ListByItem returns every row of the catalogue item.
func (r *CatalogPackagingViewRepo) ListByItem(ctx context.Context, item string) ([]inbound.PackLevel, error) {
	rows, err := queryFor(ctx, r.pool).Query(ctx, `SELECT `+packagingColumns+` FROM catalog_packaging WHERE catalog_item_id = $1 ORDER BY level, gtin`, item)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []inbound.PackLevel
	for rows.Next() {
		p, err := scanPackLevel(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
