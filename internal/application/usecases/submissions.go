package usecases

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/claudioed/vendor-connectivity/internal/application/gateway"
	"github.com/claudioed/vendor-connectivity/internal/application/ports"
	"github.com/claudioed/vendor-connectivity/internal/application/requests"
	"github.com/claudioed/vendor-connectivity/internal/application/views"
	"github.com/claudioed/vendor-connectivity/internal/domain/network"
	"github.com/claudioed/vendor-connectivity/internal/domain/purchaseorder"
	"github.com/claudioed/vendor-connectivity/internal/domain/shared"
	"github.com/claudioed/vendor-connectivity/internal/domain/submission"
)

// Err codes this context itself writes into a failed transaction.
const (
	CodeSimulatedFailure   = "ErrSimulatedFailure"
	CodeUpstreamUnavail    = "ErrUpstreamUnavailable"
	CodeMalformedPayload   = "ErrMalformedPayload"
	codeFeedAlreadyApplied = "ErrFeedAlreadyApplied"
)

func (s *Service) newSubmission(txID, vendorID string, kind submission.Kind, body any, poNumber, nodeID string) (*submission.Submission, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal submission payload: %w", err)
	}
	return submission.Receive(submission.NewSubmission{ID: submission.ID(txID), VendorID: vendorID, Kind: kind, Payload: payload, PONumber: poNumber, NodeID: nodeID},
		s.d.Clock.Now(), s.cfg.Knobs.TxProcessingDelay)
}

// receiveOnly receives a submission that changes no purchase order.
func (s *Service) receiveOnly(ctx context.Context, vendorID string, kind submission.Kind, body any, nodeID string, check func(ctx context.Context) error) (views.TransactionAccepted, error) {
	var txID string
	err := s.atomically(ctx, func(ctx context.Context) error {
		if err := s.requireActive(ctx, vendorID); err != nil {
			return err
		}
		if err := check(ctx); err != nil {
			return err
		}
		txID = s.d.IDs.NewTransactionID()
		sub, err := s.newSubmission(txID, vendorID, kind, body, "", nodeID)
		if err != nil {
			return err
		}
		return s.d.Submissions.Save(ctx, sub)
	})
	return views.TransactionAccepted{TransactionID: txID}, err
}

// ownedNode returns the node of the view after checking the vendor owns it.
func (s *Service) ownedNode(ctx context.Context, vendorID, nodeID string) (network.Node, error) {
	n, err := s.d.Nodes.Get(ctx, nodeID)
	if err != nil {
		return network.Node{}, err
	}
	return n, n.EnsureOwnedBy(vendorID)
}

// RegisterVendorNode receives a NODE_REGISTRATION (POST /vendor/nodes).
func (s *Service) RegisterVendorNode(ctx context.Context, in requests.NodeRegistration) (views.TransactionAccepted, error) {
	if err := in.Validate(); err != nil {
		return views.TransactionAccepted{}, err
	}
	if !network.VendorMayRegister(in.NodeType) {
		return views.TransactionAccepted{}, shared.Invalid("a vendor may register only FULFILLMENT_CENTER, INBOUND_CROSS_DOCK or RETURNS_CENTER nodes")
	}
	return s.receiveOnly(ctx, in.VendorID, submission.KindNodeRegistration, in, "", func(context.Context) error { return nil })
}

// DeclareNodeProfile receives a NODE_PROFILE (PUT /vendor/nodes/{nodeId}/profile).
func (s *Service) DeclareNodeProfile(ctx context.Context, nodeID string, in requests.NodeProfile) (views.TransactionAccepted, error) {
	if err := in.Validate(); err != nil {
		return views.TransactionAccepted{}, err
	}
	return s.receiveOnly(ctx, in.VendorID, submission.KindNodeProfile, in, nodeID, func(ctx context.Context) error {
		_, err := s.ownedNode(ctx, in.VendorID, nodeID)
		return err
	})
}

// SubmitInventory receives an INVENTORY feed (POST /vendor/inventory).
func (s *Service) SubmitInventory(ctx context.Context, in requests.InventoryFeed) (views.TransactionAccepted, error) {
	if err := in.Validate(); err != nil {
		return views.TransactionAccepted{}, err
	}
	return s.receiveOnly(ctx, in.VendorID, submission.KindInventory, in, in.NodeID, func(ctx context.Context) error {
		if _, err := s.ownedNode(ctx, in.VendorID, in.NodeID); err != nil {
			return err
		}
		return s.checkListings(ctx, in)
	})
}

