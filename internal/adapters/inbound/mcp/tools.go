package mcp

import (
	"context"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/claudioed/vendor-connectivity/internal/application/requests"
	"github.com/claudioed/vendor-connectivity/internal/application/views"
	"github.com/claudioed/vendor-connectivity/internal/domain/shared"
)

// Reads is EXACTLY the read surface of the application service: one method
// per GET operation of the Vendor API (apis/openapi.yaml), the same methods
// the REST adapter's handlers call. Reads and writes share the Service type,
// so read-only is enforced by the narrowest possible interface: through Reads
// no write use case can even be named (docs/adr/0006). The composition root
// (cmd/mcp) satisfies it with the same *usecases.Service cmd/api builds.
type Reads interface {
	// PollPurchaseOrders backs GET /vendor/purchase-orders.
	PollPurchaseOrders(ctx context.Context, q requests.PollQuery) (views.PurchaseOrderPage, error)
	// GetPurchaseOrder backs GET /vendor/purchase-orders/{poNumber}.
	GetPurchaseOrder(ctx context.Context, vendorID, poNumber string) (views.PurchaseOrder, error)
	// PollInboundShipments backs GET /vendor/inbound-shipments.
	PollInboundShipments(ctx context.Context, q requests.InboundPollQuery) (views.InboundShipmentPage, error)
	// GetInboundShipment backs GET /vendor/inbound-shipments/{shipmentId}.
	GetInboundShipment(ctx context.Context, vendorID, shipmentID string) (views.InboundShipment, error)
	// ListVendorNodes backs GET /vendor/nodes.
	ListVendorNodes(ctx context.Context, vendorID string) (views.VendorNodeList, error)
	// GetNode backs GET /vendor/nodes/{nodeId}.
	GetNode(ctx context.Context, vendorID, nodeID string) (views.VendorNode, error)
	// GetScorecard backs GET /vendor/nodes/{nodeId}/scorecard.
	GetScorecard(ctx context.Context, vendorID, nodeID string) (views.Scorecard, error)
	// GetTransaction backs GET /vendor/transactions/{transactionId}.
	GetTransaction(ctx context.Context, vendorID, transactionID string) (views.Transaction, error)
	// GetVendor backs GET /vendors/{vendorId}.
	GetVendor(ctx context.Context, vendorID string) (views.VendorAccount, error)
}

// Deps is everything the MCP tools need, injected by the composition root.
// It carries the read surface only; the adapter never constructs an outbound
// adapter itself, and it holds no write use case at all (docs/adr/0006). A
// zero Deps is a valid server with no reachable database, which the transport
// tests use.
type Deps struct {
	Reads Reads
}

// --- inputs -------------------------------------------------------------------

type vendorInput struct {
	VendorID string `json:"vendorId" jsonschema:"the vendor account every read is scoped to, e.g. vend-south-1; a vendor of another account is never visible"`
}

type vendorPOInput struct {
	VendorID string `json:"vendorId" jsonschema:"the vendor the order was released to, e.g. vend-south-1"`
	PONumber string `json:"poNumber" jsonschema:"the purchase order number, e.g. PO-9a1b-0001"`
}

type vendorNodeInput struct {
	VendorID string `json:"vendorId" jsonschema:"the vendor that operates the node, e.g. vend-south-1"`
	NodeID   string `json:"nodeId" jsonschema:"the fulfilment node, e.g. node-76c2"`
}

type vendorTransactionInput struct {
	VendorID      string `json:"vendorId" jsonschema:"the vendor that submitted the write, e.g. vend-south-1"`
	TransactionID string `json:"transactionId" jsonschema:"the transactionId a 202 answer returned, e.g. tx-3f0c9d"`
}

type pollPurchaseOrdersInput struct {
	VendorID     string `json:"vendorId" jsonschema:"the vendor whose orders to poll, e.g. vend-south-1"`
	NodeID       string `json:"nodeId,omitempty" jsonschema:"keep only orders released to this node; omitted means every node of the vendor"`
	CreatedAfter string `json:"createdAfter,omitempty" jsonschema:"keep only orders issued after this moment, an RFC 3339 date-time such as 2026-10-01T00:00:00Z; omitted means every issue date"`
	NextToken    string `json:"nextToken,omitempty" jsonschema:"the opaque nextToken returned by the previous page; omitted means the first page"`
	Limit        int    `json:"limit,omitempty" jsonschema:"page size, 1..200; omitted or 0 means the service default of 50"`
}

