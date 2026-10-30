package a2a

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
)

// tracerName is the OTel instrumentation scope for A2A skill spans, mirroring
// the MCP adapter's mcp.tool spans.
const tracerName = "github.com/claudioed/vendor-connectivity/internal/adapters/inbound/a2a"

// Skill ids advertised on the Agent Card (see Card). They are the MCP tool
// names of the same reads (ADR 0006), so one name identifies one question
// across both agent-facing surfaces.
const (
	skillGetPurchaseOrder = "get_purchase_order"
	skillGetNodeScorecard = "get_node_scorecard"
)

// skillRequest is the wire contract of every skill call: the first message
// part carries (as a Data part, or a Text part holding the same JSON) the
// skill id and its arguments. Strict on purpose: an agent client is
// machine-to-machine, so unknown fields are rejected, never ignored. (Go's
// encoding/json matches field names case-insensitively, so "ponumber" still
// reaches poNumber; only genuinely unknown names are refused.)
type skillRequest struct {
	Skill     string          `json:"skill"`
	Arguments json.RawMessage `json:"arguments"`
}

// purchaseOrderArgs and scorecardArgs are the per-skill argument shapes.
type purchaseOrderArgs struct {
	VendorID string `json:"vendorId"`
	PONumber string `json:"poNumber"`
}

type scorecardArgs struct {
	VendorID string `json:"vendorId"`
	NodeID   string `json:"nodeId"`
}

// answer maps one incoming message to a skill and returns the agent's reply
// Message. It is the whole executor body: this is a message-only agent, so a
// skill run is exactly one request mapped to one answer (ADR 0009).
func answer(ctx context.Context, deps Deps, ec *a2asrv.ExecutorContext) a2a.Event {
	req, prob := parseSkillRequest(ec)
	if prob != nil {
		return reply(ec, envelope{Problem: prob}, prob.summary())
	}

	ctx, span := otel.Tracer(tracerName).Start(ctx, "a2a.skill "+req.Skill)
	defer span.End()
	span.SetAttributes(attribute.String("a2a.skill", req.Skill))

	dto, prob := run(ctx, deps, req)
	if prob != nil {
		span.SetStatus(codes.Error, prob.Detail)
		return reply(ec, envelope{Skill: req.Skill, Problem: prob}, prob.summary())
	}
	return reply(ec, envelope{Skill: req.Skill, Data: &dto}, dto.Summary())
}

// run dispatches one validated request to its skill.
func run(ctx context.Context, deps Deps, req skillRequest) (skillDTO, *problem) {
	switch req.Skill {
	case skillGetPurchaseOrder:
		return deps.getPurchaseOrder(ctx, req.Arguments)
	case skillGetNodeScorecard:
		return deps.getNodeScorecard(ctx, req.Arguments)
	default:
		return skillDTO{}, problemUnknownSkill(req.Skill)
	}
}

// reply builds the agent Message for one skill run: a Data part carrying the
// structured envelope and a Text part carrying the one-line summary, bound to
// the caller's context (and task, when the client referenced one).
func reply(ec *a2asrv.ExecutorContext, env envelope, summary string) a2a.Event {
	return a2a.NewMessageForTask(a2a.MessageRoleAgent, ec, a2a.NewDataPart(env), a2a.NewTextPart(summary))
}

// parseSkillRequest extracts the skillRequest from the message's first part.
// A Data part (or a Text part holding the same JSON object) is accepted;
// anything else is a request-shape problem, answered, not raised, because a
// malformed delegation is a business answer for the calling agent, not a
// transport failure.
func parseSkillRequest(ec *a2asrv.ExecutorContext) (skillRequest, *problem) {
	msg := ec.Message
	if msg == nil || len(msg.Parts) == 0 {
		return skillRequest{}, problemMalformedRequest("the message carries no parts")
	}
	raw, err := partJSON(msg.Parts[0])
	if err != nil {
		return skillRequest{}, problemMalformedRequest(fmt.Sprintf("the first message part is not a skill request: %v", err))
	}
	var req skillRequest
	if err := decodeStrict(raw, &req); err != nil {
		return skillRequest{}, problemMalformedRequest(fmt.Sprintf("the skill request is not valid: %v", err))
	}
	if req.Skill == "" {
		return skillRequest{}, problemMalformedRequest(`the skill request carries no "skill" field`)
	}
	return req, nil
}

