package main

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"

	outboxrelay "github.com/claudioed/vendor-connectivity/internal/adapters/outbound/outbox"
	"github.com/claudioed/vendor-connectivity/internal/application/usecases"
)

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
