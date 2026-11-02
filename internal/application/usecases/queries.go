package usecases

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"sort"
	"time"

	"github.com/claudioed/vendor-connectivity/internal/application/gateway"
	"github.com/claudioed/vendor-connectivity/internal/application/ports"
	"github.com/claudioed/vendor-connectivity/internal/application/repository"
	"github.com/claudioed/vendor-connectivity/internal/application/requests"
	"github.com/claudioed/vendor-connectivity/internal/application/views"
	"github.com/claudioed/vendor-connectivity/internal/domain/network"
	"github.com/claudioed/vendor-connectivity/internal/domain/shared"
	"github.com/claudioed/vendor-connectivity/internal/domain/submission"
)

// pageToken is the opaque continuation of a poll: the keyset position and the
// filters it was issued for (A17).
type pageToken struct {
	IssuedAt time.Time `json:"t"`
	PONumber string    `json:"p"`
	VendorID string    `json:"v"`
	NodeID   string    `json:"n,omitempty"`
}

func encodeToken(t pageToken) string {
	b, _ := json.Marshal(t) //nolint:errchkjson // a struct of strings and a time always marshals
	return base64.RawURLEncoding.EncodeToString(b)
}

func decodeToken(s string) (pageToken, error) {
	b, err := base64.RawURLEncoding.DecodeString(s)
	var t pageToken
	if err != nil || json.Unmarshal(b, &t) != nil || t.PONumber == "" || t.IssuedAt.IsZero() {
		return pageToken{}, ports.ErrInvalidCursor
	}
	return t, nil
}

// PollPurchaseOrders is query PollPurchaseOrders (GET /vendor/purchase-orders):
// the vendor's orders, oldest first, paged with nextToken. With
// PO_REDELIVERY_RATE a page may repeat its first order.
func (s *Service) PollPurchaseOrders(ctx context.Context, q requests.PollQuery) (views.PurchaseOrderPage, error) {
	f, err := s.pollFilter(ctx, q)
	if err != nil {
		return views.PurchaseOrderPage{}, err
	}
	limit := s.pageLimit(q.Limit)
	f.Limit = limit + 1
	orders, err := s.d.Orders.List(ctx, f)
	if err != nil {
		return views.PurchaseOrderPage{}, err
	}
	page := views.PurchaseOrderPage{PurchaseOrders: []views.PurchaseOrder{}}
	if len(orders) > limit {
		orders = orders[:limit]
		last := orders[limit-1]
		page.NextToken = encodeToken(pageToken{IssuedAt: last.IssuedAt(), PONumber: string(last.Number()), VendorID: f.VendorID, NodeID: f.NodeID})
	}
	for _, o := range orders {
		page.PurchaseOrders = append(page.PurchaseOrders, orderView(o))
	}
	if err := s.withPackaging(ctx, page.PurchaseOrders); err != nil {
		return views.PurchaseOrderPage{}, err
	}
	if len(page.PurchaseOrders) > 0 && s.d.Random.Float64() < s.cfg.Knobs.PORedeliveryRate {
		page.PurchaseOrders = append(page.PurchaseOrders, page.PurchaseOrders[0])
	}
	return page, nil
}

func (s *Service) pageLimit(requested int) int {
	switch {
	case requested <= 0:
		return s.cfg.PageSize
	case requested > MaxPageSize:
		return MaxPageSize
	default:
		return requested
	}
}

func (s *Service) pollFilter(ctx context.Context, q requests.PollQuery) (repository.PurchaseOrderFilter, error) {
	if !shared.ValidID(q.VendorID) {
		return repository.PurchaseOrderFilter{}, shared.Invalid("vendorId is required")
	}
	if err := s.requireVendor(ctx, q.VendorID); err != nil {
		return repository.PurchaseOrderFilter{}, err
	}
	f := repository.PurchaseOrderFilter{VendorID: q.VendorID, NodeID: q.NodeID, IssuedAfter: q.CreatedAfter}
	if q.NextToken != "" {
		t, err := decodeToken(q.NextToken)
		if err != nil {
			return f, err
		}
		if t.VendorID != q.VendorID {
			return f, ports.ErrInvalidCursor
		}
		f = repository.PurchaseOrderFilter{VendorID: t.VendorID, NodeID: t.NodeID, After: &repository.Cursor{IssuedAt: t.IssuedAt, PONumber: t.PONumber}}
	}
	if f.NodeID != "" {
		if _, err := s.ownedNode(ctx, q.VendorID, f.NodeID); err != nil {
			return f, err
		}
	}
	return f, nil
}

