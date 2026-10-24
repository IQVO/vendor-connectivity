package mcp_test

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	inboundmcp "github.com/claudioed/vendor-connectivity/internal/adapters/inbound/mcp"
)

// update regenerates testdata/tool_registry.golden.json:
//
//	go test ./internal/adapters/inbound/mcp -run TestToolRegistryGolden -update
var update = flag.Bool("update", false, "rewrite the MCP tool registry golden file")

// wantTools is the curated, READ-ONLY tool set (docs/adr/0005): one tool per
// GET operation of the Vendor API.
var wantTools = []string{
	"get_node_scorecard", "get_purchase_order", "get_transaction", "get_vendor",
	"get_vendor_node", "list_vendor_nodes", "poll_purchase_orders",
}

// maxTools is the tool budget. Raising it is a deliberate, reviewed act (an
// ADR 0005 amendment): TestToolSurface still pins the exact curated set.
const maxTools = 7

// writeVerbs are the verbs of this service's write use cases (onboard a
// vendor, suspend and reinstate it, register a node, declare a profile,
// submit an acknowledgement, an inventory feed or a shipment confirmation,
// request a label, the consumer intakes and the loops) and the generic
// mutators. A tool whose name contains one of them as a word is a write
// tool, which the read-only MCP surface must never expose: writes go through
// REST, the transactional outbox and the event consumers. "poll" is NOT
// here: polling purchase orders is this context's own read (the Vendor
// API's GET /vendor/purchase-orders operationId).
var writeVerbs = []string{
	"onboard", "suspend", "reinstate", "register", "declare", "submit",
	"request", "confirm", "acknowledge", "issue", "release", "cancel",
	"expire", "process", "relay", "consume", "apply",
	"create", "update", "delete", "set", "put", "post", "assign",
}

// writeVerbViolations returns one message per tool name carrying a write
// verb as one of its snake_case words.
func writeVerbViolations(names []string) []string {
	var out []string
	for _, name := range names {
		for _, word := range strings.Split(name, "_") {
			for _, verb := range writeVerbs {
				if word == verb {
					out = append(out, name+": contains the write verb "+verb+" (the MCP surface is read-only, docs/adr/0005)")
				}
			}
		}
	}
	return out
}

// The MCP governance charter's mechanical gate: the advertised tool set is
// exactly the curated read-only one, within the budget, snake_case
// verb_noun with NO write verb, annotated read-only, described, and every
// argument is lowerCamelCase (like the REST contract) and documented.
func TestToolSurface(t *testing.T) {
	h := newHarness(t)
	res, err := h.session.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	if len(res.Tools) > maxTools {
		t.Fatalf("advertised %d tools, over the budget of %d", len(res.Tools), maxTools)
	}
	var names []string
	for _, tool := range res.Tools {
		names = append(names, tool.Name)
		checkToolMetadata(t, tool)
		checkToolArguments(t, tool)
	}
	sort.Strings(names)
	if strings.Join(names, ",") != strings.Join(wantTools, ",") {
		t.Fatalf("advertised tools %v, want exactly %v", names, wantTools)
	}
	for _, v := range writeVerbViolations(names) {
		t.Error(v)
	}
}

// The write-verb sensor must actually be able to fail: every write verb in a
// tool name is flagged, read names are not.
func TestWriteVerbSensorFailsOnWriteNames(t *testing.T) {
	for _, verb := range writeVerbs {
		for _, name := range []string{verb + "_orders", "bulk_" + verb + "_nodes"} {
			if len(writeVerbViolations([]string{name})) == 0 {
				t.Errorf("write tool name %q was not flagged", name)
			}
		}
	}
	if got := writeVerbViolations(wantTools); len(got) != 0 {
		t.Errorf("read tool names were flagged: %v", got)
	}
	// Substrings of a word are not verbs: "dataset" is not "set", "shipment" is not "ship".
	if got := writeVerbViolations([]string{"get_dataset", "list_shipments", "get_settings"}); len(got) != 0 {
		t.Errorf("false positive on non-verb words: %v", got)
	}
}

var (
	toolNaming = regexp.MustCompile(`^[a-z]+(_[a-z]+)+$`)
	argNaming  = regexp.MustCompile(`^[a-z][a-zA-Z0-9]*$`)
)

func checkToolMetadata(t *testing.T, tool *sdk.Tool) {
	t.Helper()
	if !toolNaming.MatchString(tool.Name) {
		t.Errorf("tool %q is not snake_case verb_noun", tool.Name)
	}
	if tool.Description == "" {
		t.Errorf("tool %q has no description", tool.Name)
	}
	if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint {
		t.Errorf("tool %q is not annotated read-only: %+v", tool.Name, tool.Annotations)
		return
	}
	if tool.Annotations.DestructiveHint != nil && *tool.Annotations.DestructiveHint {
		t.Errorf("tool %q is annotated destructive", tool.Name)
	}
}

func checkToolArguments(t *testing.T, tool *sdk.Tool) {
	t.Helper()
	schema, isMap := tool.InputSchema.(map[string]any)
	if !isMap {
		t.Errorf("tool %q input schema = %T", tool.Name, tool.InputSchema)
		return
	}
	props, _ := schema["properties"].(map[string]any)
	if len(props) == 0 {
		t.Errorf("tool %q has no arguments in its schema", tool.Name)
	}
	for arg, p := range props {
		if !argNaming.MatchString(arg) {
			t.Errorf("tool %q argument %q is not lowerCamelCase", tool.Name, arg)
		}
		if d, _ := p.(map[string]any)["description"].(string); d == "" {
			t.Errorf("tool %q argument %q has no description", tool.Name, arg)
		}
	}
}

