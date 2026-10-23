package kafka

import (
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/claudioed/vendor-connectivity/internal/adapters/kafka/cloudevents"
	"github.com/claudioed/vendor-connectivity/internal/application/outbox"
	"github.com/claudioed/vendor-connectivity/internal/domain/purchaseorder"
	"github.com/claudioed/vendor-connectivity/internal/domain/shared"
	"github.com/claudioed/vendor-connectivity/internal/domain/submission"
	"github.com/claudioed/vendor-connectivity/internal/domain/vendor"
)

// Topics this service publishes to (apis/asyncapi.yaml). Only the cancellation
// event goes to the analytics stream (ADR 0004).
const (
	TopicEvents    = "retailnetwork.vendor-connectivity.events"
	TopicAnalytics = "retailnetwork.vendor-connectivity.analytics"
)

// schemaVersion is the dataschema version of every payload below.
const schemaVersion = 1

// Wire payloads (CloudEvents `data`, camelCase JSON as in the AsyncAPI).
// Optional fields are omitted when unset; timestamps are RFC 3339 UTC.

type vendorOnboardedData struct {
	VendorID         string `json:"vendorId"`
	Name             string `json:"name"`
	AckWindowMinutes int    `json:"ackWindowMinutes"`
}

type vendorSuspendedData struct {
	VendorID    string `json:"vendorId"`
	SuspendedAt string `json:"suspendedAt"`
	Reason      string `json:"reason,omitempty"`
}

type vendorReinstatedData struct {
	VendorID     string `json:"vendorId"`
	ReinstatedAt string `json:"reinstatedAt"`
}

type poLineData struct {
	LineNo    int    `json:"lineNo"`
	ListingID string `json:"listingId"`
	Quantity  int    `json:"quantity"`
}

type issuedData struct {
	PONumber       string       `json:"poNumber"`
	VendorID       string       `json:"vendorId"`
	NodeID         string       `json:"nodeId"`
	AssignmentID   string       `json:"assignmentId"`
	ReleaseNo      int          `json:"releaseNo"`
	RequiredShipBy string       `json:"requiredShipBy"`
	AckBy          string       `json:"ackBy"`
	Lines          []poLineData `json:"lines"`
}

type acceptedData struct {
	PONumber      string `json:"poNumber"`
	AssignmentID  string `json:"assignmentId"`
	ReleaseNo     int    `json:"releaseNo"`
	NodeID        string `json:"nodeId"`
	AcceptedAt    string `json:"acceptedAt"`
	TransactionID string `json:"transactionId"`
}

type rejectedData struct {
	PONumber     string `json:"poNumber"`
	AssignmentID string `json:"assignmentId"`
	ReleaseNo    int    `json:"releaseNo"`
	NodeID       string `json:"nodeId"`
	ReasonCode   string `json:"reasonCode"`
	RejectedAt   string `json:"rejectedAt"`
}

type expiredData struct {
	PONumber     string `json:"poNumber"`
	AssignmentID string `json:"assignmentId"`
	ReleaseNo    int    `json:"releaseNo"`
	NodeID       string `json:"nodeId"`
	AckBy        string `json:"ackBy"`
}

type cancelledData struct {
	PONumber     string `json:"poNumber"`
	AssignmentID string `json:"assignmentId"`
	Reason       string `json:"reason"`
}

type shipmentData struct {
	PONumber        string   `json:"poNumber"`
	AssignmentID    string   `json:"assignmentId"`
	NodeID          string   `json:"nodeId"`
	TrackingNumbers []string `json:"trackingNumbers"`
	ShippedAt       string   `json:"shippedAt"`
	RequiredShipBy  string   `json:"requiredShipBy"`
}

type processedData struct {
	TransactionID string `json:"transactionId"`
	VendorID      string `json:"vendorId"`
	Kind          string `json:"kind"`
	Status        string `json:"status"`
	ErrorCount    int    `json:"errorCount"`
}

// Encoder implements outbox.Encoder: each domain event becomes one
// outbox.Message holding the CloudEvents bytes, the Kafka key (the aggregate
// root id) and the content-type header. The CloudEvents `id` is minted HERE,
// once, and persisted with the outbox row, so a relay retry republishes the
// same id.
type Encoder struct {
	// NewID mints a CloudEvents id; uuid.NewString when nil.
	NewID func() string
}

// NewEncoder returns an Encoder minting random UUID v4 ids.
func NewEncoder() *Encoder { return &Encoder{NewID: uuid.NewString} }

var _ outbox.Encoder = (*Encoder)(nil)

