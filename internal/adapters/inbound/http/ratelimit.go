package http

import (
	"math"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// Limiter is the token bucket behind RATE_LIMIT_RPS (assumption A21): one
// bucket per process over /vendor/*, burst = max(1, rps). rps <= 0 disables it.
type Limiter struct {
	rps   float64
	burst float64
	now   func() time.Time

	mu     sync.Mutex
	tokens float64
	last   time.Time
}

// NewLimiter builds a limiter allowing rps requests a second; the bucket
// starts full.
func NewLimiter(rps float64) *Limiter {
	l := &Limiter{rps: rps, burst: math.Max(1, rps), now: time.Now}
	l.tokens, l.last = l.burst, l.now()
	return l
}

// Allow takes one token. When there is none it reports how long until one is
// available.
func (l *Limiter) Allow() (ok bool, retryAfter time.Duration) {
	if l == nil || l.rps <= 0 {
		return true, 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	l.tokens = math.Min(l.burst, l.tokens+now.Sub(l.last).Seconds()*l.rps)
	l.last = now
	if l.tokens >= 1 {
		l.tokens--
		return true, 0
	}
	return false, time.Duration((1 - l.tokens) / l.rps * float64(time.Second))
}

// rateLimit answers 429 rate-limited with Retry-After (whole seconds, at least
// 1) once the limiter has no token.
func rateLimit(l *Limiter) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if ok, wait := l.Allow(); !ok {
				secs := int(math.Ceil(wait.Seconds()))
				w.Header().Set("Retry-After", strconv.Itoa(max(1, secs)))
				writeProblem(w, r, rateLimited, "too many requests; retry after the delay in Retry-After")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
