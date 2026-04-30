// Package httpx contains shared HTTP plumbing for cmd/vps-agent —
// JSON error envelope, request-id middleware, structured access
// log middleware. Every non-2xx response from vps-agent should be
// emitted via WriteError so the sandbox-side parser only has to
// know one shape.
//
// Kept under internal/ because the surface is shaped specifically
// for the agent's JSON-error / structured-log conventions and
// shouldn't be confused for a general HTTP utility kit.
package httpx

import (
	"encoding/json"
	"net/http"
)

// ErrorBody is the JSON shape returned by WriteError. The shape is
// `{"error":{"code":"...","message":"..."}}` so the sandbox-side
// agent can write `.error.code` parsing once and reuse it across
// every endpoint.
type ErrorBody struct {
	Error ErrorDetail `json:"error"`
}

// ErrorDetail is the inner object of ErrorBody. Code is a stable
// machine-readable string (e.g. "unauthorized", "not_found",
// "service_not_allowlisted"). Message is human-readable but should
// avoid leaking internal details (paths, stack traces, env vars).
type ErrorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// WriteError writes a JSON error envelope with the given HTTP
// status, machine code, and human message. It sets Content-Type
// before WriteHeader so callers don't have to remember to.
func WriteError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(ErrorBody{
		Error: ErrorDetail{Code: code, Message: message},
	})
}
