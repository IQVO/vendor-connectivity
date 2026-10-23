package usecases_test

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/claudioed/vendor-connectivity/internal/application/gateway"
	"github.com/claudioed/vendor-connectivity/internal/application/ports"
	"github.com/claudioed/vendor-connectivity/internal/application/requests"
	"github.com/claudioed/vendor-connectivity/internal/application/usecases"
	"github.com/claudioed/vendor-connectivity/internal/domain/network"
	"github.com/claudioed/vendor-connectivity/internal/domain/purchaseorder"
	"github.com/claudioed/vendor-connectivity/internal/domain/shared"
	"github.com/claudioed/vendor-connectivity/internal/domain/submission"
)

// poFixture is a vendor V-1 with node n-1 and one ISSUED order.
func poFixture(t *testing.T, cfg usecases.Config) (*harness, string) {
	t.Helper()
	h := newHarness(t, cfg)
	h.onboard("V-1")
	h.vendorNode("n-1", "V-1", 60)
	return h, h.issue("asg-1", "n-1")
}

func TestSubmitAcknowledgementReceivesATransaction(t *testing.T) {
	h, po := poFixture(t, usecases.Config{Knobs: usecases.Knobs{TxProcessingDelay: 5 * time.Second}})
	acc, err := h.svc.SubmitAcknowledgement(h.ctx, ackAll("V-1", po, "ACCEPT", ""))
	if err != nil || acc.TransactionID != "tx-1" {
		t.Fatalf("ack: %+v %v", acc, err)
	}
	if o := h.order(po); o.State != "ACK_PROCESSING" || o.Acknowledgement != nil {
		t.Fatalf("the decision stays hidden until its transaction succeeds: %+v", o)
	}
	tx := h.tx("V-1", acc.TransactionID)
	if tx.Status != "PROCESSING" || tx.Kind != "ACKNOWLEDGEMENT" || tx.PONumber != po || tx.ProcessedAt != nil || len(tx.Errors) != 0 {
		t.Fatalf("transaction = %+v", tx)
	}
	if n := h.processAll(); n != 0 {
		t.Fatalf("not due before TX_PROCESSING_DELAY: %d", n)
	}
	h.clock.Advance(5 * time.Second)
	if n := h.processAll(); n != 1 {
		t.Fatalf("due after the delay: %d", n)
	}
}

func TestAcknowledgementOutcomeIsAppliedToTheOrder(t *testing.T) {
	for name, tc := range map[string]struct {
		decision, reason, wantState, wantEvent string
	}{
		"accept": {"ACCEPT", "", "ACCEPTED", "PurchaseOrderAccepted"},
		"reject": {"REJECT", "OUT_OF_STOCK", "REJECTED", "PurchaseOrderRejected"},
	} {
		h, po := poFixture(t, usecases.Config{})
		acc, err := h.svc.SubmitAcknowledgement(h.ctx, ackAll("V-1", po, tc.decision, tc.reason))
		h.must(err)
		h.processAll()
		o := h.order(po)
		if o.State != tc.wantState || o.Acknowledgement == nil || o.Acknowledgement.TransactionID != acc.TransactionID || o.Acknowledgement.Decision != tc.decision {
			t.Errorf("%s: order = %+v", name, o)
		}
		names := h.eventNames()
		if !reflect.DeepEqual(names[len(names)-2:], []string{tc.wantEvent, "SubmissionProcessed"}) {
			t.Errorf("%s: events = %v", name, names)
		}
		if tx := h.tx("V-1", acc.TransactionID); tx.Status != "SUCCESS" || tx.ProcessedAt == nil || len(tx.Errors) != 0 {
			t.Errorf("%s: transaction = %+v", name, tx)
		}
	}
}

