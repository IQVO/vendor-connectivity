package http

import (
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/claudioed/vendor-connectivity/internal/adapters/inbound/http/page"
	"github.com/claudioed/vendor-connectivity/internal/application/requests"
	"github.com/claudioed/vendor-connectivity/internal/application/views"
)

// vendorID reads the required `vendorId` query parameter (G32). An absent one
// reaches the use case empty and is refused there with validation-failed.
func vendorID(r *http.Request) string { return r.URL.Query().Get("vendorId") }

// pageParams validates the paging query of a collection GET: an undeclared
// parameter is 400 unknown-query-parameter, a bad limit 400 invalid-limit and
// a repeated cursor 400 invalid-cursor. It writes the problem and reports false.
func pageParams(w http.ResponseWriter, r *http.Request, filters ...string) (limit int, cursor string, ok bool) {
	if name := page.Unknown(r, filters...); name != "" {
		writeProblem(w, r, unknownQueryParameter, "the query parameter "+strconv.Quote(name)+" is not declared by this operation")
		return 0, "", false
	}
	limit, err := page.Limit(r)
	if err != nil {
		writeError(w, r, err)
		return 0, "", false
	}
	if vals := r.URL.Query()["cursor"]; len(vals) > 1 {
		writeError(w, r, page.ErrInvalidCursor)
		return 0, "", false
	}
	return limit, r.URL.Query().Get("cursor"), true
}

// accepted answers an asynchronous write: 202 and the transaction to reconcile.
func accepted(w http.ResponseWriter, v views.TransactionAccepted, err error, r *http.Request) {
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, v)
}

// respond answers a read or a synchronous write with 200 and v.
func respond[T any](w http.ResponseWriter, r *http.Request, v T, err error) {
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// pollParams reads the query shared by both polls: nodeId, an RFC 3339
// createdAfter, and the page parameters (limit 1..500, opaque cursor). A query
// parameter the poll does not declare is 400 unknown-query-parameter.
func pollParams(w http.ResponseWriter, r *http.Request) (nodeID, cursor string, createdAfter *time.Time, limit int, ok bool) {
	limit, cursor, ok = pageParams(w, r, "vendorId", "createdAfter", "nodeId")
	if !ok {
		return "", "", nil, 0, false
	}
	nodeID = r.URL.Query().Get("nodeId")
	if raw := r.URL.Query().Get("createdAfter"); raw != "" {
		t, perr := time.Parse(time.RFC3339, raw)
		if perr != nil {
			writeError(w, r, fmt.Errorf("%w: createdAfter must be an RFC 3339 date-time", errMalformedRequest))
			return "", "", nil, 0, false
		}
		createdAfter = &t
	}
	return nodeID, cursor, createdAfter, limit, true
}

func (s *Server) handlePoll(w http.ResponseWriter, r *http.Request) {
	nodeID, cursor, after, limit, ok := pollParams(w, r)
	if !ok {
		return
	}
	res, err := s.Service.PollPurchaseOrders(r.Context(), requests.PollQuery{VendorID: vendorID(r), NodeID: nodeID, NextToken: cursor, CreatedAfter: after, Limit: limit})
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, page.Page[views.PurchaseOrder]{Items: res.PurchaseOrders, NextCursor: res.NextToken})
}

func (s *Server) handlePollInbound(w http.ResponseWriter, r *http.Request) {
	nodeID, cursor, after, limit, ok := pollParams(w, r)
	if !ok {
		return
	}
	res, err := s.Service.PollInboundShipments(r.Context(), requests.InboundPollQuery{VendorID: vendorID(r), NodeID: nodeID, NextToken: cursor, CreatedAfter: after, Limit: limit})
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, page.Page[views.InboundShipment]{Items: res.InboundShipments, NextCursor: res.NextToken})
}

func (s *Server) handleGetInboundShipment(w http.ResponseWriter, r *http.Request) {
	sh, err := s.Service.GetInboundShipment(r.Context(), vendorID(r), chi.URLParam(r, "shipmentId"))
	respond(w, r, sh, err)
}

func (s *Server) handleInboundReceipt(w http.ResponseWriter, r *http.Request) {
	var in requests.InboundReceipt
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, r, err)
		return
	}
	v, err := s.Service.SubmitInboundReceipt(r.Context(), in)
	accepted(w, v, err, r)
}

func (s *Server) handleGetPurchaseOrder(w http.ResponseWriter, r *http.Request) {
	po, err := s.Service.GetPurchaseOrder(r.Context(), vendorID(r), chi.URLParam(r, "poNumber"))
	respond(w, r, po, err)
}

