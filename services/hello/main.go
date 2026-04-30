// Package main runs a tiny HTTP server used as the deploy target for
// claude-deployable's M2 milestone. It exists to give GitHub Actions
// something to build and ship to the VPS, and to give M3's
// /containers/{name}/health endpoint something whose health it can
// inspect.
//
// The contract is intentionally small and stable:
//
//   GET /         -> 200, "hello from claude-deployable <version>\n"
//   GET /healthz  -> 200, "ok\n"
//   anything else -> 404
//
// `version` is set via -ldflags "-X main.version=..." at build time
// (see services/hello/Dockerfile). Default is "dev" so `go run` works
// without flags.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

// version is overwritten by -ldflags at build time. Keep the default
// short and obviously non-prod so nobody mistakes a dev binary for a
// shipped one.
var version = "dev"

// addrFromEnv returns the listen address, defaulting to :8080 for
// container-friendly defaults. M3 health checks assume :8080 inside
// the container.
func addrFromEnv() string {
	if v := os.Getenv("HELLO_ADDR"); v != "" {
		return v
	}
	return ":8080"
}

// newHandler wires up the routes. Exposed (lowercase, package-local)
// so tests can drive it directly without binding a port.
func newHandler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintln(w, "ok")
	})

	// "/" matches everything that didn't match a more specific
	// pattern; gate it to the exact root path so unknown URLs return
	// a real 404 instead of the friendly version banner.
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintf(w, "hello from claude-deployable %s\n", version)
	})

	return mux
}

func main() {
	addr := addrFromEnv()

	srv := &http.Server{
		Addr:              addr,
		Handler:           newHandler(),
		ReadHeaderTimeout: 5 * time.Second,
	}

	// Graceful shutdown so `docker compose up -d` rolling-restarts
	// don't leak in-flight requests.
	shutdownCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go func() {
		log.Printf("hello %s listening on %s", version, addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("listen: %v", err)
		}
	}()

	<-shutdownCtx.Done()
	log.Printf("shutdown signal received, draining for up to 10s")

	drain, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(drain); err != nil {
		log.Printf("shutdown: %v", err)
	}
}