func TestSimulatedFailureReturnsTheOrderToIssued(t *testing.T) {
	h, po := poFixture(t, usecases.Config{Knobs: usecases.Knobs{TxFailureRate: 0.5}})
	h.rand.Values = []float64{0.1} // below the rate: the write fails on purpose
	acc, err := h.svc.SubmitAcknowledgement(h.ctx, ackAll("V-1", po, "ACCEPT", ""))
	h.must(err)
	h.processAll()
	tx := h.tx("V-1", acc.TransactionID)
	if tx.Status != "FAILURE" || len(tx.Errors) != 1 || tx.Errors[0].Code != usecases.CodeSimulatedFailure {
		t.Fatalf("transaction = %+v", tx)
	}
	o := h.order(po)
	if o.State != "ISSUED" || o.Acknowledgement != nil {
		t.Fatalf("a failed acknowledgement returns the order to ISSUED: %+v", o)
	}
	names := h.eventNames()
	if names[len(names)-1] != "SubmissionProcessed" || len(names) != 3 { // VendorOnboarded, PurchaseOrderIssued, SubmissionProcessed
		t.Fatalf("a failed transaction publishes only SubmissionProcessed: %v", names)
	}
	if _, err := h.svc.SubmitAcknowledgement(h.ctx, ackAll("V-1", po, "ACCEPT", "")); err != nil {
		t.Fatalf("the vendor can acknowledge again: %v", err)
	}
}

func TestSubmitAcknowledgementRefusals(t *testing.T) {
	tests := map[string]struct {
		mutate func(*requests.Acknowledgement)
		want   error
	}{
		"partial":        {func(a *requests.Acknowledgement) { a.Lines = a.Lines[:1] }, purchaseorder.ErrPartialAcknowledgement},
		"wrong quantity": {func(a *requests.Acknowledgement) { a.Lines[0].Quantity = 9 }, purchaseorder.ErrPartialAcknowledgement},
		"reject no code": {func(a *requests.Acknowledgement) { a.Decision = "REJECT" }, shared.ErrValidation},
		"unknown po":     {func(a *requests.Acknowledgement) { a.PONumber = "PO-nope" }, ports.ErrNotFound},
		"unknown vendor": {func(a *requests.Acknowledgement) { a.VendorID = "V-9" }, ports.ErrNotFound},
		"bad ids":        {func(a *requests.Acknowledgement) { a.PONumber = "a b" }, shared.ErrValidation},
	}
	for name, tc := range tests {
		h, po := poFixture(t, usecases.Config{})
		before := h.store.Counts()
		in := ackAll("V-1", po, "ACCEPT", "")
		tc.mutate(&in)
		if _, err := h.svc.SubmitAcknowledgement(h.ctx, in); !errors.Is(err, tc.want) {
			t.Errorf("%s: err = %v, want %v", name, err, tc.want)
		}
		if after := h.store.Counts(); after != before {
			t.Errorf("%s: a refused acknowledgement stores nothing: %+v vs %+v", name, after, before)
		}
	}
}

func TestSubmitAcknowledgementStateRefusals(t *testing.T) {
	h, po := poFixture(t, usecases.Config{})
	h.onboard("V-2")
	if _, err := h.svc.SubmitAcknowledgement(h.ctx, ackAll("V-2", po, "ACCEPT", "")); !errors.Is(err, network.ErrNodeNotOwnedByVendor) {
		t.Fatalf("another vendor's order: %v", err)
	}
	h.accept("V-1", po)
	if _, err := h.svc.SubmitAcknowledgement(h.ctx, ackAll("V-1", po, "ACCEPT", "")); !errors.Is(err, purchaseorder.ErrAlreadyAcknowledged) {
		t.Fatalf("second acknowledgement: %v", err)
	}
	late := h.issueLate()
	if _, err := h.svc.SubmitAcknowledgement(h.ctx, ackAll("V-1", late, "ACCEPT", "")); !errors.Is(err, purchaseorder.ErrAckWindowClosed) {
		t.Fatalf("after ackBy: %v", err)
	}
}

func (h *harness) issueLate() string {
	h.t.Helper()
	po := h.issue("asg-late", "n-1")
	h.clock.Advance(5 * time.Hour)
	return po
}

