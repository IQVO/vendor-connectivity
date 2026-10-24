package http

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// The idempotency middleware's Postgres half needs a database; these tests
// cover the pure response-capture half it shares with the replay path
// (capture, flush, copyStoredHeaders) without one.

func TestCaptureRecordsTheFirstStatusCodeAndTheBody(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		run   func(*capture)
		code  int
		body  string
		hdrCT string
	}{
		{
			name: "write header then body",
			run: func(c *capture) {
				c.Header().Set("Content-Type", "application/json")
				c.WriteHeader(202)
				_, _ = c.Write([]byte(`{"transactionId":"tx-1"}`))
			},
			code: 202, body: `{"transactionId":"tx-1"}`, hdrCT: "application/json",
		},
		{
			name: "only the first WriteHeader counts",
			run:  func(c *capture) { c.WriteHeader(201); c.WriteHeader(500); _, _ = c.Write([]byte("x")) },
			code: 201, body: "x",
		},
		{
			name: "a write without WriteHeader defaults to 200",
			run:  func(c *capture) { _, _ = c.Write([]byte("hi")) },
			code: 200, body: "hi",
		},
		{
			name: "an empty capture stays code 0 for the caller's default",
			run:  func(*capture) {},
			code: 0, body: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := &capture{header: http.Header{}}
			tc.run(c)
			if c.code != tc.code {
				t.Fatalf("code = %d, want %d", c.code, tc.code)
			}
			if c.body.String() != tc.body {
				t.Fatalf("body = %q, want %q", c.body.String(), tc.body)
			}
			if got := c.header.Get("Content-Type"); got != tc.hdrCT {
				t.Fatalf("content-type = %q, want %q", got, tc.hdrCT)
			}
		})
	}
}

func TestFlushCopiesHeadersCodeAndBodyOntoTheClientResponse(t *testing.T) {
	t.Parallel()
	rec := &capture{header: http.Header{}}
	rec.Header().Set("Content-Type", "application/problem+json")
	rec.Header().Add("Retry-After", "3")
	rec.WriteHeader(429)
	_, _ = rec.Write([]byte(`{"type":"rate-limited"}`))

	out := httptest.NewRecorder()
	flush(out, rec)
	resp := out.Result()
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 429 {
		t.Fatalf("status = %d, want 429", resp.StatusCode)
	}
	if resp.Header.Get("Retry-After") != "3" || resp.Header.Get("Content-Type") != "application/problem+json" {
		t.Fatalf("headers = %v", resp.Header)
	}
	var buf bytes.Buffer
	if _, err := buf.ReadFrom(resp.Body); err != nil || buf.String() != `{"type":"rate-limited"}` {
		t.Fatalf("body = %q (err %v)", buf.String(), err)
	}
}

func TestCopyStoredHeaders(t *testing.T) {
	t.Parallel()
	stored := http.Header{}
	stored.Set("Content-Type", "application/json")
	stored.Add("Set-Cookie", "a=1")
	raw, err := json.Marshal(stored)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		raw  []byte
		want http.Header
	}{
		{"a stored header set is merged in", raw, stored.Clone()},
		{"no stored headers is a no-op", nil, http.Header{}},
		{"malformed stored headers are dropped, never fatal", []byte("{not json"), http.Header{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dst := http.Header{}
			dst.Set("Content-Length", "10")
			copyStoredHeaders(dst, tc.raw)
			for k, vs := range tc.want {
				for i, v := range vs {
					if got := dst.Values(k)[i]; got != v {
						t.Fatalf("header %q = %v, want %v", k, dst.Values(k), vs)
					}
				}
			}
			if dst.Get("Content-Length") != "10" {
				t.Fatalf("copy must not clear the destination: %v", dst)
			}
		})
	}
}

func TestErrorStringMessage(t *testing.T) {
	t.Parallel()
	if got := errorString("idempotency row committed with no recorded outcome").Error(); got != "idempotency row committed with no recorded outcome" {
		t.Fatalf("Error() = %q", got)
	}
}
