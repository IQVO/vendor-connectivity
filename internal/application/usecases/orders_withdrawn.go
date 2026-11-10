package usecases

import (
	"context"
	"errors"
	"strings"

	"github.com/claudioed/vendor-connectivity/internal/application/ports"
	"github.com/claudioed/vendor-connectivity/internal/application/requests"
	"github.com/claudioed/vendor-connectivity/internal/domain/purchaseorder"
	"github.com/claudioed/vendor-connectivity/internal/domain/shared"
)

// CancelOnReleaseWithdrawn is policy CancelOnReleaseWithdrawn (ADR 0013): on
// FulfillmentReleaseWithdrawn it cancels the ISSUED order of exactly the
// withdrawn release (assignmentId + releaseNo, never "the latest": the carrier
// is released again under a newer number). An unknown release is a no-op; an
// order past ISSUED stays as it is and the refusal is logged, like
// CancelOnSourcingCancel (A15).
func (s *Service) CancelOnReleaseWithdrawn(ctx context.Context, eventID, eventType string, in requests.ReleaseWithdrawn) error {
	return s.consume(ctx, eventID, eventType, func(ctx context.Context) error {
		p, err := s.d.Orders.FindByRelease(ctx, in.AssignmentID, in.ReleaseNo)
		if errors.Is(err, ports.ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		evs, err := p.Cancel(withdrawnReason(in.Reason), s.d.Clock.Now())
		if errors.Is(err, purchaseorder.ErrCancelAfterAcknowledgement) || errors.Is(err, shared.ErrIllegalTransition) {
			s.cfg.Logger.InfoContext(ctx, "purchase order not cancelled", "poNumber", p.Number(), "state", p.State(), "reason", err.Error())
			return nil
		}
		if err != nil {
			return err
		}
		return s.saveOrder(ctx, p, evs)
	})
}

// withdrawnReason is "release withdrawn: <reason>" (bare prefix when the
// producer sent no reason), sanitised and capped like every cancel reason.
func withdrawnReason(reason string) string {
	reason = strings.TrimSpace(strings.Map(func(c rune) rune {
		if c < 0x20 || c == 0x7f {
			return ' '
		}
		return c
	}, reason))
	if reason == "" {
		return "release withdrawn"
	}
	return cancelReason("release withdrawn: " + reason)
}
