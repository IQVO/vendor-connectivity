//go:build integration

package postgres_test

import (
	"context"
	"encoding/json"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"

	"github.com/claudioed/vendor-connectivity/internal/adapters/outbound/kafka"
	"github.com/claudioed/vendor-connectivity/internal/adapters/outbound/postgres"
	"github.com/claudioed/vendor-connectivity/internal/adapters/outbound/repocontract"
	"github.com/claudioed/vendor-connectivity/internal/domain/vendor"
	"github.com/claudioed/vendor-connectivity/internal/testing/pgtest"
)

// An Append made under a span stores that span's traceparent in the row's
// headers, after the encoder's own header, and a span-less Append stores none.
func TestOutboxAppendStoresTheActiveTraceparent(t *testing.T) {
	previous := otel.GetTextMapPropagator()
	otel.SetTextMapPropagator(propagation.TraceContext{})
	t.Cleanup(func() { otel.SetTextMapPropagator(previous) })

	pool := pgtest.NewPool(t)
	ob := postgres.NewEventOutbox(postgres.NewOutboxRepo(pool), kafka.NewEncoder())
	_, evs, err := vendor.Onboard(vendor.NewAccount{ID: "V-1", Name: "Carrier A", AckWindowMinutes: 120}, repocontract.T0)
	if err != nil {
		t.Fatal(err)
	}

	ctx, span := sdktrace.NewTracerProvider().Tracer("test").Start(context.Background(), "POST /vendors")
	defer span.End()
	if err := ob.Append(ctx, evs...); err != nil {
		t.Fatal(err)
	}
	if err := ob.Append(context.Background(), evs...); err != nil {
		t.Fatal(err)
	}

	rows, err := pool.Query(context.Background(), `SELECT headers::text FROM outbox_events ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var stored [][]struct{ Key, Value string }
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			t.Fatal(err)
		}
		var hs []struct{ Key, Value string }
		if err := json.Unmarshal([]byte(raw), &hs); err != nil {
			t.Fatal(err)
		}
		stored = append(stored, hs)
	}
	if len(stored) != 2 {
		t.Fatalf("stored %d rows, want 2", len(stored))
	}
	want := "00-" + span.SpanContext().TraceID().String() + "-" + span.SpanContext().SpanID().String() + "-01"
	if len(stored[0]) != 2 || stored[0][0].Key != "content-type" || stored[0][1].Key != "traceparent" || stored[0][1].Value != want {
		t.Errorf("row under a span has headers %v, want content-type and traceparent %s", stored[0], want)
	}
	if len(stored[1]) != 1 || stored[1][0].Key != "content-type" {
		t.Errorf("row without a span has headers %v, want only content-type", stored[1])
	}
}
