// Command api is vendor-connectivity's composition root: it wires env config
// into adapters, adapters into the application service, and the service into
// the Vendor API router, the three Kafka consumers (ADR 0003), the outbox relay
// and the background loops (submission processing, order expiry, retention),
// all on real Postgres and real Kafka: there is no in-memory mode, no log sink
// and no consumer that is off.
//
// Environment:
//
//	HTTP_ADDR                    listen address (default :8080)
//	DATABASE_URL                 Postgres DSN (REQUIRED)
//	MIGRATIONS_DATABASE_URL      direct DSN for the boot migrations (default DATABASE_URL)
//	KAFKA_BROKERS                comma-separated brokers (REQUIRED: relay, consumers, /readyz)
//	EVENT_PUBLISHER              kafka (default kafka); log is a boot error
//	NODE_CONSUMER_GROUP, LISTING_CONSUMER_GROUP, SOURCING_CONSUMER_GROUP
//	                             stable group ids (ALL REQUIRED, boot error naming the variable)
//	NODE_TOPOLOGY_URL, OFFER_AVAILABILITY_URL, SHIPPING_AND_TRACKING_URL, NODE_PERFORMANCE_URL
//	                             base URLs of the four in-organization sync calls (ALL REQUIRED)
//	TX_PROCESSING_DELAY          least time a submission stays PROCESSING (Go duration, default 0)
//	TX_FAILURE_RATE              share of submissions that fail on purpose, 0..1 (default 0)
//	PO_REDELIVERY_RATE           chance a poll page repeats its first order, 0..1 (default 0)
//	RATE_LIMIT_RPS               requests a second over /vendor/* (default 0 = unlimited)
//	SUBMISSION_POLL_INTERVAL     how often due submissions are processed (default 1s)
//	EXPIRY_SWEEP_INTERVAL        how often unacknowledged orders are expired (default 30s)
//	OUTBOX_RELAY_INTERVAL        relay poll interval (Go duration or seconds, default 1s)
//	OUTBOX_MAX_ATTEMPTS          failures before the relay excludes a row (default 20, 0 = unlimited)
//	SHUTDOWN_DRAIN_DELAY         wait after flipping /readyz before closing (default 5s)
//	LOG_LEVEL                    debug|info|warn|error (default info)
//	OTEL_SERVICE_NAME            service.name of spans and metrics (default vendor-connectivity)
//	OTEL_EXPORTER_OTLP_ENDPOINT  Collector OTLP/gRPC endpoint (default telemetry.DefaultEndpoint); export never blocks
//	SERVICE_VERSION              service.version (default dev)
//	ENVIRONMENT                  deployment.environment.name (default local)
//
// GET /readyz is 200 only while Postgres answers a ping AND a Kafka broker
// answers a metadata request (verdict cached 3s, bounded 2s); /healthz is
// liveness only. The process exits non-zero when the relay, a consumer or a
// loop returns on its own, so the orchestrator restarts it.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	inboundhttp "github.com/claudioed/vendor-connectivity/internal/adapters/inbound/http"
	inboundkafka "github.com/claudioed/vendor-connectivity/internal/adapters/inbound/kafka"
	"github.com/claudioed/vendor-connectivity/internal/adapters/outbound/clock"
	"github.com/claudioed/vendor-connectivity/internal/adapters/outbound/httpclient"
	"github.com/claudioed/vendor-connectivity/internal/adapters/outbound/ids"
	outboundkafka "github.com/claudioed/vendor-connectivity/internal/adapters/outbound/kafka"
	outboxrelay "github.com/claudioed/vendor-connectivity/internal/adapters/outbound/outbox"
	"github.com/claudioed/vendor-connectivity/internal/adapters/outbound/postgres"
	"github.com/claudioed/vendor-connectivity/internal/adapters/outbound/postgres/pgtx"
	"github.com/claudioed/vendor-connectivity/internal/adapters/outbound/telemetry"
	"github.com/claudioed/vendor-connectivity/internal/application/usecases"
	"github.com/claudioed/vendor-connectivity/internal/bootretry"
)

// shutdownTimeout bounds the HTTP server's graceful drain.
const shutdownTimeout = 10 * time.Second

// workerDrainTimeout bounds how long shutdown waits for the consumers, the
// loops and the relay to return after their context is cancelled.
const workerDrainTimeout = 10 * time.Second

// DefaultShutdownDrainDelay is how long shutdown waits, after flipping
// /readyz to not-ready, before closing the listener (SHUTDOWN_DRAIN_DELAY
// overrides it; "0" disables it).
const DefaultShutdownDrainDelay = 5 * time.Second

