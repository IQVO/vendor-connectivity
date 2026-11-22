//go:build integration

package main

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	_ "github.com/testcontainers/testcontainers-go/modules/postgres"

	inboundhttp "github.com/claudioed/vendor-connectivity/internal/adapters/inbound/http"
	"github.com/claudioed/vendor-connectivity/internal/application/ports"
	"github.com/claudioed/vendor-connectivity/internal/application/usecases"
	"github.com/claudioed/vendor-connectivity/internal/domain/vendor"
	"github.com/claudioed/vendor-connectivity/internal/testing/pgtest"
)

// Idempotency replay classes (ADR 0015) through the REAL router, the real
// Idempotency-Key middleware and the Postgres adapters of the composition root
// (postgresAdapters): a refusal that can pass on a retry is not stored, so its
// key is free again and the same request under it executes; a refusal that is
// final is stored and replayed.

// losingVendors is the real Postgres vendor repository whose reads lose the
// optimistic lock while lose is set: after the real Get it commits a version
// bump through another connection, so the Save of the same request updates
// zero rows (ports.ErrConcurrentUpdate), exactly as a concurrent writer does.
type losingVendors struct {
	ports.VendorRepository
	pool *pgtest.Pool
	lose atomic.Bool
}

func (l *losingVendors) Get(ctx context.Context, id vendor.ID) (*vendor.Account, error) {
	a, err := l.VendorRepository.Get(ctx, id)
	if err == nil && l.lose.Load() {
		_, err = l.pool.Exec(ctx, `UPDATE vendor_accounts SET version = version + 1 WHERE vendor_id = $1`, string(id))
	}
	return a, err
}

type replayFixture struct {
	t       *testing.T
	pool    *pgtest.Pool
	vendors *losingVendors
	srv     *httptest.Server
}

func newReplayFixture(t *testing.T) *replayFixture {
	t.Helper()
	pool := pgtest.NewPool(t)
	ad := postgresAdapters(pool)
	vendors := &losingVendors{VendorRepository: ad.deps.Vendors, pool: pool}
	ad.deps.Vendors = vendors
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc := usecases.New(ad.deps, usecases.Config{Logger: logger})
	srv := httptest.NewServer(inboundhttp.NewRouter(&inboundhttp.Server{Service: svc, Idempotency: ad.idempotency}, logger, ""))
	t.Cleanup(srv.Close)
	return &replayFixture{t: t, pool: pool, vendors: vendors, srv: srv}
}

