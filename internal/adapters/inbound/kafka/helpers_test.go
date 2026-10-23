package kafka_test

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	kafkago "github.com/segmentio/kafka-go"

	kafkaconsumer "github.com/claudioed/vendor-connectivity/internal/adapters/inbound/kafka"
)

func testLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// fastRetry keeps Run-loop tests quick while still exercising backoff.
var fastRetry = kafkaconsumer.RetryPolicy{Initial: time.Millisecond, Max: 4 * time.Millisecond}

// fakeReader is a scripted kafkaconsumer.Reader. It records every
// FetchMessage/CommitMessages in order so a test can assert that the offset
// was committed only after the handler succeeded, and exactly once. After the
// scripted messages it blocks until ctx is cancelled, like a real reader on an
// idle topic.
type fakeReader struct {
	mu       sync.Mutex
	msgs     []kafkago.Message
	next     int
	events   []string
	onCommit func()
}

func newFakeReader(values ...[]byte) *fakeReader {
	r := &fakeReader{}
	for i, v := range values {
		r.msgs = append(r.msgs, kafkago.Message{Partition: 0, Offset: int64(i), Value: v, Topic: "t"})
	}
	return r
}

func (r *fakeReader) FetchMessage(ctx context.Context) (kafkago.Message, error) {
	r.mu.Lock()
	if r.next < len(r.msgs) {
		m := r.msgs[r.next]
		r.next++
		r.events = append(r.events, fmt.Sprintf("fetch:%d", m.Offset))
		r.mu.Unlock()
		return m, nil
	}
	r.mu.Unlock()
	<-ctx.Done()
	return kafkago.Message{}, ctx.Err()
}

func (r *fakeReader) CommitMessages(_ context.Context, msgs ...kafkago.Message) error {
	r.mu.Lock()
	for _, m := range msgs {
		r.events = append(r.events, fmt.Sprintf("commit:%d", m.Offset))
	}
	fn := r.onCommit
	r.mu.Unlock()
	if fn != nil {
		fn()
	}
	return nil
}

func (r *fakeReader) Close() error { return nil }

func (r *fakeReader) log() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.events...)
}

func (r *fakeReader) commits() int {
	n := 0
	for _, e := range r.log() {
		if len(e) > 6 && e[:6] == "commit" {
			n++
		}
	}
	return n
}

// fakeDLQWriter records every message WriteMessages publishes.
type fakeDLQWriter struct {
	mu   sync.Mutex
	msgs []kafkago.Message
}

func (w *fakeDLQWriter) WriteMessages(_ context.Context, msgs ...kafkago.Message) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.msgs = append(w.msgs, msgs...)
	return nil
}

func (w *fakeDLQWriter) Close() error { return nil }

func (w *fakeDLQWriter) published() []kafkago.Message {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]kafkago.Message(nil), w.msgs...)
}

// runUntilCommits runs c.Run until the reader saw `want` commits (or fails
// after 10s), then cancels and returns Run's error.
func runUntilCommits(t *testing.T, c *kafkaconsumer.Consumer, reader *fakeReader, want int) error {
	t.Helper()
	c.Reader = reader
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- c.Run(ctx) }()
	deadline := time.After(10 * time.Second)
	for reader.commits() < want {
		select {
		case err := <-done:
			return err
		case <-deadline:
			t.Fatalf("timed out waiting for %d commits; events=%v", want, reader.log())
		case <-time.After(time.Millisecond):
		}
	}
	cancel()
	select {
	case err := <-done:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after cancel")
		return nil
	}
}
