package main

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// silentLogger returns a slog.Logger that writes to io.Discard, so
// tests don't dump JSON access logs into the test runner output.
func silentLogger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(io.Discard, nil))
}

func TestHealthz(t *testing.T) {
	mux := newMux(silentLogger())

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d want %d", rec.Code, http.StatusOK)
	}
	body := rec.Body.String()
	if !strings.HasPrefix(body, "ok ") || !strings.HasSuffix(body, "\n") {
		t.Fatalf("body: got %q, want %q-style", body, "ok <version>\n")
	}
}

// TestHealthzMethodNotAllowed asserts the method-aware mux pattern
// (Go 1.22+) rejects POSTs to /healthz with 405. If this regresses,
// every read endpoint built on the same mux is also at risk.
func TestHealthzMethodNotAllowed(t *testing.T) {
	mux := newMux(silentLogger())

	req := httptest.NewRequest(http.MethodPost, "/healthz", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status: got %d want %d", rec.Code, http.StatusMethodNotAllowed)
	}
}

func TestAddrFromEnvDefault(t *testing.T) {
	t.Setenv("VPS_LISTEN_ADDR", "")
	if got, want := addrFromEnv(), "127.0.0.1:8080"; got != want {
		t.Fatalf("default: got %q want %q", got, want)
	}
}

func TestAddrFromEnvOverride(t *testing.T) {
	t.Setenv("VPS_LISTEN_ADDR", "0.0.0.0:9999")
	if got, want := addrFromEnv(), "0.0.0.0:9999"; got != want {
		t.Fatalf("override: got %q want %q", got, want)
	}
}
