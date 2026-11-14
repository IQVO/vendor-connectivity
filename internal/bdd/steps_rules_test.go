package bdd_test

// Step vocabulary of the documentation-derived scenarios (features/*_rules.feature):
// every documented rule, policy and failure mode of the context that the
// openapi-driven suite does not pin. Everything reuses the world and the doubles
// the suite already wires; new Givens that change a knob rebuild the service, so
// they must come before the vendor is onboarded (the *_rules.feature files that
// use them have no Background).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/cucumber/godog"

	inboundhttp "github.com/claudioed/vendor-connectivity/internal/adapters/inbound/http"
	inboundkafka "github.com/claudioed/vendor-connectivity/internal/adapters/inbound/kafka"
	"github.com/claudioed/vendor-connectivity/internal/domain/purchaseorder"
)

// defaultShipBy is the required ship-by of every release these steps fabricate:
// with a 120 minute window and a 60 minute handling time the ackBy is 15:00.
const defaultShipBy = "2026-10-22T21:00:00Z"

// ackBody is a whole-order acknowledgement of a two-line release (quantities 2 and 1).
func ackBody(vendor, asg, decision, reason string) string {
	r := ""
	if reason != "" {
		r = fmt.Sprintf(`,"reasonCode":%q`, reason)
	}
	return fmt.Sprintf(`{"vendorId":%q,"poNumber":"{po:%s}","decision":%q%s,"lines":[{"lineNo":1,"quantity":2},{"lineNo":2,"quantity":1}]}`, vendor, asg, decision, r)
}

func (w *world) submitAck(vendor, asg, decision, reason string) error {
	if err := w.write(http.MethodPost, "/vendor/acknowledgements", ackBody(vendor, asg, decision, reason)); err != nil || w.status != http.StatusAccepted {
		return fmt.Errorf("acknowledge: %v %d %s", err, w.status, w.body)
	}
	return nil
}

func (w *world) processQueue() error {
	_, err := w.svc.ProcessDueSubmissions(context.Background())
	return err
}

func labelBody(vendor, asg string, n int) string {
	pkgs := make([]string, n)
	for i := range pkgs {
		pkgs[i] = fmt.Sprintf(`{"packageNo":%d}`, i+1)
	}
	return fmt.Sprintf(`{"vendorId":%q,"poNumber":"{po:%s}","packages":[%s]}`, vendor, asg, strings.Join(pkgs, ","))
}

func (w *world) requestLabels(vendor, asg string, n int) error {
	return w.write(http.MethodPost, "/vendor/shipping-labels", labelBody(vendor, asg, n))
}

// requireLabels requests labels and fails unless they were issued, remembering their tracking numbers.
func (w *world) requireLabels(vendor, asg string, n int) error {
	if err := w.requestLabels(vendor, asg, n); err != nil || w.status != http.StatusOK {
		return fmt.Errorf("labels: %v %d %s", err, w.status, w.body)
	}
	return w.rememberTrackingNumbers()
}

// trackingList is the JSON list of every remembered tracking number, by package.
func (w *world) trackingList() string {
	pkgs := make([]int, 0, len(w.lastLabels))
	for p := range w.lastLabels {
		pkgs = append(pkgs, p)
	}
	sort.Ints(pkgs)
	out := make([]string, len(pkgs))
	for i, p := range pkgs {
		out[i] = strconv.Quote(w.lastLabels[p])
	}
	return "[" + strings.Join(out, ",") + "]"
}

func (w *world) confirmShipment(vendor, asg string) error {
	body := fmt.Sprintf(`{"vendorId":%q,"poNumber":"{po:%s}","shippedAt":"2026-10-22T14:00:00Z","trackingNumbers":%s}`, vendor, asg, w.trackingList())
	if err := w.write(http.MethodPost, "/vendor/shipment-confirmations", body); err != nil || w.status != http.StatusAccepted {
		return fmt.Errorf("confirm shipment: %v %d %s", err, w.status, w.body)
	}
	return nil
}

func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// unescape lets a feature file spell a tab as \t inside a quoted reason.
func unescape(s string) string { return strings.ReplaceAll(s, `\t`, "\t") }

func cancelData(asg, reason string) string {
	return fmt.Sprintf(`{"assignmentId":%q,"orderId":"ord-%s","reason":%s}`, asg, asg, jsonString(reason))
}

func withdrawData(asg string, release int, reason string) string {
	return fmt.Sprintf(`{"assignmentId":%q,"releaseNo":%d,"orderId":"ord-%s","groupNo":1,"reason":%s,"withdrawnAt":"2026-10-22T14:00:00Z"}`, asg, release, asg, jsonString(reason))
}

