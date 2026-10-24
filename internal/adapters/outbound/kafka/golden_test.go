package kafka

import (
	"fmt"
	"testing"
)

// goldenEnvelope renders the exact CloudEvents 1.0 structured JSON the encoder
// must produce for one event; only the data object differs per event.
func goldenEnvelope(entity, event, subject, stream, data string) string {
	return fmt.Sprintf(`{"specversion":"1.0","id":%q,"source":"/retailnetwork/vendor-connectivity",`+
		`"type":"com.retailnetwork.partner.vendor-connectivity.%s.%s","subject":%q,`+
		`"datacontenttype":"application/json","dataschema":"urn:retailnetwork:vendor-connectivity:%s:%s:v1",`+
		`"time":"2026-10-22T13:00:00Z","data":%s}`, goldenID, entity, event, subject, stream, event, data)
}

// TestEncoder_GoldenWireFormatOfEveryPublishedEvent pins the exact bytes of
// every published event, in the order of published(), with its topic and key.
func TestEncoder_GoldenWireFormatOfEveryPublishedEvent(t *testing.T) {
	const po = "PO-asg-1-1"
	cases := []struct {
		entity, event, key, stream, topic, data string
	}{
		{"vendoraccount", "VendorOnboarded", "V-1", "events", TopicEvents,
			`{"vendorId":"V-1","name":"Carrier A","ackWindowMinutes":120}`},
		{"vendoraccount", "VendorSuspended", "V-1", "events", TopicEvents,
			`{"vendorId":"V-1","suspendedAt":"2026-10-22T13:00:00Z","reason":"late acknowledgements"}`},
		{"vendoraccount", "VendorReinstated", "V-1", "events", TopicEvents,
			`{"vendorId":"V-1","reinstatedAt":"2026-10-22T13:00:00Z"}`},
		{"purchaseorder", "PurchaseOrderIssued", po, "events", TopicEvents,
			`{"poNumber":"PO-asg-1-1","vendorId":"V-1","nodeId":"n-1","assignmentId":"asg-1","releaseNo":1,` +
				`"requiredShipBy":"2026-10-24T13:00:00Z","ackBy":"2026-10-22T15:00:00Z","lines":[{"lineNo":1,"listingId":"lst-1","quantity":2}]}`},
		{"purchaseorder", "PurchaseOrderAccepted", po, "events", TopicEvents,
			`{"poNumber":"PO-asg-1-1","assignmentId":"asg-1","releaseNo":1,"nodeId":"n-1","acceptedAt":"2026-10-22T13:00:00Z","transactionId":"tx-1"}`},
		{"purchaseorder", "PurchaseOrderRejected", po, "events", TopicEvents,
			`{"poNumber":"PO-asg-1-1","assignmentId":"asg-1","releaseNo":1,"nodeId":"n-1","reasonCode":"OUT_OF_STOCK","rejectedAt":"2026-10-22T13:00:00Z"}`},
		{"purchaseorder", "PurchaseOrderExpired", po, "events", TopicEvents,
			`{"poNumber":"PO-asg-1-1","assignmentId":"asg-1","releaseNo":1,"nodeId":"n-1","ackBy":"2026-10-22T13:00:00Z"}`},
		{"purchaseorder", "PurchaseOrderCancelled", po, "analytics", TopicAnalytics,
			`{"poNumber":"PO-asg-1-1","assignmentId":"asg-1","reason":"customer cancelled"}`},
		{"purchaseorder", "VendorShipmentConfirmed", po, "events", TopicEvents,
			`{"poNumber":"PO-asg-1-1","assignmentId":"asg-1","nodeId":"n-1","trackingNumbers":["TRK-1"],"shippedAt":"2026-10-22T13:00:00Z","requiredShipBy":"2026-10-24T13:00:00Z"}`},
		{"submission", "SubmissionProcessed", "tx-1", "events", TopicEvents,
			`{"transactionId":"tx-1","vendorId":"V-1","kind":"ACKNOWLEDGEMENT","status":"SUCCESS","errorCount":0}`},
		{"inboundreceipt", "InboundReceiptSubmitted", "SHP-1", "events", TopicEvents,
			`{"transactionId":"tx-1","vendorId":"V-1","nodeId":"n-1","shipmentId":"SHP-1","receivedAt":"2026-10-22T12:00:00Z","cartons":[` +
				`{"sscc":"006141410000000012","status":"RECEIVED","items":[{"gtin":"00614141000036","qtyEaches":12}],"complianceCodes":[]},` +
				`{"sscc":"006141410000000029","status":"MISSING","items":[],"complianceCodes":["LABEL_UNREADABLE"]}]}`},
	}
	events := published()
	if len(events) != len(cases) {
		t.Fatalf("published() has %d events, the golden table %d", len(events), len(cases))
	}
	for i, c := range cases {
		t.Run(c.event, func(t *testing.T) {
			msgs, err := encoder().Encode(events[i])
			if err != nil || len(msgs) != 1 {
				t.Fatalf("Encode: %v %d", err, len(msgs))
			}
			want := goldenEnvelope(c.entity, c.event, c.key, c.stream, c.data)
			if got := string(msgs[0].Value); got != want {
				t.Errorf("value =\n%s\nwant\n%s", got, want)
			}
			if msgs[0].Topic != c.topic || string(msgs[0].Key) != c.key {
				t.Errorf("topic/key = %s/%s, want %s/%s", msgs[0].Topic, msgs[0].Key, c.topic, c.key)
			}
		})
	}
}
