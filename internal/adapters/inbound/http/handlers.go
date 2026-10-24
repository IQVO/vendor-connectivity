package http

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/claudioed/vendor-connectivity/internal/application/requests"
	"github.com/claudioed/vendor-connectivity/internal/application/views"
)

// vendorID reads the required `vendorId` query parameter (G32). An absent one
// reaches the use case empty and is refused there with validation-failed.
func vendorID(r *http.Request) string { return r.URL.Query().Get("vendorId") }

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

// pollParams reads the query shared by both polls: nodeId, nextToken, an RFC
// 3339 createdAfter and a positive limit.
func pollParams(r *http.Request) (nodeID, nextToken string, createdAfter *time.Time, limit int, err error) {
	q := r.URL.Query()
	nodeID, nextToken = q.Get("nodeId"), q.Get("nextToken")
	if raw := q.Get("createdAfter"); raw != "" {
		t, perr := time.Parse(time.RFC3339, raw)
		if perr != nil {
			return "", "", nil, 0, fmt.Errorf("%w: createdAfter must be an RFC 3339 date-time", errMalformedRequest)
		}
		createdAfter = &t
	}
	if raw := q.Get("limit"); raw != "" {
		n, perr := strconv.Atoi(raw)
		if perr != nil || n < 1 {
			return "", "", nil, 0, fmt.Errorf("%w: limit must be a positive integer", errMalformedRequest)
		}
		limit = n
	}
	return nodeID, nextToken, createdAfter, limit, nil
}

func (s *Server) handlePoll(w http.ResponseWriter, r *http.Request) {
	nodeID, token, after, limit, err := pollParams(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	page, err := s.Service.PollPurchaseOrders(r.Context(), requests.PollQuery{VendorID: vendorID(r), NodeID: nodeID, NextToken: token, CreatedAfter: after, Limit: limit})
	respond(w, r, page, err)
}

func (s *Server) handlePollInbound(w http.ResponseWriter, r *http.Request) {
	nodeID, token, after, limit, err := pollParams(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	page, err := s.Service.PollInboundShipments(r.Context(), requests.InboundPollQuery{VendorID: vendorID(r), NodeID: nodeID, NextToken: token, CreatedAfter: after, Limit: limit})
	respond(w, r, page, err)
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
	list, err := s.Service.ListVendorNodes(r.Context(), vendorID(r))
	respond(w, r, list, err)
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

// errUnknownVendorAction is the 404 of /vendors/{id}:<unknown>.
var errUnknownVendorAction = errors.New("no such vendor action")

// handleVendorAction serves POST /vendors/{vendorId}:suspend and :reinstate.
func (s *Server) handleVendorAction(w http.ResponseWriter, r *http.Request) {
	id, action, ok := strings.Cut(chi.URLParam(r, "vendorAction"), ":")
	if !ok || id == "" {
		writeError(w, r, errUnknownVendorAction)
		return
	}
	switch action {
	case "suspend":
		var body requests.SuspendVendor
		if err := decodeOptionalJSON(r, &body); err != nil {
			writeError(w, r, err)
			return
		}
		v, err := s.Service.SuspendVendor(r.Context(), id, body.Reason)
		respond(w, r, v, err)
	case "reinstate":
		v, err := s.Service.ReinstateVendor(r.Context(), id)
		respond(w, r, v, err)
	default:
		writeError(w, r, errUnknownVendorAction)
	}
}
