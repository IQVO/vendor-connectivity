// Package mcp is the inbound Model Context Protocol adapter: it exposes this
// bounded context to the AI ecosystem as a second driving adapter over the
// same application-layer read queries the REST adapter serves. It is built on
// the official MCP Go SDK and served over Streamable HTTP only.
//
// Per docs/adr/0006-mcp-server-adoption.md the surface is READ-ONLY: every
// tool reads vendor-connectivity data (poll_purchase_orders,
// get_purchase_order, poll_inbound_shipments, get_inbound_shipment,
// list_vendor_nodes, get_vendor_node, get_node_scorecard, get_transaction,
// get_vendor); the submissions, the vendor lifecycle, the node registrations
// and the inbound receipts stay on REST, on the consumers and on the
// background loops of cmd/api, and governance_test.go fails the build on a
// write-verb tool name. Read-only is structural here: reads and writes share
// the application Service, so the adapter holds a Reads interface that names
// EXACTLY the nine read methods the REST adapter's GET routes call — through
// it no write use case is reachable, and TestReadsSurfacePinsTheReadMethods
// plus TestNoWriteUseCaseIsNamed pin that. This package depends inward on the
// application layer and the domain only, never on an outbound adapter or the
// REST adapter, and nothing else may depend on it
// (internal/architecture's TestMCPAdapterDependencyRule). The composition root
// (cmd/mcp) wires concrete repositories into the service and the service into
// Reads. There is no auth of any kind (fleet-wide revert 2026-09-11;
// TestNoAuthMiddlewareReintroduced).
package mcp

import (
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// serverName and serverVersion identify this server in the MCP initialize
// handshake.
const (
	serverName    = "vendor-connectivity-mcp"
	serverVersion = "1.0.0"
)

// instructions is what an MCP client is told about this server at initialize.
const instructions = "Vendor connectivity of the retail network (read-only): page through the purchase orders released " +
	"to one vendor with poll_purchase_orders (optionally narrowed to a node and to orders issued after a moment, " +
	"paged with nextToken) or read one order with get_purchase_order (its lines, its acknowledgement and its labels " +
	"— never a ship-to address); page through the inbound shipments bound for the vendor's nodes with " +
	"poll_inbound_shipments or read one shipment's pallet-and-carton tree with get_inbound_shipment; see the " +
	"vendor's fulfilment nodes with list_vendor_nodes or one with get_vendor_node, and its rolling scorecards with " +
	"get_node_scorecard; follow an asynchronous write with get_transaction (the 202 transactionId, its status and " +
	"its errors); and read the vendor account itself (state and acknowledgement window) with get_vendor. Nothing " +
	"here changes data: acknowledgements, inventory feeds, shipment confirmations, label requests and inbound " +
	"receipts are REST operations of vendor-connectivity."

// NewServer builds the MCP server for this bounded context with every tool
// registered.
func NewServer(deps Deps) *mcp.Server {
	server := mcp.NewServer(
		&mcp.Implementation{Name: serverName, Version: serverVersion},
		&mcp.ServerOptions{Instructions: instructions},
	)
	deps.registerTools(server)
	return server
}

// Handler returns the Streamable HTTP handler for the MCP server.
func Handler(server *mcp.Server) http.Handler {
	return mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, nil)
}
