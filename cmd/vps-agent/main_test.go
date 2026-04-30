package main

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Obee88/claude-deployable/internal/auth"
	"github.com/Obee88/claude-deployable/internal/dockerops"
)

// silentLogger returns a slog.Logger that writes to io.Discard, so
// tests don't dump JSON access logs into the test runner output.
func silentLogger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(io.Discard, nil))
}

func TestHealthz(t *testing.T) {
	mux := newMux(silentLogger(), auth.Tokens{Read: "r", Write: "w"}, nil)

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
	mux := newMux(silentLogger(), auth.Tokens{Read: "r", Write: "w"}, nil)

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

// containerRoutesMux returns a mux with the container surface
// wired against a manager that has an empty allowlist. With no
// services allowlisted, the mutating endpoints don't shell out to
// docker — we exercise auth gating and the allowlist guard
// without needing a docker daemon in CI.
func containerRoutesMux() (*http.ServeMux, auth.Tokens) {
	mgr := dockerops.NewManager(nil, "/dev/null")
	tokens := auth.Tokens{Read: "r", Write: "w"}
	return newMux(silentLogger(), tokens, mgr), tokens
}

func bearer(token string) http.Header {
	h := http.Header{}
	h.Set("Authorization", "Bearer "+token)
	return h
}

func TestContainers_NoAuthIs401(t *testing.T) {
	mux, _ := containerRoutesMux()
	req := httptest.NewRequest(http.MethodGet, "/containers", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("no-auth: got %d want 401", rec.Code)
	}
}

// Read endpoints accept the read token. Empty allowlist means
// List() returns []Container{} without invoking docker, so this
// passes in CI without a daemon.
func TestContainers_ListWithReadToken(t *testing.T) {
	mux, _ := containerRoutesMux()
	req := httptest.NewRequest(http.MethodGet, "/containers", nil)
	req.Header = bearer("r")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("/containers with read token: got %d want 200; body=%s", rec.Code, rec.Body.String())
	}
}

// The contract the M3.A.6 closeout asserts: a read-token caller
// hitting POST /containers/hello/restart gets 403, not 401 (so
// "wrong credential" is distinguishable from "no credential").
func TestContainers_RestartReadTokenIs403(t *testing.T) {
	mux, _ := containerRoutesMux()
	req := httptest.NewRequest(http.MethodPost, "/containers/hello/restart", nil)
	req.Header = bearer("r")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("restart with read token: got %d want 403", rec.Code)
	}
}

// With write token but the service is not in the allowlist,
// dockerops returns ErrNotAllowed and the handler maps it to
// 404 — leaks less info than 403 about whether the service
// exists somewhere on the host.
func TestContainers_RestartNotAllowlistedIs404(t *testing.T) {
	mux, _ := containerRoutesMux()
	req := httptest.NewRequest(http.MethodPost, "/containers/hello/restart", nil)
	req.Header = bearer("w")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("restart not-allowlisted: got %d want 404; body=%s", rec.Code, rec.Body.String())
	}
}
