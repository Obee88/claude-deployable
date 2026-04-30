// Package main runs the claude-deployable VPS agent.
//
// The agent is a small bearer-gated HTTP service that lives on the VPS
// and gives the Cowork-sandboxed Claude agent a way to (a) observe
// and restart docker compose services running on the VPS and
// (b) read GitHub Actions run status and logs (api.github.com is
// unreachable from the sandbox, see ADR-0002 in PLAN.md). It is
// fronted by Caddy for TLS termination and listens only on
// 127.0.0.1.
//
// This file is the M3.A skeleton: only /healthz, no auth, no
// business logic. Subsequent tasks layer on bearer auth (M3.A.2),
// the /containers/* surface (M3.A.3), and finally /ci/* (M3.B).
// Keeping main() small and pulling the routing tree out into
// newMux() lets each follow-up task wrap it in middleware without
// rewriting startup.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Obee88/claude-deployable/internal/auth"
	"github.com/Obee88/claude-deployable/internal/httpx"
)

// version is overwritten via -ldflags "-X main.version=..." at build
// time (see deploy-vps-agent.yml). The default is intentionally a
// non-prod-looking string so a `go run` binary is obvious.
var version = "dev"

// addrFromEnv returns the listen address. Defaults to
// 127.0.0.1:8080 — Caddy fronts the public hostname and reverse-
// proxies here, so binding to localhost is the right production
// default. Override via VPS_LISTEN_ADDR for local development or
// integration tests.
func addrFromEnv() string {
	if v := os.Getenv("VPS_LISTEN_ADDR"); v != "" {
		return v
	}
	return "127.0.0.1:8080"
}

// newMux returns the routing tree with per-route auth applied.
// Cross-cutting middleware (request-id, access log) is applied by
// newHandler; keeping route-level auth here means M3.A.3's
// /containers/* additions slot in next to the existing ones
// without touching main().
//
// The tokens parameter isn't used yet (only /healthz is wired) but
// is threaded through now so M3.A.3's additions don't need to
// retro-fit a signature change.
func newMux(_ *slog.Logger, _ auth.Tokens) *http.ServeMux {
	mux := http.NewServeMux()

	// /healthz is intentionally unauthenticated — it's hit by the
	// install script's smoke test and by curl-on-laptop sanity
	// checks. It returns version so a deploy that did not actually
	// replace the binary becomes visible without needing a token.
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok " + version + "\n"))
	})

	return mux
}

// newHandler wraps the mux in cross-cutting middleware. Order
// matters: RequestIDMiddleware is outermost so AccessLogMiddleware
// can reach the ID via context; access log is next so any
// downstream auth-failure response is still logged.
func newHandler(logger *slog.Logger, mux *http.ServeMux) http.Handler {
	return httpx.RequestIDMiddleware(
		httpx.AccessLogMiddleware(logger)(mux),
	)
}

// newLogger builds the structured logger used by main and threaded
// into newMux. JSON to stdout means journald captures structured
// fields when running under systemd; locally it's still readable
// with `| jq`.
func newLogger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
}

func main() {
	logger := newLogger()
	slog.SetDefault(logger)

	// Refuse to start without both tokens. The middleware fails
	// closed on empty configured tokens, but a missing env var is
	// almost always a misconfigured deployment — fail loud on
	// startup instead of letting requests pile up rejected at
	// runtime.
	tokens := auth.FromEnv()
	if tokens.Read == "" || tokens.Write == "" {
		logger.Error("VPS_READ_TOKEN and VPS_WRITE_TOKEN must both be set")
		os.Exit(2)
	}

	addr := addrFromEnv()

	srv := &http.Server{
		Addr:              addr,
		Handler:           newHandler(logger, newMux(logger, tokens)),
		ReadHeaderTimeout: 5 * time.Second,
	}

	// Graceful shutdown so `systemctl restart vps-agent` and the
	// deploy workflow's binary swap don't drop in-flight requests.
	shutdownCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go func() {
		logger.Info("vps-agent listening", "addr", addr, "version", version)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("listen failed", "err", err.Error())
			os.Exit(1)
		}
	}()

	<-shutdownCtx.Done()
	logger.Info("shutdown signal received, draining for up to 10s")

	drain, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(drain); err != nil {
		logger.Error("shutdown failed", "err", err.Error())
	}
}
