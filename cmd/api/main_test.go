package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// validEnv is the smallest environment loadRuntime accepts.
func validEnv() map[string]string {
	return map[string]string{
		"DATABASE_URL": "postgres://u@h/db", "KAFKA_BROKERS": "b1:9092, b2:9092",
		"NODE_CONSUMER_GROUP": "g-node", "LISTING_CONSUMER_GROUP": "g-listing", "SOURCING_CONSUMER_GROUP": "g-sourcing",
		"NODE_TOPOLOGY_URL": "http://nt", "OFFER_AVAILABILITY_URL": "http://oa", "SHIPPING_AND_TRACKING_URL": "http://st", "NODE_PERFORMANCE_URL": "http://np",
	}
}

func lookup(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestLoadRuntimeAcceptsTheMinimalEnvironment(t *testing.T) {
	rt, err := loadRuntime(lookup(validEnv()))
	if err != nil {
		t.Fatal(err)
	}
	if len(rt.brokers) != 2 || rt.brokers[1] != "b2:9092" || rt.groups["NODE_CONSUMER_GROUP"] != "g-node" || rt.urls["NODE_PERFORMANCE_URL"] != "http://np" {
		t.Fatalf("rt = %+v", rt)
	}
	if rt.knobs.TxProcessingDelay != 0 || rt.knobs.TxFailureRate != 0 || rt.knobs.PORedeliveryRate != 0 || rt.rateLimitRPS != 0 ||
		rt.submissionPoll != defaultSubmissionPoll || rt.expirySweep != defaultExpirySweep {
		t.Fatalf("defaults = %+v", rt)
	}
}

func TestLoadRuntimeReadsTheKnobs(t *testing.T) {
	env := validEnv()
	env["TX_PROCESSING_DELAY"], env["TX_FAILURE_RATE"], env["PO_REDELIVERY_RATE"] = "750ms", "0.25", "1"
	env["RATE_LIMIT_RPS"], env["SUBMISSION_POLL_INTERVAL"], env["EXPIRY_SWEEP_INTERVAL"] = "12.5", "2s", "1m"
	rt, err := loadRuntime(lookup(env))
	if err != nil {
		t.Fatal(err)
	}
	if rt.knobs.TxProcessingDelay != 750*time.Millisecond || rt.knobs.TxFailureRate != 0.25 || rt.knobs.PORedeliveryRate != 1 ||
		rt.rateLimitRPS != 12.5 || rt.submissionPoll != 2*time.Second || rt.expirySweep != time.Minute {
		t.Fatalf("rt = %+v", rt)
	}
	env["TX_PROCESSING_DELAY"] = "0"
	if rt, err := loadRuntime(lookup(env)); err != nil || rt.knobs.TxProcessingDelay != 0 {
		t.Fatalf("a zero processing delay is allowed: %v", err)
	}
}

func TestLoadRuntimeRefusesWithTheVariableName(t *testing.T) {
	tests := map[string]struct {
		set  map[string]string
		want string
	}{
		"no database":     {map[string]string{"DATABASE_URL": ""}, "DATABASE_URL"},
		"no brokers":      {map[string]string{"KAFKA_BROKERS": " , "}, "KAFKA_BROKERS"},
		"publisher log":   {map[string]string{"EVENT_PUBLISHER": "log"}, "EVENT_PUBLISHER=log"},
		"publisher other": {map[string]string{"EVENT_PUBLISHER": "nats"}, "EVENT_PUBLISHER"},
		"node group":      {map[string]string{"NODE_CONSUMER_GROUP": ""}, "NODE_CONSUMER_GROUP"},
		"listing group":   {map[string]string{"LISTING_CONSUMER_GROUP": ""}, "LISTING_CONSUMER_GROUP"},
		"sourcing group":  {map[string]string{"SOURCING_CONSUMER_GROUP": ""}, "SOURCING_CONSUMER_GROUP"},
		"node topology":   {map[string]string{"NODE_TOPOLOGY_URL": ""}, "NODE_TOPOLOGY_URL"},
		"offers":          {map[string]string{"OFFER_AVAILABILITY_URL": ""}, "OFFER_AVAILABILITY_URL"},
		"shipping":        {map[string]string{"SHIPPING_AND_TRACKING_URL": ""}, "SHIPPING_AND_TRACKING_URL"},
		"performance":     {map[string]string{"NODE_PERFORMANCE_URL": ""}, "NODE_PERFORMANCE_URL"},
		"delay garbage":   {map[string]string{"TX_PROCESSING_DELAY": "soon"}, "TX_PROCESSING_DELAY"},
		"delay negative":  {map[string]string{"TX_PROCESSING_DELAY": "-1s"}, "TX_PROCESSING_DELAY"},
		"failure rate":    {map[string]string{"TX_FAILURE_RATE": "1.5"}, "TX_FAILURE_RATE"},
		"failure garbage": {map[string]string{"TX_FAILURE_RATE": "x"}, "TX_FAILURE_RATE"},
		"redelivery":      {map[string]string{"PO_REDELIVERY_RATE": "-0.1"}, "PO_REDELIVERY_RATE"},
		"rate limit":      {map[string]string{"RATE_LIMIT_RPS": "-1"}, "RATE_LIMIT_RPS"},
		"rate limit text": {map[string]string{"RATE_LIMIT_RPS": "fast"}, "RATE_LIMIT_RPS"},
		"poll zero":       {map[string]string{"SUBMISSION_POLL_INTERVAL": "0s"}, "SUBMISSION_POLL_INTERVAL"},
		"sweep garbage":   {map[string]string{"EXPIRY_SWEEP_INTERVAL": "never"}, "EXPIRY_SWEEP_INTERVAL"},
	}
	for name, tc := range tests {
		env := validEnv()
		for k, v := range tc.set {
			env[k] = v
		}
		if _, err := loadRuntime(lookup(env)); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want it to name %s", name, err, tc.want)
		}
	}
}

