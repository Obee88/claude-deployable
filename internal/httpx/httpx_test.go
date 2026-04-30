package httpx

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWriteError_Shape(t *testing.T) {
	rec := httptest.NewRecorder()
	WriteError(rec, http.StatusUnauthorized, "unauthorized", "missing bearer token")

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status: got %d want %d", rec.Code, http.StatusUnauthorized)
	}
	if got, want := rec.Header().Get("Content-Type"), "application/json; charset=utf-8"; got != want {
		t.Fatalf("content-type: got %q want %q", got, want)
	}

	var body ErrorBody
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body.Error.Code != "unauthorized" {
		t.Fatalf("code: got %q want %q", body.Error.Code, "unauthorized")
	}
	if body.Error.Message != "missing bearer token" {
		t.Fatalf("message: got %q want %q", body.Error.Message, "missing bearer token")
	}
}

func TestRequestIDMiddleware_GeneratesWhenMissing(t *testing.T) {
	var captured string
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured = RequestIDFromContext(r.Context())
	})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	RequestIDMiddleware(inner).ServeHTTP(rec, req)

	if len(captured) != 32 {
		t.Fatalf("generated id length: got %d want 32 (%q)", len(captured), captured)
	}
	if got := rec.Header().Get("X-Request-ID"); got != captured {
		t.Fatalf("response header: got %q want %q", got, captured)
	}
}

// When the caller supplies X-Request-ID, the middleware must echo
// the same value (used for cross-system correlation in the demo
// curl scripts).
func TestRequestIDMiddleware_EchoesInbound(t *testing.T) {
	const inbound = "req-from-laptop-42"
	var captured string
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured = RequestIDFromContext(r.Context())
	})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Request-ID", inbound)
	rec := httptest.NewRecorder()
	RequestIDMiddleware(inner).ServeHTTP(rec, req)

	if captured != inbound {
		t.Fatalf("captured: got %q want %q", captured, inbound)
	}
	if got := rec.Header().Get("X-Request-ID"); got != inbound {
		t.Fatalf("response header: got %q want %q", got, inbound)
	}
}

func TestRequestIDFromContext_Empty(t *testing.T) {
	if got := RequestIDFromContext(context.Background()); got != "" {
		t.Fatalf("empty ctx: got %q want \"\"", got)
	}
}

// AccessLogMiddleware should emit a single line with the response
// status. Status capture has historically been a footgun (the
// recorder must wrap WriteHeader and Write); regression-test it
// here so the M3.A.6 closeout doesn't surface a logging gap.
func TestAccessLogMiddleware_CapturesStatus(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))

	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
		_, _ = w.Write([]byte("brewing"))
	})
	mw := AccessLogMiddleware(logger)

	rec := httptest.NewRecorder()
	mw(inner).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/teapot", nil))

	out := buf.String()
	if !strings.Contains(out, `"status":418`) {
		t.Fatalf("expected status=418 in log line, got: %s", out)
	}
	if !strings.Contains(out, `"path":"/teapot"`) {
		t.Fatalf("expected path=/teapot in log line, got: %s", out)
	}
}

// Status defaults to 200 when the handler writes a body without
// calling WriteHeader explicitly — matches net/http's behavior.
func TestAccessLogMiddleware_DefaultStatus200(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "ok")
	})

	rec := httptest.NewRecorder()
	AccessLogMiddleware(logger)(inner).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if !strings.Contains(buf.String(), `"status":200`) {
		t.Fatalf("expected default status=200, got: %s", buf.String())
	}
}
