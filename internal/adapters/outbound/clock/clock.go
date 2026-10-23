// Package clock is the system ports.Clock.
package clock

import "time"

// System reads the wall clock in UTC.
type System struct{}

// Now returns the current instant in UTC.
func (System) Now() time.Time { return time.Now().UTC() }
