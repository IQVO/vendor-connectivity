// Package bdd_test hosts the godog (Gherkin) acceptance suite. It drives the REAL
// chi router over HTTP, and feeds the local copies and the releases through the
// REAL consumers' HandleMessage with raw CloudEvents (the producers' own
// contracts), over the in-memory doubles wired the way cmd/api wires the real
// adapters. Every scenario in features/*.feature is a black-box test of the
// service. The runner lives here, not at the repository root.
package bdd_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/cucumber/godog"

	inboundhttp "github.com/claudioed/vendor-connectivity/internal/adapters/inbound/http"
	inboundkafka "github.com/claudioed/vendor-connectivity/internal/adapters/inbound/kafka"
	"github.com/claudioed/vendor-connectivity/internal/adapters/outbound/memory"
	"github.com/claudioed/vendor-connectivity/internal/application/gateway"
	"github.com/claudioed/vendor-connectivity/internal/application/usecases"
	"github.com/claudioed/vendor-connectivity/internal/domain/purchaseorder"
)

// TestFeatures runs every Gherkin feature under features/.
func TestFeatures(t *testing.T) {
	suite := godog.TestSuite{
		ScenarioInitializer: InitializeScenario,
		Options: &godog.Options{
			Format:   "progress",
			Paths:    []string{"../../features"},
			Strict:   true,
			TestingT: t,
		},
	}
	if suite.Run() != 0 {
		t.Fatal("non-zero status returned, failed to run feature tests")
	}
}

// start is the service clock at the beginning of every scenario.
var start = time.Date(2026, 10, 22, 13, 0, 0, 0, time.UTC)

// world is the per-scenario state.
type world struct {
	server  *httptest.Server
	store   *memory.Store
	svc     *usecases.Service
	clock   *memory.Clock
	random  *memory.ScriptedRandom
	nt      *memory.NodeTopology
	offers  *memory.OfferAvailability
	ship    *memory.ShippingTracking
	perf    *memory.NodePerformance
	nodes   *inboundkafka.Consumer
	listing *inboundkafka.Consumer
	sourced *inboundkafka.Consumer

	status int
	body   []byte
	header http.Header

	lastTx   string
	lastKey  int
	eventSeq int
	cfg      usecases.Config

	// lastLabels holds the tracking numbers remembered from a label
	// response, referenced as {trk:<packageNo>} in later steps.
	lastLabels map[int]string
}

