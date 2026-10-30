package a2a

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/claudioed/vendor-connectivity/internal/application/ports"
	"github.com/claudioed/vendor-connectivity/internal/application/views"
	"github.com/claudioed/vendor-connectivity/internal/domain/network"
	"github.com/claudioed/vendor-connectivity/internal/domain/shared"
)

// problem is the RFC 7807-shaped rejection an agent client receives instead of
// a result: domain rejections and request-shape failures are business
// answers, not transport errors, so they travel in the message envelope. For
// the domain outcomes both surfaces share, Type is the SAME slug the REST
// adapter's RFC 9457 `type` carries (internal/adapters/inbound/http/errors.go)
// and Status the same classification, so a delegating agent sees the outcome
// a REST caller would.
type problem struct {
	Type   string `json:"type"`
	Title  string `json:"title"`
	Detail string `json:"detail"`
	Status int    `json:"status"`
}

// summary renders the one-line form used for the reply's text part.
func (p *problem) summary() string {
	return fmt.Sprintf("%s (%d): %s", p.Title, p.Status, p.Detail)
}

func problemMalformedRequest(detail string) *problem {
	return &problem{Type: "malformed-skill-request", Title: "Malformed skill request", Detail: detail, Status: http.StatusBadRequest}
}

func problemUnknownSkill(skill string) *problem {
	return &problem{Type: "unknown-skill", Title: "Unknown skill", Detail: fmt.Sprintf("skill %q is not offered by this agent; the Agent Card lists the supported skills", skill), Status: http.StatusBadRequest}
}

func problemInvalidArgument(err error) *problem {
	return &problem{Type: "invalid-argument", Title: "Invalid skill argument", Detail: err.Error(), Status: http.StatusBadRequest}
}

// problemNotFound is the 404 of an identifier that can never name a resource
// (a NUL character: the REST adapter answers a NUL in a request path with the
// same not-found problem).
func problemNotFound(detail string) *problem {
	return &problem{Type: "not-found", Title: "Not found", Detail: detail, Status: http.StatusNotFound}
}

// envelope is the structured payload of every skill answer: exactly one of
// Data (a skill result) or Problem (why there is no result), plus the skill
// id the answer belongs to.
type envelope struct {
	Skill   string    `json:"skill"`
	Data    *skillDTO `json:"data,omitempty"`
	Problem *problem  `json:"problem,omitempty"`
}

// skillDTO is the union of the skill results. A2A parts are machine-readable
// JSON; each skill fills its own field, keeping the envelope flat and
// versioned as one contract. The fields are the application views the REST
// adapter serves and the MCP tools return, so the wire shape is the Vendor
// API's lowerCamelCase one; domain aggregates never cross the boundary.
type skillDTO struct {
	PurchaseOrder *views.PurchaseOrder `json:"purchaseOrder,omitempty"`
	Scorecard     *views.Scorecard     `json:"scorecard,omitempty"`
	summary       string
}

// Summary is the human-readable one-liner for the reply's text part.
func (s skillDTO) Summary() string { return s.summary }

func summarizeOrder(po views.PurchaseOrder) string {
	return fmt.Sprintf("%s: %s, node %s, %d line(s), ack by %s",
		po.PONumber, strings.ToLower(po.State), po.NodeID, len(po.Lines), po.AckBy.UTC().Format(time.RFC3339))
}

func summarizeScorecard(sc views.Scorecard) string {
	if len(sc.Windows) == 0 {
		return fmt.Sprintf("node %s: no scorecard windows", sc.NodeID)
	}
	parts := make([]string, 0, len(sc.Windows))
	for _, w := range sc.Windows {
		parts = append(parts, w.Window+" "+w.Grade)
	}
	return fmt.Sprintf("node %s: %s", sc.NodeID, strings.Join(parts, ", "))
}

// notFoundSubject names what a not-found answer is about, per skill, so the
// detail is adapter-owned text instead of the bare shared "not found".
type notFoundSubject string

const (
	subjectOrder notFoundSubject = "no purchase order with this poNumber exists for this vendor, or the vendor is unknown"
	subjectNode  notFoundSubject = "the vendor is unknown, the node is unknown, or the node has no scorecard yet"
)

// problemFromError maps an application error to a problem answer, using the
// SAME classification as the REST adapter (internal/adapters/inbound/http
// problemCatalogue) for the errors a READ can raise: 422 validation-failed,
// 404 node-not-owned-by-vendor and not-found, 503 upstream-unavailable, 502
// upstream-refused, and 500 internal-error for anything unrecognised. The
// write-side 409 classes belong to writes this read-only agent does not
// offer. Details of the infrastructure-facing classes are adapter-owned text,
// so an upstream URL or a SQL error never reaches an agent client.
func problemFromError(err error, subject notFoundSubject) *problem {
	switch {
	case errors.Is(err, shared.ErrValidation):
		return &problem{Type: "validation-failed", Title: "Validation failed", Detail: err.Error(), Status: http.StatusUnprocessableEntity}
	case errors.Is(err, network.ErrNodeNotOwnedByVendor):
		return &problem{Type: "node-not-owned-by-vendor", Title: "Node not found for this vendor", Detail: "the order or node belongs to another vendor", Status: http.StatusNotFound}
	case errors.Is(err, ports.ErrNotFound):
		return &problem{Type: "not-found", Title: "Not found", Detail: string(subject), Status: http.StatusNotFound}
	case errors.Is(err, ports.ErrUpstreamUnavailable):
		return &problem{Type: "upstream-unavailable", Title: "A neighbouring context did not answer; retry", Detail: "node-performance did not answer; retry later", Status: http.StatusServiceUnavailable}
	case errors.Is(err, ports.ErrUpstreamRefused):
		return &problem{Type: "upstream-refused", Title: "A neighbouring context refused the request", Detail: "node-performance refused the request", Status: http.StatusBadGateway}
	default:
		slog.Error("a2a skill failed", "error", err)
		return &problem{Type: "internal-error", Title: "Internal server error", Detail: "the skill failed; see server logs", Status: http.StatusInternalServerError}
	}
}
