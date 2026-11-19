package http

import (
	"bytes"
	"net/http"
)

// capture records the handler's response so it can be persisted and
// committed BEFORE anything reaches the client.
type capture struct {
	header http.Header
	code   int
	body   bytes.Buffer
	// class is the replay class of the problem the handler answered, set by
	// writeError (MarkReplay); replayUnset for a success and for an answer
	// that did not go through writeError.
	class replayClass
}

// MarkReplay records the class of the problem about to be written: the
// middleware decides from it whether the answer is stored or the key released.
func (c *capture) MarkReplay(class replayClass) { c.class = class }

func (c *capture) Header() http.Header { return c.header }

func (c *capture) WriteHeader(code int) {
	if c.code == 0 {
		c.code = code
	}
}

func (c *capture) Write(b []byte) (int, error) {
	if c.code == 0 {
		c.code = http.StatusOK
	}
	return c.body.Write(b)
}

// stored reports whether the answer is remembered under its key.
func (c *capture) stored() bool { return c.code < http.StatusInternalServerError }
