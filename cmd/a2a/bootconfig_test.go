package main

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func quietLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// fakeNeighbourURLs answers the four sync-call env vars for buildDeps: a
// local server that 404s everything except the scorecard relay.
func fakeNeighbourURLs() map[string]string {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/scorecards/n-1" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"nodeId":"n-1","scorecards":[` +
				`{"scorecardId":"s1","nodeId":"n-1","window":"7d","state":"GRADED","grade":"A","computedAt":"2026-10-24T08:00:00Z",` +
				`"metrics":{"observations":120,"onTimeHandoverRate":0.97}}]}`))
			return
		}
		http.NotFound(w, r)
	}))
	return map[string]string{
		"NODE_TOPOLOGY_URL":         srv.URL,
		"OFFER_AVAILABILITY_URL":    srv.URL,
		"SHIPPING_AND_TRACKING_URL": srv.URL,
		"NODE_PERFORMANCE_URL":      srv.URL,
	}
}

// A missing DATABASE_URL is a boot error, never a silent in-memory fallback.
func TestBuildDeps_MissingDatabaseFailsBoot(t *testing.T) {
	_, _, err := buildDeps(context.Background(), quietLogger(), "", "", fakeNeighbourURLs())
	if err == nil || !strings.Contains(err.Error(), "DATABASE_URL") {
		t.Fatalf("buildDeps without a database = %v, want an error naming DATABASE_URL", err)
	}
}

// An unreachable database is a boot error after the bounded retries.
func TestBuildDeps_BadDatabaseFailsBoot(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // a cancelled context makes bootretry give up at once
	if _, _, err := buildDeps(ctx, quietLogger(), "postgres://u@127.0.0.1:1/db", "postgres://u@127.0.0.1:1/db", fakeNeighbourURLs()); err == nil {
		t.Fatal("an unreachable database must fail boot")
	}
}

// Every neighbour URL is mandatory and its boot error names the variable.
func TestLoadNeighbourURLs(t *testing.T) {
	full := map[string]string{"NODE_TOPOLOGY_URL": "http://a", "OFFER_AVAILABILITY_URL": "http://b", "SHIPPING_AND_TRACKING_URL": "http://c", "NODE_PERFORMANCE_URL": "http://d"}
	for _, missing := range []string{"NODE_TOPOLOGY_URL", "OFFER_AVAILABILITY_URL", "SHIPPING_AND_TRACKING_URL", "NODE_PERFORMANCE_URL"} {
		env := map[string]string{}
		for k, v := range full {
			if k != missing {
				env[k] = v
			}
		}
		_, err := loadNeighbourURLs(func(k string) string { return env[k] })
		if err == nil || !strings.Contains(err.Error(), missing) {
			t.Errorf("loadNeighbourURLs without %s = %v, want an error naming it", missing, err)
		}
	}
	if got, err := loadNeighbourURLs(func(k string) string { return full[k] }); err != nil || len(got) != 4 {
		t.Errorf("loadNeighbourURLs with every URL = %v, %v", got, err)
	}
}

// A2A_AGENT_URL is mandatory and must be an absolute http(s) URL: the Agent
// Card advertises it, so anything else would publish a card no client can
// dial. Every refusal names the variable.
func TestLoadAgentURL(t *testing.T) {
	refused := map[string]string{
		"unset":            "",
		"relative":         "/a2a",
		"bare host":        "vendor-connectivity-a2a:8093",
		"no host":          "http://",
		"unsupported":      "ftp://vendor-connectivity-a2a:8093/",
		"scheme-less path": "vendor-connectivity-a2a/a2a",
		"not a URL":        "http://[::1",
	}
	for name, value := range refused {
		t.Run(name, func(t *testing.T) {
			got, err := loadAgentURL(func(string) string { return value })
			if err == nil || !strings.Contains(err.Error(), "A2A_AGENT_URL") {
				t.Fatalf("loadAgentURL(%q) = %q, %v; want an error naming A2A_AGENT_URL", value, got, err)
			}
		})
	}
	for _, ok := range []string{"http://vendor-connectivity-a2a.warehouse-systems.svc.cluster.local:8093/", "https://agents.example.invalid/vendor-connectivity/"} {
		if got, err := loadAgentURL(func(string) string { return ok }); err != nil || got != ok {
			t.Errorf("loadAgentURL(%q) = %q, %v; want it accepted verbatim", ok, got, err)
		}
	}
}

// Every mandatory variable refuses the boot BEFORE the listener binds: run()
// returns the error naming the variable and the address stays free.
func TestRun_RefusesToBootBeforeTheListenerBinds(t *testing.T) {
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := probe.Addr().String()
	_ = probe.Close()

	full := map[string]string{
		"A2A_ADDR": addr, "A2A_AGENT_URL": "http://agent.example.invalid:8093/",
		"NODE_TOPOLOGY_URL": "http://a", "OFFER_AVAILABILITY_URL": "http://b",
		"SHIPPING_AND_TRACKING_URL": "http://c", "NODE_PERFORMANCE_URL": "http://d",
		"DATABASE_URL": "postgres://u@127.0.0.1:1/db",
	}
	for _, missing := range []string{"A2A_AGENT_URL", "NODE_TOPOLOGY_URL", "OFFER_AVAILABILITY_URL", "SHIPPING_AND_TRACKING_URL", "NODE_PERFORMANCE_URL"} {
		t.Run(missing, func(t *testing.T) {
			for k, v := range full {
				t.Setenv(k, v)
			}
			t.Setenv(missing, "")
			err := run()
			if err == nil || !strings.Contains(err.Error(), missing) {
				t.Fatalf("run() without %s = %v, want an error naming it", missing, err)
			}
			l, lerr := net.Listen("tcp", addr)
			if lerr != nil {
				t.Fatalf("the listener bound before the boot refusal: %v", lerr)
			}
			_ = l.Close()
		})
	}
}

func TestGetenv(t *testing.T) {
	t.Setenv("VC_A2A_TEST_SET", "v")
	t.Setenv("VC_A2A_TEST_EMPTY", "")
	if got := getenv("VC_A2A_TEST_SET", "d"); got != "v" {
		t.Errorf("set: %q", got)
	}
	if got := getenv("VC_A2A_TEST_EMPTY", "d"); got != "d" {
		t.Errorf("empty: %q", got)
	}
}

func TestNewLoggerLevels(t *testing.T) {
	for level, want := range map[string]slog.Level{"debug": slog.LevelDebug, "WARN": slog.LevelWarn, "warning": slog.LevelWarn, "error": slog.LevelError, "": slog.LevelInfo, "bogus": slog.LevelInfo} {
		if got := newLogger(level); !got.Handler().Enabled(context.Background(), want) || (want > slog.LevelDebug && got.Handler().Enabled(context.Background(), want-1)) {
			t.Errorf("newLogger(%q): level %v not the minimum enabled", level, want)
		}
	}
}
