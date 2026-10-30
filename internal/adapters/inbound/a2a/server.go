// Package a2a is the inbound Agent2Agent adapter: it exposes this bounded
// context to the fleet's agent ecosystem as a third driving adapter over the
// same application-layer read queries the REST and MCP adapters use. It is
// built on the official A2A Go SDK (a2aproject/a2a-go) and speaks JSON-RPC 2.0
// over HTTP, with the Agent Card served at the well-known path.
//
// Per docs/adr/0009-a2a-inbound-adapter.md, A2A is the DELEGATION channel
// between bounded contexts: an agent client sends a goal-shaped skill request
// as a structured message part and this agent answers from its own state
// through its own use cases. It is not a data-sync channel: facts between
// contexts still travel exclusively as CloudEvents on Kafka, published through
// the transactional outbox (ADR 0004).
//
// This agent is READ-ONLY, exactly like the MCP adapter (ADR 0006): both
// skills read vendor-connectivity data. The submissions, the vendor lifecycle,
// the node registrations and the inbound receipts stay on REST, on the
// consumers and on the background loops of cmd/api. Read-only is structural:
// the adapter holds a Reads interface naming exactly the two application
// queries behind its skills, so no write use case is reachable through it.
// This package contains no outbound A2A client and no auth of any kind
// (fleet-wide revert 2026-09-11).
//
// Like the MCP adapter, this package depends inward on the application layer
// and the domain only, never on an outbound adapter, and nothing else may
// depend on it (internal/architecture's TestA2AAdapterDependencyRule). The
// composition root (cmd/a2a) wires concrete repositories into the same
// application Service cmd/api builds.
package a2a

import (
	"context"
	"iter"
	"net/http"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv"

	"github.com/claudioed/vendor-connectivity/internal/application/views"
)

// Config carries the deployment-specific identity of this agent: the absolute
// URL clients use to reach its JSON-RPC endpoint (advertised on the Agent
// Card) and the card version. It is supplied by the composition root from
// A2A_AGENT_URL; the card MUST advertise a URL the fleet's agent clients can
// actually dial (in-cluster: http://<release>-a2a.<ns>.svc.cluster.local:8093/).
type Config struct {
	AgentURL string
	Version  string
}

// Reads is EXACTLY the read surface behind the two skills: the application
// queries the REST adapter's GET /vendor/purchase-orders/{poNumber} and GET
// /vendor/nodes/{nodeId}/scorecard routes call, and the MCP tools
// get_purchase_order and get_node_scorecard wrap. Reads and writes share the
// application Service type, so read-only is enforced by the narrowest
// possible interface: through Reads no write use case can even be named.
type Reads interface {
	// GetPurchaseOrder backs GET /vendor/purchase-orders/{poNumber}.
	GetPurchaseOrder(ctx context.Context, vendorID, poNumber string) (views.PurchaseOrder, error)
	// GetScorecard backs GET /vendor/nodes/{nodeId}/scorecard.
	GetScorecard(ctx context.Context, vendorID, nodeID string) (views.Scorecard, error)
}

// Deps is everything the A2A skills need, injected by the composition root.
// The adapter never constructs an outbound adapter itself, never writes and
// holds no write use case at all.
type Deps struct {
	Reads Reads
}

