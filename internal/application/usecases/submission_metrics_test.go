package usecases_test

import (
	"context"
	"reflect"
	"sync"
	"testing"

	"github.com/claudioed/vendor-connectivity/internal/application/ports"
	"github.com/claudioed/vendor-connectivity/internal/application/usecases"
)

// recordingMetrics counts the ports.SubmissionMetrics calls of a use case.
type recordingMetrics struct {
	mu        sync.Mutex
	succeeded int
	failed    []string
}

var _ ports.SubmissionMetrics = (*recordingMetrics)(nil)

func (m *recordingMetrics) SubmissionSucceeded(context.Context) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.succeeded++
}

func (m *recordingMetrics) SubmissionFailed(_ context.Context, reason string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.failed = append(m.failed, reason)
}

func TestProcessSubmissionCountsEachOutcomeOnce(t *testing.T) {
	m := &recordingMetrics{}
	h := newHarnessMetrics(t, usecases.Config{}, m)
	h.onboard("V-1")
	h.vendorNode("n-1", "V-1", 0)
	acc, err := h.svc.SubmitInventory(h.ctx, validFeed())
	h.must(err)
	h.must(h.store.Submissions().Save(h.ctx, mustReceive(t, "tx-bad", "INVENTORY", `[1]`)))

	h.processAll()
	h.processAll() // nothing is due any more: no outcome is counted twice

	if len(m.failed) != 1 || m.failed[0] != usecases.CodeMalformedPayload {
		t.Errorf("failed = %v, want one %s", m.failed, usecases.CodeMalformedPayload)
	}
	if got := h.tx("V-1", acc.TransactionID); got.Status != "SUCCESS" || m.succeeded != 1 {
		t.Errorf("transaction %s, succeeded = %d, want SUCCESS and 1", got.Status, m.succeeded)
	}
}

func TestSimulatedFailureIsCountedWithItsErrCode(t *testing.T) {
	m := &recordingMetrics{}
	h := newHarnessMetrics(t, usecases.Config{Knobs: usecases.Knobs{TxFailureRate: 1}}, m)
	h.onboard("V-1")
	h.must(h.store.Submissions().Save(h.ctx, mustReceive(t, "tx-1", "INVENTORY", `[1]`)))

	h.processAll()

	if want := []string{usecases.CodeSimulatedFailure}; !reflect.DeepEqual(m.failed, want) || m.succeeded != 0 {
		t.Errorf("failed = %v succeeded = %d, want %v and 0", m.failed, m.succeeded, want)
	}
}

// A Deps without Metrics (every other test) must process without panicking.
func TestNoMetricsIsANoOp(t *testing.T) {
	h := newHarness(t, usecases.Config{})
	h.onboard("V-1")
	h.must(h.store.Submissions().Save(h.ctx, mustReceive(t, "tx-1", "INVENTORY", `[1]`)))
	if n := h.processAll(); n != 1 {
		t.Fatalf("processed %d, want 1", n)
	}
}