func newWorld() *world {
	w := &world{clock: memory.NewClock(start), random: memory.NewScriptedRandom(), nt: &memory.NodeTopology{}, offers: &memory.OfferAvailability{},
		ship: &memory.ShippingTracking{}, perf: &memory.NodePerformance{}}
	w.cfg = usecases.Config{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	w.build()
	return w
}

// build wires the service over a fresh store; it is called again when a step
// changes the configuration (a knob).
func (w *world) build() {
	if w.server != nil {
		w.server.Close()
	}
	w.store = memory.NewStore()
	s := w.store
	w.svc = usecases.New(usecases.Deps{
		UoW: s.UnitOfWork(), Vendors: s.Vendors(), Orders: s.Orders(), Submissions: s.Submissions(), Nodes: s.Nodes(), Listings: s.Listings(),
		Outbox: s.Outbox(), Inbox: s.Inbox(), Clock: w.clock, IDs: &memory.SequentialIDs{}, Random: w.random,
		NodeTopology: w.nt, Offers: w.offers, Shipping: w.ship, Performance: w.perf,
	}, w.cfg)
	w.nodes = &inboundkafka.Consumer{Name: "node", Topic: "nodes", Handlers: inboundkafka.NodeHandlers(w.svc), Logger: w.cfg.Logger}
	w.listing = &inboundkafka.Consumer{Name: "listing", Topic: "listings", Handlers: inboundkafka.ListingHandlers(w.svc), Logger: w.cfg.Logger}
	w.sourced = &inboundkafka.Consumer{Name: "sourcing", Topic: "sourcing", Handlers: inboundkafka.SourcingHandlers(w.svc, w.cfg.Logger), Logger: w.cfg.Logger}
	w.server = httptest.NewServer(inboundhttp.NewRouter(&inboundhttp.Server{Service: w.svc}))
}

func InitializeScenario(sc *godog.ScenarioContext) {
	w := newWorld()
	sc.After(func(ctx context.Context, _ *godog.Scenario, _ error) (context.Context, error) {
		w.server.Close()
		return ctx, nil
	})
	registerGiven(sc, w)
	registerWhen(sc, w)
	registerThen(sc, w)
	registerWave2(sc, w)
}

// --- helpers -----------------------------------------------------------------

// eventID mints a fresh CloudEvents id.
func (w *world) eventID() string {
	w.eventSeq++
	return fmt.Sprintf("evt-%04d", w.eventSeq)
}

func cloudEvent(id, typ, subject, data string) []byte {
	return []byte(fmt.Sprintf(`{"specversion":"1.0","id":%q,"source":"/retailnetwork/test","type":%q,"subject":%q,"time":"2026-10-22T13:00:00Z","datacontenttype":"application/json","dataschema":"urn:test","data":%s}`,
		id, typ, subject, data))
}

// feed hands one raw CloudEvent to a consumer, as the Kafka loop would.
func (w *world) feed(c *inboundkafka.Consumer, typ, subject, data string) error {
	return c.HandleMessage(context.Background(), cloudEvent(w.eventID(), typ, subject, data))
}

// poNumber is the purchase-order number of assignment (release 1).
func poNumber(assignment string) string { return string(purchaseorder.NumberFor(assignment, 1)) }

// expand replaces the placeholders {po:<assignment>} and {tx} in a request.
func (w *world) expand(s string) string {
	for {
		i := strings.Index(s, "{po:")
		if i < 0 {
			break
		}
		j := strings.Index(s[i:], "}")
		s = s[:i] + poNumber(s[i+4:i+j]) + s[i+j+1:]
	}
	return w.expandLabels(strings.ReplaceAll(s, "{tx}", w.lastTx))
}

func (w *world) request(method, path, body string, headers ...string) error {
	var rd io.Reader = http.NoBody
	if body != "" {
		rd = strings.NewReader(body)
	}
	req, err := http.NewRequestWithContext(context.Background(), method, w.server.URL+path, rd)
	if err != nil {
		return err
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	w.status, w.header = resp.StatusCode, resp.Header
	w.body, err = io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	var accepted struct {
		TransactionID string `json:"transactionId"`
	}
	if json.Unmarshal(w.body, &accepted) == nil && accepted.TransactionID != "" {
		w.lastTx = accepted.TransactionID
	}
	return nil
}

// write sends a vendor write with a fresh Idempotency-Key header (the key is
// enforced by the Postgres middleware; the doubles run without it).
func (w *world) write(method, path, body string) error {
	w.lastKey++
	return w.request(method, path, w.expand(body), "Idempotency-Key", "bdd-"+strconv.Itoa(w.lastKey))
}

// field walks a dotted path ("labels.0.packageNo") through the last response.
func (w *world) field(path string) (any, bool, error) {
	var doc any
	if err := json.Unmarshal(w.body, &doc); err != nil {
		return nil, false, fmt.Errorf("response is not JSON: %s", w.body)
	}
	cur := doc
	for _, part := range strings.Split(path, ".") {
		switch node := cur.(type) {
		case map[string]any:
			v, ok := node[part]
			if !ok {
				return nil, false, nil
			}
			cur = v
		case []any:
			i, err := strconv.Atoi(part)
			if err != nil || i < 0 || i >= len(node) {
				return nil, false, nil
			}
			cur = node[i]
		default:
			return nil, false, nil
		}
	}
	return cur, true, nil
}

func render(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(x)
	default:
		b, _ := json.Marshal(x)
		return string(b)
	}
}

func (w *world) eventNames() []string {
	var names []string
	for _, e := range w.store.Outbox().Events() {
		names = append(names, e.EventName())
	}
	return names
}

func refusal(code string) error { return &gateway.Refusal{Status: 409, Code: code, Message: code} }
