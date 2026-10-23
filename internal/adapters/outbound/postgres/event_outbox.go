package postgres

import (
	"context"

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