// Loop defaults.
const (
	defaultSubmissionPoll = time.Second
	defaultExpirySweep    = 30 * time.Second
	retentionSweep        = 24 * time.Hour
)

func main() {
	if err := run(context.Background()); err != nil {
		slog.Error("service exited with error", "error", err)
		os.Exit(1)
	}
}

// run boots the service and serves until SIGINT/SIGTERM or until ctx is
// cancelled (the integration test stops the real entry point that way).
func run(ctx context.Context) error {
	logger := newLogger(getenv("LOG_LEVEL", "info"))
	slog.SetDefault(logger)

	// Misconfiguration is refused before anything is opened or bound.
	rt, err := loadRuntime(os.Getenv)
	if err != nil {
		return err
	}

	// Telemetry comes up before the router is built (otelchimetric binds the
	// global MeterProvider at construction) and is flushed LAST: deferred before
	// everything below, so the final spans of the drain still leave the process.
	tel := loadTelemetryEnv(os.Getenv)
	flushTelemetry, err := startTelemetry(logger, tel)
	if err != nil {
		return err
	}
	defer flushTelemetry()

	// closeAdapters (pool.Close) is deferred FIRST so it runs LAST, after the
	// HTTP drain, the consumers, the loops and the relay have all stopped.
	ad, closeAdapters, err := buildAdapters(ctx, os.Getenv("DATABASE_URL"), logger)
	if err != nil {
		return err
	}
	defer closeAdapters()

	// The business counter binds to the MeterProvider telemetry.Setup installed.
	submissionMetrics, err := telemetry.NewSubmissionMetrics()
	if err != nil {
		return err
	}
	ad.deps.Metrics = submissionMetrics

	svc := buildService(ad, rt, logger)
	readiness := newReadiness(ad.pool.Ping, rt.brokers)
	httpServer := &http.Server{
		Addr: getenv("HTTP_ADDR", ":8080"),
		Handler: inboundhttp.NewRouter(&inboundhttp.Server{
			Service: svc, Idempotency: ad.idempotency, Limiter: inboundhttp.NewLimiter(rt.rateLimitRPS), Readiness: readiness,
		}, logger, tel.serviceName),
		ReadHeaderTimeout: 5 * time.Second,
	}

	background, stopBackground := startBackground(ad, rt, svc, logger)
	relay, closeRelay, err := startOutboxRelay(ad.outboxStore, rt.brokers, logger)
	if err != nil {
		stopBackground()
		return err
	}
	return serveUntilSignal(ctx, logger, httpServer, readiness, background, stopBackground, relay, closeRelay)
}

// Boot errors for the variables the context cannot run without. Each message
// names the variable.
var (
	errMissingDatabaseURL = errors.New("DATABASE_URL is required: this context runs on real Postgres, there is no in-memory mode")
	errMissingBrokers     = errors.New("KAFKA_BROKERS is required: this context runs on real Kafka (outbox relay, consumers, /readyz)")
)

// consumerGroups are the mandatory consumer group variables.
var consumerGroups = []string{"NODE_CONSUMER_GROUP", "LISTING_CONSUMER_GROUP", "SOURCING_CONSUMER_GROUP", "INBOUND_CONSUMER_GROUP", "PACKAGING_CONSUMER_GROUP"}

// neighbourURLs are the mandatory base URLs of the four sync calls (A24).
var neighbourURLs = []string{"NODE_TOPOLOGY_URL", "OFFER_AVAILABILITY_URL", "SHIPPING_AND_TRACKING_URL", "NODE_PERFORMANCE_URL"}

// runtimeEnv is the validated environment: the brokers, every consumer group,
// the neighbours' URLs and the simulation knobs.
type runtimeEnv struct {
	brokers                     []string
	groups                      map[string]string
	urls                        map[string]string
	knobs                       usecases.Knobs
	rateLimitRPS                float64
	submissionPoll, expirySweep time.Duration
}

// loadRuntime validates the environment before anything is opened or bound.
// Every error names the variable.
func loadRuntime(env func(string) string) (runtimeEnv, error) {
	if env("DATABASE_URL") == "" {
		return runtimeEnv{}, errMissingDatabaseURL
	}
	rt := runtimeEnv{brokers: splitBrokers(env("KAFKA_BROKERS")), groups: map[string]string{}, urls: map[string]string{}}
	if len(rt.brokers) == 0 {
		return runtimeEnv{}, errMissingBrokers
	}
	if err := parsePublisherMode(env("EVENT_PUBLISHER")); err != nil {
		return runtimeEnv{}, err
	}
	for _, name := range consumerGroups {
		if env(name) == "" {
			return runtimeEnv{}, fmt.Errorf("%s is required: the consumer always runs on real Kafka", name)
		}
		rt.groups[name] = env(name)
	}
	for _, name := range neighbourURLs {
		if env(name) == "" {
			return runtimeEnv{}, fmt.Errorf("%s is required: the in-organization call it names has no default", name)
		}
		rt.urls[name] = env(name)
	}
	return parseSettings(rt, env)
}

