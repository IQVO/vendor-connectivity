package usecases

import (
	"context"
	"errors"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/claudioed/vendor-connectivity/internal/application/gateway"
	"github.com/claudioed/vendor-connectivity/internal/application/ports"
	"github.com/claudioed/vendor-connectivity/internal/application/requests"
	"github.com/claudioed/vendor-connectivity/internal/application/views"
	"github.com/claudioed/vendor-connectivity/internal/domain/network"
	"github.com/claudioed/vendor-connectivity/internal/domain/purchaseorder"
	"github.com/claudioed/vendor-connectivity/internal/domain/shared"
	"github.com/claudioed/vendor-connectivity/internal/domain/submission"
	"github.com/claudioed/vendor-connectivity/internal/domain/vendor"
)

// maxCancelReason bounds the reason carried from FulfillmentCancelled.
const maxCancelReason = purchaseorder.MaxReason

func orderView(p *purchaseorder.PurchaseOrder) views.PurchaseOrder {
	v := views.PurchaseOrder{
		PONumber: string(p.Number()), VendorID: p.VendorID(), NodeID: p.NodeID(), State: string(p.State()),
		IssuedAt: p.IssuedAt(), RequiredShipBy: p.RequiredShipBy(), AckBy: p.AckBy(),
		Lines: make([]views.PoLine, 0, len(p.Lines())), Labels: labelViews(p.Labels()), TrackingNumbers: []string{},
	}
	for _, l := range p.Lines() {
		v.Lines = append(v.Lines, views.PoLine{LineNo: l.LineNo, ListingID: l.ListingID, Quantity: l.Quantity})
	}
	// The decision is visible only once its transaction succeeded (A25).
	if a, ok := p.Ack(); ok && (p.State() == purchaseorder.Accepted || p.State() == purchaseorder.Rejected ||
		p.State() == purchaseorder.ShipmentProcessing || p.State() == purchaseorder.Shipped) {
		v.Acknowledgement = &views.Acknowledgement{Decision: string(a.Decision), ReasonCode: string(a.ReasonCode), TransactionID: a.TransactionID}
	}
	if sh, ok := p.Shipment(); ok && p.State() == purchaseorder.Shipped {
		at := sh.ShippedAt
		v.ShippedAt, v.TrackingNumbers = &at, sh.TrackingNumbers
	}
	return v
}

func labelViews(ls []purchaseorder.Label) []views.Label {
	out := make([]views.Label, 0, len(ls))
	for _, l := range ls {
		out = append(out, views.Label{PackageNo: l.PackageNo, LabelRef: l.LabelRef, TrackingNumber: l.TrackingNumber, Carrier: l.Carrier})
	}
	return out
}

func (s *Service) saveOrder(ctx context.Context, p *purchaseorder.PurchaseOrder, evs []shared.Event) error {
	if err := s.d.Orders.Save(ctx, p); err != nil {
		return err
	}
	return s.d.Outbox.Append(ctx, evs...)
}

// IssueOnRelease is policy IssueOnRelease: on FulfillmentReleasedToNode for a
// VENDOR node it issues a purchase order (command IssuePurchaseOrder). The
// poNumber is idempotent on assignmentId + releaseNo (A1). A node the view does
// not know yet is ports.ErrNodeNotKnown, a retryable error (A4).
func (s *Service) IssueOnRelease(ctx context.Context, eventID, eventType string, in requests.ReleasedToNode) error {
	return s.consume(ctx, eventID, eventType, func(ctx context.Context) error {
		node, err := s.d.Nodes.Get(ctx, in.NodeID)
		if errors.Is(err, ports.ErrNotFound) {
			return ports.ErrNodeNotKnown
		}
		if err != nil {
			return err
		}
		if !node.VendorOperated() {
			return nil
		}
		if _, err := s.d.Orders.FindByRelease(ctx, in.AssignmentID, in.ReleaseNo); err == nil {
			return nil
		} else if !errors.Is(err, ports.ErrNotFound) {
			return err
		}
		return s.issue(ctx, node, in)
	})
}