func (w *world) order(asg string, release int) (*purchaseorder.PurchaseOrder, error) {
	return w.store.Orders().Get(context.Background(), purchaseorder.NumberFor(asg, release))
}

// reach drives the order of the assignment's first release to a lifecycle state
// through the same commands the vendor and the policies use.
func (w *world) reach(asg, state string) error {
	p, err := w.order(asg, 1)
	if err != nil {
		return err
	}
	vendor := p.VendorID()
	switch state {
	case "ISSUED":
		return nil
	case "ACK_PROCESSING":
		return w.submitAck(vendor, asg, "ACCEPT", "")
	case "ACCEPTED", "REJECTED":
		decision, reason := "ACCEPT", ""
		if state == "REJECTED" {
			decision, reason = "REJECT", "OUT_OF_STOCK"
		}
		if err := w.submitAck(vendor, asg, decision, reason); err != nil {
			return err
		}
		return w.processQueue()
	case "EXPIRED":
		w.clock.Advance(3 * time.Hour)
		_, err := w.svc.ExpireUnacknowledged(context.Background())
		return err
	case "CANCELLED":
		return w.feed(w.sourced, typeCancelled, asg, cancelData(asg, "customer cancelled"))
	case "SHIPMENT_PROCESSING", "SHIPPED":
		if err := w.reach(asg, "ACCEPTED"); err != nil {
			return err
		}
		if err := w.requireLabels(vendor, asg, 1); err != nil {
			return err
		}
		if err := w.confirmShipment(vendor, asg); err != nil {
			return err
		}
		if state == "SHIPPED" {
			return w.processQueue()
		}
		return nil
	}
	return fmt.Errorf("unknown purchase order state %q", state)
}

func (w *world) registerNode(node, operator, vendor string) error {
	reg := fmt.Sprintf(`{"nodeId":%q,"nodeType":"FULFILLMENT_CENTER","subtype":"SORTABLE","operator":%q,"vendorId":%q,"vendorNodeCode":"code-%s","regionId":"r-1","timezone":"America/Sao_Paulo"}`, node, operator, vendor, node)
	if err := w.feed(w.nodes, typeNodeRegistered, node, reg); err != nil {
		return err
	}
	return w.feed(w.nodes, typeNodeActivated, node, fmt.Sprintf(`{"nodeId":%q}`, node))
}

func registerRules(sc *godog.ScenarioContext, w *world) {
	registerRulesGiven(sc, w)
	registerRulesWhen(sc, w)
	registerRulesThen(sc, w)
}

