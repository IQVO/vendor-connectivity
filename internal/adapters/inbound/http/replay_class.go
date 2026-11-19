package http

// replayClass is what the Idempotency-Key middleware does with the answer of
// a problem type (ADR 0015). It is declared on the entry of the problem
// catalogue (errors.go); the middleware reads it and holds no list of slugs.
// A catalogue entry without a class fails
// TestEveryCatalogueEntryDeclaresAReplayClass: there is no default.
type replayClass uint8

const (
	// replayUnset is the zero value: no class. No catalogue entry may have it;
	// an error the catalogue does not know (an unmapped 5xx) has it.
	replayUnset replayClass = iota
	// replayFinal: the answer is a function of the request alone. The refusal
	// is stored under the key and replayed.
	replayFinal
	// replayOutcome: the command committed state and then answered an error
	// that must be remembered. The stored answer and the handler's writes
	// commit in the one transaction of the request. No entry is an OUTCOME
	// today (ADR 0015 lists why); the class exists for the first one.
	replayOutcome
	// replayRetryable: nothing was committed and the answer depends on state
	// that can change. Nothing is stored, the key row rolls back with the
	// request and the same request under the same key executes again.
	replayRetryable
)

func (c replayClass) String() string {
	switch c {
	case replayFinal:
		return "FINAL"
	case replayOutcome:
		return "OUTCOME"
	case replayRetryable:
		return "RETRYABLE"
	}
	return "UNSET"
}

// declared reports whether c is one of the three classes.
func (c replayClass) declared() bool {
	return c == replayFinal || c == replayOutcome || c == replayRetryable
}

// replayMarker is implemented by the middleware's capturing writer: writeError
// tells it the class of the problem it is about to write.
type replayMarker interface{ MarkReplay(replayClass) }
