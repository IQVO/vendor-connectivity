package bdd_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/cucumber/godog"

	"github.com/claudioed/vendor-connectivity/internal/application/gateway"
	"github.com/claudioed/vendor-connectivity/internal/application/requests"
	"github.com/claudioed/vendor-connectivity/internal/domain/purchaseorder"
)

const (
	typeNodeRegistered  = "com.retailnetwork.logistics.node-topology.node.NodeRegistered"
	typeProfileDeclared = "com.retailnetwork.logistics.node-topology.node.NodeOperationalProfileDeclared"
	typeNodeActivated   = "com.retailnetwork.logistics.node-topology.node.NodeActivated"
	typeNodeSuspended   = "com.retailnetwork.logistics.node-topology.node.NodeSuspended"
	typeListingCreated  = "com.retailnetwork.commerce.offer-availability.listing.ListingCreated"
	typeReleased        = "com.retailnetwork.commerce.order-sourcing.assignment.FulfillmentReleasedToNode"
	typeCancelled       = "com.retailnetwork.commerce.order-sourcing.assignment.FulfillmentCancelled"
)

func registerGiven(sc *godog.ScenarioContext, w *world) {
	sc.Step(`^the clock is "([^"]*)"$`, func(ts string) error {
		t, err := time.Parse(time.RFC3339, ts)
		if err != nil {
			return err
		}
		w.clock.Set(t)
		return nil
	})
	sc.Step(`^every transaction fails on purpose$`, func() error {
		w.cfg.Knobs.TxFailureRate = 1
		w.build()
		return nil
	})
	sc.Step(`^every poll page repeats its first order$`, func() error {
		w.cfg.Knobs.PORedeliveryRate = 1
		w.build()
		return nil
	})
	sc.Step(`^a transaction takes (\d+) seconds to process$`, func(n int) error {
		w.cfg.Knobs.TxProcessingDelay = time.Duration(n) * time.Second
		w.build()
		return nil
	})
	sc.Step(`^vendor "([^"]*)" is onboarded with an acknowledgement window of (\d+) minutes$`, func(id string, minutes int) error {
		_, _, err := w.svc.OnboardVendor(context.Background(), requests.OnboardVendor{VendorID: id, Name: "Carrier " + id, AckWindowMinutes: minutes})
		return err
	})
	sc.Step(`^node "([^"]*)" of vendor "([^"]*)" has a handling time of (\d+) minutes$`, func(node, vendor string, minutes int) error {
		reg := fmt.Sprintf(`{"nodeId":%q,"nodeType":"FULFILLMENT_CENTER","subtype":"SORTABLE","operator":"VENDOR","vendorId":%q,"vendorNodeCode":"code-%s","regionId":"r-1","timezone":"America/Sao_Paulo"}`, node, vendor, node)
		prof := fmt.Sprintf(`{"nodeId":%q,"profileRevision":1,"handlingTimeMinutes":%d}`, node, minutes)
		for _, step := range []struct{ typ, data string }{{typeNodeRegistered, reg}, {typeProfileDeclared, prof}, {typeNodeActivated, fmt.Sprintf(`{"nodeId":%q}`, node)}} {
			if err := w.feed(w.nodes, step.typ, node, step.data); err != nil {
				return err
			}
		}
		return nil
	})
	sc.Step(`^the listing "([^"]*)" is on node "([^"]*)"$`, func(listing, node string) error {
		return w.feed(w.listing, typeListingCreated, listing, fmt.Sprintf(`{"listingId":%q,"catalogItemId":"c-1","nodeId":%q}`, listing, node))
	})
	sc.Step(`^node "([^"]*)" is suspended by node-topology$`, func(node string) error {
		return w.feed(w.nodes, typeNodeSuspended, node, fmt.Sprintf(`{"nodeId":%q}`, node))
	})
	sc.Step(`^order-sourcing releases assignment "([^"]*)" to node "([^"]*)" with required ship-by "([^"]*)" for (\d+) lines?$`, func(asg, node, shipBy string, lines int) error {
		return w.feed(w.sourced, typeReleased, asg, releaseData(asg, node, shipBy, lines))
	})
	sc.Step(`^order-sourcing delivers the release of assignment "([^"]*)" to node "([^"]*)" twice$`, func(asg, node string) error {
		msg := cloudEvent("evt-dup", typeReleased, asg, releaseData(asg, node, "2026-10-22T21:00:00Z", 2))
		for range 2 {
			if err := w.sourced.HandleMessage(context.Background(), msg); err != nil {
				return err
			}
		}
		return nil
	})
	sc.Step(`^order-sourcing cancels assignment "([^"]*)"$`, func(asg string) error {
		return w.feed(w.sourced, typeCancelled, asg, fmt.Sprintf(`{"assignmentId":%q,"orderId":"ord-%s","reason":"customer cancelled"}`, asg, asg))
	})
	sc.Step(`^node-topology answers RegisterNode with node "([^"]*)" for vendor "([^"]*)"$`, func(node, vendor string) error {
		w.nt.Registered = gateway.RegisteredNode{NodeID: node, NodeType: "FULFILLMENT_CENTER", VendorID: vendor, VendorNodeCode: "SITE-2", Status: "PROPOSED"}
		return nil
	})
	sc.Step(`^node-topology refuses with "([^"]*)"$`, func(code string) error { w.nt.Err = refusal(code); return nil })
	sc.Step(`^offer-availability refuses with "([^"]*)"$`, func(code string) error { w.offers.Err = refusal(code); return nil })
	sc.Step(`^shipping-and-tracking cannot be reached when issuing package (\d+)$`, func(n int) error {
		w.ship.Err, w.ship.FailPackage = errors.New("connection refused"), n
		return nil
	})
	sc.Step(`^shipping-and-tracking is reachable again$`, func() error { w.ship.Err = nil; return nil })
	sc.Step(`^vendor "([^"]*)" has accepted the purchase order of "([^"]*)"$`, func(vendor, asg string) error {
		body := fmt.Sprintf(`{"vendorId":%q,"poNumber":"{po:%s}","decision":"ACCEPT","lines":[{"lineNo":1,"quantity":2},{"lineNo":2,"quantity":1}]}`, vendor, asg)
		if err := w.write(http.MethodPost, "/vendor/acknowledgements", body); err != nil || w.status != http.StatusAccepted {
			return fmt.Errorf("acknowledge: %v %d %s", err, w.status, w.body)
		}
		_, err := w.svc.ProcessDueSubmissions(context.Background())
		return err
	})
}

