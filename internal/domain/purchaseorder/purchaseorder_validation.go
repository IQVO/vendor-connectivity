package purchaseorder

import (
	"errors"
	"fmt"

	"github.com/claudioed/vendor-connectivity/internal/domain/shared"
)

func checkIdentity(s Snapshot) error {
	if s.Number != NumberFor(s.AssignmentID, s.ReleaseNo) {
		return errors.New("poNumber is not derived from assignmentId and releaseNo")
	}
	if !shared.ValidID(s.VendorID) || !shared.ValidID(s.NodeID) || !shared.ValidID(s.AssignmentID) || !shared.ValidID(s.OrderID) {
		return errors.New("vendorId, nodeId, assignmentId and orderId must be valid ids")
	}
	if s.GroupNo < 0 || s.GroupNo > MaxGroupNo {
		return errors.New("groupNo out of range")
	}
	if !ValidTier(s.Tier) {
		return errors.New("tier is not a service tier")
	}
	if s.ReleaseNo < 1 || s.ReleaseNo > MaxReleaseNo {
		return errors.New("releaseNo out of range")
	}
	if s.IssuedAt.IsZero() || s.RequiredShipBy.IsZero() || s.AckBy.IsZero() {
		return errors.New("issuedAt, requiredShipBy and ackBy are required")
	}
	if s.Version < 1 {
		return errors.New("version must be at least 1")
	}
	return nil
}

func checkLines(lines []Line) error {
	if len(lines) == 0 || len(lines) > MaxLines {
		return errors.New("a purchase order has 1 to 500 lines")
	}
	for i, l := range lines {
		if l.LineNo != i+1 {
			return fmt.Errorf("line %d is numbered %d", i+1, l.LineNo)
		}
		if !shared.ValidID(l.ListingID) || l.Quantity < 1 || l.Quantity > MaxQuantity {
			return fmt.Errorf("line %d needs a valid listingId and a quantity from 1 to %d", l.LineNo, MaxQuantity)
		}
	}
	return nil
}