func (f *replayFixture) post(path, key, body string) (int, string, bool) {
	f.t.Helper()
	req, err := http.NewRequest(http.MethodPost, f.srv.URL+path, strings.NewReader(body))
	if err != nil {
		f.t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", key)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		f.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(raw), resp.Header.Get("Idempotency-Replayed") == "true"
}

func (f *replayFixture) keyRows(key string) int {
	f.t.Helper()
	var n int
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM idempotency_keys WHERE key = $1`, key).Scan(&n); err != nil {
		f.t.Fatal(err)
	}
	return n
}

func (f *replayFixture) vendorState(id string) string {
	f.t.Helper()
	var s string
	if err := f.pool.QueryRow(context.Background(), `SELECT state FROM vendor_accounts WHERE vendor_id = $1`, id).Scan(&s); err != nil {
		f.t.Fatal(err)
	}
	return s
}

func (f *replayFixture) onboard(id, key string) {
	f.t.Helper()
	if code, raw, _ := f.post("/vendors", key, `{"vendorId":"`+id+`","name":"Acme","ackWindowMinutes":120}`); code != http.StatusCreated {
		f.t.Fatalf("onboard %s = %d %s", id, code, raw)
	}
}

// concurrent-update is RETRYABLE: the lost optimistic lock committed nothing,
// so the key is released and the retry, under the SAME key, executes.
func TestConcurrentUpdateIsNotStoredSoTheSameKeyRetriesTheWork(t *testing.T) {
	f := newReplayFixture(t)
	f.onboard("V-1", "k-onboard")
	const path, key = "/vendors/V-1/suspensions", "k-suspend"

	f.vendors.lose.Store(true)
	code, raw, replayed := f.post(path, key, `{"reason":"audit"}`)
	f.vendors.lose.Store(false)
	if code != http.StatusConflict || !strings.Contains(raw, `"concurrent-update"`) || replayed {
		t.Fatalf("the loser = %d %s replayed=%v, want 409 concurrent-update", code, raw, replayed)
	}
	if n := f.keyRows(key); n != 0 {
		t.Fatalf("key rows after the lost lock = %d, want 0: a retryable refusal releases the key", n)
	}

	code, raw, replayed = f.post(path, key, `{"reason":"audit"}`)
	if code != http.StatusOK || replayed {
		t.Fatalf("the retry under the same key = %d %s replayed=%v, want a fresh 200 (it executes)", code, raw, replayed)
	}
	if s := f.vendorState("V-1"); s != "SUSPENDED" {
		t.Fatalf("the retry did not execute: vendor state = %s", s)
	}
	if n := f.keyRows(key); n != 1 {
		t.Fatalf("key rows after the success = %d, want 1: a 2xx is stored", n)
	}
	if code, _, replayed = f.post(path, key, `{"reason":"audit"}`); code != http.StatusOK || !replayed {
		t.Fatalf("third request = %d replayed=%v, want the stored 200 replayed", code, replayed)
	}
}

// not-found on a vendor that is not onboarded yet is RETRYABLE: another
// request creates the fact, then the same key runs again. And while the key is
// released a DIFFERENT body under it is a new request, not a key reuse.
func TestUnknownVendorIsNotStoredSoTheKeyRunsAgainOnceItExists(t *testing.T) {
	f := newReplayFixture(t)
	const path, key = "/vendors/V-9/suspensions", "k-late"

	if code, raw, _ := f.post(path, key, `{"reason":"audit"}`); code != http.StatusNotFound || !strings.Contains(raw, `"not-found"`) {
		t.Fatalf("unknown vendor = %d %s, want 404 not-found", code, raw)
	}
	if n := f.keyRows(key); n != 0 {
		t.Fatalf("key rows after the 404 = %d, want 0: the fact may still arrive", n)
	}
	if code, raw, _ := f.post(path, key, `{"reason":"other"}`); code != http.StatusNotFound || strings.Contains(raw, "idempotency-key-reused") {
		t.Fatalf("another body under the released key = %d %s, want a new request (404), not idempotency-key-reused", code, raw)
	}

	f.onboard("V-9", "k-onboard-9")
	code, raw, replayed := f.post(path, key, `{"reason":"audit"}`)
	if code != http.StatusOK || replayed {
		t.Fatalf("the retry once the vendor exists = %d %s replayed=%v, want a fresh 200", code, raw, replayed)
	}
	if n := f.keyRows(key); n != 1 {
		t.Fatalf("key rows after the success = %d, want 1", n)
	}
}

// vendor-suspended is RETRYABLE: a reinstatement lets the same request pass.
// The retry under the same key executes (here it moves on to the next check,
// the unknown order) instead of replaying the stored 409.
func TestVendorSuspendedIsNotStoredSoTheKeyRunsAgainAfterTheReinstatement(t *testing.T) {
	f := newReplayFixture(t)
	f.onboard("V-3", "k-onboard-3")
	if code, raw, _ := f.post("/vendors/V-3/suspensions", "k-suspend-3", `{"reason":"audit"}`); code != http.StatusOK {
		t.Fatalf("suspend = %d %s", code, raw)
	}
	const key = "k-ack"
	ack := `{"vendorId":"V-3","poNumber":"PO-1","decision":"ACCEPT","lines":[{"lineNo":1,"quantity":1}]}`

	if code, raw, _ := f.post("/vendor/acknowledgements", key, ack); code != http.StatusConflict || !strings.Contains(raw, `"vendor-suspended"`) {
		t.Fatalf("suspended vendor = %d %s, want 409 vendor-suspended", code, raw)
	}
	if n := f.keyRows(key); n != 0 {
		t.Fatalf("key rows after vendor-suspended = %d, want 0: the vendor can be reinstated", n)
	}
	if code, raw, _ := f.post("/vendors/V-3/reinstatements", "k-reinstate-3", `{}`); code != http.StatusOK {
		t.Fatalf("reinstate = %d %s", code, raw)
	}
	code, raw, replayed := f.post("/vendor/acknowledgements", key, ack)
	if code != http.StatusNotFound || replayed || strings.Contains(raw, "vendor-suspended") {
		t.Fatalf("the retry = %d %s replayed=%v, want it to execute and reach the unknown order (404)", code, raw, replayed)
	}
}