// TestToolRegistryGolden pins the whole advertised registry (names,
// descriptions, annotations, input AND output schemas) byte for byte, so any
// change to what an MCP client sees shows up as a reviewed golden diff.
func TestToolRegistryGolden(t *testing.T) {
	h := newHarness(t)
	res, err := h.session.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	sort.Slice(res.Tools, func(i, j int) bool { return res.Tools[i].Name < res.Tools[j].Name })
	got, err := json.MarshalIndent(res.Tools, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	got = append(got, '\n')

	path := filepath.Join("testdata", "tool_registry.golden.json")
	if *update {
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, got, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden (run with -update to create it): %v", err)
	}
	if string(got) != string(want) {
		t.Fatalf("the MCP tool registry drifted from %s; review the change and re-run with -update.\ngot:\n%s", path, got)
	}
}

// Every input the handler treats as optional (the omitempty fields of the
// tool's input struct) is published as optional: a model that leaves a
// filter out must not be rejected by schema validation. The path parameters
// (vendorId, poNumber, ...) stay required — they name the resource.
func TestOptionalInputsAreNotRequired(t *testing.T) {
	h := newHarness(t)
	res, err := h.session.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	optional := map[string]map[string]bool{
		"poll_purchase_orders": {"nodeId": true, "createdAfter": true, "nextToken": true, "limit": true},
	}
	for _, tool := range res.Tools {
		schema := tool.InputSchema.(map[string]any)
		props, _ := schema["properties"].(map[string]any)
		req, _ := schema["required"].([]any)
		required := map[string]bool{}
		for _, r := range req {
			if s, isStr := r.(string); isStr {
				required[s] = true
			}
		}
		for arg := range props {
			_, isOptional := optional[tool.Name][arg]
			if isOptional && required[arg] {
				t.Errorf("tool %q requires the optional input %q", tool.Name, arg)
			}
			if !isOptional && !required[arg] {
				t.Errorf("tool %q does not require the path parameter %q", tool.Name, arg)
			}
		}
	}
}

// Reads is the narrow read surface, and it must stay that way: exactly the
// seven read methods the REST adapter's GET routes call, in the shape the
// service declares them. A renamed or added method shows up here (and in
// cmd/mcp, which must keep compiling) before it becomes a silent drift
// between the two driving adapters.
func TestReadsSurfacePinsTheReadMethods(t *testing.T) {
	rt := reflect.TypeOf((*inboundmcp.Reads)(nil)).Elem()
	want := map[string]bool{
		"PollPurchaseOrders": true, "GetPurchaseOrder": true, "ListVendorNodes": true,
		"GetNode": true, "GetScorecard": true, "GetTransaction": true, "GetVendor": true,
	}
	got := map[string]bool{}
	for i := range rt.NumMethod() {
		name := rt.Method(i).Name
		if !want[name] {
			t.Errorf("Reads gained the method %s: not one of the seven Vendor API reads", name)
		}
		got[name] = true
	}
	for name := range want {
		if !got[name] {
			t.Errorf("Reads lost the method %s", name)
		}
	}
	if rt.NumMethod() != len(want) {
		t.Fatalf("Reads has %d methods, want exactly %d", rt.NumMethod(), len(want))
	}
}

// writeUseCaseNames are the exported write use cases and consumer intakes of
// the application Service. None of them may be NAMED anywhere in the
// adapter's non-test sources: not in a Deps field, not in a handler, not in
// a comment that documents a call. Together with Reads this pins that no
// write path is reachable or even describable through the MCP surface.
var writeUseCaseNames = []string{
	"OnboardVendor", "SuspendVendor", "ReinstateVendor", "RegisterVendorNode",
	"DeclareNodeProfile", "SubmitAcknowledgement", "SubmitInventory",
	"RequestLabel", "SubmitShipmentConfirmation", "OnNodeRegistered",
	"OnListingChanged", "OnSourcingReleased", "IssueOnRelease", "OnOrderCancelled",
	"ProcessDueSubmissions", "ExpireOrders", "DeleteExpiredSubmissions",
}

func TestNoWriteUseCaseIsNamed(t *testing.T) {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		body, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		for _, useCase := range writeUseCaseNames {
			if strings.Contains(string(body), useCase) {
				t.Errorf("%s names the write use case %s: the MCP surface is read-only (docs/adr/0005)", name, useCase)
			}
		}
	}
}

// authMiddlewareNeedles are what an auth middleware reintroduction would
// inevitably look like in this adapter's or cmd/mcp's sources. The fleet-wide
// revert of 2026-09-11 left the MCP endpoints unauthenticated on purpose:
// access control is the in-cluster ClusterIP boundary.
var authMiddlewareNeedles = []string{
	`"Authorization"`, `"X-API-Key"`, `"x-api-key"`, "Bearer ", "APIKey", "apikey", "Unauthorized()",
}

func TestNoAuthMiddlewareReintroduced(t *testing.T) {
	t.Helper()
	dirs := []string{".", "../../../../cmd/mcp"}
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range entries {
			name := entry.Name()
			if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
				continue
			}
			body, err := os.ReadFile(filepath.Join(dir, name))
			if err != nil {
				t.Fatal(err)
			}
			for _, needle := range authMiddlewareNeedles {
				if strings.Contains(string(body), needle) {
					t.Errorf("%s mentions %q: no auth middleware may ever come back (fleet-wide revert 2026-09-11)", filepath.Join(dir, name), needle)
				}
			}
		}
	}
}
