package submission

import (
	"errors"
	"strings"
	"unicode"

	"github.com/claudioed/vendor-connectivity/internal/domain/shared"
)

// Generic words kept when a neighbour's refusal carries none of its own.
const (
	genericRefusalCode    = "ErrRefused"
	genericRefusalMessage = "the neighbour refused the request"
)

// RecordRefusal counts one processing pass that ended without an outcome because the
// neighbour refused the call with a refusal it releases under the Idempotency-Key
// (replay class RETRYABLE, ADR 0016): the same submission is tried again with the
// same key. The refusal is kept as the reason the submission is still PROCESSING;
// the next refusal replaces it. The neighbour's text is cleaned and clipped to the
// bounds of a SubmissionError. It returns the attempts so far and is refused once
// the submission is processed or at MaxAttempts.
func (s *Submission) RecordRefusal(e Error) (int, error) {
	if s.status != Processing {
		return s.attempts, ErrSubmissionAlreadyProcessed
	}
	if s.attempts >= MaxAttempts {
		return s.attempts, shared.Invalid("attempts are capped at %d", MaxAttempts)
	}
	s.lastRefusal = Error{
		Code:    boundedText(e.Code, MaxCode, genericRefusalCode),
		Message: boundedText(e.Message, MaxMessage, genericRefusalMessage),
		Path:    boundedText(e.Path, MaxPath, ""),
	}
	s.attempts++
	s.version++
	return s.attempts, nil
}

// LastRefusal returns the refusal that keeps the submission PROCESSING, if any.
// A processed submission has none: its outcome carries the errors.
func (s *Submission) LastRefusal() (Error, bool) {
	return s.lastRefusal, s.lastRefusal != Error{}
}

func checkLastRefusal(sn Snapshot) error {
	if sn.LastRefusal == (Error{}) {
		return nil
	}
	if sn.Status != Processing {
		return errors.New("only a PROCESSING submission has a pending refusal")
	}
	return checkError(sn.LastRefusal)
}

// boundedText makes the neighbour's text fit ValidTextN: control characters become
// spaces, the ends are trimmed and it is clipped to limit characters. Nothing left
// gives fallback.
func boundedText(text string, limit int, fallback string) string {
	text = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, strings.ToValidUTF8(text, " "))
	runes := []rune(strings.TrimSpace(text))
	text = strings.TrimSpace(string(runes[:min(len(runes), limit)]))
	if text == "" {
		return fallback
	}
	return text
}
