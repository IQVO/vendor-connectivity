//go:build integration

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	ce "github.com/cloudevents/sdk-go/v2/event"
	"github.com/jackc/pgx/v5/pgxpool"
	kafkago "github.com/segmentio/kafka-go"
	"github.com/testcontainers/testcontainers-go"
	tckafka "github.com/testcontainers/testcontainers-go/modules/kafka"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

// The end-to-end proof that the shipped binary is really wired: the test boots
// the REAL entry point (run, in-process) against testcontainers Postgres and
// Kafka with nothing but the environment a deployment sets, and drives it only
// through REST and Kafka. Node, listing and release events go in through the
// input topics, the Vendor API is used as a vendor uses it, and the effects
// are read from Postgres and from the produced topics (events and analytics).
// The four in-organization neighbours are one plain HTTP stub. The service is
// restarted, then its dependencies are taken away to watch /readyz follow
// them. Every Kafka read is bounded by a deadline; run it with -timeout 300s.

const (
	topicNodes     = "retailnetwork.node-topology.events"
	topicOffers    = "retailnetwork.offer-availability.events"
	topicSourcing  = "retailnetwork.order-sourcing.events"
	topicInbound   = "retailnetwork.seller-inbound.events"
	topicEvents    = "retailnetwork.vendor-connectivity.events"
	topicAnalytics = "retailnetwork.vendor-connectivity.analytics"

	source     = "/retailnetwork/vendor-connectivity"
	typePrefix = "com.retailnetwork.partner.vendor-connectivity."
)

// containerStartTimeout bounds a testcontainers start.
const containerStartTimeout = 120 * time.Second

func startPostgres(t *testing.T) (*tcpostgres.PostgresContainer, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), containerStartTimeout)
	defer cancel()
	c, err := tcpostgres.Run(ctx, "postgres:16-alpine",
		tcpostgres.WithDatabase("vendor_connectivity_e2e"), tcpostgres.WithUsername("e2e"), tcpostgres.WithPassword("e2e"),
		testcontainers.WithWaitStrategy(wait.ForLog("database system is ready to accept connections").WithOccurrence(2).WithStartupTimeout(60*time.Second)))
	if err != nil {
		t.Fatalf("start postgres: %v", err)
	}
	t.Cleanup(func() { _ = testcontainers.TerminateContainer(c) })
	url, err := c.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("postgres dsn: %v", err)
	}
	return c, url
}

func startKafka(t *testing.T) (*tckafka.KafkaContainer, []string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), containerStartTimeout)
	defer cancel()
	c, err := tckafka.Run(ctx, "confluentinc/confluent-local:7.6.1", tckafka.WithClusterID("vendor-connectivity-e2e"))
	if err != nil {
		t.Fatalf("start kafka: %v", err)
	}
	t.Cleanup(func() { _ = testcontainers.TerminateContainer(c) })
	brokers, err := c.Brokers(ctx)
	if err != nil {
		t.Fatalf("kafka brokers: %v", err)
	}
	return c, brokers
}

// createTopic creates a one-partition topic and waits for its leader.
func createTopic(t *testing.T, brokers []string, topic string) {
	t.Helper()
	conn, err := kafkago.Dial("tcp", brokers[0])
	if err != nil {
		t.Fatalf("dial broker: %v", err)
	}
	defer conn.Close()
	if err := conn.CreateTopics(kafkago.TopicConfig{Topic: topic, NumPartitions: 1, ReplicationFactor: 1}); err != nil {
		t.Fatalf("create topic %s: %v", topic, err)
	}
	for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); time.Sleep(200 * time.Millisecond) {
		if parts, err := conn.ReadPartitions(topic); err == nil && len(parts) == 1 && parts[0].Leader.ID != 0 {
			return
		}
	}
	t.Fatalf("topic %s never got a leader", topic)
}

// produce writes already-encoded messages to topic with RequireAll acks.
func produce(t *testing.T, brokers []string, topic string, msgs ...kafkago.Message) {
	t.Helper()
	transport := &kafkago.Transport{}
	defer transport.CloseIdleConnections()
	w := &kafkago.Writer{Addr: kafkago.TCP(brokers...), Topic: topic, Balancer: &kafkago.Hash{}, RequiredAcks: kafkago.RequireAll, BatchTimeout: 10 * time.Millisecond, Transport: transport}
	defer w.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := w.WriteMessages(ctx, msgs...); err != nil {
		t.Fatalf("produce to %s: %v", topic, err)
	}
}

// inputEvent builds a CloudEvent as a sibling context publishes it.
func inputEvent(t *testing.T, id, src, typ, subject, data string) []byte {
	t.Helper()
	e := ce.New(ce.CloudEventsVersionV1)
	e.SetID(id)
	e.SetSource(src)
	e.SetType(typ)
	e.SetSubject(subject)
	e.SetTime(time.Now().UTC())
	e.SetDataSchema("urn:warehouse:e2e:events:X:v1")
	if err := e.SetData("application/json", json.RawMessage(data)); err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// consume reads exactly n messages of the one-partition topic from the start,
// bounded by a deadline.
func consume(t *testing.T, brokers []string, topic string, n int) []kafkago.Message {
	t.Helper()
	r := kafkago.NewReader(kafkago.ReaderConfig{Brokers: brokers, Topic: topic, Partition: 0, MinBytes: 1, MaxBytes: 1 << 20, MaxWait: 200 * time.Millisecond})
	defer r.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := r.SetOffset(kafkago.FirstOffset); err != nil {
		t.Fatal(err)
	}
	var out []kafkago.Message
	for len(out) < n {
		m, err := r.ReadMessage(ctx)
		if err != nil {
			t.Fatalf("read %s: got %d of %d messages: %v", topic, len(out), n, err)
		}
		out = append(out, m)
	}
	return out
}

// noMoreMessages fails if the topic holds more than want messages after a
// settling period (nothing was duplicated).
func noMoreMessages(t *testing.T, brokers []string, topic string, want int) {
	t.Helper()
	r := kafkago.NewReader(kafkago.ReaderConfig{Brokers: brokers, Topic: topic, Partition: 0, MinBytes: 1, MaxBytes: 1 << 20, MaxWait: 200 * time.Millisecond})
	defer r.Close()
	if err := r.SetOffset(kafkago.FirstOffset); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	got := 0
	for {
		if _, err := r.ReadMessage(ctx); err != nil {
			break
		}
		got++
	}
	if got != want {
		t.Fatalf("%s holds %d messages, want exactly %d", topic, got, want)
	}
}

func freeAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().String()
}

