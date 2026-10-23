package vendor

import "github.com/claudioed/vendor-connectivity/internal/domain/shared"

// Entity is the CloudEvents entity segment of the VendorAccount's events.
const Entity = "vendoraccount"

// VendorOnboarded is raised by Onboard. Its fields are the model's data list.
type VendorOnboarded struct {
	shared.Header
	VendorID         ID
	Name             string
	AckWindowMinutes int
}

// EventName returns "VendorOnboarded".
func (VendorOnboarded) EventName() string { return "VendorOnboarded" }

// Entity returns "vendoraccount".
func (VendorOnboarded) Entity() string { return Entity }

// SubjectID returns the vendorId.
func (e VendorOnboarded) SubjectID() string { return string(e.VendorID) }

// VendorSuspended is raised by Suspend (Gap G30).
type VendorSuspended struct {
	shared.Header
	VendorID ID
	Reason   string
}

// EventName returns "VendorSuspended".
func (VendorSuspended) EventName() string { return "VendorSuspended" }

// Entity returns "vendoraccount".
func (VendorSuspended) Entity() string { return Entity }

// SubjectID returns the vendorId.
func (e VendorSuspended) SubjectID() string { return string(e.VendorID) }

// VendorReinstated is raised by Reinstate (Gap G30).
type VendorReinstated struct {
	shared.Header
	VendorID ID
}

// EventName returns "VendorReinstated".
func (VendorReinstated) EventName() string { return "VendorReinstated" }

// Entity returns "vendoraccount".
func (VendorReinstated) Entity() string { return Entity }

// SubjectID returns the vendorId.
func (e VendorReinstated) SubjectID() string { return string(e.VendorID) }

var (
	_ shared.Event = VendorOnboarded{}
	_ shared.Event = VendorSuspended{}
	_ shared.Event = VendorReinstated{}
)
