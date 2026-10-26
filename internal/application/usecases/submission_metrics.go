package usecases

import (
	"context"

	"github.com/claudioed/vendor-connectivity/internal/application/ports"
	"github.com/claudioed/vendor-connectivity/internal/domain/submission"
)

// unknownFailureReason is the reason of a FAILURE outcome that carries no Err
// code (the use case always sets one; this keeps the attribute non-empty).
const unknownFailureReason = "unknown"

// metrics returns the configured SubmissionMetrics or a no-op.
func (d Deps) metrics() ports.SubmissionMetrics {
	if d.Metrics != nil {
		return d.Metrics
	}
	return noMetrics{}
}

// noMetrics is the SubmissionMetrics of a Deps without one.
type noMetrics struct{}

func (noMetrics) SubmissionSucceeded(context.Context)      {}
func (noMetrics) SubmissionFailed(context.Context, string) {}

// recordSubmissionOutcome counts one submission that reached an outcome:
// succeeded when the transaction ended in SUCCESS, else failed with the first
// Err code of the outcome as the reason.
func (s *Service) recordSubmissionOutcome(ctx context.Context, o submission.Outcome) {
	if o.Status == submission.Success {
		s.d.metrics().SubmissionSucceeded(ctx)
		return
	}
	reason := unknownFailureReason
	if len(o.Errors) > 0 && o.Errors[0].Code != "" {
		reason = o.Errors[0].Code
	}
	s.d.metrics().SubmissionFailed(ctx, reason)
}
