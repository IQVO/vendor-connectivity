package usecases

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"time"

	"github.com/claudioed/vendor-connectivity/internal/application/ports"
	"github.com/claudioed/vendor-connectivity/internal/application/repository"
	"github.com/claudioed/vendor-connectivity/internal/application/requests"
	"github.com/claudioed/vendor-connectivity/internal/application/views"
	"github.com/claudioed/vendor-connectivity/internal/domain/network"
	"github.com/claudioed/vendor-connectivity/internal/domain/returns"
	"github.com/claudioed/vendor-connectivity/internal/domain/shared"
	"github.com/claudioed/vendor-connectivity/internal/domain/submission"
)

// adviceToken is the opaque continuation of the return advice poll: the keyset
// position and the node filter it was issued for.
type adviceToken struct {
	ReadyAt         time.Time `json:"t"`
	AuthorizationID string    `json:"a"`
	VendorID        string    `json:"v"`
	NodeID          string    `json:"n,omitempty"`
}

func encodeAdviceToken(t adviceToken) string {
	b, _ := json.Marshal(t) //nolint:errchkjson // a struct of strings and a time always marshals
	return base64.RawURLEncoding.EncodeToString(b)
}

func decodeAdviceToken(s string) (adviceToken, error) {
	b, err := base64.RawURLEncoding.DecodeString(s)
	var t adviceToken
	if err != nil || json.Unmarshal(b, &t) != nil || t.AuthorizationID == "" || t.ReadyAt.IsZero() {
		return adviceToken{}, ports.ErrInvalidCursor
	}
	return t, nil
}

// PollReturnAdvices is query PollReturnAdvices (GET /vendor/return-advices):
// the AUTHORIZED advices of the vendor's nodes, oldest readyAt first, paged
// with nextToken. An advice leaves the poll when its outcome is recorded or
// the authorization is cancelled (ADR 0014).
//
//nolint:dupl // the deliberate mirror of PollInboundShipments: ADR 0014 reuses the ADR 0005 seam shape for shape
func (s *Service) PollReturnAdvices(ctx context.Context, q requests.ReturnPollQuery) (views.ReturnAdvicePage, error) {
	page := views.ReturnAdvicePage{ReturnAdvices: []views.ReturnAdvice{}}
	if !shared.ValidID(q.VendorID) {
		return page, shared.Invalid("vendorId is required")
	}
	if err := s.requireVendor(ctx, q.VendorID); err != nil {
		return page, err
	}
	tok := adviceToken{VendorID: q.VendorID, NodeID: q.NodeID}
	var after *repository.AdviceCursor
	if q.NextToken != "" {
		t, err := decodeAdviceToken(q.NextToken)
		if err != nil {
			return page, err
		}
		if t.VendorID != q.VendorID {
			return page, ports.ErrInvalidCursor
		}
		tok.NodeID = t.NodeID
		after = &repository.AdviceCursor{ReadyAt: t.ReadyAt, AuthorizationID: t.AuthorizationID}
	}
	nodeIDs, err := s.adviceNodes(ctx, q.VendorID, tok.NodeID)
	if err != nil || len(nodeIDs) == 0 {
		return page, err
	}
	limit := s.pageLimit(q.Limit)
	f := repository.ReturnAdviceFilter{NodeIDs: nodeIDs, After: after, Limit: limit + 1}
	if after == nil {
		f.ReadyAfter = q.ReadyAfter
	}
	rows, err := s.d.Advices.ListReady(ctx, f)
	if err != nil {
		return page, err
	}
	if len(rows) > limit {
		rows = rows[:limit]
		last := rows[limit-1]
		page.NextToken = encodeAdviceToken(adviceToken{ReadyAt: last.ReadyAt, AuthorizationID: last.AuthorizationID, VendorID: q.VendorID, NodeID: tok.NodeID})
	}
	for _, r := range rows {
		page.ReturnAdvices = append(page.ReturnAdvices, adviceView(r))
	}
	return page, nil
}

// adviceNodes returns the node ids the advice poll may read: the named node
// after checking the vendor owns it, else every node of the vendor.
func (s *Service) adviceNodes(ctx context.Context, vendorID, nodeID string) ([]string, error) {
	if nodeID != "" {
		if _, err := s.ownedNode(ctx, vendorID, nodeID); err != nil {
			return nil, err
		}
		return []string{nodeID}, nil
	}
	nodes, err := s.d.Nodes.ListByVendor(ctx, vendorID)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(nodes))
	for _, n := range nodes {
		ids = append(ids, n.ID)
	}
	return ids, nil
}

