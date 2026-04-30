package httpx

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
)

// requestIDHeader is the canonical X-Request-ID header. The
// middleware accepts an inbound value (so a caller can correlate
// across systems) or generates one if absent.
const requestIDHeader = "X-Request-ID"

// ctxKey is unexported so callers can't accidentally collide with
// another package's context keys; reach for RequestIDFromContext
// to extract.
type ctxKey int

const requestIDKey ctxKey = iota

// RequestIDMiddleware reads X-Request-ID from the incoming request
// or generates a fresh 16-byte hex ID, stashes it on the context,
// and echoes it on the response header so callers can correlate.
//
// Wrap this *outermost* (or close to it) so subsequent middleware
// — in particular the access log — can reach it via
// RequestIDFromContext.
func RequestIDMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get(requestIDHeader)
		if id == "" {
			id = newRequestID()
		}
		w.Header().Set(requestIDHeader, id)
		ctx := context.WithValue(r.Context(), requestIDKey, id)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// RequestIDFromContext returns the request ID stashed by
// RequestIDMiddleware. Returns "" for handlers wired without the
// middleware, which is the right behavior for tests that don't
// care.
func RequestIDFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(requestIDKey).(string); ok {
		return v
	}
	return ""
}

// newRequestID returns a 32-character hex string from 16 bytes of
// crypto/rand. We don't pull in google/uuid for this — a hex blob
// is fine for log correlation, and the dep would be the first
// non-stdlib import in the module.
func newRequestID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// Falling back to a fixed string is acceptable: the
		// request ID is a correlation aid, not a security control.
		// crypto/rand failure is exotic enough that we'd rather
		// not crash the whole request over it.
		return "norand-0000000000000000000000000000"
	}
	return hex.EncodeToString(b[:])
}