// registerRulesGiven holds the set-up steps. Those that change a knob or the limiter rebuild the service.
func registerRulesGiven(sc *godog.ScenarioContext, w *world) {
	sc.Step(`^the Vendor API allows (\d+(?:\.\d+)?) requests? per second$`, func(rps string) error {
		v, err := strconv.ParseFloat(rps, 64)
		if err != nil {
			return err
		}
		w.limiter = inboundhttp.NewLimiter(v)
		w.build()
		return nil
	})
	sc.Step(`^the next transactions end as follows: "([^"]*)"$`, func(script string) error {
		var values []float64
		for _, tok := range strings.Split(script, ",") {
			switch strings.TrimSpace(tok) {
			case "ok":
				values = append(values, 0.9)
			case "fail":
				values = append(values, 0.0)
			default:
				return fmt.Errorf("unknown outcome %q: use ok or fail", tok)
			}
		}
		w.cfg.Knobs.TxFailureRate = 0.5
		w.random.Values = values
		w.build()
		return nil
	})
	sc.Step(`^node "([^"]*)" of vendor "([^"]*)" has no declared profile$`, func(node, vendor string) error { return w.registerNode(node, "VENDOR", vendor) })
	sc.Step(`^node "([^"]*)" is operated by the network$`, func(node string) error { return w.registerNode(node, "NETWORK", "") })
	sc.Step(`^node-topology declares profile revision (\d+) of node "([^"]*)" with a handling time of (\d+) minutes$`, func(rev int, node string, minutes int) error {
		return w.feed(w.nodes, typeProfileDeclared, node, fmt.Sprintf(`{"nodeId":%q,"profileRevision":%d,"handlingTimeMinutes":%d}`, node, rev, minutes))
	})
	sc.Step(`^node-topology activates node "([^"]*)"$`, func(node string) error {
		return w.feed(w.nodes, typeNodeActivated, node, fmt.Sprintf(`{"nodeId":%q}`, node))
	})
	sc.Step(`^node-topology retires node "([^"]*)"$`, func(node string) error {
		return w.feed(w.nodes, inboundkafka.TypeNodeRetired, node, fmt.Sprintf(`{"nodeId":%q}`, node))
	})
	sc.Step(`^node-topology cannot be reached$`, func() error { w.nt.Err = errors.New("connection refused"); return nil })
	sc.Step(`^node-topology is reachable again$`, func() error { w.nt.Err = nil; return nil })
	sc.Step(`^offer-availability cannot be reached$`, func() error { w.offers.Err = errors.New("connection refused"); return nil })
	sc.Step(`^offer-availability is reachable again$`, func() error { w.offers.Err = nil; return nil })
	sc.Step(`^shipping-and-tracking refuses with "([^"]*)"$`, func(code string) error {
		w.ship.Err, w.ship.FailPackage = refusal(code), 0
		return nil
	})
	sc.Step(`^the purchase order of "([^"]*)" has reached state "([A-Z_]+)"$`, w.reach)
	sc.Step(`^vendor "([^"]*)" has rejected the purchase order of "([^"]*)" with reason "([^"]*)"$`, func(vendor, asg, reason string) error {
		if err := w.submitAck(vendor, asg, "REJECT", reason); err != nil {
			return err
		}
		return w.processQueue()
	})
	sc.Step(`^vendor "([^"]*)" has acknowledged the purchase order of "([^"]*)" and its transaction is still processing$`, func(vendor, asg string) error {
		return w.submitAck(vendor, asg, "ACCEPT", "")
	})
	sc.Step(`^vendor "([^"]*)" has requested labels for (\d+) packages? of the purchase order of "([^"]*)"$`, func(vendor string, n int, asg string) error {
		return w.requireLabels(vendor, asg, n)
	})
	sc.Step(`^vendor "([^"]*)" has confirmed the shipment of the purchase order of "([^"]*)"$`, w.confirmShipment)
	sc.Step(`^offer-availability retires the "([^"]*)" level with gtin "([^"]*)" for catalog item "([^"]*)" at "([^"]*)"$`, func(level, gtin, item, at string) error {
		return w.feed(w.packagingConsumer(), inboundkafka.TypePackLevelRetired, item,
			fmt.Sprintf(`{"catalogItemId":%q,"level":%q,"gtin":%q,"retiredAt":%q}`, item, level, gtin, at))
	})
}

// registerRulesWhen holds the actions: releases, cancellations, clocks and sweeps.
func registerRulesWhen(sc *godog.ScenarioContext, w *world) {
	// pending is the release event the consumer could not place: the Kafka loop redelivers the SAME message.
	var pending []byte
	sc.Step(`^order-sourcing releases release (\d+) of assignment "([^"]*)" to node "([^"]*)" with required ship-by "([^"]*)" for (\d+) lines?$`,
		func(release int, asg, node, shipBy string, lines int) error {
			data := strings.Replace(releaseData(asg, node, shipBy, lines), `"releaseNo":1`, fmt.Sprintf(`"releaseNo":%d`, release), 1)
			return w.feed(w.sourced, typeReleased, asg, data)
		})
	sc.Step(`^order-sourcing delivers the release of assignment "([^"]*)" to node "([^"]*)" again under another event id$`, func(asg, node string) error {
		return w.feed(w.sourced, typeReleased, asg, releaseData(asg, node, defaultShipBy, 2))
	})
	sc.Step(`^order-sourcing delivers the release of assignment "([^"]*)" to node "([^"]*)" but the consumer cannot place it yet$`, func(asg, node string) error {
		pending = cloudEvent("evt-race", typeReleased, asg, releaseData(asg, node, defaultShipBy, 2))
		if err := w.sourced.HandleMessage(context.Background(), pending); err == nil {
			return errors.New("the consumer acknowledged a release for a node it does not know")
		}
		return nil
	})
	sc.Step(`^that release event is delivered again$`, func() error { return w.sourced.HandleMessage(context.Background(), pending) })
	sc.Step(`^order-sourcing cancels assignment "([^"]*)" because "([^"]*)"$`, func(asg, reason string) error {
		return w.feed(w.sourced, typeCancelled, asg, cancelData(asg, unescape(reason)))
	})
	sc.Step(`^order-sourcing cancels assignment "([^"]*)" with a reason of (\d+) characters$`, func(asg string, n int) error {
		return w.feed(w.sourced, typeCancelled, asg, cancelData(asg, strings.Repeat("a", n)))
	})
	sc.Step(`^order-sourcing withdraws release (\d+) of assignment "([^"]*)" because "([^"]*)"$`, func(release int, asg, reason string) error {
		return w.feed(w.sourced, typeWithdrawn, asg, withdrawData(asg, release, unescape(reason)))
	})
	sc.Step(`^order-sourcing withdraws release (\d+) of assignment "([^"]*)" with a reason of (\d+) characters$`, func(release int, asg string, n int) error {
		return w.feed(w.sourced, typeWithdrawn, asg, withdrawData(asg, release, strings.Repeat("a", n)))
	})
	sc.Step(`^the expiry sweep expires (\d+) orders?$`, func(want int) error {
		got, err := w.svc.ExpireUnacknowledged(context.Background())
		if err != nil {
			return err
		}
		if got != want {
			return fmt.Errorf("the sweep expired %d orders, want %d", got, want)
		}
		return nil
	})
	sc.Step(`^the submission queue is processed (\d+) times$`, func(n int) error {
		for range n {
			if err := w.processQueue(); err != nil {
				return err
			}
		}
		return nil
	})
	sc.Step(`^(\d+) days pass$`, func(n int) error { w.clock.Advance(time.Duration(n) * 24 * time.Hour); return nil })
	sc.Step(`^the retention sweep runs$`, func() error { _, err := w.svc.PurgeSubmissions(context.Background()); return err })
	sc.Step(`^I request labels for packages 1 to (\d+) of the purchase order of "([^"]*)" for vendor "([^"]*)"$`, func(n int, asg, vendor string) error {
		return w.requestLabels(vendor, asg, n)
	})
}