// service is one running instance of the real entry point.
type service struct {
	t      *testing.T
	base   string
	cancel context.CancelFunc
	done   chan error
}

// env is the environment a deployment sets.
type env struct {
	dbURL      string
	brokers    []string
	addr       string
	neighbours string
}

func (e env) apply(t *testing.T) {
	t.Helper()
	t.Setenv("DATABASE_URL", e.dbURL)
	t.Setenv("MIGRATIONS_DATABASE_URL", "")
	t.Setenv("KAFKA_BROKERS", strings.Join(e.brokers, ","))
	t.Setenv("EVENT_PUBLISHER", "kafka")
	for _, c := range []string{"NODE", "LISTING", "SOURCING", "INBOUND", "PACKAGING"} {
		t.Setenv(c+"_CONSUMER_GROUP", "vendor-connectivity-"+strings.ToLower(c)+"-e2e")
	}
	for _, name := range []string{"NODE_TOPOLOGY_URL", "OFFER_AVAILABILITY_URL", "SHIPPING_AND_TRACKING_URL", "NODE_PERFORMANCE_URL"} {
		t.Setenv(name, e.neighbours)
	}
	t.Setenv("TX_PROCESSING_DELAY", "300ms")
	t.Setenv("SUBMISSION_POLL_INTERVAL", "100ms")
	t.Setenv("HTTP_ADDR", e.addr)
	t.Setenv("SHUTDOWN_DRAIN_DELAY", "0")
	t.Setenv("OUTBOX_RELAY_INTERVAL", "200ms")
	t.Setenv("LOG_LEVEL", "warn")
}

// start boots run in-process and waits until the listener answers /healthz.
func (e env) start(t *testing.T) *service {
	t.Helper()
	e.apply(t)
	ctx, cancel := context.WithCancel(context.Background())
	s := &service{t: t, base: "http://" + e.addr, cancel: cancel, done: make(chan error, 1)}
	go func() { s.done <- run(ctx) }()
	t.Cleanup(s.stopQuietly)
	waitFor(t, 90*time.Second, "the service to listen on "+e.addr, func() bool {
		select {
		case err := <-s.done:
			t.Fatalf("run returned while booting: %v", err)
		default:
		}
		status, _ := s.try(http.MethodGet, "/healthz", "", "")
		return status == http.StatusOK
	})
	return s
}

// stop cancels run and requires a clean shutdown within a bound.
func (s *service) stop() {
	s.t.Helper()
	s.cancel()
	select {
	case err := <-s.done:
		if err != nil {
			s.t.Fatalf("shutdown returned %v", err)
		}
	case <-time.After(60 * time.Second):
		s.t.Fatal("run did not return after the shutdown")
	}
	s.done <- nil // let the cleanup find it stopped
}

func (s *service) stopQuietly() {
	s.cancel()
	select {
	case <-s.done:
	case <-time.After(60 * time.Second):
	}
}

// expectExit waits for run to return on its own (nobody cancelled it) and
// returns its error.
func (s *service) expectExit() error {
	s.t.Helper()
	select {
	case err := <-s.done:
		s.done <- nil // let the cleanup find it stopped
		return err
	case <-time.After(90 * time.Second):
		s.t.Fatal("run did not return although Kafka is gone")
		return nil
	}
}

