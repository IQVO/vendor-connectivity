package ids_test

import (
	"testing"

	"github.com/claudioed/vendor-connectivity/internal/adapters/outbound/ids"
)

func TestTransactionIDsAreUniqueUUIDs(t *testing.T) {
	seen := map[string]bool{}
	for range 50 {
		id := ids.UUID{}.NewTransactionID()
		if len(id) != 36 || seen[id] {
			t.Fatalf("id %q", id)
		}
		seen[id] = true
	}
}

func TestRandomIsInUnitInterval(t *testing.T) {
	for range 1000 {
		if f := (ids.Random{}).Float64(); f < 0 || f >= 1 {
			t.Fatalf("Float64() = %v", f)
		}
	}
}