// parseSettings reads the simulation knobs and the loop intervals; a malformed
// or out-of-range value is a boot error naming the variable.
func parseSettings(rt runtimeEnv, env func(string) string) (runtimeEnv, error) {
	var err error
	if rt.knobs.TxProcessingDelay, err = parseDuration("TX_PROCESSING_DELAY", env("TX_PROCESSING_DELAY"), 0, true); err != nil {
		return runtimeEnv{}, err
	}
	if rt.knobs.TxFailureRate, err = parseRate("TX_FAILURE_RATE", env("TX_FAILURE_RATE")); err != nil {
		return runtimeEnv{}, err
	}
	if rt.knobs.PORedeliveryRate, err = parseRate("PO_REDELIVERY_RATE", env("PO_REDELIVERY_RATE")); err != nil {
		return runtimeEnv{}, err
	}
	if rt.rateLimitRPS, err = parseNonNegative("RATE_LIMIT_RPS", env("RATE_LIMIT_RPS")); err != nil {
		return runtimeEnv{}, err
	}
	if rt.submissionPoll, err = parseDuration("SUBMISSION_POLL_INTERVAL", env("SUBMISSION_POLL_INTERVAL"), defaultSubmissionPoll, false); err != nil {
		return runtimeEnv{}, err
	}
	if rt.expirySweep, err = parseDuration("EXPIRY_SWEEP_INTERVAL", env("EXPIRY_SWEEP_INTERVAL"), defaultExpirySweep, false); err != nil {
		return runtimeEnv{}, err
	}
	return rt, nil
}

