package mcp_test

import (
	"encoding/json"
	"strings"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// The resource and the prompt are L1 by nature: reads and text, no tool Meta
// needed (ADR 0011). These tests pin the complete agent surface the way the
// reference does: list and read the resource through the in-memory client,
// get the prompt with and without its typed argument.

const nodeRosterURI = "warehouse://vendor-connectivity/vendors/V-1/nodes"

// The node roster resource is listed as a template and reads the vendor's
// nodes as JSON text.
func TestResource_NodeRosterListedAndReadable(t *testing.T) {
	h := newHarness(t)
	templates, err := h.session.ListResourceTemplates(t.Context(), nil)
	if err != nil {
		t.Fatalf("list resource templates: %v", err)
	}
	found := ""
	for _, tpl := range templates.ResourceTemplates {
		if tpl.URITemplate == "warehouse://vendor-connectivity/vendors/{vendorId}/nodes" {
			found = tpl.Name
		}
	}
	if found == "" {
		t.Fatalf("node roster template not advertised: %+v", templates.ResourceTemplates)
	}

	res, err := h.session.ReadResource(t.Context(), &sdk.ReadResourceParams{URI: nodeRosterURI})
	if err != nil {
		t.Fatalf("read resource: %v", err)
	}
	if len(res.Contents) != 1 || res.Contents[0].Text == "" {
		t.Fatalf("resource contents = %+v, want one text part", res.Contents)
	}
	if res.Contents[0].URI != nodeRosterURI || res.Contents[0].MIMEType != "application/json" {
		t.Fatalf("contents uri/mime = %q/%q", res.Contents[0].URI, res.Contents[0].MIMEType)
	}
	var roster struct {
		Nodes []struct {
			NodeID   string `json:"nodeId"`
			VendorID string `json:"vendorId"`
			Status   string `json:"status"`
		} `json:"nodes"`
	}
	if err := json.Unmarshal([]byte(res.Contents[0].Text), &roster); err != nil {
		t.Fatalf("roster is not JSON: %v (%s)", err, res.Contents[0].Text)
	}
	if len(roster.Nodes) != 1 || roster.Nodes[0].NodeID != "n-1" || roster.Nodes[0].VendorID != "V-1" {
		t.Fatalf("roster = %+v, want the one registered node of V-1", roster.Nodes)
	}
}

// A malformed or unknown-scheme resource URI is rejected, not defaulted.
func TestResource_MalformedURIIsRejected(t *testing.T) {
	h := newHarness(t)
	for _, uri := range []string{
		"warehouse://other-context/vendors/V-1/nodes",
		"warehouse://vendor-connectivity/vendors//nodes",
		"warehouse://vendor-connectivity/nodes/V-1",
	} {
		if _, err := h.session.ReadResource(t.Context(), &sdk.ReadResourceParams{URI: uri}); err == nil {
			t.Errorf("uri %q: expected a transport error", uri)
		}
	}
}

// A vendor with no nodes yet reads as an empty roster, not an error.
func TestResource_UnknownVendorFailsClosed(t *testing.T) {
	h := newHarness(t)
	_, err := h.session.ReadResource(t.Context(), &sdk.ReadResourceParams{
		URI: "warehouse://vendor-connectivity/vendors/vend-nobody/nodes",
	})
	if err == nil {
		t.Fatal("an unknown vendor must surface the read use case's not-found, not an empty roster")
	}
	if !strings.Contains(err.Error(), "not-found") {
		t.Fatalf("err = %v, want not-found", err)
	}
}

// The operator SOP prompt is listable, carries its typed argument, and
// interpolates the vendor under triage.
func TestPrompt_TriageVendorOrders(t *testing.T) {
	h := newHarness(t)
	list, err := h.session.ListPrompts(t.Context(), nil)
	if err != nil {
		t.Fatalf("list prompts: %v", err)
	}
	if len(list.Prompts) != 1 || list.Prompts[0].Name != "triage_vendor_orders" {
		t.Fatalf("prompts = %+v, want exactly triage_vendor_orders", list.Prompts)
	}
	if len(list.Prompts[0].Arguments) != 1 || list.Prompts[0].Arguments[0].Name != "vendorId" || !list.Prompts[0].Arguments[0].Required {
		t.Fatalf("prompt arguments = %+v, want one required vendorId", list.Prompts[0].Arguments)
	}

	res, err := h.session.GetPrompt(t.Context(), &sdk.GetPromptParams{
		Name:      "triage_vendor_orders",
		Arguments: map[string]string{"vendorId": "V-1"},
	})
	if err != nil {
		t.Fatalf("get prompt: %v", err)
	}
	if len(res.Messages) != 1 {
		t.Fatalf("messages = %+v, want one", res.Messages)
	}
	text := res.Messages[0].Content.(*sdk.TextContent).Text
	if !strings.HasPrefix(text, "Vendor under triage: V-1.") {
		t.Fatalf("SOP does not open with the vendor: %q", firstLine(text))
	}
	for _, tool := range []string{"get_vendor", "list_vendor_nodes", "poll_purchase_orders", "get_purchase_order", "get_transaction"} {
		if !strings.Contains(text, tool+"(") {
			t.Errorf("SOP never names the tool %s", tool)
		}
	}

	// The argument is required: an empty vendorId is refused, not defaulted.
	if _, err := h.session.GetPrompt(t.Context(), &sdk.GetPromptParams{
		Name:      "triage_vendor_orders",
		Arguments: map[string]string{"vendorId": ""},
	}); err == nil {
		t.Fatal("an empty vendorId must be rejected")
	}
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return line
}