// GetPurchaseOrder is query GetPurchaseOrder (GET /vendor/purchase-orders/{poNumber}).
func (s *Service) GetPurchaseOrder(ctx context.Context, vendorID, poNumber string) (views.PurchaseOrder, error) {
	if !shared.ValidID(vendorID) {
		return views.PurchaseOrder{}, shared.Invalid("vendorId is required")
	}
	if err := s.requireVendor(ctx, vendorID); err != nil {
		return views.PurchaseOrder{}, err
	}
	p, err := s.ownedOrder(ctx, vendorID, poNumber)
	if err != nil {
		return views.PurchaseOrder{}, err
	}
	one := []views.PurchaseOrder{orderView(p)}
	if err := s.withPackaging(ctx, one); err != nil {
		return views.PurchaseOrder{}, err
	}
	return one[0], nil
}

// GetTransaction is query GetTransaction (GET /vendor/transactions/{transactionId}).
// A transaction of another vendor is not found.
func (s *Service) GetTransaction(ctx context.Context, vendorID, transactionID string) (views.Transaction, error) {
	if !shared.ValidID(vendorID) {
		return views.Transaction{}, shared.Invalid("vendorId is required")
	}
	if err := s.requireVendor(ctx, vendorID); err != nil {
		return views.Transaction{}, err
	}
	sub, err := s.d.Submissions.Get(ctx, submission.ID(transactionID))
	if err != nil {
		return views.Transaction{}, err
	}
	if sub.VendorID() != vendorID {
		return views.Transaction{}, ports.ErrNotFound
	}
	return transactionView(sub), nil
}

func transactionView(sub *submission.Submission) views.Transaction {
	v := views.Transaction{TransactionID: string(sub.ID()), VendorID: sub.VendorID(), Kind: string(sub.Kind()), Status: string(sub.Status()),
		ReceivedAt: sub.ReceivedAt(), Errors: []views.SubmissionError{}, NodeID: sub.ResultNodeID(), PONumber: sub.PONumber()}
	if sub.Status() != submission.Processing {
		at := sub.ProcessedAt()
		v.ProcessedAt = &at
	}
	for _, e := range sub.Errors() {
		v.Errors = append(v.Errors, views.SubmissionError{Code: e.Code, Message: e.Message, Path: e.Path})
	}
	return v
}

func nodeViewOf(n network.Node) views.VendorNode {
	return views.VendorNode{NodeID: n.ID, VendorID: n.VendorID, VendorNodeCode: n.VendorNodeCode, NodeType: n.NodeType, Subtype: n.Subtype,
		Status: string(n.Status), RegionID: n.RegionID, Timezone: n.Timezone, HandlingTimeMinutes: n.HandlingTimeMinutes, ProfileRevision: n.ProfileRevision}
}

// GetNode is query GetNode (GET /vendor/nodes/{nodeId}).
func (s *Service) GetNode(ctx context.Context, vendorID, nodeID string) (views.VendorNode, error) {
	if !shared.ValidID(vendorID) {
		return views.VendorNode{}, shared.Invalid("vendorId is required")
	}
	if err := s.requireVendor(ctx, vendorID); err != nil {
		return views.VendorNode{}, err
	}
	n, err := s.ownedNode(ctx, vendorID, nodeID)
	if err != nil {
		return views.VendorNode{}, err
	}
	return nodeViewOf(n), nil
}

// ListVendorNodes is query ListVendorNodes (GET /vendor/nodes, Gap G30).
func (s *Service) ListVendorNodes(ctx context.Context, vendorID string) (views.VendorNodeList, error) {
	if !shared.ValidID(vendorID) {
		return views.VendorNodeList{}, shared.Invalid("vendorId is required")
	}
	if err := s.requireVendor(ctx, vendorID); err != nil {
		return views.VendorNodeList{}, err
	}
	nodes, err := s.d.Nodes.ListByVendor(ctx, vendorID)
	if err != nil {
		return views.VendorNodeList{}, err
	}
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].ID < nodes[j].ID })
	out := views.VendorNodeList{Nodes: make([]views.VendorNode, 0, len(nodes))}
	for _, n := range nodes {
		out.Nodes = append(out.Nodes, nodeViewOf(n))
	}
	return out, nil
}