// try performs a request and returns -1 when the connection fails. A non-empty
// key is sent as the Idempotency-Key.
func (s *service) try(method, path, body, key string) (int, string) {
	req, err := http.NewRequestWithContext(context.Background(), method, s.base+path, strings.NewReader(body))
	if err != nil {
		return -1, err.Error()
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return -1, err.Error()
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(raw)
}

func (s *service) call(method, path, body, key string, want int) string {
	s.t.Helper()
	status, raw := s.try(method, path, body, key)
	if status != want {
		s.t.Fatalf("%s %s = %d %s, want %d", method, path, status, raw, want)
	}
	return raw
}

func waitFor(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("timed out after %s waiting for %s", timeout, what)
}

// pgView reads the service's own database.
type pgView struct {
	t    *testing.T
	pool *pgxpool.Pool
}

func newPGView(t *testing.T, url string) pgView {
	t.Helper()
	pool, err := pgxpool.New(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pgView{t: t, pool: pool}
}

func (v pgView) count(query string, args ...any) int {
	v.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var n int
	if err := v.pool.QueryRow(ctx, query, args...).Scan(&n); err != nil {
		v.t.Fatalf("%s: %v", query, err)
	}
	return n
}

// The CloudEvents context attributes of a published message, plus its data.
type envelope struct {
	SpecVersion     string          `json:"specversion"`
	ID              string          `json:"id"`
	Source          string          `json:"source"`
	Type            string          `json:"type"`
	Subject         string          `json:"subject"`
	Time            string          `json:"time"`
	DataContentType string          `json:"datacontenttype"`
	DataSchema      string          `json:"dataschema"`
	Data            json.RawMessage `json:"data"`
}

func decodeEnvelope(t *testing.T, m kafkago.Message) envelope {
	t.Helper()
	var e envelope
	if err := json.Unmarshal(m.Value, &e); err != nil {
		t.Fatalf("not a CloudEvent: %v: %s", err, m.Value)
	}
	return e
}

// expectedEvent is one published occurrence: event name, subject (CloudEvents

func readyz(s *service) (int, string) { return s.try(http.MethodGet, "/readyz", "", "") }

func waitReadyzStatus(t *testing.T, s *service, want int, what string) string {
	t.Helper()
	var body string
	waitFor(t, 60*time.Second, what, func() bool {
		var status int
		status, body = readyz(s)
		return status == want
	})
	return body
}

func mustDecode(t *testing.T, raw string, dest any) {
	t.Helper()
	if err := json.Unmarshal([]byte(raw), dest); err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}
}

func assertProblem(t *testing.T, body, slug string) {
	t.Helper()
	if !strings.Contains(body, `"type":"`+slug+`"`) {
		t.Fatalf("body = %s, want problem type %s", body, slug)
	}
}

// neighbourStub is the one HTTP server standing in for the four in-organization
// neighbours; it records what each call carried.
type neighbourStub struct {
	srv    *httptest.Server
	labels atomic.Int32
	mu     sync.Mutex
	calls  []string
}

func newNeighbourStub(t *testing.T) *neighbourStub {
	t.Helper()
	n := &neighbourStub{}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /nodes", func(w http.ResponseWriter, r *http.Request) {
		n.record(r)
		writeStub(w, 201, `{"nodeId":"n-2","nodeType":"FULFILLMENT_CENTER","operator":"VENDOR","vendorRef":{"vendorId":"V-1","vendorNodeCode":"SITE-2"},"regionId":"r-1","timezone":"America/Sao_Paulo","status":"PROPOSED"}`)
	})
	mux.HandleFunc("PUT /nodes/{id}/operational-profile", func(w http.ResponseWriter, r *http.Request) { n.record(r); writeStub(w, 200, `{}`) })
	mux.HandleFunc("POST /inventory-feeds", func(w http.ResponseWriter, r *http.Request) { n.record(r); writeStub(w, 201, `{"state":"APPLIED"}`) })
	mux.HandleFunc("POST /labels", func(w http.ResponseWriter, r *http.Request) {
		raw := n.record(r)
		var in struct {
			PONumber  string `json:"poNumber"`
			PackageNo int    `json:"packageNo"`
		}
		_ = json.Unmarshal(raw, &in)
		n.labels.Add(1)
		writeStub(w, 201, fmt.Sprintf(`{"labelRef":"lbl-%s-%d","trackingNumber":"TRK-%s-%d","carrier":"carrier-a","status":"ISSUED"}`, in.PONumber, in.PackageNo, in.PONumber, in.PackageNo))
	})
	mux.HandleFunc("GET /scorecards/{id}", func(w http.ResponseWriter, r *http.Request) {
		n.record(r)
		writeStub(w, 200, `{"nodeId":"n-1","scorecards":[{"window":"30d","grade":"A","state":"GRADED","computedAt":"2026-10-22T12:00:00Z","metrics":{"onTimeHandoverRate":0.99,"observations":80}}]}`)
	})
	n.srv = httptest.NewServer(mux)
	t.Cleanup(n.srv.Close)
	return n
}

func writeStub(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(body))
}

func (n *neighbourStub) record(r *http.Request) []byte {
	raw, _ := io.ReadAll(r.Body)
	n.mu.Lock()
	defer n.mu.Unlock()
	n.calls = append(n.calls, r.Method+" "+r.URL.Path+" key="+r.Header.Get("Idempotency-Key")+" "+string(raw))
	return raw
}

func (n *neighbourStub) seen(prefix string) []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	var out []string
	for _, c := range n.calls {
		if strings.HasPrefix(c, prefix) {
			out = append(out, c)
		}
	}
	return out
}

// world is what the phases share.
type world struct {
	e        env
	pg       *tcpostgres.PostgresContainer
	kc       *tckafka.KafkaContainer
	db       pgView
	brokers  []string
	stub     *neighbourStub
	expected []expectedEvent // events topic, in publication order
	analyze  []expectedEvent // analytics topic
}

type expectedEvent struct{ name, subject string }

func (w *world) expect(name, subject string) {
	w.expected = append(w.expected, expectedEvent{name, subject})
}

// assertEnvelope checks every context attribute and the Kafka key/header of a
// message of this service.
func assertEnvelope(t *testing.T, m kafkago.Message, want expectedEvent, stream string) envelope {
	t.Helper()
	e := decodeEnvelope(t, m)
	wantType := typePrefix + entityOf(want.name) + "." + want.name
	schema := fmt.Sprintf("urn:retailnetwork:vendor-connectivity:%s:%s:v1", stream, want.name)
	if e.SpecVersion != "1.0" || e.Type != wantType || e.Source != source || e.DataSchema != schema ||
		e.DataContentType != "application/json" || e.Subject != want.subject || e.ID == "" {
		t.Fatalf("envelope = %+v, want 1.0 %s from %s (%s) about %s", e, wantType, source, schema, want.subject)
	}
	if _, err := time.Parse(time.RFC3339Nano, e.Time); err != nil {
		t.Fatalf("time %q is not RFC 3339: %v", e.Time, err)
	}
	if string(m.Key) != want.subject {
		t.Fatalf("key = %q, want %q", m.Key, want.subject)
	}
	ct := ""
	for _, h := range m.Headers {
		if h.Key == "content-type" {
			ct = string(h.Value)
		}
	}
	if ct != "application/cloudevents+json; charset=UTF-8" {
		t.Fatalf("content-type header = %q", ct)
	}
	return e
}

func entityOf(event string) string {
	switch event {
	case "VendorOnboarded", "VendorSuspended", "VendorReinstated":
		return "vendoraccount"
	case "SubmissionProcessed":
		return "submission"
	case "InboundReceiptSubmitted":
		return "inboundreceipt"
	default:
		return "purchaseorder"
	}
}

func nodeEvent(t *testing.T, id, name, node, data string) kafkago.Message {
	t.Helper()
	return kafkago.Message{Key: []byte(node), Value: inputEvent(t, id, "/retailnetwork/node-topology", "com.retailnetwork.logistics.node-topology.node."+name, node, data)}
}

