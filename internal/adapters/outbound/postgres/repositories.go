package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/claudioed/vendor-connectivity/internal/application/ports"
	"github.com/claudioed/vendor-connectivity/internal/domain/network"
	"github.com/claudioed/vendor-connectivity/internal/domain/vendor"
)

// uniqueViolation is the SQLSTATE of a duplicate key.
const uniqueViolation = "23505"

// saveErr maps a failed INSERT: a duplicate key is a lost create race.
func saveErr(err error) error {
	var pg *pgconn.PgError
	if errors.As(err, &pg) && pg.Code == uniqueViolation {
		return ports.ErrConcurrentUpdate
	}
	return err
}

// VendorRepo is the Postgres ports.VendorRepository.
type VendorRepo struct{ pool *pgxpool.Pool }

// NewVendorRepo constructs a VendorRepo over pool.
func NewVendorRepo(pool *pgxpool.Pool) *VendorRepo { return &VendorRepo{pool: pool} }

// Get returns the account or ports.ErrNotFound.
func (r *VendorRepo) Get(ctx context.Context, id vendor.ID) (*vendor.Account, error) {
	var (
		s         vendor.Snapshot
		suspended *time.Time
	)
	err := queryFor(ctx, r.pool).QueryRow(ctx, `
		SELECT vendor_id, name, ack_window_minutes, state, onboarded_at, suspended_at, suspend_reason, version
		FROM vendor_accounts WHERE vendor_id = $1`, string(id)).
		Scan(&s.ID, &s.Name, &s.AckWindowMinutes, &s.State, &s.OnboardedAt, &suspended, &s.SuspendReason, &s.Version)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ports.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if suspended != nil {
		s.SuspendedAt = *suspended
	}
	return vendor.Restore(s)
}