// Encode encodes events in order.
func (e *Encoder) Encode(events ...shared.Event) ([]outbox.Message, error) {
	newID := e.NewID
	if newID == nil {
		newID = uuid.NewString
	}
	out := make([]outbox.Message, 0, len(events))
	for _, ev := range events {
		data, err := payloadFor(ev)
		if err != nil {
			return nil, err
		}
		stream, topic := cloudevents.StreamEvents, TopicEvents
		if _, analytics := ev.(purchaseorder.PurchaseOrderCancelled); analytics {
			stream, topic = cloudevents.StreamAnalytics, TopicAnalytics
		}
		msg, err := buildMessage(ev, newID(), topic, stream, data)
		if err != nil {
			return nil, err
		}
		out = append(out, msg)
	}
	return out, nil
}

func buildMessage(ev shared.Event, id, topic, stream string, data any) (outbox.Message, error) {
	subject := ev.SubjectID()
	value, err := cloudevents.New(cloudevents.Spec{
		ID: id, Entity: ev.Entity(), EventName: ev.EventName(), Subject: subject,
		Time: ev.OccurredAt(), Stream: stream, Version: schemaVersion, Data: data,
	})
	if err != nil {
		return outbox.Message{}, fmt.Errorf("encode %s: %w", ev.EventName(), err)
	}
	ct := cloudevents.ContentTypeHeader()
	return outbox.Message{
		EventID:    id,
		Topic:      topic,
		EventType:  cloudevents.Type(ev.Entity(), ev.EventName()),
		Subject:    subject,
		Key:        []byte(subject),
		DataSchema: cloudevents.DataSchema(stream, ev.EventName(), schemaVersion),
		Value:      value,
		Headers:    []outbox.Header{{Key: ct.Key, Value: string(ct.Value)}},
	}, nil
}

func ts(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

func payloadFor(ev shared.Event) (any, error) {
	switch e := ev.(type) {
	case vendor.VendorOnboarded:
		return vendorOnboardedData{VendorID: string(e.VendorID), Name: e.Name, AckWindowMinutes: e.AckWindowMinutes}, nil
	case vendor.VendorSuspended:
		return vendorSuspendedData{VendorID: string(e.VendorID), SuspendedAt: ts(e.At), Reason: e.Reason}, nil
	case vendor.VendorReinstated:
		return vendorReinstatedData{VendorID: string(e.VendorID), ReinstatedAt: ts(e.At)}, nil
	case submission.SubmissionProcessed:
		return processedData{TransactionID: string(e.TransactionID), VendorID: e.VendorID, Kind: string(e.Kind), Status: string(e.Status), ErrorCount: e.ErrorCount}, nil
	default:
		return purchaseOrderPayload(ev)
	}
}

func purchaseOrderPayload(ev shared.Event) (any, error) {
	switch e := ev.(type) {
	case purchaseorder.PurchaseOrderIssued:
		lines := make([]poLineData, len(e.Lines))
		for i, l := range e.Lines {
			lines[i] = poLineData{LineNo: l.LineNo, ListingID: l.ListingID, Quantity: l.Quantity}
		}
		return issuedData{PONumber: string(e.PONumber), VendorID: e.VendorID, NodeID: e.NodeID, AssignmentID: e.AssignmentID, ReleaseNo: e.ReleaseNo,
			RequiredShipBy: ts(e.RequiredShipBy), AckBy: ts(e.AckBy), Lines: lines}, nil
	case purchaseorder.PurchaseOrderAccepted:
		return acceptedData{PONumber: string(e.PONumber), AssignmentID: e.AssignmentID, ReleaseNo: e.ReleaseNo, NodeID: e.NodeID,
			AcceptedAt: ts(e.AcceptedAt), TransactionID: e.TransactionID}, nil
	case purchaseorder.PurchaseOrderRejected:
		return rejectedData{PONumber: string(e.PONumber), AssignmentID: e.AssignmentID, ReleaseNo: e.ReleaseNo, NodeID: e.NodeID,
			ReasonCode: string(e.ReasonCode), RejectedAt: ts(e.RejectedAt)}, nil
	case purchaseorder.PurchaseOrderExpired:
		return expiredData{PONumber: string(e.PONumber), AssignmentID: e.AssignmentID, ReleaseNo: e.ReleaseNo, NodeID: e.NodeID, AckBy: ts(e.AckBy)}, nil
	case purchaseorder.PurchaseOrderCancelled:
		return cancelledData{PONumber: string(e.PONumber), AssignmentID: e.AssignmentID, Reason: e.Reason}, nil
	case purchaseorder.VendorShipmentConfirmed:
		return shipmentData{PONumber: string(e.PONumber), AssignmentID: e.AssignmentID, NodeID: e.NodeID, TrackingNumbers: e.TrackingNumbers,
			ShippedAt: ts(e.ShippedAt), RequiredShipBy: ts(e.RequiredShipBy)}, nil
	default:
		return nil, fmt.Errorf("kafka encoder: %s is not a published event", ev.EventName())
	}
}