func releaseEvent(t *testing.T, id, assignment, node string) kafkago.Message {
	t.Helper()
	data := fmt.Sprintf(`{"assignmentId":%q,"releaseNo":1,"orderId":"ord-%s","groupNo":1,"nodeId":%q,"requiredShipBy":"2099-01-02T12:00:00Z","tier":"NEXT_DAY","lines":[{"listingId":"lst-1","quantity":2},{"listingId":"lst-2","quantity":1}],"unknown":"ignored"}`, assignment, assignment, node)
	return kafkago.Message{Key: []byte(assignment), Value: inputEvent(t, id, "/retailnetwork/order-sourcing", "com.retailnetwork.commerce.order-sourcing.assignment.FulfillmentReleasedToNode", assignment, data)}
}

// Phase 1: onboard a vendor through REST; feed the node and listing copies
// through Kafka.
func phaseSetup(t *testing.T, s *service, w *world) {
	t.Helper()
	body := s.call(http.MethodPost, "/vendors", `{"vendorId":"V-1","name":"Carrier A","ackWindowMinutes":240}`, "onboard-1", http.StatusCreated)
	if !strings.Contains(body, `"state":"ACTIVE"`) {
		t.Fatalf("onboard = %s", body)
	}
	w.expect("VendorOnboarded", "V-1")
	if got := s.call(http.MethodPost, "/vendors", `{"vendorId":"V-1","name":"Carrier A","ackWindowMinutes":240}`, "onboard-1", http.StatusCreated); got != body {
		t.Fatalf("a replayed key answers the first response: %s vs %s", got, body)
	}
	assertProblem(t, s.call(http.MethodPost, "/vendors", `{"vendorId":"V-1","name":"Other","ackWindowMinutes":240}`, "onboard-1", http.StatusUnprocessableEntity), "idempotency-key-reused")
	assertProblem(t, s.call(http.MethodPost, "/vendors", `{"vendorId":"V-9","name":"Other","ackWindowMinutes":240}`, "", http.StatusUnprocessableEntity), "idempotency-key-required")

	produce(t, w.brokers, topicNodes,
		nodeEvent(t, "evt-n1", "NodeRegistered", "n-1", `{"nodeId":"n-1","nodeType":"FULFILLMENT_CENTER","subtype":"SORTABLE","operator":"VENDOR","vendorId":"V-1","vendorNodeCode":"SITE-1","regionId":"r-1","timezone":"America/Sao_Paulo"}`),
		nodeEvent(t, "evt-n2", "NodeOperationalProfileDeclared", "n-1", `{"nodeId":"n-1","profileRevision":1,"handlingTimeMinutes":60}`),
		nodeEvent(t, "evt-n3", "NodeActivated", "n-1", `{"nodeId":"n-1","activatedAt":"2026-10-22T12:00:00Z"}`))
	produce(t, w.brokers, topicOffers, kafkago.Message{Key: []byte("lst-1"), Value: inputEvent(t, "evt-l1", "/retailnetwork/offer-availability",
		"com.retailnetwork.commerce.offer-availability.listing.ListingCreated", "lst-1", `{"listingId":"lst-1","catalogItemId":"c-1","nodeId":"n-1"}`)})
	waitFor(t, 60*time.Second, "the node and listing copies", func() bool {
		return w.db.count(`SELECT count(*) FROM vendor_nodes WHERE node_id = 'n-1' AND status = 'ACTIVE' AND handling_time_minutes = 60`) == 1 &&
			w.db.count(`SELECT count(*) FROM listings`) == 1
	})
	var node struct {
		NodeID, Status string
		HandlingTime   int `json:"handlingTimeMinutes"`
	}
	mustDecode(t, s.call(http.MethodGet, "/vendor/nodes/n-1?vendorId=V-1", "", "", http.StatusOK), &node)
	if node.NodeID != "n-1" || node.Status != "ACTIVE" {
		t.Fatalf("node = %+v", node)
	}
	assertProblem(t, s.call(http.MethodGet, "/vendor/nodes/n-1?vendorId=V-404", "", "", http.StatusNotFound), "not-found")
}

type poView struct {
	PONumber string `json:"poNumber"`
	State    string `json:"state"`
	Lines    []struct {
		LineNo   int `json:"lineNo"`
		Quantity int `json:"quantity"`
	} `json:"lines"`
	Acknowledgement *struct{ Decision, TransactionID string } `json:"acknowledgement"`
	Labels          []struct{ PackageNo int }                 `json:"labels"`
	TrackingNumbers []string                                  `json:"trackingNumbers"`
}

func poll(t *testing.T, s *service, wantCount int) []poView {
	t.Helper()
	var page struct {
		PurchaseOrders []poView `json:"purchaseOrders"`
	}
	waitFor(t, 120*time.Second, fmt.Sprintf("%d purchase orders to be pollable", wantCount), func() bool {
		status, raw := s.try(http.MethodGet, "/vendor/purchase-orders?vendorId=V-1", "", "")
		page.PurchaseOrders = nil
		return status == http.StatusOK && json.Unmarshal([]byte(raw), &page) == nil && len(page.PurchaseOrders) == wantCount
	})
	return page.PurchaseOrders
}

func waitTransaction(t *testing.T, s *service, id, status string) string {
	t.Helper()
	var raw string
	waitFor(t, 60*time.Second, "transaction "+id+" to be "+status, func() bool {
		_, raw = s.try(http.MethodGet, "/vendor/transactions/"+id+"?vendorId=V-1", "", "")
		return strings.Contains(raw, `"status":"`+status+`"`)
	})
	return raw
}

