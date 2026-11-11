package kafka_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	kafkaconsumer "github.com/claudioed/vendor-connectivity/internal/adapters/inbound/kafka"
	"github.com/claudioed/vendor-connectivity/internal/application/requests"
	"github.com/claudioed/vendor-connectivity/internal/domain/purchaseorder"
)

// mergedRelease is a FulfillmentReleasedToNode of a consolidated package (ADR
// 0013): the carrier's ids on top, the UNION of the members' lines (no lineNo,
// each tagged with its member), and the optional members array. Two members
// ordered lst-1 (two lines of one listing) and the owners differ.
func mergedRelease(id, carrier string, releaseNo int) []byte {
	return event(id, kafkaconsumer.TypeReleasedToNode, carrier, m{
		"assignmentId": carrier, "releaseNo": releaseNo, "orderId": "ord-1", "groupNo": 1, "nodeId": "n-1",
		"requiredShipBy": "2026-10-22T21:00:00Z", "tier": "STANDARD", "promisedDeliverByLatest": "2026-10-25T21:00:00Z",
		"members": []m{
			{"assignmentId": carrier, "orderId": "ord-1", "groupNo": 1, "promiseId": "p-1"},
			{"assignmentId": "asg-2", "orderId": "ord-2", "groupNo": 1, "promiseId": "p-2"},
		},
		"lines": []m{
			{"assignmentId": carrier, "listingId": "lst-1", "quantity": 2},
			{"assignmentId": carrier, "listingId": "lst-2", "quantity": 1, "ownerRef": "seller-7"},
			{"assignmentId": "asg-2", "listingId": "lst-1", "quantity": 3, "ownerRef": "seller-9"},
		},
	})
}

func withdrawnEvent(id, carrier string, releaseNo int) []byte {
	return event(id, kafkaconsumer.TypeReleaseWithdrawn, carrier, m{
		"assignmentId": carrier, "releaseNo": releaseNo, "orderId": "ord-1", "groupNo": 1, "reason": "PROMISE_REVISED",
		"withdrawnAt": "2026-10-22T14:00:00Z", "somethingNew": []int{1},
	})
}

func vendorFixture(t *testing.T) *fixture {
	t.Helper()
	f := newFixture(t)
	if err := f.nodes().HandleMessage(context.Background(), registered("e1", "n-1", "V-1")); err != nil {
		t.Fatal(err)
	}
	return f
}

func TestMergedReleaseIssuesOneOrderWithPositionalLines(t *testing.T) {
	f := vendorFixture(t)
	ctx := context.Background()
	if err := f.sourcing().HandleMessage(ctx, mergedRelease("r1", "asg-1", 1)); err != nil {
		t.Fatal(err)
	}
	if c := f.store.Counts(); c.Orders != 1 {
		t.Fatalf("one merged release is one purchase order, got %d", c.Orders)
	}
	po, err := f.store.Orders().Get(ctx, purchaseorder.NumberFor("asg-1", 1))
	if err != nil {
		t.Fatal(err)
	}
	want := []purchaseorder.Line{
		{LineNo: 1, ListingID: "lst-1", Quantity: 2},
		{LineNo: 2, ListingID: "lst-2", Quantity: 1, OwnerRef: "seller-7"},
		{LineNo: 3, ListingID: "lst-1", Quantity: 3, OwnerRef: "seller-9"},
	}
	if !reflect.DeepEqual(po.Lines(), want) {
		t.Fatalf("lines = %+v, want %+v (duplicate listing from two members, two owners)", po.Lines(), want)
	}
	if po.AssignmentID() != "asg-1" || po.OrderID() != "ord-1" || po.GroupNo() != 1 || po.Tier() != "STANDARD" || po.State() != purchaseorder.Issued {
		t.Fatalf("the order keeps the carrier's ids and the fulfilment class: %+v", po)
	}
}

func TestMergedReleaseRedeliveredIssuesNothingMore(t *testing.T) {
	f := vendorFixture(t)
	ctx := context.Background()
	c := f.sourcing()
	if err := c.HandleMessage(ctx, mergedRelease("r1", "asg-1", 1)); err != nil {
		t.Fatal(err)
	}
	before := f.store.Counts()
	for _, id := range []string{"r1", "r2"} { // same event id, then the same release under a new id
		if err := c.HandleMessage(ctx, mergedRelease(id, "asg-1", 1)); err != nil {
			t.Fatal(err)
		}
	}
	if got := f.store.Counts(); got.Orders != before.Orders || got.OutboxEvents != before.OutboxEvents {
		t.Fatalf("redelivery must change nothing: %+v vs %+v", got, before)
	}
}

