package usecases_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/claudioed/vendor-connectivity/internal/adapters/outbound/memory"
	"github.com/claudioed/vendor-connectivity/internal/application/gateway"
	"github.com/claudioed/vendor-connectivity/internal/application/requests"
	"github.com/claudioed/vendor-connectivity/internal/application/usecases"
	"github.com/claudioed/vendor-connectivity/internal/application/views"
	"github.com/claudioed/vendor-connectivity/internal/domain/purchaseorder"
	"github.com/claudioed/vendor-connectivity/internal/domain/submission"
)

var t0 = time.Date(2026, 10, 22, 13, 0, 0, 0, time.UTC)

type harness struct {
	t      *testing.T
	ctx    context.Context
	store  *memory.Store
	svc    *usecases.Service
	clock  *memory.Clock
	nt     *memory.NodeTopology
	offers *memory.OfferAvailability
	ship   *memory.ShippingTracking
	perf   *memory.NodePerformance
	rand   *memory.ScriptedRandom
	events int
}

func newHarness(t *testing.T, cfg usecases.Config) *harness {
	t.Helper()
	s := memory.NewStore()
	h := &harness{t: t, ctx: context.Background(), store: s, clock: memory.NewClock(t0), nt: &memory.NodeTopology{}, offers: &memory.OfferAvailability{},
		ship: &memory.ShippingTracking{}, perf: &memory.NodePerformance{}, rand: memory.NewScriptedRandom()}
	h.svc = usecases.New(usecases.Deps{
		UoW: s.UnitOfWork(), Vendors: s.Vendors(), Orders: s.Orders(), Submissions: s.Submissions(), Nodes: s.Nodes(), Listings: s.Listings(), Shipments: s.Shipments(), Packaging: s.Packaging(),
		Outbox: s.Outbox(), Inbox: s.Inbox(), Clock: h.clock, IDs: &memory.SequentialIDs{}, Random: h.rand,
		NodeTopology: h.nt, Offers: h.offers, Shipping: h.ship, Performance: h.perf,
	}, cfg)
	return h
}

func (h *harness) eventID() string {
	h.events++
	return fmt.Sprintf("evt-%d", h.events)
}

func (h *harness) must(err error) {
	h.t.Helper()
	if err != nil {
		h.t.Fatal(err)
	}
}

func (h *harness) onboard(vendorID string) {
	h.t.Helper()
	_, _, err := h.svc.OnboardVendor(h.ctx, requests.OnboardVendor{VendorID: vendorID, Name: "Vendor " + vendorID, AckWindowMinutes: 240})
	h.must(err)
}

// vendorNode feeds VendorNodeView with a VENDOR node through the consumer.
func (h *harness) vendorNode(nodeID, vendorID string, handlingMinutes int) {
	h.t.Helper()
	h.must(h.svc.OnNodeRegistered(h.ctx, h.eventID(), "NodeRegistered", requests.NodeRegisteredFacts{NodeID: nodeID, NodeType: "FULFILLMENT_CENTER", Subtype: "SORTABLE",
		Operator: "VENDOR", VendorID: vendorID, VendorNodeCode: "code-" + nodeID, RegionID: "r-1", Timezone: "America/Sao_Paulo"}))
	if handlingMinutes > 0 {
		h.must(h.svc.OnNodeProfileDeclared(h.ctx, h.eventID(), "NodeOperationalProfileDeclared", requests.NodeProfileFacts{NodeID: nodeID, ProfileRevision: 1, HandlingTimeMinutes: handlingMinutes}))
	}
}

func release(assignment, nodeID string) requests.ReleasedToNode {
	return requests.ReleasedToNode{AssignmentID: assignment, ReleaseNo: 1, OrderID: "ord-" + assignment, GroupNo: 1, NodeID: nodeID,
		RequiredShipBy: t0.Add(8 * time.Hour), Tier: "NEXT_DAY",
		Lines: []requests.ReleasedLine{{ListingID: "lst-1", Quantity: 2}, {ListingID: "lst-2", Quantity: 1}}}
}

// issue releases an assignment to a node and returns the poNumber.
func (h *harness) issue(assignment, nodeID string) string {
	h.t.Helper()
	h.must(h.svc.IssueOnRelease(h.ctx, h.eventID(), "FulfillmentReleasedToNode", release(assignment, nodeID)))
	return string(purchaseorder.NumberFor(assignment, 1))
}

func ackAll(vendorID, po, decision, reason string) requests.Acknowledgement {
	return requests.Acknowledgement{VendorID: vendorID, PONumber: po, Decision: decision, ReasonCode: reason,
		Lines: []requests.AckLine{{LineNo: 1, Quantity: 2}, {LineNo: 2, Quantity: 1}}}
}

// processAll runs every due submission.
func (h *harness) processAll() int {
	h.t.Helper()
	n, err := h.svc.ProcessDueSubmissions(h.ctx)
	h.must(err)
	return n
}

// accept acknowledges (ACCEPT) and processes until the order is ACCEPTED.
func (h *harness) accept(vendorID, po string) string {
	h.t.Helper()
	tx, err := h.svc.SubmitAcknowledgement(h.ctx, ackAll(vendorID, po, "ACCEPT", ""))
	h.must(err)
	h.processAll()
	return tx.TransactionID
}

func (h *harness) order(po string) views.PurchaseOrder {
	h.t.Helper()
	v, err := h.svc.GetPurchaseOrder(h.ctx, h.orderVendor(po), po)
	h.must(err)
	return v
}

func (h *harness) orderVendor(po string) string {
	o, err := h.store.Orders().Get(h.ctx, purchaseorder.Number(po))
	h.must(err)
	return o.VendorID()
}

func (h *harness) tx(vendorID, id string) views.Transaction {
	h.t.Helper()
	v, err := h.svc.GetTransaction(h.ctx, vendorID, id)
	h.must(err)
	return v
}

// eventNames lists the outbox events by name.
func (h *harness) eventNames() []string {
	var out []string
	for _, e := range h.store.Outbox().Events() {
		out = append(out, e.EventName())
	}
	return out
}

func registration() requests.NodeRegistration {
	return requests.NodeRegistration{VendorID: "V-1", VendorNodeCode: "SITE-9", NodeType: "FULFILLMENT_CENTER", Subtype: "SORTABLE", RegionID: "r-1",
		Timezone: "America/Sao_Paulo", PostalCode: "07750-000", CountryCode: "BR"}
}

func refusal(code string) error {
	return &gateway.Refusal{Status: 409, Code: code, Message: "refused by the neighbour", Path: "$.x"}
}

func mustReceive(t *testing.T, id, kind, payload string) *submission.Submission {
	t.Helper()
	s, err := submission.Receive(submission.NewSubmission{ID: submission.ID(id), VendorID: "V-1", Kind: submission.Kind(kind), Payload: []byte(payload), NodeID: "n-1"}, t0, 0)
	if err != nil {
		t.Fatal(err)
	}
	return s
}