func releaseData(asg, node, shipBy string, lines int) string {
	var items []string
	for i := 1; i <= lines; i++ {
		items = append(items, fmt.Sprintf(`{"listingId":"lst-%d","quantity":%d}`, i, 3-i))
	}
	return fmt.Sprintf(`{"assignmentId":%q,"releaseNo":1,"orderId":"ord-%s","groupNo":1,"nodeId":%q,"requiredShipBy":%q,"tier":"NEXT_DAY","lines":[%s]}`,
		asg, asg, node, shipBy, strings.Join(items, ","))
}

func registerWhen(sc *godog.ScenarioContext, w *world) {
	sc.Step(`^I send GET "([^"]*)"$`, func(path string) error { return w.request(http.MethodGet, w.expand(path), "") })
	sc.Step(`^I send (POST|PUT) "([^"]*)"$`, func(method, path string) error { return w.write(method, w.expand(path), "") })
	sc.Step(`^I send (POST|PUT) "([^"]*)" with:$`, func(method, path string, body *godog.DocString) error {
		return w.write(method, w.expand(path), body.Content)
	})
	sc.Step(`^the submission queue is processed$`, func() error {
		_, err := w.svc.ProcessDueSubmissions(context.Background())
		return err
	})
	sc.Step(`^(\d+) (seconds|minutes|hours) pass$`, func(n int, unit string) error {
		d := map[string]time.Duration{"seconds": time.Second, "minutes": time.Minute, "hours": time.Hour}[unit]
		w.clock.Advance(time.Duration(n) * d)
		return nil
	})
	sc.Step(`^the expiry sweep runs$`, func() error {
		_, err := w.svc.ExpireUnacknowledged(context.Background())
		return err
	})
}

func registerThen(sc *godog.ScenarioContext, w *world) {
	registerThenResponse(sc, w)
	registerThenBody(sc, w)
	registerThenState(sc, w)
	registerThenCalls(sc, w)
}