// GetReturnAdvice is query GetReturnAdvice (GET /vendor/return-advices/{authorizationId}).
// An advice of another vendor's node, or unknown, is not found. Unlike the
// poll, this read also answers for a closed advice (state CANCELLED or
// OUTCOME_RECORDED).
func (s *Service) GetReturnAdvice(ctx context.Context, vendorID, authorizationID string) (views.ReturnAdvice, error) {
	if !shared.ValidID(vendorID) {
		return views.ReturnAdvice{}, shared.Invalid("vendorId is required")
	}
	if err := s.requireVendor(ctx, vendorID); err != nil {
		return views.ReturnAdvice{}, err
	}
	a, err := s.d.Advices.Get(ctx, authorizationID)
	if err != nil {
		return views.ReturnAdvice{}, err
	}
	if _, err := s.ownedNode(ctx, vendorID, a.NodeID); err != nil {
		if errors.Is(err, network.ErrNodeNotOwnedByVendor) || errors.Is(err, ports.ErrNotFound) {
			return views.ReturnAdvice{}, ports.ErrNotFound
		}
		return views.ReturnAdvice{}, err
	}
	return adviceView(a), nil
}

func adviceView(a returns.Advice) views.ReturnAdvice {
	v := views.ReturnAdvice{AuthorizationID: a.AuthorizationID, OrderID: a.OrderID, GroupNo: a.GroupNo, VendorID: a.VendorID,
		NodeID: a.NodeID, Disposition: a.Disposition, Method: a.Method, State: string(a.State), ReadyAt: a.ReadyAt,
		Lines: make([]views.ReturnAdviceLine, 0, len(a.Lines)), TrackingNumbers: a.TrackingNumbers}
	if !a.ExpiresAt.IsZero() {
		at := a.ExpiresAt
		v.ExpiresAt = &at
	}
	for _, l := range a.Lines {
		v.Lines = append(v.Lines, views.ReturnAdviceLine{LineNo: l.LineNo, CatalogItemID: l.CatalogItemID, Quantity: l.Quantity, OwnerRef: l.OwnerRef})
	}
	if v.TrackingNumbers == nil {
		v.TrackingNumbers = []string{}
	}
	return v
}

// SubmitReturnOutcome receives a RETURN_OUTCOME (POST /vendor/return-outcomes):
// the outcome is judged against ReturnAdviceView, a PROCESSING submission is
// received, the advice moves to OUTCOME_RECORDED (it leaves the poll) and
// VendorReturnOutcomeSubmitted is appended to the outbox, all in one unit.
func (s *Service) SubmitReturnOutcome(ctx context.Context, in requests.ReturnOutcome) (views.TransactionAccepted, error) {
	if err := in.Validate(); err != nil {
		return views.TransactionAccepted{}, err
	}
	o := returns.Outcome{AuthorizationID: in.AuthorizationID, VendorID: in.VendorID, NodeID: in.NodeID, ReceivedAt: in.ReceivedAt,
		Lines: make([]returns.OutcomeLine, 0, len(in.Lines))}
	for _, l := range in.Lines {
		o.Lines = append(o.Lines, returns.OutcomeLine{CatalogItemID: l.CatalogItemID, OwnerRef: l.OwnerRef,
			QtyRestockable: l.QtyRestockable, QtyUnsellable: l.QtyUnsellable})
	}
	oc, err := o.Normalise()
	if err != nil {
		return views.TransactionAccepted{}, err
	}
	var txID string
	err = s.atomically(ctx, func(ctx context.Context) error {
		a, err := s.d.Advices.Get(ctx, oc.AuthorizationID)
		if err != nil {
			return err
		}
		if a.VendorID != oc.VendorID {
			// Another vendor's advice is not this vendor's business (G32
			// consistency, not security): not-found, like the reads.
			return ports.ErrNotFound
		}
		if err := a.CheckOutcome(oc); err != nil {
			return err
		}
		if err := s.requireActive(ctx, oc.VendorID); err != nil {
			return err
		}
		closed, err := a.RecordOutcome()
		if err != nil {
			return err
		}
		if err := s.d.Advices.Save(ctx, closed); err != nil {
			return err
		}
		txID = s.d.IDs.NewTransactionID()
		sub, err := s.newSubmission(txID, oc.VendorID, submission.KindReturnOutcome, outcomeBody(oc), "", oc.NodeID)
		if err != nil {
			return err
		}
		if err := s.d.Submissions.Save(ctx, sub); err != nil {
			return err
		}
		return s.d.Outbox.Append(ctx, returns.NewOutcomeSubmitted(txID, oc.VendorID, oc, s.d.Clock.Now()))
	})
	return views.TransactionAccepted{TransactionID: txID}, err
}

