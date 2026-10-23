package clock_test

import (
	"testing"
	"time"

	"github.com/claudioed/vendor-connectivity/internal/adapters/outbound/clock"
)

func TestSystemClockIsUTCAndCurrent(t *testing.T) {
	before := time.Now()
	got := clock.System{}.Now()
	if got.Location() != time.UTC || got.Before(before.Add(-time.Second)) || got.After(time.Now().Add(time.Second)) {
		t.Fatalf("Now() = %v", got)
	}
}
