package memory_test

import (
	"context"
	"errors"
	"strconv"
	"testing"

	"github.com/claudioed/vendor-connectivity/internal/adapters/outbound/memory"
	"github.com/claudioed/vendor-connectivity/internal/application/outbox"
)

func msg(eventID string) outbox.Message {
	return outbox.Message{EventID: eventID, Topic: "retailnetwork.vendor-connectivity.events", EventType: "VendorOnboarded", Subject: "V-1", Key: []byte("V-1"), Value: []byte(eventID)}
}

func ids(ms []outbox.Message) []string {
	out := make([]string, 0, len(ms))
	for _, m := range ms {
		out = append(out, m.EventID)
	}
	return out
}

func seededOutbox(t *testing.T, n int) *memory.OutboxRepo {
	t.Helper()
	r := memory.NewOutboxRepo()
	ctx := context.Background()
	for i := 1; i <= n; i++ {
		if err := r.Insert(ctx, msg("e-"+strconv.Itoa(i))); err != nil {
			t.Fatal(err)
		}
	}
	return r
}

// The OutboxRepo double mirrors the Postgres outbox table: Messages()
// reports every row in insertion order, published or not.
func TestOutboxRepoMessagesKeepsInsertionOrder(t *testing.T) {
	t.Parallel()
	r := seededOutbox(t, 3)
	if got := r.Unpublished(); got != 3 {
		t.Fatalf("unpublished = %d, want 3", got)
	}
	if ms := r.Messages(); len(ms) != 3 || ms[0].EventID != "e-1" || ms[2].EventID != "e-3" {
		t.Fatalf("Messages() = %v, want the three rows in insertion order", ids(ms))
	}
	published, err := r.Drain(context.Background(), 10, 0, func(context.Context, outbox.Message) error { return nil })
	if err != nil || published != 3 {
		t.Fatalf("drain = %d, %v; want everything published", published, err)
	}
	if got := r.Unpublished(); got != 0 {
		t.Fatalf("unpublished after the pass = %d, want 0", got)
	}
	if ms := r.Messages(); len(ms) != 3 {
		t.Fatalf("Messages() must keep every row, published or not: %v", ids(ms))
	}
}

// A failed send records its error on the row, leaves it unpublished and
// reports it as the SendFailure of that row.
func TestOutboxRepoRecordsTheFailureOfItsRow(t *testing.T) {
	t.Parallel()
	r := seededOutbox(t, 3)
	boom := errors.New("kafka: broker unavailable")
	published, err := r.Drain(context.Background(), 10, 0, func(context.Context, outbox.Message) error { return boom })
	var sf *outbox.SendFailure
	if published != 0 || !errors.As(err, &sf) || sf.RowID != 1 || !errors.Is(sf.Err, boom) {
		t.Fatalf("drain = %d, %v; want row 1 failing with the send error", published, err)
	}
	if got := r.Unpublished(); got != 3 {
		t.Fatalf("unpublished after the failure = %d, want 3", got)
	}
	if le := r.LastError(0); le != boom.Error() {
		t.Fatalf("LastError(0) = %q, want %q", le, boom.Error())
	}
	if le := r.LastError(1); le != "" {
		t.Fatalf("LastError(1) = %q, want none yet", le)
	}
}

// With maxAttempts = 1 a row that already failed once is skipped and the
// healthy rows behind it still publish.
func TestOutboxRepoSkipsPoisonedRows(t *testing.T) {
	t.Parallel()
	r := seededOutbox(t, 3)
	_, err := r.Drain(context.Background(), 10, 0, func(context.Context, outbox.Message) error {
		return errors.New("kafka: broker unavailable")
	})
	if err == nil {
		t.Fatal("the first pass must fail on row 1")
	}
	published, err := r.Drain(context.Background(), 10, 1, func(context.Context, outbox.Message) error { return nil })
	if err != nil || published != 2 {
		t.Fatalf("drain = %d, %v; want the two healthy rows published", published, err)
	}
	if got := r.Unpublished(); got != 1 {
		t.Fatalf("unpublished = %d, want only the poisoned row", got)
	}
}