// Card builds this agent's Agent Card (ADR 0009). The surface is two curated,
// goal-shaped READ skills derived from the MCP tools get_purchase_order and
// get_node_scorecard (ADR 0006): this context's MCP charter is entirely
// read-only, so the agent is read-only too and never widens what the context
// exposes. The card declares no security schemes on purpose: this surface is
// unauthenticated by the same fleet-wide decision as REST and MCP; schemes
// are added here, not in a redesign, if that decision is ever reversed.
func Card(cfg Config) *a2a.AgentCard {
	return &a2a.AgentCard{
		Name:        "vendor-connectivity",
		Description: "Vendor Connectivity agent of the retail network: the read-only Open Host Service for vendors that operate fulfilment nodes. It answers where one purchase order released to a vendor stands (state, windows, lines, acknowledgement, labels, tracking) and how one of the vendor's nodes is performing (rolling scorecards relayed from node-performance). It never changes anything: acknowledgements, inventory feeds, shipment confirmations and label requests are REST operations, and facts leave as CloudEvents on Kafka.",
		Version:     cfg.Version,
		Provider: &a2a.AgentProvider{
			Org: "IQVO",
			URL: "https://github.com/IQVO/vendor-connectivity",
		},
		SupportedInterfaces: []*a2a.AgentInterface{
			a2a.NewAgentInterface(cfg.AgentURL, a2a.TransportProtocolJSONRPC),
		},
		DefaultInputModes:  []string{"application/json", "text/plain"},
		DefaultOutputModes: []string{"application/json", "text/plain"},
		Capabilities:       a2a.AgentCapabilities{Streaming: false},
		Skills: []a2a.AgentSkill{
			{
				ID:          skillGetPurchaseOrder,
				Name:        "Check where one purchase order stands",
				Description: "Read one purchase order released to a vendor, by vendorId and poNumber: state (ISSUED, ACK_PROCESSING, ACCEPTED, REJECTED, EXPIRED, SHIPMENT_PROCESSING, SHIPPED or CANCELLED), nodeId, the windows (issuedAt, requiredShipBy, ackBy), the lines (lineNo, listingId, quantity) and, once they exist, the acknowledgement, the labels, the tracking numbers and shippedAt. Never a ship-to address, an orderId or a delivery promise. Answers a 404 problem for an unknown vendor or order and for an order of another vendor (node-not-owned-by-vendor). Persists nothing.",
				Tags:        []string{"vendor", "purchase-order", "read"},
				InputModes:  []string{"application/json"},
				Examples: []string{
					`{"skill":"get_purchase_order","arguments":{"vendorId":"vend-south-1","poNumber":"PO-9a1b-0001"}}`,
				},
			},
			{
				ID:          skillGetNodeScorecard,
				Name:        "Read how one vendor node is performing",
				Description: "Read a vendor node's rolling scorecards, by vendorId and nodeId, relayed live from the node-performance context: per window (7d, 30d, 90d) the grade, on-time handover rate, late acknowledgement rate, reject rate, expiry rate, promise-kept rate and feed freshness p95, with the observation count. The node must belong to the vendor. Answers a 404 problem for an unknown vendor, a node of another vendor or a node without a scorecard yet, and a 503/502 problem when node-performance is unavailable or refuses. Persists nothing.",
				Tags:        []string{"vendor", "node", "scorecard", "read"},
				InputModes:  []string{"application/json"},
				Examples: []string{
					`{"skill":"get_node_scorecard","arguments":{"vendorId":"vend-south-1","nodeId":"node-76c2"}}`,
				},
			},
		},
	}
}

// NewExecutor returns the AgentExecutor that maps an incoming message to a
// skill and answers with a single agent Message (a message-only agent in A2A
// terms): both curated skills are transactional request/response reads, so
// there is no long-running Task lifecycle to track in this first A2A surface.
// The response carries the caller's contextId (and taskId when the client
// referenced one) so a delegating saga can group its interactions.
func NewExecutor(deps Deps) a2asrv.AgentExecutor {
	return a2asrv.AgentExecutorFunc(func(ctx context.Context, ec *a2asrv.ExecutorContext) iter.Seq2[a2a.Event, error] {
		return func(yield func(a2a.Event, error) bool) {
			yield(answer(ctx, deps, ec), nil)
		}
	})
}

// Handler wires the executor into the transport-agnostic A2A request handler
// and wraps it in the JSON-RPC 2.0 HTTP handler, the only transport this
// agent ships (ADR 0009), mirroring ADR 0006's "Streamable HTTP only"
// minimalism for MCP.
func Handler(executor a2asrv.AgentExecutor) http.Handler {
	return a2asrv.NewJSONRPCHandler(a2asrv.NewHandler(executor))
}
