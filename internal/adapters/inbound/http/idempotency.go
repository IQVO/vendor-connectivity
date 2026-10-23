package http

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// IdempotencyKeyHeader is the request header a caller must supply on a route
// wrapped by RequireIdempotencyKey.
const IdempotencyKeyHeader = "Idempotency-Key"

// MaxIdempotencyKeyLength is the longest accepted key (apis/openapi.yaml).
const MaxIdempotencyKeyLength = 128

// IdempotencyTTL is how long a key answers replays (model conventions: 24h).
const IdempotencyTTL = 24 * time.Hour

// TxBinder returns a child context carrying tx, so the use case's own unit of
// work (the Postgres adapter's) JOINS the transaction the middleware opened
// instead of beginning a second one. cmd/ passes the outbound adapter's
// binder in: adapters never import each other.
type TxBinder func(ctx context.Context, tx pgx.Tx) context.Context

// RequireIdempotencyKey is the fleet's transactional Idempotency-Key
// middleware, applied to every write of the Vendor API. A key is scoped to its
// route (method + path) and kept for IdempotencyTTL (model conventions).
//
// The correctness argument in short:
//
//   - idempotency_keys.status_code starts NULL and is set by the SAME
//     transaction that inserted the row, immediately before it commits. A
//     committed row therefore always carries its outcome: there is no "in
//     progress" state, no timeout and no retry-later answer.
//   - Two concurrent requests with the same key race on INSERT ... ON CONFLICT
//     DO NOTHING. Postgres serialises them on the primary-key unique index:
//     the second statement blocks until the first transaction resolves, so a
//     reader that sees "0 rows inserted" can rely on the original transaction
//     being finished. If it rolled back, the blocked insert succeeds instead.
//   - The wrapped handler runs with the transaction bound into its context
//     (TxBinder), so the idempotency row, the plan and its outbox rows commit
//     or roll back together.
//   - Every normal response below 500 is cached, 4xx included: the same key
//     and body replays the same answer (for an asynchronous write, the same
//     transactionId), a different body is 422 idempotency-key-reused. A 5xx or a panic rolls everything back and is
//     never cached, so the same key can retry the real work.
func RequireIdempotencyKey(pool *pgxpool.Pool, bind TxBinder) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			key := r.Header.Get(IdempotencyKeyHeader)
			if key == "" || len(key) > MaxIdempotencyKeyLength {
				writeProblem(w, r, idempotencyKeyRequired,
					"send an Idempotency-Key header of 1 to 128 characters with a unique value per logical request")
				return
			}
			route := r.Method + " " + r.URL.Path
			body, err := io.ReadAll(io.LimitReader(r.Body, maxBodyBytes))
			if err != nil {
				writeError(w, r, errMalformedRequest)
				return
			}
			_ = r.Body.Close()
			r.Body = io.NopCloser(bytes.NewReader(body))
			sum := sha256.Sum256(body)
			requestHash := hex.EncodeToString(sum[:])

			ctx := r.Context()
			tx, err := pool.Begin(ctx)
			if err != nil {
				writeError(w, r, err)
				return
			}
			// An expired key (older than the TTL) is forgotten first, in this transaction.
			if _, err := tx.Exec(ctx, `DELETE FROM idempotency_keys WHERE route = $1 AND key = $2 AND created_at < now() - make_interval(secs => $3)`,
				route, key, IdempotencyTTL.Seconds()); err != nil {
				_ = tx.Rollback(ctx)
				writeError(w, r, err)
				return
			}
			tag, err := tx.Exec(ctx, `
				INSERT INTO idempotency_keys (route, key, request_hash)
				VALUES ($1, $2, $3)
				ON CONFLICT (route, key) DO NOTHING
			`, route, key, requestHash)
			if err != nil {
				_ = tx.Rollback(ctx)
				writeError(w, r, err)
				return
			}
			if tag.RowsAffected() == 0 {
				_ = tx.Rollback(ctx)
				replayCachedResponse(w, r, pool, route, key, requestHash)
				return
			}
			runFreshRequest(ctx, w, r, tx, bind, route, key, next)
		})
	}
}

