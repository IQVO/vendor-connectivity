package kafka

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"testing"
	"time"

	kafkago "github.com/segmentio/kafka-go"
)

// The dead-letter publish path (writeDLQ / publishDeadLetter / isTopicNotReady)
// against a fake DeadLetterWriter: no broker needed anywhere.

func TestIsTopicNotReady(t *testing.T) {
	t.Parallel()
	notReady := []error{
		kafkago.UnknownTopicOrPartition,
		kafkago.LeaderNotAvailable,
		fmt.Errorf("write: %w", kafkago.UnknownTopicOrPartition),
		kafkago.WriteErrors{0: nil, 1: kafkago.LeaderNotAvailable},
	}
	for i, err := range notReady {
		if !isTopicNotReady(err) {
			t.Fatalf("case %d: %v reported not transient, want topic-not-ready", i, err)
		}
	}
	other := []error{
		errors.New("broker down"),
		fmt.Errorf("write: %w", errors.New("context deadline exceeded")),
		kafkago.WriteErrors{},                            // no failed partition at all
		kafkago.WriteErrors{0: nil},                      // only successes
		kafkago.WriteErrors{1: errors.New("bad record")}, // a real failure mixed in
		nil,
	}
	for i, err := range other {
		if isTopicNotReady(err) {
			t.Fatalf("case %d: %v reported topic-not-ready, want a real error", i, err)
		}
	}
}

// recordingWriter is a DeadLetterWriter answering the scripted errors, in
// order, before it starts accepting.
type recordingWriter struct {
	failures []error
	written  []kafkago.Message
	closed   bool
}

func (w *recordingWriter) WriteMessages(_ context.Context, msgs ...kafkago.Message) error {
	if len(w.failures) > 0 {
		err := w.failures[0]
		w.failures = w.failures[1:]
		return err
	}
	w.written = append(w.written, msgs...)
	return nil
}

func (w *recordingWriter) Close() error { w.closed = true; return nil }

func TestWriteDLQStopsAtTheFirstNonTransientError(t *testing.T) {
	t.Parallel()
	real := errors.New("broker unreachable")
	w := &recordingWriter{failures: []error{real}}
	if err := writeDLQ(context.Background(), w, kafkago.Message{Topic: "t.dlq"}); !errors.Is(err, real) {
		t.Fatalf("writeDLQ = %v, want the untouched broker error", err)
	}
	if len(w.written) != 0 {
		t.Fatalf("nothing may be published: %v", w.written)
	}
}

func TestWriteDLQRetriesATopicWithoutItsLeader(t *testing.T) {
	t.Parallel()
	w := &recordingWriter{failures: []error{kafkago.UnknownTopicOrPartition}}
	msg := kafkago.Message{Topic: "t.dlq", Key: []byte("k")}
	if err := writeDLQ(context.Background(), w, msg); err != nil {
		t.Fatalf("writeDLQ = %v, want nil once the topic has a leader", err)
	}
	if len(w.written) != 1 || string(w.written[0].Key) != "k" {
		t.Fatalf("published %v, want the one message", w.written)
	}
}

func TestWriteDLQReturnsPromptlyOnCancel(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	w := &recordingWriter{failures: []error{kafkago.LeaderNotAvailable, kafkago.LeaderNotAvailable, kafkago.LeaderNotAvailable}}
	err := writeDLQ(ctx, w, kafkago.Message{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("writeDLQ = %v, want context.Canceled joined in", err)
	}
}

func headerOf(msg kafkago.Message, key string) (string, bool) {
	for _, h := range msg.Headers {
		if h.Key == key {
			return string(h.Value), true
		}
	}
	return "", false
}

func TestPublishDeadLetterCarriesTheOriginalMessageAndItsContext(t *testing.T) {
	t.Parallel()
	w := &recordingWriter{}
	cause := errors.New("transient handler failure after 5 attempts")
	src := kafkago.Message{
		Topic:     "retailnetwork.order-sourcing.events",
		Partition: 2,
		Offset:    41,
		Key:       []byte("asg-1"),
		Value:     []byte(`{"id":"evt-1"}`),
		Headers:   []kafkago.Header{{Key: "content-type", Value: []byte("application/cloudevents+json")}},
	}
	if err := publishDeadLetter(context.Background(), w, src.Topic+DLQSuffix, src, cause); err != nil {
		t.Fatal(err)
	}
	if len(w.written) != 1 {
		t.Fatalf("published %d messages, want 1", len(w.written))
	}
	out := w.written[0]
	if out.Topic != "retailnetwork.order-sourcing.events.dlq" {
		t.Fatalf("topic = %q", out.Topic)
	}
	if string(out.Key) != "asg-1" || string(out.Value) != `{"id":"evt-1"}` {
		t.Fatalf("key/value not preserved: %q %q", out.Key, out.Value)
	}
	if first, _ := headerOf(out, "content-type"); first != "application/cloudevents+json" {
		t.Fatalf("the original headers must lead: %v", out.Headers)
	}
	want := map[string]string{
		"x-dlq-source-topic":     src.Topic,
		"x-dlq-source-partition": strconv.Itoa(src.Partition),
		"x-dlq-source-offset":    strconv.FormatInt(src.Offset, 10),
		"x-dlq-error":            cause.Error(),
	}
	for k, v := range want {
		if got, ok := headerOf(out, k); !ok || got != v {
			t.Fatalf("header %s = %q (present %v), want %q", k, got, ok, v)
		}
	}
	at, ok := headerOf(out, "x-dlq-failed-at")
	if !ok {
		t.Fatal("x-dlq-failed-at header missing")
	}
	if _, err := time.Parse(time.RFC3339, at); err != nil {
		t.Fatalf("x-dlq-failed-at = %q, want an RFC 3339 instant: %v", at, err)
	}
}