// partJSON renders one part's content as the JSON bytes of a skill request.
func partJSON(part *a2a.Part) ([]byte, error) {
	switch content := part.Content.(type) {
	case a2a.Data:
		return json.Marshal(content.Value)
	case a2a.Text:
		return []byte(string(content)), nil
	default:
		return nil, errors.New("expected a data part (or a text part holding the skill JSON)")
	}
}

// decodeStrict decodes one JSON document into dst, rejecting unknown fields
// and trailing data. Absent arguments decode as the empty object so each
// skill's own required-field checks produce the specific problem.
func decodeStrict(raw []byte, dst any) error {
	if len(bytes.TrimSpace(raw)) == 0 {
		raw = []byte("{}")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return err
	}
	if dec.More() {
		return errors.New("unexpected data after the JSON document")
	}
	return nil
}

// decodeArgs decodes one skill's arguments strictly.
func decodeArgs(raw json.RawMessage, dst any) *problem {
	if err := decodeStrict(raw, dst); err != nil {
		return problemInvalidArgument(fmt.Errorf("arguments: %w", err))
	}
	return nil
}

// requireNonBlank returns an invalid-argument problem for the first of the
// name/value pairs whose value is blank.
func requireNonBlank(pairs ...[2]string) *problem {
	for _, p := range pairs {
		if strings.TrimSpace(p[1]) == "" {
			return problemInvalidArgument(fmt.Errorf("%s is required", p[0]))
		}
	}
	return nil
}

// hasNUL reports whether any value carries U+0000. PostgreSQL text cannot
// hold it and a statement that carries one aborts its transaction, so the
// REST adapter answers a NUL in a request path with 404 not-found before
// it reaches a repository (internal/adapters/inbound/http/nul.go); an
// identifier that can never name a resource gets the same answer here.
func hasNUL(values ...string) bool {
	for _, v := range values {
		if strings.ContainsRune(v, 0) {
			return true
		}
	}
	return false
}

// getPurchaseOrder is the single-order read skill, through the same
// GetPurchaseOrder query the REST adapter and the MCP tool use.
func (d Deps) getPurchaseOrder(ctx context.Context, rawArgs json.RawMessage) (skillDTO, *problem) {
	var args purchaseOrderArgs
	if prob := decodeArgs(rawArgs, &args); prob != nil {
		return skillDTO{}, prob
	}
	if prob := requireNonBlank([2]string{"vendorId", args.VendorID}, [2]string{"poNumber", args.PONumber}); prob != nil {
		return skillDTO{}, prob
	}
	if hasNUL(args.PONumber) {
		return skillDTO{}, problemNotFound(string(subjectOrder))
	}
	po, err := d.Reads.GetPurchaseOrder(ctx, args.VendorID, args.PONumber)
	if err != nil {
		return skillDTO{}, problemFromError(err, subjectOrder)
	}
	return skillDTO{PurchaseOrder: &po, summary: summarizeOrder(po)}, nil
}

// getNodeScorecard is the node-performance read skill, through the same
// GetScorecard query the REST adapter and the MCP tool use (it relays from
// node-performance exactly as GET /vendor/nodes/{nodeId}/scorecard does).
func (d Deps) getNodeScorecard(ctx context.Context, rawArgs json.RawMessage) (skillDTO, *problem) {
	var args scorecardArgs
	if prob := decodeArgs(rawArgs, &args); prob != nil {
		return skillDTO{}, prob
	}
	if prob := requireNonBlank([2]string{"vendorId", args.VendorID}, [2]string{"nodeId", args.NodeID}); prob != nil {
		return skillDTO{}, prob
	}
	if hasNUL(args.NodeID) {
		return skillDTO{}, problemNotFound(string(subjectNode))
	}
	sc, err := d.Reads.GetScorecard(ctx, args.VendorID, args.NodeID)
	if err != nil {
		return skillDTO{}, problemFromError(err, subjectNode)
	}
	return skillDTO{Scorecard: &sc, summary: summarizeScorecard(sc)}, nil
}
