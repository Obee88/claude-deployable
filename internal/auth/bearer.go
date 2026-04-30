// Package auth provides bearer-token middleware for cmd/vps-agent.
//
// Two tokens, both held in /home/deploy/etc/vps-agent.env on the
// VPS: VPS_READ_TOKEN gates everything, VPS_WRITE_TOKEN additionally
// gates POST /containers/{name}/restart. Both are compared with
// crypto/subtle.ConstantTimeCompare; an empty configured token
// always fails closed (so an unset env var can never be matched by
// an empty Authorization header).
//
// vps-agent's main() is responsible for refusing to start when
// either token is empty — see the FromEnv comment.
package auth

import (
	"crypto/subtle"
	"net/http"
	"os"
	"strings"

	"github.com/Obee88/claude-deployable/internal/httpx"
)

// Tokens holds the two bearer tokens compared at request time.
// Tokens are plain strings on purpose: nothing in the agent stores
// them encrypted, and the env file they come from is mode 0600.
type Tokens struct {
	Read  string
	Write string
}

// FromEnv constructs Tokens from VPS_READ_TOKEN / VPS_WRITE_TOKEN.
// Empty values are returned as-is; the caller must check and refuse
// to start, since the equalConst helper fails closed but a missing
// token still indicates a misconfigured deployment.
func FromEnv() Tokens {
	return Tokens{
		Read:  os.Getenv("VPS_READ_TOKEN"),
		Write: os.Getenv("VPS_WRITE_TOKEN"),
	}
}

// RequireRead returns a middleware that gates the wrapped handler
// on the request carrying a bearer header matching either the read
// or the write token. Accepting both means the operator can carry
// a single credential bundle for the curl smoke tests; only the
// write-only routes (RequireWrite) reject the read token.
func (t Tokens) RequireRead(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := bearerFromHeader(r)
		if got == "" {
			httpx.WriteError(w, http.StatusUnauthorized, "unauthorized", "missing bearer token")
			return
		}
		if !equalConst(got, t.Read) && !equalConst(got, t.Write) {
			httpx.WriteError(w, http.StatusUnauthorized, "unauthorized", "invalid bearer token")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// RequireWrite gates strictly on the write token. The read token
// is rejected with 403 so the caller can distinguish "wrong
// credential" from "no credential" without leaking which.
func (t Tokens) RequireWrite(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := bearerFromHeader(r)
		if got == "" {
			httpx.WriteError(w, http.StatusUnauthorized, "unauthorized", "missing bearer token")
			return
		}
		if !equalConst(got, t.Write) {
			httpx.WriteError(w, http.StatusForbidden, "forbidden", "write token required")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// bearerFromHeader returns the token portion of a `Bearer <token>`
// Authorization header, or "" if absent / malformed. We don't
// support `Token <token>` or any other scheme — the agent only
// ever talks bearer.
func bearerFromHeader(r *http.Request) string {
	h := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if !strings.HasPrefix(h, prefix) {
		return ""
	}
	return strings.TrimSpace(h[len(prefix):])
}

// equalConst is a constant-time comparison that fails closed on
// empty want — i.e. an unset env var never matches anything,
// including an empty bearer string. ConstantTimeCompare returns 0
// for length-mismatched inputs as well, but the explicit empty
// guard makes the behavior obvious to a reader.
func equalConst(got, want string) bool {
	if want == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}