// Save persists the account under the optimistic-concurrency rule.
func (r *VendorRepo) Save(ctx context.Context, a *vendor.Account) error {
	s := a.Snapshot()
	q := queryFor(ctx, r.pool)
	if s.Version == 1 {
		_, err := q.Exec(ctx, `
			INSERT INTO vendor_accounts (vendor_id, name, ack_window_minutes, state, onboarded_at, suspended_at, suspend_reason, version)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
			string(s.ID), s.Name, s.AckWindowMinutes, string(s.State), s.OnboardedAt, nullTime(s.SuspendedAt), s.SuspendReason, s.Version)
		return saveErr(err)
	}
	tag, err := q.Exec(ctx, `
		UPDATE vendor_accounts SET name = $2, ack_window_minutes = $3, state = $4, suspended_at = $5, suspend_reason = $6, version = $7
		WHERE vendor_id = $1 AND version = $8`,
		string(s.ID), s.Name, s.AckWindowMinutes, string(s.State), nullTime(s.SuspendedAt), s.SuspendReason, s.Version, s.Version-1)
	return updated(tag, err)
}

func updated(tag pgconn.CommandTag, err error) error {
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ports.ErrConcurrentUpdate
	}
	return nil
}

func nullTime(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

// NodeViewRepo is the Postgres ports.NodeViewRepository.
type NodeViewRepo struct{ pool *pgxpool.Pool }

// NewNodeViewRepo constructs a NodeViewRepo over pool.
func NewNodeViewRepo(pool *pgxpool.Pool) *NodeViewRepo { return &NodeViewRepo{pool: pool} }

const nodeColumns = `node_id, operator, vendor_id, vendor_node_code, node_type, subtype, region_id, timezone, status, handling_time_minutes, profile_revision`

func scanNode(row pgx.Row) (network.Node, error) {
	var n network.Node
	err := row.Scan(&n.ID, &n.Operator, &n.VendorID, &n.VendorNodeCode, &n.NodeType, &n.Subtype, &n.RegionID, &n.Timezone, &n.Status, &n.HandlingTimeMinutes, &n.ProfileRevision)
	if errors.Is(err, pgx.ErrNoRows) {
		return network.Node{}, ports.ErrNotFound
	}
	return n, err
}

// Get returns the node or ports.ErrNotFound.
func (r *NodeViewRepo) Get(ctx context.Context, nodeID string) (network.Node, error) {
	return scanNode(queryFor(ctx, r.pool).QueryRow(ctx, `SELECT `+nodeColumns+` FROM vendor_nodes WHERE node_id = $1`, nodeID))
}

// ListByVendor returns the vendor's nodes ordered by id.
func (r *NodeViewRepo) ListByVendor(ctx context.Context, vendorID string) ([]network.Node, error) {
	rows, err := queryFor(ctx, r.pool).Query(ctx, `SELECT `+nodeColumns+` FROM vendor_nodes WHERE operator = 'VENDOR' AND vendor_id = $1 ORDER BY node_id`, vendorID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []network.Node
	for rows.Next() {
		n, err := scanNode(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// Save inserts or replaces the node row.
func (r *NodeViewRepo) Save(ctx context.Context, n network.Node) error {
	_, err := queryFor(ctx, r.pool).Exec(ctx, `
		INSERT INTO vendor_nodes (`+nodeColumns+`) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
		ON CONFLICT (node_id) DO UPDATE SET operator = EXCLUDED.operator, vendor_id = EXCLUDED.vendor_id, vendor_node_code = EXCLUDED.vendor_node_code,
			node_type = EXCLUDED.node_type, subtype = EXCLUDED.subtype, region_id = EXCLUDED.region_id, timezone = EXCLUDED.timezone,
			status = EXCLUDED.status, handling_time_minutes = EXCLUDED.handling_time_minutes, profile_revision = EXCLUDED.profile_revision`,
		n.ID, string(n.Operator), n.VendorID, n.VendorNodeCode, n.NodeType, n.Subtype, n.RegionID, n.Timezone, string(n.Status), n.HandlingTimeMinutes, n.ProfileRevision)
	return err
}

// ListingViewRepo is the Postgres ports.ListingViewRepository.
type ListingViewRepo struct{ pool *pgxpool.Pool }

// NewListingViewRepo constructs a ListingViewRepo over pool.
func NewListingViewRepo(pool *pgxpool.Pool) *ListingViewRepo { return &ListingViewRepo{pool: pool} }

// Get returns the listing or ports.ErrNotFound.
func (r *ListingViewRepo) Get(ctx context.Context, listingID string) (network.Listing, error) {
	var l network.Listing
	err := queryFor(ctx, r.pool).QueryRow(ctx, `SELECT listing_id, node_id, catalog_item_id FROM listings WHERE listing_id = $1`, listingID).Scan(&l.ID, &l.NodeID, &l.CatalogItemID)
	if errors.Is(err, pgx.ErrNoRows) {
		return network.Listing{}, ports.ErrNotFound
	}
	return l, err
}

// Save inserts or replaces the listing row.
func (r *ListingViewRepo) Save(ctx context.Context, l network.Listing) error {
	_, err := queryFor(ctx, r.pool).Exec(ctx, `
		INSERT INTO listings (listing_id, node_id, catalog_item_id) VALUES ($1, $2, $3)
		ON CONFLICT (listing_id) DO UPDATE SET node_id = EXCLUDED.node_id, catalog_item_id = EXCLUDED.catalog_item_id`, l.ID, l.NodeID, l.CatalogItemID)
	return err
}

// ProcessedEventRepo is the Postgres ports.ProcessedEvents (the inbox).
type ProcessedEventRepo struct{ pool *pgxpool.Pool }

// NewProcessedEventRepo constructs a ProcessedEventRepo over pool.
func NewProcessedEventRepo(pool *pgxpool.Pool) *ProcessedEventRepo {
	return &ProcessedEventRepo{pool: pool}
}

// Claim records the CloudEvents id and reports whether it was new. Inside a
// unit of work the INSERT shares the transaction of the event's effects, so a
// rollback un-claims it. A concurrent claim of the same id blocks on the primary
// key until the other transaction ends.
func (r *ProcessedEventRepo) Claim(ctx context.Context, eventID, eventType string, at time.Time) (bool, error) {
	tag, err := queryFor(ctx, r.pool).Exec(ctx, `
		INSERT INTO processed_events (event_id, event_type, processed_at) VALUES ($1, $2, $3) ON CONFLICT (event_id) DO NOTHING`, eventID, eventType, at)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}
