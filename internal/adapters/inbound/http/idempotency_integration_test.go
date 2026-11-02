//go:build integration

package http_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5"
	// The database itself is booted by pgtest (testcontainers Postgres); the
	// blank import documents that dependency for the fleet sensor.
	_ "github.com/testcontainers/testcontainers-go/modules/postgres"

	vchttp "github.com/claudioed/vendor-connectivity/internal/adapters/inbound/http"
	"github.com/claudioed/vendor-connectivity/internal/testing/pgtest"
)

type txKey struct{}

// idemFixture mounts the real middleware over a real Postgres in front of a
// handler whose only effect is one processed_events row, so "one effect" is a
// row count.
type idemFixture struct {
	srv   *httptest.Server
	calls atomic.Int32
	fail  atomic.Bool
	rows  func() int
}

func newIdemFixture(t *testing.T) *idemFixture {
	t.Helper()
	pool := pgtest.NewPool(t)
	f := &idemFixture{rows: func() int { return pgtest.Count(t, pool, "processed_events") }}
	mw := vchttp.RequireIdempotencyKey(pool, func(ctx context.Context, tx pgx.Tx) context.Context {
		return context.WithValue(ctx, txKey{}, tx)
	})
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.calls.Add(1)
		body, _ := io.ReadAll(r.Body)
		if string(body) == "bad" {
			w.Header().Set("Content-Type", "application/problem+json")
			w.WriteHeader(http.StatusUnprocessableEntity)
			_, _ = w.Write([]byte(`{"type":"validation-failed"}`))
			return
		}
		if f.fail.Load() {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		tx := r.Context().Value(txKey{}).(pgx.Tx)
		if _, err := tx.Exec(r.Context(), `INSERT INTO processed_events (event_id, event_type, processed_at) VALUES ($1, 'x', now())`, r.URL.Path+string(body)); err != nil {
			w.WriteHeader(http.StatusConflict)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"transactionId":"tx-1"}`))
	}))
	f.srv = httptest.NewServer(h)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *idemFixture) post(t *testing.T, path, key, body string) (*http.Response, string) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, f.srv.URL+path, strings.NewReader(body))
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp, string(raw)
}

func TestIdempotencyFreshReplayAndReuse(t *testing.T) {
	f := newIdemFixture(t)
	first, body1 := f.post(t, "/a", "k-1", "one")
	if first.StatusCode != 202 || first.Header.Get("Idempotency-Replayed") != "" {
		t.Fatalf("first = %d replayed=%q", first.StatusCode, first.Header.Get("Idempotency-Replayed"))
	}
	again, body2 := f.post(t, "/a", "k-1", "one")
	if again.StatusCode != 202 || body2 != body1 || again.Header.Get("Idempotency-Replayed") != "true" || f.calls.Load() != 1 {
		t.Fatalf("replay = %d %q replayed=%q calls=%d", again.StatusCode, body2, again.Header.Get("Idempotency-Replayed"), f.calls.Load())
	}
	for name, c := range map[string]struct{ path, body string }{"other body": {"/a", "two"}, "other route": {"/b", "one"}} {
		resp, raw := f.post(t, c.path, "k-1", c.body)
		if resp.StatusCode != 409 || !strings.Contains(raw, "idempotency-key-reused") {
			t.Errorf("%s = %d %s, want 409 idempotency-key-reused", name, resp.StatusCode, raw)
		}
	}
	if f.rows() != 1 {
		t.Fatalf("rows = %d, want 1", f.rows())
	}
}

func TestIdempotencyKeyRequiredAndInvalid(t *testing.T) {
	f := newIdemFixture(t)
	if resp, raw := f.post(t, "/a", "", "x"); resp.StatusCode != 400 || !strings.Contains(raw, "idempotency-key-required") {
		t.Fatalf("missing = %d %s", resp.StatusCode, raw)
	}
	for name, key := range map[string]string{"too long": strings.Repeat("k", 129), "control char": "a\tb", "non ascii": "chave-ç"} {
		if resp, raw := f.post(t, "/a", key, "x"); resp.StatusCode != 400 || !strings.Contains(raw, "idempotency-key-invalid") {
			t.Errorf("%s = %d %s, want 400 idempotency-key-invalid", name, resp.StatusCode, raw)
		}
	}
	if resp, _ := f.post(t, "/a", strings.Repeat("k", 128), "x"); resp.StatusCode != 202 {
		t.Fatalf("128 characters is valid: %d", resp.StatusCode)
	}
	if f.calls.Load() != 1 {
		t.Fatalf("rejected keys never reach the handler: calls=%d", f.calls.Load())
	}
}

func TestIdempotencyConcurrentSameKeyOneEffect(t *testing.T) {
	f := newIdemFixture(t)
	var wg sync.WaitGroup
	codes := make([]int, 8)
	for i := range codes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp, _ := f.post(t, "/a", "k-conc", "same")
			codes[i] = resp.StatusCode
		}()
	}
	wg.Wait()
	for _, c := range codes {
		if c != 202 {
			t.Fatalf("every concurrent request answers the one result: %v", codes)
		}
	}
	if f.rows() != 1 || f.calls.Load() != 1 {
		t.Fatalf("rows=%d calls=%d, want exactly one effect", f.rows(), f.calls.Load())
	}
}

func TestIdempotency4xxIsCachedAnd5xxIsNot(t *testing.T) {
	f := newIdemFixture(t)
	for i := 0; i < 2; i++ {
		resp, raw := f.post(t, "/a", "k-4xx", "bad")
		if resp.StatusCode != 422 {
			t.Fatalf("attempt %d: 4xx = %d %s", i, resp.StatusCode, raw)
		}
		if replayed := resp.Header.Get("Idempotency-Replayed") == "true"; replayed != (i == 1) {
			t.Fatalf("attempt %d replayed=%v", i, replayed)
		}
	}
	if f.calls.Load() != 1 {
		t.Fatalf("a cached 4xx does not re-run the handler: calls=%d", f.calls.Load())
	}
	f.fail.Store(true)
	if resp, _ := f.post(t, "/a", "k-5xx", "ok"); resp.StatusCode != 500 {
		t.Fatalf("5xx = %d", resp.StatusCode)
	}
	f.fail.Store(false)
	resp, _ := f.post(t, "/a", "k-5xx", "ok")
	if resp.StatusCode != 202 || resp.Header.Get("Idempotency-Replayed") != "" || f.rows() != 1 {
		t.Fatalf("the retry after a 5xx re-executes: %d replayed=%q rows=%d", resp.StatusCode, resp.Header.Get("Idempotency-Replayed"), f.rows())
	}
}
