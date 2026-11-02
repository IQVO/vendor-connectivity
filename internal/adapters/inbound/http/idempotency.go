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

// IdempotencyReplayedHeader is set (to "true") on a response replayed from the
// stored answer, and only then.
const IdempotencyReplayedHeader = "Idempotency-Replayed"

// MaxIdempotencyKeyLength is the longest accepted key (apis/openapi.yaml).
const MaxIdempotencyKeyLength = 128

// validIdempotencyKey reports whether k is 1..128 printable ASCII characters.
func validIdempotencyKey(k string) bool {
	if k == "" || len(k) > MaxIdempotencyKeyLength {
		return false
	}
	for i := 0; i < len(k); i++ {
		if k[i] < 0x20 || k[i] > 0x7e {
			return false
		}
	}
	return true
}

// IdempotencyTTL is how long a key answers replays (model conventions: 24h).
const IdempotencyTTL = 24 * time.Hour

// TxBinder returns a child context carrying tx, so the use case's own unit of
// work (the Postgres adapter's) JOINS the transaction the middleware opened
// instead of beginning a second one. cmd/ passes the outbound adapter's
// binder in: adapters never import each other.
type TxBinder func(ctx context.Context, tx pgx.Tx) context.Context

// RequireIdempotencyKey is the fleet's transactional Idempotency-Key
// middleware, applied to every write of the Vendor API. The key is GLOBAL
// (not per route): a request is the same request when method, path and body
// all match, and the same key on anything else is 409 idempotency-key-reused.
// Rows are kept for IdempotencyTTL (model conventions).
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
//     transactionId) with Idempotency-Replayed: true, a different method,
//     path or body is 409 idempotency-key-reused. A 5xx or a panic rolls
//     everything back and is never cached, so the same key can retry the real work.
func RequireIdempotencyKey(pool *pgxpool.Pool, bind TxBinder) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			key, ok := requestKey(w, r)
			if !ok {
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
			if _, err := tx.Exec(ctx, `DELETE FROM idempotency_keys WHERE key = $1 AND created_at < now() - make_interval(secs => $2::float8)`,
				key, IdempotencyTTL.Seconds()); err != nil {
				_ = tx.Rollback(ctx)
				writeError(w, r, err)
				return
			}
			tag, err := tx.Exec(ctx, `
				INSERT INTO idempotency_keys (route, key, request_hash)
				VALUES ($1, $2, $3)
				ON CONFLICT (key) DO NOTHING
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

// requestKey reads the Idempotency-Key header: absent is 400
// idempotency-key-required, not 1-128 printable ASCII is 400
// idempotency-key-invalid. It writes the problem and reports false.
func requestKey(w http.ResponseWriter, r *http.Request) (string, bool) {
	key := r.Header.Get(IdempotencyKeyHeader)
	switch {
	case key == "":
		writeProblem(w, r, idempotencyKeyRequired,
			"send an Idempotency-Key header of 1 to 128 printable ASCII characters with a unique value per logical request")
	case !validIdempotencyKey(key):
		writeProblem(w, r, idempotencyKeyInvalid, "the Idempotency-Key must be 1 to 128 printable ASCII characters")
	default:
		return key, true
	}
	return "", false
}

// replayCachedResponse answers a request whose key already has a committed
// row: the stored outcome when the request matches, 409 when it does not.
func replayCachedResponse(w http.ResponseWriter, r *http.Request, pool *pgxpool.Pool, route, key, requestHash string) {
	var (
		storedRoute string
		storedHash  string
		statusCode  *int
		body        []byte
		headersRaw  []byte
	)
	err := pool.QueryRow(r.Context(), `
		SELECT route, request_hash, status_code, response_body, response_headers
		FROM idempotency_keys WHERE key = $1
	`, key).Scan(&storedRoute, &storedHash, &statusCode, &body, &headersRaw)
	if err != nil {
		writeError(w, r, err)
		return
	}
	if storedRoute != route || storedHash != requestHash {
		writeProblem(w, r, idempotencyKeyReused, "the Idempotency-Key was used with a different method, path or body")
		return
	}
	if statusCode == nil {
		// Unreachable (a committed row always has its outcome); a defensive
		// 500 rather than a fabricated response.
		writeError(w, r, errMissingOutcome)
		return
	}
	copyStoredHeaders(w.Header(), headersRaw)
	w.Header().Set(IdempotencyReplayedHeader, "true")
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
		WHERE key = $4
	`, rec.code, rec.body.Bytes(), headers, key); err != nil {
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