// registerThenResponse holds the assertions on the last HTTP response.
func registerThenResponse(sc *godog.ScenarioContext, w *world) {
	sc.Step(`^the response status is (\d+)$`, func(want int) error {
		if w.status != want {
			return fmt.Errorf("status = %d, want %d: %s", w.status, want, w.body)
		}
		return nil
	})
	sc.Step(`^the problem type is "([^"]*)"$`, func(slug string) error {
		got, ok, err := w.field("type")
		if err != nil || !ok || got != slug || !strings.HasPrefix(w.header.Get("Content-Type"), "application/problem+json") {
			return fmt.Errorf("problem = %s (content-type %q), want type %q", w.body, w.header.Get("Content-Type"), slug)
		}
		return nil
	})
	sc.Step(`^the response field "([^"]*)" is "([^"]*)"$`, func(path, want string) error {
		want = w.expand(want)
		got, ok, err := w.field(path)
		if err != nil || !ok || render(got) != want {
			return fmt.Errorf("%s = %v (present %v), want %q in %s", path, got, ok, want, w.body)
		}
		return nil
	})
	sc.Step(`^the response field "([^"]*)" is absent$`, func(path string) error {
		if _, ok, err := w.field(path); err != nil || ok {
			return fmt.Errorf("%s is present in %s", path, w.body)
		}
		return nil
	})
}

// registerThenBody holds the assertions on the shape of the last response body.
func registerThenBody(sc *godog.ScenarioContext, w *world) {
	sc.Step(`^the response lists (\d+) items in "([^"]*)"$`, func(n int, path string) error {
		got, ok, err := w.field(path)
		list, isList := got.([]any)
		if err != nil || !ok || !isList || len(list) != n {
			return fmt.Errorf("%s = %v, want %d items in %s", path, got, n, w.body)
		}
		return nil
	})
	sc.Step(`^the response never mentions "([^"]*)"$`, func(text string) error {
		if strings.Contains(string(w.body), text) {
			return fmt.Errorf("the response mentions %q: %s", text, w.body)
		}
		return nil
	})
}

// registerThenState holds the assertions on the stored aggregates and the outbox.
func registerThenState(sc *godog.ScenarioContext, w *world) {
	sc.Step(`^the purchase order of "([^"]*)" is in state "([^"]*)"$`, func(asg, state string) error {
		p, err := w.store.Orders().Get(context.Background(), purchaseorder.Number(poNumber(asg)))
		if err != nil {
			return err
		}
		if string(p.State()) != state {
			return fmt.Errorf("state = %s, want %s", p.State(), state)
		}
		return nil
	})
	sc.Step(`^there are (\d+) purchase orders$`, func(n int) error {
		if got := w.store.Counts().Orders; got != n {
			return fmt.Errorf("orders = %d, want %d", got, n)
		}
		return nil
	})
	sc.Step(`^the purchase order of "([^"]*)" must be acknowledged by "([^"]*)"$`, func(asg, ts string) error {
		p, err := w.store.Orders().Get(context.Background(), purchaseorder.Number(poNumber(asg)))
		if err != nil {
			return err
		}
		if got := p.AckBy().UTC().Format(time.RFC3339); got != ts {
			return fmt.Errorf("ackBy = %s, want %s", got, ts)
		}
		return nil
	})
	sc.Step(`^the outbox event types are "([^"]*)"$`, func(want string) error {
		got := strings.Join(w.eventNames(), ", ")
		if got != want {
			return fmt.Errorf("events = %q, want %q", got, want)
		}
		return nil
	})
}

// registerThenCalls holds the assertions on the synchronous calls made to neighbours.
func registerThenCalls(sc *godog.ScenarioContext, w *world) {
	sc.Step(`^shipping-and-tracking received (\d+) IssueLabel calls?$`, func(n int) error {
		if len(w.ship.Calls) != n {
			return fmt.Errorf("IssueLabel calls = %d, want %d", len(w.ship.Calls), n)
		}
		return nil
	})
	sc.Step(`^the IssueLabel call for package (\d+) carried order "([^"]*)" and tier "([^"]*)"$`, func(pkg int, order, tier string) error {
		for _, c := range w.ship.Calls {
			if c.PackageNo == pkg {
				if c.OrderID != order || c.Tier != tier {
					return fmt.Errorf("call = %+v", c)
				}
				return nil
			}
		}
		return fmt.Errorf("no IssueLabel call for package %d", pkg)
	})
	sc.Step(`^node-topology received (\d+) RegisterNode calls?$`, func(n int) error {
		if len(w.nt.Registers) != n {
			return fmt.Errorf("RegisterNode calls = %d, want %d", len(w.nt.Registers), n)
		}
		return nil
	})
	sc.Step(`^offer-availability received (\d+) inventory feeds?$`, func(n int) error {
		if len(w.offers.Feeds) != n {
			return fmt.Errorf("feeds = %d, want %d", len(w.offers.Feeds), n)
		}
		return nil
	})
}
