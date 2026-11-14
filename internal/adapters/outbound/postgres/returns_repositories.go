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
	"github.com/claudioed/vendor-connectivity/internal/domain/returns"
)

// adviceLineRow is the stored JSON form of one advice line.
type adviceLineRow struct {
	LineNo        int    `json:"lineNo"`
	CatalogItemID string `json:"catalogItemId"`
	Quantity      int    `json:"quantity"`
	OwnerRef      string `json:"ownerRef,omitempty"`
}

func adviceLinesJSON(ls []returns.Line) ([]byte, error) {
	rows := make([]adviceLineRow, 0, len(ls))
	for _, l := range ls {
		rows = append(rows, adviceLineRow{LineNo: l.LineNo, CatalogItemID: l.CatalogItemID, Quantity: l.Quantity, OwnerRef: l.OwnerRef})
	}
	return json.Marshal(rows)
}

func adviceLinesOf(raw []byte) ([]returns.Line, error) {
	var rows []adviceLineRow
	if err := json.Unmarshal(raw, &rows); err != nil {
		return nil, fmt.Errorf("decode return advice lines: %w", err)
	}
	out := make([]returns.Line, 0, len(rows))
	for _, r := range rows {
		out = append(out, returns.Line{LineNo: r.LineNo, CatalogItemID: r.CatalogItemID, Quantity: r.Quantity, OwnerRef: r.OwnerRef})
	}
	return out, nil
}

func trackingJSON(ts []string) ([]byte, error) {
	if ts == nil {
		ts = []string{}
	}
	return json.Marshal(ts)
}

func trackingOf(raw []byte) ([]string, error) {
	var out []string
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("decode return advice tracking numbers: %w", err)
	}
	if out == nil {
		out = []string{}
	}
	return out, nil
}

// ReturnAdviceViewRepo is the Postgres ports.ReturnAdviceViewRepository.
type ReturnAdviceViewRepo struct{ pool *pgxpool.Pool }

// NewReturnAdviceViewRepo constructs a ReturnAdviceViewRepo over pool.
func NewReturnAdviceViewRepo(pool *pgxpool.Pool) *ReturnAdviceViewRepo {
	return &ReturnAdviceViewRepo{pool: pool}
}

const adviceColumns = `authorization_id, order_id, group_no, vendor_id, node_id, disposition, method, state, lines, tracking_numbers, ready_at, expires_at`

func scanAdvice(row pgx.Row) (returns.Advice, error) {
	var (
		a               returns.Advice
		state           string
		lines, tracking []byte
		ready           time.Time
		expires         *time.Time
	)
	err := row.Scan(&a.AuthorizationID, &a.OrderID, &a.GroupNo, &a.VendorID, &a.NodeID, &a.Disposition, &a.Method, &state,
		&lines, &tracking, &ready, &expires)
	if errors.Is(err, pgx.ErrNoRows) {
		return returns.Advice{}, ports.ErrNotFound
	}
	if err != nil {
		return returns.Advice{}, err
	}
	a.State = returns.State(state)
	if a.Lines, err = adviceLinesOf(lines); err != nil {
		return returns.Advice{}, err
	}
	if a.TrackingNumbers, err = trackingOf(tracking); err != nil {
		return returns.Advice{}, err
	}
	a.ReadyAt, a.AuthorizedAt = ready.UTC(), ready.UTC()
	a.ExpiresAt = zeroIfNil(expires)
	return a, nil
}

// Get returns the advice or ports.ErrNotFound.
func (r *ReturnAdviceViewRepo) Get(ctx context.Context, authorizationID string) (returns.Advice, error) {
	return scanAdvice(queryFor(ctx, r.pool).QueryRow(ctx, `SELECT `+adviceColumns+` FROM vendor_return_advices WHERE authorization_id = $1`, authorizationID))
}

// Save inserts or replaces the advice row.
func (r *ReturnAdviceViewRepo) Save(ctx context.Context, a returns.Advice) error {
	lines, err := adviceLinesJSON(a.Lines)
	if err != nil {
		return err
	}
	tracking, err := trackingJSON(a.TrackingNumbers)
	if err != nil {
		return err
	}
	_, err = queryFor(ctx, r.pool).Exec(ctx, `
		INSERT INTO vendor_return_advices (`+adviceColumns+`) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
		ON CONFLICT (authorization_id) DO UPDATE SET order_id = EXCLUDED.order_id, group_no = EXCLUDED.group_no,
			vendor_id = EXCLUDED.vendor_id, node_id = EXCLUDED.node_id, disposition = EXCLUDED.disposition, method = EXCLUDED.method,
			state = EXCLUDED.state, lines = EXCLUDED.lines, tracking_numbers = EXCLUDED.tracking_numbers,
			ready_at = EXCLUDED.ready_at, expires_at = EXCLUDED.expires_at`,
		a.AuthorizationID, a.OrderID, a.GroupNo, a.VendorID, a.NodeID, a.Disposition, a.Method, string(a.State), lines, tracking,
		a.ReadyAt, nullTime(a.ExpiresAt))
	return err
}

// ListReady returns the AUTHORIZED advices of the filter's nodes, oldest
// readyAt first (keyset on ready_at, authorization_id).
//
//nolint:dupl // the deliberate mirror of InboundShipmentViewRepo.ListShipped (ADR 0014 reuses the ADR 0005 poll)
func (r *ReturnAdviceViewRepo) ListReady(ctx context.Context, f repository.ReturnAdviceFilter) ([]returns.Advice, error) {
	sql := `SELECT ` + adviceColumns + ` FROM vendor_return_advices
		WHERE state = 'AUTHORIZED' AND node_id = ANY($1)`
	args := []any{f.NodeIDs}
	if f.ReadyAfter != nil {
		args = append(args, *f.ReadyAfter)
		sql += fmt.Sprintf(" AND ready_at > $%d", len(args))
	}
	if f.After != nil {
		args = append(args, f.After.ReadyAt, f.After.AuthorizationID)
		sql += fmt.Sprintf(" AND (ready_at, authorization_id) > ($%d, $%d)", len(args)-1, len(args))
	}
	sql += " ORDER BY ready_at, authorization_id"
	if f.Limit > 0 {
		args = append(args, f.Limit)
		sql += fmt.Sprintf(" LIMIT $%d", len(args))
	}
	rows, err := queryFor(ctx, r.pool).Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []returns.Advice
	for rows.Next() {
		a, err := scanAdvice(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}