// Phase 2: a release becomes a purchase order; the vendor acknowledges it, asks
// for labels and confirms the shipment, each write asynchronous.
func phaseWorkflow(t *testing.T, s *service, w *world) {
	t.Helper()
	produce(t, w.brokers, topicSourcing, releaseEvent(t, "evt-r1", "asg-1", "n-1"))
	pos := poll(t, s, 1)
	po := pos[0]
	if po.State != "ISSUED" || len(po.Lines) != 2 || po.PONumber == "" {
		t.Fatalf("purchase order = %+v", po)
	}
	w.expect("PurchaseOrderIssued", po.PONumber)

	ack := fmt.Sprintf(`{"vendorId":"V-1","poNumber":%q,"decision":"ACCEPT","lines":[{"lineNo":1,"quantity":2},{"lineNo":2,"quantity":1}]}`, po.PONumber)
	var accepted struct {
		TransactionID string `json:"transactionId"`
	}
	mustDecode(t, s.call(http.MethodPost, "/vendor/acknowledgements", ack, "ack-1", http.StatusAccepted), &accepted)
	var replay struct {
		TransactionID string `json:"transactionId"`
	}
	mustDecode(t, s.call(http.MethodPost, "/vendor/acknowledgements", ack, "ack-1", http.StatusAccepted), &replay)
	if accepted.TransactionID == "" || replay.TransactionID != accepted.TransactionID {
		t.Fatalf("a retried key returns the same transactionId: %q vs %q", replay.TransactionID, accepted.TransactionID)
	}
	waitTransaction(t, s, accepted.TransactionID, "SUCCESS")
	w.expect("PurchaseOrderAccepted", po.PONumber)
	w.expect("SubmissionProcessed", accepted.TransactionID)
	assertProblem(t, s.call(http.MethodPost, "/vendor/acknowledgements", ack, "ack-2", http.StatusConflict), "already-acknowledged")

	phaseLabelsAndShipment(t, s, w, po)
}

// phaseLabelsAndShipment is the second half of the workflow: labels are
// synchronous, the shipment confirmation is not.
func phaseLabelsAndShipment(t *testing.T, s *service, w *world, po poView) {
	t.Helper()
	labels := s.call(http.MethodPost, "/vendor/shipping-labels", fmt.Sprintf(`{"vendorId":"V-1","poNumber":%q,"packages":[{"packageNo":1},{"packageNo":2}]}`, po.PONumber), "label-1", http.StatusOK)
	if !strings.Contains(labels, "TRK-"+po.PONumber+"-1") || !strings.Contains(labels, "TRK-"+po.PONumber+"-2") {
		t.Fatalf("labels = %s", labels)
	}
	issued := w.stub.seen("POST /labels")
	if len(issued) != 2 || !strings.Contains(issued[0], `"orderId":"ord-asg-1"`) || !strings.Contains(issued[0], `"tier":"NEXT_DAY"`) || !strings.Contains(issued[0], "key=vc-label-"+po.PONumber+"-1") {
		t.Fatalf("IssueLabel calls = %v", issued)
	}
	if strings.Contains(labels, "ord-asg-1") || strings.Contains(labels, "NEXT_DAY") {
		t.Fatalf("orderId and tier never reach the vendor (G2): %s", labels)
	}

	ship := fmt.Sprintf(`{"vendorId":"V-1","poNumber":%q,"shippedAt":"2026-10-22T15:00:00Z","trackingNumbers":["TRK-%s-1","TRK-%s-2"]}`, po.PONumber, po.PONumber, po.PONumber)
	var shipped struct {
		TransactionID string `json:"transactionId"`
	}
	mustDecode(t, s.call(http.MethodPost, "/vendor/shipment-confirmations", ship, "ship-1", http.StatusAccepted), &shipped)
	waitTransaction(t, s, shipped.TransactionID, "SUCCESS")
	w.expect("VendorShipmentConfirmed", po.PONumber)
	w.expect("SubmissionProcessed", shipped.TransactionID)
	var final poView
	mustDecode(t, s.call(http.MethodGet, "/vendor/purchase-orders/"+po.PONumber+"?vendorId=V-1", "", "", http.StatusOK), &final)
	if final.State != "SHIPPED" || len(final.TrackingNumbers) != 2 || final.Acknowledgement == nil || final.Acknowledgement.Decision != "ACCEPT" {
		t.Fatalf("final purchase order = %+v", final)
	}
}

// Phase 3: the other writes relay to their neighbours; a second release is
// cancelled by order-sourcing (analytics stream only).
func phaseNeighboursAndCancel(t *testing.T, s *service, w *world) {
	t.Helper()
	var reg struct {
		TransactionID string `json:"transactionId"`
	}
	mustDecode(t, s.call(http.MethodPost, "/vendor/nodes", `{"vendorId":"V-1","vendorNodeCode":"SITE-2","nodeType":"FULFILLMENT_CENTER","regionId":"r-1","timezone":"America/Sao_Paulo","postalCode":"07750-000","countryCode":"BR"}`, "node-1", http.StatusAccepted), &reg)
	tx := waitTransaction(t, s, reg.TransactionID, "SUCCESS")
	if !strings.Contains(tx, `"nodeId":"n-2"`) {
		t.Fatalf("the transaction carries the registered nodeId: %s", tx)
	}
	w.expect("SubmissionProcessed", reg.TransactionID)
	if calls := w.stub.seen("POST /nodes"); len(calls) != 1 || !strings.Contains(calls[0], `"operator":"VENDOR"`) || !strings.Contains(calls[0], "key="+reg.TransactionID) {
		t.Fatalf("RegisterNode calls = %v", calls)
	}
	s.call(http.MethodGet, "/vendor/nodes/n-2?vendorId=V-1", "", "", http.StatusOK)

	var feed struct {
		TransactionID string `json:"transactionId"`
	}
	mustDecode(t, s.call(http.MethodPost, "/vendor/inventory", `{"vendorId":"V-1","nodeId":"n-1","fullUpdate":true,"asOf":"2026-10-22T13:00:00Z","lines":[{"listingId":"lst-1","availableQty":5,"basis":"PHYSICAL"}]}`, "feed-1", http.StatusAccepted), &feed)
	waitTransaction(t, s, feed.TransactionID, "SUCCESS")
	w.expect("SubmissionProcessed", feed.TransactionID)
	if calls := w.stub.seen("POST /inventory-feeds"); len(calls) != 1 || !strings.Contains(calls[0], `"feedId":"`+feed.TransactionID+`"`) {
		t.Fatalf("ApplyInventoryFeed calls = %v", calls)
	}
	if got := s.call(http.MethodGet, "/vendor/nodes/n-1/scorecard?vendorId=V-1", "", "", http.StatusOK); !strings.Contains(got, `"grade":"A"`) {
		t.Fatalf("scorecard = %s", got)
	}

	produce(t, w.brokers, topicSourcing, releaseEvent(t, "evt-r2", "asg-2", "n-1"))
	pos := poll(t, s, 2)
	second := pos[1]
	w.expect("PurchaseOrderIssued", second.PONumber)
	produce(t, w.brokers, topicSourcing, kafkago.Message{Key: []byte("asg-2"), Value: inputEvent(t, "evt-c2", "/retailnetwork/order-sourcing",
		"com.retailnetwork.commerce.order-sourcing.assignment.FulfillmentCancelled", "asg-2", `{"assignmentId":"asg-2","orderId":"ord-asg-2","reason":"customer cancelled"}`)})
	waitFor(t, 60*time.Second, "the second order to be cancelled", func() bool {
		return w.db.count(`SELECT count(*) FROM purchase_orders WHERE assignment_id = 'asg-2' AND state = 'CANCELLED'`) == 1
	})
	w.analyze = append(w.analyze, expectedEvent{"PurchaseOrderCancelled", second.PONumber})
}

