// Package usecases holds the application services: one method per command and
// query of the model and per REST operation, plus the consumers' policies.
// They orchestrate domain aggregates through ports; they perform no I/O of
// their own. Every command runs in ONE unit of work that saves the aggregates
// and appends their events to the outbox (and, for a consumer, claims the
// CloudEvents id in the inbox).
package usecases

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/claudioed/vendor-connectivity/internal/application/ports"
)

// Knobs are the simulation knobs of the Vendor API (model policy
// SimulationKnobs, assumption A18). The zero value is well-behaved.
type Knobs struct {
	// TxProcessingDelay is the least time a submission stays PROCESSING (TX_PROCESSING_DELAY).
	TxProcessingDelay time.Duration
	// TxFailureRate is the share of submissions that fail on purpose, 0..1 (TX_FAILURE_RATE).
	TxFailureRate float64
	// PORedeliveryRate is the chance a poll page repeats its first order, 0..1 (PO_REDELIVERY_RATE).
	PORedeliveryRate float64
}

// Config tunes the service.
type Config struct {
	Knobs Knobs
	// SubmissionMaxAttempts is how many passes a submission may end without an
	// outcome before it fails with ErrUpstreamUnavailable (A10). Default 5.
	SubmissionMaxAttempts int
	// PageSize is the default poll page size. Default 50.
	PageSize int
	// SubmissionRetention is how long processed submissions are kept (A22). Default 30 days.
	SubmissionRetention time.Duration
	Logger              *slog.Logger
}

// Page size and retention defaults.
const (
	DefaultPageSize              = 50
	MaxPageSize                  = 200
	DefaultSubmissionMaxAttempts = 5
	DefaultSubmissionRetention   = 30 * 24 * time.Hour
	batchSize                    = 100
)

// Deps are the out ports the service needs.
type Deps struct {
	UoW          ports.UnitOfWork
	Vendors      ports.VendorRepository
	Orders       ports.PurchaseOrderRepository
	Submissions  ports.SubmissionRepository
	Nodes        ports.NodeViewRepository
	Listings     ports.ListingViewRepository
	Outbox       ports.EventOutbox
	Inbox        ports.ProcessedEvents
	Clock        ports.Clock
	IDs          ports.IDGenerator
	Random       ports.Random
	NodeTopology ports.NodeTopology
	Offers       ports.OfferAvailability
	Shipping     ports.ShippingTracking
	Performance  ports.NodePerformance
}

// Service is the application service of vendor-connectivity.
type Service struct {
	d   Deps
	cfg Config
}

// New builds the service, filling the Config defaults.
func New(d Deps, cfg Config) *Service {
	if cfg.SubmissionMaxAttempts <= 0 {
		cfg.SubmissionMaxAttempts = DefaultSubmissionMaxAttempts
	}
	if cfg.PageSize <= 0 {
		cfg.PageSize = DefaultPageSize
	}
	if cfg.SubmissionRetention <= 0 {
		cfg.SubmissionRetention = DefaultSubmissionRetention
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	return &Service{d: d, cfg: cfg}
}

// atomically runs fn in one unit of work and re-runs the whole unit once when
// a save lost an optimistic-concurrency race (the caller re-reads and retries).
func (s *Service) atomically(ctx context.Context, fn func(ctx context.Context) error) error {
	err := s.d.UoW.Do(ctx, fn)
	if errors.Is(err, ports.ErrConcurrentUpdate) {
		err = s.d.UoW.Do(ctx, fn)
	}
	return err
}

// consume is the inbox wrapper of every consumer policy: claim the event id,
// run fn, commit both together. A duplicate id is skipped.
func (s *Service) consume(ctx context.Context, eventID, eventType string, fn func(ctx context.Context) error) error {
	return s.atomically(ctx, func(ctx context.Context) error {
		first, err := s.d.Inbox.Claim(ctx, eventID, eventType, s.d.Clock.Now())
		if err != nil {
			return err
		}
		if !first {
			return nil
		}
		return fn(ctx)
	})
}
