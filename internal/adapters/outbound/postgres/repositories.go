package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/claudioed/vendor-connectivity/internal/application/ports"
	"github.com/claudioed/vendor-connectivity/internal/application/repository"
	"github.com/claudioed/vendor-connectivity/internal/domain/network"
	"github.com/claudioed/vendor-connectivity/internal/domain/purchaseorder"
	"github.com/claudioed/vendor-connectivity/internal/domain/submission"
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

// PurchaseOrderRepo is the Postgres ports.PurchaseOrderRepository.
type PurchaseOrderRepo struct{ pool *pgxpool.Pool }

// NewPurchaseOrderRepo constructs a PurchaseOrderRepo over pool.
func NewPurchaseOrderRepo(pool *pgxpool.Pool) *PurchaseOrderRepo {
	return &PurchaseOrderRepo{pool: pool}
}

const orderColumns = `po_number, vendor_id, node_id, assignment_id, release_no, order_id, group_no, tier, issued_at, required_ship_by, ack_by, state,
	lines, ack_decision, ack_reason_code, ack_transaction_id, labels, shipment_transaction_id, shipment_shipped_at, shipment_tracking,
	cancel_reason, cancelled_at, version`

type lineJSON struct {
	LineNo    int    `json:"lineNo"`
	ListingID string `json:"listingId"`
	Quantity  int    `json:"quantity"`
}

type labelJSON struct {
	PackageNo      int    `json:"packageNo"`
	LabelRef       string `json:"labelRef"`
	TrackingNumber string `json:"trackingNumber"`
	Carrier        string `json:"carrier"`
}

func scanOrder(row pgx.Row) (*purchaseorder.PurchaseOrder, error) {
	var (
		s                               purchaseorder.Snapshot
		lines, labels, tracking         []byte
		decision, reason, ackTx, shipTx *string
		shippedAt, cancelledAt          *time.Time
	)
	err := row.Scan(&s.Number, &s.VendorID, &s.NodeID, &s.AssignmentID, &s.ReleaseNo, &s.OrderID, &s.GroupNo, &s.Tier, &s.IssuedAt, &s.RequiredShipBy,
		&s.AckBy, &s.State, &lines, &decision, &reason, &ackTx, &labels, &shipTx, &shippedAt, &tracking, &s.CancelReason, &cancelledAt, &s.Version)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ports.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if err := fillOrder(&s, lines, labels, tracking, decision, reason, ackTx, shipTx, shippedAt, cancelledAt); err != nil {
		return nil, err
	}
	return purchaseorder.Restore(s)
}

func fillOrder(s *purchaseorder.Snapshot, lines, labels, tracking []byte, decision, reason, ackTx, shipTx *string, shippedAt, cancelledAt *time.Time) error {
	var ls []lineJSON
	if err := json.Unmarshal(lines, &ls); err != nil {
		return fmt.Errorf("decode lines: %w", err)
	}
	for _, l := range ls {
		s.Lines = append(s.Lines, purchaseorder.Line{LineNo: l.LineNo, ListingID: l.ListingID, Quantity: l.Quantity})
	}
	var lb []labelJSON
	if err := json.Unmarshal(labels, &lb); err != nil {
		return fmt.Errorf("decode labels: %w", err)
	}
	for _, l := range lb {
		s.Labels = append(s.Labels, purchaseorder.Label{PackageNo: l.PackageNo, LabelRef: l.LabelRef, TrackingNumber: l.TrackingNumber, Carrier: l.Carrier})
	}
	if decision != nil {
		a := purchaseorder.Ack{Decision: purchaseorder.Decision(*decision)}
		if reason != nil {
			a.ReasonCode = purchaseorder.RejectReason(*reason)
		}
		if ackTx != nil {
			a.TransactionID = *ackTx
		}
		s.Ack = &a
	}
	if shipTx != nil {
		sh := purchaseorder.Shipment{TransactionID: *shipTx}
		if shippedAt != nil {
			sh.ShippedAt = *shippedAt
		}
		if err := json.Unmarshal(tracking, &sh.TrackingNumbers); err != nil {
			return fmt.Errorf("decode tracking numbers: %w", err)
		}
		s.Shipment = &sh
	}
	if cancelledAt != nil {
		s.CancelledAt = *cancelledAt
	}
	return nil
}

// Get returns the order or ports.ErrNotFound.
func (r *PurchaseOrderRepo) Get(ctx context.Context, n purchaseorder.Number) (*purchaseorder.PurchaseOrder, error) {
	return scanOrder(queryFor(ctx, r.pool).QueryRow(ctx, `SELECT `+orderColumns+` FROM purchase_orders WHERE po_number = $1`, string(n)))
}

// FindByRelease returns the order of (assignmentId, releaseNo) or ports.ErrNotFound.
func (r *PurchaseOrderRepo) FindByRelease(ctx context.Context, assignmentID string, releaseNo int) (*purchaseorder.PurchaseOrder, error) {
	return scanOrder(queryFor(ctx, r.pool).QueryRow(ctx, `SELECT `+orderColumns+` FROM purchase_orders WHERE assignment_id = $1 AND release_no = $2`, assignmentID, releaseNo))
}

