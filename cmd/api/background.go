package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	inboundhttp "github.com/claudioed/vendor-connectivity/internal/adapters/inbound/http"
	inboundkafka "github.com/claudioed/vendor-connectivity/internal/adapters/inbound/kafka"
	outboundkafka "github.com/claudioed/vendor-connectivity/internal/adapters/outbound/kafka"
	outboxrelay "github.com/claudioed/vendor-connectivity/internal/adapters/outbound/outbox"
	"github.com/claudioed/vendor-connectivity/internal/adapters/outbound/postgres"
	"github.com/claudioed/vendor-connectivity/internal/application/usecases"
)

// worker is a started background loop and the channel closed once it returned.
type worker struct {
	name string
	done chan struct{}
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
