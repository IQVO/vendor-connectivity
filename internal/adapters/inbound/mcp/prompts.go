package mcp

import (
	"context"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// triageVendorOrdersSOP is the operator standard-operating-procedure the
// triage_vendor_orders prompt hands to the model (Agentic Standard v2, AS-3;
// ADR 0011). It names tools but never calls them, and it encodes no hidden
// write: the MCP surface is read-only (ADR 0006), and every vendor write —
// an acknowledgement, an inventory feed, a shipment confirmation, a label
// request — stays a REST operation a human decides on. The prompt only
// standardises how an agent reads this context's state and reports.
const triageVendorOrdersSOP = `You are helping a network operator triage one vendor's purchase orders. Use only the MCP tools of vendor-connectivity; never assume state, never invent identifiers.

Procedure:
1. Call get_vendor(vendorId). If the account is SUSPENDED, say so first and stop: reads still work, but every write of that vendor is refused by the REST API, so there is nothing to chase until the account is active again. The acknowledgement window (ackWindowMinutes) tells you how long after issuedAt an acknowledgement is still accepted.
2. Call list_vendor_nodes(vendorId) (or read the warehouse://vendor-connectivity/vendors/<vendorId>/nodes resource) so every later nodeId you see has a home: a node that is not on the roster is not this vendor's.
3. Call poll_purchase_orders(vendorId) for the open picture. States that need action: ISSUED (awaiting the vendor's acknowledgement), ACK_PROCESSING (a write is in flight), REJECTED and EXPIRED (dead ends worth reporting), SHIPMENT_PROCESSING and SHIPPED (healthy). Narrow with nodeId or createdAfter when the operator asked for one node or window; page with nextToken until it is absent.
4. For every ISSUED order call get_purchase_order(vendorId, poNumber) and check it against its own windows: ackBy is the moment the acknowledgement is no longer accepted, requiredShipBy the moment the network needs the shipment. An ISSUED order whose ackBy has passed is the urgent finding — say how late it is.
5. When a write is in flight (ACK_PROCESSING) or the operator asks what became of one, call get_transaction(vendorId, transactionId) with the id the 202 answer returned and report its status and, on FAILURE, every error code.

Interpretation:
- One late ISSUED order is a vendor to chase; many across nodes and days is a vendor-performance finding — report both readings and let the operator decide.
- A REJECTED order carries the vendor's reason on its acknowledgement; do not re-litigate it, just report it with the reason.
- get_node_scorecard(vendorId, nodeId) tells you whether slowness is chronic (grades, on-time handover, late acknowledgement rates per 7d/30d/90d window). It relays from node-performance: an upstream-unavailable answer is a neighbour outage, not a vendor verdict.

Escalate to a human when: an order is past its ackBy window, a transaction sits in FAILURE with errors the vendor cannot retry, or a suspended account still shows open ISSUED orders. You never submit anything: acknowledgements, inventory feeds, shipment confirmations and label requests are REST operations the operator performs after reading your report.

Done means: the vendor's account state, its node roster, every open order with its state and deadline pressure, the transactions behind every in-flight write, and a one-line priority list of what the operator should do next.`

// registerPrompts adds the workflow prompt (the operator SOP).
func (d Deps) registerPrompts(server *mcp.Server) {
	server.AddPrompt(&mcp.Prompt{
		Name:        "triage_vendor_orders",
		Description: "Standard operating procedure for triaging one vendor's purchase orders: account state, node roster, open orders against their acknowledgement windows, in-flight transactions, and what to escalate to a human.",
		Arguments: []*mcp.PromptArgument{{
			Name:        "vendorId",
			Description: "the vendor account to triage, e.g. vend-south-1; every tool call in the procedure is scoped to it",
			Required:    true,
		}},
	}, func(_ context.Context, req *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
		vendorID := req.Params.Arguments["vendorId"]
		if vendorID == "" {
			return nil, fmt.Errorf("arguments: vendorId is required")
		}
		sop := fmt.Sprintf("Vendor under triage: %s.\n\n%s", vendorID, triageVendorOrdersSOP)
		return &mcp.GetPromptResult{
			Description: "How to triage " + vendorID + "'s purchase orders: read the account, the roster and every open order against its windows, follow in-flight transactions, escalate the late ones.",
			Messages: []*mcp.PromptMessage{{
				Role:    "user",
				Content: &mcp.TextContent{Text: sop},
			}},
		}, nil
	})
}
