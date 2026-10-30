package main

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

	inbounda2a "github.com/claudioed/vendor-connectivity/internal/adapters/inbound/a2a"
	"github.com/claudioed/vendor-connectivity/internal/adapters/outbound/telemetry"
)

func TestLoadTelemetryEnvDefaultsToTheA2AServiceName(t *testing.T) {
	got := loadTelemetryEnv(func(string) string { return "" })
	if got.serviceName != "vendor-connectivity-a2a" || got.otlpEndpoint != telemetry.DefaultEndpoint || got.serviceVersion != telemetry.DefaultServiceVersion {
		t.Fatalf("defaults = %+v", got)
	}
}

func TestLoadTelemetryEnvReadsTheOTelContract(t *testing.T) {
	env := map[string]string{"OTEL_SERVICE_NAME": "svc", "SERVICE_VERSION": "1.2.3", "OTEL_EXPORTER_OTLP_ENDPOINT": "collector:4317"}
	got := loadTelemetryEnv(func(k string) string { return env[k] })
	want := telemetryEnv{serviceName: "svc", serviceVersion: "1.2.3", otlpEndpoint: "collector:4317"}
	if got != want {
		t.Fatalf("env = %+v, want %+v", got, want)
	}
}

// One A2A request yields a server span named after the route and a request log
// line whose trace_id/span_id are that span's.
func TestRouterLogCarriesTheServerSpanTraceID(t *testing.T) {
	tp, mp := otel.GetTracerProvider(), otel.GetMeterProvider()
	t.Cleanup(func() { otel.SetTracerProvider(tp); otel.SetMeterProvider(mp) })
	recorder := tracetest.NewSpanRecorder()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder)))
	var buf bytes.Buffer
	logger := slog.New(telemetry.WithTraceContext(slog.NewJSONHandler(&buf, nil)))
	router := newRouter(inbounda2a.Handler(inbounda2a.NewExecutor(inbounda2a.Deps{})), inbounda2a.Card(inbounda2a.Config{AgentURL: testAgentURL, Version: agentVersion}), logger, "vendor-connectivity-a2a")

	// The card discovery request is a plain GET the router serves itself.
	router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/.well-known/agent-card.json", nil))

	spans := recorder.Ended()
	if len(spans) != 1 || spans[0].SpanKind() != trace.SpanKindServer {
		t.Fatalf("spans = %d, want one server span", len(spans))
	}
	var line map[string]any
	if err := json.Unmarshal(buf.Bytes(), &line); err != nil {
		t.Fatalf("log output is not one JSON line: %q (%v)", buf.String(), err)
	}
	sc := spans[0].SpanContext()
	if line[telemetry.TraceIDKey] != sc.TraceID().String() || line[telemetry.SpanIDKey] != sc.SpanID().String() {
		t.Fatalf("log ids = %v/%v, want the server span's %s/%s", line[telemetry.TraceIDKey], line[telemetry.SpanIDKey], sc.TraceID(), sc.SpanID())
	}
	if id, _ := line["request_id"].(string); id == "" {
		t.Error("request_id missing from the request line")
	}
}
