package kafka_test

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"testing"
	"time"

	kafkaconsumer "github.com/claudioed/vendor-connectivity/internal/adapters/inbound/kafka"
	"github.com/claudioed/vendor-connectivity/internal/adapters/outbound/memory"
	"github.com/claudioed/vendor-connectivity/internal/application/ports"
	"github.com/claudioed/vendor-connectivity/internal/application/requests"
	"github.com/claudioed/vendor-connectivity/internal/application/usecases"
	"github.com/claudioed/vendor-connectivity/internal/domain/purchaseorder"
)

var t0 = time.Date(2026, 10, 22, 13, 0, 0, 0, time.UTC)

// fixture is a Service over the memory doubles with vendor V-1 onboarded and
// the three consumers wired over it.
type fixture struct {
	store *memory.Store
	svc   *usecases.Service
	dlq   *fakeDLQWriter
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	s := memory.NewStore()
	svc := usecases.New(usecases.Deps{
		UoW: s.UnitOfWork(), Vendors: s.Vendors(), Orders: s.Orders(), Submissions: s.Submissions(), Nodes: s.Nodes(), Listings: s.Listings(), Shipments: s.Shipments(), Packaging: s.Packaging(),
		Outbox: s.Outbox(), Inbox: s.Inbox(), Clock: memory.NewClock(t0), IDs: &memory.SequentialIDs{}, Random: memory.NewScriptedRandom(),
		NodeTopology: &memory.NodeTopology{}, Offers: &memory.OfferAvailability{}, Shipping: &memory.ShippingTracking{}, Performance: &memory.NodePerformance{},
	}, usecases.Config{Logger: testLogger()})
	if _, _, err := svc.OnboardVendor(context.Background(), requests.OnboardVendor{VendorID: "V-1", Name: "Carrier A", AckWindowMinutes: 240}); err != nil {
		t.Fatal(err)
	}
	return &fixture{store: s, svc: svc, dlq: &fakeDLQWriter{}}
}

func (f *fixture) consumer(handlers func(*usecases.Service) map[string]kafkaconsumer.HandlerFunc) *kafkaconsumer.Consumer {
	return &kafkaconsumer.Consumer{Name: "test consumer", Topic: "src", Handlers: handlers(f.svc), Logger: testLogger(), Retry: fastRetry, DLQ: f.dlq}
}

func (f *fixture) nodes() *kafkaconsumer.Consumer { return f.consumer(kafkaconsumer.NodeHandlers) }
func (f *fixture) listings() *kafkaconsumer.Consumer {
	return f.consumer(kafkaconsumer.ListingHandlers)
}
func (f *fixture) sourcing() *kafkaconsumer.Consumer {
	return f.consumer(func(s *usecases.Service) map[string]kafkaconsumer.HandlerFunc {
		return kafkaconsumer.SourcingHandlers(s, testLogger())
	})
}

// event builds a structured-mode CloudEvents message of a neighbour's type.
func event(id, typ, subject string, data any) []byte {
	d, _ := json.Marshal(data)
	return []byte(fmt.Sprintf(`{"specversion":"1.0","id":%q,"source":"/retailnetwork/x","type":%q,"subject":%q,"time":"2026-10-22T13:00:00Z",`+
		`"datacontenttype":"application/json","dataschema":"urn:x","data":%s}`, id, typ, subject, d))
}

type m = map[string]any

func registered(id, node, vendor string) []byte {
	return event(id, kafkaconsumer.TypeNodeRegistered, node, m{"nodeId": node, "nodeType": "FULFILLMENT_CENTER", "subtype": "SORTABLE", "operator": "VENDOR",
		"vendorId": vendor, "vendorNodeCode": "SITE-" + node, "regionId": "r-1", "timezone": "America/Sao_Paulo", "somethingNew": true})
}

func released(id, assignment, node string) []byte {
	return event(id, kafkaconsumer.TypeReleasedToNode, assignment, m{"assignmentId": assignment, "releaseNo": 1, "orderId": "ord-1", "groupNo": 1, "nodeId": node,
		"requiredShipBy": "2026-10-22T21:00:00Z", "tier": "NEXT_DAY", "lines": []m{{"listingId": "lst-1", "quantity": 2, "catalogItemId": "c-1"}}})
}

func TestNodeEventsFeedTheNodeView(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	c := f.nodes()
	for _, msg := range [][]byte{
		registered("e1", "n-1", "V-1"),
		event("e2", kafkaconsumer.TypeProfileDeclared, "n-1", m{"nodeId": "n-1", "profileRevision": 2, "handlingTimeMinutes": 45}),
		event("e3", kafkaconsumer.TypeNodeActivated, "n-1", m{"nodeId": "n-1", "activatedAt": "2026-10-22T13:00:00Z"}),
	} {
		if err := c.HandleMessage(ctx, msg); err != nil {
			t.Fatal(err)
		}
	}
	n, err := f.store.Nodes().Get(ctx, "n-1")
	if err != nil || n.Status != "ACTIVE" || n.HandlingTimeMinutes != 45 || n.VendorID != "V-1" || n.ProfileRevision != 2 {
		t.Fatalf("node = %+v %v", n, err)
	}
	// Ordered on purpose: RETIRED is terminal, so SUSPENDED must be applied first.
	for _, step := range []struct{ typ, want string }{
		{kafkaconsumer.TypeNodeSuspended, "SUSPENDED"},
		{kafkaconsumer.TypeNodeRetired, "RETIRED"},
	} {
		if err := c.HandleMessage(ctx, event("s-"+step.want, step.typ, "n-1", m{"nodeId": "n-1"})); err != nil {
			t.Fatal(err)
		}
		if n, _ := f.store.Nodes().Get(ctx, "n-1"); string(n.Status) != step.want {
			t.Fatalf("after %s status = %s", step.typ, n.Status)
		}
	}
}