func (s *Server) handleAcknowledge(w http.ResponseWriter, r *http.Request) {
	var in requests.Acknowledgement
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, r, err)
		return
	}
	v, err := s.Service.SubmitAcknowledgement(r.Context(), in)
	accepted(w, v, err, r)
}

func (s *Server) handleInventory(w http.ResponseWriter, r *http.Request) {
	var in requests.InventoryFeed
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, r, err)
		return
	}
	v, err := s.Service.SubmitInventory(r.Context(), in)
	accepted(w, v, err, r)
}

func (s *Server) handleListNodes(w http.ResponseWriter, r *http.Request) {
	limit, cursor, ok := pageParams(w, r, "vendorId")
	if !ok {
		return
	}
	var afterID string
	if cursor != "" {
		pos, err := page.Decode(cursor)
		if err != nil {
			writeError(w, r, err)
			return
		}
		afterID = pos.ID
	}
	list, err := s.Service.ListVendorNodesPage(r.Context(), vendorID(r), afterID, limit)
	if err != nil {
		writeError(w, r, err)
		return
	}
	out := page.Page[views.VendorNode]{Items: list.Nodes}
	if list.Next != "" {
		out.NextCursor = page.Encode(page.Position{Sort: list.Next, ID: list.Next})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleRegisterNode(w http.ResponseWriter, r *http.Request) {
	var in requests.NodeRegistration
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, r, err)
		return
	}
	v, err := s.Service.RegisterVendorNode(r.Context(), in)
	accepted(w, v, err, r)
}

func (s *Server) handleGetNode(w http.ResponseWriter, r *http.Request) {
	n, err := s.Service.GetNode(r.Context(), vendorID(r), chi.URLParam(r, "nodeId"))
	respond(w, r, n, err)
}

func (s *Server) handleDeclareProfile(w http.ResponseWriter, r *http.Request) {
	var in requests.NodeProfile
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, r, err)
		return
	}
	v, err := s.Service.DeclareNodeProfile(r.Context(), chi.URLParam(r, "nodeId"), in)
	accepted(w, v, err, r)
}

func (s *Server) handleScorecard(w http.ResponseWriter, r *http.Request) {
	sc, err := s.Service.GetScorecard(r.Context(), vendorID(r), chi.URLParam(r, "nodeId"))
	respond(w, r, sc, err)
}

func (s *Server) handleLabel(w http.ResponseWriter, r *http.Request) {
	var in requests.LabelRequest
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, r, err)
		return
	}
	resp, err := s.Service.RequestLabel(r.Context(), in)
	respond(w, r, resp, err)
}

func (s *Server) handleShipment(w http.ResponseWriter, r *http.Request) {
	var in requests.ShipmentConfirmation
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, r, err)
		return
	}
	v, err := s.Service.SubmitShipmentConfirmation(r.Context(), in)
	accepted(w, v, err, r)
}

func (s *Server) handleGetTransaction(w http.ResponseWriter, r *http.Request) {
	tx, err := s.Service.GetTransaction(r.Context(), vendorID(r), chi.URLParam(r, "transactionId"))
	respond(w, r, tx, err)
}

func (s *Server) handleOnboard(w http.ResponseWriter, r *http.Request) {
	var in requests.OnboardVendor
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, r, err)
		return
	}
	v, created, err := s.Service.OnboardVendor(r.Context(), in)
	if err != nil {
		writeError(w, r, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, status, v)
}

func (s *Server) handleGetVendor(w http.ResponseWriter, r *http.Request) {
	v, err := s.Service.GetVendor(r.Context(), chi.URLParam(r, "vendorId"))
	respond(w, r, v, err)
}

// handleSuspendVendor serves POST /vendors/{vendorId}/suspensions.
func (s *Server) handleSuspendVendor(w http.ResponseWriter, r *http.Request) {
	var body requests.SuspendVendor
	if err := decodeOptionalJSON(r, &body); err != nil {
		writeError(w, r, err)
		return
	}
	v, err := s.Service.SuspendVendor(r.Context(), chi.URLParam(r, "vendorId"), body.Reason)
	respond(w, r, v, err)
}

// handleReinstateVendor serves POST /vendors/{vendorId}/reinstatements.
func (s *Server) handleReinstateVendor(w http.ResponseWriter, r *http.Request) {
	v, err := s.Service.ReinstateVendor(r.Context(), chi.URLParam(r, "vendorId"))
	respond(w, r, v, err)
}