// ListVendorNodesPage is query ListVendorNodes as the REST page (GET /vendor/nodes):
// up to limit nodes with an id after afterID, ordered by id. Next is the id of
// the last node when more follow, else empty.
func (s *Service) ListVendorNodesPage(ctx context.Context, vendorID, afterID string, limit int) (views.VendorNodeList, error) {
	out := views.VendorNodeList{Nodes: []views.VendorNode{}}
	if !shared.ValidID(vendorID) {
		return out, shared.Invalid("vendorId is required")
	}
	if err := s.requireVendor(ctx, vendorID); err != nil {
		return out, err
	}
	limit = s.pageLimit(limit)
	nodes, err := s.d.Nodes.ListByVendorPage(ctx, vendorID, afterID, limit+1)
	if err != nil {
		return out, err
	}
	if len(nodes) > limit {
		nodes = nodes[:limit]
		out.Next = nodes[limit-1].ID
	}
	for _, n := range nodes {
		out.Nodes = append(out.Nodes, nodeViewOf(n))
	}
	return out, nil
}

// GetScorecard is query GetScorecard (GET /vendor/nodes/{nodeId}/scorecard),
// relayed synchronously from node-performance.
func (s *Service) GetScorecard(ctx context.Context, vendorID, nodeID string) (views.Scorecard, error) {
	if _, err := s.GetNode(ctx, vendorID, nodeID); err != nil {
		return views.Scorecard{}, err
	}
	sc, err := s.d.Performance.GetScorecard(ctx, nodeID)
	var ref *gateway.Refusal
	if errors.As(err, &ref) && ref.Status == 404 {
		return views.Scorecard{}, ports.ErrNotFound
	}
	if err != nil {
		return views.Scorecard{}, upstreamError(err)
	}
	return sc, nil
}

// --- consumers of node-topology and offer-availability events -------------

// OnNodeRegistered feeds VendorNodeView from NodeRegistered (A13).
func (s *Service) OnNodeRegistered(ctx context.Context, eventID, eventType string, in requests.NodeRegisteredFacts) error {
	return s.updateNode(ctx, eventID, eventType, in.NodeID, func(cur network.Node) (network.Node, error) {
		return cur.OnRegistered(network.Registered{NodeID: in.NodeID, NodeType: in.NodeType, Subtype: in.Subtype, Operator: network.Operator(in.Operator),
			VendorID: in.VendorID, VendorNodeCode: in.VendorNodeCode, RegionID: in.RegionID, Timezone: in.Timezone})
	})
}

// OnNodeProfileDeclared feeds the handling time of ackBy from
// NodeOperationalProfileDeclared (E4, Gap G4).
func (s *Service) OnNodeProfileDeclared(ctx context.Context, eventID, eventType string, in requests.NodeProfileFacts) error {
	return s.updateNode(ctx, eventID, eventType, in.NodeID, func(cur network.Node) (network.Node, error) {
		return cur.OnProfileDeclared(in.NodeID, in.ProfileRevision, in.HandlingTimeMinutes)
	})
}

// OnNodeStatus feeds the status of VendorNodeView from NodeActivated,
// NodeSuspended and NodeRetired.
func (s *Service) OnNodeStatus(ctx context.Context, eventID, eventType string, in requests.NodeStatusFacts) error {
	return s.updateNode(ctx, eventID, eventType, in.NodeID, func(cur network.Node) (network.Node, error) {
		return cur.OnStatus(in.NodeID, network.Status(in.Status))
	})
}

func (s *Service) updateNode(ctx context.Context, eventID, eventType, nodeID string, apply func(network.Node) (network.Node, error)) error {
	return s.consume(ctx, eventID, eventType, func(ctx context.Context) error {
		cur, err := s.d.Nodes.Get(ctx, nodeID)
		if err != nil && !errors.Is(err, ports.ErrNotFound) {
			return err
		}
		next, err := apply(cur)
		if errors.Is(err, shared.ErrValidation) {
			s.cfg.Logger.WarnContext(ctx, "node event carries invalid facts; ignored", "eventId", eventID, "type", eventType, "error", err)
			return nil
		}
		if err != nil {
			return err
		}
		return s.d.Nodes.Save(ctx, next)
	})
}

// OnListingCreated feeds ListingView from ListingCreated.
func (s *Service) OnListingCreated(ctx context.Context, eventID, eventType string, in requests.ListingFacts) error {
	return s.consume(ctx, eventID, eventType, func(ctx context.Context) error {
		l, err := network.NewListing(in.ListingID, in.NodeID, in.CatalogItemID)
		if errors.Is(err, shared.ErrValidation) {
			s.cfg.Logger.WarnContext(ctx, "ListingCreated carries invalid ids; ignored", "eventId", eventID)
			return nil
		}
		if err != nil {
			return err
		}
		return s.d.Listings.Save(ctx, l)
	})
}