// Phase 4: NUL characters and unknown routes are client errors, never a 500.
// phaseInbound drives the inbound-shipment leg only through Kafka and REST: the
// seller-inbound events fill InboundShipmentView, the vendor polls the tree and
// reports a receipt (202, INBOUND_RECEIPT), InboundReceiptSubmitted leaves
// through the outbox, and a packaging event shows up on the purchase order line.
func phaseInbound(t *testing.T, s *service, w *world) {
	t.Helper()
	prefix := "com.retailnetwork.partner.seller-inbound.inboundshipment."
	src := "/retailnetwork/seller-inbound"
	const (
		sscc1, sscc2, pallet = "006141410000000012", "006141410000000029", "006141410000000036"
		each, caseGTIN       = "00614141000036", "10614141000019"
	)
	labelled := fmt.Sprintf(`{"shipmentId":"SHP-1","planId":"P-1","sellerId":"S-1","nodeId":"n-1","labelledAt":"2026-10-22T12:00:00Z","cartons":[`+
		`{"sscc":%q,"parentSscc":%q,"kind":"HOMOGENEOUS_CASE","contents":[{"catalogItemId":"c-1","gtin":%q,"qtyEaches":12}],"grossWeightG":4200},`+
		`{"sscc":%q,"kind":"MIXED","contents":[{"catalogItemId":"c-1","gtin":%q,"qtyEaches":2}],"grossWeightG":900}]}`, sscc1, pallet, each, sscc2, each)
	produce(t, w.brokers, topicInbound,
		kafkago.Message{Key: []byte("SHP-1"), Value: inputEvent(t, "evt-i1", src, prefix+"InboundShipmentLabelled", "SHP-1", labelled)},
		kafkago.Message{Key: []byte("SHP-1"), Value: inputEvent(t, "evt-i2", src, prefix+"InboundShipmentShipped", "SHP-1",
			`{"shipmentId":"SHP-1","planId":"P-1","sellerId":"S-1","nodeId":"n-1","shippedAt":"2026-10-22T13:00:00Z"}`)})
	produce(t, w.brokers, topicOffers, kafkago.Message{Key: []byte("c-1"), Value: inputEvent(t, "evt-p1", "/retailnetwork/offer-availability",
		"com.retailnetwork.partner.offer-availability.catalogitem.CatalogItemPackagingDeclared", "c-1",
		fmt.Sprintf(`{"catalogItemId":"c-1","level":"CASE","gtin":%q,"childGtin":%q,"unitsPerPack":12,"declaredAt":"2026-10-20T08:00:00Z"}`, caseGTIN, each))})
	waitFor(t, 60*time.Second, "the shipment copy", func() bool {
		return w.db.count(`SELECT count(*) FROM inbound_shipments WHERE status = 'SHIPPED'`) == 1
	})
	waitFor(t, 60*time.Second, "the packaging copy", func() bool {
		return w.db.count(`SELECT count(*) FROM catalog_packaging`) == 1
	})

	var page struct {
		InboundShipments []struct {
			ShipmentID, NodeID, SellerID, Status string
			Pallets                              []struct{ SSCC string }
			Cartons                              []struct{ SSCC string }
		} `json:"inboundShipments"`
	}
	mustDecode(t, s.call(http.MethodGet, "/vendor/inbound-shipments?vendorId=V-1", "", "", http.StatusOK), &page)
	if len(page.InboundShipments) != 1 || page.InboundShipments[0].ShipmentID != "SHP-1" || page.InboundShipments[0].SellerID != "S-1" ||
		len(page.InboundShipments[0].Pallets) != 1 || page.InboundShipments[0].Pallets[0].SSCC != pallet || len(page.InboundShipments[0].Cartons) != 1 {
		t.Fatalf("inbound poll = %+v", page)
	}
	s.call(http.MethodGet, "/vendor/inbound-shipments/SHP-1?vendorId=V-1", "", "", http.StatusOK)
	assertProblem(t, s.call(http.MethodGet, "/vendor/inbound-shipments/SHP-1?vendorId=V-404", "", "", http.StatusNotFound), "not-found")
	if body := s.call(http.MethodGet, "/vendor/purchase-orders?vendorId=V-1", "", "", http.StatusOK); !strings.Contains(body,
		`"packaging":[{"level":"CASE","gtin":"`+caseGTIN+`","childGtin":"`+each+`","unitsPerPack":12}]`) {
		t.Fatalf("purchase order lines carry no packaging: %s", body)
	}

	receipt := fmt.Sprintf(`{"vendorId":"V-1","shipmentId":"SHP-1","nodeId":"n-1","receivedAt":"2026-10-23T09:30:00Z","cartons":[`+
		`{"sscc":%q,"status":"RECEIVED","items":[{"gtin":%q,"qtyEaches":12}]},{"sscc":%q,"status":"DAMAGED","items":[{"gtin":%q,"qtyEaches":1}],"complianceCodes":["CARTON_CRUSHED"]}]}`,
		sscc1, each, sscc2, each)
	var accepted struct {
		TransactionID string `json:"transactionId"`
	}
	mustDecode(t, s.call(http.MethodPost, "/vendor/inbound-receipts", receipt, "rcpt-1", http.StatusAccepted), &accepted)
	if accepted.TransactionID == "" {
		t.Fatal("no transactionId for the inbound receipt")
	}
	assertProblem(t, s.call(http.MethodPost, "/vendor/inbound-receipts", strings.Replace(receipt, sscc1, pallet, 1), "rcpt-2", http.StatusUnprocessableEntity), "validation-failed")
	assertProblem(t, s.call(http.MethodPost, "/vendor/inbound-receipts", strings.Replace(receipt, "SHP-1", "SHP-404", 1), "rcpt-3", http.StatusNotFound), "not-found")
	if raw := waitTransaction(t, s, accepted.TransactionID, "SUCCESS"); !strings.Contains(raw, `"kind":"INBOUND_RECEIPT"`) {
		t.Fatalf("transaction = %s", raw)
	}
	w.expect("InboundReceiptSubmitted", "SHP-1")
	w.expect("SubmissionProcessed", accepted.TransactionID)

	// seller-inbound closes the shipment: it leaves the poll and cannot be received again.
	produce(t, w.brokers, topicInbound, kafkago.Message{Key: []byte("SHP-1"), Value: inputEvent(t, "evt-i3", src, prefix+"InboundShipmentReceived", "SHP-1",
		`{"shipmentId":"SHP-1","sellerId":"S-1","nodeId":"n-1","status":"CLOSED_WITH_DISCREPANCY","receivedAt":"2026-10-23T10:00:00Z"}`)})
	waitFor(t, 60*time.Second, "the shipment to close", func() bool {
		return w.db.count(`SELECT count(*) FROM inbound_shipments WHERE status = 'CLOSED_WITH_DISCREPANCY'`) == 1
	})
	mustDecode(t, s.call(http.MethodGet, "/vendor/inbound-shipments?vendorId=V-1", "", "", http.StatusOK), &page)
	if len(page.InboundShipments) != 0 {
		t.Fatalf("a received shipment is still polled: %+v", page)
	}
	assertProblem(t, s.call(http.MethodPost, "/vendor/inbound-receipts", receipt, "rcpt-4", http.StatusConflict), "illegal-transition")
}

