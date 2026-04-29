package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Tests exist primarily so ci.yml's `go test ./...` has something to
// run — the contract is small, but the contract is also what M3's
// /containers/{name}/health observes, so we lock it down.

func TestHealthzReturns200OK(t *testing.T) {
	srv := httptest.NewServer(newHandler())
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "/healthz")
	if err != nil {
		t.Fatalf("GET /healthz: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if got := strings.TrimSpace(string(body)); got != "ok" {
		t.Fatalf("body = %q, want %q", got, "ok")
	}
}

func TestRootReturnsVersionBanner(t *testing.T) {
	// Force a known version so the assertion isn't tied to the
	// default. main_test.go runs against the same package, so
	// modifying the package-level var is fine here.
	prev := version
	version = "test-build"
	t.Cleanup(func() { version = prev })

	srv := httptest.NewServer(newHandler())
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "/")
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	want := "hello from claude-deployable test-build"
	if !strings.Contains(string(body), want) {
		t.Fatalf("body = %q, want substring %q", string(body), want)
	}
}

func TestUnknownPathReturns404(t *testing.T) {
	srv := httptest.NewServer(newHandler())
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "/nope")
	if err != nil {
		t.Fatalf("GET /nope: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (so unknown paths don't masquerade as /)", resp.StatusCode)
	}
}

func TestAddrFromEnv(t *testing.T) {
	t.Setenv("HELLO_ADDR", "")
	if got := addrFromEnv(); got != ":8080" {
		t.Errorf("default addr = %q, want %q", got, ":8080")
	}
	t.Setenv("HELLO_ADDR", "127.0.0.1:9090")
	if got := addrFromEnv(); got != "127.0.0.1:9090" {
		t.Errorf("override addr = %q, want %q", got, "127.0.0.1:9090")
	}
}