type vendorShipmentInput struct {
	VendorID   string `json:"vendorId" jsonschema:"the vendor whose node the shipment is bound for, e.g. vend-south-1"`
	ShipmentID string `json:"shipmentId" jsonschema:"the inbound shipment id, e.g. shp-4b7e"`
}

type pollInboundShipmentsInput struct {
	VendorID     string `json:"vendorId" jsonschema:"the vendor whose inbound shipments to poll, e.g. vend-south-1"`
	NodeID       string `json:"nodeId,omitempty" jsonschema:"keep only shipments bound for this node; omitted means every node of the vendor"`
	CreatedAfter string `json:"createdAfter,omitempty" jsonschema:"keep only shipments shipped after this moment, an RFC 3339 date-time such as 2026-10-01T00:00:00Z; omitted means every ship date"`
	NextToken    string `json:"nextToken,omitempty" jsonschema:"the opaque nextToken returned by the previous page; omitted means the first page"`
	Limit        int    `json:"limit,omitempty" jsonschema:"page size, 1..200; omitted or 0 means the service default of 50"`
}

// --- handlers -----------------------------------------------------------------

// pollPurchaseOrders mirrors GET /vendor/purchase-orders: the vendor's orders,
// oldest first, a page at a time. A malformed createdAfter or a negative
// limit is refused here exactly as the REST adapter refuses it, with
// validation-failed.
func (d Deps) pollPurchaseOrders(ctx context.Context, in pollPurchaseOrdersInput) (views.PurchaseOrderPage, error) {
	q := requests.PollQuery{VendorID: in.VendorID, NodeID: in.NodeID, NextToken: in.NextToken, Limit: in.Limit}
	if in.CreatedAfter != "" {
		t, err := time.Parse(time.RFC3339, in.CreatedAfter)
		if err != nil {
			return views.PurchaseOrderPage{}, mapError(shared.Invalid("createdAfter must be an RFC 3339 date-time"))
		}
		q.CreatedAfter = &t
	}
	if in.Limit < 0 {
		return views.PurchaseOrderPage{}, mapError(shared.Invalid("limit must be a positive integer"))
	}
	page, err := d.Reads.PollPurchaseOrders(ctx, q)
	if err != nil {
		return views.PurchaseOrderPage{}, mapError(err)
	}
	return page, nil
}

func (d Deps) getPurchaseOrder(ctx context.Context, in vendorPOInput) (views.PurchaseOrder, error) {
	po, err := d.Reads.GetPurchaseOrder(ctx, in.VendorID, in.PONumber)
	if err != nil {
		return views.PurchaseOrder{}, mapError(err)
	}
	return po, nil
}

// pollInboundShipments mirrors GET /vendor/inbound-shipments: the SHIPPED
// shipments bound for the vendor's nodes, oldest first, a page at a time.
// A malformed createdAfter or a negative limit is refused here exactly as
// the REST adapter refuses it, with validation-failed.
func (d Deps) pollInboundShipments(ctx context.Context, in pollInboundShipmentsInput) (views.InboundShipmentPage, error) {
	q := requests.InboundPollQuery{VendorID: in.VendorID, NodeID: in.NodeID, NextToken: in.NextToken, Limit: in.Limit}
	if in.CreatedAfter != "" {
		t, err := time.Parse(time.RFC3339, in.CreatedAfter)
		if err != nil {
			return views.InboundShipmentPage{}, mapError(shared.Invalid("createdAfter must be an RFC 3339 date-time"))
		}
		q.CreatedAfter = &t
	}
	if in.Limit < 0 {
		return views.InboundShipmentPage{}, mapError(shared.Invalid("limit must be a positive integer"))
	}
	page, err := d.Reads.PollInboundShipments(ctx, q)
	if err != nil {
		return views.InboundShipmentPage{}, mapError(err)
	}
	return page, nil
}

func (d Deps) getInboundShipment(ctx context.Context, in vendorShipmentInput) (views.InboundShipment, error) {
	sh, err := d.Reads.GetInboundShipment(ctx, in.VendorID, in.ShipmentID)
	if err != nil {
		return views.InboundShipment{}, mapError(err)
	}
	return sh, nil
}

func (d Deps) listVendorNodes(ctx context.Context, in vendorInput) (views.VendorNodeList, error) {
	list, err := d.Reads.ListVendorNodes(ctx, in.VendorID)
	if err != nil {
		return views.VendorNodeList{}, mapError(err)
	}
	return list, nil
}

