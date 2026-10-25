// Package http is the REST inbound adapter: the Vendor API of
// apis/openapi.yaml on a chi router, RFC 9457 problem documents for every
// error, and the liveness/readiness endpoints. It has no auth middleware
// (fleet decision; internal/architecture's TestNoAuthMiddlewareReintroduced):
// the vendor is named in the request and ownership is a consistency check (G32).
package http

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/claudioed/vendor-connectivity/internal/application/usecases"
)

// Server holds the application service the handlers call.
type Server struct {
	Service *usecases.Service

	// Idempotency wraps every write (the fleet Idempotency-Key middleware,
	// built by cmd/ over the Postgres pool); the shipped binary always sets it,
	// nil (handler unit tests) leaves the routes unwrapped.
	Idempotency func(http.Handler) http.Handler
	// Limiter backs RATE_LIMIT_RPS over /vendor/*; nil means unlimited.
	Limiter *Limiter
	// Readiness backs GET /readyz: the drain gate plus the Postgres and Kafka
	// checks (NewReadiness). The shipped binary always builds it with
	// NewReadiness; nil (handler unit tests) means always ready.
	Readiness *Readiness
}

// NewRouter builds the chi router for every operation of apis/openapi.yaml.
func NewRouter(s *Server) http.Handler {
	r := chi.NewRouter()
	// A NUL (U+0000) in the path, the query or a body is a uniform client
	// error here, never a Postgres 22021 -> 500; it runs before the
	// idempotency middleware so a rejected request never records its key.
	r.Use(rejectNUL)

	r.Get("/healthz", handleHealthz)
	r.Get("/readyz", s.handleReadyz)

	write := func(h http.HandlerFunc) http.Handler {
		if s.Idempotency == nil {
			return h
		}
		return s.Idempotency(h)
	}

	r.Group(func(r chi.Router) {
		r.Use(rateLimit(s.Limiter))
		r.Get("/vendor/purchase-orders", s.handlePoll)
		r.Get("/vendor/purchase-orders/{poNumber}", s.handleGetPurchaseOrder)
		r.Method(http.MethodPost, "/vendor/acknowledgements", write(s.handleAcknowledge))
		r.Method(http.MethodPost, "/vendor/inventory", write(s.handleInventory))
		r.Get("/vendor/nodes", s.handleListNodes)
		r.Method(http.MethodPost, "/vendor/nodes", write(s.handleRegisterNode))
		r.Get("/vendor/nodes/{nodeId}", s.handleGetNode)
		r.Method(http.MethodPut, "/vendor/nodes/{nodeId}/profile", write(s.handleDeclareProfile))
		r.Get("/vendor/nodes/{nodeId}/scorecard", s.handleScorecard)
		r.Method(http.MethodPost, "/vendor/shipping-labels", write(s.handleLabel))
		r.Method(http.MethodPost, "/vendor/shipment-confirmations", write(s.handleShipment))
		r.Get("/vendor/transactions/{transactionId}", s.handleGetTransaction)
		r.Get("/vendor/inbound-shipments", s.handlePollInbound)
		r.Get("/vendor/inbound-shipments/{shipmentId}", s.handleGetInboundShipment)
		r.Method(http.MethodPost, "/vendor/inbound-receipts", write(s.handleInboundReceipt))
	})

	r.Method(http.MethodPost, "/vendors", write(s.handleOnboard))
	r.Get("/vendors/{vendorId}", s.handleGetVendor)
	// chi cannot route a ":verb" suffix inside one segment, so the two vendor
	// actions share a pattern and are dispatched on the suffix.
	r.Method(http.MethodPost, "/vendors/{vendorAction}", write(s.handleVendorAction))
	return r
}

func handleHealthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, statusBody{Status: "ok"})
}
