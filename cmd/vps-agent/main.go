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

// newMux returns the routing tree. Kept as a package-local helper
// so M3.A.2 can wrap it in middleware (auth, request-id, access
// log) and M3.A.3 / M3.B can register more routes without
// disturbing main().
//
// The logger is accepted explicitly rather than reached for via the
// slog.Default() singleton so handlers that need structured logging
// can be added with their dependencies threaded through normally.
func newMux(_ *slog.Logger) *http.ServeMux {
	mux := http.NewServeMux()

	// /healthz is intentionally unauthenticated — it's hit by the
	// install script's smoke test and by curl-on-laptop sanity
	// checks. It returns version so a deploy that did not actually
	// replace the binary becomes visible without needing a write
	// token.
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok " + version + "\n"))
	})

	return mux
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

	addr := addrFromEnv()

	srv := &http.Server{
		Addr:              addr,
		Handler:           newMux(logger),
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