func TestRequestLabelIssuesOncePerPackage(t *testing.T) {
	h, po := poFixture(t, usecases.Config{})
	req := requests.LabelRequest{VendorID: "V-1", PONumber: po, Packages: []requests.LabelPackage{{PackageNo: 2}, {PackageNo: 1}}}
	if _, err := h.svc.RequestLabel(h.ctx, req); !errors.Is(err, purchaseorder.ErrLabelBeforeAcceptance) {
		t.Fatalf("before acceptance: %v", err)
	}
	h.accept("V-1", po)
	resp, err := h.svc.RequestLabel(h.ctx, req)
	if err != nil || resp.PONumber != po || len(resp.Labels) != 2 || resp.Labels[0].PackageNo != 1 || resp.Labels[1].PackageNo != 2 {
		t.Fatalf("labels: %+v %v", resp, err)
	}
	want := gateway.LabelCall{PONumber: po, PackageNo: 2, AssignmentID: "asg-1", OrderID: "ord-asg-1", Tier: "NEXT_DAY"}
	if len(h.ship.Calls) != 2 || h.ship.Calls[0] != want || h.ship.Keys[0] != "vc-label-"+po+"-2" {
		t.Fatalf("IssueLabel calls = %+v keys %v", h.ship.Calls, h.ship.Keys)
	}
	again, err := h.svc.RequestLabel(h.ctx, req)
	if err != nil || !reflect.DeepEqual(again, resp) || len(h.ship.Calls) != 2 {
		t.Fatalf("a replay is answered from storage: %+v %v calls %d", again, err, len(h.ship.Calls))
	}
	if o := h.order(po); len(o.Labels) != 2 {
		t.Fatalf("labels are on the order: %+v", o.Labels)
	}
}

func TestRequestLabelKeepsWhatWasIssuedWhenALaterPackageFails(t *testing.T) {
	h, po := poFixture(t, usecases.Config{})
	h.accept("V-1", po)
	h.ship.Err, h.ship.FailPackage = errors.New("connection refused"), 2
	req := requests.LabelRequest{VendorID: "V-1", PONumber: po, Packages: []requests.LabelPackage{{PackageNo: 1}, {PackageNo: 2}}}
	if _, err := h.svc.RequestLabel(h.ctx, req); !errors.Is(err, ports.ErrUpstreamUnavailable) {
		t.Fatalf("outage: %v", err)
	}
	if o := h.order(po); len(o.Labels) != 1 || o.Labels[0].PackageNo != 1 {
		t.Fatalf("the first label is kept: %+v", o.Labels)
	}
	h.ship.Err = nil
	resp, err := h.svc.RequestLabel(h.ctx, req)
	if err != nil || len(resp.Labels) != 2 || len(h.ship.Calls) != 3 {
		t.Fatalf("the retry issues only the missing label: %+v %v calls %d", resp, err, len(h.ship.Calls))
	}
	h.ship.Err, h.ship.FailPackage = &gateway.Refusal{Status: 422, Code: "ErrX", Message: "m"}, 3
	three := requests.LabelRequest{VendorID: "V-1", PONumber: po, Packages: []requests.LabelPackage{{PackageNo: 3}}}
	if _, err := h.svc.RequestLabel(h.ctx, three); !errors.Is(err, ports.ErrUpstreamRefused) {
		t.Fatalf("refusal: %v", err)
	}
}

