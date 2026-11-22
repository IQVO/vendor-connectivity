// Package gateway holds the value vocabulary of the synchronous in-organization
// calls (model sync_calls): the answers the neighbours give and the way their
// refusals are carried. It has no behaviour beyond naming a refusal.
package gateway

import (
	"fmt"
	"strings"
	"unicode"
)

// RegisteredNode is what node-topology answered to RegisterNode.
type RegisteredNode struct {
	NodeID         string
	NodeType       string
	Subtype        string
	Status         string
	VendorID       string
	VendorNodeCode string
	RegionID       string
	Timezone       string
}

// LabelCall is the in-organization IssueLabel input (Gap G2): orderId and tier
// are supplied from the release this context consumed; the Vendor API never
// carries them.
type LabelCall struct {
	PONumber     string
	PackageNo    int
	AssignmentID string
	OrderID      string
	Tier         string
}

// IssuedLabel is what shipping-and-tracking answered.
type IssuedLabel struct {
	LabelRef       string
	TrackingNumber string
	Carrier        string
}

// RefusalError is a neighbour's 4xx answer: a deterministic refusal, not an outage.
// Code is the neighbour's Err identifier derived from its problem type.
type RefusalError struct {
	Status  int
	Code    string
	Message string
	Path    string
	// Slug is the neighbour's problem type slug ("" when it sent none).
	Slug string
	// Retryable is the outbound adapter's verdict: the neighbour releases this refusal
	// under the Idempotency-Key, so the same request may pass later.
	Retryable bool
}

// Error implements error.
func (r *RefusalError) Error() string {
	return fmt.Sprintf("neighbour refused (%d %s): %s", r.Status, r.Code, r.Message)
}

// ProblemSlug is the slug of a problem type: the type itself, or the last segment
// of a URI-shaped one ("https://x/problems/not-found" is "not-found").
func ProblemSlug(problemType string) string {
	if i := strings.LastIndexAny(problemType, "/#"); i >= 0 {
		return problemType[i+1:]
	}
	return problemType
}

// CodeFromProblemType turns a problem type (a slug or a URI whose last segment
// is the slug) into the neighbour's Err identifier: "stale-profile-revision"
// becomes "ErrStaleProfileRevision". An empty slug becomes "ErrRefused".
func CodeFromProblemType(problemType string) string {
	slug := ProblemSlug(problemType)
	var b strings.Builder
	b.WriteString("Err")
	up := true
	for _, r := range slug {
		switch {
		case r == '-' || r == '_' || r == ' ':
			up = true
		case !unicode.IsLetter(r) && !unicode.IsDigit(r):
		case up:
			b.WriteRune(unicode.ToUpper(r))
			up = false
		default:
			b.WriteRune(r)
		}
	}
	if b.Len() == len("Err") {
		return "ErrRefused"
	}
	return b.String()
}