func TestListingCreatedFeedsTheListingView(t *testing.T) {
	f := newFixture(t)
	err := f.listings().HandleMessage(context.Background(), event("l1", kafkaconsumer.TypeListingCreated, "lst-1", m{"listingId": "lst-1", "catalogItemId": "c-1", "nodeId": "n-1"}))
	l, gerr := f.store.Listings().Get(context.Background(), "lst-1")
	if err != nil || gerr != nil || l.NodeID != "n-1" || l.CatalogItemID != "c-1" {
		t.Fatalf("listing = %+v %v %v", l, gerr, err)
	}
}

func TestReleaseIssuesAPurchaseOrderAndCancelCancelsIt(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	if err := f.nodes().HandleMessage(ctx, registered("e1", "n-1", "V-1")); err != nil {
		t.Fatal(err)
	}
	c := f.sourcing()
	if err := c.HandleMessage(ctx, released("r1", "asg-1", "n-1")); err != nil {
		t.Fatal(err)
	}
	po, err := f.store.Orders().Get(ctx, purchaseorder.NumberFor("asg-1", 1))
	if err != nil || po.State() != purchaseorder.Issued || po.OrderID() != "ord-1" || po.Tier() != "NEXT_DAY" || len(po.Lines()) != 1 {
		t.Fatalf("order = %+v %v", po, err)
	}
	before := f.store.Counts()
	if err := c.HandleMessage(ctx, released("r1", "asg-1", "n-1")); err != nil || f.store.Counts() != before {
		t.Fatalf("a redelivered CloudEvents id is skipped: %v", err)
	}
	if err := c.HandleMessage(ctx, event("c1", kafkaconsumer.TypeFulfillmentCancl, "asg-1", m{"assignmentId": "asg-1", "reason": "customer cancelled"})); err != nil {
		t.Fatal(err)
	}
	if po, _ := f.store.Orders().Get(ctx, purchaseorder.NumberFor("asg-1", 1)); po.State() != purchaseorder.Cancelled {
		t.Fatalf("state = %s", po.State())
	}
}

func TestSkippableMessagesReturnNil(t *testing.T) {
	f := newFixture(t)
	c := f.sourcing()
	for name, value := range map[string][]byte{
		"not json":              []byte("{"),
		"not a cloudevent":      []byte(`{"order_id":"o-1"}`),
		"a type it ignores":     event("x1", "com.retailnetwork.commerce.order-sourcing.assignment.SomethingElse", "a", m{}),
		"a producer's own type": event("x2", kafkaconsumer.TypeNodeActivated, "n-1", m{"nodeId": "n-1"}),
	} {
		if err := c.HandleMessage(context.Background(), value); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	if f.store.Counts().Inbox != 0 {
		t.Fatal("skipped messages claim nothing")
	}
}

func TestMalformedPayloadIsDeadLetteredAtOnceAndSkippedWithoutADLQ(t *testing.T) {
	f := newFixture(t)
	bad := event("b1", kafkaconsumer.TypeReleasedToNode, "asg-1", m{"assignmentId": 7})
	reader := newFakeReader(bad)
	if err := runUntilCommits(t, f.sourcing(), reader, 1); err != context.Canceled {
		t.Fatalf("Run = %v", err)
	}
	got := f.dlq.published()
	if len(got) != 1 || got[0].Topic != "src.dlq" || string(got[0].Value) != string(bad) || !reflect.DeepEqual(reader.log(), []string{"fetch:0", "commit:0"}) {
		t.Fatalf("dlq = %+v log %v", got, reader.log())
	}
	noDLQ := f.sourcing()
	noDLQ.DLQ = nil
	if err := noDLQ.HandleMessage(context.Background(), bad); err != nil {
		t.Fatalf("without a DLQ a malformed payload is skipped: %v", err)
	}
}

func TestTransientFailureIsRetriedThenDeadLettered(t *testing.T) {
	f := newFixture(t) // node n-9 is not known: ports.ErrNodeNotKnown is retryable (A4)
	reader := newFakeReader(released("r1", "asg-1", "n-9"))
	if err := runUntilCommits(t, f.sourcing(), reader, 1); err != context.Canceled {
		t.Fatalf("Run = %v", err)
	}
	got := f.dlq.published()
	if len(got) != 1 || got[0].Topic != "src.dlq" {
		t.Fatalf("dlq = %+v", got)
	}
	var cause string
	for _, h := range got[0].Headers {
		if h.Key == "x-dlq-error" {
			cause = string(h.Value)
		}
	}
	if cause != ports.ErrNodeNotKnown.Error() {
		t.Fatalf("x-dlq-error = %q", cause)
	}
}

func TestConsumerConstructorsDoNotDial(t *testing.T) {
	f := newFixture(t)
	for _, c := range []*kafkaconsumer.Consumer{
		kafkaconsumer.NewNodeConsumer([]string{"127.0.0.1:1"}, "g", f.svc, nil),
		kafkaconsumer.NewListingConsumer([]string{"127.0.0.1:1"}, "g", f.svc, nil),
		kafkaconsumer.NewSourcingConsumer([]string{"127.0.0.1:1"}, "g", f.svc, nil),
	} {
		if c.Topic == "" || c.DLQ == nil || c.Name == "" {
			t.Errorf("consumer = %+v", c)
		}
		if err := c.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	}
}
