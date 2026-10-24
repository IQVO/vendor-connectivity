package bdd_test

// Step vocabulary of the inbound-shipment and packaging scenarios. Events are
// handed to real consumers over the same service the HTTP server uses.

import (
	"fmt"
	"net/url"

	"github.com/cucumber/godog"

	inboundkafka "github.com/claudioed/vendor-connectivity/internal/adapters/inbound/kafka"
)

const (
	ssccCase  = "006141410000000012"
	ssccMixed = "006141410000000029"
	ssccPlt   = "006141410000000036"
)

func (w *world) inboundConsumer() *inboundkafka.Consumer {
	return &inboundkafka.Consumer{Name: "inbound", Topic: "inbound", Handlers: inboundkafka.InboundHandlers(w.svc), Logger: w.cfg.Logger}
}

func (w *world) packagingConsumer() *inboundkafka.Consumer {
	return &inboundkafka.Consumer{Name: "packaging", Topic: "packaging", Handlers: inboundkafka.PackagingHandlers(w.svc), Logger: w.cfg.Logger}
}

func (w *world) label(shipment, node string) error {
	data := fmt.Sprintf(`{"shipmentId":%q,"planId":"P-1","sellerId":"S-1","nodeId":%q,"labelledAt":"2026-10-22T12:00:00Z","cartons":[`+
		`{"sscc":%q,"parentSscc":%q,"kind":"HOMOGENEOUS_CASE","grossWeightG":4200,"contents":[{"catalogItemId":"c-1","gtin":"00614141000036","qtyEaches":12}]},`+
		`{"sscc":%q,"kind":"MIXED","grossWeightG":2100,"contents":[{"catalogItemId":"c-1","gtin":"00614141000036","qtyEaches":2}]}]}`,
		shipment, node, ssccCase, ssccPlt, ssccMixed)
	return w.feed(w.inboundConsumer(), inboundkafka.TypeShipmentLabelled, shipment, data)
}

func registerInbound(sc *godog.ScenarioContext, w *world) {
	sc.Step(`^seller-inbound ships shipment "([^"]*)" to node "([^"]*)"$`, func(shipment, node string) error {
		if err := w.label(shipment, node); err != nil {
			return err
		}
		at := w.clock.Now().Format("2006-01-02T15:04:05Z")
		return w.feed(w.inboundConsumer(), inboundkafka.TypeShipmentShipped, shipment,
			fmt.Sprintf(`{"shipmentId":%q,"planId":"P-1","sellerId":"S-1","nodeId":%q,"shippedAt":%q}`, shipment, node, at))
	})
	sc.Step(`^seller-inbound labels shipment "([^"]*)" for node "([^"]*)" without shipping it$`, w.label)
	sc.Step(`^seller-inbound closes shipment "([^"]*)" for node "([^"]*)"$`, func(shipment, node string) error {
		return w.feed(w.inboundConsumer(), inboundkafka.TypeShipmentReceived, shipment,
			fmt.Sprintf(`{"shipmentId":%q,"sellerId":"S-1","nodeId":%q,"status":"CLOSED","receivedAt":"2026-10-25T10:00:00Z"}`, shipment, node))
	})
	sc.Step(`^offer-availability declares a "([^"]*)" level with gtin "([^"]*)" of (\d+) eaches for catalog item "([^"]*)"$`, func(level, gtin string, units int, item string) error {
		return w.feed(w.packagingConsumer(), inboundkafka.TypePackagingDeclared, item,
			fmt.Sprintf(`{"catalogItemId":%q,"level":%q,"gtin":%q,"childGtin":"00614141000036","unitsPerPack":%d,"declaredAt":"2026-10-20T08:00:00Z"}`, item, level, gtin, units))
	})
	sc.Step(`^I poll inbound shipments again with the returned nextToken for vendor "([^"]*)"$`, func(vendor string) error {
		v, _, err := w.field("nextToken")
		if err != nil {
			return err
		}
		token, _ := v.(string)
		return w.request("GET", "/vendor/inbound-shipments?vendorId="+url.QueryEscape(vendor)+"&limit=1&nextToken="+url.QueryEscape(token), "")
	})
}