func TestRequestLabelRefusals(t *testing.T) {
	h, po := poFixture(t, usecases.Config{})
	h.accept("V-1", po)
	h.onboard("V-2")
	one := []requests.LabelPackage{{PackageNo: 1}}
	for name, tc := range map[string]struct {
		req  requests.LabelRequest
		want error
	}{
		"other vendor": {requests.LabelRequest{VendorID: "V-2", PONumber: po, Packages: one}, network.ErrNodeNotOwnedByVendor},
		"unknown po":   {requests.LabelRequest{VendorID: "V-1", PONumber: "PO-x", Packages: one}, ports.ErrNotFound},
		"no packages":  {requests.LabelRequest{VendorID: "V-1", PONumber: po}, shared.ErrValidation},
		"bad ids":      {requests.LabelRequest{VendorID: "", PONumber: po, Packages: one}, shared.ErrValidation},
		"unknown vend": {requests.LabelRequest{VendorID: "V-9", PONumber: po, Packages: one}, ports.ErrNotFound},
	} {
		if _, err := h.svc.RequestLabel(h.ctx, tc.req); !errors.Is(err, tc.want) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	if len(h.ship.Calls) != 0 {
		t.Fatal("a refused request never calls shipping-and-tracking")
	}
}

func labelAll(t *testing.T, h *harness, po string) {
	t.Helper()
	_, err := h.svc.RequestLabel(h.ctx, requests.LabelRequest{VendorID: "V-1", PONumber: po, Packages: []requests.LabelPackage{{PackageNo: 1}, {PackageNo: 2}}})
	h.must(err)
}

func TestShipmentConfirmationLifecycle(t *testing.T) {
	h, po := poFixture(t, usecases.Config{})
	confirm := func(nums ...string) requests.ShipmentConfirmation {
		return requests.ShipmentConfirmation{VendorID: "V-1", PONumber: po, ShippedAt: t0.Add(2 * time.Hour), TrackingNumbers: nums}
	}
	if _, err := h.svc.SubmitShipmentConfirmation(h.ctx, confirm("T")); !errors.Is(err, purchaseorder.ErrConfirmWithoutLabel) {
		t.Fatalf("before acceptance: %v", err)
	}
	h.accept("V-1", po)
	if _, err := h.svc.SubmitShipmentConfirmation(h.ctx, confirm("T")); !errors.Is(err, purchaseorder.ErrConfirmWithoutLabel) {
		t.Fatalf("without labels: %v", err)
	}
	labelAll(t, h, po)
	if _, err := h.svc.SubmitShipmentConfirmation(h.ctx, confirm("TRK-"+po+"-1")); !errors.Is(err, purchaseorder.ErrConfirmWithoutLabel) {
		t.Fatalf("one package missing: %v", err)
	}
	acc, err := h.svc.SubmitShipmentConfirmation(h.ctx, confirm("TRK-"+po+"-1", "TRK-"+po+"-2"))
	h.must(err)
	if o := h.order(po); o.State != "SHIPMENT_PROCESSING" {
		t.Fatalf("state = %s", o.State)
	}
	h.processAll()
	o := h.order(po)
	if o.State != "SHIPPED" || o.ShippedAt == nil || !o.ShippedAt.Equal(t0.Add(2*time.Hour)) || len(o.TrackingNumbers) != 2 {
		t.Fatalf("order = %+v", o)
	}
	names := h.eventNames()
	if !reflect.DeepEqual(names[len(names)-2:], []string{"VendorShipmentConfirmed", "SubmissionProcessed"}) {
		t.Fatalf("events = %v", names)
	}
	if tx := h.tx("V-1", acc.TransactionID); tx.Kind != "SHIPMENT_CONFIRMATION" || tx.Status != "SUCCESS" {
		t.Fatalf("transaction = %+v", tx)
	}
	if _, err := h.svc.SubmitShipmentConfirmation(h.ctx, confirm("T")); !errors.Is(err, shared.ErrIllegalTransition) {
		t.Fatalf("after shipping: %v", err)
	}
}

func TestFailedShipmentConfirmationReturnsToAccepted(t *testing.T) {
	h, po := poFixture(t, usecases.Config{Knobs: usecases.Knobs{TxFailureRate: 0.5}})
	h.accept("V-1", po)
	labelAll(t, h, po)
	acc, err := h.svc.SubmitShipmentConfirmation(h.ctx, requests.ShipmentConfirmation{VendorID: "V-1", PONumber: po, ShippedAt: t0,
		TrackingNumbers: []string{"TRK-" + po + "-1", "TRK-" + po + "-2"}})
	h.must(err)
	h.rand.Values = []float64{0.1} // below the failure rate
	h.processAll()
	if tx := h.tx("V-1", acc.TransactionID); tx.Status != "FAILURE" {
		t.Fatalf("transaction = %+v", tx)
	}
	if o := h.order(po); o.State != "ACCEPTED" || len(o.Labels) != 2 || o.ShippedAt != nil {
		t.Fatalf("a failed confirmation returns the order to ACCEPTED: %+v", o)
	}
}

func TestProcessSubmissionSkips(t *testing.T) {
	h, po := poFixture(t, usecases.Config{Knobs: usecases.Knobs{TxProcessingDelay: time.Minute}})
	acc, err := h.svc.SubmitAcknowledgement(h.ctx, ackAll("V-1", po, "ACCEPT", ""))
	h.must(err)
	id := submission.ID(acc.TransactionID)
	if err := h.svc.ProcessSubmission(h.ctx, id); !errors.Is(err, submission.ErrSubmissionNotDue) {
		t.Fatalf("not due: %v", err)
	}
	h.clock.Advance(time.Minute)
	h.must(h.svc.ProcessSubmission(h.ctx, id))
	if err := h.svc.ProcessSubmission(h.ctx, id); !errors.Is(err, submission.ErrSubmissionAlreadyProcessed) {
		t.Fatalf("already processed: %v", err)
	}
	if err := h.svc.ProcessSubmission(h.ctx, "tx-nope"); !errors.Is(err, ports.ErrNotFound) {
		t.Fatalf("unknown: %v", err)
	}
	if h.order(po).State != "ACCEPTED" {
		t.Fatal("the outcome was applied once")
	}
}