// FindLatestByAssignment returns the order with the highest releaseNo.
func (r *PurchaseOrderRepo) FindLatestByAssignment(ctx context.Context, assignmentID string) (*purchaseorder.PurchaseOrder, error) {
	return scanOrder(queryFor(ctx, r.pool).QueryRow(ctx,
		`SELECT `+orderColumns+` FROM purchase_orders WHERE assignment_id = $1 ORDER BY release_no DESC LIMIT 1`, assignmentID))
}

// List returns the matching orders ordered by (issuedAt, poNumber).
func (r *PurchaseOrderRepo) List(ctx context.Context, f repository.PurchaseOrderFilter) ([]*purchaseorder.PurchaseOrder, error) {
	sql := `SELECT ` + orderColumns + ` FROM purchase_orders WHERE true`
	var args []any
	add := func(clause string, v any) {
		args = append(args, v)
		sql += fmt.Sprintf(" AND "+clause, len(args))
	}
	if f.VendorID != "" {
		add("vendor_id = $%d", f.VendorID)
	}
	if f.NodeID != "" {
		add("node_id = $%d", f.NodeID)
	}
	if f.IssuedAfter != nil {
		add("issued_at > $%d", *f.IssuedAfter)
	}
	if f.After != nil {
		args = append(args, f.After.IssuedAt, f.After.PONumber)
		sql += fmt.Sprintf(" AND (issued_at, po_number) > ($%d, $%d)", len(args)-1, len(args))
	}
	sql += ` ORDER BY issued_at, po_number`
	if f.Limit > 0 {
		args = append(args, f.Limit)
		sql += fmt.Sprintf(" LIMIT $%d", len(args))
	}
	return r.queryOrders(ctx, sql, args...)
}

// ListExpirable returns ISSUED orders whose ackBy is before now, oldest ackBy first.
func (r *PurchaseOrderRepo) ListExpirable(ctx context.Context, now time.Time, limit int) ([]*purchaseorder.PurchaseOrder, error) {
	return r.queryOrders(ctx, `SELECT `+orderColumns+` FROM purchase_orders WHERE state = 'ISSUED' AND ack_by < $1 ORDER BY ack_by, po_number LIMIT $2`, now, limit)
}

