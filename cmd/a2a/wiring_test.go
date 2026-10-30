package main

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"
)

// The boot migrations run against MIGRATIONS_DATABASE_URL (a DIRECT Postgres
// DSN where DATABASE_URL points at PgBouncer, which cannot honour the
// session-scoped advisory lock), never against DATABASE_URL. Both DSNs are
// unreachable here; the retry log of the migration step names the host:port
// it dialled, so the test pins WHICH of the two it used. (A cancelled context
// would lose the cause: the error would wrap only ctx.Err(), so a short
// deadline is used and the logged `retrying ... err=` line is asserted.)
func TestBuildDeps_MigrationsUseTheDirectDSN(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))

	_, _, err := buildDeps(ctx, logger,
		"postgres://u@127.0.0.1:1/runtime", "postgres://u@127.0.0.1:2/direct", fakeNeighbourURLs())
	if err == nil {
		t.Fatal("unreachable databases must fail boot")
	}
	logged := buf.String()
	if !strings.Contains(logged, "run migrations") || !strings.Contains(logged, "retrying") {
		t.Fatalf("no migration retry was logged: %q", logged)
	}
	if !strings.Contains(logged, "127.0.0.1:2") {
		t.Fatalf("the migrations did not dial MIGRATIONS_DATABASE_URL (127.0.0.1:2): %q", logged)
	}
	if strings.Contains(logged, "127.0.0.1:1") {
		t.Fatalf("the migrations dialled DATABASE_URL (127.0.0.1:1) instead of the direct DSN: %q", logged)
	}
}
