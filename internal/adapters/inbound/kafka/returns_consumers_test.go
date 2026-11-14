package kafka_test

import (
	"testing"

	kafkaconsumer "github.com/claudioed/vendor-connectivity/internal/adapters/inbound/kafka"
)

func returnsConsumer(f *fixture) *kafkaconsumer.Consumer {
	return f.consumer(kafkaconsumer.CustomerReturnsHandlers)
}

// registeredNode feeds VendorNodeView with a RETURNS_CENTER of vendor V-1.
func registeredNode(id string) []byte {
	return event("ev-node-"+id, kafkaconsumer.TypeNodeRegistered, id, m{"nodeId": id, "nodeType": "RETURNS_CENTER", "subtype": "SORTABLE",
		"operator": "VENDOR", "vendorId": "V-1", "vendorNodeCode": "SITE-" + id, "regionId": "r-1", "timezone": "America/Sao_Paulo"})
}

func TestCustomerReturnsConsumerBuildsTheAdviceView(t *testing.T) {
	f := newFixture(t)
	c := returnsConsumer(f)
	nodes := f.nodes()
	ctx := t.Context()
	if err := nodes.HandleMessage(ctx, registeredNode("n-1")); err != nil {
		t.Fatal(err)
	}
	auth := event("e1", kafkaconsumer.TypeReturnAuthorized, "ret-1", m{"authorizationId": "ret-1", "orderId": "ord-1", "groupNo": 1,
		"reason": "NOT_NEEDED", "disposition": "NEAREST_RETURNS_CENTER", "method": "DROP_OFF",
		"lines":             []m{{"lineNo": 1, "catalogItemId": "ci-1", "quantity": 2, "ownerRef": "seller-7"}},
		"destinationNodeId": "n-1", "authorizedAt": "2026-11-14T10:00:00Z", "expiresAt": "2026-12-14T10:00:00Z", "unknownField": 1})
	label := event("e2", kafkaconsumer.TypeReturnLabelled, "ret-1", m{"authorizationId": "ret-1", "trackingNumber": "TRK-RET-9",
		"destinationNodeId": "n-1", "issuedAt": "2026-11-14T10:05:00Z"})
	dup := event("e2", kafkaconsumer.TypeReturnLabelled, "ret-1", m{"authorizationId": "ret-1", "trackingNumber": "TRK-RET-9",
		"destinationNodeId": "n-1", "issuedAt": "2026-11-14T10:05:00Z"})
	for _, msg := range [][]byte{auth, label, dup} { // the repeated label carries the same id and is skipped
		if err := c.HandleMessage(ctx, msg); err != nil {
			t.Fatal(err)
		}
	}
	a, err := f.store.Advices().Get(ctx, "ret-1")
	if err != nil || a.VendorID != "V-1" || a.NodeID != "n-1" || a.State != "AUTHORIZED" || !a.Pollable() {
		t.Fatalf("advice = %+v %v", a, err)
	}
	if len(a.Lines) != 1 || a.Lines[0].OwnerRef != "seller-7" || len(a.TrackingNumbers) != 1 || a.TrackingNumbers[0] != "TRK-RET-9" {
		t.Fatalf("advice details = %+v", a)
	}
	if a.ReadyAt.UTC().Format("2006-01-02T15:04:05Z07:00") != "2026-11-14T10:00:00Z" {
		t.Errorf("readyAt = %v", a.ReadyAt)
	}
}

func TestCustomerReturnsConsumerAttributesByOriginNodeAndSkipsUnknown(t *testing.T) {
	f := newFixture(t)
	c := returnsConsumer(f)
	nodes := f.nodes()
	ctx := t.Context()
	if err := nodes.HandleMessage(ctx, registeredNode("n-2")); err != nil {
		t.Fatal(err)
	}
	// No destinationNodeId: the ORIGIN_NODE disposition ships to the origin node.
	origin := event("e1", kafkaconsumer.TypeReturnAuthorized, "ret-2", m{"authorizationId": "ret-2", "orderId": "ord-2", "groupNo": 1,
		"disposition": "ORIGIN_NODE", "method": "DROP_OFF", "lines": []m{{"lineNo": 1, "catalogItemId": "ci-1", "quantity": 1}},
		"originNodeId": "n-2", "authorizedAt": "2026-11-14T11:00:00Z"})
	if err := c.HandleMessage(ctx, origin); err != nil {
		t.Fatal(err)
	}
	a, err := f.store.Advices().Get(ctx, "ret-2")
	if err != nil || a.NodeID != "n-2" || a.VendorID != "V-1" {
		t.Fatalf("origin advice = %+v %v", a, err)
	}
	// A KEEP authorization names no node at all: skipped, never an advice.
	keep := event("e3", kafkaconsumer.TypeReturnAuthorized, "ret-3", m{"authorizationId": "ret-3", "orderId": "ord-3", "groupNo": 1,
		"disposition": "KEEP", "method": "DROP_OFF", "lines": []m{{"lineNo": 1, "catalogItemId": "ci-1", "quantity": 1}},
		"authorizedAt": "2026-11-14T11:30:00Z"})
	if err := c.HandleMessage(ctx, keep); err != nil {
		t.Fatal(err)
	}
	// A node the view never saw is skipped too.
	unknown := event("e4", kafkaconsumer.TypeReturnAuthorized, "ret-4", m{"authorizationId": "ret-4", "orderId": "ord-4", "groupNo": 1,
		"disposition": "NEAREST_RETURNS_CENTER", "method": "DROP_OFF", "lines": []m{{"lineNo": 1, "catalogItemId": "ci-1", "quantity": 1}},
		"destinationNodeId": "n-x", "authorizedAt": "2026-11-14T11:45:00Z"})
	if err := c.HandleMessage(ctx, unknown); err != nil {
		t.Fatal(err)
	}
	cancelled := event("e5", kafkaconsumer.TypeReturnCancelled, "ret-2", m{"authorizationId": "ret-2", "reason": "SHOPPER_CANCELLED",
		"cancelledAt": "2026-11-15T09:00:00Z"})
	if err := c.HandleMessage(ctx, cancelled); err != nil {
		t.Fatal(err)
	}
	closed, err := f.store.Advices().Get(ctx, "ret-2")
	if err != nil || closed.State != "CANCELLED" || closed.Pollable() {
		t.Fatalf("cancelled advice = %+v %v", closed, err)
	}
}

func TestNewCustomerReturnsConsumerReadsTheCRTopic(t *testing.T) {
	f := newFixture(t)
	c := kafkaconsumer.NewCustomerReturnsConsumer([]string{"127.0.0.1:1"}, "g", f.svc, nil)
	if c.Topic != "retailnetwork.customer-returns.events" {
		t.Fatalf("topic = %s", c.Topic)
	}
	if len(c.Handlers) != 3 {
		t.Fatalf("handlers = %d", len(c.Handlers))
	}
	for _, typ := range []string{
		"com.retailnetwork.commerce.customer-returns.returnauthorization.ReturnAuthorized",
		"com.retailnetwork.commerce.customer-returns.returnauthorization.ReturnLabelIssued",
		"com.retailnetwork.commerce.customer-returns.returnauthorization.ReturnCancelled",
	} {
		if _, ok := c.Handlers[typ]; !ok {
			t.Errorf("no handler for %s", typ)
		}
	}
}
