// Package ids holds the production ports.IDGenerator and ports.Random.
package ids

import (
	"math/rand/v2"

	"github.com/google/uuid"
)

// UUID mints transaction ids as random UUID v4 strings.
type UUID struct{}

// NewTransactionID returns a fresh transaction id.
func (UUID) NewTransactionID() string { return uuid.NewString() }

// Random is the process-wide pseudo-random source of the simulation knobs.
type Random struct{}

// Float64 returns a value in [0, 1).
func (Random) Float64() float64 { return rand.Float64() } //nolint:gosec // simulation dice, not a secret
