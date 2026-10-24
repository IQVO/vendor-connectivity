//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/claudioed/vendor-connectivity/internal/adapters/outbound/kafka"
	"github.com/claudioed/vendor-connectivity/internal/adapters/outbound/postgres"
	"github.com/claudioed/vendor-connectivity/internal/adapters/outbound/repocontract"
	"github.com/claudioed/vendor-connectivity/internal/application/outbox"
	"github.com/claudioed/vendor-connectivity/internal/application/ports"
	"github.com/claudioed/vendor-connectivity/internal/domain/vendor"
	"github.com/claudioed/vendor-connectivity/internal/testing/pgtest"
)

// The same behavioural contract the in-memory doubles pass, run against a real
// Postgres: every contract case gets its own database.
func TestRepositoryContract(t *testing.T) {
	repocontract.Run(t, func(t *testing.T) repocontract.Env {
		pool := pgtest.NewPool(t)
		repo := postgres.NewOutboxRepo(pool)
		return repocontract.Env{
			UoW: postgres.NewUnitOfWork(pool), Vendors: postgres.NewVendorRepo(pool), Orders: postgres.NewPurchaseOrderRepo(pool),
			Submissions: postgres.NewSubmissionRepo(pool), Nodes: postgres.NewNodeViewRepo(pool), Listings: postgres.NewListingViewRepo(pool),
			Shipments: postgres.NewInboundShipmentViewRepo(pool), Packaging: postgres.NewCatalogPackagingViewRepo(pool),
			Inbox: postgres.NewProcessedEventRepo(pool), Outbox: postgres.NewEventOutbox(repo, kafka.NewEncoder()),
			OutboxCount: func(ctx context.Context) (int, error) {
				var n int
				err := pool.QueryRow(ctx, `SELECT count(*) FROM outbox_events`).Scan(&n)
				return n, err
			},
		}
	})
}

// TestOutboxEventsAreCloudEventsOnTheRightTopic checks the stored rows: the
// CloudEvents type, subject, key and topic, and one id per row.
func TestOutboxEventsAreCloudEventsOnTheRightTopic(t *testing.T) {
	ctx := context.Background()
	pool := pgtest.NewPool(t)
	ob := postgres.NewEventOutbox(postgres.NewOutboxRepo(pool), kafka.NewEncoder())
	a, evs, err := vendor.Onboard(vendor.NewAccount{ID: "V-1", Name: "Carrier A", AckWindowMinutes: 120}, repocontract.T0)
	if err != nil || a == nil {
		t.Fatal(err)
	}
	if err := ob.Append(ctx, evs...); err != nil {
		t.Fatal(err)
	}
	var topic, typ, subject, key string
	if err := pool.QueryRow(ctx, `SELECT topic, event_type, subject, convert_from(key, 'UTF8') FROM outbox_events`).Scan(&topic, &typ, &subject, &key); err != nil {
		t.Fatal(err)
	}
	if topic != kafka.TopicEvents || typ != "com.retailnetwork.partner.vendor-connectivity.vendoraccount.VendorOnboarded" || subject != "V-1" || key != "V-1" {
		t.Fatalf("row = %s %s %s %s", topic, typ, subject, key)
	}
}

// TestUnitOfWorkRollsBackAggregateOutboxAndClaimTogether proves the
// transactional outbox on real Postgres.
func TestUnitOfWorkRollsBackAggregateOutboxAndClaimTogether(t *testing.T) {
	ctx := context.Background()
	pool := pgtest.NewPool(t)
	vendors, uow := postgres.NewVendorRepo(pool), postgres.NewUnitOfWork(pool)
	ob := postgres.NewEventOutbox(postgres.NewOutboxRepo(pool), kafka.NewEncoder())
	inbox := postgres.NewProcessedEventRepo(pool)
	boom := errors.New("boom")
	err := uow.Do(ctx, func(ctx context.Context) error {
		a, evs, _ := vendor.Onboard(vendor.NewAccount{ID: "V-1", Name: "Carrier A", AckWindowMinutes: 120}, repocontract.T0)
		if err := vendors.Save(ctx, a); err != nil {
			return err
		}
		if err := ob.Append(ctx, evs...); err != nil {
			return err
		}
		if ok, err := inbox.Claim(ctx, "evt-1", "X", repocontract.T0); err != nil || !ok {
			return errors.Join(err, errors.New("claim"))
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("Do = %v", err)
	}
	for _, table := range []string{"vendor_accounts", "outbox_events", "processed_events"} {
		var n int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM `+table).Scan(&n); err != nil || n != 0 {
			t.Fatalf("%s rows after rollback = %d, %v", table, n, err)
		}
	}
}

// TestConcurrentCreateOfOneVendorHasOneWinner proves the primary key backs the
// optimistic concurrency rule for creates.
func TestConcurrentCreateOfOneVendorHasOneWinner(t *testing.T) {
	ctx := context.Background()
	repo := postgres.NewVendorRepo(pgtest.NewPool(t))
	const n = 6
	results := make([]error, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			a, _, _ := vendor.Onboard(vendor.NewAccount{ID: "V-1", Name: "Carrier A", AckWindowMinutes: 120}, repocontract.T0)
			results[i] = repo.Save(ctx, a)
		}()
	}
	wg.Wait()
	won := 0
	for _, err := range results {
		switch {
		case err == nil:
			won++
		case !errors.Is(err, ports.ErrConcurrentUpdate):
			t.Fatalf("a loser must get ErrConcurrentUpdate, got %v", err)
		}
	}
	if won != 1 {
		t.Fatalf("%d creates won, want 1", won)
	}
}

// TestDrainSendsInOrderAndStopsAtTheFirstFailure covers the relay's read side.
func TestDrainSendsInOrderAndStopsAtTheFirstFailure(t *testing.T) {
	ctx := context.Background()
	pool := pgtest.NewPool(t)
	repo := postgres.NewOutboxRepo(pool)
	ids := []int64{pgtest.InsertOutboxRow(t, pool, "t.A", "k1"), pgtest.InsertOutboxRow(t, pool, "t.B", "k1"), pgtest.InsertOutboxRow(t, pool, "t.C", "k2")}
	var sent []string
	n, err := repo.Drain(ctx, 10, 0, func(_ context.Context, m outbox.Message) error {
		if m.EventType == "t.B" {
			return errors.New("broker down")
		}
		sent = append(sent, m.EventType)
		return nil
	})
	var failure *outbox.SendFailure
	if n != 1 || !errors.As(err, &failure) || failure.Attempts != 1 || len(sent) != 1 {
		t.Fatalf("n=%d err=%v sent=%v", n, err, sent)
	}
	if _, last, published := pgtest.OutboxRowState(t, pool, ids[0]); !published || last != "" {
		t.Fatal("the first row is published")
	}
	if attempts, last, published := pgtest.OutboxRowState(t, pool, ids[1]); published || attempts != 1 || last != "broker down" {
		t.Fatalf("the failed row records its error: %d %q %v", attempts, last, published)
	}
	if _, _, published := pgtest.OutboxRowState(t, pool, ids[2]); published {
		t.Fatal("a later row never overtakes a failed one")
	}
	// With the attempt cap reached the failing row is skipped and later rows go.
	n, err = repo.Drain(ctx, 10, 1, func(_ context.Context, m outbox.Message) error { return nil })
	if err != nil || n != 1 {
		t.Fatalf("capped drain: n=%d err=%v", n, err)
	}
	if _, _, published := pgtest.OutboxRowState(t, pool, ids[2]); !published {
		t.Fatal("rows overtake a capped one")
	}
}
