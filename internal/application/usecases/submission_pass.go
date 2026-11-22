package usecases

import (
	"context"
	"fmt"

	"github.com/claudioed/vendor-connectivity/internal/domain/submission"
)

// passEnd is what a pass without an outcome came to: still pending, or the attempt
// cap was reached and res is the terminal failure.
type passEnd struct {
	reached bool
	res     result
}

// passWithoutOutcome counts the pass (an outage or a released refusal), saves it and,
// at the attempt cap, returns the failure that ends the submission: ErrUpstreamUnavailable
// after an outage, the neighbour's own refusal when that is what kept it pending (the
// terminal state a refusal always had, with the same code).
func (s *Service) passWithoutOutcome(ctx context.Context, sub *submission.Submission, res result) (passEnd, error) {
	var n int
	var err error
	if res.refusal != nil {
		n, err = sub.RecordRefusal(*res.refusal)
		s.cfg.Logger.InfoContext(ctx, "neighbour released a refusal; the submission stays pending", "transactionId", sanitizeLog(string(sub.ID())), "kind", sanitizeLog(string(sub.Kind())), "code", sanitizeLog(res.refusal.Code), "attempt", n)
	} else {
		n, err = sub.RecordAttempt()
	}
	if err != nil {
		return passEnd{}, err
	}
	if err := s.d.Submissions.Save(ctx, sub); err != nil {
		return passEnd{}, err
	}
	if n < s.cfg.SubmissionMaxAttempts {
		return passEnd{}, nil
	}
	if last, ok := sub.LastRefusal(); ok && res.refusal != nil {
		return passEnd{reached: true, res: failure(last.Code, last.Message, last.Path)}, nil
	}
	return passEnd{reached: true, res: failure(CodeUpstreamUnavail, fmt.Sprintf("the neighbour did not answer in %d attempts", n), "")}, nil
}