func (s *Service) issue(ctx context.Context, node network.Node, in requests.ReleasedToNode) error {
	acct, err := s.d.Vendors.Get(ctx, vendor.ID(node.VendorID))
	if errors.Is(err, ports.ErrNotFound) {
		s.cfg.Logger.WarnContext(ctx, "release for a node whose vendor has no account; dropped", "assignmentId", in.AssignmentID, "nodeId", in.NodeID, "vendorId", node.VendorID)
		return nil
	}
	if err != nil {
		return err
	}
	lines := make([]purchaseorder.NewLine, len(in.Lines))
	for i, l := range in.Lines {
		lines[i] = purchaseorder.NewLine{ListingID: l.ListingID, Quantity: l.Quantity}
	}
	p, evs, err := purchaseorder.Issue(purchaseorder.NewOrder{
		AssignmentID: in.AssignmentID, ReleaseNo: in.ReleaseNo, VendorID: node.VendorID, NodeID: node.ID, OrderID: in.OrderID,
		GroupNo: in.GroupNo, Tier: purchaseorder.Tier(in.Tier), RequiredShipBy: in.RequiredShipBy, Lines: lines,
		AckWindow: acct.AckWindow(), HandlingTime: time.Duration(node.HandlingTimeMinutes) * time.Minute,
	}, s.d.Clock.Now())
	if errors.Is(err, shared.ErrValidation) {
		s.cfg.Logger.WarnContext(ctx, "release is not a valid purchase order; dropped", "assignmentId", in.AssignmentID, "error", err)
		return nil
	}
	if err != nil {
		return err
	}
	return s.saveOrder(ctx, p, evs)
}

