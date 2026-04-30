package main

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/Obee88/claude-deployable/internal/auth"
	"github.com/Obee88/claude-deployable/internal/dockerops"
	"github.com/Obee88/claude-deployable/internal/httpx"
)

// Default values for the GET /containers/{name}/logs query
// parameters. PLAN.md's API surface table specifies these as the
// canonical defaults; mirroring them here means a bare
// `/containers/hello/logs` already returns something useful.
const (
	defaultLogsSince    = "10m"
	defaultLogsTail     = 500
	defaultLogsMaxBytes = 65536
)

// registerContainerRoutes wires the four /containers/* endpoints
// onto the mux with appropriate auth gating. Read endpoints accept
// either token; the restart endpoint requires the write token (a
// read-token attempt returns 403 — that contract is explicitly
// asserted in the M3.A.6 closeout).
func registerContainerRoutes(mux *http.ServeMux, logger *slog.Logger, tokens auth.Tokens, mgr *dockerops.Manager) {
	mux.Handle("GET /containers", tokens.RequireRead(handleListContainers(logger, mgr)))
	mux.Handle("GET /containers/{name}/logs", tokens.RequireRead(handleContainerLogs(logger, mgr)))
	mux.Handle("GET /containers/{name}/health", tokens.RequireRead(handleContainerHealth(logger, mgr)))
	mux.Handle("POST /containers/{name}/restart", tokens.RequireWrite(handleContainerRestart(logger, mgr)))
}

func handleListContainers(logger *slog.Logger, mgr *dockerops.Manager) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		list, err := mgr.List(r.Context())
		if err != nil {
			logger.Error("list containers", "err", err.Error(), "request_id", httpx.RequestIDFromContext(r.Context()))
			httpx.WriteError(w, http.StatusInternalServerError, "internal", "failed to list containers")
			return
		}
		writeJSON(w, http.StatusOK, list)
	})
}

func handleContainerLogs(logger *slog.Logger, mgr *dockerops.Manager) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("name")
		q := r.URL.Query()
		since := q.Get("since")
		if since == "" {
			since = defaultLogsSince
		}
		tail := defaultLogsTail
		if v := q.Get("tail"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				tail = n
			}
		}
		maxBytes := defaultLogsMaxBytes
		if v := q.Get("max_bytes"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				maxBytes = n
			}
		}
		result, err := mgr.Logs(r.Context(), name, since, tail, maxBytes)
		if err != nil {
			mapDockeropsError(w, r, logger, err)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		if result.Truncated {
			// Surface truncation in a header too so curl can see
			// it without reading the body. Body still carries the
			// "[...truncated N bytes]" marker as the contract.
			w.Header().Set("X-Logs-Truncated", "1")
			w.Header().Set("X-Logs-Original-Bytes", strconv.Itoa(result.OriginalBytes))
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(result.Body)
	})
}

func handleContainerHealth(logger *slog.Logger, mgr *dockerops.Manager) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("name")
		h, err := mgr.HealthOf(r.Context(), name)
		if err != nil {
			mapDockeropsError(w, r, logger, err)
			return
		}
		writeJSON(w, http.StatusOK, h)
	})
}

func handleContainerRestart(logger *slog.Logger, mgr *dockerops.Manager) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("name")
		res, err := mgr.Restart(r.Context(), name)
		if err != nil {
			mapDockeropsError(w, r, logger, err)
			return
		}
		writeJSON(w, http.StatusOK, res)
	})
}

// mapDockeropsError translates dockerops sentinel errors to HTTP
// statuses + JSON envelopes. ErrNotAllowed is mapped to 404
// (rather than 403) so the response body doesn't leak the
// allowlist's contents to anyone with a valid bearer token.
// Anything else is logged and returned as a generic 500.
func mapDockeropsError(w http.ResponseWriter, r *http.Request, logger *slog.Logger, err error) {
	switch {
	case errors.Is(err, dockerops.ErrNotAllowed), errors.Is(err, dockerops.ErrNotFound):
		httpx.WriteError(w, http.StatusNotFound, "not_found", "no such service")
	default:
		logger.Error("dockerops",
			"err", err.Error(),
			"request_id", httpx.RequestIDFromContext(r.Context()),
		)
		httpx.WriteError(w, http.StatusInternalServerError, "internal", "internal error")
	}
}

// writeJSON is the canonical JSON-OK helper. Mirrors WriteError's
// content-type discipline so success and error responses look
// shape-symmetric to the parser on the agent side.
func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
