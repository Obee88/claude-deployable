package mcpx

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeEnv(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestLoadConfigHappyPath(t *testing.T) {
	dir := t.TempDir()
	repoA := filepath.Join(dir, "repoA")
	repoB := filepath.Join(dir, "repoB")
	for _, p := range []string{repoA, repoB} {
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	envPath := filepath.Join(dir, ".env")
	writeEnv(t, envPath, `# bridge env (test)
GIT_AUTHOR_NAME=claude-agent
GIT_AUTHOR_EMAIL="claude-agent@users.noreply.github.com"
GH_PAT=ghp_secret
CLAUDE_DEPLOYABLE_ALLOWLIST=`+repoA+","+repoB+`
`)

	cfg, err := LoadConfig(envPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.IdentityName != "claude-agent" {
		t.Errorf("identity name = %q", cfg.IdentityName)
	}
	if cfg.IdentityEmail != "claude-agent@users.noreply.github.com" {
		t.Errorf("identity email = %q", cfg.IdentityEmail)
	}
	if cfg.GHPAT != "ghp_secret" {
		t.Errorf("GHPAT = %q", cfg.GHPAT)
	}
	if len(cfg.Allowlist) != 2 {
		t.Fatalf("allowlist len = %d, want 2: %v", len(cfg.Allowlist), cfg.Allowlist)
	}

	// Summary must not include the PAT.
	body, _ := json.Marshal(cfg.Summary())
	if strings.Contains(string(body), "ghp_secret") {
		t.Errorf("Summary leaks GH_PAT: %s", body)
	}
	if !strings.Contains(string(body), `"gh_pat_present":true`) {
		t.Errorf("Summary missing gh_pat_present flag: %s", body)
	}
}

func TestLoadConfigRejectsEmptyAllowlist(t *testing.T) {
	dir := t.TempDir()
	envPath := filepath.Join(dir, ".env")
	writeEnv(t, envPath, `GIT_AUTHOR_NAME=x
GIT_AUTHOR_EMAIL=x@y
CLAUDE_DEPLOYABLE_ALLOWLIST=
`)
	if _, err := LoadConfig(envPath); err == nil {
		t.Error("expected error on empty allowlist")
	}
}

func TestLoadConfigRejectsMissingIdentity(t *testing.T) {
	dir := t.TempDir()
	repo := filepath.Join(dir, "r")
	os.MkdirAll(repo, 0o755)
	envPath := filepath.Join(dir, ".env")
	writeEnv(t, envPath, `CLAUDE_DEPLOYABLE_ALLOWLIST=`+repo+`
`)
	if _, err := LoadConfig(envPath); err == nil {
		t.Error("expected error on missing identity")
	}
}

func TestResolveAllowed(t *testing.T) {
	dir := t.TempDir()
	repo := filepath.Join(dir, "r")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := Config{
		Allowlist:     []string{repo},
		IdentityName:  "x",
		IdentityEmail: "x@y",
	}

	abs, e := cfg.ResolveAllowed(repo)
	if e != nil {
		t.Fatalf("ResolveAllowed: %+v", e)
	}
	if abs != repo {
		t.Errorf("abs = %q, want %q", abs, repo)
	}

	// Outside the allowlist → CodeOutsideAllowlist.
	other := t.TempDir()
	_, e = cfg.ResolveAllowed(other)
	if e == nil || e.Code != CodeOutsideAllowlist {
		t.Errorf("expected CodeOutsideAllowlist, got %+v", e)
	}

	// Empty arg → CodeInvalidArgs.
	_, e = cfg.ResolveAllowed("")
	if e == nil || e.Code != CodeInvalidArgs {
		t.Errorf("expected CodeInvalidArgs on empty arg, got %+v", e)
	}
}

func TestResolveAllowedFollowsSymlinks(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "real")
	link := filepath.Join(dir, "link")
	if err := os.MkdirAll(real, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, link); err != nil {
		t.Skip("symlinks not supported on this platform")
	}
	cfg := Config{
		Allowlist:     []string{real},
		IdentityName:  "x",
		IdentityEmail: "x@y",
	}
	abs, e := cfg.ResolveAllowed(link)
	if e != nil {
		t.Fatalf("ResolveAllowed via symlink: %+v", e)
	}
	if abs != real {
		t.Errorf("abs = %q, want %q (resolved through symlink)", abs, real)
	}
}
