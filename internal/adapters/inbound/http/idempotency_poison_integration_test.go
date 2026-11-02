//go:build integration

package http_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5"
	_ "github.com/testcontainers/testcontainers-go/modules/postgres"

	vchttp "github.com/claudioed/vendor-connectivity/internal/adapters/inbound/http"
	"github.com/claudioed/vendor-connectivity/internal/testing/pgtest"
)

type poisonTxKey struct{}

// poisonTx makes a statement fail on the server (23505) inside the
// middleware's transaction, as a lost race on a unique index does, which
// leaves the transaction aborted.
func poisonTx(t *testing.T, r *http.Request) {
	t.Helper()
	tx, ok := r.Context().Value(poisonTxKey{}).(pgx.Tx)
	if !ok {
		t.Fatal("the middleware did not bind a transaction into the request context")
	}
	for i := 0; i < 2; i++ {
		if _, err := tx.Exec(r.Context(), `INSERT INTO processed_events (event_id, event_type, processed_at) VALUES ('dup', 'x', now())`); err != nil {
			return
		}
	}
	t.Fatal("the duplicate insert did not abort the transaction")
}

func poisonPost(mw http.Handler, key string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/vendor/acknowledgements", strings.NewReader(`{}`))
	req.Header.Set(vchttp.IdempotencyKeyHeader, key)
	rec := httptest.NewRecorder()
	mw.ServeHTTP(rec, req)
	return rec
}

func TestIdempotency_A409OverAnAbortedTransactionIsCommittedAndReplayed(t *testing.T) {
	pool := pgtest.NewPool(t)
	var calls atomic.Int32
	mw := vchttp.RequireIdempotencyKey(pool, func(ctx context.Context, tx pgx.Tx) context.Context {
		return context.WithValue(ctx, poisonTxKey{}, tx)
	})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		poisonTx(t, r)
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"type":"some-conflict","status":409}`))
	}))

	first := poisonPost(mw, "k-poison")
	if first.Code != http.StatusConflict {
		t.Fatalf("first = %d %s, want the truthful 409", first.Code, first.Body.String())
	}
	var status int
	if err := pool.QueryRow(t.Context(), `SELECT status_code FROM idempotency_keys WHERE key = 'k-poison'`).Scan(&status); err != nil || status != http.StatusConflict {
		t.Fatalf("409 outcome not committed: status=%d err=%v", status, err)
	}
	replay := poisonPost(mw, "k-poison")
	if replay.Code != http.StatusConflict || replay.Header().Get(vchttp.IdempotencyReplayedHeader) != "true" {
		t.Fatalf("replay = %d replayed=%q", replay.Code, replay.Header().Get(vchttp.IdempotencyReplayedHeader))
	}
	if replay.Body.String() != first.Body.String() {
		t.Fatalf("replay body = %s, want %s", replay.Body.String(), first.Body.String())
	}
	if calls.Load() != 1 {
		t.Fatalf("handler ran %d times, want 1", calls.Load())
	}
}

func TestIdempotency_A2xxOverAnAbortedTransactionIsNeverCached(t *testing.T) {
	pool := pgtest.NewPool(t)
	var calls atomic.Int32
	mw := vchttp.RequireIdempotencyKey(pool, func(ctx context.Context, tx pgx.Tx) context.Context {
		return context.WithValue(ctx, poisonTxKey{}, tx)
	})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			poisonTx(t, r)
		}
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"transactionId":"tx-1"}`))
	}))

	first := poisonPost(mw, "k-lie")
	if first.Code != http.StatusInternalServerError {
		t.Fatalf("first = %d %s, want 500", first.Code, first.Body.String())
	}
	var n int
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM idempotency_keys WHERE key = 'k-lie'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("a failed success left %d key rows (err=%v)", n, err)
	}
	second := poisonPost(mw, "k-lie")
	if second.Code != http.StatusAccepted {
		t.Fatalf("retry = %d %s, want 202", second.Code, second.Body.String())
	}
	if calls.Load() != 2 {
		t.Fatalf("handler ran %d times, want 2", calls.Load())
	}
}