// checkListings refuses a line whose listing the view knows to belong to
// another node (A12); unknown listings pass through.
func (s *Service) checkListings(ctx context.Context, in requests.InventoryFeed) error {
	for i, l := range in.Lines {
		known, err := s.d.Listings.Get(ctx, l.ListingID)
		if errors.Is(err, ports.ErrNotFound) {
			continue
		}
		if err != nil {
			return err
		}
		if known.NodeID != in.NodeID {
			return shared.Invalid("lines[%d]: listing %s belongs to another node", i, l.ListingID)
		}
	}
	return nil
}

// result is what executing a submission produced.
type result struct {
	outcome submission.Outcome
	pending bool
	node    *gateway.RegisteredNode
}

func failure(code, message, path string) result {
	return result{outcome: submission.Outcome{Status: submission.Failure, Errors: []submission.Error{{Code: code, Message: message, Path: path}}}}
}

func success() result { return result{outcome: submission.Outcome{Status: submission.Success}} }

// classify turns a neighbour call error into a result: nil is success, a
// refusal is a failed transaction carrying the neighbour's Err code, anything
// else leaves the submission PROCESSING for the next pass (A10).
func classify(err error) result {
	var ref *gateway.Refusal
	switch {
	case err == nil:
		return success()
	case errors.As(err, &ref):
		return failure(ref.Code, ref.Message, ref.Path)
	default:
		return result{pending: true}
	}
}

func (s *Service) execute(ctx context.Context, sub *submission.Submission) result {
	if s.d.Random.Float64() < s.cfg.Knobs.TxFailureRate {
		return failure(CodeSimulatedFailure, "the write failed on purpose (TX_FAILURE_RATE)", "")
	}
	switch sub.Kind() {
	case submission.KindAcknowledgement, submission.KindShipmentConfirmation, submission.KindInboundReceipt:
		return success()
	case submission.KindNodeRegistration:
		return s.executeRegistration(ctx, sub)
	case submission.KindNodeProfile:
		var req requests.NodeProfile
		if err := json.Unmarshal(sub.Payload(), &req); err != nil {
			return failure(CodeMalformedPayload, err.Error(), "")
		}
		return classify(s.d.NodeTopology.DeclareOperationalProfile(ctx, string(sub.ID()), sub.NodeID(), req))
	default:
		return s.executeInventory(ctx, sub)
	}
}

func (s *Service) executeRegistration(ctx context.Context, sub *submission.Submission) result {
	var req requests.NodeRegistration
	if err := json.Unmarshal(sub.Payload(), &req); err != nil {
		return failure(CodeMalformedPayload, err.Error(), "")
	}
	node, err := s.d.NodeTopology.RegisterNode(ctx, string(sub.ID()), req)
	res := classify(err)
	if err == nil {
		res.outcome.NodeID = node.NodeID
		res.node = &node
	}
	return res
}

func (s *Service) executeInventory(ctx context.Context, sub *submission.Submission) result {
	var req requests.InventoryFeed
	if err := json.Unmarshal(sub.Payload(), &req); err != nil {
		return failure(CodeMalformedPayload, err.Error(), "")
	}
	err := s.d.Offers.ApplyInventoryFeed(ctx, string(sub.ID()), req)
	var ref *gateway.Refusal
	if errors.As(err, &ref) && ref.Code == codeFeedAlreadyApplied {
		return success() // an earlier attempt of this very transaction applied it (A10)
	}
	return classify(err)
}

// ProcessSubmission is command ProcessSubmission for one submission: it
// executes the write (calling the neighbour where the kind needs one), sets the
// outcome once and applies it to the purchase order, in ONE unit that also
// appends SubmissionProcessed and the order's event. A neighbour outage leaves
// the submission PROCESSING (counted, capped by SUBMISSION_MAX_ATTEMPTS). The
// errors submission.ErrSubmissionNotDue and ErrSubmissionAlreadyProcessed mean
// "skip".
func (s *Service) ProcessSubmission(ctx context.Context, id submission.ID) error {
	_, err := s.process(ctx, id)
	return err
}