func (d Deps) getVendorNode(ctx context.Context, in vendorNodeInput) (views.VendorNode, error) {
	n, err := d.Reads.GetNode(ctx, in.VendorID, in.NodeID)
	if err != nil {
		return views.VendorNode{}, mapError(err)
	}
	return n, nil
}

func (d Deps) getNodeScorecard(ctx context.Context, in vendorNodeInput) (views.Scorecard, error) {
	sc, err := d.Reads.GetScorecard(ctx, in.VendorID, in.NodeID)
	if err != nil {
		return views.Scorecard{}, mapError(err)
	}
	return sc, nil
}

func (d Deps) getTransaction(ctx context.Context, in vendorTransactionInput) (views.Transaction, error) {
	tx, err := d.Reads.GetTransaction(ctx, in.VendorID, in.TransactionID)
	if err != nil {
		return views.Transaction{}, mapError(err)
	}
	return tx, nil
}

func (d Deps) getVendor(ctx context.Context, in vendorInput) (views.VendorAccount, error) {
	v, err := d.Reads.GetVendor(ctx, in.VendorID)
	if err != nil {
		return views.VendorAccount{}, mapError(err)
	}
	return v, nil
}

// --- registry -----------------------------------------------------------------

// registerTools adds the nine read-only tools, one per GET operation of the
// Vendor API, grouped the way the Vendor API groups them. Every tool is
// annotated read-only, idempotent and closed-world; TestToolSurface pins the
// set and fails the build on any write-verb name (docs/adr/0006).
func (d Deps) registerTools(server *mcp.Server) {
	closedWorld := false
	readOnly := &mcp.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true, OpenWorldHint: &closedWorld}
	d.registerOrderTools(server, readOnly)
	d.registerInboundTools(server, readOnly)
	d.registerNodeTools(server, readOnly)
	d.registerAccountTools(server, readOnly)
}

// registerOrderTools registers the purchase-order reads.
func (d Deps) registerOrderTools(server *mcp.Server, readOnly *mcp.ToolAnnotations) {

	addTool(server, &mcp.Tool{
		Name: "poll_purchase_orders",
		Description: "Page through the purchase orders released to one vendor, oldest first: poNumber, nodeId, state " +
			"(ISSUED, ACK_PROCESSING, ACCEPTED, REJECTED, EXPIRED, SHIPMENT_PROCESSING, SHIPPED or CANCELLED), issuedAt, " +
			"requiredShipBy, ackBy, the lines (lineNo, listingId, quantity) and, once they exist, the acknowledgement, the " +
			"labels and the tracking numbers. Never a ship-to address, an orderId or a delivery promise. Narrow to one " +
			"node (nodeId) or to orders issued after a moment (createdAfter, RFC 3339); pass the returned nextToken to get " +
			"the next page, absent on the last one. Read-only; an unknown vendor fails with not-found, a malformed one " +
			"with validation-failed, a node of another vendor with node-not-owned-by-vendor and a bad nextToken with " +
			"validation-failed.",
		Annotations: readOnly,
	}, d.pollPurchaseOrders)

	addTool(server, &mcp.Tool{
		Name: "get_purchase_order",
		Description: "Read one purchase order by poNumber as the vendor sees it: state and windows (issuedAt, " +
			"requiredShipBy, ackBy), the lines, the acknowledgement once its transaction succeeded, the shipping labels " +
			"and tracking numbers, and shippedAt once confirmed. Read-only; an order of another vendor fails with " +
			"node-not-owned-by-vendor, an unknown one with not-found and a malformed vendorId with validation-failed.",
		Annotations: readOnly,
	}, d.getPurchaseOrder)
}

// registerInboundTools registers the inbound-shipment reads (ADR 0005).
func (d Deps) registerInboundTools(server *mcp.Server, readOnly *mcp.ToolAnnotations) {
	addTool(server, &mcp.Tool{
		Name: "poll_inbound_shipments",
		Description: "Page through the inbound shipments bound for one vendor's nodes, oldest shipment first: " +
			"shipmentId, sellerId, nodeId, shippedAt, status (SHIPPED or RECEIVED) and the pallet-and-carton tree " +
			"(pallet SSCC, carton SSCC and parent, kind, eaches, lot, expiry). Narrow to one node (nodeId) or to " +
			"shipments shipped after a moment (createdAfter, RFC 3339); pass the returned nextToken to get the next " +
			"page, absent on the last one. Read-only; an unknown vendor fails with not-found, a malformed one with " +
			"validation-failed, a node of another vendor with node-not-owned-by-vendor and a bad nextToken with " +
			"validation-failed.",
		Annotations: readOnly,
	}, d.pollInboundShipments)

	addTool(server, &mcp.Tool{
		Name: "get_inbound_shipment",
		Description: "Read one inbound shipment by shipmentId: the pallets with the cartons stacked on them, the " +
			"top-level cartons, and every item line in eaches (gtin, sku, qtyEaches, lot, expiry). A shipment that has " +
			"not shipped yet, or is bound for another vendor's node, is as absent as an unknown one. Read-only; fails " +
			"with not-found for an unknown or not-yet-shipped shipment and validation-failed for a malformed id.",
		Annotations: readOnly,
	}, d.getInboundShipment)
}

