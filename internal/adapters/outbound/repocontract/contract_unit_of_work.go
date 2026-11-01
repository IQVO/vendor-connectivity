package repocontract

import (
	"context"
	"testing"

	"github.com/claudioed/vendor-connectivity/internal/application/ports"
	"github.com/claudioed/vendor-connectivity/internal/domain/shared"
	"github.com/claudioed/vendor-connectivity/internal/domain/vendor"
)

type boom struct{}

func (boom) Error() string { return "boom" }

func unitOfWork(t *testing.T, e Env) {
	ctx := context.Background()
	count := func() int {
		n, err := e.OutboxCount(ctx)
		must(t, err)
		return n
	}
	ev := []shared.Event{vendor.VendorOnboarded{Header: shared.Header{Version: 1, At: T0}, VendorID: "V-1", Name: "n", AckWindowMinutes: 60}}
	// commit
	err := e.UoW.Do(ctx, func(ctx context.Context) error {
		must(t, e.Vendors.Save(ctx, NewVendor(t, "V-1")))
		must(t, e.Outbox.Append(ctx, ev...))
		_, err := e.Inbox.Claim(ctx, "evt-commit", "t", T0)
		return err
	})
	must(t, err)
	if _, err := e.Vendors.Get(ctx, "V-1"); err != nil || count() != 1 {
		t.Fatalf("commit must persist the vendor and its outbox row: %v %d", err, count())
	}
	// rollback
	err = e.UoW.Do(ctx, func(ctx context.Context) error {
		must(t, e.Vendors.Save(ctx, NewVendor(t, "V-2")))
		must(t, e.Outbox.Append(ctx, ev...))
		must(t, e.Orders.Save(ctx, NewOrder(t, "asg-rb", "V-2", "n-1", T0)))
		if _, err := e.Inbox.Claim(ctx, "evt-rollback", "t", T0); err != nil {
			return err
		}
		return boom{}
	})
	isErr(t, err, boom{}, "the unit returns fn's error unchanged")
	_, gerr := e.Vendors.Get(ctx, "V-2")
	_, oerr := e.Orders.FindByRelease(ctx, "asg-rb", 1)
	isErr(t, gerr, ports.ErrNotFound, "rolled-back vendor")
	isErr(t, oerr, ports.ErrNotFound, "rolled-back order")
	if count() != 1 {
		t.Fatalf("rolled-back outbox rows must vanish: %d", count())
	}
	claimed, err := e.Inbox.Claim(ctx, "evt-rollback", "t", T0)
	must(t, err)
	if !claimed {
		t.Fatal("a rolled-back inbox claim must vanish")
	}
	// re-entrant: an inner failure inside a joined unit rolls the OUTER unit back
	err = e.UoW.Do(ctx, func(ctx context.Context) error {
		must(t, e.Vendors.Save(ctx, NewVendor(t, "V-3")))
		return e.UoW.Do(ctx, func(ctx context.Context) error {
			must(t, e.Vendors.Save(ctx, NewVendor(t, "V-4")))
			return boom{}
		})
	})
	isErr(t, err, boom{}, "inner error")
	for _, id := range []vendor.ID{"V-3", "V-4"} {
		_, err := e.Vendors.Get(ctx, id)
		isErr(t, err, ports.ErrNotFound, "joined unit "+string(id))
	}
	err = e.UoW.Do(ctx, func(ctx context.Context) error {
		return e.UoW.Do(ctx, func(ctx context.Context) error { return e.Vendors.Save(ctx, NewVendor(t, "V-5")) })
	})
	must(t, err)
	if _, err := e.Vendors.Get(ctx, "V-5"); err != nil {
		t.Fatalf("joined unit commit: %v", err)
	}
}
