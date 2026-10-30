package telemetry

import (
	"context"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/claudioed/vendor-connectivity/internal/application/ports"
)

// meterName scopes this service's own instruments, keeping them distinct from
// the ones otelchi and the runtime collector register.
const meterName = "github.com/claudioed/vendor-connectivity"

// submissionsProcessedName is the Tier 2 business metric of the context (ADR
// 0008, the fleet's standard metrics convention): every vendor transaction
// that reached an outcome, by outcome.
const submissionsProcessedName = "vendor.submissions.processed"

const (
	outcomeKey = attribute.Key("outcome")
	reasonKey  = attribute.Key("reason")

	outcomeSucceeded = "succeeded"
	outcomeFailed    = "failed"
)

// SubmissionMetrics implements ports.SubmissionMetrics against the global
// MeterProvider. Until Setup installs a real provider the global one is a
// no-op, so recording is cheap and safe in tests and local runs.
type SubmissionMetrics struct {
	counter metric.Int64Counter
}

var _ ports.SubmissionMetrics = (*SubmissionMetrics)(nil)

// NewSubmissionMetrics registers the counter. It only fails if the instrument
// name is invalid, which is a programming error, not a runtime condition. Call
// it after Setup so the instrument binds to the real MeterProvider.
func NewSubmissionMetrics() (*SubmissionMetrics, error) {
	counter, err := otel.Meter(meterName).Int64Counter(
		submissionsProcessedName,
		metric.WithDescription("Vendor transactions (acknowledgements, shipment confirmations, inventory feeds, node registrations and profiles, "+
			"inbound receipts) that reached an outcome, by outcome (succeeded or failed; reason is the Err code the vendor reads, "+
			"such as ErrUpstreamUnavailable or ErrSimulatedFailure). A rising failed share means vendors are being told their "+
			"writes did not apply: ErrUpstreamUnavailable means a neighbour context stopped answering, not that the vendors sent bad data."),
		metric.WithUnit("{submission}"),
	)
	if err != nil {
		return nil, err
	}
	return &SubmissionMetrics{counter: counter}, nil
}

// SubmissionSucceeded counts a submission whose transaction ended in SUCCESS.
func (m *SubmissionMetrics) SubmissionSucceeded(ctx context.Context) {
	m.counter.Add(ctx, 1, metric.WithAttributes(outcomeKey.String(outcomeSucceeded)))
}

// SubmissionFailed counts a submission whose transaction ended in FAILURE, with
// the Err code that says why.
func (m *SubmissionMetrics) SubmissionFailed(ctx context.Context, reason string) {
	m.counter.Add(ctx, 1, metric.WithAttributes(outcomeKey.String(outcomeFailed), reasonKey.String(reason)))
}