// CancelOnSourcingCancel is policy CancelOnSourcingCancel: on
// FulfillmentCancelled it cancels the ISSUED order of the assignment's open
// release. An order past ISSUED stays as it is (ErrCancelAfterAcknowledgement is
// logged, A15); an unknown assignment is a no-op (A16).
func (s *Service) CancelOnSourcingCancel(ctx context.Context, eventID, eventType string, in requests.FulfillmentCancelled) error {
	return s.consume(ctx, eventID, eventType, func(ctx context.Context) error {
		p, err := s.d.Orders.FindLatestByAssignment(ctx, in.AssignmentID)
		if errors.Is(err, ports.ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		evs, err := p.Cancel(cancelReason(in.Reason), s.d.Clock.Now())
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

func cancelReason(r string) string {
	r = strings.TrimSpace(strings.Map(func(c rune) rune {
		if c < 0x20 || c == 0x7f {
			return ' '
		}
		return c
	}, r))
	if r == "" {
		return "cancelled by order-sourcing"
	}
	if rs := []rune(r); len(rs) > maxCancelReason {
		r = strings.TrimSpace(string(rs[:maxCancelReason]))
	}
	return r
}

// ExpireUnacknowledged is policy ExpireUnacknowledged (command
// ExpireUnacknowledgedOrders): every ISSUED order past ackBy becomes EXPIRED and
// publishes PurchaseOrderExpired. An order the vendor acknowledged meanwhile is
// skipped. It returns how many orders expired.
func (s *Service) ExpireUnacknowledged(ctx context.Context) (int, error) {
	orders, err := s.d.Orders.ListExpirable(ctx, s.d.Clock.Now(), batchSize)
	if err != nil {
		return 0, err
	}
	expired := 0
	for _, o := range orders {
		id := o.Number()
		err := s.d.UoW.Do(ctx, func(ctx context.Context) error {
			p, err := s.d.Orders.Get(ctx, id)
			if err != nil {
				return err
			}
			evs, err := p.Expire(s.d.Clock.Now())
			if err != nil {
				return err
			}
			return s.saveOrder(ctx, p, evs)
		})
		switch {
		case err == nil:
			expired++
		case errors.Is(err, ports.ErrConcurrentUpdate), errors.Is(err, shared.ErrIllegalTransition), errors.Is(err, ports.ErrNotFound):
			s.cfg.Logger.InfoContext(ctx, "purchase order skipped by the expiry sweep", "poNumber", id, "reason", err.Error())
		default:
			return expired, err
		}
	}
	return expired, nil
}

// ownedOrder loads the order and checks that it belongs to the vendor.
func (s *Service) ownedOrder(ctx context.Context, vendorID, poNumber string) (*purchaseorder.PurchaseOrder, error) {
	p, err := s.d.Orders.Get(ctx, purchaseorder.Number(poNumber))
	if err != nil {
		return nil, err
	}
	if p.VendorID() != vendorID {
		return nil, network.ErrNodeNotOwnedByVendor
	}
	return p, nil
}

// SubmitAcknowledgement is the vendor's single, whole-order decision
// (POST /vendor/acknowledgements): the order moves to ACK_PROCESSING and a
// Submission of kind ACKNOWLEDGEMENT is received in the same unit.
func (s *Service) SubmitAcknowledgement(ctx context.Context, in requests.Acknowledgement) (views.TransactionAccepted, error) {
	if err := in.Validate(); err != nil {
		return views.TransactionAccepted{}, err
	}
	var txID string
	err := s.atomically(ctx, func(ctx context.Context) error {
		if err := s.requireActive(ctx, in.VendorID); err != nil {
			return err
		}
		p, err := s.ownedOrder(ctx, in.VendorID, in.PONumber)
		if err != nil {
			return err
		}
		txID = s.d.IDs.NewTransactionID()
		lines := make([]purchaseorder.AckLine, len(in.Lines))
		for i, l := range in.Lines {
			lines[i] = purchaseorder.AckLine{LineNo: l.LineNo, Quantity: l.Quantity}
		}
		if _, err := p.SubmitAcknowledgement(purchaseorder.AckInput{
			Decision: purchaseorder.Decision(in.Decision), ReasonCode: purchaseorder.RejectReason(in.ReasonCode), Lines: lines, TransactionID: txID,
		}, s.d.Clock.Now()); err != nil {
			return err
		}
		return s.receiveWithOrder(ctx, p, txID, submission.KindAcknowledgement, in)
	})
	return views.TransactionAccepted{TransactionID: txID}, err
}

// SubmitShipmentConfirmation confirms shipment (POST /vendor/shipment-confirmations):
// the order moves to SHIPMENT_PROCESSING and a SHIPMENT_CONFIRMATION submission is received.
func (s *Service) SubmitShipmentConfirmation(ctx context.Context, in requests.ShipmentConfirmation) (views.TransactionAccepted, error) {
	if err := in.Validate(); err != nil {
		return views.TransactionAccepted{}, err
	}
	var txID string
	err := s.atomically(ctx, func(ctx context.Context) error {
		if err := s.requireActive(ctx, in.VendorID); err != nil {
			return err
		}
		p, err := s.ownedOrder(ctx, in.VendorID, in.PONumber)
		if err != nil {
			return err
		}
		txID = s.d.IDs.NewTransactionID()
		if _, err := p.SubmitShipmentConfirmation(purchaseorder.ShipInput{TrackingNumbers: in.TrackingNumbers, ShippedAt: in.ShippedAt, TransactionID: txID}); err != nil {
			return err
		}
		return s.receiveWithOrder(ctx, p, txID, submission.KindShipmentConfirmation, in)
	})
	return views.TransactionAccepted{TransactionID: txID}, err
}

func (s *Service) receiveWithOrder(ctx context.Context, p *purchaseorder.PurchaseOrder, txID string, kind submission.Kind, body any) error {
	sub, err := s.newSubmission(txID, p.VendorID(), kind, body, string(p.Number()), "")
	if err != nil {
		return err
	}
	if err := s.d.Orders.Save(ctx, p); err != nil {
		return err
	}
	return s.d.Submissions.Save(ctx, sub)
}

// RequestLabel issues labels for the packages of an ACCEPTED order
// (POST /vendor/shipping-labels), the one synchronous write (Gap G31). Labels
// already held are returned from storage; the missing ones are issued by
// shipping-and-tracking with a deterministic Idempotency-Key and stored, even
// when a later package fails (A19).
func (s *Service) RequestLabel(ctx context.Context, in requests.LabelRequest) (views.LabelResponse, error) {
	if err := in.Validate(); err != nil {
		return views.LabelResponse{}, err
	}
	nos := make([]int, len(in.Packages))
	for i, p := range in.Packages {
		nos[i] = p.PackageNo
	}
	if err := s.requireActive(ctx, in.VendorID); err != nil {
		return views.LabelResponse{}, err
	}
	p, err := s.ownedOrder(ctx, in.VendorID, in.PONumber)
	if err != nil {
		return views.LabelResponse{}, err
	}
	have, missing, err := p.LabelPlan(nos)
	if err != nil {
		return views.LabelResponse{}, err
	}
	issued, callErr := s.issueLabels(ctx, p, missing)
	if len(issued) > 0 {
		if err := s.storeLabels(ctx, p.Number(), issued); err != nil {
			return views.LabelResponse{}, err
		}
	}
	if callErr != nil {
		return views.LabelResponse{}, callErr
	}
	all := append(append([]purchaseorder.Label(nil), have...), issued...)
	return views.LabelResponse{PONumber: in.PONumber, Labels: labelViews(sortLabels(all))}, nil
}

func sortLabels(ls []purchaseorder.Label) []purchaseorder.Label {
	sort.Slice(ls, func(i, j int) bool { return ls[i].PackageNo < ls[j].PackageNo })
	return ls
}

// issueLabels calls IssueLabel for each missing package, stopping at the first
// failure; it returns the labels issued so far and the failure.
func (s *Service) issueLabels(ctx context.Context, p *purchaseorder.PurchaseOrder, missing []int) ([]purchaseorder.Label, error) {
	var issued []purchaseorder.Label
	for _, n := range missing {
		call := gateway.LabelCall{PONumber: string(p.Number()), PackageNo: n, AssignmentID: p.AssignmentID(), OrderID: p.OrderID(), Tier: string(p.Tier())}
		l, err := s.d.Shipping.IssueLabel(ctx, labelKey(p.Number(), n), call)
		if err != nil {
			return issued, upstreamError(err)
		}
		issued = append(issued, purchaseorder.Label{PackageNo: n, LabelRef: l.LabelRef, TrackingNumber: l.TrackingNumber, Carrier: l.Carrier})
	}
	return issued, nil
}

func labelKey(n purchaseorder.Number, pkg int) string {
	return "vc-label-" + string(n) + "-" + strconv.Itoa(pkg)
}

// upstreamError turns a neighbour failure into the application's sentinels:
// a refusal is ErrUpstreamRefused, anything else ErrUpstreamUnavailable.
func upstreamError(err error) error {
	var ref *gateway.Refusal
	if errors.As(err, &ref) {
		return errors.Join(ports.ErrUpstreamRefused, err)
	}
	return errors.Join(ports.ErrUpstreamUnavailable, err)
}

func (s *Service) storeLabels(ctx context.Context, n purchaseorder.Number, labels []purchaseorder.Label) error {
	return s.atomically(ctx, func(ctx context.Context) error {
		p, err := s.d.Orders.Get(ctx, n)
		if err != nil {
			return err
		}
		if err := p.AddLabels(labels); err != nil {
			return err
		}
		return s.d.Orders.Save(ctx, p)
	})
}
