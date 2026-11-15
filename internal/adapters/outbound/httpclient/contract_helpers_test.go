// Package httpclient: shared helpers of the consumer contract tests. The four
// <producer>_contract_test.go files check this context's REST edges against
// byte-pinned producer specs (contracts/pinned.json + the pinned-contracts CI
// job keep testdata/*.openapi.yaml equal to the producer's develop). Shared
// machinery lives here so each archetype file stays a readable R1-R4 list.
package httpclient

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/getkin/kin-openapi/openapi3"
)

// loadPinnedSpec loads one byte-pinned producer spec from testdata.
func loadPinnedSpec(t *testing.T, filename string) *openapi3.T {
	t.Helper()
	doc, err := openapi3.NewLoader().LoadFromFile("testdata/" + filename)
	if err != nil {
		t.Fatalf("load pinned spec %s: %v", filename, err)
	}
	return doc
}

// readExampleFixture reads one testdata fixture.
func readExampleFixture(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// schemaOf returns one named component schema of a pinned spec.
func schemaOf(t *testing.T, doc *openapi3.T, name string) *openapi3.Schema {
	t.Helper()
	ref := doc.Components.Schemas[name]
	if ref == nil || ref.Value == nil {
		t.Fatalf("schema %s is not in the pinned spec copy", name)
	}
	return ref.Value
}

// typeOf returns the reflect.Type of a (possibly unexported) wire struct.
func typeOf[T any]() reflect.Type { return reflect.TypeOf((*T)(nil)).Elem() }

// checkExampleSatisfies: a testdata fixture validates against a pinned schema
// (required keys present, shapes compatible).
func checkExampleSatisfies(t *testing.T, doc *openapi3.T, schemaName, fixture string) {
	t.Helper()
	var v any
	if err := json.Unmarshal(readExampleFixture(t, fixture), &v); err != nil {
		t.Fatal(err)
	}
	if err := schemaOf(t, doc, schemaName).VisitJSON(v); err != nil {
		t.Errorf("%s does not satisfy the pinned %s schema: %v", fixture, schemaName, err)
	}
}

// checkBodySatisfiesSchema validates a JSON request body the gateway sent
// against a pinned schema: the producer would accept it.
func checkBodySatisfiesSchema(t *testing.T, doc *openapi3.T, schema string, body []byte) {
	t.Helper()
	var sent any
	if err := json.Unmarshal(body, &sent); err != nil {
		t.Fatalf("the gateway's request is not JSON: %v", err)
	}
	if err := schemaOf(t, doc, schema).VisitJSON(sent); err != nil {
		t.Errorf("the request the gateway sends does not satisfy the pinned %s schema: %v", schema, err)
	}
}

// captured is the one request serveFixture recorded.
type captured struct {
	method, path, key string
	body              []byte
}

// serveFixture answers every request with status and body (problem fixtures
// answer application/problem+json), recording the last request.
func serveFixture(t *testing.T, status int, contentType, body string) (*httptest.Server, *captured) {
	t.Helper()
	got := &captured{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read request body: %v", err)
		}
		got.method, got.path, got.key, got.body = r.Method, r.URL.EscapedPath(), r.Header.Get("Idempotency-Key"), raw
		w.Header().Set("Content-Type", contentType)
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv, got
}

var timeType = reflect.TypeOf(time.Time{})

// R4 machinery: every wire field a DTO carries must be declared by the pinned
// schema with a compatible type. decode says the DTO READS the message (so a
// field the schema does not require must tolerate absence); a DTO that writes
// the message always sends its keys, and R3 checks what it sends.
func checkDTOFields(t *testing.T, dt reflect.Type, sch *openapi3.Schema, path string, decode bool) {
	t.Helper()
	props, required := flattenSchema(sch)
	for i := range dt.NumField() {
		f := dt.Field(i)
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if name == "" || name == "-" {
			continue // not a wire field
		}
		p := props[name]
		if p == nil || p.Value == nil {
			t.Errorf("%s.%s: json %q is not declared by the pinned schema", path, f.Name, name)
			continue
		}
		checkWireType(t, f.Type, p.Value, path+"."+name, decode)
		if decode && !required[name] && !toleratesAbsence(f, path+"."+name) {
			t.Errorf("%s.%s: optional in the pinned schema, so the DTO must tolerate its absence (pointer, slice, omitempty or a documented zero-value default)", path, f.Name)
		}
	}
}

// flattenSchema merges a schema's own properties and required keys with those
// of its allOf branches into one view.
func flattenSchema(sch *openapi3.Schema) (map[string]*openapi3.SchemaRef, map[string]bool) {
	props := map[string]*openapi3.SchemaRef{}
	required := map[string]bool{}
	var walk func(*openapi3.Schema)
	walk = func(s *openapi3.Schema) {
		if s == nil {
			return
		}
		for n, p := range s.Properties {
			props[n] = p
		}
		for _, r := range s.Required {
			required[r] = true
		}
		for _, b := range s.AllOf {
			walk(b.Value)
		}
	}
	walk(sch)
	return props, required
}

// zeroValueDefaults lists DTO fields whose Go zero value is this context's
// documented reading of an absent optional field (R4's absence-tolerance).
// Keys are schema paths as the R4 walk reports them.
var zeroValueDefaults = map[string]string{
	"Node.subtype":         "no subtype: the spec declares subtype only for FULFILLMENT_CENTER nodes",
	"Scorecard.computedAt": "the zero time reads as not-yet-computed; the flattening tests assert IsZero handling",
}

func toleratesAbsence(f reflect.StructField, path string) bool {
	if _, documented := zeroValueDefaults[path]; documented {
		return true
	}
	tag := f.Tag.Get("json")
	if strings.HasSuffix(tag, ",omitempty") {
		return true
	}
	switch f.Type.Kind() {
	case reflect.Pointer, reflect.Slice, reflect.Map:
		return true
	}
	return false
}

// scalarWireTypes maps the scalar Go kinds a DTO may carry to the OpenAPI
// type that must declare them.
var scalarWireTypes = map[reflect.Kind]string{
	reflect.String: "string", reflect.Bool: "boolean",
	reflect.Int: "integer", reflect.Int8: "integer", reflect.Int16: "integer", reflect.Int32: "integer", reflect.Int64: "integer",
	reflect.Uint: "integer", reflect.Uint8: "integer", reflect.Uint16: "integer", reflect.Uint32: "integer", reflect.Uint64: "integer",
	reflect.Float32: "number", reflect.Float64: "number",
}

// checkWireType asserts a Go type and a pinned schema agree on the wire shape.
// Decoding is held to the tolerant side of the truth only: a Go float64 reads
// any JSON number, so a schema integer still satisfies it, while an encoding
// float64 could emit a fraction a schema integer would reject.
func checkWireType(t *testing.T, dt reflect.Type, sch *openapi3.Schema, path string, decode bool) {
	t.Helper()
	if dt == timeType {
		if !sch.Type.Includes("string") || sch.Format != "date-time" {
			t.Errorf("%s: time.Time, but the pinned schema says %v/%q", path, sch.Type.Slice(), sch.Format)
		}
		return
	}
	switch dt.Kind() {
	case reflect.Pointer:
		checkWireType(t, dt.Elem(), sch, path, decode)
	case reflect.Slice:
		checkArrayWireType(t, dt.Elem(), sch, path, decode)
	case reflect.Map:
		checkMapWireType(t, dt.Elem(), sch, path, decode)
	case reflect.Struct:
		checkObjectWireType(t, dt, sch, path, decode)
	default:
		want, scalar := scalarWireTypes[dt.Kind()]
		if !scalar {
			t.Errorf("%s: Go kind %s has no wire mapping", path, dt.Kind())
			return
		}
		ok := sch.Type.Includes(want) || (decode && want == "number" && sch.Type.Includes("integer"))
		if len(sch.Type.Slice()) > 0 && !ok {
			t.Errorf("%s: pinned schema type %v, the Go field is %s", path, sch.Type.Slice(), want)
		}
	}
}

func checkArrayWireType(t *testing.T, elem reflect.Type, sch *openapi3.Schema, path string, decode bool) {
	t.Helper()
	if assertWireType(t, sch, path, "array") && sch.Items != nil && sch.Items.Value != nil {
		checkWireType(t, elem, sch.Items.Value, path+"[]", decode)
	}
}

// checkMapWireType: a Go map value type must satisfy EVERY property the
// schema declares, because one Go type answers for all of the map's keys.
func checkMapWireType(t *testing.T, elem reflect.Type, sch *openapi3.Schema, path string, decode bool) {
	t.Helper()
	if !assertWireType(t, sch, path, "object") {
		return
	}
	names := make([]string, 0, len(sch.Properties))
	for n := range sch.Properties {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		if p := sch.Properties[n]; p != nil && p.Value != nil {
			checkWireType(t, elem, p.Value, path+"."+n, decode)
		}
	}
}

func checkObjectWireType(t *testing.T, dt reflect.Type, sch *openapi3.Schema, path string, decode bool) {
	t.Helper()
	if assertWireType(t, sch, path, "object") {
		checkDTOFields(t, dt, sch, path, decode)
	}
}

func assertWireType(t *testing.T, sch *openapi3.Schema, path, want string) bool {
	t.Helper()
	if len(sch.Type.Slice()) > 0 && !sch.Type.Includes(want) {
		t.Errorf("%s: pinned schema type %v, the Go field is %s", path, sch.Type.Slice(), want)
		return false
	}
	return true
}