// outcomeBody is the normalised outcome kept on the submission.
func outcomeBody(oc returns.Outcome) requests.ReturnOutcome {
	out := requests.ReturnOutcome{VendorID: oc.VendorID, AuthorizationID: oc.AuthorizationID, NodeID: oc.NodeID, ReceivedAt: oc.ReceivedAt,
		Lines: make([]requests.ReturnOutcomeLine, 0, len(oc.Lines))}
	for _, l := range oc.Lines {
		out.Lines = append(out.Lines, requests.ReturnOutcomeLine{CatalogItemID: l.CatalogItemID, OwnerRef: l.OwnerRef,
			QtyRestockable: l.QtyRestockable, QtyUnsellable: l.QtyUnsellable})
	}
	return out
}

// --- consumer of customer-returns events ----------------------------------

// OnReturnAuthorized feeds ReturnAdviceView from ReturnAuthorized. The advice's
// node is the event's destination, else its origin node (the ORIGIN_NODE
// disposition ships there); vendorId is that node's operator vendor from
// VendorNodeView, never the event (which does not carry it). An advice whose
// destination the view does not know, or whose node has no operator vendor, is
// counted and skipped (WARN): node-topology facts precede any authorization,
// and a mis-attributed advice would hand one vendor another's work (ADR 0014).
func (s *Service) OnReturnAuthorized(ctx context.Context, eventID, eventType string, in returns.Authorized) error {
	dest := in.NodeID // the consumer resolved destination || origin into NodeID
	if dest == "" {
		s.cfg.Logger.WarnContext(ctx, "ReturnAuthorized carries no destination node; ignored", "eventId", eventID, "type", eventType)
		return nil
	}
	n, err := s.d.Nodes.Get(ctx, dest)
	if errors.Is(err, ports.ErrNotFound) || (err == nil && n.VendorID == "") {
		s.cfg.Logger.WarnContext(ctx, "ReturnAuthorized names a node this context cannot attribute to a vendor; ignored",
			"eventId", eventID, "type", eventType, "nodeId", dest, "error", err)
		return nil
	}
	if err != nil {
		return err
	}
	in.VendorID = n.VendorID
	return s.updateAdvice(ctx, eventID, eventType, in.AuthorizationID, func(cur returns.Advice) (returns.Advice, error) {
		return cur.OnAuthorized(in)
	})
}

// OnReturnLabelIssued feeds ReturnAdviceView from ReturnLabelIssued: the
// tracking number joins the advice (idempotent).
func (s *Service) OnReturnLabelIssued(ctx context.Context, eventID, eventType string, authorizationID, trackingNumber string) error {
	return s.updateAdvice(ctx, eventID, eventType, authorizationID, func(cur returns.Advice) (returns.Advice, error) {
		return cur.OnLabelIssued(trackingNumber)
	})
}

// OnReturnCancelled feeds ReturnAdviceView from ReturnCancelled: the advice
// goes CANCELLED unless an outcome was already recorded.
func (s *Service) OnReturnCancelled(ctx context.Context, eventID, eventType string, authorizationID string) error {
	return s.updateAdvice(ctx, eventID, eventType, authorizationID, func(cur returns.Advice) (returns.Advice, error) {
		return cur.OnCancelled()
	})
}

func (s *Service) updateAdvice(ctx context.Context, eventID, eventType, authorizationID string, apply func(returns.Advice) (returns.Advice, error)) error {
	return s.consume(ctx, eventID, eventType, func(ctx context.Context) error {
		cur, err := s.d.Advices.Get(ctx, authorizationID)
		if err != nil && !errors.Is(err, ports.ErrNotFound) {
			return err
		}
		next, err := apply(cur)
		if errors.Is(err, shared.ErrValidation) {
			s.cfg.Logger.WarnContext(ctx, "return advice event carries invalid facts; ignored", "eventId", eventID, "type", eventType, "error", err)
			return nil
		}
		if err != nil {
			return err
		}
		return s.d.Advices.Save(ctx, next)
	})
}
