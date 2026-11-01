package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/claudioed/vendor-connectivity/internal/application/ports"
	"github.com/claudioed/vendor-connectivity/internal/domain/submission"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

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