// parseDuration reads a Go duration. Unset gives def; zero is allowed only when
// allowZero (TX_PROCESSING_DELAY); negative is always refused.
func parseDuration(name, raw string, def time.Duration, allowZero bool) (time.Duration, error) {
	if raw == "" {
		return def, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d < 0 || (d == 0 && !allowZero) {
		return 0, fmt.Errorf("%s: %q is not a valid duration (want a positive Go duration such as 500ms)", name, raw)
	}
	return d, nil
}

// parseRate reads a probability in [0, 1].
func parseRate(name, raw string) (float64, error) {
	if raw == "" {
		return 0, nil
	}
	f, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if err != nil || f < 0 || f > 1 {
		return 0, fmt.Errorf("%s: %q is not a number between 0 and 1", name, raw)
	}
	return f, nil
}

// parseNonNegative reads a number >= 0.
func parseNonNegative(name, raw string) (float64, error) {
	if raw == "" {
		return 0, nil
	}
	f, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if err != nil || f < 0 {
		return 0, fmt.Errorf("%s: %q is not a number >= 0", name, raw)
	}
	return f, nil
}

// splitBrokers splits KAFKA_BROKERS on commas, trimming blanks.
func splitBrokers(raw string) []string {
	var out []string
	for _, b := range strings.Split(raw, ",") {
		if b = strings.TrimSpace(b); b != "" {
			out = append(out, b)
		}
	}
	return out
}

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

// worker is a started background loop and the channel closed once it returned.
type worker struct {
	name string
	done chan struct{}
}

// parsePublisherMode validates EVENT_PUBLISHER: unset or "kafka". The log sink
// does not exist; the variable stays because deployment manifests set it.
func parsePublisherMode(raw string) error {
	switch raw {
	case "", "kafka":
		return nil
	case "log":
		return errors.New("EVENT_PUBLISHER=log is not supported: this context publishes to real Kafka")
	default:
		return fmt.Errorf("EVENT_PUBLISHER: unknown value %q (want kafka)", raw)
	}
}

// startBackground starts the three consumers and the loops. They are always
// on; the readers dial lazily inside Run, so a broker outage never blocks boot.
func startBackground(ad adapters, rt runtimeEnv, svc *usecases.Service, logger *slog.Logger) ([]*worker, func()) {
	ctx, stop := context.WithCancel(context.Background())
	var (
		workers []*worker
		closers []func() error
	)
	consumers := []*inboundkafka.Consumer{
		inboundkafka.NewNodeConsumer(rt.brokers, rt.groups["NODE_CONSUMER_GROUP"], svc, logger),
		inboundkafka.NewListingConsumer(rt.brokers, rt.groups["LISTING_CONSUMER_GROUP"], svc, logger),
		inboundkafka.NewSourcingConsumer(rt.brokers, rt.groups["SOURCING_CONSUMER_GROUP"], svc, logger),
		inboundkafka.NewInboundConsumer(rt.brokers, rt.groups["INBOUND_CONSUMER_GROUP"], svc, logger),
		inboundkafka.NewPackagingConsumer(rt.brokers, rt.groups["PACKAGING_CONSUMER_GROUP"], svc, logger),
	}
	for _, c := range consumers {
		w := &worker{name: c.Name, done: make(chan struct{})}
		workers = append(workers, w)
		closers = append(closers, c.Close)
		go func() {
			defer close(w.done)
			logger.Info("consumer running", "consumer", c.Name, "topic", c.Topic, "brokers", rt.brokers)
			if err := c.Run(ctx); !errors.Is(err, context.Canceled) {
				logger.Error("consumer stopped", "consumer", c.Name, "error", err)
			}
		}()
	}
	loops := []struct {
		name     string
		interval time.Duration
		fn       func(context.Context) (int, error)
	}{
		{"submission processor", rt.submissionPoll, svc.ProcessDueSubmissions},
		{"expiry sweeper", rt.expirySweep, svc.ExpireUnacknowledged},
		{"retention sweeper", retentionSweep, func(ctx context.Context) (int, error) { return retention(ctx, svc, ad.pool) }},
	}
	for _, l := range loops {
		w := &worker{name: l.name, done: make(chan struct{})}
		workers = append(workers, w)
		go func() {
			defer close(w.done)
			runLoop(ctx, logger, l.name, l.interval, l.fn)
		}()
	}
	return workers, func() {
		stop()
		for _, closeFn := range closers {
			_ = closeFn()
		}
	}
}

// retention purges processed submissions (A22) and expired Idempotency-Key rows.
func retention(ctx context.Context, svc *usecases.Service, pool *pgxpool.Pool) (int, error) {
	n, err := svc.PurgeSubmissions(ctx)
	if err != nil {
		return n, err
	}
	k, err := postgres.PurgeIdempotencyKeys(ctx, pool, time.Now().Add(-inboundhttp.IdempotencyTTL))
	return n + k, err
}

// runLoop calls fn at once and then every interval until ctx ends. A failed
// pass is logged and retried at the next tick: the loops are idempotent.
func runLoop(ctx context.Context, logger *slog.Logger, name string, interval time.Duration, fn func(context.Context) (int, error)) {
	logger.Info("loop running", "loop", name, "interval", interval.String())
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		switch n, err := fn(ctx); {
		case err != nil && ctx.Err() == nil:
			logger.Error("loop pass failed", "loop", name, "error", err)
		case n > 0:
			logger.Info("loop pass done", "loop", name, "count", n)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// parseRelayInterval reads OUTBOX_RELAY_INTERVAL (Go duration or plain
// seconds), defaulting when unset, malformed or non-positive.
func parseRelayInterval(raw string, logger *slog.Logger) time.Duration {
	if raw == "" {
		return outboxrelay.DefaultInterval
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		if secs, serr := strconv.ParseFloat(raw, 64); serr == nil {
			d, err = time.Duration(secs*float64(time.Second)), nil
		}
	}
	if err != nil || d <= 0 {
		logger.Warn("invalid OUTBOX_RELAY_INTERVAL; using the default", "value", raw, "default", outboxrelay.DefaultInterval)
		return outboxrelay.DefaultInterval
	}
	return d
}

// outboxMaxAttempts reads OUTBOX_MAX_ATTEMPTS: how many times an outbox row may
// fail before the relay excludes it from claims. Unset = the fleet default
// (20); 0 = unlimited; negative or unparsable values log a warning and use the
// default, so a typo never silently removes the cap.
func outboxMaxAttempts(raw string, logger *slog.Logger) int {
	if raw == "" {
		return outboxrelay.DefaultMaxAttempts
	}
	n, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || n < 0 {
		logger.Warn("invalid OUTBOX_MAX_ATTEMPTS; using the default", "value", raw, "default", outboxrelay.DefaultMaxAttempts)
		return outboxrelay.DefaultMaxAttempts
	}
	return n
}

// startOutboxRelay starts the always-on relay goroutine over the Kafka sink.
// Kafka is dialled lazily on the first send, never here.
func startOutboxRelay(store outboxrelay.Store, brokers []string, logger *slog.Logger) (*worker, func(), error) {
	if len(brokers) == 0 {
		return nil, nil, errMissingBrokers
	}
	kafkaSink := outboundkafka.NewRelaySink(brokers)
	closeSink := func() {
		if err := kafkaSink.Close(); err != nil {
			logger.Error("kafka relay sink close failed", "error", err)
		}
	}
	interval := parseRelayInterval(os.Getenv("OUTBOX_RELAY_INTERVAL"), logger)
	maxAttempts := outboxMaxAttempts(os.Getenv("OUTBOX_MAX_ATTEMPTS"), logger)
	relay := outboxrelay.NewRelay(store, kafkaSink, logger, outboxrelay.WithInterval(interval), outboxrelay.WithMaxAttempts(maxAttempts))

	ctx, stop := context.WithCancel(context.Background())
	w := &worker{name: "outbox-relay", done: make(chan struct{})}
	go func() {
		defer close(w.done)
		defer closeSink()
		logger.Info("outbox relay running", "publisher", "kafka", "interval", interval, "max_attempts", maxAttempts, "topic", outboundkafka.TopicEvents)
		if err := relay.Run(ctx); !errors.Is(err, context.Canceled) {
			logger.Error("outbox relay stopped", "error", err)
		}
	}()
	return w, stop, nil
}

// serveUntilSignal runs httpServer until SIGINT/SIGTERM (or a listen error),
// then shuts down in the fleet order: (1) /readyz flips to 503, (2) wait the
// drain delay, (3) drain HTTP, (4) stop and await the consumers and loops,
// (5) stop and await the relay LAST so nothing committed above is stranded; the
// caller's deferred pool.Close runs after all of it.
func serveUntilSignal(ctx context.Context, logger *slog.Logger, httpServer *http.Server, readiness *inboundhttp.Readiness,
	background []*worker, stopBackground func(), relay *worker, closeRelay func(),
) error {
	errCh := make(chan error, 1)
	go func() {
		logger.Info("http server listening", "addr", httpServer.Addr)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	sigCtx, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	died := watchWorkers(append([]*worker{relay}, background...)...)
	select {
	case err := <-errCh:
		stopBackground()
		closeRelay()
		return err
	case name := <-died:
		// A consumer, a loop or the relay returned on its own: the service
		// cannot run without it and must not sit "alive" with a dead worker,
		// so exit non-zero and let the orchestrator restart it.
		stopBackground()
		closeRelay()
		return fmt.Errorf("%s stopped unexpectedly: exiting so the orchestrator restarts the service", name)
	case <-sigCtx.Done():
	}

	readiness.SetNotReady()
	if delay := shutdownDrainDelay(logger); delay > 0 {
		logger.Info("shutdown: readiness flipped to not-ready; waiting for traffic to drain", "drain_delay", delay.String())
		time.Sleep(delay)
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	err := httpServer.Shutdown(shutdownCtx)

	stopBackground()
	for _, w := range background {
		await(logger, w)
	}
	closeRelay()
	await(logger, relay)
	return err
}

// watchWorkers returns a channel that receives the name of every worker that
// returns. Workers only return when stopped or on an unrecoverable failure, so
// a value read while the service is serving means a worker died.
func watchWorkers(workers ...*worker) <-chan string {
	died := make(chan string, len(workers))
	for _, w := range workers {
		if w == nil {
			continue
		}
		go func() {
			<-w.done
			died <- w.name
		}()
	}
	return died
}

// await waits (bounded) for w to return; nil w is a no-op.
func await(logger *slog.Logger, w *worker) {
	if w == nil {
		return
	}
	select {
	case <-w.done:
	case <-time.After(workerDrainTimeout):
		logger.Warn("worker did not stop before the shutdown drain deadline", "worker", w.name)
	}
}

// shutdownDrainDelay reads SHUTDOWN_DRAIN_DELAY ("0" disables; unset, negative
// or unparsable = the default).
func shutdownDrainDelay(logger *slog.Logger) time.Duration {
	raw := os.Getenv("SHUTDOWN_DRAIN_DELAY")
	if raw == "" {
		return DefaultShutdownDrainDelay
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d < 0 {
		logger.Warn("ignoring invalid SHUTDOWN_DRAIN_DELAY", "value", raw, "default", DefaultShutdownDrainDelay.String())
		return DefaultShutdownDrainDelay
	}
	return d
}

// newLogger builds the JSON logger; LOG_LEVEL debug|info|warn|warning|error
// (case-insensitive; "warning" is accepted like "warn" fleet-wide). The handler
// is wrapped so a record written with a span-carrying context (the *Context
// slog variants) also carries trace_id and span_id.
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
	return slog.New(telemetry.WithTraceContext(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: lvl})))
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
