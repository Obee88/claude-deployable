package auth

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// okHandler is the inner handler the middleware should reach when
// auth passes. We assert on its having been called via the 200
// response code.
var okHandler = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
})

func newReq(authz string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	if authz != "" {
		r.Header.Set("Authorization", authz)
	}
	return r
}

func TestRequireRead_ValidReadToken(t *testing.T) {
	tok := Tokens{Read: "r-secret", Write: "w-secret"}
	rec := httptest.NewRecorder()
	tok.RequireRead(okHandler).ServeHTTP(rec, newReq("Bearer r-secret"))
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200; body=%q", rec.Code, rec.Body.String())
	}
}

// RequireRead must accept the write token too — operators carrying
// only the write token should be able to read.
func TestRequireRead_WriteTokenAlsoOK(t *testing.T) {
	tok := Tokens{Read: "r-secret", Write: "w-secret"}
	rec := httptest.NewRecorder()
	tok.RequireRead(okHandler).ServeHTTP(rec, newReq("Bearer w-secret"))
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200", rec.Code)
	}
}

func TestRequireRead_MissingHeader(t *testing.T) {
	tok := Tokens{Read: "r-secret", Write: "w-secret"}
	rec := httptest.NewRecorder()
	tok.RequireRead(okHandler).ServeHTTP(rec, newReq(""))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status: got %d want 401", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"code":"unauthorized"`) {
		t.Fatalf("body did not include unauthorized code: %q", rec.Body.String())
	}
}

func TestRequireRead_WrongToken(t *testing.T) {
	tok := Tokens{Read: "r-secret", Write: "w-secret"}
	rec := httptest.NewRecorder()
	tok.RequireRead(okHandler).ServeHTTP(rec, newReq("Bearer wat"))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status: got %d want 401", rec.Code)
	}
}

// Header parser is strict: only "Bearer <token>" is recognized.
// "Token <token>" or anything else returns 401.
func TestRequireRead_NonBearerScheme(t *testing.T) {
	tok := Tokens{Read: "r-secret", Write: "w-secret"}
	rec := httptest.NewRecorder()
	tok.RequireRead(okHandler).ServeHTTP(rec, newReq("Token r-secret"))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status: got %d want 401", rec.Code)
	}
}

func TestRequireWrite_ValidWriteToken(t *testing.T) {
	tok := Tokens{Read: "r-secret", Write: "w-secret"}
	rec := httptest.NewRecorder()
	tok.RequireWrite(okHandler).ServeHTTP(rec, newReq("Bearer w-secret"))
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200", rec.Code)
	}
}

// RequireWrite must reject the read token with 403 — that's how
// a downstream operator distinguishes "wrong credential" from
// "no credential". Critical contract for the M3.A closeout test
// that asserts read-token-on-restart returns 403.
func TestRequireWrite_ReadTokenRejected(t *testing.T) {
	tok := Tokens{Read: "r-secret", Write: "w-secret"}
	rec := httptest.NewRecorder()
	tok.RequireWrite(okHandler).ServeHTTP(rec, newReq("Bearer r-secret"))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status: got %d want 403", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"code":"forbidden"`) {
		t.Fatalf("body did not include forbidden code: %q", rec.Body.String())
	}
}

func TestRequireWrite_MissingHeader(t *testing.T) {
	tok := Tokens{Read: "r-secret", Write: "w-secret"}
	rec := httptest.NewRecorder()
	tok.RequireWrite(okHandler).ServeHTTP(rec, newReq(""))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status: got %d want 401", rec.Code)
	}
}

// Empty configured tokens must fail closed — an unset env var
// must never trivially match an empty bearer string. This is the
// guard that prevents a misconfigured deployment from silently
// accepting all requests.
func TestRequireRead_EmptyConfigFailsClosed(t *testing.T) {
	tok := Tokens{Read: "", Write: ""}
	rec := httptest.NewRecorder()
	tok.RequireRead(okHandler).ServeHTTP(rec, newReq("Bearer "))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status: got %d want 401", rec.Code)
	}
	rec = httptest.NewRecorder()
	tok.RequireRead(okHandler).ServeHTTP(rec, newReq("Bearer something"))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status: got %d want 401 (anything)", rec.Code)
	}
}
