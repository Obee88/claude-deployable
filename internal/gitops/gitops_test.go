package gitops

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// testIdentity is the per-test identity we assert lands on every commit.
var testIdentity = Identity{Name: "claude-agent", Email: "claude-agent@users.noreply.github.com"}

// initRepo makes a fresh git repo in t.TempDir() with one initial commit
// on `main` and returns its path.  The repo's user.* config is
// deliberately *not* set — we want to confirm that GIT_AUTHOR_* /
// GIT_COMMITTER_* env vars are what gets recorded on the commit.
func initRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	mustGit(t, dir, "init", "-b", "main")
	// Identity for the bootstrap commit only — not used by anything we
	// test below.
	mustGit(t, dir, "-c", "user.name=bootstrap", "-c", "user.email=boot@x", "commit", "--allow-empty", "-m", "init")
	return dir
}

func mustGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s in %s: %v\n%s", strings.Join(args, " "), dir, err, out)
	}
}

func writeFile(t *testing.T, dir, rel, body string) {
	t.Helper()
	p := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestStatusClean(t *testing.T) {
	repo := initRepo(t)
	got, err := Status(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != StateClean {
		t.Errorf("state = %q, want %q", got.State, StateClean)
	}
	if got.Branch != "main" {
		t.Errorf("branch = %q, want main", got.Branch)
	}
	if len(got.DirtyFiles) != 0 {
		t.Errorf("dirty_files = %v, want []", got.DirtyFiles)
	}
	if got.HeadSHA == "" {
		t.Error("head_sha is empty on a non-empty repo")
	}
}

func TestStatusDirty(t *testing.T) {
	repo := initRepo(t)
	writeFile(t, repo, "untracked.txt", "x")
	writeFile(t, repo, "tracked.txt", "y")
	mustGit(t, repo, "add", "tracked.txt")
	mustGit(t, repo, "-c", "user.name=x", "-c", "user.email=x@x", "commit", "-m", "track")
	writeFile(t, repo, "tracked.txt", "y-modified")

	got, err := Status(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != StateDirty {
		t.Errorf("state = %q, want %q", got.State, StateDirty)
	}
	gotSet := map[string]bool{}
	for _, f := range got.DirtyFiles {
		gotSet[f] = true
	}
	for _, want := range []string{"untracked.txt", "tracked.txt"} {
		if !gotSet[want] {
			t.Errorf("dirty_files missing %q (got %v)", want, got.DirtyFiles)
		}
	}
}

func TestStatusMerging(t *testing.T) {
	repo := initRepo(t)
	// Create two divergent branches with conflicting edits, then attempt
	// to merge — the merge will fail and the repo enters MERGING state.
	writeFile(t, repo, "f.txt", "base\n")
	mustGit(t, repo, "add", "f.txt")
	mustGit(t, repo, "-c", "user.name=x", "-c", "user.email=x@x", "commit", "-m", "base")
	mustGit(t, repo, "checkout", "-b", "feature")
	writeFile(t, repo, "f.txt", "feature\n")
	mustGit(t, repo, "-c", "user.name=x", "-c", "user.email=x@x", "commit", "-am", "feature")
	mustGit(t, repo, "checkout", "main")
	writeFile(t, repo, "f.txt", "main\n")
	mustGit(t, repo, "-c", "user.name=x", "-c", "user.email=x@x", "commit", "-am", "main")
	// Conflicting merge — non-zero exit is expected.
	exec.Command("git", "-C", repo, "-c", "user.name=x", "-c", "user.email=x@x", "merge", "feature").Run()

	got, err := Status(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != StateMerging {
		t.Errorf("state = %q, want %q", got.State, StateMerging)
	}
}

func TestCommitUsesConfiguredIdentity(t *testing.T) {
	repo := initRepo(t)
	writeFile(t, repo, "a.txt", "hello\n")

	res, err := Commit(context.Background(), repo, testIdentity, "add a.txt", nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.SHA == "" {
		t.Fatal("commit sha is empty")
	}

	out, err := exec.Command("git", "-C", repo, "show", "-s", "--format=%an <%ae>|%cn <%ce>", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	want := testIdentity.String() + "|" + testIdentity.String()
	if got := strings.TrimSpace(string(out)); got != want {
		t.Errorf("identity = %q, want %q", got, want)
	}
}

func TestCommitRejectsEmptyIdentity(t *testing.T) {
	repo := initRepo(t)
	writeFile(t, repo, "a.txt", "hello\n")
	if _, err := Commit(context.Background(), repo, Identity{}, "msg", nil); err == nil {
		t.Fatal("expected error on empty identity, got nil")
	}
}

func TestCommitRejectsEmptyMessage(t *testing.T) {
	repo := initRepo(t)
	writeFile(t, repo, "a.txt", "hello\n")
	if _, err := Commit(context.Background(), repo, testIdentity, "   ", nil); err == nil {
		t.Fatal("expected error on empty message, got nil")
	}
}

func TestCommitSpecificFilesOnly(t *testing.T) {
	repo := initRepo(t)
	writeFile(t, repo, "a.txt", "a")
	writeFile(t, repo, "b.txt", "b")

	if _, err := Commit(context.Background(), repo, testIdentity, "add a only", []string{"a.txt"}); err != nil {
		t.Fatal(err)
	}
	got, err := Status(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != StateDirty {
		t.Fatalf("state = %q, want dirty (b.txt should still be untracked)", got.State)
	}
	if !contains(got.DirtyFiles, "b.txt") {
		t.Errorf("expected b.txt still dirty; got %v", got.DirtyFiles)
	}
}

func TestBranchCreatesAndChecksOut(t *testing.T) {
	repo := initRepo(t)
	got, err := Branch(context.Background(), repo, "feature/x", "")
	if err != nil {
		t.Fatal(err)
	}
	if got.Branch != "feature/x" || got.SHA == "" {
		t.Errorf("Branch = %+v", got)
	}
	st, err := Status(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	if st.Branch != "feature/x" {
		t.Errorf("not on new branch after Branch(): %s", st.Branch)
	}
}

func TestResetHardRevertsHEAD(t *testing.T) {
	repo := initRepo(t)
	writeFile(t, repo, "a.txt", "1")
	if _, err := Commit(context.Background(), repo, testIdentity, "c1", nil); err != nil {
		t.Fatal(err)
	}
	writeFile(t, repo, "a.txt", "2")
	if _, err := Commit(context.Background(), repo, testIdentity, "c2", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := Reset(context.Background(), repo, "hard", "HEAD~1"); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(repo, "a.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "1" {
		t.Errorf("a.txt = %q after reset --hard HEAD~1, want %q", body, "1")
	}
}

func TestResetRejectsBadMode(t *testing.T) {
	repo := initRepo(t)
	if _, err := Reset(context.Background(), repo, "yolo", "HEAD"); err == nil {
		t.Error("expected error on bogus reset mode")
	}
}

func TestAbortMerge(t *testing.T) {
	repo := initRepo(t)
	writeFile(t, repo, "f.txt", "base\n")
	mustGit(t, repo, "add", "f.txt")
	mustGit(t, repo, "-c", "user.name=x", "-c", "user.email=x@x", "commit", "-m", "base")
	mustGit(t, repo, "checkout", "-b", "feature")
	writeFile(t, repo, "f.txt", "feature\n")
	mustGit(t, repo, "-c", "user.name=x", "-c", "user.email=x@x", "commit", "-am", "feature")
	mustGit(t, repo, "checkout", "main")
	writeFile(t, repo, "f.txt", "main\n")
	mustGit(t, repo, "-c", "user.name=x", "-c", "user.email=x@x", "commit", "-am", "main")
	exec.Command("git", "-C", repo, "-c", "user.name=x", "-c", "user.email=x@x", "merge", "feature").Run()

	res, err := Abort(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	if res.StateBefore != StateMerging {
		t.Errorf("StateBefore = %q, want merging", res.StateBefore)
	}
	if res.StateAfter != StateClean {
		t.Errorf("StateAfter = %q, want clean", res.StateAfter)
	}
}

func TestAbortNothingToAbort(t *testing.T) {
	repo := initRepo(t)
	if _, err := Abort(context.Background(), repo); err == nil {
		t.Error("expected error when calling Abort on a clean repo")
	}
}

func TestPushPullAgainstLocalBare(t *testing.T) {
	bare := t.TempDir()
	mustGit(t, bare, "init", "--bare", "-b", "main")

	repo := initRepo(t)
	mustGit(t, repo, "remote", "add", "origin", bare)
	mustGit(t, repo, "push", "-u", "origin", "main")

	// New commit, pushed via gitops.Push.
	writeFile(t, repo, "a.txt", "hello")
	if _, err := Commit(context.Background(), repo, testIdentity, "add a", nil); err != nil {
		t.Fatal(err)
	}
	pushed, err := Push(context.Background(), repo, "main", "" /* no PAT */, false)
	if err != nil {
		t.Fatal(err)
	}
	if pushed.PushedSHA == "" {
		t.Fatal("pushed sha is empty")
	}

	// Independent clone of the bare repo to verify pull semantics.
	clone := t.TempDir()
	mustGit(t, ".", "clone", bare, clone)
	st, err := Status(context.Background(), clone)
	if err != nil {
		t.Fatal(err)
	}
	if st.HeadSHA != pushed.PushedSHA {
		t.Errorf("clone head = %q, pushed = %q", st.HeadSHA, pushed.PushedSHA)
	}

	// And exercise Pull from a third clone after another push.
	other := t.TempDir()
	mustGit(t, ".", "clone", bare, other)
	writeFile(t, repo, "b.txt", "more")
	if _, err := Commit(context.Background(), repo, testIdentity, "add b", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := Push(context.Background(), repo, "main", "", false); err != nil {
		t.Fatal(err)
	}
	pulled, err := Pull(context.Background(), other, "main")
	if err != nil {
		t.Fatal(err)
	}
	if pulled.UpdatedToSHA == "" {
		t.Error("pull returned empty sha")
	}
}

func TestPushDoesNotPersistPATToConfig(t *testing.T) {
	bare := t.TempDir()
	mustGit(t, bare, "init", "--bare", "-b", "main")

	repo := initRepo(t)
	// origin URL is the bare repo (file:// is fine; PAT is only injected
	// for https schemes, so this push won't actually rewrite the URL —
	// but we still want to confirm config never gains a PAT for any
	// reason).
	mustGit(t, repo, "remote", "add", "origin", bare)
	mustGit(t, repo, "push", "-u", "origin", "main")

	writeFile(t, repo, "x.txt", "x")
	if _, err := Commit(context.Background(), repo, testIdentity, "x", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := Push(context.Background(), repo, "main", "ghp_thisShouldNeverLandInConfig", false); err != nil {
		t.Fatal(err)
	}
	cfg, err := os.ReadFile(filepath.Join(repo, ".git", "config"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(cfg), "ghp_") {
		t.Errorf(".git/config contains a PAT-shaped string after Push — must never persist:\n%s", cfg)
	}
}

func TestInjectPAT(t *testing.T) {
	got, err := injectPAT("https://github.com/owner/repo.git", "ghp_X")
	if err != nil {
		t.Fatal(err)
	}
	if got != "https://oauth2:ghp_X@github.com/owner/repo.git" {
		t.Errorf("injectPAT https = %q", got)
	}
	// Non-https → unchanged.
	got, err = injectPAT("git@github.com:owner/repo.git", "ghp_X")
	if err != nil {
		t.Fatal(err)
	}
	if got != "git@github.com:owner/repo.git" {
		t.Errorf("injectPAT non-https rewrote URL: %q", got)
	}
}

func TestStatusAheadBehind(t *testing.T) {
	bare := t.TempDir()
	mustGit(t, bare, "init", "--bare", "-b", "main")

	repo := initRepo(t)
	mustGit(t, repo, "remote", "add", "origin", bare)
	mustGit(t, repo, "push", "-u", "origin", "main")

	writeFile(t, repo, "x.txt", "x")
	if _, err := Commit(context.Background(), repo, testIdentity, "x", nil); err != nil {
		t.Fatal(err)
	}

	st, err := Status(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	if st.Ahead != 1 {
		t.Errorf("ahead = %d, want 1", st.Ahead)
	}
	if st.Behind != 0 {
		t.Errorf("behind = %d, want 0", st.Behind)
	}
}

// Sanity: contextual cancellation propagates through runGit so a stuck
// git invocation doesn't pin a goroutine forever.
func TestContextCancelKillsGit(t *testing.T) {
	repo := initRepo(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	// `git wait-for-it` doesn't exist; we just check that *some* git
	// command obeys ctx — Status calls multiple git invocations and
	// cancelling before the first should cause it to fail rather than
	// hang.  This is mostly a smoke test against accidental sync.WaitGroup
	// or io.Copy bugs in runGit.
	cancel() // pre-cancel
	if _, err := Status(ctx, repo); err == nil {
		t.Error("expected error from canceled ctx; got nil")
	}
}

func contains(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}
