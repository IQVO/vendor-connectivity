package repocontract

import (
	"context"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/claudioed/vendor-connectivity/internal/application/ports"
	"github.com/claudioed/vendor-connectivity/internal/domain/submission"
)

func submissions(t *testing.T, e Env) {
	ctx := context.Background()
	_, err := e.Submissions.Get(ctx, "tx-0")
	isErr(t, err, ports.ErrNotFound, "Get on empty")
	s := NewSubmission(t, "tx-1", submission.KindInventory, T0, time.Minute)
	must(t, e.Submissions.Save(ctx, s))
	got, err := e.Submissions.Get(ctx, "tx-1")
	must(t, err)
	if !reflect.DeepEqual(got.Snapshot(), s.Snapshot()) {
		t.Fatalf("round trip:\n got %+v\nwant %+v", got.Snapshot(), s.Snapshot())
	}
	isErr(t, e.Submissions.Save(ctx, NewSubmission(t, "tx-1", submission.KindInventory, T0, 0)), ports.ErrConcurrentUpdate, "second create")
	for i, delay := range []time.Duration{0, 2 * time.Minute, 30 * time.Second} {
		must(t, e.Submissions.Save(ctx, NewSubmission(t, fmt.Sprintf("tx-d%d", i), submission.KindAcknowledgement, T0, delay)))
	}
	due, err := e.Submissions.ListDue(ctx, T0.Add(time.Minute), 10)
	must(t, err)
	ids := make([]string, len(due))
	for i, d := range due {
		ids[i] = string(d.ID())
	}
	if !reflect.DeepEqual(ids, []string{"tx-d0", "tx-d2", "tx-1"}) {
		t.Fatalf("due (notBefore <= now, oldest first) = %v", ids)
	}
	one, _ := e.Submissions.ListDue(ctx, T0.Add(time.Minute), 1)
	if len(one) != 1 {
		t.Fatalf("limit: %d", len(one))
	}
	processedAndPurged(t, ctx, e)
}

func processedAndPurged(t *testing.T, ctx context.Context, e Env) {
	t.Helper()
	p, err := e.Submissions.Get(ctx, "tx-d0")
	must(t, err)
	_, err = p.Complete(submission.Outcome{Status: submission.Failure, Errors: []submission.Error{{Code: "ErrX", Message: "m", Path: "$.a"}}}, T0.Add(time.Second))
	must(t, err)
	must(t, e.Submissions.Save(ctx, p))
	again, _ := e.Submissions.Get(ctx, "tx-d0")
	if !reflect.DeepEqual(again.Snapshot(), p.Snapshot()) {
		t.Fatalf("outcome round trip:\n got %+v\nwant %+v", again.Snapshot(), p.Snapshot())
	}
	due, _ := e.Submissions.ListDue(ctx, T0.Add(time.Hour), 10)
	if len(due) != 3 {
		t.Fatalf("a processed submission is not due: %d", len(due))
	}
	n, err := e.Submissions.DeleteProcessedBefore(ctx, T0)
	must(t, err)
	if n != 0 {
		t.Fatalf("nothing was processed before T0: %d", n)
	}
	n, err = e.Submissions.DeleteProcessedBefore(ctx, T0.Add(time.Minute))
	must(t, err)
	if n != 1 {
		t.Fatalf("one processed submission is old: %d", n)
	}
	_, err = e.Submissions.Get(ctx, "tx-d0")
	isErr(t, err, ports.ErrNotFound, "purged")
	if _, err := e.Submissions.Get(ctx, "tx-1"); err != nil {
		t.Fatalf("PROCESSING submissions are never purged: %v", err)
	}
}