func TestSplitBrokers(t *testing.T) {
	if got := splitBrokers(" a:1,,b:2 ,"); len(got) != 2 || got[0] != "a:1" || got[1] != "b:2" {
		t.Fatalf("got %v", got)
	}
	if got := splitBrokers(""); len(got) != 0 {
		t.Fatalf("got %v", got)
	}
}

func TestNewReadinessNamesTheFailingDependency(t *testing.T) {
	g := newReadiness(func(context.Context) error { return errors.New("down") }, []string{"127.0.0.1:1"})
	failing := g.Failing(context.Background())
	if len(failing) != 2 || failing[0] != "postgres" || failing[1] != "kafka" {
		t.Fatalf("failing = %v", failing)
	}
}

func TestRelayAndShutdownSettings(t *testing.T) {
	log := quiet()
	for in, want := range map[string]time.Duration{"": time.Second, "250ms": 250 * time.Millisecond, "2": 2 * time.Second, "x": time.Second, "-1s": time.Second, "0": time.Second} {
		if got := parseRelayInterval(in, log); got != want {
			t.Errorf("parseRelayInterval(%q) = %v, want %v", in, got, want)
		}
	}
	for in, want := range map[string]int{"": 20, "5": 5, "0": 0, "-1": 20, "x": 20} {
		if got := outboxMaxAttempts(in, log); got != want {
			t.Errorf("outboxMaxAttempts(%q) = %d, want %d", in, got, want)
		}
	}
	for in, want := range map[string]time.Duration{"": DefaultShutdownDrainDelay, "0": 0, "1s": time.Second, "-1s": DefaultShutdownDrainDelay, "x": DefaultShutdownDrainDelay} {
		t.Setenv("SHUTDOWN_DRAIN_DELAY", in)
		if got := shutdownDrainDelay(log); got != want {
			t.Errorf("shutdownDrainDelay(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestPublisherModeAndLogger(t *testing.T) {
	if parsePublisherMode("") != nil || parsePublisherMode("kafka") != nil {
		t.Fatal("kafka is the default")
	}
	for in, want := range map[string]slog.Level{"debug": slog.LevelDebug, "WARN": slog.LevelWarn, "warning": slog.LevelWarn, "error": slog.LevelError, "": slog.LevelInfo, "loud": slog.LevelInfo} {
		l := newLogger(in)
		if !l.Enabled(context.Background(), want) || (want > slog.LevelDebug && l.Enabled(context.Background(), want-1)) {
			t.Errorf("newLogger(%q) has the wrong level", in)
		}
	}
	t.Setenv("X_SET", "v")
	if getenv("X_SET", "d") != "v" || getenv("X_UNSET_FOR_SURE", "d") != "d" {
		t.Fatal("getenv")
	}
}

func TestRunLoopCallsAtOnceThenEveryIntervalUntilCancelled(t *testing.T) {
	var calls atomic.Int32
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		runLoop(ctx, quiet(), "test", 5*time.Millisecond, func(context.Context) (int, error) {
			if calls.Add(1) == 1 {
				return 0, errors.New("first pass fails")
			}
			return 2, nil
		})
	}()
	deadline := time.After(5 * time.Second)
	for calls.Load() < 3 {
		select {
		case <-deadline:
			t.Fatal("the loop did not keep ticking after a failed pass")
		case <-time.After(time.Millisecond):
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the loop did not stop")
	}
}

func TestWatchWorkersAndAwait(t *testing.T) {
	a, b := &worker{name: "a", done: make(chan struct{})}, &worker{name: "b", done: make(chan struct{})}
	died := watchWorkers(a, nil, b)
	close(b.done)
	if name := <-died; name != "b" {
		t.Fatalf("died = %s", name)
	}
	await(quiet(), nil)
	await(quiet(), b)
	close(a.done)
	await(quiet(), a)
}

func TestStartOutboxRelayNeedsBrokers(t *testing.T) {
	if _, _, err := startOutboxRelay(nil, nil, quiet()); !errors.Is(err, errMissingBrokers) {
		t.Fatalf("err = %v", err)
	}
	if _, _, err := buildAdapters(context.Background(), "", quiet()); !errors.Is(err, errMissingDatabaseURL) {
		t.Fatalf("err = %v", err)
	}
}
