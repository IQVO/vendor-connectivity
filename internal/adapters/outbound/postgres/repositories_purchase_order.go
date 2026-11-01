package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/claudioed/vendor-connectivity/internal/application/ports"
	"github.com/claudioed/vendor-connectivity/internal/application/repository"
	"github.com/claudioed/vendor-connectivity/internal/domain/purchaseorder"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

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
