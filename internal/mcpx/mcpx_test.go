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
	// In production LoadConfig resolves symlinks before storing
	// allowlist entries.  Mirror that here — on macOS, t.TempDir()
	// returns /var/... which symlinks to /private/var/..., and
	// ResolveAllowed always returns the resolved form.
	repoResolved, err := filepath.EvalSymlinks(repo)
	if err != nil {
		t.Fatal(err)
	}
	cfg := Config{
		Allowlist:     []string{repoResolved},
		IdentityName:  "x",
		IdentityEmail: "x@y",
	}

	abs, e := cfg.ResolveAllowed(repo)
	if e != nil {
		t.Fatalf("ResolveAllowed: %+v", e)
	}
	if abs != repoResolved {
		t.Errorf("abs = %q, want %q", abs, repoResolved)
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

// TestDiscoverEnvPathFindsRepoEnv simulates the common case: the user
// runs the bridge from somewhere inside a git repo whose root contains
// a .env.  Discovery should walk up to the .git/ boundary and return
// that .env regardless of which subdirectory we started from.
func TestDiscoverEnvPathFindsRepoEnv(t *testing.T) {
	t.Setenv("CLAUDE_DEPLOYABLE_ENV", "")

	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	envPath := filepath.Join(repo, ".env")
	writeEnv(t, envPath, `GIT_AUTHOR_NAME=claude-agent
GIT_AUTHOR_EMAIL=claude-agent@users.noreply.github.com
GH_PAT=tok
CLAUDE_DEPLOYABLE_ALLOWLIST=`+repo+`
`)
	// Drop into a nested subdir; discovery should still find the
	// repo-root .env via the walk-up.
	sub := filepath.Join(repo, "a", "b", "c")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(sub)

	got := discoverEnvPath()
	// EvalSymlinks resolves macOS /var → /private/var; compare resolved.
	wantResolved, _ := filepath.EvalSymlinks(envPath)
	gotResolved, _ := filepath.EvalSymlinks(got)
	if gotResolved != wantResolved {
		t.Errorf("discoverEnvPath = %q, want %q", got, envPath)
	}

	// And LoadConfig with no explicit path goes through the same
	// discovery, so this end-to-end works too.
	if _, err := LoadConfig(""); err != nil {
		t.Errorf("LoadConfig(\"\") with discovered .env: %v", err)
	}
}

// TestDiscoverEnvPathRespectsExplicitOverride confirms that
// $CLAUDE_DEPLOYABLE_ENV beats the walk-up.  Useful when a forker wants
// to keep secrets outside the working tree.
func TestDiscoverEnvPathRespectsExplicitOverride(t *testing.T) {
	repo := t.TempDir()
	os.MkdirAll(filepath.Join(repo, ".git"), 0o755)
	repoEnv := filepath.Join(repo, ".env")
	writeEnv(t, repoEnv, "GH_PAT=should_not_be_used\n")

	overrideDir := t.TempDir()
	overridePath := filepath.Join(overrideDir, "elsewhere.env")
	writeEnv(t, overridePath, "GH_PAT=override_wins\n")

	t.Chdir(repo)
	t.Setenv("CLAUDE_DEPLOYABLE_ENV", overridePath)

	if got := discoverEnvPath(); got != overridePath {
		t.Errorf("discoverEnvPath = %q, want %q (env override should win over walk-up)", got, overridePath)
	}
}

// TestDiscoverEnvPathStopsAtGitBoundary protects against the footgun of
// picking up a stray .env in $HOME or some unrelated parent dir.  If
// there's no enclosing git repo, the walk-up MUST NOT return a .env
// found higher up the tree.
func TestDiscoverEnvPathStopsAtGitBoundary(t *testing.T) {
	t.Setenv("CLAUDE_DEPLOYABLE_ENV", "")

	// Simulated layout:
	//   <root>/.env             ← stray, unrelated
	//   <root>/sub/...          ← cwd, no .git/ anywhere upward
	root := t.TempDir()
	writeEnv(t, filepath.Join(root, ".env"), "GH_PAT=stray\n")
	sub := filepath.Join(root, "sub", "deeper")
	os.MkdirAll(sub, 0o755)
	t.Chdir(sub)

	got := discoverEnvPath()
	// Without .git/, the walk-up must not return root/.env.  The
	// allowed fallbacks are exe-dir or $HOME/.claude-deployable/.env;
	// either way, it must not be the stray file at root.
	if strayResolved, _ := filepath.EvalSymlinks(filepath.Join(root, ".env")); strayResolved != "" {
		gotResolved, _ := filepath.EvalSymlinks(got)
		if gotResolved == strayResolved {
			t.Errorf("walk-up returned stray .env at %q with no .git/ boundary", got)
		}
	}
}

// TestFindRepoRoot exercises the boundary: .git/ present, .git/ as a
// file (worktree shape), and no .git/ at all.
func TestFindRepoRoot(t *testing.T) {
	t.Run("dir", func(t *testing.T) {
		root := t.TempDir()
		os.MkdirAll(filepath.Join(root, ".git"), 0o755)
		sub := filepath.Join(root, "a", "b")
		os.MkdirAll(sub, 0o755)
		got := findRepoRoot(sub)
		gotR, _ := filepath.EvalSymlinks(got)
		wantR, _ := filepath.EvalSymlinks(root)
		if gotR != wantR {
			t.Errorf("findRepoRoot = %q, want %q", got, root)
		}
	})
	t.Run("file", func(t *testing.T) {
		// .git as a file (worktree pattern) should also count.
		root := t.TempDir()
		if err := os.WriteFile(filepath.Join(root, ".git"), []byte("gitdir: ../foo\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		got := findRepoRoot(root)
		gotR, _ := filepath.EvalSymlinks(got)
		wantR, _ := filepath.EvalSymlinks(root)
		if gotR != wantR {
			t.Errorf("findRepoRoot (.git file) = %q, want %q", got, root)
		}
	})
	t.Run("none", func(t *testing.T) {
		root := t.TempDir()
		if got := findRepoRoot(root); got != "" {
			t.Errorf("findRepoRoot with no .git = %q, want empty", got)
		}
	})
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
	// Same fix as TestResolveAllowed: store the fully-resolved path
	// in the allowlist (LoadConfig does this for real env files).
	realResolved, err := filepath.EvalSymlinks(real)
	if err != nil {
		t.Fatal(err)
	}
	cfg := Config{
		Allowlist:     []string{realResolved},
		IdentityName:  "x",
		IdentityEmail: "x@y",
	}
	abs, e := cfg.ResolveAllowed(link)
	if e != nil {
		t.Fatalf("ResolveAllowed via symlink: %+v", e)
	}
	if abs != realResolved {
		t.Errorf("abs = %q, want %q (resolved through symlink)", abs, realResolved)
	}
}