func phaseNUL(t *testing.T, s *service) {
	t.Helper()
	assertProblem(t, s.call(http.MethodGet, "/vendor/purchase-orders/a%00b?vendorId=V-1", "", "", http.StatusNotFound), "not-found")
	assertProblem(t, s.call(http.MethodGet, "/vendor/nodes?vendorId=a%00b", "", "", http.StatusUnprocessableEntity), "validation-failed")
	assertProblem(t, s.call(http.MethodPost, "/vendor/acknowledgements", "{\"vendorId\":\"V-1\",\"poNumber\":\"a\\u0000b\"}", "nul-1", http.StatusUnprocessableEntity), "validation-failed")
}

// Phase 5: the rows are in Postgres and the relay drained every outbox row.
func phaseRows(t *testing.T, w *world) {
	t.Helper()
	db := w.db
	for table, want := range map[string]int{"vendor_accounts": 1, "purchase_orders": 2, "submissions": 5, "inbound_shipments": 1, "catalog_packaging": 1} {
		if n := db.count(`SELECT count(*) FROM ` + table); n != want {
			t.Fatalf("%s rows = %d, want %d", table, n, want)
		}
	}
	for state, want := range map[string]int{"SHIPPED": 1, "CANCELLED": 1} {
		if n := db.count(`SELECT count(*) FROM purchase_orders WHERE state = $1`, state); n != want {
			t.Fatalf("purchase_orders in %s = %d, want %d", state, n, want)
		}
	}
	if n := db.count(`SELECT count(*) FROM processed_events`); n != 11 {
		t.Fatalf("processed_events = %d, want 11 (3 node, 1 listing, 2 releases, 1 cancellation, 3 inbound shipment, 1 packaging)", n)
	}
	if n := db.count(`SELECT count(*) FROM outbox_events`); n != len(w.expected)+len(w.analyze) {
		t.Fatalf("outbox rows = %d, want %d", n, len(w.expected)+len(w.analyze))
	}
	waitFor(t, 60*time.Second, "the relay to publish every outbox row", func() bool {
		return db.count(`SELECT count(*) FROM outbox_events WHERE published_at IS NULL`) == 0
	})
}

// Phase 6: the CloudEvents on both output topics, exact envelope, key and order.
func phaseTopics(t *testing.T, w *world) {
	t.Helper()
	for i, m := range consume(t, w.brokers, topicEvents, len(w.expected)) {
		assertEnvelope(t, m, w.expected[i], "events")
	}
	for i, m := range consume(t, w.brokers, topicAnalytics, len(w.analyze)) {
		assertEnvelope(t, m, w.analyze[i], "analytics")
	}
	var accepted struct {
		PONumber, AssignmentID, NodeID, TransactionID, AcceptedAt string
		ReleaseNo                                                 int
	}
	for _, m := range consume(t, w.brokers, topicEvents, len(w.expected)) {
		if e := decodeEnvelope(t, m); strings.HasSuffix(e.Type, ".PurchaseOrderAccepted") {
			var d struct {
				PONumber      string `json:"poNumber"`
				AssignmentID  string `json:"assignmentId"`
				ReleaseNo     int    `json:"releaseNo"`
				NodeID        string `json:"nodeId"`
				TransactionID string `json:"transactionId"`
				AcceptedAt    string `json:"acceptedAt"`
			}
			mustDecode(t, string(e.Data), &d)
			accepted.PONumber, accepted.AssignmentID, accepted.ReleaseNo, accepted.NodeID, accepted.TransactionID, accepted.AcceptedAt =
				d.PONumber, d.AssignmentID, d.ReleaseNo, d.NodeID, d.TransactionID, d.AcceptedAt
		}
	}
	if accepted.AssignmentID != "asg-1" || accepted.ReleaseNo != 1 || accepted.NodeID != "n-1" || accepted.TransactionID == "" || accepted.AcceptedAt == "" {
		t.Fatalf("PurchaseOrderAccepted data = %+v", accepted)
	}
}

