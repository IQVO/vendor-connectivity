package kafka

import (
	"encoding/json"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/claudioed/vendor-connectivity/internal/adapters/kafka/cloudevents"
	"github.com/claudioed/vendor-connectivity/internal/domain/purchaseorder"
	"github.com/claudioed/vendor-connectivity/internal/domain/shared"
	"github.com/claudioed/vendor-connectivity/internal/domain/submission"
	"github.com/claudioed/vendor-connectivity/internal/domain/vendor"
)

const goldenID = "1b0c9a4e-2f7d-4a63-9d1e-5a7c3e2b9f10"

var (
	t0    = time.Date(2026, 10, 22, 13, 0, 0, 0, time.UTC)
	later = t0.Add(48 * time.Hour)
)

func hdr() shared.Header { return shared.Header{Version: 2, At: t0} }

// published is one instance of every event this service publishes.
func published() []shared.Event {
	return []shared.Event{
		vendor.VendorOnboarded{Header: hdr(), VendorID: "V-1", Name: "Carrier A", AckWindowMinutes: 120},
		vendor.VendorSuspended{Header: hdr(), VendorID: "V-1", Reason: "late acknowledgements"},
		vendor.VendorReinstated{Header: hdr(), VendorID: "V-1"},
		purchaseorder.PurchaseOrderIssued{Header: hdr(), PONumber: "PO-asg-1-1", VendorID: "V-1", NodeID: "n-1", AssignmentID: "asg-1", ReleaseNo: 1,
			RequiredShipBy: later, AckBy: t0.Add(2 * time.Hour), Lines: []purchaseorder.Line{{LineNo: 1, ListingID: "lst-1", Quantity: 2}}},
		purchaseorder.PurchaseOrderAccepted{Header: hdr(), PONumber: "PO-asg-1-1", AssignmentID: "asg-1", ReleaseNo: 1, NodeID: "n-1", AcceptedAt: t0, TransactionID: "tx-1"},
		purchaseorder.PurchaseOrderRejected{Header: hdr(), PONumber: "PO-asg-1-1", AssignmentID: "asg-1", ReleaseNo: 1, NodeID: "n-1", ReasonCode: "OUT_OF_STOCK", RejectedAt: t0},
		purchaseorder.PurchaseOrderExpired{Header: hdr(), PONumber: "PO-asg-1-1", AssignmentID: "asg-1", ReleaseNo: 1, NodeID: "n-1", AckBy: t0},
		purchaseorder.PurchaseOrderCancelled{Header: hdr(), PONumber: "PO-asg-1-1", AssignmentID: "asg-1", Reason: "customer cancelled"},
		purchaseorder.VendorShipmentConfirmed{Header: hdr(), PONumber: "PO-asg-1-1", AssignmentID: "asg-1", NodeID: "n-1",
			TrackingNumbers: []string{"TRK-1"}, ShippedAt: t0, RequiredShipBy: later},
		submission.SubmissionProcessed{Header: hdr(), TransactionID: "tx-1", VendorID: "V-1", Kind: submission.KindAcknowledgement, Status: submission.Success, ErrorCount: 0},
	}
}

func encoder() *Encoder { return &Encoder{NewID: func() string { return goldenID }} }

func TestEncoder_GoldenWireFormat(t *testing.T) {
	msgs, err := encoder().Encode(vendor.VendorOnboarded{Header: hdr(), VendorID: "V-1", Name: "Carrier A", AckWindowMinutes: 120})
	if err != nil || len(msgs) != 1 {
		t.Fatalf("Encode: %v %d", err, len(msgs))
	}
	want := `{"specversion":"1.0","id":"` + goldenID + `","source":"/retailnetwork/vendor-connectivity",` +
		`"type":"com.retailnetwork.partner.vendor-connectivity.vendoraccount.VendorOnboarded","subject":"V-1",` +
		`"datacontenttype":"application/json","dataschema":"urn:retailnetwork:vendor-connectivity:events:VendorOnboarded:v1",` +
		`"time":"2026-10-22T13:00:00Z","data":{"vendorId":"V-1","name":"Carrier A","ackWindowMinutes":120}}`
	m := msgs[0]
	if string(m.Value) != want {
		t.Fatalf("value =\n%s\nwant\n%s", m.Value, want)
	}
	if m.EventID != goldenID || m.Topic != TopicEvents || string(m.Key) != "V-1" || m.Subject != "V-1" ||
		m.EventType != "com.retailnetwork.partner.vendor-connectivity.vendoraccount.VendorOnboarded" ||
		m.DataSchema != "urn:retailnetwork:vendor-connectivity:events:VendorOnboarded:v1" ||
		len(m.Headers) != 1 || m.Headers[0].Key != "content-type" || m.Headers[0].Value != cloudevents.MediaType {
		t.Fatalf("row = %+v", m)
	}
}

