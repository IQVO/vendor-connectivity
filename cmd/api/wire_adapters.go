package main

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"

	inboundhttp "github.com/claudioed/vendor-connectivity/internal/adapters/inbound/http"
	"github.com/claudioed/vendor-connectivity/internal/adapters/outbound/clock"
	"github.com/claudioed/vendor-connectivity/internal/adapters/outbound/httpclient"
	"github.com/claudioed/vendor-connectivity/internal/adapters/outbound/ids"
	outboundkafka "github.com/claudioed/vendor-connectivity/internal/adapters/outbound/kafka"
	outboxrelay "github.com/claudioed/vendor-connectivity/internal/adapters/outbound/outbox"
	"github.com/claudioed/vendor-connectivity/internal/adapters/outbound/postgres"
	"github.com/claudioed/vendor-connectivity/internal/adapters/outbound/postgres/pgtx"
	"github.com/claudioed/vendor-connectivity/internal/application/usecases"
	"github.com/claudioed/vendor-connectivity/internal/bootretry"
)

// newReadiness builds the /readyz gate: the drain gate plus a Postgres ping
// and a Kafka broker answering a topic-less metadata request (never topics:
// the broker auto-creates them on first write), cached 3s, bounded 2s. The
// ping is a parameter so the gate is testable without a pool.
func newReadiness(ping func(context.Context) error, brokers []string) *inboundhttp.Readiness {
	return inboundhttp.NewReadiness(inboundhttp.DefaultReadinessTTL, inboundhttp.DefaultReadinessTimeout,
		inboundhttp.Dependency{Name: "postgres", Check: ping},
		inboundhttp.Dependency{Name: "kafka", Check: outboundkafka.NewBrokerCheck(brokers).Check},
	)
}

// adapters is the set of Postgres adapters the composition root wires.
type adapters struct {
	pool        *pgxpool.Pool
	outboxStore outboxrelay.Store
	idempotency func(http.Handler) http.Handler
	deps        usecases.Deps
}

// buildAdapters runs the embedded migrations, then wires the Postgres adapters.
// An empty databaseURL is a boot error: there is no in-memory runtime.
func buildAdapters(ctx context.Context, databaseURL string, logger *slog.Logger) (adapters, func(), error) {
	noop := func() {}
	if databaseURL == "" {
		return adapters{}, noop, errMissingDatabaseURL
	}
	// The first outbound dial of an injected pod can be reset (native
	// sidecars), so migrations and the first ping retry with backoff; on
	// exhaustion the LAST error is returned and the process refuses to boot.
	migrationsURL := getenv("MIGRATIONS_DATABASE_URL", databaseURL)
	if err := bootretry.Retry(ctx, logger, "run migrations", func() error { return postgres.RunMigrations(migrationsURL) }); err != nil {
		return adapters{}, noop, err
	}
	pool, err := postgres.NewPool(ctx, databaseURL)
	if err != nil {
		return adapters{}, noop, err
	}
	if err := bootretry.Retry(ctx, logger, "ping postgres", func() error { return pool.Ping(ctx) }); err != nil {
		pool.Close()
		return adapters{}, noop, err
	}
	logger.Info("postgres adapters configured")
	return postgresAdapters(pool), pool.Close, nil
}

func postgresAdapters(pool *pgxpool.Pool) adapters {
	ob := postgres.NewOutboxRepo(pool)
	return adapters{
		pool:        pool,
		outboxStore: ob,
		idempotency: inboundhttp.RequireIdempotencyKey(pool, pgtx.With),
		deps: usecases.Deps{
			UoW: postgres.NewUnitOfWork(pool), Vendors: postgres.NewVendorRepo(pool), Orders: postgres.NewPurchaseOrderRepo(pool),
			Submissions: postgres.NewSubmissionRepo(pool), Nodes: postgres.NewNodeViewRepo(pool), Listings: postgres.NewListingViewRepo(pool),
			Shipments: postgres.NewInboundShipmentViewRepo(pool), Packaging: postgres.NewCatalogPackagingViewRepo(pool),
			Outbox: postgres.NewEventOutbox(ob, outboundkafka.NewEncoder()), Inbox: postgres.NewProcessedEventRepo(pool),
			Clock: clock.System{}, IDs: ids.UUID{}, Random: ids.Random{},
		},
	}
}

// buildService adds the four neighbour clients to the adapters' dependencies.
func buildService(ad adapters, rt runtimeEnv, logger *slog.Logger) *usecases.Service {
	deps := ad.deps
	deps.NodeTopology = httpclient.NewNodeTopology(rt.urls["NODE_TOPOLOGY_URL"], nil)
	deps.Offers = httpclient.NewOfferAvailability(rt.urls["OFFER_AVAILABILITY_URL"], nil)
	deps.Shipping = httpclient.NewShippingTracking(rt.urls["SHIPPING_AND_TRACKING_URL"], nil)
	deps.Performance = httpclient.NewNodePerformance(rt.urls["NODE_PERFORMANCE_URL"], nil)
	return usecases.New(deps, usecases.Config{Knobs: rt.knobs, Logger: logger})
}
