// Command mcp is the composition root for the vendor-connectivity MCP server
// (docs/adr/0006-mcp-server-adoption.md): it wires env config to the outbound
// Postgres repositories, the repositories to the SAME application Service
// the REST adapter serves, and that Service's read surface to the inbound
// MCP adapter, then serves MCP over Streamable HTTP (and nothing else: no
// stdio, no SSE). It is a second, independent deployable alongside cmd/api.
//
// The surface is READ-ONLY: the adapter holds a Reads interface naming
// exactly the nine read methods the REST adapter's GET routes call, so no
// write use case is reachable through it. This binary never starts the
// submission loop, the order-expiry sweep or the retention sweep, never runs
// the outbox relay, never runs the local-copy consumers and never dials
// Kafka (no KAFKA_BROKERS, no consumer groups here): those run in cmd/api
// only. It makes no REST or MCP call to any sibling context beyond the one
// synchronous relay a read needs (the node scorecard, from node-performance,
// exactly as GET /vendor/nodes/{nodeId}/scorecard does).
//
// There is NO auth of any kind (fleet-wide revert 2026-09-11): no keys, no
// bearer checks. Access control is the in-cluster ClusterIP boundary.
//
// Environment:
//
//	MCP_ADDR                 listen address (default :8090)
//	DATABASE_URL             REQUIRED. Postgres DSN (the service database cmd/api owns)
//	MIGRATIONS_DATABASE_URL  direct DSN for the boot migrations (default DATABASE_URL)
//	 NODE_TOPOLOGY_URL, OFFER_AVAILABILITY_URL, SHIPPING_AND_TRACKING_URL,
//	 NODE_PERFORMANCE_URL     base URLs of the four sync calls, as cmd/api reads
//	                          them (ALL REQUIRED: the Service is shared, and
//	                          get_node_scorecard relays from node-performance)
//	LOG_LEVEL                debug | info | warn | error (default info)
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	inboundmcp "github.com/claudioed/vendor-connectivity/internal/adapters/inbound/mcp"
	"github.com/claudioed/vendor-connectivity/internal/adapters/outbound/clock"
	"github.com/claudioed/vendor-connectivity/internal/adapters/outbound/httpclient"
	"github.com/claudioed/vendor-connectivity/internal/adapters/outbound/ids"
	outboundkafka "github.com/claudioed/vendor-connectivity/internal/adapters/outbound/kafka"
	"github.com/claudioed/vendor-connectivity/internal/adapters/outbound/postgres"
	"github.com/claudioed/vendor-connectivity/internal/application/usecases"
	"github.com/claudioed/vendor-connectivity/internal/bootretry"
)

// shutdownTimeout bounds the HTTP server's graceful drain.
const shutdownTimeout = 10 * time.Second

// errNoDatabase is the boot error of a missing DATABASE_URL: this binary has
// no in-memory mode.
var errNoDatabase = errors.New("DATABASE_URL is required: this context runs on real Postgres")

// neighbourURLs are the base URLs of the four synchronous in-organization
// calls, the same variables cmd/api reads.
var neighbourURLs = []string{"NODE_TOPOLOGY_URL", "OFFER_AVAILABILITY_URL", "SHIPPING_AND_TRACKING_URL", "NODE_PERFORMANCE_URL"}

func main() {
	if err := run(); err != nil {
		slog.Error("mcp server exited with error", "error", err)
		os.Exit(1)
	}
}

func run() error {
	logger := newLogger(getenv("LOG_LEVEL", "info"))
	slog.SetDefault(logger)

	urls, err := loadNeighbourURLs(os.Getenv)
	if err != nil {
		return err
	}
	databaseURL := os.Getenv("DATABASE_URL")
	deps, closeAdapters, err := buildDeps(context.Background(), logger, databaseURL, getenv("MIGRATIONS_DATABASE_URL", databaseURL), urls)
	if err != nil {
		return err
	}
	defer closeAdapters()

	srv := &http.Server{
		Addr:              getenv("MCP_ADDR", ":8090"),
		Handler:           newRouter(inboundmcp.Handler(inboundmcp.NewServer(deps))),
		ReadHeaderTimeout: 5 * time.Second,
	}
	return serveMCP(logger, srv)
}

