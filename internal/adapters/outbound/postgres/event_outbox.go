package postgres

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/claudioed/vendor-connectivity/internal/application/outbox"
	"github.com/claudioed/vendor-connectivity/internal/application/ports"
	"github.com/claudioed/vendor-connectivity/internal/domain/shared"
)

// EventOutbox is the Postgres ports.EventOutbox: it encodes domain events into
// CloudEvents messages and inserts them into outbox_events on the transaction
// carried by ctx, so they commit with the aggregates that raised them.
type EventOutbox struct {
	repo *OutboxRepo
	enc  outbox.Encoder
}

// NewEventOutbox composes the outbox table with an encoder.
func NewEventOutbox(repo *OutboxRepo, enc outbox.Encoder) *EventOutbox {
	return &EventOutbox{repo: repo, enc: enc}
}

var _ ports.EventOutbox = (*EventOutbox)(nil)

// Append encodes and stores events in order; no events is a no-op.
func (o *EventOutbox) Append(ctx context.Context, events ...shared.Event) error {
	if len(events) == 0 {
		return nil
	}
	msgs, err := o.enc.Encode(events...)
	if err != nil {
		return err
	}
	return o.repo.Insert(ctx, msgs...)
}

// PurgeIdempotencyKeys deletes the Idempotency-Key rows created before t and
// returns how many it deleted (the 24 hour TTL of the model conventions; an
// expired key is also forgotten on its next use).
func PurgeIdempotencyKeys(ctx context.Context, pool *pgxpool.Pool, t time.Time) (int, error) {
	tag, err := pool.Exec(ctx, `DELETE FROM idempotency_keys WHERE created_at < $1`, t)
	if err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
}
