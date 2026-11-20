// Package httpclient holds the outbound REST adapters of the model's four
// sync_calls: node-topology, offer-availability, shipping-and-tracking and
// node-performance. Each speaks the neighbour's published OpenAPI, restated
// here as small wire structs (never imported). A 4xx answer is a
// *gateway.RefusalError carrying the neighbour's problem slug as its Err name and the
// callee's verdict on whether it releases that refusal for a retry (refusal_classes.go); an
// outage, a timeout or a 5xx is ports.ErrUpstreamUnavailable.
package httpclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/claudioed/vendor-connectivity/internal/application/gateway"
	"github.com/claudioed/vendor-connectivity/internal/application/ports"
)

// DefaultTimeout bounds one neighbour call.
const DefaultTimeout = 5 * time.Second

// maxResponseBytes bounds what a neighbour may answer.
const maxResponseBytes = 4 << 20

// base is the shared transport of the four clients. refusals is the callee's table of
// released refusals (refusal_classes.go); nil for a callee this context only reads.
type base struct {
	url      string
	http     *http.Client
	refusals refusalTable
}

func newBase(baseURL string, c *http.Client, refusals refusalTable) base {
	if c == nil {
		c = &http.Client{Timeout: DefaultTimeout}
	}
	return base{url: strings.TrimRight(baseURL, "/"), http: c, refusals: refusals}
}

// problem is the neighbour's RFC 9457 body.
type problem struct {
	Type   string `json:"type"`
	Title  string `json:"title"`
	Detail string `json:"detail"`
}

// do sends one request. in is JSON-encoded when non-nil; a 2xx answer is
// decoded into out when out is non-nil.
func (b base) do(ctx context.Context, method, path, idempotencyKey string, in, out any) error {
	var body io.Reader = http.NoBody
	if in != nil {
		raw, err := json.Marshal(in)
		if err != nil {
			return fmt.Errorf("encode request: %w", err)
		}
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, b.url+path, body)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if idempotencyKey != "" {
		req.Header.Set("Idempotency-Key", idempotencyKey)
	}
	resp, err := b.http.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %s %s: %w", ports.ErrUpstreamUnavailable, method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return fmt.Errorf("%w: read %s %s: %w", ports.ErrUpstreamUnavailable, method, path, err)
	}
	switch {
	case resp.StatusCode >= http.StatusInternalServerError:
		return fmt.Errorf("%w: %s %s answered %d", ports.ErrUpstreamUnavailable, method, path, resp.StatusCode)
	case resp.StatusCode >= http.StatusBadRequest:
		return b.refusal(resp.StatusCode, raw)
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("%w: %s %s answered %d with a body that is not the contract: %w", ports.ErrUpstreamUnavailable, method, path, resp.StatusCode, err)
	}
	return nil
}

// refusal turns a 4xx answer into a *gateway.RefusalError. Retryable is the callee's
// own verdict from its table: only a slug the callee releases under the Idempotency-Key
// is retried; a slug the table does not know, a body that is not a problem and every
// refusal of a callee with no table are terminal.
func (b base) refusal(status int, raw []byte) error {
	var p problem
	if err := json.Unmarshal(raw, &p); err != nil || p.Type == "" {
		return &gateway.RefusalError{Status: status, Code: gateway.CodeFromProblemType(""), Message: http.StatusText(status)}
	}
	msg := p.Detail
	if msg == "" {
		msg = p.Title
	}
	slug := gateway.ProblemSlug(p.Type)
	return &gateway.RefusalError{Status: status, Code: gateway.CodeFromProblemType(p.Type), Slug: slug, Message: msg, Retryable: b.refusals.retryable(slug)}
}

var errEmpty = errors.New("the neighbour answered without the field this context needs")
