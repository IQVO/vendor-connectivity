//go:build integration

package postgres_test

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/claudioed/vendor-connectivity/internal/adapters/outbound/postgres"
	"github.com/claudioed/vendor-connectivity/internal/adapters/outbound/repocontract"
	"github.com/claudioed/vendor-connectivity/internal/domain/purchaseorder"
	"github.com/claudioed/vendor-connectivity/internal/testing/pgtest"
)

// latestMigration is the ceiling of the embedded migrations; bump it with
// every new migration file.
const latestMigration = 5

// ownerRefMigration is 0004_po_line_owner_ref (ADR 0012).
const ownerRefMigration = 4

func wantVersion(t *testing.T, url string, want uint) {
	t.Helper()
	v, dirty, err := postgres.MigrationVersion(url)
	if err != nil || dirty || v != want {
		t.Fatalf("migration version = %d (dirty %v, err %v), want %d", v, dirty, err, want)
	}
}

// storedLines returns the raw purchase_orders.lines elements of one order.
func storedLines(t *testing.T, pool *pgtest.Pool, po purchaseorder.Number) []map[string]any {
	t.Helper()
	var raw []byte
	if err := pool.QueryRow(context.Background(), `SELECT lines FROM purchase_orders WHERE po_number = $1`, string(po)).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var ls []map[string]any
	if err := json.Unmarshal(raw, &ls); err != nil {
		t.Fatal(err)
	}
	return ls
}

func owners(ls []map[string]any) []any {
	out := make([]any, len(ls))
	for i, l := range ls {
		v, ok := l["ownerRef"]
		if !ok {
			v = "<absent>"
		}
		out[i] = v
	}
	return out
}

// seedBeforeOwnerRef migrates a fresh database to the version before 0004 and
// stores two orders: "old" with its ownerRef keys stripped (a row written
// before ADR 0012) and "seller" whose second line names a seller.
func seedBeforeOwnerRef(t *testing.T) (url string, pool *pgtest.Pool, old, seller purchaseorder.Number) {
	t.Helper()
	ctx := context.Background()
	url = pgtest.EmptyURL(t)
	if err := postgres.MigrateTo(url, ownerRefMigration-1); err != nil {
		t.Fatal(err)
	}
	wantVersion(t, url, ownerRefMigration-1)
	pool = pgtest.Open(t, url)
	repo := postgres.NewPurchaseOrderRepo(pool)
	o := repocontract.NewOrder(t, "asg-old", "V-1", "n-1", repocontract.T0)
	s := repocontract.NewOrder(t, "asg-seller", "V-1", "n-1", repocontract.T0)
	for _, p := range []*purchaseorder.PurchaseOrder{o, s} {
		if err := repo.Save(ctx, p); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pool.Exec(ctx, `UPDATE purchase_orders SET lines = (SELECT jsonb_agg(l - 'ownerRef' ORDER BY ord)
		FROM jsonb_array_elements(lines) WITH ORDINALITY AS t(l, ord)) WHERE po_number = $1`, string(o.Number())); err != nil {
		t.Fatal(err)
	}
	if got := owners(storedLines(t, pool, o.Number())); !reflect.DeepEqual(got, []any{"<absent>", "<absent>"}) {
		t.Fatalf("seeded old row owners = %v", got)
	}
	return url, pool, o.Number(), s.Number()
}

// TestMigrationOwnerRefBackfillsExistingLines: 0004 writes ownerRef "" into
// every stored line, keeps the line order and leaves a named owner alone; the
// repository then reads the old row as operator-owned.
func TestMigrationOwnerRefBackfillsExistingLines(t *testing.T) {
	url, pool, old, seller := seedBeforeOwnerRef(t)
	if err := postgres.RunMigrations(url); err != nil {
		t.Fatal(err)
	}
	wantVersion(t, url, latestMigration)
	ls := storedLines(t, pool, old)
	if got := owners(ls); !reflect.DeepEqual(got, []any{"", ""}) {
		t.Fatalf("backfilled owners = %v, want two empty", got)
	}
	if ls[0]["listingId"] != "lst-1" || ls[1]["listingId"] != "lst-2" || ls[0]["lineNo"] != float64(1) || ls[1]["lineNo"] != float64(2) {
		t.Fatalf("line order or content changed: %v", ls)
	}
	if got := owners(storedLines(t, pool, seller)); !reflect.DeepEqual(got, []any{"", "seller-7"}) {
		t.Fatalf("named owner must survive the backfill: %v", got)
	}
	p, err := postgres.NewPurchaseOrderRepo(pool).Get(context.Background(), old)
	if err != nil {
		t.Fatal(err)
	}
	if l := p.Lines(); len(l) != 2 || l[0].OwnerRef != "" || l[1].OwnerRef != "" {
		t.Fatalf("old row lines = %+v, want operator-owned", l)
	}
}

// TestMigrationOwnerRefDownAndUpAgain walks down one step (the key is
// dropped from every line) and up again (backfilled as operator-owned).
func TestMigrationOwnerRefDownAndUpAgain(t *testing.T) {
	url, pool, old, seller := seedBeforeOwnerRef(t)
	if err := postgres.RunMigrations(url); err != nil {
		t.Fatal(err)
	}
	// Step down to exactly the version before 0004: with later migrations
	// stacked on top (0005 today), that is more than one step.
	if err := postgres.StepMigrations(url, int(ownerRefMigration-1-latestMigration)); err != nil {
		t.Fatal(err)
	}
	wantVersion(t, url, ownerRefMigration-1)
	for _, po := range []purchaseorder.Number{old, seller} {
		if got := owners(storedLines(t, pool, po)); !reflect.DeepEqual(got, []any{"<absent>", "<absent>"}) {
			t.Fatalf("%s after down: owners = %v", po, got)
		}
	}
	if err := postgres.RunMigrations(url); err != nil {
		t.Fatal(err)
	}
	wantVersion(t, url, latestMigration)
	for _, po := range []purchaseorder.Number{old, seller} {
		if got := owners(storedLines(t, pool, po)); !reflect.DeepEqual(got, []any{"", ""}) {
			t.Fatalf("%s after up again: owners = %v", po, got)
		}
	}
}