func TestEncoder_OnlyCancellationGoesToTheAnalyticsStream(t *testing.T) {
	msgs, err := encoder().Encode(published()...)
	if err != nil || len(msgs) != len(published()) {
		t.Fatalf("Encode: %v %d", err, len(msgs))
	}
	for _, m := range msgs {
		analytics := strings.HasSuffix(m.EventType, ".PurchaseOrderCancelled")
		if analytics != (m.Topic == TopicAnalytics) || analytics != strings.Contains(m.DataSchema, ":analytics:") {
			t.Errorf("%s: topic %s schema %s", m.EventType, m.Topic, m.DataSchema)
		}
	}
}

func TestEncoder_MintsAFreshUUIDPerEvent(t *testing.T) {
	msgs, err := NewEncoder().Encode(published()[:2]...)
	if err != nil || msgs[0].EventID == msgs[1].EventID || len(msgs[0].EventID) != 36 {
		t.Fatalf("ids = %v %v", msgs, err)
	}
	msgs, err = (&Encoder{}).Encode(published()[:1]...)
	if err != nil || len(msgs[0].EventID) != 36 {
		t.Fatalf("a zero Encoder falls back to uuid: %v", err)
	}
	if empty, err := encoder().Encode(); err != nil || len(empty) != 0 {
		t.Fatalf("no events: %v %v", empty, err)
	}
}

type unknown struct{ shared.Header }

func (unknown) EventName() string { return "Unknown" }
func (unknown) Entity() string    { return "x" }
func (unknown) SubjectID() string { return "s" }

func TestEncoder_RejectsAnEventItDoesNotPublish(t *testing.T) {
	if _, err := encoder().Encode(unknown{}); err == nil || !strings.Contains(err.Error(), "not a published event") {
		t.Fatalf("err = %v", err)
	}
	if _, err := encoder().Encode(vendor.VendorOnboarded{Header: hdr()}); err == nil {
		t.Fatal("an empty subject cannot be a CloudEvent")
	}
}

// TestEncodedEventsMatchTheAsyncAPI checks every published type against
// apis/asyncapi.yaml: the channel/topic, the type, the dataschema and the
// data properties (required present, nothing undeclared).
func TestEncodedEventsMatchTheAsyncAPI(t *testing.T) {
	raw, err := os.ReadFile("../../../../apis/asyncapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Channels   map[string]struct{ Address string }
		Components struct{ Schemas map[string]yaml.Node }
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	topics := map[string]bool{}
	for _, c := range doc.Channels {
		topics[c.Address] = true
	}
	msgs, err := encoder().Encode(published()...)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range msgs {
		if !topics[m.Topic] {
			t.Errorf("%s: topic %s is not a channel of the AsyncAPI", m.EventType, m.Topic)
		}
		name := m.EventType[strings.LastIndex(m.EventType, ".")+1:]
		node, ok := doc.Components.Schemas[name+"Event"]
		if !ok {
			t.Errorf("%s: no schema %sEvent", name, name)
			continue
		}
		checkData(t, name, m.Value, &node)
	}
}

// checkData asserts the required properties are present and none is undeclared.
func checkData(t *testing.T, name string, value []byte, node *yaml.Node) {
	t.Helper()
	props, required := dataSchema(t, node)
	var env struct{ Data map[string]json.RawMessage }
	if err := json.Unmarshal(value, &env); err != nil {
		t.Fatal(err)
	}
	for _, r := range required {
		if _, ok := env.Data[r]; !ok {
			t.Errorf("%s: required property %s is missing", name, r)
		}
	}
	for k := range env.Data {
		if !props[k] {
			t.Errorf("%s: property %s is not declared", name, k)
		}
	}
}

// dataSchema returns the declared and required properties of the `data` object
// of an event schema (allOf envelope + data).
func dataSchema(t *testing.T, n *yaml.Node) (map[string]bool, []string) {
	t.Helper()
	var s struct {
		AllOf []struct {
			Properties map[string]struct {
				Required   []string
				Properties map[string]yaml.Node
			}
		} `yaml:"allOf"`
	}
	if err := n.Decode(&s); err != nil {
		t.Fatal(err)
	}
	for _, part := range s.AllOf {
		if d, ok := part.Properties["data"]; ok {
			props := map[string]bool{}
			for k := range d.Properties {
				props[k] = true
			}
			sort.Strings(d.Required)
			return props, d.Required
		}
	}
	t.Fatal("no data property in schema")
	return nil, nil
}
