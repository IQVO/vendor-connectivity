package requests

import (
	"time"

	"github.com/claudioed/vendor-connectivity/internal/domain/shared"
)

// ReturnPollQuery is the query of GET /vendor/return-advices.
type ReturnPollQuery struct {
	VendorID   string
	NodeID     string
	ReadyAfter *time.Time
	NextToken  string
	Limit      int
}

// ReturnOutcomeLine is one graded quantity of a return outcome.
type ReturnOutcomeLine struct {
	CatalogItemID  string `json:"catalogItemId"`
	OwnerRef       string `json:"ownerRef,omitempty"`
	QtyRestockable int    `json:"qtyRestockable"`
	QtyUnsellable  int    `json:"qtyUnsellable"`
}

// ReturnOutcome is the body of POST /vendor/return-outcomes. vendorId is in the
// body like in every other write of this API (G32); the orchestrator's contract
// body did not list it (ADR 0005 made the same call for the receipt).
type ReturnOutcome struct {
	VendorID        string              `json:"vendorId"`
	AuthorizationID string              `json:"authorizationId"`
	NodeID          string              `json:"nodeId"`
	ReceivedAt      time.Time           `json:"receivedAt"`
	Lines           []ReturnOutcomeLine `json:"lines"`
}

// Validate checks what can be checked without the advice.
func (r ReturnOutcome) Validate() error {
	if !shared.ValidID(r.VendorID) {
		return shared.Invalid("vendorId must be a valid id")
	}
	return nil
}