// process is ProcessSubmission reporting whether the submission reached an outcome.
func (s *Service) process(ctx context.Context, id submission.ID) (bool, error) {
	sub, err := s.d.Submissions.Get(ctx, id)
	if err != nil {
		return false, err
	}
	if sub.Status() != submission.Processing {
		return false, submission.ErrSubmissionAlreadyProcessed
	}
	if !sub.Due(s.d.Clock.Now()) {
		return false, submission.ErrSubmissionNotDue
	}
	res := s.execute(ctx, sub)
	if res.pending {
		n, err := sub.RecordAttempt()
		if err != nil {
			return false, err
		}
		if err := s.d.Submissions.Save(ctx, sub); err != nil {
			return false, err
		}
		if n < s.cfg.SubmissionMaxAttempts {
			return false, nil
		}
		res = failure(CodeUpstreamUnavail, fmt.Sprintf("the neighbour did not answer in %d attempts", n), "")
	}
	if err := s.d.UoW.Do(ctx, func(ctx context.Context) error { return s.finish(ctx, sub, res) }); err != nil {
		return false, err
	}
	s.recordSubmissionOutcome(ctx, res.outcome)
	return true, nil
}

func (s *Service) finish(ctx context.Context, sub *submission.Submission, res result) error {
	now := s.d.Clock.Now()
	var events []shared.Event
	switch sub.Kind() {
	case submission.KindAcknowledgement, submission.KindShipmentConfirmation:
		evs, err := s.applyToOrder(ctx, sub, res.outcome.Status == submission.Success, now)
		if err != nil {
			return err
		}
		events = evs
	case submission.KindNodeRegistration:
		if res.node != nil {
			if err := s.upsertRegistered(ctx, *res.node); err != nil {
				return err
			}
		}
	}
	own, err := sub.Complete(res.outcome, now)
	if err != nil {
		return err
	}
	if err := s.d.Submissions.Save(ctx, sub); err != nil {
		return err
	}
	return s.d.Outbox.Append(ctx, append(events, own...)...)
}

// applyToOrder applies a processed ACKNOWLEDGEMENT or SHIPMENT_CONFIRMATION to its order.
func (s *Service) applyToOrder(ctx context.Context, sub *submission.Submission, ok bool, now time.Time) ([]shared.Event, error) {
	p, err := s.d.Orders.Get(ctx, purchaseorder.Number(sub.PONumber()))
	if err != nil {
		return nil, err
	}
	var evs []shared.Event
	if sub.Kind() == submission.KindAcknowledgement {
		evs, err = p.ApplyAckOutcome(ok, now)
	} else {
		evs, err = p.ApplyShipmentOutcome(ok, now)
	}
	if errors.Is(err, shared.ErrIllegalTransition) {
		s.cfg.Logger.WarnContext(ctx, "processed submission found its order in another state; order left as is", "transactionId", sub.ID(), "poNumber", p.Number(), "state", p.State())
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return evs, s.d.Orders.Save(ctx, p)
}

// upsertRegistered puts a node node-topology just created into VendorNodeView so
// the vendor can use it as soon as it reads SUCCESS (A11).
func (s *Service) upsertRegistered(ctx context.Context, r gateway.RegisteredNode) error {
	cur, err := s.d.Nodes.Get(ctx, r.NodeID)
	if err != nil && !errors.Is(err, ports.ErrNotFound) {
		return err
	}
	n, err := cur.OnRegistered(network.Registered{NodeID: r.NodeID, NodeType: r.NodeType, Subtype: r.Subtype, Operator: network.OperatorVendor,
		VendorID: r.VendorID, VendorNodeCode: r.VendorNodeCode, RegionID: r.RegionID, Timezone: r.Timezone})
	if err != nil {
		return err
	}
	return s.d.Nodes.Save(ctx, n)
}

// ProcessDueSubmissions runs ProcessSubmission over every due submission and
// returns how many reached an outcome. Skips (not due, already processed, lost a
// race) are not errors.
func (s *Service) ProcessDueSubmissions(ctx context.Context) (int, error) {
	due, err := s.d.Submissions.ListDue(ctx, s.d.Clock.Now(), batchSize)
	if err != nil {
		return 0, err
	}
	done := 0
	for _, sub := range due {
		reached, err := s.process(ctx, sub.ID())
		switch {
		case err == nil:
			if reached {
				done++
			}
		case errors.Is(err, submission.ErrSubmissionNotDue), errors.Is(err, submission.ErrSubmissionAlreadyProcessed), errors.Is(err, ports.ErrConcurrentUpdate):
		default:
			return done, fmt.Errorf("process submission %s: %w", sub.ID(), err)
		}
	}
	return done, nil
}

// PurgeSubmissions deletes processed submissions older than the retention (A22).
func (s *Service) PurgeSubmissions(ctx context.Context) (int, error) {
	return s.d.Submissions.DeleteProcessedBefore(ctx, s.d.Clock.Now().Add(-s.cfg.SubmissionRetention))
}