// replayCachedResponse answers a request whose key already has a committed
// row: the stored outcome when the body matches, 422 when it does not.
func replayCachedResponse(w http.ResponseWriter, r *http.Request, pool *pgxpool.Pool, route, key, requestHash string) {
	var (
		storedHash string
		statusCode *int
		body       []byte
		headersRaw []byte
	)
	err := pool.QueryRow(r.Context(), `
		SELECT request_hash, status_code, response_body, response_headers
		FROM idempotency_keys WHERE route = $1 AND key = $2
	`, route, key).Scan(&storedHash, &statusCode, &body, &headersRaw)
	if err != nil {
		writeError(w, r, err)
		return
	}
	if storedHash != requestHash {
		writeProblem(w, r, idempotencyKeyReused, "the Idempotency-Key was used with a different request body")
		return
	}
	if statusCode == nil {
		// Unreachable (a committed row always has its outcome); a defensive
		// 500 rather than a fabricated response.
		writeError(w, r, errMissingOutcome)
		return
	}
	copyStoredHeaders(w.Header(), headersRaw)
	w.WriteHeader(*statusCode)
	_, _ = w.Write(body)
}

var errMissingOutcome = errorString("idempotency row committed with no recorded outcome")

type errorString string

func (e errorString) Error() string { return string(e) }

func copyStoredHeaders(dst http.Header, raw []byte) {
	if len(raw) == 0 {
		return
	}
	var stored http.Header
	if err := json.Unmarshal(raw, &stored); err != nil {
		return
	}
	for k, vs := range stored {
		for _, v := range vs {
			dst.Add(k, v)
		}
	}
}

// capture records the handler's response so it can be persisted and
// committed BEFORE anything reaches the client.
type capture struct {
	header http.Header
	code   int
	body   bytes.Buffer
}

func (c *capture) Header() http.Header { return c.header }

func (c *capture) WriteHeader(code int) {
	if c.code == 0 {
		c.code = code
	}
}

func (c *capture) Write(b []byte) (int, error) {
	if c.code == 0 {
		c.code = http.StatusOK
	}
	return c.body.Write(b)
}

// runFreshRequest handles a genuinely new key: capture the response, persist
// the outcome in the same transaction, commit, and only then write to the
// real client.
func runFreshRequest(ctx context.Context, w http.ResponseWriter, r *http.Request, tx pgx.Tx, bind TxBinder, route, key string, next http.Handler) {
	defer func() {
		if p := recover(); p != nil {
			_ = tx.Rollback(ctx)
			panic(p)
		}
	}()
	rec := &capture{header: http.Header{}}
	next.ServeHTTP(rec, r.WithContext(bind(ctx, tx)))
	if rec.code == 0 {
		rec.code = http.StatusOK
	}
	if rec.code >= http.StatusInternalServerError {
		// A server failure is never cached: roll everything back so the same
		// key can retry the real work.
		_ = tx.Rollback(ctx)
		flush(w, rec)
		return
	}
	headers, err := json.Marshal(rec.header)
	if err != nil {
		_ = tx.Rollback(ctx)
		writeError(w, r, err)
		return
	}
	if _, err := tx.Exec(ctx, `
		UPDATE idempotency_keys
		SET status_code = $1, response_body = $2, response_headers = $3, completed_at = now()
		WHERE route = $4 AND key = $5
	`, rec.code, rec.body.Bytes(), headers, route, key); err != nil {
		_ = tx.Rollback(ctx)
		writeError(w, r, err)
		return
	}
	if err := tx.Commit(ctx); err != nil {
		writeError(w, r, err)
		return
	}
	flush(w, rec)
}

// flush copies the captured response onto the real client connection.
func flush(w http.ResponseWriter, rec *capture) {
	for k, vs := range rec.header {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(rec.code)
	_, _ = w.Write(rec.body.Bytes())
}