// loadNeighbourURLs validates the four sync-call base URLs before anything
// is opened. Every error names the variable, like cmd/api's loadRuntime.
func loadNeighbourURLs(env func(string) string) (map[string]string, error) {
	urls := map[string]string{}
	for _, name := range neighbourURLs {
		if env(name) == "" {
			return nil, fmt.Errorf("%s is required: the in-organization call it names has no default", name)
		}
		urls[name] = env(name)
	}
	return urls, nil
}

// buildDeps migrates the database, connects a pgx pool and wires the SAME
// application Service cmd/api builds, over the Postgres repositories. Only
// its read surface goes to the MCP adapter (inboundmcp.Reads); the write
// use cases exist on the Service but nothing here names them. The returned
// close func releases the pool. An empty databaseURL is a boot error: there
// is no fallback runtime.
//
// Like cmd/api this binary runs the idempotent embedded migrations on start,
// so it can boot against a fresh database before cmd/api has; golang-
// migrate's advisory lock makes concurrent starts safe.
// migrationsDatabaseURL is used ONLY for that step (a DIRECT Postgres DSN
// where DATABASE_URL points at PgBouncer, which cannot honour the
// session-scoped advisory lock); the runtime pool always uses databaseURL.
func buildDeps(ctx context.Context, logger *slog.Logger, databaseURL, migrationsDatabaseURL string, urls map[string]string) (inboundmcp.Deps, func(), error) {
	noop := func() {}
	if databaseURL == "" {
		return inboundmcp.Deps{}, noop, errNoDatabase
	}
	// The first outbound dial of an injected pod can be reset (Istio native
	// sidecars), so migrations and the first ping retry with backoff; on
	// exhaustion the process refuses to boot.
	if err := bootretry.Retry(ctx, logger, "run migrations", func() error {
		return postgres.RunMigrations(migrationsDatabaseURL)
	}); err != nil {
		return inboundmcp.Deps{}, noop, err
	}
	pool, err := postgres.NewPool(ctx, databaseURL)
	if err != nil {
		return inboundmcp.Deps{}, noop, err
	}
	if err := bootretry.Retry(ctx, logger, "ping postgres", func() error { return pool.Ping(ctx) }); err != nil {
		pool.Close()
		return inboundmcp.Deps{}, noop, err
	}
	logger.Info("postgres repositories configured")
	ob := postgres.NewOutboxRepo(pool)
	svc := usecases.New(usecases.Deps{
		UoW: postgres.NewUnitOfWork(pool), Vendors: postgres.NewVendorRepo(pool),
		Orders: postgres.NewPurchaseOrderRepo(pool), Submissions: postgres.NewSubmissionRepo(pool),
		Nodes: postgres.NewNodeViewRepo(pool), Listings: postgres.NewListingViewRepo(pool),
		Shipments: postgres.NewInboundShipmentViewRepo(pool), Packaging: postgres.NewCatalogPackagingViewRepo(pool),
		Outbox: postgres.NewEventOutbox(ob, outboundkafka.NewEncoder()), Inbox: postgres.NewProcessedEventRepo(pool),
		Clock: clock.System{}, IDs: ids.UUID{}, Random: ids.Random{},
		NodeTopology: httpclient.NewNodeTopology(urls["NODE_TOPOLOGY_URL"], nil),
		Offers:       httpclient.NewOfferAvailability(urls["OFFER_AVAILABILITY_URL"], nil),
		Shipping:     httpclient.NewShippingTracking(urls["SHIPPING_AND_TRACKING_URL"], nil),
		Performance:  httpclient.NewNodePerformance(urls["NODE_PERFORMANCE_URL"], nil),
	}, usecases.Config{Logger: logger})
	return inboundmcp.Deps{Reads: svc}, pool.Close, nil
}

// serveMCP runs srv until it fails or SIGINT/SIGTERM arrives, then drains it
// gracefully.
func serveMCP(logger *slog.Logger, srv *http.Server) error {
	serverErr := make(chan error, 1)
	go func() {
		logger.Info("mcp server listening (Streamable HTTP)", "addr", srv.Addr)
		serverErr <- srv.ListenAndServe()
	}()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	select {
	case err := <-serverErr:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	case <-ctx.Done():
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}

// newLogger builds the process-wide JSON logger. LOG_LEVEL maps
// debug|info|warn|warning|error (case-insensitive; "warning" is accepted
// like "warn" fleet-wide), defaulting to Info.
func newLogger(level string) *slog.Logger {
	var lvl slog.Level
	switch strings.ToLower(level) {
	case "debug":
		lvl = slog.LevelDebug
	case "warn", "warning":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: lvl}))
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
