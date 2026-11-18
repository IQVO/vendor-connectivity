package usecases

import (
	"context"

	"github.com/claudioed/vendor-connectivity/internal/domain/submission"
)

// ExecuteKind exposes the kind dispatch of execute to the black-box tests of
// this package: the outcome it produced and whether the submission stays
// PROCESSING. The kind is passed apart from the submission because the domain
// builds no submission of a kind it does not declare, so the dispatch of an
// unknown kind can only be exercised this way.
func (s *Service) ExecuteKind(ctx context.Context, kind submission.Kind, sub *submission.Submission) (submission.Outcome, bool) {
	res := s.executeKind(ctx, kind, sub)
	return res.outcome, res.pending
}
