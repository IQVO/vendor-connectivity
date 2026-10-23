package submission

import "github.com/claudioed/vendor-connectivity/internal/domain/shared"

// Entity is the CloudEvents entity segment of the Submission's events.
const Entity = "submission"

// SubmissionProcessed is raised by Complete. Its fields are the model's data list.
type SubmissionProcessed struct {
	shared.Header
	TransactionID ID
	VendorID      string
	Kind          Kind
	Status        Status
	ErrorCount    int
}

// EventName returns "SubmissionProcessed".
func (SubmissionProcessed) EventName() string { return "SubmissionProcessed" }

// Entity returns "submission".
func (SubmissionProcessed) Entity() string { return Entity }

// SubjectID returns the transactionId.
func (e SubmissionProcessed) SubjectID() string { return string(e.TransactionID) }

var _ shared.Event = SubmissionProcessed{}
