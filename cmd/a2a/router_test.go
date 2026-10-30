package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2aclient"
	"github.com/a2aproject/a2a-go/v2/a2asrv"

	inbounda2a "github.com/claudioed/vendor-connectivity/internal/adapters/inbound/a2a"
	"github.com/claudioed/vendor-connectivity/internal/application/views"
)

// stubReads answers the two skills' reads without a database: the transport
// tests only need a handler that proves the request reached the use cases.
type stubReads struct{}

func (stubReads) GetPurchaseOrder(_ context.Context, vendorID, poNumber string) (views.PurchaseOrder, error) {
	return views.PurchaseOrder{PONumber: poNumber, VendorID: vendorID, NodeID: "n-1", State: "ISSUED", Lines: []views.PoLine{}}, nil
}

func (stubReads) GetScorecard(_ context.Context, _, nodeID string) (views.Scorecard, error) {
	return views.Scorecard{NodeID: nodeID, Windows: []views.ScorecardWindow{}}, nil
}

const testAgentURL = "http://vendor-connectivity-a2a.warehouse-systems.svc.cluster.local:8093/"

func testHandler() http.Handler {
	return newRouter(
		inbounda2a.Handler(inbounda2a.NewExecutor(inbounda2a.Deps{Reads: stubReads{}})),
		inbounda2a.Card(inbounda2a.Config{AgentURL: testAgentURL, Version: agentVersion}),
		quietLogger(), "vendor-connectivity-a2a")
}

func newTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(testHandler())
	t.Cleanup(srv.Close)
	return srv
}

func TestRouter_HealthzIsOpenAndServesOK(t *testing.T) {
	srv := newTestServer(t)
	resp, err := http.Get(srv.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || string(body) != `{"status":"ok"}` {
		t.Fatalf("GET /healthz = %d %q, want 200 {\"status\":\"ok\"}", resp.StatusCode, body)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", ct)
	}
}

// The Agent Card is served at the well-known discovery path and advertises
// the configured URL and exactly the two curated skills.
func TestRouter_ServesTheAgentCard(t *testing.T) {
	srv := newTestServer(t)
	resp, err := http.Get(srv.URL + a2asrv.WellKnownAgentCardPath)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s = %d, want 200", a2asrv.WellKnownAgentCardPath, resp.StatusCode)
	}
	var card a2a.AgentCard
	if err := json.NewDecoder(resp.Body).Decode(&card); err != nil {
		t.Fatalf("card is not JSON: %v", err)
	}
	if card.Name != "vendor-connectivity" || len(card.Skills) != 2 ||
		len(card.SupportedInterfaces) != 1 || card.SupportedInterfaces[0].URL != testAgentURL {
		t.Fatalf("card = %+v", card)
	}
}

// authRoundTripper adds an Authorization header to every request: the
// binary reads no credential, so it must behave identically with one.
type authRoundTripper struct{ header string }

func (a authRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) {
	if a.header != "" {
		r = r.Clone(r.Context())
		r.Header.Set("Authorization", a.header)
	}
	return http.DefaultTransport.RoundTrip(r)
}

// callSkill drives get_purchase_order over the real JSON-RPC wire, through
// the SDK client, at url.
func callSkill(t *testing.T, url, authorization string) string {
	t.Helper()
	card := inbounda2a.Card(inbounda2a.Config{AgentURL: url, Version: "test"})
	client, err := a2aclient.NewFromCard(context.Background(), card,
		a2aclient.WithJSONRPCTransport(&http.Client{Transport: authRoundTripper{header: authorization}}))
	if err != nil {
		t.Fatalf("NewFromCard: %v", err)
	}
	msg := a2a.NewMessage(a2a.MessageRoleUser, a2a.NewDataPart(map[string]any{
		"skill": "get_purchase_order", "arguments": map[string]any{"vendorId": "V-1", "poNumber": "PO-9a1b-0001"},
	}))
	resp, err := client.SendMessage(context.Background(), &a2a.SendMessageRequest{Message: msg})
	if err != nil {
		t.Fatalf("SendMessage at %s: %v", url, err)
	}
	reply, ok := resp.(*a2a.Message)
	if !ok || len(reply.Parts) < 2 {
		t.Fatalf("reply = %#v, want a message with a data and a text part", resp)
	}
	text, _ := reply.Parts[1].Content.(a2a.Text)
	return string(text)
}

// Both mount points speak JSON-RPC, with no credentials, and a real client
// reaches the skills through either: "/" (the card's advertised URL) and
// "/a2a" (the fleet's <CTX>_A2A_ENDPOINT convention).
func TestJSONRPC_RootAndA2APathsServeSkills(t *testing.T) {
	srv := newTestServer(t)
	for _, path := range []string{"/", "/a2a"} {
		t.Run(path, func(t *testing.T) {
			for _, auth := range []string{"", "Bearer nonsense"} {
				text := callSkill(t, srv.URL+path, auth)
				if !strings.HasPrefix(text, "PO-9a1b-0001: issued, node n-1") {
					t.Fatalf("summary at %s with Authorization=%q = %q", path, auth, text)
				}
			}
		})
	}
}

// The liveness probe is not request-logged (the fleet request-log standard).
func TestRouterDoesNotLogTheLivenessProbe(t *testing.T) {
	var buf bytes.Buffer
	router := newRouter(
		inbounda2a.Handler(inbounda2a.NewExecutor(inbounda2a.Deps{Reads: stubReads{}})),
		inbounda2a.Card(inbounda2a.Config{AgentURL: testAgentURL, Version: agentVersion}),
		slog.New(slog.NewJSONHandler(&buf, nil)), "vendor-connectivity-a2a")
	router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if buf.Len() != 0 {
		t.Fatalf("/healthz was logged: %s", buf.String())
	}
}
