package bdd_test

// Step vocabulary of the consolidation slice (ADR 0013): a merged release and
// its withdrawal, fed to the sourcing consumer as order-sourcing publishes them.

import (
	"context"
	"fmt"
	"net/http"

	"github.com/cucumber/godog"
)

const typeWithdrawn = "com.retailnetwork.commerce.order-sourcing.assignment.FulfillmentReleaseWithdrawn"

// mergedReleaseData is a release of a consolidated package: the carrier's ids
// on top, the optional members array, and the union lines tagged by member.
func mergedReleaseData(carrier, follower, node string) string {
	return fmt.Sprintf(`{"assignmentId":%[1]q,"releaseNo":1,"orderId":"ord-%[1]s","groupNo":1,"nodeId":%[3]q,`+
		`"requiredShipBy":"2026-10-22T21:00:00Z","tier":"STANDARD",`+
		`"members":[{"assignmentId":%[1]q,"orderId":"ord-%[1]s","groupNo":1,"promiseId":"p-1"},{"assignmentId":%[2]q,"orderId":"ord-%[2]s","groupNo":1,"promiseId":"p-2"}],`+
		`"lines":[{"assignmentId":%[1]q,"listingId":"lst-1","quantity":2},{"assignmentId":%[1]q,"listingId":"lst-2","quantity":1,"ownerRef":"seller-7"},`+
		`{"assignmentId":%[2]q,"listingId":"lst-1","quantity":3,"ownerRef":"seller-9"}]}`, carrier, follower, node)
}

func registerWithdrawn(sc *godog.ScenarioContext, w *world) {
	sc.Step(`^order-sourcing releases merged assignment "([^"]*)" with follower "([^"]*)" to node "([^"]*)"$`, func(carrier, follower, node string) error {
		return w.feed(w.sourced, typeReleased, carrier, mergedReleaseData(carrier, follower, node))
	})
	sc.Step(`^vendor "([^"]*)" has accepted the merged purchase order of "([^"]*)"$`, func(vendor, asg string) error {
		body := fmt.Sprintf(`{"vendorId":%q,"poNumber":"{po:%s}","decision":"ACCEPT","lines":[{"lineNo":1,"quantity":2},{"lineNo":2,"quantity":1},{"lineNo":3,"quantity":3}]}`, vendor, asg)
		if err := w.write(http.MethodPost, "/vendor/acknowledgements", body); err != nil || w.status != http.StatusAccepted {
			return fmt.Errorf("acknowledge: %w %d %s", err, w.status, w.body)
		}
		_, err := w.svc.ProcessDueSubmissions(context.Background())
		return err
	})
	sc.Step(`^order-sourcing withdraws release 1 of assignment "([^"]*)"$`, func(asg string) error {
		return w.feed(w.sourced, typeWithdrawn, asg, fmt.Sprintf(`{"assignmentId":%q,"releaseNo":1,"orderId":"ord-%s","groupNo":1,"reason":"PROMISE_REVISED","withdrawnAt":"2026-10-22T14:00:00Z"}`, asg, asg))
	})
}