// registerRulesThen holds the assertions on states, counts, calls and event payloads.
func registerRulesThen(sc *godog.ScenarioContext, w *world) {
	sc.Step(`^release (\d+) of "([^"]*)" is in state "([^"]*)"$`, func(release int, asg, state string) error {
		p, err := w.order(asg, release)
		if err != nil {
			return err
		}
		if string(p.State()) != state {
			return fmt.Errorf("release %d of %s is %s, want %s", release, asg, p.State(), state)
		}
		return nil
	})
	sc.Step(`^there are (\d+) transactions?$`, func(n int) error {
		if got := w.store.Counts().Submissions; got != n {
			return fmt.Errorf("transactions = %d, want %d", got, n)
		}
		return nil
	})
	sc.Step(`^the response header "([^"]*)" is "([^"]*)"$`, func(name, want string) error {
		if got := w.header.Get(name); got != want {
			return fmt.Errorf("header %s = %q, want %q", name, got, want)
		}
		return nil
	})
	sc.Step(`^every call to (node-topology|offer-availability) carried the transaction id as its key$`, func(neighbour string) error {
		keys := w.nt.Keys
		if neighbour == "offer-availability" {
			keys = w.offers.FeedIDs
		}
		if len(keys) == 0 {
			return fmt.Errorf("%s received no call", neighbour)
		}
		for _, k := range keys {
			if k != w.lastTx {
				return fmt.Errorf("%s keys = %v, want every one to be %s", neighbour, keys, w.lastTx)
			}
		}
		return nil
	})
	sc.Step(`^shipping-and-tracking was asked for packages "([^"]*)"$`, func(want string) error {
		got := make([]string, len(w.ship.Calls))
		for i, c := range w.ship.Calls {
			got[i] = strconv.Itoa(c.PackageNo)
		}
		if strings.Join(got, ", ") != want {
			return fmt.Errorf("IssueLabel packages = %q, want %q", strings.Join(got, ", "), want)
		}
		return nil
	})
	sc.Step(`^the IssueLabel idempotency keys were "([^"]*)"$`, func(want string) error {
		if got := strings.Join(w.ship.Keys, ", "); got != w.expand(want) {
			return fmt.Errorf("IssueLabel keys = %q, want %q", got, w.expand(want))
		}
		return nil
	})
	sc.Step(`^the "([^"]*)" event about "([^"]*)" has a "([^"]*)" of (\d+) characters$`, func(name, subject, field string, n int) error {
		got, ok := w.eventField(name, w.expand(subject), field)
		if !ok {
			return fmt.Errorf("no %q event about %q carrying %q in %v", name, subject, field, w.eventNames())
		}
		if c := utf8.RuneCountInString(got); c != n {
			return fmt.Errorf("%s.%s has %d characters, want %d: %q", name, field, c, n, got)
		}
		return nil
	})
}
