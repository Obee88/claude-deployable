package httpx

import (
	"log/slog"
	"net/http"
	"time"
)

// statusRecorder wraps http.ResponseWriter to capture the status
// code and bytes written so the access log can report both. We
// don't care about hijacker / flusher / pusher interfaces because
// vps-agent doesn't use streaming responses; if M3.B's log
// streaming changes that, this needs a revisit.
type statusRecorder struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (s *statusRecorder) WriteHeader(code int) {
	if s.status == 0 {
		s.status = code
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	if s.status == 0 {
		s.status = http.StatusOK
	}
	n, err := s.ResponseWriter.Write(b)
	s.bytes += n
	return n, err
}

// AccessLogMiddleware emits one slog INFO line per request with
// method, path, status, duration_ms, bytes, request_id, and
// remote_addr. Pair with RequestIDMiddleware (wrapped *outside*
// this one) so request_id is populated.
//
// The middleware factory takes a logger so per-request fields
// inherit any handler-level attrs the caller has already attached
// (`logger.With(...)`). vps-agent currently passes the root
// logger — adjust if we ever want per-route attrs.
func AccessLogMiddleware(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			rec := &statusRecorder{ResponseWriter: w}
			next.ServeHTTP(rec, r)
			logger.Info("http",
				"method", r.Method,
				"path", r.URL.Path,
				"status", rec.status,
				"duration_ms", time.Since(start).Milliseconds(),
				"bytes", rec.bytes,
				"request_id", RequestIDFromContext(r.Context()),
				"remote_addr", r.RemoteAddr,
			)
		})
	}
}