func TestWithdrawCancelsTheIssuedOrderOfThatRelease(t *testing.T) {
	f := vendorFixture(t)
	ctx := context.Background()
	c := f.sourcing()
	if err := c.HandleMessage(ctx, mergedRelease("r1", "asg-1", 1)); err != nil {
		t.Fatal(err)
	}
	if err := c.HandleMessage(ctx, withdrawnEvent("w1", "asg-1", 1)); err != nil {
		t.Fatal(err)
	}
	po, err := f.store.Orders().Get(ctx, purchaseorder.NumberFor("asg-1", 1))
	if err != nil || po.State() != purchaseorder.Cancelled || po.CancelReason() != "release withdrawn: PROMISE_REVISED" {
		t.Fatalf("order = %v %v", po, err)
	}
	before := f.store.Counts()
	if err := c.HandleMessage(ctx, withdrawnEvent("w1", "asg-1", 1)); err != nil || f.store.Counts() != before {
		t.Fatalf("a redelivered CloudEvents id is skipped: %v", err)
	}
	if err := c.HandleMessage(ctx, mergedRelease("r2", "asg-1", 2)); err != nil {
		t.Fatal(err)
	}
	if again, err := f.store.Orders().Get(ctx, purchaseorder.NumberFor("asg-1", 2)); err != nil || again.State() != purchaseorder.Issued {
		t.Fatalf("the carrier released again gets its own order: %v %v", again, err)
	}
}

func TestWithdrawLeavesAnAcceptedOrder(t *testing.T) {
	f := vendorFixture(t)
	ctx := context.Background()
	c := f.sourcing()
	if err := c.HandleMessage(ctx, mergedRelease("r1", "asg-1", 1)); err != nil {
		t.Fatal(err)
	}
	po := string(purchaseorder.NumberFor("asg-1", 1))
	_, err := f.svc.SubmitAcknowledgement(ctx, requests.Acknowledgement{VendorID: "V-1", PONumber: po, Decision: "ACCEPT",
		Lines: []requests.AckLine{{LineNo: 1, Quantity: 2}, {LineNo: 2, Quantity: 1}, {LineNo: 3, Quantity: 3}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.ProcessDueSubmissions(ctx); err != nil {
		t.Fatal(err)
	}
	if o, _ := f.store.Orders().Get(ctx, purchaseorder.Number(po)); o.State() != purchaseorder.Accepted {
		t.Fatalf("precondition: the order is %s", o.State())
	}
	before := f.store.Counts()
	if err := c.HandleMessage(ctx, withdrawnEvent("w1", "asg-1", 1)); err != nil {
		t.Fatalf("a refused withdraw is acknowledged, not returned: %v", err)
	}
	o, _ := f.store.Orders().Get(ctx, purchaseorder.Number(po))
	after := f.store.Counts()
	if o.State() != purchaseorder.Accepted || after.OutboxEvents != before.OutboxEvents {
		t.Fatalf("an accepted order stays and nothing is published: %s %+v vs %+v", o.State(), after, before)
	}
}

func TestWithdrawOfAnUnknownReleaseIsAcknowledged(t *testing.T) {
	f := vendorFixture(t)
	before := f.store.Counts()
	if err := f.sourcing().HandleMessage(context.Background(), withdrawnEvent("w1", "asg-none", 1)); err != nil {
		t.Fatal(err)
	}
	if got := f.store.Counts(); got.Orders != before.Orders || got.OutboxEvents != before.OutboxEvents {
		t.Fatalf("unknown release is a no-op: %+v vs %+v", got, before)
	}
}

func TestWithdrawThatCannotDecodeIsDeadLettered(t *testing.T) {
	f := vendorFixture(t)
	bad := event("w1", kafkaconsumer.TypeReleaseWithdrawn, "asg-1", m{"assignmentId": "asg-1", "releaseNo": "one"})
	reader := newFakeReader(bad)
	if err := runUntilCommits(t, f.sourcing(), reader, 1); !errors.Is(err, context.Canceled) {
		t.Fatalf("Run = %v", err)
	}
	if got := len(f.dlq.published()); got != 1 {
		t.Fatalf("dead-lettered messages = %d, want 1", got)
	}
}