// Phase 7: restart the real entry point in-process on the same database and
// broker: nothing is lost, nothing is published or consumed twice.
func phaseRestart(t *testing.T, w *world, s *service) *service {
	t.Helper()
	s.stop()
	s2 := w.e.start(t)
	s2.call(http.MethodGet, "/vendors/V-1", "", "", http.StatusOK)
	// Give the restarted consumers time to (wrongly) re-apply and the relay to
	// (wrongly) re-publish; neither may happen.
	time.Sleep(3 * time.Second)
	if n := w.db.count(`SELECT count(*) FROM processed_events`); n != 11 {
		t.Fatalf("processed_events after the restart = %d, want still 11", n)
	}
	noMoreMessages(t, w.brokers, topicEvents, len(w.expected))
	noMoreMessages(t, w.brokers, topicAnalytics, len(w.analyze))
	return s2
}

// Phase 8: /readyz follows Postgres going away (Kafka still up): the 503
// problem names only postgres and /healthz stays liveness (200). Then Kafka
// goes away: /readyz is 503 dependency-unavailable while /healthz stays 200,
// and the service EXITS non-zero naming the dead worker, so the orchestrator
// restarts it instead of leaving a pod alive with consumers that never return.
func phaseDependenciesGoAway(t *testing.T, s *service, w *world) {
	t.Helper()
	stopTimeout := 10 * time.Second
	if err := w.pg.Stop(context.Background(), &stopTimeout); err != nil {
		t.Fatalf("stop postgres: %v", err)
	}
	body := waitReadyzStatus(t, s, http.StatusServiceUnavailable, "/readyz to turn 503 once Postgres is gone")
	if !strings.Contains(body, "dependency-unavailable") || !strings.Contains(body, "postgres") || strings.Contains(body, "kafka") {
		t.Fatalf("readyz with Postgres down = %s, want dependency-unavailable naming only postgres", body)
	}
	if status, body := s.try(http.MethodGet, "/healthz", "", ""); status != http.StatusOK {
		t.Fatalf("healthz with Postgres down = %d %s, want 200 (liveness only)", status, body)
	}
	if err := w.kc.Stop(context.Background(), &stopTimeout); err != nil {
		t.Fatalf("stop kafka: %v", err)
	}
	err := s.expectExit()
	if err == nil || !strings.Contains(err.Error(), "stopped unexpectedly") {
		t.Fatalf("run returned %v after Kafka went away, want a 'stopped unexpectedly' error naming the dead worker", err)
	}
}

// TestKafkaGoneTurnsReadyzUnavailable stops ONLY Kafka: /readyz is 503
// dependency-unavailable naming kafka, /healthz stays 200.
func TestKafkaGoneTurnsReadyzUnavailable(t *testing.T) {
	_, dbURL := startPostgres(t)
	kc, brokers := startKafka(t)
	for _, topic := range []string{topicNodes, topicOffers, topicSourcing} {
		createTopic(t, brokers, topic)
	}
	stub := newNeighbourStub(t)
	e := env{dbURL: dbURL, brokers: brokers, addr: freeAddr(t), neighbours: stub.srv.URL}
	s := e.start(t)
	if status, body := readyz(s); status != http.StatusOK {
		t.Fatalf("readyz = %d %s", status, body)
	}
	stopTimeout := 10 * time.Second
	if err := kc.Stop(context.Background(), &stopTimeout); err != nil {
		t.Fatalf("stop kafka: %v", err)
	}
	body := waitReadyzStatus(t, s, http.StatusServiceUnavailable, "/readyz to turn 503 once Kafka is gone")
	if !strings.Contains(body, "dependency-unavailable") || !strings.Contains(body, "kafka") || strings.Contains(body, "postgres") {
		t.Fatalf("readyz with Kafka down = %s, want dependency-unavailable naming only kafka", body)
	}
	if status, body := s.try(http.MethodGet, "/healthz", "", ""); status != http.StatusOK {
		t.Fatalf("healthz with Kafka down = %d %s, want 200", status, body)
	}
}

func TestRunOnRealPostgresAndKafka(t *testing.T) {
	pg, dbURL := startPostgres(t)
	kc, brokers := startKafka(t)
	// The input topics are the producers': create them the way the cluster has them.
	// The consumed topics exist before the first start, as in the cluster: a group
	// that joins before its topic exists is handed no partition and kafka-go does
	// not retry the assignment.
	for _, topic := range []string{topicNodes, topicOffers, topicSourcing, topicInbound} {
		createTopic(t, brokers, topic)
	}
	stub := newNeighbourStub(t)
	w := &world{e: env{dbURL: dbURL, brokers: brokers, addr: freeAddr(t), neighbours: stub.srv.URL}, pg: pg, kc: kc, db: newPGView(t, dbURL), brokers: brokers, stub: stub}

	s := w.e.start(t)
	phaseSetup(t, s, w)
	phaseWorkflow(t, s, w)
	phaseNeighboursAndCancel(t, s, w)
	phaseInbound(t, s, w)
	phaseNUL(t, s)
	phaseRows(t, w)
	phaseTopics(t, w)
	s = phaseRestart(t, w, s)
	phaseDependenciesGoAway(t, s, w)
}
