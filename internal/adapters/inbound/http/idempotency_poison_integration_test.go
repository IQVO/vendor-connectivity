//go:build integration

package http_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	vchttp "github.com/claudioed/vendor-connectivity/internal/adapters/inbound/http"
	"github.com/claudioed/vendor-connectivity/internal/testing/pgtest"
)

// poisonTx simulates what a mapped domain conflict does to the middleware's
// transaction: a statement fails on the server (a unique violation, exactly
// like a racing write hitting a unique index), the adapter translates it to a
// typed error, and the handler answers 4xx -- but the transaction is ABORTED
// from then on. The middleware must still record and return that truthful
// 4xx, never a 500.
func poisonTx(t *testing.T, r *http.Request) {
	t.Helper()
	tx, ok := r.Context().Value(txKey{}).(pgx.Tx)
	if !ok {
		t.Fatal("the middleware did not bind a transaction into the request context")
	}
	for i := 0; i < 2; i++ {
		if _, err := tx.Exec(r.Context(), `INSERT INTO idempotency_keys (route, key, request_hash) VALUES ('m p', 'dup', 'h')`); err != nil {
			return // the second insert aborts the transaction (23505)
		}
	}
	t.Fatal("the duplicate insert did not abort the transaction")
}

func poisonBinder(ctx context.Context, tx pgx.Tx) context.Context {
	return context.WithValue(ctx, txKey{}, tx)
}

func poisonConflictHandler(t *testing.T) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		poisonTx(t, r)
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"type":"https://errors.vendor-connectivity.warehouse-systems.dev/conflict","title":"Conflict","status":409}`))
	})
}

func poisonRequest(key string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/vendor/purchase-orders", nil)
	req.Header.Set(vchttp.IdempotencyKeyHeader, key)
	return req
}

// TestIdempotency_A409FromAnAbortedTransactionCommitsTheOutcome: a write that
// loses on a unique index answers 409, not 500, and that 409 is cached and
// replayed -- a retry with the same key must NEVER re-run the handler.
func TestIdempotency_A409FromAnAbortedTransactionCommitsTheOutcome(t *testing.T) {
	pool := pgtest.NewPool(t)
	mw := vchttp.RequireIdempotencyKey(pool, poisonBinder)(poisonConflictHandler(t))

	first := httptest.NewRecorder()
	mw.ServeHTTP(first, poisonRequest("k-poison"))
	if first.Code != http.StatusConflict {
		t.Fatalf("first = %d %s, want the truthful 409", first.Code, first.Body.String())
	}

	var statusCode int
	if err := pool.QueryRow(t.Context(), `SELECT status_code FROM idempotency_keys WHERE key = 'k-poison'`).Scan(&statusCode); err != nil {
		t.Fatalf("the 409 outcome was not committed: %v", err)
	}
	if statusCode != http.StatusConflict {
		t.Fatalf("cached status = %d", statusCode)
	}

	replay := httptest.NewRecorder()
	mw.ServeHTTP(replay, poisonRequest("k-poison"))
	if replay.Code != http.StatusConflict || replay.Header().Get(vchttp.IdempotencyReplayedHeader) != "true" {
		t.Fatalf("replay = %d replayed=%q, want the cached 409", replay.Code, replay.Header().Get(vchttp.IdempotencyReplayedHeader))
	}
	if replay.Body.String() != first.Body.String() {
		t.Fatalf("replay body = %s, want %s", replay.Body.String(), first.Body.String())
	}
}

// TestIdempotency_A2xxFromAnAbortedTransactionIsNeverCached: a success answer
// whose writes died with the transaction is a LIE if returned or cached.
// Roll everything back and fail the request, so the same key retries the
// real work.
func TestIdempotency_A2xxFromAnAbortedTransactionIsNeverCached(t *testing.T) {
	pool := pgtest.NewPool(t)
	var calls atomic.Int64
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			poisonTx(t, r) // only the first attempt dies inside the transaction
		}
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"transactionId":"tx-1"}`))
	})
	mw := vchttp.RequireIdempotencyKey(pool, poisonBinder)(handler)

	first := httptest.NewRecorder()
	mw.ServeHTTP(first, poisonRequest("k-lie"))
	if first.Code != http.StatusInternalServerError {
		t.Fatalf("first = %d %s, want 500 (the writes never happened)", first.Code, first.Body.String())
	}
	if n := countKey(t, pool, "k-lie"); n != 0 {
		t.Fatalf("a failed success left %d idempotency rows: the key could not retry", n)
	}

	second := httptest.NewRecorder()
	mw.ServeHTTP(second, poisonRequest("k-lie"))
	if second.Code != http.StatusAccepted {
		t.Fatalf("retry = %d %s, want the real work to succeed", second.Code, second.Body.String())
	}
	if calls.Load() != 2 {
		t.Fatalf("handler ran %d times, want 2 (the first attempt was discarded)", calls.Load())
	}
}

func countKey(t *testing.T, pool *pgxpool.Pool, key string) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM idempotency_keys WHERE key = $1`, key).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