func (r *PurchaseOrderRepo) queryOrders(ctx context.Context, sql string, args ...any) ([]*purchaseorder.PurchaseOrder, error) {
	rows, err := queryFor(ctx, r.pool).Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*purchaseorder.PurchaseOrder
	for rows.Next() {
		p, err := scanOrder(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// Save persists the order under the optimistic-concurrency rule.
func (r *PurchaseOrderRepo) Save(ctx context.Context, p *purchaseorder.PurchaseOrder) error {
	s := p.Snapshot()
	lines, labels, tracking, err := orderJSON(s)
	if err != nil {
		return err
	}
	var decision, reason, ackTx, shipTx *string
	var shippedAt *time.Time
	if s.Ack != nil {
		d, c, t := string(s.Ack.Decision), string(s.Ack.ReasonCode), s.Ack.TransactionID
		decision, ackTx = &d, &t
		if c != "" {
			reason = &c
		}
	}
	if s.Shipment != nil {
		shipTx, shippedAt = &s.Shipment.TransactionID, &s.Shipment.ShippedAt
	}
	q := queryFor(ctx, r.pool)
	if s.Version == 1 {
		_, err := q.Exec(ctx, `INSERT INTO purchase_orders (`+orderColumns+`)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23)`,
			string(s.Number), s.VendorID, s.NodeID, s.AssignmentID, s.ReleaseNo, s.OrderID, s.GroupNo, string(s.Tier), s.IssuedAt, s.RequiredShipBy, s.AckBy,
			string(s.State), lines, decision, reason, ackTx, labels, shipTx, shippedAt, tracking, s.CancelReason, nullTime(s.CancelledAt), s.Version)
		return saveErr(err)
	}
	tag, err := q.Exec(ctx, `UPDATE purchase_orders SET state = $2, ack_decision = $3, ack_reason_code = $4, ack_transaction_id = $5, labels = $6,
			shipment_transaction_id = $7, shipment_shipped_at = $8, shipment_tracking = $9, cancel_reason = $10, cancelled_at = $11, version = $12
		WHERE po_number = $1 AND version = $13`,
		string(s.Number), string(s.State), decision, reason, ackTx, labels, shipTx, shippedAt, tracking, s.CancelReason, nullTime(s.CancelledAt), s.Version, s.Version-1)
	return updated(tag, err)
}

func orderJSON(s purchaseorder.Snapshot) (lines, labels, tracking []byte, err error) {
	ls := make([]lineJSON, len(s.Lines))
	for i, l := range s.Lines {
		ls[i] = lineJSON{LineNo: l.LineNo, ListingID: l.ListingID, Quantity: l.Quantity}
	}
	lb := make([]labelJSON, len(s.Labels))
	for i, l := range s.Labels {
		lb[i] = labelJSON{PackageNo: l.PackageNo, LabelRef: l.LabelRef, TrackingNumber: l.TrackingNumber, Carrier: l.Carrier}
	}
	if lines, err = json.Marshal(ls); err != nil {
		return nil, nil, nil, err
	}
	if labels, err = json.Marshal(lb); err != nil {
		return nil, nil, nil, err
	}
	if s.Shipment != nil {
		if tracking, err = json.Marshal(s.Shipment.TrackingNumbers); err != nil {
			return nil, nil, nil, err
		}
	}
	return lines, labels, tracking, nil
}

// SubmissionRepo is the Postgres ports.SubmissionRepository.
type SubmissionRepo struct{ pool *pgxpool.Pool }

// NewSubmissionRepo constructs a SubmissionRepo over pool.
func NewSubmissionRepo(pool *pgxpool.Pool) *SubmissionRepo { return &SubmissionRepo{pool: pool} }

const submissionColumns = `transaction_id, vendor_id, kind, status, payload, po_number, node_id, result_node_id, received_at, not_before, processed_at, errors, attempts, version`

type errorJSON struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Path    string `json:"path,omitempty"`
}

func scanSubmission(row pgx.Row) (*submission.Submission, error) {
	var (
		s         submission.Snapshot
		errs      []byte
		processed *time.Time
	)
	err := row.Scan(&s.ID, &s.VendorID, &s.Kind, &s.Status, &s.Payload, &s.PONumber, &s.NodeID, &s.ResultNodeID, &s.ReceivedAt, &s.NotBefore, &processed, &errs, &s.Attempts, &s.Version)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ports.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if processed != nil {
		s.ProcessedAt = *processed
	}
	var es []errorJSON
	if err := json.Unmarshal(errs, &es); err != nil {
		return nil, fmt.Errorf("decode submission errors: %w", err)
	}
	for _, e := range es {
		s.Errors = append(s.Errors, submission.Error{Code: e.Code, Message: e.Message, Path: e.Path})
	}
	return submission.Restore(s)
}

// Get returns the submission or ports.ErrNotFound.
func (r *SubmissionRepo) Get(ctx context.Context, id submission.ID) (*submission.Submission, error) {
	return scanSubmission(queryFor(ctx, r.pool).QueryRow(ctx, `SELECT `+submissionColumns+` FROM submissions WHERE transaction_id = $1`, string(id)))
}

// ListDue returns PROCESSING submissions whose notBefore is not after now.
func (r *SubmissionRepo) ListDue(ctx context.Context, now time.Time, limit int) ([]*submission.Submission, error) {
	rows, err := queryFor(ctx, r.pool).Query(ctx, `SELECT `+submissionColumns+` FROM submissions
		WHERE status = 'PROCESSING' AND not_before <= $1 ORDER BY not_before, transaction_id LIMIT $2`, now, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*submission.Submission
	for rows.Next() {
		s, err := scanSubmission(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// Save persists the submission under the optimistic-concurrency rule.
func (r *SubmissionRepo) Save(ctx context.Context, sub *submission.Submission) error {
	s := sub.Snapshot()
	es := make([]errorJSON, len(s.Errors))
	for i, e := range s.Errors {
		es[i] = errorJSON{Code: e.Code, Message: e.Message, Path: e.Path}
	}
	errs, err := json.Marshal(es)
	if err != nil {
		return err
	}
	q := queryFor(ctx, r.pool)
	if s.Version == 1 {
		_, err := q.Exec(ctx, `INSERT INTO submissions (`+submissionColumns+`) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)`,
			string(s.ID), s.VendorID, string(s.Kind), string(s.Status), s.Payload, s.PONumber, s.NodeID, s.ResultNodeID, s.ReceivedAt, s.NotBefore,
			nullTime(s.ProcessedAt), errs, s.Attempts, s.Version)
		return saveErr(err)
	}
	tag, err := q.Exec(ctx, `UPDATE submissions SET status = $2, result_node_id = $3, processed_at = $4, errors = $5, attempts = $6, version = $7
		WHERE transaction_id = $1 AND version = $8`,
		string(s.ID), string(s.Status), s.ResultNodeID, nullTime(s.ProcessedAt), errs, s.Attempts, s.Version, s.Version-1)
	return updated(tag, err)
}

// DeleteProcessedBefore removes processed submissions processed before t.
func (r *SubmissionRepo) DeleteProcessedBefore(ctx context.Context, t time.Time) (int, error) {
	tag, err := queryFor(ctx, r.pool).Exec(ctx, `DELETE FROM submissions WHERE status <> 'PROCESSING' AND processed_at < $1`, t)
	if err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
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
