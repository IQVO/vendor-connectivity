package postgres

import (
	"context"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"

	"github.com/claudioed/vendor-connectivity/internal/application/outbox"
)

func useTraceContextPropagator(t *testing.T) {
	t.Helper()
	previous := otel.GetTextMapPropagator()
	otel.SetTextMapPropagator(propagation.TraceContext{})
	t.Cleanup(func() { otel.SetTextMapPropagator(previous) })
}

func headerValue(hs []outbox.Header, key string) (string, bool) {
	for _, h := range hs {
		if h.Key == key {
			return h.Value, true
		}
	}
	return "", false
}

// Under a span the stored headers carry the W3C traceparent of that span after
// the encoder's own header, which stays untouched.
func TestWithTraceHeadersStampsTheActiveSpan(t *testing.T) {
	useTraceContextPropagator(t)
	ctx, span := sdktrace.NewTracerProvider().Tracer("test").Start(context.Background(), "POST /vendor/acknowledgements")
	defer span.End()
	encoder := []outbox.Header{{Key: "content-type", Value: "application/cloudevents+json; charset=UTF-8"}}

	got := withTraceHeaders(encoder, traceHeaders(ctx))

	if v, _ := headerValue(got, "content-type"); v != "application/cloudevents+json; charset=UTF-8" {
		t.Errorf("content-type = %q, want the encoder's header unchanged", v)
	}
	want := "00-" + span.SpanContext().TraceID().String() + "-" + span.SpanContext().SpanID().String() + "-01"
	if v, ok := headerValue(got, "traceparent"); !ok || v != want {
		t.Errorf("traceparent = %q (present %v), want %s", v, ok, want)
	}
	if len(encoder) != 1 {
		t.Errorf("the encoder's slice was modified: %v", encoder)
	}
}

// Without a span (a background loop, a test) the row is stored exactly as the
// encoder built it.
func TestWithTraceHeadersAddsNothingWithoutASpan(t *testing.T) {
	useTraceContextPropagator(t)
	encoder := []outbox.Header{{Key: "content-type", Value: "x"}}

	got := withTraceHeaders(encoder, traceHeaders(context.Background()))

	if len(got) != 1 {
		t.Fatalf("headers = %v, want only the encoder's", got)
	}
}

// An encoder-set header always wins over the propagated one.
func TestWithTraceHeadersKeepsAnEncoderSetTraceparent(t *testing.T) {
	useTraceContextPropagator(t)
	ctx, span := sdktrace.NewTracerProvider().Tracer("test").Start(context.Background(), "x")
	defer span.End()

	got := withTraceHeaders([]outbox.Header{{Key: "traceparent", Value: "encoder"}}, traceHeaders(ctx))

	if len(got) != 1 || got[0].Value != "encoder" {
		t.Fatalf("headers = %v, want the encoder's traceparent only", got)
	}
}
