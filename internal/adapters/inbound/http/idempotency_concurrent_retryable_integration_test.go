//go:build integration

package http_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	_ "github.com/testcontainers/testcontainers-go/modules/postgres"

	vchttp "github.com/claudioed/vendor-connectivity/internal/adapters/inbound/http"
	"github.com/claudioed/vendor-connectivity/internal/application/ports"
	"github.com/claudioed/vendor-connectivity/internal/testing/pgtest"
)

// A concurrent duplicate waits on the key row of the first request. When the
// first ends RETRYABLE its transaction rolls back and the key row goes with
// it: the waiting duplicate inserts its own row and EXECUTES the command; it is
// not answered with the refusal it waited for (ADR 0015).
func TestIdempotency_AConcurrentDuplicateExecutesWhenTheFirstEndsRetryable(t *testing.T) {
	pool := pgtest.NewPool(t)
	var calls atomic.Int32
	started, release := make(chan struct{}), make(chan struct{})
	mw := vchttp.RequireIdempotencyKey(pool, func(ctx context.Context, _ pgx.Tx) context.Context { return ctx })(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if calls.Add(1) == 1 {
				close(started)
				<-release
				vchttp.WriteErrorForTest(w, r, ports.ErrConcurrentUpdate)
				return
			}
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"transactionId":"tx-1"}`))
		}))

	var wg sync.WaitGroup
	var first, dup *httptest.ResponseRecorder
	wg.Add(2)
	go func() { defer wg.Done(); first = poisonPost(mw, "k-dup") }()
	<-started
	go func() { defer wg.Done(); dup = poisonPost(mw, "k-dup") }()
	waitForBlockedKeyInsert(t, pool)
	close(release)
	wg.Wait()

	if first.Code != http.StatusConflict || !strings.Contains(first.Body.String(), "concurrent-update") {
		t.Fatalf("first = %d %s, want 409 concurrent-update", first.Code, first.Body.String())
	}
	if dup.Code != http.StatusAccepted || dup.Header().Get(vchttp.IdempotencyReplayedHeader) != "" || calls.Load() != 2 {
		t.Fatalf("duplicate = %d replayed=%q calls=%d, want a fresh 202 from its own execution", dup.Code, dup.Header().Get(vchttp.IdempotencyReplayedHeader), calls.Load())
	}
	var n int
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM idempotency_keys WHERE key = 'k-dup'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("key rows = %d (err=%v), want the duplicate's own stored 202", n, err)
	}
}

// waitForBlockedKeyInsert returns once a request is parked on the key row's
// INSERT ... ON CONFLICT, the state the duplicate waits in.
func waitForBlockedKeyInsert(t *testing.T, pool *pgtest.Pool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		var n int
		if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM pg_stat_activity WHERE wait_event_type = 'Lock' AND query LIKE '%INSERT INTO idempotency_keys%'`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n > 0 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("the duplicate never blocked on the key row")
}
