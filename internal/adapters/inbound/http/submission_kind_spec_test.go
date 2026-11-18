package http_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"slices"
	"strconv"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/claudioed/vendor-connectivity/internal/domain/submission"
)

const (
	submissionSource = "../../../domain/submission/submission.go"
	openAPISpec      = "../../../../apis/openapi.yaml"
	asyncAPISpec     = "../../../../apis/asyncapi.yaml"
	kindSchemaRef    = "#/components/schemas/SubmissionKind"
)

// codeSubmissionKinds reads every constant of type Kind the domain declares,
// so a kind added to the code is seen here without touching this test.
func codeSubmissionKinds(t *testing.T) []string {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), submissionSource, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var kinds []string
	ast.Inspect(file, func(n ast.Node) bool {
		spec, ok := n.(*ast.ValueSpec)
		if !ok {
			return true
		}
		if typ, ok := spec.Type.(*ast.Ident); !ok || typ.Name != "Kind" {
			return true
		}
		for _, v := range spec.Values {
			lit, ok := v.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				t.Fatalf("a Kind constant of %s is not a string literal", submissionSource)
			}
			s, err := strconv.Unquote(lit.Value)
			if err != nil {
				t.Fatal(err)
			}
			kinds = append(kinds, s)
		}
		return true
	})
	if len(kinds) == 0 {
		t.Fatalf("%s declares no Kind constant", submissionSource)
	}
	slices.Sort(kinds)
	return kinds
}

// specDoc is the part of an OpenAPI or AsyncAPI document the kind sensor reads.
type specDoc struct {
	Components struct{ Schemas map[string]yaml.Node }
}

func loadSpec(t *testing.T, path string) specDoc {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc specDoc
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	return doc
}

// child returns the value of key in a YAML mapping, or nil.
func child(n *yaml.Node, key string) *yaml.Node {
	if n == nil || n.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			return n.Content[i+1]
		}
	}
	return nil
}

// at walks a path of mapping keys; "allOf" merges are searched branch by branch.
func at(n *yaml.Node, path ...string) *yaml.Node {
	for i, key := range path {
		next := child(n, key)
		if next == nil {
			all := child(n, "allOf")
			if all == nil {
				return nil
			}
			for _, branch := range all.Content {
				if found := at(branch, path[i:]...); found != nil {
					return found
				}
			}
			return nil
		}
		n = next
	}
	return n
}

func specSubmissionKinds(t *testing.T, path string, doc specDoc) []string {
	t.Helper()
	schema, ok := doc.Components.Schemas["SubmissionKind"]
	if !ok {
		t.Fatalf("%s has no SubmissionKind schema", path)
	}
	var kinds []string
	if err := at(&schema, "enum").Decode(&kinds); err != nil || len(kinds) == 0 {
		t.Fatalf("%s: SubmissionKind.enum = %v (%v)", path, kinds, err)
	}
	slices.Sort(kinds)
	return kinds
}

// assertRefsKind checks that the property the service answers with is the
// SubmissionKind schema, so the enum compared is the one consumers read.
func assertRefsKind(t *testing.T, path string, doc specDoc, schemaName string, property ...string) {
	t.Helper()
	schema, ok := doc.Components.Schemas[schemaName]
	if !ok {
		t.Fatalf("%s has no %s schema", path, schemaName)
	}
	ref := at(&schema, append(property, "$ref")...)
	if ref == nil || ref.Value != kindSchemaRef {
		t.Fatalf("%s: %s %v does not reference %s", path, schemaName, property, kindSchemaRef)
	}
}

// TestSubmissionKindEnumMatchesTheDomain keeps the SubmissionKind enum of both
// specs equal to the kinds the domain declares: GET /vendor/transactions
// answers with Transaction.kind and SubmissionProcessed carries data.kind, so a
// kind the code knows and a spec does not is a value consumers were never told.
func TestSubmissionKindEnumMatchesTheDomain(t *testing.T) {
	code := codeSubmissionKinds(t)
	for _, k := range code {
		in := submission.NewSubmission{ID: "tx-1", VendorID: "V-1", Kind: submission.Kind(k), Payload: []byte(`{}`)}
		if _, err := submission.Receive(in, time.Unix(0, 0), 0); err != nil {
			t.Errorf("the domain declares kind %s and refuses it: %v", k, err)
		}
	}
	rest := loadSpec(t, openAPISpec)
	assertRefsKind(t, openAPISpec, rest, "Transaction", "properties", "kind")
	if got := specSubmissionKinds(t, openAPISpec, rest); !slices.Equal(got, code) {
		t.Errorf("%s SubmissionKind enum:\n got %v\nwant %v (internal/domain/submission)", openAPISpec, got, code)
	}
	events := loadSpec(t, asyncAPISpec)
	assertRefsKind(t, asyncAPISpec, events, "SubmissionProcessedEvent", "properties", "data", "properties", "kind")
	if got := specSubmissionKinds(t, asyncAPISpec, events); !slices.Equal(got, code) {
		t.Errorf("%s SubmissionKind enum:\n got %v\nwant %v (internal/domain/submission)", asyncAPISpec, got, code)
	}
}