// registerNodeTools registers the node and scorecard reads.
func (d Deps) registerNodeTools(server *mcp.Server, readOnly *mcp.ToolAnnotations) {
	addTool(server, &mcp.Tool{
		Name: "list_vendor_nodes",
		Description: "List the fulfilment nodes the network has registered for one vendor, sorted by nodeId: type and " +
			"subtype, status (PROPOSED, ACTIVE, SUSPENDED or RETIRED), region, timezone, handling time and profile " +
			"revision. The local copy of node-topology events, as GET /vendor/nodes serves it. Read-only; an unknown " +
			"vendor fails with not-found and a malformed one with validation-failed.",
		Annotations: readOnly,
	}, d.listVendorNodes)

	addTool(server, &mcp.Tool{
		Name: "get_vendor_node",
		Description: "Read one fulfilment node of a vendor: type, subtype, status, region, timezone, handling time " +
			"minutes and profile revision. A node the network registered under another vendor is as absent as an unknown " +
			"one. Read-only; fails with node-not-owned-by-vendor for a node of another vendor, not-found for an unknown " +
			"one and validation-failed for a malformed id.",
		Annotations: readOnly,
	}, d.getVendorNode)

	addTool(server, &mcp.Tool{
		Name: "get_node_scorecard",
		Description: "Read a vendor node's rolling scorecards, relayed live from the node-performance context: per " +
			"window (7d, 30d, 90d) the grade, on-time handover rate, late acknowledgement rate, reject rate, expiry rate, " +
			"promise-kept rate and feed freshness p95, with the observation count. A node without a scorecard yet fails " +
			"with not-found. Read-only; the node must belong to the vendor (node-not-owned-by-vendor) and a neighbour " +
			"outage surfaces as upstream-unavailable or upstream-refused.",
		Annotations: readOnly,
	}, d.getNodeScorecard)
}

// registerAccountTools registers the transaction and vendor-account reads.
func (d Deps) registerAccountTools(server *mcp.Server, readOnly *mcp.ToolAnnotations) {
	addTool(server, &mcp.Tool{
		Name: "get_transaction",
		Description: "Follow one asynchronous write of a vendor by the transactionId its 202 answer returned: kind " +
			"(ACKNOWLEDGEMENT, INVENTORY, NODE_REGISTRATION, NODE_PROFILE or SHIPMENT_CONFIRMATION), status (PROCESSING, " +
			"SUCCESS or FAILURE), receivedAt and processedAt, the node and poNumber it concerns and, when it failed, " +
			"every error (code, message, path). Read-only; a transaction of another vendor is not-found, an unknown one " +
			"not-found, a malformed vendorId validation-failed.",
		Annotations: readOnly,
	}, d.getTransaction)

	addTool(server, &mcp.Tool{
		Name: "get_vendor",
		Description: "Read a vendor account: name, acknowledgement window in minutes (how long after issuedAt an " +
			"acknowledgement is accepted) and state (ACTIVE or SUSPENDED). Reads stay allowed while the vendor is " +
			"suspended; only writes are refused. Read-only; an unknown vendor fails with not-found and a malformed one " +
			"with validation-failed.",
		Annotations: readOnly,
	}, d.getVendor)
}

// addTool registers one tool. A handler error is returned to the SDK as the
// handler's error, which the SDK turns into an isError tool result (never a
// transport/protocol failure), carrying the error text.
func addTool[In, Out any](
	server *mcp.Server,
	tool *mcp.Tool,
	handle func(context.Context, In) (Out, error),
) {
	mcp.AddTool(server, tool, func(ctx context.Context, _ *mcp.CallToolRequest, in In) (*mcp.CallToolResult, Out, error) {
		out, err := handle(ctx, in)
		if err != nil {
			var zero Out
			return nil, zero, err
		}
		return nil, out, nil
	})
}
