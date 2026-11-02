package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// mimeTypeJSON is the MIME type of every resource this server serves.
const mimeTypeJSON = "application/json"

// registerResources adds the scoped read-model resource (Agentic Standard v2,
// AS-3; ADR 0011). Per the charter the surface stays READ-ONLY: a resource is
// context data an agent can load without a tool call, built from an existing
// read use case, never a dump of a table. The vendor's node roster is exactly
// that: the local read model of the consumed node-topology events (ADR 0003)
// behind GET /vendor/nodes and the list_vendor_nodes tool, scoped to ONE
// vendor and bounded by the nodes that vendor operates.
func (d Deps) registerResources(server *mcp.Server) {
	const nodeRosterTemplate = "warehouse://vendor-connectivity/vendors/{vendorId}/nodes"
	server.AddResourceTemplate(&mcp.ResourceTemplate{
		URITemplate: nodeRosterTemplate,
		Name:        "vendor node roster",
		Description: "The fulfilment nodes the retail network has registered for one vendor (nodeId, type, subtype, status, region, timezone, handling time, profile revision): the local read model behind list_vendor_nodes, scoped to the vendorId of the URI. Read-only, like the whole surface (ADR 0006).",
		MIMEType:    mimeTypeJSON,
	}, func(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		vendorID, err := vendorIDOfResourceURI(req.Params.URI)
		if err != nil {
			return nil, err
		}
		list, err := d.Reads.ListVendorNodes(ctx, vendorID)
		if err != nil {
			return nil, mapError(ctx, err)
		}
		return resourceResult(req.Params.URI, list)
	})
}

// resourceResult renders one read view as the resource's JSON text.
func resourceResult(uri string, view any) (*mcp.ReadResourceResult, error) {
	body, err := json.Marshal(view)
	if err != nil {
		return nil, fmt.Errorf("render resource %s: %w", uri, err)
	}
	return &mcp.ReadResourceResult{
		Contents: []*mcp.ResourceContents{{
			URI:      uri,
			MIMEType: mimeTypeJSON,
			Text:     string(body),
		}},
	}, nil
}

// vendorIDOfResourceURI extracts the vendorId segment of
// warehouse://vendor-connectivity/vendors/{vendorId}/nodes. The segment is
// model-supplied and therefore untrusted; an empty or malformed one is
// rejected rather than silently defaulted.
func vendorIDOfResourceURI(uri string) (string, error) {
	const (
		scheme = "warehouse://vendor-connectivity/"
		leaf   = "/nodes"
	)
	rest, ok := strings.CutPrefix(uri, scheme)
	if !ok {
		return "", fmt.Errorf("resource uri %q must start with %q", uri, scheme)
	}
	vendorPart, ok := strings.CutSuffix(strings.TrimPrefix(rest, "vendors/"), leaf)
	if !ok || vendorPart == "" || strings.Contains(vendorPart, "/") {
		return "", fmt.Errorf("resource uri %q must be warehouse://vendor-connectivity/vendors/{vendorId}/nodes", uri)
	}
	return vendorPart, nil
}
